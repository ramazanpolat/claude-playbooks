package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
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
	selfUninstallDryRun = true

	out := captureStdout(t, func() {
		if err := runSelfUninstall(nil, nil); err != nil {
			t.Fatal(err)
		}
	})

	if !strings.Contains(out, "completion line(s) from") {
		t.Fatalf("dry-run does not preview the rc completion edit:\n%s", out)
	}
	data, err := os.ReadFile(filepath.Join(home, ".zshrc"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "source <(cpb completion zsh)") {
		t.Fatalf("dry-run modified an rc file:\n%s", data)
	}
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
