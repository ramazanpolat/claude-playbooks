package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/launcher"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

var aliasRemove bool

var aliasCmd = &cobra.Command{
	Use:   "alias [name] [alias]",
	Short: "Show or manage playbook aliases",
	Long: `A playbook is addressed by its directory name and, optionally, one alias —
an alternate command name recorded in its .playbook manifest and
materialized as a launcher command.

With no arguments: list every playbook's alias.
With one argument: show the alias for that playbook (read-only).
With two arguments: set the alias (replaces any previous one, and its
launcher).
With --remove: remove the playbook's alias and its launcher.`,
	Args:              cobra.RangeArgs(0, 2),
	ValidArgsFunction: autocompletePlaybookNames,
	RunE:              runAlias,
}

func init() {
	aliasCmd.Flags().BoolVar(&aliasRemove, "remove", false, "remove the alias for the named playbook")
}

func runAlias(cmd *cobra.Command, args []string) error {
	playbooksDir := config.ResolvePlaybooksDir()

	// No args — list all aliases.
	if len(args) == 0 {
		if aliasRemove {
			return fmt.Errorf("--remove requires a playbook name")
		}
		pbs, err := playbook.Discover(playbooksDir)
		if err != nil {
			return err
		}
		if len(pbs) == 0 {
			fmt.Println("No playbooks found.")
			return nil
		}
		maxLen := 0
		for _, pb := range pbs {
			if len(pb.Name) > maxLen {
				maxLen = len(pb.Name)
			}
		}
		for _, pb := range pbs {
			if a := pb.Alias(); a != "" {
				fmt.Printf("%-*s  %s\n", maxLen, pb.Name, a)
			} else {
				fmt.Printf("%-*s  (no alias)\n", maxLen, pb.Name)
			}
		}
		return nil
	}

	name := args[0]

	// Mutations (set, --remove) serialize the WHOLE read-modify-write under
	// the registry lock: snapshotting the current alias before locking lets
	// two overlapping replacements read the same "old" value, so the loser
	// never retires the winner's launcher — and an unlocked removal could
	// delete a launcher another process just legitimately claimed.
	if aliasRemove || len(args) == 2 {
		unlock, lerr := lockRegistry()
		if lerr != nil {
			return lerr
		}
		defer unlock()
	}

	pb, err := playbook.Require(playbooksDir, name)
	if err != nil {
		return err
	}

	// A linked playbook's manifest is SHARED state — other registry roots
	// may resolve their launchers through it — so alias mutations are
	// refused, exactly as in rename.
	linked := false
	if info, lerr := os.Lstat(pb.RootPath); lerr == nil && info.Mode()&os.ModeSymlink != 0 {
		linked = true
	}

	// --remove
	if aliasRemove {
		old := pb.Alias()
		if old == "" {
			fmt.Printf("Playbook %q has no alias set.\n", name)
			return nil
		}
		if linked {
			return fmt.Errorf("cannot clear alias %q: the linked target's manifest is shared with other registrations. Edit the target's %s directly if you really mean it", old, manifest.FileName)
		}
		manifestFile := filepath.Join(pb.RootPath, manifest.FileName)
		origBytes, rerr := os.ReadFile(manifestFile)
		if rerr != nil {
			return fmt.Errorf("cannot read manifest: %w", rerr)
		}
		pb.Manifest.Alias = ""
		if err := manifest.Write(pb.RootPath, pb.Manifest); err != nil {
			if werr := os.WriteFile(manifestFile, origBytes, 0o644); werr != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not restore manifest: %v\n", werr)
			}
			return err
		}
		if err := retireAliasLauncher(old, name); err != nil {
			// The launcher is still there; a cleared manifest would make it
			// stale while this command reports success — restore it.
			if werr := os.WriteFile(manifestFile, origBytes, 0o644); werr != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not restore manifest: %v\n", werr)
			}
			return fmt.Errorf("could not retire launcher %q (alias unchanged): %w", old, err)
		}
		fmt.Printf("Removed alias %q from playbook %q\n", old, name)
		return nil
	}

	// Two args — set the alias.
	if len(args) == 2 {
		newAlias := args[1]
		old := pb.Alias()
		if linked && newAlias != old {
			return fmt.Errorf("cannot set alias %q on a linked target's shared %s (current alias %q). Edit the target's manifest directly if you really mean it", newAlias, manifest.FileName, old)
		}
		if newAlias == pb.Name {
			return fmt.Errorf("alias %q is already the playbook's name; nothing to add", newAlias)
		}
		if err := launcher.ValidateName(newAlias); err != nil {
			return err
		}
		if newAlias == old {
			// Nothing to record — and for a linked playbook, rewriting the
			// shared target manifest would drop comments and unknown fields
			// for no reason. Just make sure the launcher exists, and FAIL
			// when it cannot be written: "repair my command" that leaves no
			// command must not exit 0. Ownership is rechecked first — a
			// hand-edited manifest elsewhere may have claimed this alias,
			// and dispatch would resolve the launcher to THAT playbook.
			if err := preflightCommandNames(pb.Name, newAlias); err != nil {
				return err
			}
			if !launcherOpsAllowed() {
				fmt.Fprintf(os.Stderr, "Note: launchers are managed only for the default playbooks root; alias %q is recorded in the manifest only.\n", newAlias)
				return nil
			}
			ldir, derr := config.ResolveLauncherDir()
			if derr != nil {
				return fmt.Errorf("no launcher written: %w", derr)
			}
			lpath, werr := launcher.Write(ldir, newAlias)
			if werr != nil {
				return fmt.Errorf("could not write launcher %q: %w", newAlias, werr)
			}
			fmt.Printf("Command:  %s  (launcher at %s)\n", newAlias, lpath)
			warnIfShadowedOrUnreachable(newAlias, lpath, pb.Path)
			return nil
		}
		if err := preflightCommandNames(pb.Name, newAlias); err != nil {
			return err
		}
		// A foreign file squatting on the name must fail BEFORE the manifest
		// changes and the old launcher is retired — installLauncher's late
		// warning would otherwise leave the playbook with neither its old
		// command nor a working new one, and exit 0. Same preflight as
		// rename.
		if launcherOpsAllowed() {
			if ldir, lerr := config.ResolveLauncherDir(); lerr == nil {
				if _, _, foreign := launcher.Lookup(ldir, newAlias); foreign {
					return fmt.Errorf("command name %q is taken by a file claude-playbook did not generate", newAlias)
				}
			}
		}
		m := pb.Manifest
		if m == nil {
			// Flat playbook: the alias needs a manifest to be registered in,
			// same bootstrap as create --alias.
			m = &manifest.Manifest{Version: "0.1.0", Name: pb.Name}
		}
		// Capture the pre-change manifest so a failed launcher write can
		// restore it byte-for-byte (or remove a manifest this command
		// bootstrapped) — the alias operation's entire point is the
		// command, so a launcher failure must leave NO state changed.
		manifestFile := filepath.Join(pb.RootPath, manifest.FileName)
		origBytes, rerr := os.ReadFile(manifestFile)
		origExisted := rerr == nil
		restoreManifest := func() {
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
		m.Alias = newAlias
		if err := manifest.Write(pb.RootPath, m); err != nil {
			restoreManifest()
			return fmt.Errorf("cannot record alias %q in manifest (required for the command to resolve): %w", newAlias, err)
		}
		if !launcherOpsAllowed() {
			fmt.Fprintf(os.Stderr, "Note: launchers are managed only for the default playbooks root; alias %q recorded in the manifest only.\n", newAlias)
			return nil
		}
		// Prove the NEW command exists before retiring the old one: the
		// reverse order can leave the playbook with neither command while
		// exiting 0 (launcher dir uncreatable, permissions, a post-preflight
		// race on the name).
		ldir, derr := config.ResolveLauncherDir()
		if derr != nil {
			restoreManifest()
			return fmt.Errorf("no launcher written (alias unchanged): %w", derr)
		}
		// Whether a launcher already existed under the new name decides what
		// rollback restores: launcher.Write refreshes an unclaimed link of
		// ours in place, and deleting it on rollback would destroy a link
		// that predates this command.
		_, newExisted, _ := launcher.Lookup(ldir, newAlias)
		lpath, werr := launcher.Write(ldir, newAlias)
		if werr != nil {
			restoreManifest()
			return fmt.Errorf("could not write launcher %q (alias unchanged): %w", newAlias, werr)
		}
		if old != "" {
			if err := retireAliasLauncher(old, name); err != nil {
				// Roll back what this command CREATED: the manifest change,
				// and the new launcher only if it did not exist before (a
				// pre-existing refreshed link still resolves to this binary
				// and stays).
				if !newExisted {
					if _, derr := launcher.Remove(ldir, newAlias); derr != nil {
						fmt.Fprintf(os.Stderr, "Warning: could not remove launcher %q during rollback: %v\n", newAlias, derr)
					}
				}
				restoreManifest()
				return fmt.Errorf("could not retire old launcher %q (alias unchanged): %w", old, err)
			}
		}
		fmt.Printf("Command:  %s  (launcher at %s)\n", newAlias, lpath)
		warnIfShadowedOrUnreachable(newAlias, lpath, pb.Path)
		return nil
	}

	// One arg — show (read-only).
	if a := pb.Alias(); a != "" {
		fmt.Printf("Alias for %q: %s\n", name, a)
		return nil
	}
	fmt.Printf("Playbook %q has no alias set.\n", name)
	fmt.Printf("Use 'claude-playbook alias %s <alias-name>' to set one.\n", name)
	return nil
}

// retireAliasLauncher removes the launcher for an alias the user explicitly
// unregistered. Unlike rename's implicit retirement (retention with a hint),
// an explicit `alias --remove`/replacement means "this command goes" — but
// still claim-aware: a name that now addresses another playbook keeps its
// launcher, and launcher.Remove only ever deletes a symlink resolving to
// this binary. Failures are returned, not swallowed: the caller changed the
// manifest and must be able to roll it back rather than exit 0 with a
// stale launcher behind.
func retireAliasLauncher(old, exceptName string) error {
	if !launcherOpsAllowed() {
		return nil
	}
	owner, err := commandNameOwner(old, exceptName)
	if err != nil {
		return fmt.Errorf("cannot verify ownership of %q: %w", old, err)
	}
	if owner != nil {
		fmt.Printf("Kept command %q (still addresses playbook %q)\n", old, owner.Name)
		return nil
	}
	dir, err := config.ResolveLauncherDir()
	if err != nil {
		return err
	}
	ok, err := launcher.Remove(dir, old)
	if err != nil {
		return err
	}
	if ok {
		fmt.Printf("Removed command %q\n", old)
	}
	return nil
}
