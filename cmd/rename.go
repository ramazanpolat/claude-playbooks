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
	oldConfigPath := pb.Path
	newPath := filepath.Join(playbooksDir, newName)
	newConfigPath := newPath
	if rel, err := filepath.Rel(oldRoot, oldConfigPath); err == nil && rel != "." {
		newConfigPath = filepath.Join(newPath, rel)
	}

	if _, err := os.Stat(newPath); err == nil {
		return fmt.Errorf("%q already exists at %s", newName, newPath)
	}

	// Preflight the --alias launcher collision BEFORE any mutation (the
	// directory rename and the alias rewrites): failing afterwards would
	// leave A renamed to C with its old launcher broken, and an installed
	// alias running a different playbook than the PATH command.
	if renameAlias != "" {
		if ldir, lerr := config.ResolveLauncherDir(); lerr == nil {
			if e, exists, foreign := launcher.Lookup(ldir, renameAlias); foreign {
				return fmt.Errorf("command name %q is taken by a file claude-playbook did not generate", renameAlias)
			} else if exists && e.ConfigDir != newConfigPath &&
				!launcher.Under(e.ConfigDir, oldRoot) && !launcher.Under(e.ConfigDir, newPath) {
				return fmt.Errorf("command name %q belongs to playbook %q (%s)", renameAlias, e.PlaybookName, e.ConfigDir)
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

	// Keep the manifest's name in sync with the directory name. Skip linked
	// playbooks: their manifest belongs to the external source tree.
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

	// Launchers referencing the old path are dead either way; regenerate
	// them against the new location (preserving custom command names) unless
	// --no-alias drops them or --alias replaces them with a single new name.
	if ldir, err := config.ResolveLauncherDir(); err == nil {
		stale, lerr := launcher.RemoveForPathPrefix(ldir, oldRoot)
		if lerr != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not update launchers: %v\n", lerr)
		}
		switch {
		case renameNoAlias:
			// dropped
		case renameAlias != "":
			if _, werr := launcher.Write(ldir, renameAlias, newName, newConfigPath, playbooksDir); werr != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not write launcher %q: %v\n", renameAlias, werr)
			} else {
				fmt.Printf("Command %q now points at %q\n", renameAlias, newName)
			}
		default:
			for _, e := range stale {
				if _, werr := launcher.Write(ldir, e.CmdName, newName, newConfigPath, playbooksDir); werr != nil {
					fmt.Fprintf(os.Stderr, "Warning: could not update launcher %q: %v\n", e.CmdName, werr)
				} else {
					fmt.Printf("Command %q now points at %q\n", e.CmdName, newName)
				}
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
