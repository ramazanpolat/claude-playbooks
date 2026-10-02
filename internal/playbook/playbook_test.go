package playbook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDiscoverBareDirectory verifies the flat model: a direct child directory
// with no .playbook manifest is still discovered as a valid playbook.
func TestDiscoverBareDirectory(t *testing.T) {
	root := t.TempDir()
	bare := filepath.Join(root, "bare")
	if err := os.MkdirAll(bare, 0755); err != nil {
		t.Fatal(err)
	}

	pbs, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(pbs) != 1 {
		t.Fatalf("expected 1 playbook, got %d: %#v", len(pbs), pbs)
	}
	pb := pbs[0]
	if pb.Name != "bare" {
		t.Fatalf("name = %q, want %q", pb.Name, "bare")
	}
	if pb.Path != bare {
		t.Fatalf("path = %q, want %q", pb.Path, bare)
	}
	if pb.Manifest != nil {
		t.Fatalf("expected nil manifest for bare directory, got %#v", pb.Manifest)
	}
}

// TestDiscoverFlatNoNesting verifies that directories nested below the first
// level are not themselves discovered as playbooks, and that a top-level dir
// with a manifest declaring metadata is discovered exactly once.
func TestDiscoverFlatNoNesting(t *testing.T) {
	root := t.TempDir()
	// A monorepo-like tree dropped in by hand: one top-level dir with nested
	// subdirectories that must NOT be discovered as separate playbooks.
	top := filepath.Join(root, "mono")
	for _, sub := range []string{"playbooks/sre", "playbooks/dba"} {
		if err := os.MkdirAll(filepath.Join(top, sub), 0755); err != nil {
			t.Fatal(err)
		}
	}
	// A second real top-level playbook with a manifest.
	other := filepath.Join(root, "other")
	if err := os.MkdirAll(other, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, ".playbook"),
		[]byte("version = \"1.0.0\"\ndescription = \"desc\"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	pbs, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(pbs) != 2 {
		t.Fatalf("expected 2 top-level playbooks, got %d: %#v", len(pbs), pbs)
	}
	// Sorted alphabetically: mono, other.
	if pbs[0].Name != "mono" || pbs[1].Name != "other" {
		t.Fatalf("names = %q, %q; want mono, other", pbs[0].Name, pbs[1].Name)
	}
	if pbs[1].Description != "desc" {
		t.Fatalf("description = %q, want %q", pbs[1].Description, "desc")
	}
}

func TestDiscoverReturnsInvalidManifestError(t *testing.T) {
	root := t.TempDir()
	playbookDir := filepath.Join(root, "broken")
	if err := os.Mkdir(playbookDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(playbookDir, ".playbook"), []byte("subdir = [broken\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Discover(root); err == nil {
		t.Fatal("expected malformed manifest to fail discovery")
	}
}

// A top-level subdir is not a v4 key: discovery refuses the manifest and
// names it.
func TestDiscoverRefusesATopLevelSubdir(t *testing.T) {
	root := t.TempDir()
	playbookDir := filepath.Join(root, "missing")
	if err := os.Mkdir(playbookDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(playbookDir, ".playbook"), []byte("subdir = \"config\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Discover(root); err == nil || !strings.Contains(err.Error(), `unknown key "subdir"`) {
		t.Fatalf("discovery over a top-level subdir: %v", err)
	}
}
