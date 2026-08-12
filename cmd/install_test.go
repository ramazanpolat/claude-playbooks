package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/launcher"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
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

func TestCopyDirDereferencesRootSymlink(t *testing.T) {
	root := t.TempDir()
	realSource := filepath.Join(root, "real")
	if err := os.Mkdir(realSource, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realSource, "CLAUDE.md"), []byte("# copied\n"), 0644); err != nil {
		t.Fatal(err)
	}
	sourceLink := filepath.Join(root, "source-link")
	if err := os.Symlink(realSource, sourceLink); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "dest")
	if err := copyDir(sourceLink, dest); err != nil {
		t.Fatal(err)
	}
	assertNotSymlink(t, dest)
	if _, err := os.Stat(filepath.Join(dest, "CLAUDE.md")); err != nil {
		t.Fatal(err)
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

func TestInstallRejectsEscapingSubdir(t *testing.T) {
	resetCommandTestState(t)
	root := t.TempDir()
	source := filepath.Join(root, "source")
	sibling := filepath.Join(root, "sibling")
	if err := os.MkdirAll(source, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sibling, 0755); err != nil {
		t.Fatal(err)
	}
	config.PlaybooksDir = filepath.Join(root, "playbooks")
	installSubdir = "../sibling"
	installNoAlias = true
	if err := runInstall(nil, []string{source}); err == nil {
		t.Fatal("expected escaping --subdir to be rejected")
	}
	entries, err := os.ReadDir(config.PlaybooksDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("install created output for escaping subdir: entries=%v err=%v", entries, err)
	}
}

func TestDeleteRejectsParentSegment(t *testing.T) {
	resetCommandTestState(t)
	root := t.TempDir()
	parent := filepath.Join(root, "parent")
	config.PlaybooksDir = filepath.Join(parent, "playbooks")
	config.ShellConfig = filepath.Join(root, "shellrc")
	if err := os.MkdirAll(config.PlaybooksDir, 0755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(parent, "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	deleteYes = true
	if err := runDelete(nil, []string{".."}); err == nil {
		t.Fatal("expected parent segment to be rejected")
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("parent contents were removed: %v", err)
	}
}

func TestSplitTreePathChoosesLongestRemoteRef(t *testing.T) {
	ref, subdir, ok := splitTreePathByRefs("feature/foo/playbooks/dba", []string{"main", "feature", "feature/foo"})
	if !ok || ref != "feature/foo" || subdir != "playbooks/dba" {
		t.Fatalf("ref=%q subdir=%q ok=%v", ref, subdir, ok)
	}
}

func TestGitInstallPreservesCustomUpdateScript(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	resetCommandTestState(t)
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0755); err != nil {
		t.Fatal(err)
	}
	manifestData := "name = \"custom-update\"\nisolate_auth = true\n[source]\nupdate_script = \"custom-update.sh\"\n"
	if err := os.WriteFile(filepath.Join(repo, ".playbook"), []byte(manifestData), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "custom-update.sh"), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "initial"}} {
		c := exec.Command("git", args...)
		c.Dir = repo
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	config.PlaybooksDir = filepath.Join(root, "playbooks")
	config.ShellConfig = filepath.Join(root, "shellrc")
	installNoAlias = true
	if err := runInstall(nil, []string{"file://" + repo}); err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Read(filepath.Join(config.PlaybooksDir, "custom-update"))
	if err != nil {
		t.Fatal(err)
	}
	if m == nil || m.Source == nil || m.Source.UpdateScript != "custom-update.sh" {
		t.Fatalf("source metadata=%#v", m)
	}
}

func TestLinkManifestSubdirUsesConfigPath(t *testing.T) {
	resetCommandTestState(t)
	t.Setenv("CLAUDE_PLAYBOOKS_ISOLATE_AUTH", "true")
	root := t.TempDir()
	config.PlaybooksDir = filepath.Join(root, "playbooks")
	config.ShellConfig = filepath.Join(root, "shellrc")
	target := filepath.Join(root, "target")
	configDir := filepath.Join(target, "config")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, ".playbook"), []byte("subdir = \"config\"\nisolate_auth = true\n"), 0644); err != nil {
		t.Fatal(err)
	}
	linkName = "linked"
	linkAlias = "linkedalias"
	if err := runLink(nil, []string{target}); err != nil {
		t.Fatal(err)
	}
	pb, err := playbook.Require(config.PlaybooksDir, config.ShellConfig, "linked")
	if err != nil {
		t.Fatal(err)
	}
	if pb.Path != filepath.Join(config.PlaybooksDir, "linked", "config") {
		t.Fatalf("linked playbook path=%q", pb.Path)
	}
	entries, err := launcher.List(config.LauncherDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].CmdName != "linkedalias" {
		t.Fatalf("launcher entries = %#v", entries)
	}
	// The alias must be resolvable at invocation time via the manifest.
	if pb.Manifest == nil || pb.Manifest.Alias != "linkedalias" {
		t.Fatalf("manifest alias not recorded: %#v", pb.Manifest)
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
	m, err := manifest.Read(filepath.Join(config.PlaybooksDir, "new"))
	if err != nil || m == nil {
		t.Fatalf("new root missing manifest: m=%#v err=%v", m, err)
	}
	if m.Name != "new" {
		t.Fatalf("manifest name = %q, want \"new\"", m.Name)
	}
	if m.Subdir != "config" {
		t.Fatalf("manifest subdir = %q, want \"config\"", m.Subdir)
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

func TestInstallRewritesManifestNameToInstallName(t *testing.T) {
	resetCommandTestState(t)
	home := t.TempDir()
	config.PlaybooksDir = filepath.Join(home, "playbooks")
	config.ShellConfig = filepath.Join(home, ".zshrc")
	src := testPlaybookSource(t, "kommander")

	installName = "kommander-dev"
	installNoAlias = true
	if err := runInstall(nil, []string{src}); err != nil {
		t.Fatal(err)
	}

	m, err := manifest.Read(filepath.Join(config.PlaybooksDir, "kommander-dev"))
	if err != nil || m == nil {
		t.Fatalf("installed manifest: m=%#v err=%v", m, err)
	}
	if m.Name != "kommander-dev" {
		t.Fatalf("manifest name = %q, want \"kommander-dev\"", m.Name)
	}
}

func TestRenameLinkedPlaybookKeepsExternalManifest(t *testing.T) {
	resetCommandTestState(t)
	home := t.TempDir()
	config.PlaybooksDir = filepath.Join(home, "playbooks")
	config.ShellConfig = filepath.Join(home, ".zshrc")
	external := filepath.Join(home, "external")
	if err := os.MkdirAll(external, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(external, ".playbook"), []byte("version = \"0.1.0\"\nname = \"orig\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(config.PlaybooksDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(config.PlaybooksDir, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.ShellConfig, nil, 0644); err != nil {
		t.Fatal(err)
	}

	if err := runRename(nil, []string{"linked", "moved"}); err != nil {
		t.Fatal(err)
	}

	newPath := filepath.Join(config.PlaybooksDir, "moved")
	if info, err := os.Lstat(newPath); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("renamed playbook is not a symlink: info=%v err=%v", info, err)
	}
	m, err := manifest.Read(external)
	if err != nil || m == nil || m.Name != "orig" {
		t.Fatalf("external manifest was modified: m=%#v err=%v", m, err)
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
	config.LauncherDir = t.TempDir()
	installName = ""
	installSubdir = ""
	installBranch = ""
	installAlias = ""
	installNoAlias = false
	renameAlias = ""
	renameNoAlias = false
	selfUninstallYes = false
	selfUninstallKeepData = false
	selfUninstallKeepBinary = false
	selfUninstallDryRun = false
	deleteYes = false
	linkName = ""
	linkAlias = ""
	linkNoAlias = false
	t.Cleanup(func() {
		config.PlaybooksDir = ""
		config.ShellConfig = ""
		config.LauncherDir = ""
		installName = ""
		installSubdir = ""
		installBranch = ""
		installAlias = ""
		installNoAlias = false
		renameAlias = ""
		renameNoAlias = false
		selfUninstallYes = false
		selfUninstallKeepData = false
		selfUninstallKeepBinary = false
		selfUninstallDryRun = false
		deleteYes = false
		linkName = ""
		linkAlias = ""
		linkNoAlias = false
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

func TestInstallFlattensSubdirFromManifest(t *testing.T) {
	resetCommandTestState(t)
	home := t.TempDir()
	config.PlaybooksDir = filepath.Join(home, "playbooks")
	config.ShellConfig = filepath.Join(home, ".zshrc")

	// Create source directory
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "playbook"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, ".playbook"), []byte("version = \"0.1.0\"\nname = \"flatpb\"\nsubdir = \"playbook\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "playbook", "CLAUDE.md"), []byte("# flat CLAUDE\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := runInstall(nil, []string{src}); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(config.PlaybooksDir, "flatpb")
	// The playbook files should be flat under dest
	if _, err := os.Stat(filepath.Join(dest, "CLAUDE.md")); err != nil {
		t.Fatalf("expected CLAUDE.md flat under dest: %v", err)
	}
	// The extra "playbook" folder should NOT exist under dest
	if _, err := os.Stat(filepath.Join(dest, "playbook")); !os.IsNotExist(err) {
		t.Fatalf("expected extra 'playbook' directory to not exist under dest")
	}

	// Manifest should exist flat and have subdir cleared
	data, err := os.ReadFile(filepath.Join(dest, ".playbook"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "subdir") {
		t.Fatalf("expected subdir field to be removed from manifest, got: %s", string(data))
	}
}

func TestRenameAliasCollisionPreflightLeavesStateUntouched(t *testing.T) {
	resetCommandTestState(t)
	home := t.TempDir()
	config.PlaybooksDir = filepath.Join(home, "playbooks")
	config.ShellConfig = filepath.Join(home, ".zshrc")
	for _, name := range []string{"aaa", "bbb"} {
		if err := os.MkdirAll(filepath.Join(config.PlaybooksDir, name), 0755); err != nil {
			t.Fatal(err)
		}
	}
	// Command name "x" belongs to bbb via its manifest alias.
	if err := manifest.Write(filepath.Join(config.PlaybooksDir, "bbb"),
		&manifest.Manifest{Version: "0.1.0", Name: "bbb", Alias: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := launcher.Write(config.LauncherDir, "x"); err != nil {
		t.Fatal(err)
	}

	renameAlias = "x"
	err := runRename(nil, []string{"aaa", "ccc"})
	if err == nil {
		t.Fatal("expected collision error")
	}
	// The collision must abort BEFORE any mutation: aaa still present,
	// ccc absent, launcher x untouched.
	if _, serr := os.Stat(filepath.Join(config.PlaybooksDir, "aaa")); serr != nil {
		t.Errorf("aaa was renamed despite the error: %v", serr)
	}
	if _, serr := os.Stat(filepath.Join(config.PlaybooksDir, "ccc")); serr == nil {
		t.Error("ccc exists despite the error")
	}
	if _, exists, foreign := launcher.Lookup(config.LauncherDir, "x"); !exists || foreign {
		t.Errorf("launcher x mutated: exists=%v foreign=%v", exists, foreign)
	}
}
