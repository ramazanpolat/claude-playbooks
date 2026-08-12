package launcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteAndParse(t *testing.T) {
	dir := t.TempDir()
	path, err := Write(dir, "demo", "demo", "/home/u/.claude-playbooks/demo", "/pb")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("launcher not executable: %v", info.Mode())
	}
	entries, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %#v", entries)
	}
	e := entries[0]
	if e.CmdName != "demo" || e.PlaybookName != "demo" || e.ConfigDir != "/home/u/.claude-playbooks/demo" {
		t.Errorf("parsed entry = %#v", e)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `CLAUDE_CONFIG_DIR='/home/u/.claude-playbooks/demo' exec `) {
		t.Errorf("script body:\n%s", data)
	}
	if !strings.Contains(string(data), ` run 'demo' "$@"`) {
		t.Errorf("script body:\n%s", data)
	}
}

func TestWriteRefusesForeignFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "taken"), []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(dir, "taken", "pb", "/x", "/pb"); err == nil {
		t.Fatal("expected ErrTaken")
	}
}

func TestWriteRefreshesOwnPlaybookOnly(t *testing.T) {
	dir := t.TempDir()
	if _, err := Write(dir, "demo", "demo", "/pb/demo", "/pb"); err != nil {
		t.Fatal(err)
	}
	// Same playbook: refresh in place.
	if _, err := Write(dir, "demo", "demo", "/pb/demo", "/pb"); err != nil {
		t.Fatal(err)
	}
	// Another playbook wanting the same command must not silently repoint
	// it at a different isolated configuration.
	if _, err := Write(dir, "demo", "other", "/pb/other", "/pb"); err == nil {
		t.Fatal("expected ErrTaken for a launcher owned by another playbook")
	}
	entries, _ := List(dir)
	if len(entries) != 1 || entries[0].ConfigDir != "/pb/demo" {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestWriteAbsolutizesRelativePaths(t *testing.T) {
	dir := t.TempDir()
	work := t.TempDir()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(work); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(orig) })
	// filepath.Abs resolves against Getwd, which may differ from the
	// TempDir string on symlinked temp roots (macOS /var -> /private/var).
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	path, err := Write(dir, "rel", "rel", "./pb/rel", "./pb")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "'./pb") {
		t.Errorf("relative path embedded verbatim:\n%s", data)
	}
	if !strings.Contains(string(data), "--playbooks-dir '"+filepath.Join(wd, "pb")+"'") {
		t.Errorf("script body:\n%s", data)
	}
	entries, _ := List(dir)
	if len(entries) != 1 || entries[0].ConfigDir != filepath.Join(wd, "pb", "rel") {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestListForPathPrefix(t *testing.T) {
	dir := t.TempDir()
	if _, err := Write(dir, "in", "in", "/rootA/in", "/rootA"); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(dir, "out", "out", "/rootB/out", "/rootB"); err != nil {
		t.Fatal(err)
	}
	got, err := ListForPathPrefix(dir, "/rootA")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].CmdName != "in" {
		t.Fatalf("got = %#v", got)
	}
}

func TestRemoveForPathPrefix(t *testing.T) {
	dir := t.TempDir()
	if _, err := Write(dir, "a", "a", "/pb/a", "/pb"); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(dir, "a-sub", "a", "/pb/a/config", "/pb"); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(dir, "b", "b", "/pb/b", "/pb"); err != nil {
		t.Fatal(err)
	}
	// Foreign files are untouched.
	if err := os.WriteFile(filepath.Join(dir, "other"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	removed, err := RemoveForPathPrefix(dir, "/pb/a")
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 2 {
		t.Fatalf("removed = %#v", removed)
	}
	entries, _ := List(dir)
	if len(entries) != 1 || entries[0].CmdName != "b" {
		t.Fatalf("entries = %#v", entries)
	}
	if _, err := os.Stat(filepath.Join(dir, "other")); err != nil {
		t.Errorf("foreign file was touched: %v", err)
	}
	// Prefix must match path components, not string prefixes: /pb/a must
	// not remove /pb/ab.
	if _, err := Write(dir, "ab", "ab", "/pb/ab", "/pb"); err != nil {
		t.Fatal(err)
	}
	if removed, _ := RemoveForPathPrefix(dir, "/pb/a"); len(removed) != 0 {
		t.Fatalf("component-prefix violation: %#v", removed)
	}
}

func TestQuoteInScript(t *testing.T) {
	dir := t.TempDir()
	cfg := "/tmp/o'brien/pb"
	path, err := Write(dir, "q", "o'brien pb", cfg, "/pb")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `CLAUDE_CONFIG_DIR='/tmp/o'\''brien/pb' exec `) {
		t.Errorf("script body:\n%s", data)
	}
	entries, _ := List(dir)
	if len(entries) != 1 || entries[0].ConfigDir != cfg {
		t.Fatalf("round-trip failed: %#v", entries)
	}
}

func TestWriteRejectsBadNames(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"", ".", "..", "a/b"} {
		if _, err := Write(dir, name, "pb", "/x", "/pb"); err == nil {
			t.Errorf("name %q accepted", name)
		}
	}
}
