package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	var playbooksDir string
	var force, checkOnly bool
	var rest []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--playbooks-dir" && i+1 < len(args):
			playbooksDir = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--playbooks-dir="):
			playbooksDir = strings.TrimPrefix(args[i], "--playbooks-dir=")
		case (args[i] == "--help" || args[i] == "-h") && len(rest) == 0:
			printUpdateHelp()
			return nil
		// --force/--check are self-update flags; only honor them before a name
		// so a playbook's delegated update script still receives its own flags
		// (e.g. `update kommander --force` passes --force to kommander's script).
		case (args[i] == "--force" || args[i] == "-f") && len(rest) == 0:
			force = true
		case args[i] == "--check" && len(rest) == 0:
			checkOnly = true
		default:
			rest = append(rest, args[i])
		}
	}
	if playbooksDir != "" {
		config.PlaybooksDir = playbooksDir
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

	newManifest, err := manifest.Read(candidate)
	if err != nil {
		return err
	}
	if newManifest == nil {
		newManifest = pb.Manifest
	} else {
		newManifest.Alias = pb.Manifest.Alias
		newManifest.IsolateAuth = pb.Manifest.IsolateAuth
		newManifest.Source = pb.Manifest.Source
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
