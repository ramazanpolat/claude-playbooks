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
	Short: "Remove claude-playbook, its playbooks, launchers, and shell integration",
	Long: `Removes all installed playbooks, their launcher commands, legacy shell
aliases, the completion lines install.sh added to shell rc files, the
playbooks directory, and the claude-playbook binary itself.

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
		for _, ldir := range launcherSweepDirs() {
			if les := launchersToRemove(ldir, pbs); len(les) > 0 {
				fmt.Printf("  Launchers:     %d command(s) in %s\n", len(les), ldir)
			}
		}
		if !selfUninstallKeepBinary {
			for _, rc := range completionRcFiles() {
				if n, err := shell.CountExactLines(rc, completionLines()); err == nil && n > 0 {
					fmt.Printf("  Completions:   %d line(s) in %s\n", n, rc)
				}
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
		for _, ldir := range launcherSweepDirs() {
			for _, e := range launchersToRemove(ldir, pbs) {
				fmt.Printf("  launcher: %s (%s)\n", e.CmdName, e.Path)
			}
		}
		if !selfUninstallKeepBinary {
			for _, rc := range completionRcFiles() {
				if n, err := shell.CountExactLines(rc, completionLines()); err == nil && n > 0 {
					fmt.Printf("  %d completion line(s) from: %s\n", n, rc)
				}
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
	// to it would dangle — sweep them all, in every directory launcher.Write
	// may have used. With --keep-binary, launchers for OTHER playbook roots
	// sharing the directory keep working, so none are removed.
	sweepDirs := launcherSweepDirs()
	if len(sweepDirs) == 0 {
		fmt.Fprintf(os.Stderr, "warning: could not resolve any launcher directory; launchers not swept\n")
	}
	for _, ldir := range sweepDirs {
		for _, e := range launchersToRemove(ldir, pbs) {
			if rerr := os.Remove(e.Path); rerr != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to remove launcher %s: %v\n", e.Path, rerr)
			} else {
				removed = append(removed, fmt.Sprintf("launcher %q (%s)", e.CmdName, e.Path))
			}
		}
	}

	// Step 3.5: remove the completion lines install.sh appended to shell rc
	// files — after the binary is gone they error on every new shell. With
	// --keep-binary they keep working, so they stay.
	if !selfUninstallKeepBinary {
		for _, rc := range completionRcFiles() {
			if n, err := shell.RemoveExactLines(rc, completionLines()); err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not update %s: %v\n", rc, err)
			} else if n > 0 {
				removed = append(removed, fmt.Sprintf("%d completion line(s) from %s", n, rc))
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

	// The rc-file editors keep an advisory lock file beside each file they
	// touch; once the tool itself is gone that litter is ours to clear.
	if !selfUninstallKeepBinary {
		for _, rc := range append(completionRcFiles(), shellConfig) {
			shell.RemoveLockFile(rc)
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
// --keep-binary every link to the binary is doomed to dangle, so all are
// swept. With --keep-binary none are touched: a symlink carries no root
// identity, so a same-named command may be serving another registry
// (selected via environment) — removed default-root playbooks then fail
// loudly as "unknown playbook" when invoked, which is cleanable, unlike a
// silently deleted shared command.
func launchersToRemove(ldir string, pbs []*playbook.Playbook) []launcher.Entry {
	if selfUninstallKeepBinary {
		return nil
	}
	les, err := launcher.List(ldir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to list launchers: %v\n", err)
		return nil
	}
	return les
}

// completionRcFiles returns the rc files install.sh may have appended
// completion lines to. Empty when the home directory is unknown — better to
// leave the lines behind than to rewrite a relative .bashrc in the current
// working directory.
func completionRcFiles() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{filepath.Join(home, ".bashrc"), filepath.Join(home, ".zshrc")}
}

// completionLines is the exact set of `source <(NAME completion SHELL)`
// lines install.sh appends: both CLI names, plus the basename the binary is
// actually running under in case an install predates the fixed-name scheme.
func completionLines() []string {
	names := map[string]bool{"claude-playbook": true, "cpb": true}
	if exe, err := os.Executable(); err == nil {
		names[filepath.Base(exe)] = true
	}
	var out []string
	for name := range names {
		for _, sh := range []string{"bash", "zsh"} {
			out = append(out, fmt.Sprintf("source <(%s completion %s)", name, sh))
		}
	}
	return out
}

// launcherSweepDirs returns every directory the launcher sweep must cover:
// the resolved launcher dir plus the ~/.local/bin fallback launcher.Write
// uses when the primary dir is unwritable — resolution is
// writability-sensitive (think sudo), so the two can disagree with where
// launchers were actually written. Deduped canonically; preview and removal
// share this so they always agree.
func launcherSweepDirs() []string {
	var dirs []string
	if ldir, err := config.ResolveLauncherDir(); err == nil {
		dirs = append(dirs, ldir)
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".local", "bin"))
	}
	seen := map[string]bool{}
	var out []string
	for _, d := range dirs {
		c := canonPath(d)
		if seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, d)
	}
	return out
}
