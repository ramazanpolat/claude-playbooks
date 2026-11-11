package auth

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The shared login store is written only when its value changes, never in
// place, and never with an older login. Every value here is made up; the
// Keychain is the findGenericPassword stub and the OS the hostOS seam, so
// the macOS branch runs on every OS and no test reaches a real Keychain.

// fakeLogin is a made-up store value whose grant expires at exp (0: none).
func fakeLogin(name string, exp int64) string {
	if exp == 0 {
		return `{"claudeAiOauth":{"accessToken":"fake-access-` + name + `","refreshToken":"fake-refresh-` + name + `"}}`
	}
	return fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"fake-access-%s","refreshToken":"fake-refresh-%s","expiresAt":%d}}`, name, name, exp)
}

type storeFixture struct {
	home, global, target, local string
	notices                     []string
	keychainCalls               []string
}

// newStoreFixture: a scratch HOME on macOS (by the seam) whose Keychain item
// holds keychain ("" for no item), a machine store holding machine ("" for
// none) with an old file time, and a shared playbook of the machine's own
// account with no store yet.
func newStoreFixture(t *testing.T, keychain, machine string) *storeFixture {
	t.Helper()
	f := &storeFixture{home: t.TempDir()}
	t.Setenv("HOME", f.home)
	t.Setenv(IsolatedLoginEnv, "")
	if err := os.MkdirAll(filepath.Join(f.home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.global = filepath.Join(f.home, ".claude", CredentialsFileName)
	if machine != "" {
		writeOld(t, f.global, machine)
	}
	f.target = filepath.Join(t.TempDir(), "playbook")
	if err := os.Mkdir(f.target, 0o755); err != nil {
		t.Fatal(err)
	}
	f.local = filepath.Join(f.target, CredentialsFileName)
	sameAccountState(t, f.home, f.target, "acct", "acct")

	oldFind, oldOS, oldNotice, oldNow := findGenericPassword, hostOS, Notice, now
	hostOS = "darwin"
	findGenericPassword = func(service string) ([]byte, error) {
		f.keychainCalls = append(f.keychainCalls, service)
		if keychain == "" {
			return nil, errors.New("no such item")
		}
		return []byte(keychain + "\n"), nil
	}
	Notice = func(m string) { f.notices = append(f.notices, m) }
	now = func() time.Time { return time.Date(2026, 9, 27, 16, 0, 0, 0, time.Local) }
	t.Cleanup(func() { findGenericPassword, hostOS, Notice, now = oldFind, oldOS, oldNotice, oldNow })
	return f
}

// writeOld writes a file and dates it an hour back.
func writeOld(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

// snapshot is a file's bytes, time and identity, to tell "not written" from
// "rewritten with the same bytes" and "replaced by a rename".
type snapshot struct {
	data string
	info os.FileInfo
}

func snap(t *testing.T, path string) snapshot {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot{string(data), info}
}

// untouched: the file at path is the one snapped, with the same bytes and
// the same time.
func (s snapshot) untouched(t *testing.T, path string) {
	t.Helper()
	now := snap(t, path)
	if now.data != s.data {
		t.Fatalf("%s changed: %s", filepath.Base(path), now.data)
	}
	if !now.info.ModTime().Equal(s.info.ModTime()) || !os.SameFile(now.info, s.info) {
		t.Fatalf("%s was written (time %v -> %v, same file %v)", filepath.Base(path), s.info.ModTime(), now.info.ModTime(), os.SameFile(now.info, s.info))
	}
}

// replacedWith: the file at path holds want and is a new file renamed into
// place (a truncate in place keeps the old file's identity), mode 0600.
func (s snapshot) replacedWith(t *testing.T, path, want string) {
	t.Helper()
	now := snap(t, path)
	if now.data != want {
		t.Fatalf("%s holds %s, want %s", filepath.Base(path), now.data, want)
	}
	if os.SameFile(now.info, s.info) {
		t.Fatalf("%s was rewritten in place, not replaced by a rename", filepath.Base(path))
	}
	if now.info.Mode().Perm() != 0o600 {
		t.Fatalf("%s has mode %v", filepath.Base(path), now.info.Mode().Perm())
	}
}

// noTempFiles: nothing a write left behind next to the store.
func noTempFiles(t *testing.T, dir string) {
	t.Helper()
	left, _ := filepath.Glob(filepath.Join(dir, ".credentials-*.tmp"))
	if len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}

// linked: the playbook's store is a link to the machine store.
func (f *storeFixture) linked(t *testing.T) {
	t.Helper()
	if got, err := os.Readlink(f.local); err != nil || got != f.global {
		t.Fatalf("the playbook's store is %q (%v), want a link to the machine store", got, err)
	}
}

// oneUndecidedNotice: exactly one notice, saying it could not tell, naming
// the sources and carrying no value.
func (f *storeFixture) oneUndecidedNotice(t *testing.T, sources ...string) {
	t.Helper()
	if len(f.notices) != 1 || !strings.Contains(f.notices[0], "cannot tell which login is newest") {
		t.Fatalf("notices: %q", f.notices)
	}
	for _, s := range sources {
		if !strings.Contains(f.notices[0], s) {
			t.Fatalf("the notice does not name %s: %s", s, f.notices[0])
		}
	}
	for _, s := range []string{"fake-", "accessToken", "refreshToken"} {
		if strings.Contains(f.notices[0], s) {
			t.Fatalf("the notice carries %q: %s", s, f.notices[0])
		}
	}
}

func TestSyncIdenticalKeychainValueWritesNothing(t *testing.T) {
	value := fakeLogin("m", 2000)
	f := newStoreFixture(t, value, value)
	before := snap(t, f.global)

	if err := SyncCredentials(f.target); err != nil {
		t.Fatal(err)
	}
	before.untouched(t, f.global)
	f.linked(t)
	if len(f.notices) != 0 {
		t.Fatalf("notices: %q", f.notices)
	}
	// Read, and only the machine's item: there is no Keychain write.
	if len(f.keychainCalls) != 1 || f.keychainCalls[0] != machineKeychainService {
		t.Fatalf("Keychain calls: %q", f.keychainCalls)
	}
}

func TestSyncNewerKeychainValueReplacesTheStoreAtomically(t *testing.T) {
	f := newStoreFixture(t, fakeLogin("k", 3000), fakeLogin("m", 2000))
	before := snap(t, f.global)

	path, err := EnsureGlobalCredentials()
	if err != nil || path != f.global {
		t.Fatalf("path %q, err %v", path, err)
	}
	before.replacedWith(t, f.global, fakeLogin("k", 3000))
	noTempFiles(t, filepath.Dir(f.global))
	if len(f.notices) != 0 {
		t.Fatalf("notices: %q", f.notices)
	}
}

func TestSyncKeychainValueMaterialisesAMissingStore(t *testing.T) {
	f := newStoreFixture(t, fakeLogin("k", 3000), "")
	if err := SyncCredentials(f.target); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(f.global); string(data) != fakeLogin("k", 3000) {
		t.Fatalf("the machine store holds %s", data)
	}
	if info, _ := os.Stat(f.global); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode().Perm())
	}
	f.linked(t)
}

// Never a newer login replaced by an older one: a Keychain item older than
// the store (the store took a newer login elsewhere) leaves the store alone.
func TestSyncOlderKeychainValueLeavesTheStore(t *testing.T) {
	f := newStoreFixture(t, fakeLogin("k", 1000), fakeLogin("m", 2000))
	before := snap(t, f.global)
	if err := SyncCredentials(f.target); err != nil {
		t.Fatal(err)
	}
	before.untouched(t, f.global)
	f.linked(t)
	if len(f.notices) != 0 {
		t.Fatalf("notices: %q", f.notices)
	}
}

func TestSyncInvalidKeychainValueIsAnErrorAndWritesNothing(t *testing.T) {
	f := newStoreFixture(t, "not json", fakeLogin("m", 2000))
	before := snap(t, f.global)
	err := SyncCredentials(f.target)
	if err == nil || !strings.Contains(err.Error(), "did not contain valid JSON") {
		t.Fatalf("err = %v", err)
	}
	before.untouched(t, f.global)
	if _, err := os.Lstat(f.local); !os.IsNotExist(err) {
		t.Fatalf("the playbook was linked anyway: %v", err)
	}
}

// The playbook's newer login wins over both the Keychain and the store, is
// written once, by rename, and the playbook is then linked. The file is the
// OLDEST of the three: the grant's expiry decides. Before, the Keychain value
// was written first, its new file time beat the playbook's, and the
// playbook's newer login was replaced by the link.
func TestSyncNewerPlaybookStoreWinsAndIsLinked(t *testing.T) {
	f := newStoreFixture(t, fakeLogin("k", 2000), fakeLogin("m", 1000))
	writeOld(t, f.local, fakeLogin("own", 3000))
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(f.local, old, old); err != nil {
		t.Fatal(err)
	}
	before := snap(t, f.global)

	if err := SyncCredentials(f.target); err != nil {
		t.Fatal(err)
	}
	before.replacedWith(t, f.global, fakeLogin("own", 3000))
	f.linked(t)
	noTempFiles(t, filepath.Dir(f.global))
	if aside, _ := filepath.Glob(f.local + ".cpb-own-*"); len(aside) != 0 || len(f.notices) != 0 {
		t.Fatalf("set aside %v, notices %q", aside, f.notices)
	}
}

// An older playbook login is not copied, whatever its file time: the
// playbook is linked as before, to the newest login.
func TestSyncOlderPlaybookStoreIsLinkedNotCopied(t *testing.T) {
	t.Run("the Keychain is newest", func(t *testing.T) {
		f := newStoreFixture(t, fakeLogin("k", 3000), fakeLogin("m", 1000))
		writeOld(t, f.local, fakeLogin("own", 2000))
		before := snap(t, f.global)
		if err := SyncCredentials(f.target); err != nil {
			t.Fatal(err)
		}
		before.replacedWith(t, f.global, fakeLogin("k", 3000))
		f.linked(t)
	})
	t.Run("the store is newest, the playbook's file is newer", func(t *testing.T) {
		f := newStoreFixture(t, "", fakeLogin("m", 3000))
		hostOS = "linux"
		if err := os.WriteFile(f.local, []byte(fakeLogin("own", 2000)), 0o600); err != nil {
			t.Fatal(err)
		}
		before := snap(t, f.global)
		if err := SyncCredentials(f.target); err != nil {
			t.Fatal(err)
		}
		before.untouched(t, f.global)
		f.linked(t)
		if len(f.notices) != 0 {
			t.Fatalf("notices: %q", f.notices)
		}
	})
}

// When no value can be told to be the newest, nothing is written, the
// playbook's own file stays, and one line says which sources and why.
func TestSyncUndecidableChangesNothing(t *testing.T) {
	for _, c := range []struct {
		name, keychain, machine, own string
		sources                      []string // beyond the machine store
		linked                       bool     // a playbook with no file is still linked
	}{
		{"the Keychain value has no expiry", fakeLogin("k", 0), fakeLogin("m", 2000), "", []string{"Keychain"}, true},
		{"the store has no expiry", fakeLogin("k", 3000), fakeLogin("m", 0), "", []string{"Keychain"}, true},
		{"same expiry, different values", fakeLogin("k", 2000), fakeLogin("m", 2000), "", []string{"Keychain", "expire at the same time"}, true},
		{"the store does not parse", fakeLogin("k", 2000), `{"claudeAiOauth":`, "", []string{"Keychain"}, false},
		{"the playbook's login has no expiry", "", fakeLogin("m", 2000), fakeLogin("own", 0), []string{"playbook"}, false},
		{"the playbook ties the Keychain", fakeLogin("k", 3000), fakeLogin("m", 1000), fakeLogin("own", 3000), []string{"Keychain", "playbook"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newStoreFixture(t, c.keychain, c.machine)
			var own snapshot
			if c.own != "" {
				writeOld(t, f.local, c.own)
				own = snap(t, f.local)
			}
			before := snap(t, f.global)

			state := snap(t, filepath.Join(f.target, StateFileName))

			if err := SyncCredentials(f.target); err != nil {
				t.Fatal(err)
			}
			before.untouched(t, f.global)
			noTempFiles(t, filepath.Dir(f.global))
			if !c.linked {
				// Nothing linked, and no account metadata seeded either.
				state.untouched(t, filepath.Join(f.target, StateFileName))
			}
			if c.own != "" {
				own.untouched(t, f.local)
				if info, _ := os.Lstat(f.local); !info.Mode().IsRegular() {
					t.Fatal("the playbook's own login was replaced")
				}
			} else if c.linked {
				f.linked(t)
			} else if _, err := os.Lstat(f.local); !os.IsNotExist(err) {
				t.Fatalf("the playbook was linked to a store that does not parse: %v", err)
			}
			f.oneUndecidedNotice(t, append([]string{f.global}, c.sources...)...)
		})
	}
}

// A playbook login of an unconfirmed account (its state names none) stays
// unjudged when the machine store cannot be ordered against the Keychain:
// nothing is linked over it, and the machine's account is not seeded into
// its state, which would let it pass for the machine's own at the next sync.
func TestSyncUndecidableSeedsNoAccountMetadata(t *testing.T) {
	f := newStoreFixture(t, fakeLogin("k", 2000), `{"claudeAiOauth":`)
	stateFile := filepath.Join(f.target, StateFileName)
	if err := os.WriteFile(stateFile, []byte(`{"theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	writeOld(t, f.local, fakeLogin("own", 3000))
	before, own, state := snap(t, f.global), snap(t, f.local), snap(t, stateFile)

	if err := SyncCredentials(f.target); err != nil {
		t.Fatal(err)
	}
	before.untouched(t, f.global)
	own.untouched(t, f.local)
	state.untouched(t, stateFile)
	if sameAccount(f.target) {
		t.Fatal("the playbook's login now passes for the machine's account")
	}
	f.oneUndecidedNotice(t, f.global, "Keychain")
}

// An interrupted write (the rename fails) leaves the old store byte for byte
// and no temporary file, on the Keychain path and on the copy back.
func TestSyncInterruptedWriteLeavesTheOldStore(t *testing.T) {
	failRename := func(t *testing.T) {
		old := renameFile
		renameFile = func(string, string) error { return errors.New("interrupted") }
		t.Cleanup(func() { renameFile = old })
	}
	t.Run("from the Keychain", func(t *testing.T) {
		f := newStoreFixture(t, fakeLogin("k", 3000), fakeLogin("m", 2000))
		failRename(t)
		before := snap(t, f.global)
		if err := SyncCredentials(f.target); err == nil || !strings.Contains(err.Error(), "interrupted") {
			t.Fatalf("err = %v", err)
		}
		before.untouched(t, f.global)
		noTempFiles(t, filepath.Dir(f.global))
	})
	t.Run("from the playbook", func(t *testing.T) {
		f := newStoreFixture(t, "", fakeLogin("m", 2000))
		hostOS = "linux"
		writeOld(t, f.local, fakeLogin("own", 3000))
		failRename(t)
		before, own := snap(t, f.global), snap(t, f.local)
		if err := LinkCredentials(f.target, f.global); err == nil || !strings.Contains(err.Error(), "interrupted") {
			t.Fatalf("err = %v", err)
		}
		before.untouched(t, f.global)
		own.untouched(t, f.local)
		noTempFiles(t, filepath.Dir(f.global))
	})
}

// A machine store that is a link (to a store kept elsewhere) is written
// through, as before: the file it names is replaced and the link stays.
func TestSyncWritesThroughALinkedStore(t *testing.T) {
	f := newStoreFixture(t, fakeLogin("k", 3000), "")
	real := filepath.Join(f.home, "elsewhere", CredentialsFileName)
	if err := os.MkdirAll(filepath.Dir(real), 0o700); err != nil {
		t.Fatal(err)
	}
	writeOld(t, real, fakeLogin("m", 2000))
	if err := os.Symlink(real, f.global); err != nil {
		t.Fatal(err)
	}
	before := snap(t, real)
	if _, err := EnsureGlobalCredentials(); err != nil {
		t.Fatal(err)
	}
	if got, err := os.Readlink(f.global); err != nil || got != real {
		t.Fatalf("the machine store's link is %q (%v)", got, err)
	}
	before.replacedWith(t, real, fakeLogin("k", 3000))
	noTempFiles(t, filepath.Dir(real))
}

// The token path keeps a same-account grant by the same rule: an older one
// is stripped without touching the machine store, whatever its file time,
// and one that cannot be ordered is kept aside rather than lost.
func TestSharedTokenLaunchOrdersTheGrantByExpiry(t *testing.T) {
	activate := func(t *testing.T) {
		tokenPath := filepath.Join(t.TempDir(), "oauth-token")
		if err := os.WriteFile(tokenPath, []byte("fake-oauth-token\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(oauthTokenFileEnv, tokenPath)
	}
	t.Run("older grant, newer file", func(t *testing.T) {
		f := newSyncFixture(t, "acct", "acct")
		if err := os.WriteFile(f.local, []byte(fakeLogin("own", 1000)), 0o600); err != nil {
			t.Fatal(err)
		}
		activate(t)
		before := snap(t, f.global)
		if _, err := PrepareLaunchEnv(f.target); err != nil {
			t.Fatal(err)
		}
		before.untouched(t, f.global)
		if storeHasGrant(t, f.local) {
			t.Fatal("the token launch kept a stored grant")
		}
		if len(f.notices) != 0 {
			t.Fatalf("notices: %q", f.notices)
		}
	})
	t.Run("cannot be ordered", func(t *testing.T) {
		f := newSyncFixture(t, "acct", "acct")
		if err := os.WriteFile(f.local, []byte(fakeLogin("own", 0)), 0o600); err != nil {
			t.Fatal(err)
		}
		activate(t)
		before := snap(t, f.global)
		if _, err := PrepareLaunchEnv(f.target); err != nil {
			t.Fatal(err)
		}
		before.untouched(t, f.global)
		if data, _ := os.ReadFile(f.local + ".cpb-own-2026-09-27-16_00_00"); string(data) != fakeLogin("own", 0) {
			t.Fatalf("the grant was not kept aside: %q", data)
		}
		if storeHasGrant(t, f.local) {
			t.Fatal("the token launch kept a stored grant")
		}
		if len(f.notices) != 1 || !strings.Contains(f.notices[0], "cannot be told newer or older") || strings.Contains(f.notices[0], "fake-") {
			t.Fatalf("notices: %q", f.notices)
		}
	})
}
