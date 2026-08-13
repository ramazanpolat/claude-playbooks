package shell

import (
	"os"
	"path/filepath"
	"testing"
)

var completionDoomed = []string{
	"source <(claude-playbook completion bash)",
	"source <(claude-playbook completion zsh)",
	"source <(cpb completion bash)",
	"source <(cpb completion zsh)",
}

func TestRemoveExactLinesKeepsEverythingElse(t *testing.T) {
	rc := filepath.Join(t.TempDir(), ".zshrc")
	content := "export PATH=/usr/local/bin:$PATH\n" +
		"source <(claude-playbook completion zsh)\n" +
		"alias ll='ls -la'\n" +
		"source <(cpb completion zsh)\n" +
		"# source <(cpb completion zsh) -- commented, must stay\n" +
		"  source <(cpb completion zsh)\n" // indented: not an exact match, must stay
	if err := os.WriteFile(rc, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	n, err := RemoveExactLines(rc, completionDoomed)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("removed %d lines, want 2", n)
	}
	got, err := os.ReadFile(rc)
	if err != nil {
		t.Fatal(err)
	}
	want := "export PATH=/usr/local/bin:$PATH\n" +
		"alias ll='ls -la'\n" +
		"# source <(cpb completion zsh) -- commented, must stay\n" +
		"  source <(cpb completion zsh)\n"
	if string(got) != want {
		t.Fatalf("content after removal:\n%q\nwant:\n%q", got, want)
	}
	info, err := os.Stat(rc)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode changed to %v, want 0600", info.Mode().Perm())
	}
}

func TestRemoveExactLinesKeepsPrecedingClaudePlaybookComment(t *testing.T) {
	// The alias editors strip a preceding "# claude-playbook:" marker line;
	// completion lines are never written with one, so here such a comment is
	// user-authored and must survive.
	rc := filepath.Join(t.TempDir(), ".zshrc")
	content := "# claude-playbook: custom setup\nsource <(cpb completion zsh)\n"
	if err := os.WriteFile(rc, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := RemoveExactLines(rc, completionDoomed)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("removed %d lines, want 1", n)
	}
	got, _ := os.ReadFile(rc)
	if string(got) != "# claude-playbook: custom setup\n" {
		t.Fatalf("user comment did not survive: %q", got)
	}
}

func TestRemoveExactLinesWritesThroughSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "dotfiles-zshrc")
	link := filepath.Join(dir, ".zshrc")
	if err := os.WriteFile(real, []byte("keep me\nsource <(cpb completion zsh)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	n, err := RemoveExactLines(link, completionDoomed)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("removed %d lines, want 1", n)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf(".zshrc is no longer a symlink after the edit")
	}
	got, err := os.ReadFile(real)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "keep me\n" {
		t.Fatalf("symlink target content: %q, want %q", got, "keep me\n")
	}
}

func TestRemoveExactLinesNoMatchLeavesFileUntouched(t *testing.T) {
	rc := filepath.Join(t.TempDir(), ".bashrc")
	if err := os.WriteFile(rc, []byte("just a line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(rc)
	n, err := RemoveExactLines(rc, completionDoomed)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("removed %d lines, want 0", n)
	}
	after, _ := os.Stat(rc)
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("file was rewritten despite zero matches")
	}
}

func TestCountExactLines(t *testing.T) {
	rc := filepath.Join(t.TempDir(), ".bashrc")
	if err := os.WriteFile(rc, []byte("a\nsource <(cpb completion bash)\nsource <(claude-playbook completion bash)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := CountExactLines(rc, completionDoomed)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("counted %d, want 2", n)
	}
	// Counting must not modify.
	data, _ := os.ReadFile(rc)
	if len(data) == 0 {
		t.Fatal("file emptied by CountExactLines")
	}
	// A missing file counts as zero lines, matching readLines semantics.
	if n, err := CountExactLines(filepath.Join(t.TempDir(), "absent"), completionDoomed); err != nil || n != 0 {
		t.Fatalf("missing file: n=%d err=%v, want 0, nil", n, err)
	}
}
