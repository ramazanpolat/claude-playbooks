package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// sameAccountState writes the machine's and a playbook's account records
// (an accountUuid each; "" writes none).
func sameAccountState(t *testing.T, home, target, machine, own string) {
	t.Helper()
	write := func(path, uuid string) {
		if uuid == "" {
			return
		}
		data := `{"hasCompletedOnboarding":true,"userID":"u-` + uuid + `","oauthAccount":{"accountUuid":"` + uuid + `"},"theme":"dark"}`
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(home, StateFileName), machine)
	write(filepath.Join(target, StateFileName), own)
}

type syncFixture struct {
	home, target, global, local string
	notices                     []string
}

const (
	machineGrant = `{"claudeAiOauth":{"accessToken":"MACHINE"}}`
	ownGrant     = `{"claudeAiOauth":{"accessToken":"OWN"}}`
)

// newSyncFixture: a machine store an hour old, and a shared playbook whose
// store is a newer file of its own (what a refresh or /login leaves, since
// Claude Code writes by rename).
func newSyncFixture(t *testing.T, machineAccount, ownAccount string) *syncFixture {
	t.Helper()
	f := &syncFixture{home: t.TempDir()}
	t.Setenv("HOME", f.home)
	t.Setenv(IsolatedLoginEnv, "")
	if err := os.MkdirAll(filepath.Join(f.home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.global = filepath.Join(f.home, ".claude", CredentialsFileName)
	if err := os.WriteFile(f.global, []byte(machineGrant), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(f.global, old, old); err != nil {
		t.Fatal(err)
	}
	f.target = filepath.Join(t.TempDir(), "playbook")
	if err := os.Mkdir(f.target, 0o755); err != nil {
		t.Fatal(err)
	}
	f.local = filepath.Join(f.target, CredentialsFileName)
	if err := os.WriteFile(f.local, []byte(ownGrant), 0o600); err != nil {
		t.Fatal(err)
	}
	sameAccountState(t, f.home, f.target, machineAccount, ownAccount)
	oldNotice, oldNow := Notice, now
	Notice = func(m string) { f.notices = append(f.notices, m) }
	now = func() time.Time { return time.Date(2026, 9, 27, 16, 0, 0, 0, time.Local) }
	t.Cleanup(func() { Notice, now = oldNotice, oldNow })
	return f
}

// assertSetAside: the machine store is byte-identical, the playbook links
// it, its own login is kept aside, its account record left with a backup,
// and one notice names neither account nor any value.
func (f *syncFixture) assertSetAside(t *testing.T, why string) {
	t.Helper()
	if data, _ := os.ReadFile(f.global); string(data) != machineGrant {
		t.Fatalf("the machine store changed: %s", data)
	}
	if target, err := os.Readlink(f.local); err != nil || target != f.global {
		t.Fatalf("the playbook's store is %q (%v), want a link to the machine's", target, err)
	}
	kept := f.local + ".cpb-own-2026-09-27-16_00_00"
	if data, _ := os.ReadFile(kept); string(data) != ownGrant {
		t.Fatalf("the playbook's own login was not kept aside: %q", data)
	}
	state, _ := os.ReadFile(filepath.Join(f.target, StateFileName))
	if strings.Contains(string(state), "own-acct") {
		t.Fatalf("the set-aside account's state stayed: %s", state)
	}
	if len(f.notices) != 1 || !strings.Contains(f.notices[0], why) || !strings.Contains(f.notices[0], ".cpb-own-2026-09-27-16_00_00") {
		t.Fatalf("notices: %q", f.notices)
	}
	for _, s := range []string{"MACHINE", "OWN", "machine-acct", "own-acct"} {
		if strings.Contains(f.notices[0], s) {
			t.Fatalf("the notice carries %q: %s", s, f.notices[0])
		}
	}
}

func TestSyncSetsAsideAnotherAccountsLogin(t *testing.T) {
	f := newSyncFixture(t, "machine-acct", "own-acct")
	if err := SyncCredentials(f.target); err != nil {
		t.Fatal(err)
	}
	f.assertSetAside(t, "another account's login")
	if _, err := os.Stat(filepath.Join(f.target, StateFileName+".cpb-backup-2026-09-27-16_00_00")); err != nil {
		t.Fatalf("no backup of the playbook's account state: %v", err)
	}
}

func TestSyncSetsAsideAnUnconfirmedLogin(t *testing.T) {
	for _, c := range []struct{ name, machine, own string }{
		{"the playbook records no account", "machine-acct", ""},
		{"the machine records no account", "", "own-acct"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newSyncFixture(t, c.machine, c.own)
			if err := SyncCredentials(f.target); err != nil {
				t.Fatal(err)
			}
			f.assertSetAside(t, "cannot be confirmed as the machine's account")
		})
	}
}

// The same account: a refresh inside the playbook reaches the machine's
// store, as before (the heal), and nothing is set aside.
func TestSyncHealsTheSameAccount(t *testing.T) {
	f := newSyncFixture(t, "acct", "acct")
	if err := SyncCredentials(f.target); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(f.global); string(data) != ownGrant {
		t.Fatalf("the same account's newer login did not reach the machine store: %s", data)
	}
	if aside, _ := filepath.Glob(f.local + ".cpb-own-*"); len(aside) != 0 || len(f.notices) != 0 {
		t.Fatalf("set aside %v, notices %q", aside, f.notices)
	}
}

// No machine store at all: there is nothing to link or replace, so the
// playbook's own login stays where it is, as before, and nothing is copied.
func TestSyncLeavesALoginWhenTheMachineHasNone(t *testing.T) {
	f := newSyncFixture(t, "", "own-acct")
	if err := os.Remove(f.global); err != nil {
		t.Fatal(err)
	}
	if err := SyncCredentials(f.target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.global); !os.IsNotExist(err) {
		t.Fatalf("a machine store appeared: %v", err)
	}
	if data, _ := os.ReadFile(f.local); string(data) != ownGrant {
		t.Fatalf("the playbook's own login changed: %q", data)
	}
	if len(f.notices) != 0 {
		t.Fatalf("notices: %q", f.notices)
	}
}
