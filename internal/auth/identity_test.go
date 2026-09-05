package auth

import (
	"os"
	"path/filepath"
	"testing"
)

// A symlink to the global config directory names the same directory; string
// comparison of absolute paths let both mutations through to the global store.
func TestQuarantineRefusesGlobalStoreThroughSymlinkedDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	global := filepath.Join(home, ".claude")
	if err := os.MkdirAll(global, 0o755); err != nil {
		t.Fatal(err)
	}
	store := writeStore(t, global, `{`+grantJSON+`}`)
	alias := filepath.Join(home, "linked-global")
	if err := os.Symlink(global, alias); err != nil {
		t.Fatal(err)
	}

	if err := QuarantineStoredOAuth(alias); err != nil {
		t.Fatalf("quarantine through the alias: %v", err)
	}
	if _, present := readStore(t, store)["claudeAiOauth"]; !present {
		t.Fatal("the GLOBAL grant was quarantined through a symlinked directory")
	}
}

func TestLinkCredentialsRefusesGlobalStoreThroughSymlinkedDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	global := filepath.Join(home, ".claude")
	if err := os.MkdirAll(global, 0o755); err != nil {
		t.Fatal(err)
	}
	store := writeStore(t, global, testCreds)
	alias := filepath.Join(home, "linked-global")
	if err := os.Symlink(global, alias); err != nil {
		t.Fatal(err)
	}

	if err := LinkCredentials(alias, store); err != nil {
		t.Fatalf("link through the alias: %v", err)
	}
	info, err := os.Lstat(store)
	if err != nil {
		t.Fatalf("global store gone: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("the global store was replaced by a symlink to itself")
	}
	if got, _ := os.ReadFile(store); string(got) != testCreds {
		t.Fatalf("global store content changed: %q", got)
	}
}

// The same protection when ~/.claude itself is the symlink and the playbook
// path is the resolved target: identity, not spelling.
func TestSameDirSeesThroughSymlinkedGlobal(t *testing.T) {
	home := t.TempDir()
	real := filepath.Join(home, "real-claude")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(home, ".claude")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	if !isGlobalConfigDir(real) {
		t.Fatal("the resolved target of a symlinked ~/.claude was not recognised as the global dir")
	}
	if isGlobalConfigDir(filepath.Join(home, "elsewhere")) {
		t.Fatal("a non-existent path was taken for the global dir")
	}
}

// The reverse alias: ~/.claude/.credentials.json is a symlink INTO the config
// dir being quarantined. Its regular file is the global store's target.
func TestQuarantineRefusesGlobalStoreTargetInAnotherDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	shared := filepath.Join(home, "configs", "shared")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	store := writeStore(t, shared, `{`+grantJSON+`}`)
	global := filepath.Join(home, ".claude")
	if err := os.MkdirAll(global, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(store, filepath.Join(global, CredentialsFileName)); err != nil {
		t.Fatal(err)
	}

	if err := QuarantineStoredOAuth(shared); err != nil {
		t.Fatalf("quarantine: %v", err)
	}
	if _, present := readStore(t, store)["claudeAiOauth"]; !present {
		t.Fatal("the global store's target was stripped through the directory that holds it")
	}
}
