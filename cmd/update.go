package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

var updateCmd = &cobra.Command{
	Use:                "update [name]",
	Short:              "Self-update the tool, or update a playbook from its script or source",
	DisableFlagParsing: true,
	ValidArgsFunction:  autocompletePlaybookNames,
	RunE:               runUpdate,
}

func runUpdate(cmd *cobra.Command, args []string) error {
	rest, playbooksDir := scanPlaybooksDirArg(args)
	if playbooksDir != "" {
		config.PlaybooksDir = playbooksDir
	}
	// --force/--check are self-update flags; only honor them before a name
	// so a playbook's delegated update script still receives its own flags
	// (e.g. `update kommander --force` passes --force to kommander's script).
	var force, checkOnly bool
consume:
	for len(rest) > 0 {
		switch rest[0] {
		case "--force", "-f":
			force = true
			rest = rest[1:]
		case "--check":
			checkOnly = true
			rest = rest[1:]
		default:
			break consume
		}
	}
	// --help before the name prints usage (after it, it forwards).
	if len(rest) > 0 && (rest[0] == "--help" || rest[0] == "-h") {
		printUpdateHelp()
		return nil
	}

	if len(rest) == 0 {
		return runSelfUpdate(force, checkOnly)
	}

	name := rest[0]
	scriptArgs := rest[1:]
	return runPlaybookUpdate(name, scriptArgs)
}

func printUpdateHelp() {
	fmt.Println("Usage: claude-playbook update [name] [script-args...]")
	fmt.Println()
	fmt.Println("Without <name>: self-update the claude-playbook binary to the latest release.")
	fmt.Println("  --check    report the latest version without installing it")
	fmt.Println("  --force    reinstall even if already on the latest version")
	fmt.Println()
	fmt.Println("With <name>: run its delegated update script, or update natively from source metadata.")
}

