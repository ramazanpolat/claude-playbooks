package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/launcher"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
	"github.com/ramazanpolat/claude-playbooks/internal/shell"
)

var (
	renameAlias   string
	renameNoAlias bool
)

var renameCmd = &cobra.Command{
	Use:               "rename <old-name> <new-name>",
	Short:             "Rename a top-level playbook",
	Args:              cobra.ExactArgs(2),
	ValidArgsFunction: autocompletePlaybookNames,
	RunE:              runRename,
}

func init() {
	renameCmd.Flags().StringVar(&renameAlias, "alias", "", "custom alias for renamed playbook")
	renameCmd.Flags().BoolVar(&renameNoAlias, "no-alias", false, "drop the alias if one existed")
}

func runRename(cmd *cobra.Command, args []string) error {
	if renameNoAlias && renameAlias != "" {
		return fmt.Errorf("--no-alias and --alias cannot be used together")
	}
	oldName := args[0]
	newName := args[1]

	if strings.Contains(oldName, "/") {
		return fmt.Errorf("playbook name cannot contain '/'")
	}
	if strings.Contains(newName, "/") {
		return fmt.Errorf("playbook name cannot contain '/'")
	}
	if err := validateTopLevelName("new name", newName); err != nil {
		return err
	}

	shellConfig, err := config.ResolveShellConfig()
	if err != nil {
		return err
	}
	playbooksDir := config.ResolvePlaybooksDir()

	pb, err := playbook.Require(playbooksDir, shellConfig, oldName)
	if err != nil {
		return err
	}

	oldRoot := pb.RootPath
	if oldRoot == "" {
		oldRoot = pb.Path
	}
	oldManifestAlias := ""
	if pb.Manifest != nil {
		oldManifestAlias = pb.Manifest.Alias
	}
	oldConfigPath := pb.Path
	newPath := filepath.Join(playbooksDir, newName)
	newConfigPath := newPath
	if rel, err := filepath.Rel(oldRoot, oldConfigPath); err == nil && rel != "." {
		newConfigPath = filepath.Join(newPath, rel)
	}

	if _, err := os.Stat(newPath); err == nil {
		return fmt.Errorf("%q already exists at %s", newName, newPath)
	}

	// Preflight command-name collisions BEFORE any mutation (the directory
	// rename and the alias rewrites): failing afterwards would leave A
	// renamed to C with stale launcher state. Registry ownership applies to
	// every name that will address this playbook; the foreign-file check
	// applies only to the launcher name that will actually be written.
	for _, cand := range []string{renameAlias, newName} {
		if cand == "" {
			continue
		}
		if owner, oerr := commandNameOwner(cand, oldName); oerr != nil {
			return fmt.Errorf("cannot verify command name %q: %w", cand, oerr)
		} else if owner != nil {
			return fmt.Errorf("command name %q already addresses playbook %q", cand, owner.Name)
		}
	}
	if !renameNoAlias && launcherOpsAllowed() {
		writeName := renameAlias
		if writeName == "" {
			writeName = newName
		}
		if ldir, lerr := config.ResolveLauncherDir(); lerr == nil {
			if _, _, foreign := launcher.Lookup(ldir, writeName); foreign {
				return fmt.Errorf("command name %q is taken by a file claude-playbook did not generate", writeName)
			}
		}
	}

	// Persist a requested alias change BEFORE the directory rename and the
	// shell-alias rewrites: failing afterwards would leave the playbook
	// renamed with shell state changed, and a retry against the old name
	// reporting an unknown playbook. The manifest sits at the pre-rename
	// location (through the symlink for linked playbooks).
	if renameAlias != "" || renameNoAlias {
		oldManifestDir := oldRoot
		if info, lerr := os.Lstat(oldRoot); lerr == nil && info.Mode()&os.ModeSymlink != 0 {
			if resolved, rerr := filepath.EvalSymlinks(oldRoot); rerr == nil {
				oldManifestDir = resolved
			}
		}
		m, merr := manifest.Read(oldManifestDir)
		if merr != nil {
			return fmt.Errorf("cannot update alias: reading manifest failed: %w", merr)
		}
		if m == nil {
			m = &manifest.Manifest{Version: "0.1.0"}
		}
		switch {
		case renameNoAlias && m.Alias != "":
			m.Alias = ""
		case renameAlias != "" && m.Alias != renameAlias:
			m.Alias = renameAlias
		default:
			m = nil // nothing to persist
		}
		if m != nil {
			if werr := manifest.Write(oldManifestDir, m); werr != nil {
				return fmt.Errorf("cannot persist alias change (nothing renamed): %w", werr)
			}
		}
	}

	if err := os.Rename(oldRoot, newPath); err != nil {
		return fmt.Errorf("failed to rename: %w", err)
	}

	changed, skippedAliases, err := shell.RewritePathPrefix(shellConfig, oldRoot, newPath)
	if err != nil {
		return fmt.Errorf("failed to update aliases: %w", err)
	}

	switch {
	case renameNoAlias:
		if pb.HasAlias() {
			// Skipped (hand-edited) lines still carry the OLD path, so cleaning
			// only newPath would leave them behind despite an explicit flag.
			if err := removeAliasesForBothPaths(shellConfig, oldRoot, newPath); err != nil {
				return fmt.Errorf("failed to drop alias: %w", err)
			}
			skippedAliases = nil
		}
	case renameAlias != "":
		if err := removeAliasesForBothPaths(shellConfig, oldRoot, newPath); err != nil {
			return fmt.Errorf("failed to update alias: %w", err)
		}
		skippedAliases = nil
		if err := shell.Write(shellConfig, renameAlias, newConfigPath); err != nil {
			return fmt.Errorf("failed to write alias: %w", err)
		}
	}

	// Alias changes were persisted before the rename; here only the name is
	// kept in sync with the directory, for local playbooks (a linked
	// playbook's name belongs to the external tree). Failures are warnings:
	// dispatch resolves by directory name, not the manifest name.
	if info, err := os.Lstat(newPath); err == nil && info.Mode()&os.ModeSymlink == 0 {
		if m, err := manifest.Read(newPath); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not read manifest to update its name: %v\n", err)
		} else if m != nil && m.Name != newName {
			m.Name = newName
			if err := manifest.Write(newPath, m); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not update manifest name: %v\n", err)
			}
		}
	}

	// Launchers carry no state — a link named by the manifest alias keeps
	// working untouched. Only the link named after the OLD directory name
	// goes stale (that name no longer resolves), so retire it and register
	// the new name; --alias sets a fresh manifest alias, --no-alias drops
	// launcher registration.
	if ldir, err := config.ResolveLauncherDir(); err == nil && !launcherOpsAllowed() {
		_ = ldir
		fmt.Fprintf(os.Stderr, "Note: launchers are managed only for the default playbooks root; none changed.\n")
	} else if err == nil {
		if removed, rerr := launcher.Remove(ldir, oldName); rerr != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not remove launcher %q: %v\n", oldName, rerr)
		} else if removed {
			fmt.Printf("Removed command %q\n", oldName)
		}
		switch {
		case renameNoAlias:
			// An alias-named link resolves through the manifest, which is
			// about to be cleared — retire it too, or --no-alias leaves a
			// working command behind.
			if oldManifestAlias != "" {
				if removed, rerr := launcher.Remove(ldir, oldManifestAlias); rerr != nil {
					fmt.Fprintf(os.Stderr, "Warning: could not remove launcher %q: %v\n", oldManifestAlias, rerr)
				} else if removed {
					fmt.Printf("Removed command %q\n", oldManifestAlias)
				}
			}
		case renameAlias != "":
			// The previous alias no longer resolves through the registry
			// once the manifest changes — retire its link too.
			if oldManifestAlias != "" && oldManifestAlias != renameAlias {
				if removed, rerr := launcher.Remove(ldir, oldManifestAlias); rerr != nil {
					fmt.Fprintf(os.Stderr, "Warning: could not remove launcher %q: %v\n", oldManifestAlias, rerr)
				} else if removed {
					fmt.Printf("Removed command %q\n", oldManifestAlias)
				}
			}
			if _, werr := launcher.Write(ldir, renameAlias); werr != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not write launcher %q: %v\n", renameAlias, werr)
			} else {
				fmt.Printf("Command %q now runs %q\n", renameAlias, newName)
			}
		default:
			if _, werr := launcher.Write(ldir, newName); werr != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not write launcher %q: %v\n", newName, werr)
			} else {
				fmt.Printf("Command %q now runs %q\n", newName, newName)
			}
		}
	}

	fmt.Printf("Renamed %q → %q\n", oldName, newName)
	if changed > 0 {
		fmt.Printf("Updated %d alias line%s in %s\n", changed, pluralS(changed), shellConfig)
	}
	// A hand-edited alias is left exactly as written rather than guessed at:
	// editing shell-encoded text in place is what makes silent corruption
	// possible. Say so, and name the one command that fixes it — otherwise the
	// alias quietly keeps pointing at the old playbook.
	for _, aliasName := range skippedAliases {
		// Every value is shell-quoted: a playbook name may legally contain spaces
		// and shell metacharacters, so an unquoted suggestion is unrunnable as
		// printed and would execute the extra text if pasted.
		fmt.Fprintf(os.Stderr,
			"Warning: alias %q in %s was hand-edited, so it was left unchanged and still refers to %q.\n"+
				"         Regenerate it with: %s%s alias %s %s\n",
			aliasName, shellConfig, oldName,
			shell.QuoteArg(filepath.Base(os.Args[0])),
			configOverrideArgs(),
			shell.QuoteArg(newName),
			shell.QuoteArg(aliasName))
	}
	return nil
}

// removeAliasesForBothPaths drops alias lines for a renamed playbook under
// either its new or its old path. Lines this package regenerated moved to the
// new path; lines it deliberately left alone (hand-edited) still name the old
// one, and an explicit --no-alias/--alias must reach both.
func removeAliasesForBothPaths(shellConfig, oldRoot, newPath string) error {
	for _, p := range []string{newPath, oldRoot} {
		if _, err := shell.RemoveByPathPrefix(shellConfig, p); err != nil {
			return err
		}
	}
	return nil
}

// configOverrideArgs renders the --playbooks-dir / --shell-config overrides in
// effect, so a suggested command reproduces this invocation's configuration.
// Without them, a suggestion made under `--playbooks-dir ./pb` would search the
// default directory and fail, or write the alias into the wrong rc file.
func configOverrideArgs() string {
	var b strings.Builder
	if config.PlaybooksDir != "" {
		b.WriteString(" --playbooks-dir " + shell.QuoteArg(config.PlaybooksDir))
	}
	if config.ShellConfig != "" {
		b.WriteString(" --shell-config " + shell.QuoteArg(config.ShellConfig))
	}
	return b.String()
}
