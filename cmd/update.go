package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

var updateCmd = &cobra.Command{
	Use:                "update [name]",
	Short:              "Self-update the tool, or update a playbook via its bin/update-playbook.sh",
	DisableFlagParsing: true,
	ValidArgsFunction:  autocompletePlaybookNames,
	RunE:               runUpdate,
}

func runUpdate(cmd *cobra.Command, args []string) error {
	var playbooksDir, shellConfigOverride string
	var rest []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--playbooks-dir" && i+1 < len(args):
			playbooksDir = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--playbooks-dir="):
			playbooksDir = strings.TrimPrefix(args[i], "--playbooks-dir=")
		case args[i] == "--shell-config" && i+1 < len(args):
			shellConfigOverride = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--shell-config="):
			shellConfigOverride = strings.TrimPrefix(args[i], "--shell-config=")
		case args[i] == "--help" || args[i] == "-h":
			printUpdateHelp()
			return nil
		default:
			rest = append(rest, args[i])
		}
	}
	if playbooksDir != "" {
		config.PlaybooksDir = playbooksDir
	}
	if shellConfigOverride != "" {
		config.ShellConfig = shellConfigOverride
	}

	if len(rest) == 0 {
		return runSelfUpdate()
	}

	name := rest[0]
	scriptArgs := rest[1:]
	return runPlaybookUpdate(name, scriptArgs)
}

func printUpdateHelp() {
	fmt.Println("Usage: claude-playbook update [name] [script-args...]")
	fmt.Println()
	fmt.Println("Without <name>: self-update the claude-playbook binary.")
	fmt.Println("With <name>: run <playbook>/bin/update-playbook.sh, forwarding extra args.")
}

func runSelfUpdate() error {
	fmt.Printf("Current version: %s\n", Version)
	fmt.Println()
	fmt.Println("Self-update is not yet implemented. To update, re-run:")
	fmt.Println("  curl -fsSL https://raw.githubusercontent.com/ramazanpolat/claude-playbooks/main/install.sh | sh")
	return nil
}

func runPlaybookUpdate(name string, scriptArgs []string) error {
	playbooksDir := config.ResolvePlaybooksDir()
	shellConfig, _ := config.ResolveShellConfig()

	pb, err := playbook.Require(playbooksDir, shellConfig, name)
	if err != nil {
		return err
	}

	// Tier 1: Delegated Script
	scriptPath := "bin/update-playbook.sh"
	if pb.Manifest != nil && pb.Manifest.Source != nil && pb.Manifest.Source.UpdateScript != "" {
		scriptPath = pb.Manifest.Source.UpdateScript
	}
	script := filepath.Join(pb.Path, scriptPath)
	info, err := os.Stat(script)
	
	if err == nil {
		// Script exists, run it
		if info.Mode()&0111 == 0 {
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
				os.Exit(exitErr.ExitCode())
			}
			return err
		}
		return nil
	}

	// Tier 2: Native Smart Overwrite
	if pb.Manifest == nil || pb.Manifest.Source == nil || pb.Manifest.Source.Repository == "" {
		return fmt.Errorf("%q has no update script (%s) and no [source] metadata in .playbook. Cannot update natively.", name, scriptPath)
	}

	fmt.Printf("Updating natively from %s...\n", pb.Manifest.Source.Repository)
	
	work, cleanup, err := stageSource(pb.Manifest.Source.Repository, isGitURL(pb.Manifest.Source.Repository), pb.Manifest.Source.Branch, pb.Manifest.Source.Subdir)
	if err != nil {
		return fmt.Errorf("failed to fetch latest source: %w", err)
	}
	defer cleanup()

	// Simple Native Overwrite: Copy from staged dir into pb.Path.
	// Since the user warned about blindly overwriting, we back up the existing directory just in case.
	backupPath := pb.Path + ".bak." + fmt.Sprintf("%d", os.Getpid())
	if err := os.Rename(pb.Path, backupPath); err != nil {
		return fmt.Errorf("failed to backup existing playbook to %s: %w", backupPath, err)
	}
	
	if err := copyDir(work, pb.Path); err != nil {
		// Restore backup
		os.RemoveAll(pb.Path)
		os.Rename(backupPath, pb.Path)
		return fmt.Errorf("failed to apply update: %w", err)
	}
	
	// Preserve the old .playbook manifest because we might have local alias tweaks, etc.
	// (or at least merge them) but copyDir overwrote it.
	// We'll write the merged manifest.
	newManifest, _ := manifest.Read(pb.Path)
	if newManifest == nil {
		newManifest = pb.Manifest
	} else {
		newManifest.Alias = pb.Manifest.Alias // preserve alias
		newManifest.Source = pb.Manifest.Source
	}
	newManifest.Subdir = ""
	manifest.Write(pb.Path, newManifest)
	
	fmt.Printf("Successfully updated %q natively. Backup saved to %s (feel free to delete it).\n", name, backupPath)
	return nil
}
