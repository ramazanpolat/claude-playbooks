package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/launcher"
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
		if !selfUninstallKeepData {
			fmt.Printf("  Playbooks:     %d playbook(s) under %s\n", len(pbs), playbooksDir)
		}
		if !selfUninstallKeepData {
			fmt.Printf("  Directory:     %s\n", playbooksDir)
		}
		if !selfUninstallKeepBinary {
			fmt.Printf("  Binary:        %s\n", execPath)
		}
		fmt.Printf("  Shell aliases: all CLAUDE_CONFIG_DIR aliases in %s\n", shellConfig)
		if ldir, lerr := config.ResolveLauncherDir(); lerr == nil {
			if les := launchersToRemove(ldir, pbs); len(les) > 0 {
				fmt.Printf("  Launchers:     %d command(s) in %s\n", len(les), ldir)
			}
		}
		fmt.Println()
		if !confirm("Permanently uninstall claude-playbook? [y/N] ") {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	if selfUninstallDryRun {
		fmt.Println("[dry-run] Would remove:")
		if !selfUninstallKeepData {
			for _, pb := range pbs {
				removePath := pb.RootPath
				if removePath == "" {
					removePath = pb.Path
				}
				fmt.Printf("  playbook: %s (%s)\n", pb.Name, removePath)
			}
		}
		if !selfUninstallKeepData {
			fmt.Printf("  directory: %s\n", playbooksDir)
		}
		if !selfUninstallKeepBinary {
			fmt.Printf("  binary: %s\n", execPath)
		}
		fmt.Printf("  shell aliases in: %s\n", shellConfig)
		if ldir, lerr := config.ResolveLauncherDir(); lerr == nil {
			for _, e := range launchersToRemove(ldir, pbs) {
				fmt.Printf("  launcher: %s (%s)\n", e.CmdName, e.Path)
			}
		}
		return nil
	}

	var removed []string
	var needsManual []string

	// Step 1: remove each playbook's aliases and directory.
	for _, pb := range pbs {
		removePath := pb.RootPath
		if removePath == "" {
			removePath = pb.Path
		}
		if n, err := shell.RemoveByPathPrefix(shellConfig, removePath); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to remove aliases for %s: %v\n", pb.Name, err)
		} else if n > 0 {
			removed = append(removed, fmt.Sprintf("aliases for playbook %q", pb.Name))
		}
		if !selfUninstallKeepData {
			if err := removeAny(removePath); err != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to remove %s: %v\n", removePath, err)
			} else {
				removed = append(removed, fmt.Sprintf("playbook %q (%s)", pb.Name, removePath))
			}
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

	// Step 3: remove launchers. When the binary is going away, every link
	// to it would dangle — sweep them all. With --keep-binary, launchers
	// for OTHER playbook roots sharing the directory keep working, so only
	// the selected registry's command names are removed.
	if ldir, lerr := config.ResolveLauncherDir(); lerr != nil {
		fmt.Fprintf(os.Stderr, "warning: could not resolve launcher dir: %v\n", lerr)
	} else {
		for _, e := range launchersToRemove(ldir, pbs) {
			if rerr := os.Remove(e.Path); rerr != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to remove launcher %s: %v\n", e.Path, rerr)
			} else {
				removed = append(removed, fmt.Sprintf("launcher %q (%s)", e.CmdName, e.Path))
			}
		}
	}

	// Step 4: sweep any leftover CLAUDE_CONFIG_DIR aliases pointing into the playbooks dir.
	if n, err := shell.RemoveByPathPrefix(shellConfig, playbooksDir); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to sweep leftover aliases: %v\n", err)
	} else if n > 0 {
		removed = append(removed, fmt.Sprintf("%d leftover alias(es) from %s", n, shellConfig))
	}

	// Step 4: remove the binary.
	if !selfUninstallKeepBinary && execPath != "(unknown)" {
		dir := filepath.Dir(execPath)
		base := filepath.Base(execPath)
		siblingName := ""
		if base == "claude-playbook" {
			siblingName = "cpb"
		} else if base == "cpb" {
			siblingName = "claude-playbook"
		}

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

		if siblingName != "" {
			siblingPath := filepath.Join(dir, siblingName)
			if info, err := os.Lstat(siblingPath); err == nil {
				if err := os.Remove(siblingPath); err != nil {
					if os.IsPermission(err) {
						fmt.Fprintf(os.Stderr, "note: cannot remove sibling binary (permission denied). Run manually:\n  sudo rm %s\n", siblingPath)
						needsManual = append(needsManual, fmt.Sprintf("sudo rm %s", siblingPath))
					} else {
						fmt.Fprintf(os.Stderr, "warning: failed to remove sibling binary %s: %v\n", siblingName, err)
					}
				} else {
					typeName := "sibling binary"
					if info.Mode()&os.ModeSymlink != 0 {
						typeName = "sibling symlink"
					}
					removed = append(removed, fmt.Sprintf("%s (%s)", typeName, siblingPath))
				}
			}
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
	fmt.Printf("  %s\n", shell.ReloadHint(shellConfig))

	return nil
}

// launchersToRemove returns the launcher entries self-uninstall will delete
// — previews and the removal step share this so they always agree. Without
// --keep-binary every link to the binary is doomed to dangle; with it, only
// the selected registry's command names are removed.
func launchersToRemove(ldir string, pbs []*playbook.Playbook) []launcher.Entry {
	les, err := launcher.List(ldir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to list launchers: %v\n", err)
		return nil
	}
	if !selfUninstallKeepBinary {
		return les
	}
	owned := map[string]bool{}
	for _, pb := range pbs {
		for _, n := range launcherNamesFor(pb) {
			owned[n] = true
		}
	}
	var out []launcher.Entry
	for _, e := range les {
		if owned[e.CmdName] {
			out = append(out, e)
		}
	}
	return out
}
