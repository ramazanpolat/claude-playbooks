package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// TMPDIR inside the source tree must not make staging copy itself into
// itself: install and update both stage a local source.
func TestLocalSourceStagesOutsideItselfWhenTmpdirIsInside(t *testing.T) {
	resetCommandTestState(t)
	root := t.TempDir()
	config.PlaybooksDir = filepath.Join(root, "playbooks")
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(filepath.Join(source, "tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "CLAUDE.md"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Write(source, &manifest.Manifest{Name: "pb"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", filepath.Join(source, "tmp"))

	installNoAlias = true
	if err := runInstall(nil, []string{source}); err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(config.PlaybooksDir, "pb")
	if _, err := os.Stat(filepath.Join(installed, "CLAUDE.md")); err != nil {
		t.Fatalf("install did not land: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(source, "tmp")); len(entries) != 0 {
		t.Fatalf("staging landed inside the source: %v", entries)
	}
	if _, err := os.Stat(filepath.Join(installed, "tmp", "tmp")); err == nil {
		t.Fatal("recursive self-copy reached the install")
	}

	// update from the same local source, same TMPDIR
	if err := manifest.Write(installed, &manifest.Manifest{Name: "pb", Source: &manifest.Source{Repository: source}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "CLAUDE.md"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runPlaybookUpdate("pb", false); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(installed, "CLAUDE.md")); string(got) != "v2\n" {
		t.Fatalf("update did not land: %q", got)
	}
	if entries, _ := os.ReadDir(filepath.Join(source, "tmp")); len(entries) != 0 {
		t.Fatalf("update staging landed inside the source: %v", entries)
	}
}
