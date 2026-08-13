package launcher

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func TestWriteAndList(t *testing.T) {
	dir := t.TempDir()
	path, err := Write(dir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("launcher is not a symlink: %v", info.Mode())
	}
	bin, _ := BinPath()
	if resolved, err := filepath.EvalSymlinks(path); err != nil || resolved != bin {
		t.Errorf("resolves to %q (%v), want %q", resolved, err, bin)
	}
	entries, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].CmdName != "demo" {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestWriteRefusesForeignFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "taken"), []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(dir, "taken"); err == nil {
		t.Fatal("expected ErrTaken for a regular file")
	}
	// A symlink to something else is just as foreign.
	if err := os.Symlink("/bin/sh", filepath.Join(dir, "shlink")); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(dir, "shlink"); err == nil {
		t.Fatal("expected ErrTaken for a foreign symlink")
	}
}

func TestWriteRefreshesOwnLink(t *testing.T) {
	dir := t.TempDir()
	if _, err := Write(dir, "demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(dir, "demo"); err != nil {
		t.Fatalf("refresh of own link failed: %v", err)
	}
	entries, _ := List(dir)
	if len(entries) != 1 {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestWriteRejectsBadAndReservedNames(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"", ".", "..", "a/b", "cpb", "claude-playbook"} {
		if _, err := Write(dir, name); err == nil {
			t.Errorf("name %q accepted", name)
		}
	}
}

func TestLookupAndRemove(t *testing.T) {
	dir := t.TempDir()
	if _, exists, _ := Lookup(dir, "none"); exists {
		t.Fatal("phantom entry")
	}
	if err := os.WriteFile(filepath.Join(dir, "foreign"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, foreign := Lookup(dir, "foreign"); !foreign {
		t.Fatal("foreign file not flagged")
	}
	if _, err := Write(dir, "ours"); err != nil {
		t.Fatal(err)
	}
	if e, exists, foreign := Lookup(dir, "ours"); !exists || foreign || e.CmdName != "ours" {
		t.Fatalf("e=%#v exists=%v foreign=%v", e, exists, foreign)
	}

	// Remove touches launchers only.
	if removed, err := Remove(dir, "foreign"); err != nil || removed {
		t.Fatalf("foreign removed=%v err=%v", removed, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "foreign")); err != nil {
		t.Fatal("foreign file was deleted")
	}
	if removed, err := Remove(dir, "ours"); err != nil || !removed {
		t.Fatalf("ours removed=%v err=%v", removed, err)
	}
	if entries, _ := List(dir); len(entries) != 0 {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestListIgnoresForeignEntries(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "regular"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/bin/sh", filepath.Join(dir, "shlink")); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(dir, "mine"); err != nil {
		t.Fatal(err)
	}
	entries, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].CmdName != "mine" {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestConcurrentWritesSameName(t *testing.T) {
	// All writers produce an identical link, so concurrency must never
	// error or corrupt: every call succeeds and exactly one link remains.
	dir := t.TempDir()
	const n = 16
	var wins int32
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := Write(dir, "shared"); err == nil {
				atomic.AddInt32(&wins, 1)
			}
		}()
	}
	wg.Wait()
	if wins != n {
		t.Fatalf("wins = %d, want %d", wins, n)
	}
	entries, _ := List(dir)
	if len(entries) != 1 {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestRemoveNeverTouchesReservedNames(t *testing.T) {
	dir := t.TempDir()
	// The installer's own cpb shortcut also resolves to this binary.
	bin, _ := BinPath()
	if err := os.Symlink(bin, filepath.Join(dir, "cpb")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"cpb", "claude-playbook", "../cpb"} {
		if removed, err := Remove(dir, name); err != nil || removed {
			t.Errorf("Remove(%q) = %v, %v", name, removed, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(dir, "cpb")); err != nil {
		t.Fatal("the CLI shortcut was deleted")
	}
}
