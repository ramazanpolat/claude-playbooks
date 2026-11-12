package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/envset"
)

// storeVarIn is the first of cpb's secret-store variables in env
// (CPB_SECRET_FILE_STORE or any CPB_SECRET_STORE_*), "" when none.
func storeVarIn(env []string) string {
	for _, kv := range env {
		if strings.HasPrefix(kv, fileStoreOptInEnv+"=") || strings.HasPrefix(kv, storeEnvPrefix) {
			return kv
		}
	}
	return ""
}

// A helper set earlier in the same run is refused as cpb itself the same
// way as a stored one: through a link too, and saying where it was set.
func TestHelperSetEarlierInTheRunThatIsCPBIsRefused(t *testing.T) {
	useMemStore(t)
	bin := t.TempDir()
	self := filepath.Join(bin, "cpb")
	other := filepath.Join(bin, "other")
	for _, p := range []string{self, other} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	selfExecutable = func() (string, error) { return self, nil }
	link := filepath.Join(bin, "k")
	if err := os.Symlink(self, link); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []string{self, link} {
		s := helperState{set: true, h: &envset.Helper{Command: cmd, From: "setting"}}
		if _, _, err := s.resolve(); err == nil || !strings.Contains(err.Error(), "is cpb itself") || !strings.Contains(err.Error(), "set earlier in this run") {
			t.Errorf("%s: %v", cmd, err)
		}
	}
	s := helperState{set: true, h: &envset.Helper{Command: other, From: "setting"}}
	if _, path, err := s.resolve(); err != nil || path != other {
		t.Fatalf("another program: %q %v", path, err)
	}
}

// Exec mode's pairs name variables to set, never a reserved one.
func TestExecPairsRefuseReservedKeys(t *testing.T) {
	m := useMemStore(t)
	m.items["tok"] = []byte("v-1")
	for _, key := range []string{envset.SecretHelperEnv, fileStoreOptInEnv, storeEnvPrefix + "DIR"} {
		if _, _, _, err := secretHelperExec(m, []string{key + "=cpb:tok", "--", "sh", "-c", "true"}, nil); err == nil {
			t.Errorf("%s=cpb:tok was taken as a pair", key)
		}
	}
	if _, _, _, err := secretHelperExec(m, []string{"TOKEN=cpb:tok", "--", "sh", "-c", "true"}, nil); err != nil {
		t.Fatalf("TOKEN=cpb:tok: %v", err)
	}
}

// No store variable reaches a store's tool or the command exec mode runs,
// whatever the helper's own environment holds: the opt-in as well as the
// CPB_SECRET_STORE_* family, each place the environment is built.
func TestStoreVariablesNeverReachAToolOrACommand(t *testing.T) {
	m := useMemStore(t)
	m.items["tok"] = []byte("v-1")
	handOver(t, nil)
	t.Setenv(fileStoreOptInEnv, "1")
	t.Setenv(storeEnvPrefix+"STRAY", "x")

	for _, kv := range defaultHelperEnv([]string{fileStoreOptInEnv + "=1", storeEnvPrefix + "STRAY=x"}) {
		if kv == fileStoreOptInEnv+"=1" || kv == storeEnvPrefix+"STRAY=x" {
			t.Errorf("defaultHelperEnv kept %s", kv)
		}
	}
	if kv := storeVarIn(storeToolEnv()); kv != "" {
		t.Errorf("a tool's environment, nothing handed: %s", kv)
	}
	if err := handOver(t, []string{storeEnvPrefix + "SET=1", storeEnvPrefix + "DIR=/store", storeEnvPrefix + "LOCK_DIR=/locks"}); err != nil {
		t.Fatal(err)
	}
	if kv := storeVarIn(storeToolEnv()); kv != "" {
		t.Errorf("a tool's environment, handed: %s", kv)
	}

	handOver(t, nil)
	t.Setenv(fileStoreOptInEnv, "1")
	t.Setenv(storeEnvPrefix+"STRAY", "x")
	var got []string
	saved := helperExec
	helperExec = func(_ string, _ []string, env []string) error { got = env; return nil }
	t.Cleanup(func() { helperExec = saved })
	if err := runSecretHelper([]string{"TOKEN=cpb:tok", "--", "/bin/true"}, nil); err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("exec mode ran nothing")
	}
	if kv := storeVarIn(got); kv != "" {
		t.Errorf("exec mode's command got %s", kv)
	}
}

// The notices cpb ask shows are the default helper's two lines, both; the
// rest, claude's own stderr among it, is kept for a failure's reason.
func TestSplitHelperNotices(t *testing.T) {
	stderr := "cpb:a is read from the file /x/a: the keyring does not hold it\n" +
		"API Error: 529 overloaded\n" +
		"cpb:b is also kept in the file /x/b, beside the keyring\n" +
		"cpb:c something else\n"
	notices, rest := splitHelperNotices(stderr)
	if notices != "cpb:a is read from the file /x/a: the keyring does not hold it\ncpb:b is also kept in the file /x/b, beside the keyring\n" {
		t.Errorf("notices %q", notices)
	}
	if rest != "API Error: 529 overloaded\ncpb:c something else\n" {
		t.Errorf("rest %q", rest)
	}
}
