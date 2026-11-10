package cmd

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/envset"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// memStore is an in-memory osSecretStore; writes counts every write call.
type memStore struct {
	items  map[string][]byte
	writes int
	fail   bool
}

func (m *memStore) where(name string) string { return "the test store, as " + name }
func (m *memStore) exists(name string) (bool, error) {
	_, ok := m.items[name]
	return ok, nil
}
func (m *memStore) read(name string) ([]byte, error) {
	v, ok := m.items[name]
	if !ok {
		return nil, errors.New("not found")
	}
	return append([]byte{}, v...), nil
}
func (m *memStore) write(name string, value []byte) error {
	m.writes++
	if m.fail {
		return errors.New("the test store refused")
	}
	m.items[name] = append([]byte{}, value...)
	return nil
}

// useMemStore makes the default helper's store an in-memory one, and this
// binary's path a fixed one.
func useMemStore(t *testing.T) *memStore {
	t.Helper()
	m := &memStore{items: map[string][]byte{}}
	saved, savedSelf := defaultSecretStore, selfExecutable
	defaultSecretStore = func() (osSecretStore, error) { return m, nil }
	selfExecutable = func() (string, error) { return "/opt/cpb/bin/cpb", nil }
	t.Cleanup(func() { defaultSecretStore, selfExecutable = saved, savedSelf })
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
