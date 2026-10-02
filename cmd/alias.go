package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/launcher"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

// launcherOpts carries ALTER PLAYBOOK … LAUNCHER / NO LAUNCHER. No state is shared
// between two calls.
type launcherOpts struct {
	remove bool
}

func doLauncher(o launcherOpts, args []string) error {
	playbooksDir := config.ResolvePlaybooksDir()
	name := args[0]

	// Both forms serialize the WHOLE read-modify-write under the registry
	// lock: snapshotting the current alias before locking lets two
	// overlapping replacements read the same "old" value, so the loser
	// never retires the winner's launcher — and an unlocked removal could
	// delete a launcher another process just legitimately claimed.
	unlock, lerr := lockRegistry()
	if lerr != nil {
		return lerr
	}
	defer unlock()

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

	// NO LAUNCHER
	if o.remove {
		old := pb.Alias()
		if old == "" {
			fmt.Printf("Playbook %q has no launcher.\n", name)
			return nil
		}
		if linked {
			return fmt.Errorf("cannot clear launcher %q: the linked target's manifest is shared with other registrations. Edit the target's %s directly if you really mean it", old, manifest.FileName)
		}
		manifestFile := filepath.Join(pb.RootPath, manifest.FileName)
		if _, rerr := os.Stat(manifestFile); rerr != nil {
			return fmt.Errorf("cannot read manifest: %w", rerr)
		}
		restore, cerr := captureManifestRestore(pb.RootPath)
		if cerr != nil {
			return cerr
		}
		pb.Manifest.Launcher = ""
		if err := manifest.Write(pb.RootPath, pb.Manifest); err != nil {
			restore()
			return err
		}
		if err := retireAliasLauncher(old, name); err != nil {
			// The launcher is still there; a cleared manifest would make it
			// stale while this command reports success — restore it.
			restore()
			return fmt.Errorf("could not retire launcher %q (launcher unchanged): %w", old, err)
		}
		fmt.Printf("Removed launcher %q from playbook %q\n", old, name)
		if !launcherOpsAllowed() {
			return nil
		}
		if ldir, lerr := config.ResolveLauncherDir(); lerr == nil {
			if _, _, foreign := launcher.Lookup(ldir, name); !foreign {
				if _, err := os.Lstat(filepath.Join(ldir, name)); err != nil {
					fmt.Printf("Playbook %q now has no launcher. Restore one with: cpb ALTER PLAYBOOK %s LAUNCHER %s\n", name, name, name)
				}
			}
		}
		return nil
	}

	// LAUNCHER <name>
	newAlias := args[1]
	old := pb.Alias()
	if newAlias == pb.Name {
		// "LAUNCHER <name>" with the playbook's own name is the repair spelling for the name
		// launcher: nothing to record in the manifest (the name is not
		// an alias), just make sure the command exists. Without this,
		// a playbook whose alias was removed — or that never had a
		// name launcher because its alias won at install time — has no
		// command and no way to get one back.
		//
		// Checked BEFORE the linked-playbook guard: this path never
		// touches the shared target manifest, and a linked registration
		// owns its local name launcher just like any other. Unlike the
		// equal-alias branch below, ownership IS rechecked — the name is
		// compared against every other playbook's aliases too, so a
		// pilot who re-homed the name as another playbook's alias gets
		// a refusal, not a launcher that dispatches to the wrong
		// install.
		owner, oerr := commandNameOwner(newAlias, pb.Name)
		if oerr != nil {
			return fmt.Errorf("cannot verify launcher name %q: %w", newAlias, oerr)
		}
		if owner != nil {
			return fmt.Errorf("launcher name %q already addresses playbook %q", newAlias, owner.Name)
		}
		if !launcherOpsAllowed() {
			fmt.Fprintf(os.Stderr, "Note: launchers are managed only for the default playbooks root; run manually with:\n  cpb --playbooks-dir %q run %q\n", config.ResolvePlaybooksDir(), newAlias)
			return nil
		}
		ldir, derr := config.ResolveLauncherDir()
		if derr != nil {
			return fmt.Errorf("no launcher written: %w", derr)
		}
		if _, _, foreign := launcher.Lookup(ldir, newAlias); foreign {
			return fmt.Errorf("launcher name %q is taken by a file cpb did not generate", newAlias)
		}
		lpath, werr := launcher.Write(ldir, newAlias)
		if werr != nil {
			return fmt.Errorf("could not write launcher %q: %w", newAlias, werr)
		}
		fmt.Printf("Launcher: %s  (at %s)\n", newAlias, lpath)
		warnIfShadowedOrUnreachable(newAlias, lpath, pb.Path)
		return nil
	}
	if linked && newAlias != old {
		return fmt.Errorf("cannot set launcher %q on a linked target's shared %s (current launcher %q). Edit the target's manifest directly if you really mean it", newAlias, manifest.FileName, old)
	}
	if err := launcher.ValidateName(newAlias); err != nil {
		return err
	}
	if newAlias == old {
		// Nothing to record — and for a linked playbook, rewriting the
		// shared target manifest would drop comments and unknown fields
		// for no reason. Just make sure the launcher exists, and FAIL
		// when it cannot be written: "repair my command" that leaves no
		// command must not exit 0. No ownership recheck here: the tool
		// refuses to CREATE colliding aliases, so a collision can only
		// exist through hand-edited manifests — the pilot's own state,
		// deliberately not defended against (dispatch stays
		// deterministic: names first, then sorted-order aliases).
		if !launcherOpsAllowed() {
			fmt.Fprintf(os.Stderr, "Note: launchers are managed only for the default playbooks root; launcher %q is recorded in the manifest only.\n", newAlias)
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
		fmt.Printf("Launcher: %s  (at %s)\n", newAlias, lpath)
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
				return fmt.Errorf("launcher name %q is taken by a file cpb did not generate", newAlias)
			}
		}
	}
	// Record the alias (bootstrapping a manifest for a flat playbook,
	// same as create --alias), wrapped in the pre-change snapshot so a
	// failed launcher write can restore it byte-for-byte (or remove a
	// manifest this command bootstrapped) — the alias operation's
	// entire point is the command, so a launcher failure must leave NO
	// state changed.
	restoreManifest, cerr := captureManifestRestore(pb.RootPath)
	if cerr != nil {
		return cerr
	}
	if err := writeAliasManifest(pb.RootPath, pb.Name, newAlias); err != nil {
		restoreManifest()
		return fmt.Errorf("cannot record launcher %q in manifest (required for the launcher to resolve): %w", newAlias, err)
	}
	if !launcherOpsAllowed() {
		fmt.Fprintf(os.Stderr, "Note: launchers are managed only for the default playbooks root; launcher %q recorded in the manifest only.\n", newAlias)
		return nil
	}
	// Prove the NEW command exists before retiring the old one: the
	// reverse order can leave the playbook with neither command while
	// exiting 0 (launcher dir uncreatable, permissions, a post-preflight
	// race on the name).
	ldir, derr := config.ResolveLauncherDir()
	if derr != nil {
		restoreManifest()
		return fmt.Errorf("no launcher written (launcher unchanged): %w", derr)
	}
	// Whether a launcher already existed under the new name decides what
	// rollback restores: launcher.Write refreshes an unclaimed link of
	// ours in place, and deleting it on rollback would destroy a link
	// that predates this command.
	_, newExisted, _ := launcher.Lookup(ldir, newAlias)
	lpath, werr := launcher.Write(ldir, newAlias)
	if werr != nil {
		restoreManifest()
		return fmt.Errorf("could not write launcher %q (launcher unchanged): %w", newAlias, werr)
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
			return fmt.Errorf("could not retire old launcher %q (launcher unchanged): %w", old, err)
		}
	}
	fmt.Printf("Launcher: %s  (at %s)\n", newAlias, lpath)
	warnIfShadowedOrUnreachable(newAlias, lpath, pb.Path)
	return nil
}

// retireAliasLauncher removes the launcher for an alias the pilot explicitly
// unregistered, under the same retirement rule delete and rename apply:
// claim-aware, so a name that now addresses another playbook keeps its
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
		fmt.Printf("Kept launcher %q (still addresses playbook %q)\n", old, owner.Name)
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
		fmt.Printf("Removed launcher %q\n", old)
	}
	return nil
}
