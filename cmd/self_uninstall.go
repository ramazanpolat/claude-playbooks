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
	selfUninstallBinaryOnly bool
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
Use --binary-only to remove only the binary, its cpb sibling, launchers,
and completion lines — playbooks stay (uninstall.sh
delegates to this mode).
Use --dry-run to preview what would be removed without making any changes.

Launchers are removed wherever they were created: every launcher this
tool writes is recorded in a registry file, so custom --launcher-dir and
CLAUDE_LAUNCHER_DIR locations are cleaned automatically; a resolution
scan of the standard directories additionally covers launchers that
predate the registry.`,
	Args: cobra.NoArgs,
	RunE: runSelfUninstall,
}

func init() {
	selfUninstallCmd.Flags().BoolVarP(&selfUninstallYes, "yes", "y", false, "skip confirmation prompt")
	selfUninstallCmd.Flags().BoolVar(&selfUninstallKeepData, "keep-data", false, "preserve the playbooks directory")
	selfUninstallCmd.Flags().BoolVar(&selfUninstallKeepBinary, "keep-binary", false, "leave the binary in place")
	selfUninstallCmd.Flags().BoolVar(&selfUninstallBinaryOnly, "binary-only", false, "remove only the binary, its cpb sibling, launchers, and completion lines — playbooks and aliases untouched (what uninstall.sh runs)")
	selfUninstallCmd.Flags().BoolVar(&selfUninstallDryRun, "dry-run", false, "print what would be removed without doing anything")
}

func runSelfUninstall(cmd *cobra.Command, args []string) error {
	// cmd is nil only in tests, which combine the flags directly to keep
	// the test executable alive; through the CLI they contradict.
	if cmd != nil && selfUninstallBinaryOnly && selfUninstallKeepBinary {
		return fmt.Errorf("--binary-only and --keep-binary contradict each other")
	}
	playbooksDir := config.ResolvePlaybooksDir()

	execPath, err := os.Executable()
	if err != nil {
		execPath = "(unknown)"
	}

	var pbs []*playbook.Playbook
	if !selfUninstallBinaryOnly {
		pbs, _ = playbook.Discover(playbooksDir)
	}

	if !selfUninstallYes && !selfUninstallDryRun {
		fmt.Printf("This will remove:\n")
		if !selfUninstallKeepData && !selfUninstallBinaryOnly {
			fmt.Printf("  Playbooks:     %d playbook(s) under %s\n", len(pbs), playbooksDir)
			fmt.Printf("  Directory:     %s\n", playbooksDir)
		}
		if !selfUninstallKeepBinary {
			fmt.Printf("  Binary:        %s\n", execPath)
			if s := siblingToRemove(execPath); s != "" {
				fmt.Printf("  Sibling:       %s\n", s)
			}
		}
		for _, e := range launcherRemovalPlan(pbs) {
			fmt.Printf("  Launcher:      %s (%s)\n", e.CmdName, e.Path)
		}
		if !selfUninstallKeepBinary {
			for _, rc := range completionRcFiles() {
				if n, err := shell.CountExactLines(rc, completionLines()); err == nil && n > 0 {
					fmt.Printf("  Completions:   %d line(s) in %s\n", n, rc)
				}
			}
			if rp := launcher.ReceiptPath(); rp != "" {
				if _, err := os.Stat(rp); err == nil {
					fmt.Printf("  Registry:      %s\n", rp)
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
		if !selfUninstallKeepData && !selfUninstallBinaryOnly {
			for _, pb := range pbs {
				removePath := pb.RootPath
				if removePath == "" {
					removePath = pb.Path
				}
				fmt.Printf("  playbook: %s (%s)\n", pb.Name, removePath)
			}
			fmt.Printf("  directory: %s\n", playbooksDir)
		}
		if !selfUninstallKeepBinary {
			fmt.Printf("  binary: %s\n", execPath)
			if s := siblingToRemove(execPath); s != "" {
				fmt.Printf("  sibling: %s\n", s)
			}
		}
		for _, e := range launcherRemovalPlan(pbs) {
			fmt.Printf("  launcher: %s (%s)\n", e.CmdName, e.Path)
		}
		if !selfUninstallKeepBinary {
			for _, rc := range completionRcFiles() {
				if n, err := shell.CountExactLines(rc, completionLines()); err == nil && n > 0 {
					fmt.Printf("  %d completion line(s) from: %s\n", n, rc)
				}
			}
			if rp := launcher.ReceiptPath(); rp != "" {
				if _, err := os.Stat(rp); err == nil {
					fmt.Printf("  launcher registry: %s\n", rp)
				}
			}
		}
		return nil
	}

	var removed []string
	var needsManual []string

	// Step 1: remove each playbook's directory. (Binary-only mode
	// discovered no playbooks: data stays untouched.)
	for _, pb := range pbs {
		removePath := pb.RootPath
		if removePath == "" {
			removePath = pb.Path
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
	if !selfUninstallKeepData && !selfUninstallBinaryOnly {
		if err := os.RemoveAll(playbooksDir); err != nil && !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "warning: failed to remove %s: %v\n", playbooksDir, err)
		} else {
			removed = append(removed, fmt.Sprintf("playbooks directory (%s)", playbooksDir))
		}
	}

	// Step 3: remove launchers. When the binary is going away, every link
	// to it would dangle — sweep the scan dirs plus every receipt entry
	// that still verifies as ours. With --keep-binary, launchers for OTHER
	// playbook roots sharing the directory keep working, so none are
	// removed. Once the launchers are gone the receipt has nothing left to
	// describe, so it goes too.
	for _, e := range launcherRemovalPlan(pbs) {
		if rerr := os.Remove(e.Path); rerr != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to remove launcher %s: %v\n", e.Path, rerr)
		} else {
			removed = append(removed, fmt.Sprintf("launcher %q (%s)", e.CmdName, e.Path))
		}
	}
	if !selfUninstallKeepBinary {
		if rp := launcher.ReceiptPath(); rp != "" {
			if _, err := os.Stat(rp); err == nil {
				launcher.RemoveReceipt()
				removed = append(removed, fmt.Sprintf("launcher registry (%s)", rp))
			}
		}
	}

	// Step 3.5: remove the completion lines install.sh appended to shell rc
	// files — after the binary is gone they error on every new shell. With
	// --keep-binary they keep working, so they stay.
	completionLinesRemoved := 0
	if !selfUninstallKeepBinary {
		for _, rc := range completionRcFiles() {
			if n, err := shell.RemoveExactLines(rc, completionLines()); err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not update %s: %v\n", rc, err)
			} else if n > 0 {
				completionLinesRemoved += n
				removed = append(removed, fmt.Sprintf("%d completion line(s) from %s", n, rc))
			}
		}
	}

	// Step 4: remove the binary. The sibling must be identified BEFORE the
	// binary goes: ownership is proven by both resolving to the same file,
	// which is impossible to check once one of them is deleted.
	if !selfUninstallKeepBinary && execPath != "(unknown)" {
		siblingPath := siblingToRemove(execPath)

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

		if siblingPath != "" {
			if info, err := os.Lstat(siblingPath); err == nil {
				if err := os.Remove(siblingPath); err != nil {
					if os.IsPermission(err) {
						fmt.Fprintf(os.Stderr, "note: cannot remove sibling binary (permission denied). Run manually:\n  sudo rm %s\n", siblingPath)
						needsManual = append(needsManual, fmt.Sprintf("sudo rm %s", siblingPath))
					} else {
						fmt.Fprintf(os.Stderr, "warning: failed to remove sibling binary %s: %v\n", siblingPath, err)
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

	// The advisory rc lock files (<rc>.claude-playbook.lock) are deliberately
	// LEFT BEHIND: unlinking a flock pathname splits any concurrent locker
	// onto a different inode, and a claude-playbook process already running
	// survives its binary's removal — deleting the lock could let its edit
	// overlap a later editor's and lose rc content. Two empty files are the
	// cheaper failure.

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

	// Editing the rc files does not reach shells that are already open —
	// their loaded completion functions go stale only on reload (SPEC
	// self-uninstall step 7).
	if completionLinesRemoved > 0 {
		fmt.Println()
		fmt.Println("Completion lines were removed; open a new shell (or re-source your rc file) to drop the stale completion functions.")
	}

	if selfUninstallBinaryOnly {
		fmt.Println()
		fmt.Printf("Playbooks were not touched: %s\n", playbooksDir)
	}

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
	// The reserved CLI names resolve to this binary too but are not
	// playbook launchers — the sibling/binary cleanup owns them. Sweeping
	// them here would double-report the same path (launcher AND sibling)
	// and desync the preview from what each step actually removes.
	var out []launcher.Entry
	for _, e := range les {
		if launcher.ReservedNames[e.CmdName] {
			continue
		}
		out = append(out, e)
	}
	return out
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

// siblingToRemove returns the OTHER reserved-name entry beside the binary
// (claude-playbook <-> cpb) if and only if it resolves to the same file as
// the binary being removed — the installer only ever creates the pair that
// way, so an unrelated regular file or foreign link under a reserved name
// is left alone. Shared by preview and removal. Empty when there is nothing
// provably ours to remove.
func siblingToRemove(execPath string) string {
	if selfUninstallKeepBinary || execPath == "(unknown)" {
		return ""
	}
	var siblingName string
	switch filepath.Base(execPath) {
	case "claude-playbook":
		siblingName = "cpb"
	case "cpb":
		siblingName = "claude-playbook"
	default:
		return ""
	}
	p := filepath.Join(filepath.Dir(execPath), siblingName)
	if _, err := os.Lstat(p); err != nil {
		return ""
	}
	pResolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return ""
	}
	execResolved, err := filepath.EvalSymlinks(execPath)
	if err != nil {
		return ""
	}
	if pResolved != execResolved {
		return ""
	}
	return p
}

// launcherRemovalPlan is THE list of launchers uninstall will delete —
// previews and the removal step share it so consent always matches
// execution. It unions two sources: a resolution scan of the standard
// directories (covers launchers that predate the receipt), and the
// receipt's recorded paths (covers custom --launcher-dir installs the
// scan cannot know about). Every candidate is verified against the live
// filesystem; deduplication is by canonical location.
func launcherRemovalPlan(pbs []*playbook.Playbook) []launcher.Entry {
	if selfUninstallKeepBinary {
		return nil
	}
	seen := map[string]bool{}
	var out []launcher.Entry
	add := func(e launcher.Entry) {
		key := canonPath(filepath.Dir(e.Path)) + "/" + e.CmdName
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, e)
	}
	for _, ldir := range launcherSweepDirs() {
		for _, e := range launchersToRemove(ldir, pbs) {
			add(e)
		}
	}
	for _, e := range receiptLaunchers() {
		add(e)
	}
	return out
}

// receiptLaunchers returns recorded launcher paths that still verify as
// ours: a symlink resolving to this binary, or a dangling one (we wrote
// it; a live command resolving elsewhere is never removed on the
// receipt's say-so). Paths the user renamed or deleted by hand no longer
// match anything and are skipped.
func receiptLaunchers() []launcher.Entry {
	bin, berr := launcher.BinPath()
	var out []launcher.Entry
	for _, p := range launcher.Recorded() {
		name := filepath.Base(p)
		if launcher.ReservedNames[name] {
			continue
		}
		info, err := os.Lstat(p)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		if resolved, rerr := filepath.EvalSymlinks(p); rerr == nil {
			if berr != nil || resolved != bin {
				continue // resolves, but not to us (or unverifiable): live foreign command
			}
		}
		target, _ := os.Readlink(p)
		out = append(out, launcher.Entry{CmdName: name, Path: p, Target: target})
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
