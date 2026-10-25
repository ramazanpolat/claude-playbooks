package auth

import (
	"os"
	"path/filepath"
	"testing"
)

// writeNestedState writes ~/.claude/.claude.json, the state Claude Code keeps
// when it runs with CLAUDE_CONFIG_DIR=~/.claude (cpb start ~/.claude); a raw
// claude keeps ~/.claude.json instead.
func writeNestedState(t *testing.T, home, uuid string) {
	t.Helper()
	data := `{"hasCompletedOnboarding":true,"oauthAccount":{"accountUuid":"` + uuid + `"}}`
	if err := os.WriteFile(filepath.Join(home, ".claude", StateFileName), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A stale nested state file naming an old account must not make that
// account's grant look like the machine's: the machine store is never
// overwritten with it.
func TestStaleNestedStateNeverPromotesAnotherAccount(t *testing.T) {
	f := newSyncFixture(t, "machine-acct", "own-acct")
	writeNestedState(t, f.home, "own-acct")
	if err := SyncCredentials(f.target); err != nil {
		t.Fatal(err)
	}
	f.assertSetAside(t, "cannot be confirmed")
}

// Both machine state files naming one account: the machine's account is
// confirmed and a newer grant of it is promoted, as before.
func TestAgreeingMachineStatesPromote(t *testing.T) {
	f := newSyncFixture(t, "acct", "acct")
	writeNestedState(t, f.home, "acct")
	if err := SyncCredentials(f.target); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(f.global); string(data) != ownGrant {
		t.Fatalf("the newer grant of the machine's account was not promoted: %s", data)
	}
}

// A stale ~/.claude.json against a current nested file: still disagreement,
// still unconfirmed. Neither direction promotes.
func TestStaleCanonicalStateNeverPromotes(t *testing.T) {
	f := newSyncFixture(t, "old-acct", "own-acct")
	writeNestedState(t, f.home, "own-acct")
	if err := SyncCredentials(f.target); err != nil {
		t.Fatal(err)
	}
	f.assertSetAside(t, "cannot be confirmed")
}

// ~/.claude.json decides whenever it exists: a malformed one is an error,
// never a reason to read the nested file; the nested file is read only
// when ~/.claude.json is absent.
func TestMachineStateFallsBackOnlyOnAbsence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeNestedState(t, home, "nested-acct")
	target := t.TempDir()

	state, err := findAccountState(target)
	if err != nil || machineAccountUUID(target) != "nested-acct" || state == nil {
		t.Fatalf("absent ~/.claude.json: state=%v err=%v", state, err)
	}
	if err := os.WriteFile(filepath.Join(home, StateFileName), []byte(`{broken`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := findAccountState(target); err == nil {
		t.Fatal("a malformed ~/.claude.json fell back to the nested file")
	}
	if got := machineAccountUUID(target); got != "" {
		t.Fatalf("machine account = %q under a malformed ~/.claude.json, want unknown", got)
	}
}