func runPlaybookUpdate(name string, scriptArgs []string) error {
	playbooksDir := config.ResolvePlaybooksDir()

	pb, err := playbook.Require(playbooksDir, name)
	if err != nil {
		return err
	}

	scriptPath := "bin/update-playbook.sh"
	if pb.Manifest != nil && pb.Manifest.Source != nil && pb.Manifest.Source.UpdateScript != "" {
		scriptPath = pb.Manifest.Source.UpdateScript
	}
	script, scriptErr := manifest.ResolvePath(pb.Path, "source.update_script", scriptPath)
	if scriptErr == nil {
		info, err := os.Stat(script)
		if err != nil {
			return err
		}
		if info.IsDir() || info.Mode()&0111 == 0 {
			return fmt.Errorf("update script is not executable: %s", script)
		}

		c := exec.Command(script, scriptArgs...)
		c.Dir = pb.Path
		c.Env = append(os.Environ(),
			"CLAUDE_CONFIG_DIR="+pb.Path,
			"CLAUDE_PLAYBOOK_TARGET="+name,
			"CLAUDE_PLAYBOOK_PATH="+pb.Path,
		)
		c.Stdin = os.Stdin
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		if err := c.Run(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				fmt.Fprintf(os.Stderr, "update script exited with code %d\n", exitErr.ExitCode())
			}
			return preserveExitCode(err)
		}
		return nil
	}
	if !errors.Is(scriptErr, os.ErrNotExist) {
		return scriptErr
	}

	if pb.Manifest == nil || pb.Manifest.Source == nil || pb.Manifest.Source.Repository == "" {
		return fmt.Errorf("%q has no update script (%s) and no [source] metadata in .playbook", name, scriptPath)
	}
	if len(scriptArgs) > 0 {
		return fmt.Errorf("native update does not accept script arguments")
	}

	root := pb.RootPath
	if root == "" {
		root = pb.Path
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%q is linked; native update is disabled to avoid replacing its external source. Add an update script instead", name)
	}
	rootAbs, _ := filepath.Abs(root)
	pathAbs, _ := filepath.Abs(pb.Path)
	if rootAbs != pathAbs {
		return fmt.Errorf("%q uses manifest subdir %q; native update requires a flat playbook or a delegated update script", name, pb.Manifest.Subdir)
	}

	fmt.Printf("Updating natively from %s...\n", pb.Manifest.Source.Repository)
	work, cleanup, err := stageSource(pb.Manifest.Source.Repository, isGitURL(pb.Manifest.Source.Repository), pb.Manifest.Source.Branch, pb.Manifest.Source.Subdir)
	if err != nil {
		return fmt.Errorf("failed to fetch latest source: %w", err)
	}
	defer cleanup()

	parent := filepath.Dir(root)
	candidate, err := os.MkdirTemp(parent, "."+filepath.Base(root)+".update-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(candidate)
	if err := copyDir(root, candidate); err != nil {
		return fmt.Errorf("failed to stage current playbook: %w", err)
	}
	if err := copyDir(work, candidate); err != nil {
		return fmt.Errorf("failed to stage update: %w", err)
	}
	for _, localName := range []string{".credentials.json", ".claude.json"} {
		if err := restoreLocalEntry(root, candidate, localName); err != nil {
			return fmt.Errorf("failed to preserve %s: %w", localName, err)
		}
	}

	// Staging ran unlocked (it may fetch from the network); the swap must
	// not. Take the registry lock and RE-READ the live manifest: a
	// concurrent `alias` (or other manifest mutation) that landed while the
	// candidate was staging would otherwise be resurrected from the stale
	// pre-staging snapshot, leaving launchers and manifest disagreeing.
	unlock, lerr := lockRegistry()
	if lerr != nil {
		return lerr
	}
	defer unlock()
	liveManifest, err := manifest.Read(root)
	if err != nil {
		return fmt.Errorf("cannot re-read manifest before activation: %w", err)
	}
	// The staged candidate belongs to the installation we snapshotted.
	// Bind activation to that exact installation: the DIRECTORY must be
	// the same filesystem object as before staging (a delete + reinstall
	// from the very same repository passes any manifest comparison), and
	// every source field must match. Anything else means the playbook was
	// deleted, re-created, or re-sourced while staging ran — discard the
	// candidate instead of repairing.
	liveInfo, lierr := os.Lstat(root)
	if lierr != nil || !os.SameFile(rootInfo, liveInfo) ||
		liveManifest == nil || liveManifest.Source == nil ||
		*liveManifest.Source != *pb.Manifest.Source {
		return fmt.Errorf("playbook %q changed while the update was staging (deleted, re-created, or re-sourced); nothing activated — re-run update", name)
	}

	newManifest, err := manifest.Read(candidate)
	if err != nil {
		return err
	}
	if newManifest == nil {
		newManifest = liveManifest
	} else {
		newManifest.Alias = liveManifest.Alias
		newManifest.IsolateAuth = liveManifest.IsolateAuth
		newManifest.Source = liveManifest.Source
	}
	// The install's name is its directory name; never adopt the source's.
	// This also heals installs whose manifest predates name rewriting.
	newManifest.Name = filepath.Base(root)
	newManifest.Subdir = ""
	if err := manifest.Write(candidate, newManifest); err != nil {
		return fmt.Errorf("failed to write updated manifest: %w", err)
	}

	backupPath := filepath.Join(parent, fmt.Sprintf(".%s.bak.%s", filepath.Base(root), time.Now().UTC().Format("20060102T150405.000000000")))
	if err := os.Rename(root, backupPath); err != nil {
		return fmt.Errorf("failed to backup existing playbook to %s: %w", backupPath, err)
	}
	if err := os.Rename(candidate, root); err != nil {
		if restoreErr := os.Rename(backupPath, root); restoreErr != nil {
			return fmt.Errorf("failed to activate update: %v; restore also failed: %v", err, restoreErr)
		}
		return fmt.Errorf("failed to activate update: %w", err)
	}

	fmt.Printf("Successfully updated %q natively. Backup saved to %s.\n", name, backupPath)
	return nil
}

func restoreLocalEntry(root, candidate, name string) error {
	src := filepath.Join(root, name)
	dst := filepath.Join(candidate, name)
	info, err := os.Lstat(src)
	if os.IsNotExist(err) {
		return removeAny(dst)
	}
	if err != nil {
		return err
	}
	if err := removeAny(dst); err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(target, dst)
	}
	if info.IsDir() {
		return copyDir(src, dst)
	}
	return copyFile(src, dst, info.Mode())
}
