package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
	"github.com/ramazanpolat/claude-playbooks/internal/shell"
)

var (
	selfUninstallYes        bool
	selfUninstallKeepData   bool
	selfUninstallKeepBinary bool
	selfUninstallDryRun     bool
)

var selfUninstallCmd = &cobra.Command{
	Use:   "self-uninstall",
	Short: "Remove claude-playbook, its playbooks, and its shell aliases",
	Long: `Removes all installed playbooks, their shell aliases, the playbooks directory,
and the claude-playbook binary itself.

Use --keep-data to preserve the playbooks directory.
Use --keep-binary to leave the binary in place.
Use --dry-run to preview what would be removed without making any changes.`,
	Args: cobra.NoArgs,
	RunE: runSelfUninstall,
}

func init() {
	selfUninstallCmd.Flags().BoolVarP(&selfUninstallYes, "yes", "y", false, "skip confirmation prompt")
	selfUninstallCmd.Flags().BoolVar(&selfUninstallKeepData, "keep-data", false, "preserve the playbooks directory")
	selfUninstallCmd.Flags().BoolVar(&selfUninstallKeepBinary, "keep-binary", false, "leave the binary in place")
	selfUninstallCmd.Flags().BoolVar(&selfUninstallDryRun, "dry-run", false, "print what would be removed without doing anything")
}

func runSelfUninstall(cmd *cobra.Command, args []string) error {
	shellConfig, err := config.ResolveShellConfig()
	if err != nil {
		return err
	}
	playbooksDir := config.ResolvePlaybooksDir()

	execPath, err := os.Executable()
	if err != nil {
		execPath = "(unknown)"
	}

	pbs, _ := playbook.Discover(playbooksDir, shellConfig)

	if !selfUninstallYes && !selfUninstallDryRun {
		fmt.Printf("This will remove:\n")
		fmt.Printf("  Playbooks:     %d playbook(s) under %s\n", countTopLevel(pbs), playbooksDir)
		if !selfUninstallKeepData {
			fmt.Printf("  Directory:     %s\n", playbooksDir)
		}
		if !selfUninstallKeepBinary {
			fmt.Printf("  Binary:        %s\n", execPath)
		}
		fmt.Printf("  Shell aliases: all CLAUDE_CONFIG_DIR aliases in %s\n", shellConfig)
		fmt.Println()
		if !confirm("Permanently uninstall claude-playbook? [y/N] ") {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	if selfUninstallDryRun {
		fmt.Println("[dry-run] Would remove:")
		for _, pb := range pbs {
			if !pb.IsChild {
				fmt.Printf("  playbook: %s (%s)\n", pb.Name, pb.Path)
			}
		}
		if !selfUninstallKeepData {
			fmt.Printf("  directory: %s\n", playbooksDir)
		}
		if !selfUninstallKeepBinary {
			fmt.Printf("  binary: %s\n", execPath)
		}
		fmt.Printf("  shell aliases in: %s\n", shellConfig)
		return nil
	}

	var removed []string
	var needsManual []string

	// Step 1: remove each top-level playbook's aliases and directory.
	for _, pb := range pbs {
		if pb.IsChild {
			continue
		}
		if n, err := shell.RemoveByPathPrefix(shellConfig, pb.Path); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to remove aliases for %s: %v\n", pb.Name, err)
		} else if n > 0 {
			removed = append(removed, fmt.Sprintf("aliases for playbook %q", pb.Name))
		}
		if err := removeAny(pb.Path); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to remove %s: %v\n", pb.Path, err)
		} else {
			removed = append(removed, fmt.Sprintf("playbook %q (%s)", pb.Name, pb.Path))
		}
	}

	// Step 2: remove the playbooks root directory.
	if !selfUninstallKeepData {
		if err := os.RemoveAll(playbooksDir); err != nil && !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "warning: failed to remove %s: %v\n", playbooksDir, err)
		} else {
			removed = append(removed, fmt.Sprintf("playbooks directory (%s)", playbooksDir))
		}
	}

	// Step 3: sweep any leftover CLAUDE_CONFIG_DIR aliases pointing into the playbooks dir.
	if n, err := shell.RemoveByPathPrefix(shellConfig, playbooksDir); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to sweep leftover aliases: %v\n", err)
	} else if n > 0 {
		removed = append(removed, fmt.Sprintf("%d leftover alias(es) from %s", n, shellConfig))
	}

	// Step 4: remove the binary.
	if !selfUninstallKeepBinary && execPath != "(unknown)" {
		if err := os.Remove(execPath); err != nil {
			if os.IsPermission(err) {
				fmt.Fprintf(os.Stderr, "note: cannot remove binary (permission denied). Run manually:\n  sudo rm %s\n", execPath)
				needsManual = append(needsManual, fmt.Sprintf("sudo rm %s", execPath))
			} else if !errors.Is(err, os.ErrNotExist) {
				fmt.Fprintf(os.Stderr, "warning: failed to remove binary %s: %v\n", execPath, err)
			}
		} else {
			removed = append(removed, fmt.Sprintf("binary (%s)", execPath))
		}
	}

	fmt.Println("Removed:")
	if len(removed) == 0 {
		fmt.Println("  (nothing)")
	}
	for _, r := range removed {
		fmt.Printf("  %s\n", r)
	}

	if len(needsManual) > 0 {
		fmt.Println()
		fmt.Println("Manual follow-up needed:")
		for _, m := range needsManual {
			fmt.Printf("  %s\n", m)
		}
	}

	fmt.Println()
	fmt.Println("Reload your shell to clear any cached aliases:")
	fmt.Printf("  source %s\n", shellConfig)

	return nil
}

func countTopLevel(pbs []*playbook.Playbook) int {
	n := 0
	for _, pb := range pbs {
		if !pb.IsChild {
			n++
		}
	}
	return n
}
