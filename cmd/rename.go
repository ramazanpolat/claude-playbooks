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
	renameCmd.Flags().StringVar(&renameAlias, "alias", "", "new launcher command name for the renamed playbook")
	renameCmd.Flags().BoolVar(&renameNoAlias, "no-alias", false, "drop the launcher command and manifest alias")
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

	playbooksDir := config.ResolvePlaybooksDir()

	// Lock BEFORE discovery: rename has no interactive prompt, so the lock
	// spans discovery through mutation cheaply — everything derived from pb
	// (paths, manifest alias) stays fresh against concurrent delete/create
	// cycles of the same name (see lockRegistry).
	unlock, err := lockRegistry()
	if err != nil {
		return err
	}
	defer unlock()

	pb, err := playbook.Require(playbooksDir, oldName)
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
	newPath := filepath.Join(playbooksDir, newName)

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
	// An explicitly requested alias that can never name a launcher
	// (reserved, path separators) must fail before any mutation — the late
	// launcher.Write warning would leave a renamed playbook without its
	// requested command.
	if renameAlias != "" {
		if err := launcher.ValidateName(renameAlias); err != nil {
			return err
		}
	}
	if !renameNoAlias {
		writeName := renameAlias
		if writeName == "" {
			writeName = newName
		}
		// Rename promises a replacement command; an unwritable default name
		// (reserved CLI name) must fail before mutation, with --no-alias as
		// the opt-out.
		if err := launcher.ValidateName(writeName); err != nil {
			return fmt.Errorf("%w (pass --no-alias to rename without a launcher)", err)
		}
		if launcherOpsAllowed() {
			if ldir, lerr := config.ResolveLauncherDir(); lerr == nil {
				if _, _, foreign := launcher.Lookup(ldir, writeName); foreign {
					return fmt.Errorf("command name %q is taken by a file claude-playbook did not generate", writeName)
				}
			}
		}
	}

	linkedOld := false
	if info, lerr := os.Lstat(oldRoot); lerr == nil && info.Mode()&os.ModeSymlink != 0 {
		linkedOld = true
	}
	// A linked playbook's manifest is SHARED state — other registry roots
	// may resolve their launchers through it. Clearing an alias, changing
	// one, or ADDING one where none existed can break or reroute those
	// registrations (collisions are only preflighted in the selected
	// root), so every differing alias mutation is refused.
	if linkedOld {
		if renameNoAlias && oldManifestAlias != "" {
			return fmt.Errorf("cannot clear alias %q: the linked target's manifest is shared with other registrations. Edit the target's %s directly if you really mean it", oldManifestAlias, manifest.FileName)
		}
		if renameAlias != "" && renameAlias != oldManifestAlias {
			return fmt.Errorf("cannot set alias %q on a linked target's shared %s (current alias %q). Edit the target's manifest directly if you really mean it", renameAlias, manifest.FileName, oldManifestAlias)
		}
	}
	// A manifest alias that merely mirrors the old directory name (link's
	// default) follows the rename for local playbooks: leaving it would
	// keep the old name registered while its launcher goes away.
	aliasFollows := !linkedOld && renameAlias == "" && !renameNoAlias && oldManifestAlias == oldName

	// Persist a requested alias change BEFORE the directory rename and the
	// shell-alias rewrites: failing afterwards would leave the playbook
	// renamed with shell state changed, and a retry against the old name
	// reporting an unknown playbook. The manifest sits at the pre-rename
	// location (through the symlink for linked playbooks).
	restoreManifest := func() {}
	if renameAlias != "" || renameNoAlias || aliasFollows {
		oldManifestDir := oldRoot
		if linkedOld {
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
		case aliasFollows:
			m.Alias = newName
		default:
			m = nil // nothing to persist
		}
		if m != nil {
			// Capture the pre-change manifest so a failed directory rename
			// can restore it — otherwise the alias moves while the rename
			// reports that nothing happened.
			manifestFile := filepath.Join(oldManifestDir, manifest.FileName)
			origBytes, rerr := os.ReadFile(manifestFile)
			origExisted := rerr == nil
			restore := func() {
				var rerr error
				if origExisted {
					rerr = os.WriteFile(manifestFile, origBytes, 0o644)
				} else {
					rerr = os.Remove(manifestFile)
				}
				if rerr != nil {
					fmt.Fprintf(os.Stderr, "Warning: could not restore manifest: %v\n", rerr)
				}
			}
			if werr := manifest.Write(oldManifestDir, m); werr != nil {
				// A failing write may have truncated or partially
				// overwritten the file in place — restore the captured
				// bytes so the error really does leave nothing changed.
				restore()
				return fmt.Errorf("cannot persist alias change (nothing renamed): %w", werr)
			}
			restoreManifest = restore
		}
	}

	if err := os.Rename(oldRoot, newPath); err != nil {
		restoreManifest()
		return fmt.Errorf("failed to rename: %w", err)
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
	// Gate before resolving: ResolveLauncherDir probes directory writability
	// by creating a temp file, which a custom-root invocation must not do.
	if !launcherOpsAllowed() {
		fmt.Fprintf(os.Stderr, "Note: launchers are managed only for the default playbooks root; none changed.\n")
	} else if ldir, err := config.ResolveLauncherDir(); err == nil {
		// Retirement is CLAIM-AWARE (same post-mutation re-resolution as
		// delete): a name still addressed after the rename — a linked
		// playbook whose shared alias is the old name, or another playbook
		// whose manifest alias equals it — keeps its launcher.
		removeUnclaimedLaunchers([]string{oldName})
		switch {
		case renameNoAlias:
			// An alias-named link resolves through the manifest, which was
			// cleared — retire it too (unless someone else claims it), or
			// --no-alias leaves a working command behind.
			if oldManifestAlias != "" {
				removeUnclaimedLaunchers([]string{oldManifestAlias})
			}
		case renameAlias != "":
			// The previous alias no longer resolves through this playbook
			// once the manifest changes — retire its link too, unless
			// another playbook claims it.
			if oldManifestAlias != "" && oldManifestAlias != renameAlias {
				removeUnclaimedLaunchers([]string{oldManifestAlias})
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
	return nil
}

// configOverrideArgs renders the --playbooks-dir override in effect, so a
// suggested command reproduces this invocation's configuration. Without it, a
// suggestion made under `--playbooks-dir ./pb` would search the default
// directory and fail.
func configOverrideArgs() string {
	var b strings.Builder
	if config.PlaybooksDir != "" {
		b.WriteString(" --playbooks-dir " + shell.QuoteArg(config.PlaybooksDir))
	}
	return b.String()
}
