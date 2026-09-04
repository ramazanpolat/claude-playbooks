package cmd

import (
	"os"
	"path/filepath"
	"strings"
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

// When every staging candidate lies inside the source and does not exist
// yet, the refusal must not create it there: containment is decided from
// the nearest existing ancestor before anything is made.
func TestStagingRefusalCreatesNothingInsideSource(t *testing.T) {
	resetCommandTestState(t)
	root := t.TempDir()
	config.PlaybooksDir = filepath.Join(root, "playbooks")
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Write(source, &manifest.Manifest{Name: "pb"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", filepath.Join(source, "tmp"))           // does not exist
	t.Setenv("XDG_CACHE_HOME", filepath.Join(source, "cache")) // does not exist
	t.Setenv("HOME", filepath.Join(source, "home"))            // darwin cache dir; does not exist
	before, _ := os.ReadDir(source)

	installNoAlias = true
	err := runInstall(nil, []string{source})
	if err == nil || !strings.Contains(err.Error(), "cannot stage") {
		t.Fatalf("install with every candidate inside the source: err = %v", err)
	}
	after, _ := os.ReadDir(source)
	if len(after) != len(before) {
		t.Fatalf("refusal created entries in the source: before=%d after=%v", len(before), after)
	}
}

// A relative TMPDIR while running inside the source must be anchored before
// the containment check, or it silently passes and staging recurses.
func TestRelativeTmpdirInsideSourceIsRejected(t *testing.T) {
	resetCommandTestState(t)
	root := t.TempDir()
	config.PlaybooksDir = filepath.Join(root, "playbooks")
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(filepath.Join(source, ".tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Write(source, &manifest.Manifest{Name: "pb"}); err != nil {
		t.Fatal(err)
	}
	wd, _ := os.Getwd()
	if err := os.Chdir(source); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(wd) })
	t.Setenv("TMPDIR", ".tmp")

	installNoAlias = true
	if err := runInstall(nil, []string{source}); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(filepath.Join(source, ".tmp")); len(entries) != 0 {
		t.Fatalf("relative TMPDIR staged inside the source: %v", entries)
	}
}

// With --subdir, containment is judged against the original source root: a
// temp dir beside the selected subdirectory is still the pilot's tree.
func TestSubdirInstallNeverStagesInsideSourceRoot(t *testing.T) {
	resetCommandTestState(t)
	root := t.TempDir()
	config.PlaybooksDir = filepath.Join(root, "playbooks")
	source := filepath.Join(root, "repo")
	if err := os.MkdirAll(filepath.Join(source, "playbook"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Write(filepath.Join(source, "playbook"), &manifest.Manifest{Name: "pb"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", filepath.Join(source, "tmp")) // sibling of the subdir, does not exist

	installNoAlias = true
	installSubdir = "playbook"
	if err := runInstall(nil, []string{source}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(source, "tmp")); err == nil {
		t.Fatal("staging created <source>/tmp beside the selected subdir")
	}
	if _, err := os.Stat(filepath.Join(config.PlaybooksDir, "pb", manifest.FileName)); err != nil {
		t.Fatalf("install did not land: %v", err)
	}
}
