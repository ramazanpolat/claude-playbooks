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

func TestWriteOverwritesOwnLauncher(t *testing.T) {
	dir := t.TempDir()
	if _, err := Write(dir, "demo", "demo", "/old", "/pb"); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(dir, "demo", "demo", "/new", "/pb"); err != nil {
		t.Fatal(err)
	}
	entries, _ := List(dir)
	if len(entries) != 1 || entries[0].ConfigDir != "/new" {
		t.Fatalf("entries = %#v", entries)
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
