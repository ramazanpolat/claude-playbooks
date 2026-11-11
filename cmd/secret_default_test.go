package cmd

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

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
}

func (m *memStore) where(name string) string { return "the test store, as " + name }
func (m *memStore) exists(name string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.unavailable {
		return false, errors.New("the test store cannot be reached")
	}
	_, ok := m.items[name]
	return ok, nil
}
func (m *memStore) read(name string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.items[name]
	if !ok {
		return nil, errors.New("not found")
	}
	return append([]byte{}, v...), nil
}
func (m *memStore) write(name string, value []byte) error {
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
