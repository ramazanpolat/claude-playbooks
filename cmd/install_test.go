package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/shell"
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

func TestInstallRejectsPathNameFlag(t *testing.T) {
	resetCommandTestState(t)
	source := testPlaybookSource(t, "safe")
	config.PlaybooksDir = filepath.Join(t.TempDir(), "playbooks")
	installName = "../escape"

	err := runInstall(nil, []string{source})
	if err == nil {
		t.Fatal("expected install to reject path-like --name")
	}
	if !strings.Contains(err.Error(), "top-level playbook name") {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(config.PlaybooksDir, "..", "escape")); !os.IsNotExist(statErr) {
		t.Fatalf("install wrote outside playbooks dir, stat err=%v", statErr)
	}
}

func TestInstallRejectsPathManifestName(t *testing.T) {
	resetCommandTestState(t)
	source := testPlaybookSource(t, "../escape")
	config.PlaybooksDir = filepath.Join(t.TempDir(), "playbooks")

	err := runInstall(nil, []string{source})
	if err == nil {
		t.Fatal("expected install to reject path-like manifest name")
	}
	if !strings.Contains(err.Error(), "top-level playbook name") {
		t.Fatalf("error = %v", err)
	}
}

func TestRenameMovesRootForSubdirManifest(t *testing.T) {
	resetCommandTestState(t)
	home := t.TempDir()
	config.PlaybooksDir = filepath.Join(home, "playbooks")
	config.ShellConfig = filepath.Join(home, ".zshrc")
	root := filepath.Join(config.PlaybooksDir, "old")
	configDir := filepath.Join(root, "config")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".playbook"), []byte("version = \"0.1.0\"\nname = \"old\"\nsubdir = \"config\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "CLAUDE.md"), []byte("# Config\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := shell.Write(config.ShellConfig, "oldalias", configDir); err != nil {
		t.Fatal(err)
	}

	if err := runRename(nil, []string{"old", "new"}); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("old root still exists, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(config.PlaybooksDir, "new", ".playbook")); err != nil {
		t.Fatalf("new root missing manifest: %v", err)
	}
	newConfig := filepath.Join(config.PlaybooksDir, "new", "config")
	if _, err := os.Stat(filepath.Join(newConfig, "CLAUDE.md")); err != nil {
		t.Fatalf("new config missing contents: %v", err)
	}
	entries, err := shell.ReadAll(config.ShellConfig)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Path != newConfig {
		t.Fatalf("alias paths = %#v, want %s", entries, newConfig)
	}
}

func TestSelfUninstallKeepDataPreservesPlaybooks(t *testing.T) {
	resetCommandTestState(t)
	home := t.TempDir()
	config.PlaybooksDir = filepath.Join(home, "playbooks")
	config.ShellConfig = filepath.Join(home, ".zshrc")
	root := filepath.Join(config.PlaybooksDir, "kept")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".playbook"), []byte("version = \"0.1.0\"\nname = \"kept\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := shell.Write(config.ShellConfig, "kept", root); err != nil {
		t.Fatal(err)
	}
	selfUninstallYes = true
	selfUninstallKeepData = true
	selfUninstallKeepBinary = true

	if err := runSelfUninstall(nil, nil); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(root, ".playbook")); err != nil {
		t.Fatalf("playbook data was not preserved: %v", err)
	}
	entries, err := shell.ReadAll(config.ShellConfig)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("aliases were not removed: %#v", entries)
	}
}

func testPlaybookSource(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".playbook"), []byte("version = \"0.1.0\"\nname = "+quoteTOML(name)+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func quoteTOML(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

func resetCommandTestState(t *testing.T) {
	t.Helper()
	config.PlaybooksDir = ""
	config.ShellConfig = ""
	installName = ""
	installSubdir = ""
	installBranch = ""
	installAlias = ""
	installAliasAll = false
	installNoAlias = false
	installInit = false
	renameAlias = ""
	renameNoAlias = false
	selfUninstallYes = false
	selfUninstallKeepData = false
	selfUninstallKeepBinary = false
	selfUninstallDryRun = false
	t.Cleanup(func() {
		config.PlaybooksDir = ""
		config.ShellConfig = ""
		installName = ""
		installSubdir = ""
		installBranch = ""
		installAlias = ""
		installAliasAll = false
		installNoAlias = false
		installInit = false
		renameAlias = ""
		renameNoAlias = false
		selfUninstallYes = false
		selfUninstallKeepData = false
		selfUninstallKeepBinary = false
		selfUninstallDryRun = false
	})
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
