package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/shell"
)

// seedCompletionRc creates a sandbox HOME holding rc files with the exact
// completion lines install.sh appends, plus one user line that must survive
// every path through self-uninstall.
func seedCompletionRc(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	content := "# user content\nsource <(claude-playbook completion zsh)\nsource <(cpb completion zsh)\n"
	for _, name := range []string{".bashrc", ".zshrc"} {
		if err := os.WriteFile(filepath.Join(home, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func TestSelfUninstallKeepBinaryPreservesCompletionLines(t *testing.T) {
	resetCommandTestState(t)
	home := seedCompletionRc(t)
	config.PlaybooksDir = filepath.Join(home, "playbooks")
	config.ShellConfig = filepath.Join(home, "shellrc")
	selfUninstallYes = true
	selfUninstallKeepData = true
	selfUninstallKeepBinary = true

	if err := runSelfUninstall(nil, nil); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(home, ".zshrc"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "source <(cpb completion zsh)") {
		t.Fatalf("--keep-binary removed completion lines for a binary that still works:\n%s", data)
	}
}

func TestSelfUninstallDryRunPreviewsCompletionLinesAndMutatesNothing(t *testing.T) {
	resetCommandTestState(t)
	home := seedCompletionRc(t)
	config.PlaybooksDir = filepath.Join(home, "playbooks")
	config.ShellConfig = filepath.Join(home, "shellrc")
	lockFile := filepath.Join(home, ".zshrc.claude-playbook.lock")
	if err := os.WriteFile(lockFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	selfUninstallDryRun = true

	out := captureStdout(t, func() {
		if err := runSelfUninstall(nil, nil); err != nil {
			t.Fatal(err)
		}
	})

	if !strings.Contains(out, "completion line(s) from") {
		t.Fatalf("dry-run does not preview the rc completion edit:\n%s", out)
	}
	// Advisory lock files survive uninstall by design (deleting a flock
	// pathname splits concurrent lockers across inodes), so the preview
	// must not claim them either.
	if strings.Contains(out, lockFile) {
		t.Fatalf("dry-run previews a lock-file removal that must not happen:\n%s", out)
	}
	if _, err := os.Stat(lockFile); err != nil {
		t.Fatalf("dry-run removed the lock file: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".zshrc"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "source <(cpb completion zsh)") {
		t.Fatalf("dry-run modified an rc file:\n%s", data)
	}
}

func TestSelfUninstallBinaryOnlyLeavesDataAndAliases(t *testing.T) {
	resetCommandTestState(t)
	home := seedCompletionRc(t)
	config.PlaybooksDir = filepath.Join(home, "playbooks")
	config.ShellConfig = filepath.Join(home, "shellrc")
	root := filepath.Join(config.PlaybooksDir, "kept")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".playbook"), []byte("version = \"0.1.0\"\nname = \"kept\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := shell.Write(config.ShellConfig, "kept", root); err != nil {
		t.Fatal(err)
	}
	selfUninstallYes = true
	selfUninstallBinaryOnly = true
	selfUninstallKeepBinary = true // test seam: keeps the running test executable alive

	if err := runSelfUninstall(nil, nil); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(root, ".playbook")); err != nil {
		t.Fatalf("binary-only removed playbook data: %v", err)
	}
	entries, err := shell.ReadAll(config.ShellConfig)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("binary-only touched aliases: %#v", entries)
	}
}

func TestSelfUninstallBinaryOnlyRejectsKeepBinaryContradiction(t *testing.T) {
	resetCommandTestState(t)
	selfUninstallBinaryOnly = true
	selfUninstallKeepBinary = true
	selfUninstallYes = true
	// Passing the real command engages the CLI-only validation, which
	// rejects the combination before touching anything.
	err := runSelfUninstall(selfUninstallCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "contradict") {
		t.Fatalf("expected contradiction error, got %v", err)
	}
}

func TestLaunchersToRemoveExcludesReservedNames(t *testing.T) {
	resetCommandTestState(t)
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"deploy", "cpb", "claude-playbook"} {
		if err := os.Symlink(exe, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	les := launchersToRemove(dir, nil)
	if len(les) != 1 || les[0].CmdName != "deploy" {
		t.Fatalf("reserved names must be left to sibling/binary cleanup, got %#v", les)
	}
}

func TestSiblingToRemoveRequiresSameResolution(t *testing.T) {
	resetCommandTestState(t)
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude-playbook")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cpb := filepath.Join(dir, "cpb")

	if got := siblingToRemove(bin); got != "" {
		t.Fatalf("no sibling on disk, got %q", got)
	}

	// The installer's own pair: cpb -> claude-playbook is removed.
	if err := os.Symlink("claude-playbook", cpb); err != nil {
		t.Fatal(err)
	}
	if got := siblingToRemove(bin); got != cpb {
		t.Fatalf("installer sibling: got %q, want %q", got, cpb)
	}

	// Invoked THROUGH the symlink: the real binary is the sibling.
	if got := siblingToRemove(cpb); got != bin {
		t.Fatalf("exec via symlink: got %q, want %q", got, bin)
	}

	// A foreign regular file under the reserved name survives.
	if err := os.Remove(cpb); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cpb, []byte("someone else's cpb"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := siblingToRemove(bin); got != "" {
		t.Fatalf("foreign regular file claimed as sibling: %q", got)
	}

	// A symlink resolving elsewhere survives.
	other := filepath.Join(dir, "other-tool")
	if err := os.WriteFile(other, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(cpb); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("other-tool", cpb); err != nil {
		t.Fatal(err)
	}
	if got := siblingToRemove(bin); got != "" {
		t.Fatalf("foreign symlink claimed as sibling: %q", got)
	}

	// Non-reserved basenames never have siblings.
	if got := siblingToRemove(other); got != "" {
		t.Fatalf("non-reserved basename produced sibling %q", got)
	}

	// --keep-binary keeps the pair intact.
	if err := os.Remove(cpb); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("claude-playbook", cpb); err != nil {
		t.Fatal(err)
	}
	selfUninstallKeepBinary = true
	if got := siblingToRemove(bin); got != "" {
		t.Fatalf("--keep-binary still selects sibling %q", got)
	}
	selfUninstallKeepBinary = false
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string)
	go func() {
		buf := make([]byte, 0, 4096)
		tmp := make([]byte, 1024)
		for {
			n, err := r.Read(tmp)
			buf = append(buf, tmp[:n]...)
			if err != nil {
				break
			}
		}
		done <- string(buf)
	}()
	defer func() {
		os.Stdout = old
	}()
	fn()
	w.Close()
	os.Stdout = old
	return <-done
}
