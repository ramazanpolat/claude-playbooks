package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyDirDereferencesInternalSymlinks(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")

	if err := os.MkdirAll(filepath.Join(src, "playbook", "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "playbook", "CLAUDE.md"), []byte("# Playbook\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "playbook", "bin", "helper"), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("playbook/CLAUDE.md", filepath.Join(src, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("playbook/bin", filepath.Join(src, "bin")); err != nil {
		t.Fatal(err)
	}

	if err := copyDir(src, dst); err != nil {
		t.Fatal(err)
	}

	assertNotSymlink(t, filepath.Join(dst, "CLAUDE.md"))
	assertNotSymlink(t, filepath.Join(dst, "bin"))
	if got, err := os.ReadFile(filepath.Join(dst, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	} else if string(got) != "# Playbook\n" {
		t.Fatalf("copied CLAUDE.md = %q", string(got))
	}
	if _, err := os.Stat(filepath.Join(dst, "bin", "helper")); err != nil {
		t.Fatalf("copied symlinked dir contents: %v", err)
	}
}

func TestCopyDirPreservesExternalSymlinks(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	external := filepath.Join(root, "external.txt")

	if err := os.MkdirAll(src, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(external, []byte("external\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(src, "external.txt")); err != nil {
		t.Fatal(err)
	}

	if err := copyDir(src, dst); err != nil {
		t.Fatal(err)
	}

	got, err := os.Readlink(filepath.Join(dst, "external.txt"))
	if err != nil {
		t.Fatalf("expected external symlink to be preserved: %v", err)
	}
	if got != external {
		t.Fatalf("external symlink target = %q, want %q", got, external)
	}
}

func TestCopyDirTerminatesOnSymlinkCycle(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")

	if err := os.MkdirAll(filepath.Join(src, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "f.txt"), []byte("data\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..", filepath.Join(src, "sub", "up")); err != nil {
		t.Fatal(err)
	}

	if err := copyDir(src, dst); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dst, "sub", "f.txt")); err != nil {
		t.Fatalf("regular file not copied: %v", err)
	}
	if got, err := os.Readlink(filepath.Join(dst, "sub", "up")); err != nil {
		t.Fatalf("expected cyclic symlink to be preserved: %v", err)
	} else if got != ".." {
		t.Fatalf("cyclic symlink target = %q, want %q", got, "..")
	}
}

func assertNotSymlink(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("%s is still a symlink", path)
	}
}
