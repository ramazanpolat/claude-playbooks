package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ramazanpolat/claude-playbooks/internal/account"
	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envset"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// memStore is an in-memory osSecretStore; writes counts every write call.
// With other set, a write reports success and keeps other instead, as when
// another program wrote the same item at the same moment.
type memStore struct {
	mu     sync.Mutex
	items  map[string][]byte
	writes int
	fail   bool
	other  []byte
	// unavailable makes exists fail, as a store that cannot be reached.
	unavailable bool
	// max is maxValue (0: maxSecretBytes).
	max int
	// onExists and onWrite run first in exists and write (a test checks
	// the lock there).
	onExists, onWrite func(name string)
	// readErr, when set, is what read answers.
	readErr error
	// notReady, when set, is what readyToStore answers.
	notReady error
	// lastWant is the store kind the last write was told to keep to.
	lastWant string
	// targetErr and targetWhy are what target answers; targetSeq, when
	// set, the kinds it answers call after call (a store that flaps).
	targetErr   error
	targetWhy   string
	targetSeq   []string
	targetCalls int
}

func (m *memStore) readyToStore(string) error { return m.notReady }

func (m *memStore) maxValue() int {
	if m.max > 0 {
		return m.max
	}
	return maxSecretBytes
}

func (m *memStore) where(name string) string { return "the test store, as " + name }
func (m *memStore) describe(name string, t storeTarget) string {
	return "the test store (" + t.kind + "), as " + name
}
func (m *memStore) target(name string) (storeTarget, error) {
	if m.targetErr != nil {
		return storeTarget{}, m.targetErr
	}
	m.targetCalls++
	if len(m.targetSeq) > 0 {
		k := m.targetSeq[min(m.targetCalls, len(m.targetSeq))-1]
		return storeTarget{kind: k, path: "/test-store/" + name}, nil
	}
	return storeTarget{kind: storeFile, path: "/test-store/" + name, why: m.targetWhy}, nil
}
func (m *memStore) exists(name string) (bool, error) {
	if m.onExists != nil {
		m.onExists(name)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.unavailable {
		return false, errors.New("the test store cannot be reached")
	}
	_, ok := m.items[name]
	return ok, nil
}
func (m *memStore) read(name string) ([]byte, error) {
	if m.readErr != nil {
		return nil, m.readErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.items[name]
	if !ok {
		return nil, errors.New("not found")
	}
	return append([]byte{}, v...), nil
}
func (m *memStore) write(name string, value []byte, want string) error {
	m.lastWant = want
	if m.onWrite != nil {
		m.onWrite(name)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writes++
	if m.fail {
		return errors.New("the test store refused")
	}
	if m.other != nil {
		value = m.other
	}
	m.items[name] = append([]byte{}, value...)
	return nil
}
func (m *memStore) get(name string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.items[name]
	return string(v), ok
}

// useMemStore makes the default helper's store an in-memory one, this
// binary's path a fixed one, and its locks a temp directory's.
func useMemStore(t *testing.T) *memStore {
	t.Helper()
	m := &memStore{items: map[string][]byte{}}
	saved, savedSelf, savedLocks := defaultSecretStore, selfExecutable, secretLockDir
	locks := t.TempDir()
	defaultSecretStore = func() (osSecretStore, error) { return m, nil }
	selfExecutable = func() (string, error) { return "/opt/cpb/bin/cpb", nil }
	secretLockDir = func() (string, error) { return locks, nil }
	t.Cleanup(func() { defaultSecretStore, selfExecutable, secretLockDir = saved, savedSelf, savedLocks })
	return m
}

func TestDefaultHelperCheckAndStore(t *testing.T) {
	m := useMemStore(t)
	if err := runSecretHelper([]string{"--check", "K=cpb:tok"}, strings.NewReader("")); err == nil {
		t.Fatal("--check passed a reference that is not stored")
	} else if code, ok := exitCode(err); !ok || code != 4 {
		t.Fatalf("--check of a missing reference: %v", err)
	}
	if err := runSecretHelper([]string{"--store", "cpb:tok"}, strings.NewReader("v-1")); err != nil {
		t.Fatalf("--store: %v", err)
	}
	if got := string(m.items["tok"]); got != "v-1" {
		t.Fatalf("stored %q", got)
	}
	if err := runSecretHelper([]string{"--check", "K=cpb:tok"}, strings.NewReader("")); err != nil {
		t.Fatalf("--check after --store: %v", err)
	}
	// Never an overwrite: rotation is a new name.
	err := runSecretHelper([]string{"--store", "cpb:tok"}, strings.NewReader("v-2"))
	if err == nil || !strings.Contains(err.Error(), "never replaces") || string(m.items["tok"]) != "v-1" || m.writes != 1 {
		t.Fatalf("an overwrite: %v, %q, %d writes", err, m.items["tok"], m.writes)
	}
	if err := runSecretHelper([]string{"--store", "cpb:empty"}, strings.NewReader("")); err == nil || m.writes != 1 {
		t.Fatalf("an empty value: %v, %d writes", err, m.writes)
	}
	for _, args := range [][]string{{"--check", "K=keychain:x"}, {"--store", "keychain:x"}, {"--store", "cpb:Bad"}, {"--check", "K=cpb:a/b"}} {
		if err := runSecretHelper(args, strings.NewReader("v")); err == nil {
			t.Errorf("%v passed", args)
		}
	}
	if m.writes != 1 {
		t.Fatalf("a refused call wrote: %d writes", m.writes)
	}
}

func TestDefaultHelperStoreFailureStoresNothing(t *testing.T) {
	m := useMemStore(t)
	m.fail = true
	if err := runSecretHelper([]string{"--store", "cpb:tok"}, strings.NewReader("v")); err == nil {
		t.Fatal("a store that refused reported success")
	}
	if _, ok := m.items["tok"]; ok {
		t.Fatal("a refused write left an item")
	}
}

func TestDefaultHelperStoresPrintableASCIIOnly(t *testing.T) {
	m := useMemStore(t)
	for _, v := range []string{"tab\there", "line\n", "p\u00e4ss", "nul\x00"} {
		err := runSecretHelper([]string{"--store", "cpb:tok"}, strings.NewReader(v))
		if err == nil || !strings.Contains(err.Error(), "printable ASCII") || strings.Contains(err.Error(), v) {
			t.Errorf("%q: %v", v, err)
		}
	}
	if m.writes != 0 {
		t.Fatalf("a refused value was written: %d writes", m.writes)
	}
	if err := runSecretHelper([]string{"--store", "cpb:tok"}, strings.NewReader(" A-z~! 0 ")); err != nil {
		t.Fatalf("printable ASCII refused: %v", err)
	}
}

// The store holds another value after the write (another program wrote the
// same item at the same moment, or macOS's add failed with exit 0): the
// value is read back, and the store is reported as not holding it.
func TestDefaultHelperReadsTheValueBack(t *testing.T) {
	m := useMemStore(t)
	m.other = []byte("someone-else")
	err := runSecretHelper([]string{"--store", "cpb:tok"}, strings.NewReader("v-1"))
	if err == nil || !strings.Contains(err.Error(), "does not hold the value given") || strings.Contains(err.Error(), "v-1") || strings.Contains(err.Error(), "someone-else") {
		t.Fatalf("a value the store did not keep: %v", err)
	}
}

// Two default helpers storing one name are serialized: the second waits for
// the first's lock, then finds the name stored and refuses.
func TestDefaultHelperStoresOneNameAtATime(t *testing.T) {
	m := useMemStore(t)
	unlock, err := lockSecretName("tok")
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- runSecretHelper([]string{"--store", "cpb:tok"}, strings.NewReader("v-2")) }()
	select {
	case err := <-result:
		unlock()
		t.Fatalf("--store did not wait for the lock: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	// The first helper stores its value while it holds the lock.
	m.mu.Lock()
	m.items["tok"] = []byte("v-1")
	m.mu.Unlock()
	unlock()
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "never replaces") {
			t.Fatalf("the second store: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("--store never took the lock")
	}
	if v, _ := m.get("tok"); v != "v-1" || m.writes != 0 {
		t.Fatalf("the first value was replaced: %q, %d writes", v, m.writes)
	}
}

func TestDefaultHelperExecSetsTheValue(t *testing.T) {
	m := useMemStore(t)
	m.items["tok"] = []byte("v-1")
	path, argv, env, err := secretHelperExec(m, []string{"TOKEN=cpb:tok", "--", "sh", "-c", "true"}, []string{"PATH=/usr/bin:/bin", "TOKEN=stale"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(path, "/sh") || !slices.Equal(argv, []string{"sh", "-c", "true"}) {
		t.Fatalf("exec %s %v", path, argv)
	}
	if !slices.Contains(env, "TOKEN=v-1") || slices.Contains(env, "TOKEN=stale") {
		t.Fatalf("env %v", env)
	}
	if _, _, _, err := secretHelperExec(m, []string{"TOKEN=cpb:missing", "--", "sh"}, nil); err == nil || strings.Contains(err.Error(), "v-1") {
		t.Fatalf("a missing reference: %v", err)
	}
}

func TestDefaultHelperOnlyWithoutAConfiguredOne(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	useMemStore(t)
	t.Setenv(envset.SecretHelperEnv, "")
	x, err := resolveHelperFor([]string{"cpb:a", "cpb:b"})
	if err != nil || !x.def || x.path != "/opt/cpb/bin/cpb" || !slices.Equal(x.pre, []string{secretHelperArg}) {
		t.Fatalf("cpb: references, no helper: %+v %v", x, err)
	}
	// Any other scheme, alone or mixed in, keeps today's refusal.
	for _, refs := range [][]string{{"keychain:a"}, {"cpb:a", "op://v/i/f"}} {
		if _, err := resolveHelperFor(refs); !errors.Is(err, errNoHelper) {
			t.Errorf("%v: %v", refs, err)
		}
	}
	// A configured helper always wins, cpb: references included.
	helper, _ := fakeHelper(t)
	t.Setenv(envset.SecretHelperEnv, helper)
	if x, err := resolveHelperFor([]string{"cpb:a"}); err != nil || x.def || x.path != helper {
		t.Fatalf("a configured helper: %+v %v", x, err)
	}
}

func TestDefaultHelperLaunchArgv(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	useMemStore(t)
	t.Setenv(envset.SecretHelperEnv, "")
	lp, err := planLaunch(&manifest.Env{Refs: map[string]string{"TOKEN": "cpb:tok"}})
	if err != nil {
		t.Fatal(err)
	}
	c := lp.command("/bin/claude", []string{"-p", "hi"}, []string{"TOKEN=stale"})
	want := []string{"/opt/cpb/bin/cpb", secretHelperArg, "TOKEN=cpb:tok", "--", "/bin/claude", "-p", "hi"}
	if !slices.Equal(c.Args, want) {
		t.Fatalf("argv %v, want %v", c.Args, want)
	}
	if slices.Contains(c.Env, "TOKEN=stale") {
		t.Fatal("a stale shell value reached the launch")
	}
}

// A store that cannot answer is never "not stored": --check and --store
// both exit 5 (unavailableExit), so the store step is not armed, and
// nothing is written.
func TestDefaultHelperUnavailableStoreIsNotNotStored(t *testing.T) {
	m := useMemStore(t)
	m.unavailable = true
	for _, args := range [][]string{{"--check", "K=cpb:tok"}, {"--store", "cpb:tok"}} {
		err := runSecretHelper(args, strings.NewReader("v-1"))
		if code, ok := exitCode(err); !ok || code != unavailableExit {
			t.Fatalf("%v on an unavailable store: %v", args, err)
		}
	}
	if m.writes != 0 {
		t.Fatalf("%d writes to an unavailable store", m.writes)
	}
}

// A store keeps only so many bytes whole: a longer value is refused before
// the lock and the write, naming the limit, and a value at the limit is
// stored.
func TestDefaultHelperRefusesAValueOverTheStoresLimit(t *testing.T) {
	m := useMemStore(t)
	m.max = 8
	err := runSecretHelper([]string{"--store", "cpb:long"}, strings.NewReader("123456789"))
	if err == nil || !strings.Contains(err.Error(), "at most 8 bytes") || m.writes != 0 {
		t.Fatalf("a value over the limit: %v, %d writes", err, m.writes)
	}
	if err := runSecretHelper([]string{"--store", "cpb:fits"}, strings.NewReader("12345678")); err != nil || m.writes != 1 {
		t.Fatalf("a value at the limit: %v, %d writes", err, m.writes)
	}
}

// lockFileHeld reports whether the lock file of name is held, by trying to
// take it without waiting.
func lockFileHeld(t *testing.T, name string) bool {
	t.Helper()
	dir, err := secretLockDir()
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, name+".lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return true
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}

// The existence check and the write both happen under the name's lock.
func TestDefaultHelperHoldsTheLockAcrossCheckAndWrite(t *testing.T) {
	m := useMemStore(t)
	var atCheck, atWrite bool
	m.onExists = func(name string) { atCheck = lockFileHeld(t, name) }
	m.onWrite = func(name string) { atWrite = lockFileHeld(t, name) }
	if err := runSecretHelper([]string{"--store", "cpb:tok"}, strings.NewReader("v-1")); err != nil {
		t.Fatal(err)
	}
	if !atCheck || !atWrite {
		t.Fatalf("lock held at the check: %v, at the write: %v", atCheck, atWrite)
	}
	if lockFileHeld(t, "tok") {
		t.Fatal("the lock outlived the store")
	}
}

// A lock that stays taken is waited on for lockTimeout, then the store is
// one that cannot answer (exit 5), and the message names the locked name
// and nothing else.
func TestDefaultHelperLockTimeout(t *testing.T) {
	m := useMemStore(t)
	saved := lockTimeout
	lockTimeout = 300 * time.Millisecond
	t.Cleanup(func() { lockTimeout = saved })
	dir, _ := secretLockDir()
	os.MkdirAll(dir, 0o700)
	f, err := os.OpenFile(filepath.Join(dir, "tok.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	var herr error
	start := time.Now()
	out := captureStderr(t, func() {
		done := make(chan error, 1)
		go func() { done <- runSecretHelper([]string{"--store", "cpb:tok"}, strings.NewReader("Value-77")) }()
		select {
		case herr = <-done:
		case <-time.After(5 * time.Second):
			herr = errors.New("still waiting on the lock after 5 s")
		}
	})
	if code, ok := exitCode(herr); !ok || code != unavailableExit || time.Since(start) > 4*time.Second {
		t.Fatalf("a held lock, after %s: %v", time.Since(start), herr)
	}
	if !strings.Contains(out, "cpb:tok") || strings.Contains(out, "Value-77") || strings.Contains(out, dir) || m.writes != 0 {
		t.Fatalf("the message: %q, %d writes", out, m.writes)
	}
}

// A write the store could not answer (a locked store, a tool that did not
// answer) is exit 5; a write it refused is not.
func TestDefaultHelperUnavailableWriteIsExit5(t *testing.T) {
	m := useMemStore(t)
	m.fail = true
	err := runSecretHelper([]string{"--store", "cpb:tok"}, strings.NewReader("v-1"))
	if code, ok := exitCode(err); err == nil || (ok && code == unavailableExit) {
		t.Fatalf("a refused write: %v", err)
	}
	m.fail = false
	lw := &lockedWriteStore{memStore: m}
	defaultSecretStore = func() (osSecretStore, error) { return lw, nil }
	var herr error
	out := captureStderr(t, func() { herr = runSecretHelper([]string{"--store", "cpb:tok2"}, strings.NewReader("v-1")) })
	if code, ok := exitCode(herr); !ok || code != unavailableExit || !strings.Contains(out, "locked") {
		t.Fatalf("a locked store's write: %v %q", herr, out)
	}
}

// lockedWriteStore is a memStore whose writes find the store locked.
type lockedWriteStore struct{ *memStore }

func (l *lockedWriteStore) write(string, []byte, string) error {
	return unavailable("the test store is locked; nothing was stored")
}

// A value over the store's limit is refused before the lock is even asked
// for: with the lock held elsewhere, the answer is the limit, at once.
func TestDefaultHelperChecksTheCapBeforeTheLock(t *testing.T) {
	m := useMemStore(t)
	m.max = 4
	saved := lockTimeout
	lockTimeout = 200 * time.Millisecond
	t.Cleanup(func() { lockTimeout = saved })
	dir, _ := secretLockDir()
	os.MkdirAll(dir, 0o700)
	f, err := os.OpenFile(filepath.Join(dir, "tok.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
	err = runSecretHelper([]string{"--store", "cpb:tok"}, strings.NewReader("12345"))
	if code, ok := exitCode(err); ok && code == unavailableExit {
		t.Fatalf("the lock was asked for before the limit: %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "at most 4 bytes") {
		t.Fatalf("a value over the limit: %v", err)
	}
}

// The lock is taken with LOCK_NB, so the wait is the helper's own,
// bounded by lockTimeout, never flock's.
func TestDefaultHelperLockNeverBlocksInFlock(t *testing.T) {
	useMemStore(t)
	var flags []int
	saved := flockName
	flockName = func(fd, how int) error {
		flags = append(flags, how)
		return saved(fd, how)
	}
	t.Cleanup(func() { flockName = saved })
	if err := runSecretHelper([]string{"--store", "cpb:tok"}, strings.NewReader("v-1")); err != nil {
		t.Fatal(err)
	}
	if len(flags) == 0 || flags[0] != syscall.LOCK_EX|syscall.LOCK_NB {
		t.Fatalf("flock flags %v, want LOCK_EX|LOCK_NB first", flags)
	}
}

// A read that the store could not answer is exit 5: the read-back after a
// store (which may have stored the value: the message says so), and the
// read before a launch.
func TestDefaultHelperUnansweredReadIsExit5(t *testing.T) {
	m := useMemStore(t)
	m.readErr = unavailable("the test store did not answer within 1ms")
	var err error
	out := captureStderr(t, func() { err = runSecretHelper([]string{"--store", "cpb:tok"}, strings.NewReader("v-1")) })
	if code, ok := exitCode(err); !ok || code != unavailableExit || !strings.Contains(out, "may be stored") {
		t.Fatalf("the read-back: %v %q", err, out)
	}
	m.items["tok2"] = []byte("v-2")
	out = captureStderr(t, func() { err = runSecretHelper([]string{"T=cpb:tok2", "--", "sh", "-c", "true"}, strings.NewReader("")) })
	if code, ok := exitCode(err); !ok || code != unavailableExit || strings.Contains(out, "v-2") {
		t.Fatalf("the launch read: %v %q", err, out)
	}
}

// --ready is the default helper's own question before the store step asks
// for a value: exit 0 when the store can take one, exit 5 with the
// store's reason when it cannot; it takes one cpb: reference.
func TestDefaultHelperReady(t *testing.T) {
	m := useMemStore(t)
	if err := runSecretHelper([]string{"--ready", "cpb:tok"}, strings.NewReader("")); err != nil {
		t.Fatalf("a ready store: %v", err)
	}
	m.notReady = unavailable("the test store is locked; nothing was asked for or stored")
	var herr error
	out := captureStderr(t, func() { herr = runSecretHelper([]string{"--ready", "cpb:tok"}, strings.NewReader("")) })
	if code, ok := exitCode(herr); !ok || code != unavailableExit || !strings.Contains(out, "locked") {
		t.Fatalf("a store that cannot take a value: %v %q", herr, out)
	}
	for _, args := range [][]string{{"--ready"}, {"--ready", "vault:x/y"}, {"--ready", "cpb:a", "cpb:b"}} {
		if err := runSecretHelper(args, strings.NewReader("")); err == nil {
			t.Fatalf("%q was taken", args)
		}
	}
	if m.writes != 0 {
		t.Fatalf("%d writes", m.writes)
	}
}

// cpb ask on an account whose Claude token is a cpb: reference runs the
// default helper with cpb's own store, whatever ask's narrow child
// environment holds: a planted CPB_SECRET_STORE_* or opt-in is replaced.
func TestAskThroughAnAccountGetsCPBsStore(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	useMemStore(t)
	t.Setenv(envset.SecretHelperEnv, "")
	t.Setenv(fileStoreOptInEnv, "")
	launch := []string{"XDG_DATA_HOME=/elsewhere", storeEnvPrefix + "DIR=/planted", storeEnvPrefix + "FILE_OK=1"}
	signIn := askSignIn{mode: askLoginAccount, account: &account.Account{Name: "a", Auth: account.Auth{Ref: "cpb:tok"}}}
	c, err := askCommand(context.Background(), signIn, "/bin/claude", nil, launch)
	if err != nil {
		t.Fatal(err)
	}
	env := strings.Join(c.Env, "\n")
	if !strings.Contains(env, storeEnvPrefix+"SET=1") || strings.Contains(env, "=/planted") || strings.Contains(env, storeEnvPrefix+"FILE_OK=1") {
		t.Fatalf("ask's helper environment: %q", c.Env)
	}
	if want := storeDirForHelper(); want != "" && !strings.Contains(env, storeEnvPrefix+"DIR="+want) {
		t.Fatalf("ask's helper got another store than %s: %q", want, c.Env)
	}
}

// The file store opt-in comes from cpb's own environment only.
func TestFileStoreOptInIsCPBsOwn(t *testing.T) {
	t.Setenv(fileStoreOptInEnv, "")
	if env := strings.Join(defaultHelperEnv([]string{fileStoreOptInEnv + "=1"}), "\n"); strings.Contains(env, storeEnvPrefix+"FILE_OK=1") {
		t.Fatalf("a launch environment opted in: %q", env)
	}
	t.Setenv(fileStoreOptInEnv, "1")
	if env := strings.Join(defaultHelperEnv(nil), "\n"); !strings.Contains(env, storeEnvPrefix+"FILE_OK=1") {
		t.Fatalf("cpb's own opt-in was not handed: %q", env)
	}
}

// A configured helper that is cpb's own binary (its path, a symlink to it on
// PATH, a hard link) is refused before any store is touched: at ALTER
// DEFAULTS SET, at a check, and at launch, with the line that unsets it.
// Unset, cpb's default helper is used.
func TestConfiguredHelperThatIsCPBIsRefused(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	m := useMemStore(t)
	opened := 0
	defaultSecretStore = func() (osSecretStore, error) { opened++; return m, nil }
	touched := 0
	m.onExists = func(string) { touched++ }
	m.onWrite = func(string) { touched++ }
	bin := t.TempDir()
	self := filepath.Join(bin, "cpb")
	if err := os.WriteFile(self, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	selfExecutable = func() (string, error) { return self, nil }
	if err := os.Symlink(self, filepath.Join(bin, "k")); err != nil {
		t.Fatal(err)
	}
	hard := filepath.Join(bin, "cpb-copy")
	if err := os.Link(self, hard); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(envset.SecretHelperEnv, "")
	setting := filepath.Join(envset.Dir(config.ResolvePlaybooksDir()), envset.SecretHelperFile)

	for _, cmd := range []string{self, "k", hard} {
		err := stmtErr(t, "ALTER DEFAULTS SET secret_helper = "+cmd)
		if err == nil || !strings.Contains(err.Error(), "is cpb itself") || !strings.Contains(err.Error(), "ALTER DEFAULTS RESET secret_helper") {
			t.Fatalf("SET %s: %v", cmd, err)
		}
		if _, serr := os.Stat(setting); serr == nil {
			t.Fatalf("SET %s was stored", cmd)
		}
	}

	// A setting already stored (hand-written, or from before the refusal).
	if err := envset.SetSecretHelper(envset.Dir(config.ResolvePlaybooksDir()), "k"); err != nil {
		t.Fatal(err)
	}
	if _, err := planLaunch(&manifest.Env{Refs: map[string]string{"TOKEN": "cpb:tok"}}); err == nil || !strings.Contains(err.Error(), "is cpb itself") || !strings.Contains(err.Error(), "ALTER DEFAULTS RESET secret_helper") {
		t.Fatalf("launch with the setting: %v", err)
	}
	mustStmt(t, "CREATE ENV e SET A=literal AS PLAINTEXT")
	if err := stmtErr(t, "ALTER ENV e SET TOKEN FROM cpb:tok"); err == nil || !strings.Contains(err.Error(), "is cpb itself") {
		t.Fatalf("check with the setting: %v", err)
	}
	// CPB_SECRET_HELPER names the variable to unset.
	t.Setenv(envset.SecretHelperEnv, hard)
	if _, err := resolveHelperFor([]string{"cpb:tok"}); err == nil || !strings.Contains(err.Error(), "unset "+envset.SecretHelperEnv) {
		t.Fatalf("CPB_SECRET_HELPER: %v", err)
	}
	if opened != 0 || touched != 0 {
		t.Fatalf("a store was touched: opened %d, calls %d", opened, touched)
	}

	// Unset, the default helper is used.
	t.Setenv(envset.SecretHelperEnv, "")
	mustStmt(t, "ALTER DEFAULTS RESET secret_helper")
	if x, err := resolveHelperFor([]string{"cpb:tok"}); err != nil || !x.def {
		t.Fatalf("unset: %+v %v", x, err)
	}
}

// handOver puts this process in the state a helper is in after env's
// hand-over (applyHelperStoreEnv), and resets it after the test.
func handOver(t *testing.T, env []string) error {
	t.Helper()
	t.Cleanup(func() {
		helperHanded, helperFileOK, helperToolEnv, helperLockDir = false, false, nil, ""
		setHelperStoreDir("")
		setHelperSecretTool("")
	})
	for _, k := range []string{"SET", "DIR", "LOCK_DIR", "SECRET_TOOL", "FILE_OK"} {
		t.Setenv(storeEnvPrefix+k, "")
		os.Unsetenv(storeEnvPrefix + k)
	}
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(k, storeEnvPrefix) {
			t.Setenv(k, v)
		}
	}
	return applyHelperStoreEnv()
}

// No helper gets a secret-store variable from the launch environment:
// a configured helper gets none at all, cpb's default helper only what
// cpb hands it. Checks, launches and cpb ask alike.
func TestEveryHelperGetsOnlyCPBsHandOver(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	t.Setenv(fileStoreOptInEnv, "")
	dir := t.TempDir()
	envLog := filepath.Join(dir, "env.log")
	helper := filepath.Join(dir, "my-helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nenv > \"$ENV_LOG\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENV_LOG", envLog)
	foreign := append(os.Environ(), fileStoreOptInEnv+"=1", storeEnvPrefix+"SET=1", storeEnvPrefix+"SECRET_TOOL=/tmp/other-tool",
		storeEnvPrefix+"DIR=/tmp/planted", storeEnvPrefix+"FILE_OK=1", storeEnvPrefix+"LOCK_DIR=/tmp/planted-locks")
	foreignIn := func(env []string) string {
		for _, kv := range env {
			if strings.HasPrefix(kv, fileStoreOptInEnv+"=") || strings.Contains(kv, "/tmp/other-tool") || strings.Contains(kv, "/tmp/planted") || kv == storeEnvPrefix+"FILE_OK=1" {
				return kv
			}
		}
		return ""
	}
	// A configured helper: a check, a launch, cpb ask.
	x := helperExe{path: helper, name: helper}
	if err := runHelper(x, nil, nil, nil, foreign, "--check", "K=keychain:x"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(envLog)
	if len(data) == 0 || strings.Contains(string(data), storeEnvPrefix) || strings.Contains(string(data), fileStoreOptInEnv+"=") {
		t.Fatalf("a configured helper's check got a store variable:\n%s", data)
	}
	lp := &launchPlan{helper: helper, refs: map[string]string{"K": "keychain:x"}}
	for _, kv := range lp.command("/bin/true", nil, foreign).Env {
		if strings.HasPrefix(kv, storeEnvPrefix) || strings.HasPrefix(kv, fileStoreOptInEnv+"=") {
			t.Fatalf("a configured helper's launch got %s", kv)
		}
	}
	t.Setenv(envset.SecretHelperEnv, helper)
	signIn := askSignIn{mode: askLoginAccount, account: &account.Account{Name: "a", Auth: account.Auth{Ref: "keychain:x"}}}
	c, err := askCommand(context.Background(), signIn, "/bin/true", nil, foreign)
	if err != nil {
		t.Fatal(err)
	}
	for _, kv := range c.Env {
		if strings.HasPrefix(kv, storeEnvPrefix) || strings.HasPrefix(kv, fileStoreOptInEnv+"=") {
			t.Fatalf("a configured helper under cpb ask got %s", kv)
		}
	}
	// cpb's default helper: cpb's own hand-over, nothing foreign.
	t.Setenv(envset.SecretHelperEnv, "")
	useMemStore(t)
	def := &launchPlan{helper: "/opt/cpb/bin/cpb", helperPre: []string{secretHelperArg}, def: true, refs: map[string]string{"K": "cpb:tok"}}
	env := def.command("/bin/true", nil, foreign).Env
	if kv := foreignIn(env); kv != "" || !slices.Contains(env, storeEnvPrefix+"SET=1") {
		t.Fatalf("the default helper's launch: foreign %q in %q", kv, env)
	}
	c, err = askCommand(context.Background(), askSignIn{mode: askLoginAccount, account: &account.Account{Name: "a", Auth: account.Auth{Ref: "cpb:tok"}}}, "/bin/true", nil, foreign)
	if err != nil {
		t.Fatal(err)
	}
	if kv := foreignIn(c.Env); kv != "" {
		t.Fatalf("the default helper under cpb ask got %s", kv)
	}
}

// A hand-over whose paths are not absolute is broken: the helper refuses
// it before any store is touched.
func TestHandedPathsMustBeAbsolute(t *testing.T) {
	m := useMemStore(t)
	touched := 0
	m.onExists = func(string) { touched++ }
	for _, k := range []string{"DIR", "LOCK_DIR", "SECRET_TOOL"} {
		t.Run(k, func(t *testing.T) {
			handOver(t, nil)
			t.Setenv(storeEnvPrefix+"SET", "1")
			t.Setenv(storeEnvPrefix+k, "relative/"+strings.ToLower(k))
			err := runSecretHelper([]string{"--check", "K=cpb:tok"}, strings.NewReader(""))
			if err == nil || !strings.Contains(err.Error(), storeEnvPrefix+k+" is not an absolute path") {
				t.Fatalf("%v", err)
			}
		})
	}
	if touched != 0 {
		t.Fatalf("a store was asked %d times", touched)
	}
}

// The helper locks where the cpb that runs it says, never under the
// launch's HOME or XDG_CACHE_HOME.
func TestHelperLocksWhereCPBSays(t *testing.T) {
	saved := secretLockDir
	t.Cleanup(func() { secretLockDir = saved })
	own, err := secretLockDir()
	if err != nil {
		t.Skip("no cache directory here")
	}
	env := defaultHelperEnv(nil)
	if !slices.Contains(env, storeEnvPrefix+"LOCK_DIR="+own) {
		t.Fatalf("no lock directory handed: %q", env)
	}
	launch := t.TempDir()
	t.Setenv("HOME", launch)
	t.Setenv("XDG_CACHE_HOME", launch)
	if err := handOver(t, env); err != nil {
		t.Fatal(err)
	}
	if got, err := secretLockDir(); err != nil || got != own {
		t.Fatalf("the helper locks in %s (%v), want %s", got, err, own)
	}
	// Handed nothing: its own lookup, as before.
	if err := handOver(t, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := secretLockDir(); got == own {
		t.Fatalf("with nothing handed the helper still used %s", own)
	}
}

// A playbook never declares cpb's secret-store variables.
func TestPlaybookCannotDeclareSecretStoreVariables(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	seedFlatPlaybook(t, "p")
	for _, line := range []string{
		"ALTER PLAYBOOK p SET VAR " + fileStoreOptInEnv + "=1 AS PLAINTEXT",
		"ALTER PLAYBOOK p SET VAR " + storeEnvPrefix + "SET=1 AS PLAINTEXT",
		"CREATE ENV e SET " + fileStoreOptInEnv + "=1 AS PLAINTEXT",
	} {
		if err := stmtErr(t, line); err == nil || !strings.Contains(err.Error(), "never in a playbook") {
			t.Errorf("%s: %v", line, err)
		}
	}
	// The secret helper is the person's own, from their shell or DEFAULTS.
	for _, line := range []string{
		"ALTER PLAYBOOK p SET VAR " + envset.SecretHelperEnv + "='/usr/local/bin/h' AS PLAINTEXT",
		"ALTER PLAYBOOK p SET VAR " + envset.SecretHelperEnv + " FROM 'keychain:h'",
		"ALTER PLAYBOOK p BLOCK VAR " + envset.SecretHelperEnv,
		"CREATE ENV e SET " + envset.SecretHelperEnv + "='/usr/local/bin/h' AS PLAINTEXT",
	} {
		if err := stmtErr(t, line); err == nil || !strings.Contains(err.Error(), "own shell") {
			t.Errorf("%s: %v", line, err)
		}
	}
}
