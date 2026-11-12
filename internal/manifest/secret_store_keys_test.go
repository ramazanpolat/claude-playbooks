package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cpb's secret-store variables are reserved: no layer declares them, as a
// value, a reference or an --env-file line, and the refusal says where the
// opt-in belongs.
func TestSecretStoreKeysAreReserved(t *testing.T) {
	for _, k := range []string{SecretFileStoreEnv, SecretStoreEnvPrefix + "SET", SecretStoreEnvPrefix + "SECRET_TOOL", SecretStoreEnvPrefix + "ANYTHING"} {
		err := ValidateEnvKey(k)
		if err == nil || !strings.Contains(err.Error(), "environment of the one machine that needs it, never in a playbook") {
			t.Errorf("%s: %v", k, err)
		}
		if err := ValidateRefs(map[string]string{k: "keychain:x"}, nil, nil); err == nil {
			t.Errorf("%s as a reference was accepted", k)
		}
		path := filepath.Join(t.TempDir(), "env")
		if err := os.WriteFile(path, []byte(k+"=1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ParseEnvFile(path); err == nil || !strings.Contains(err.Error(), k) {
			t.Errorf("%s in an --env-file: %v", k, err)
		}
	}
	// The prefix alone reserves nothing beside it.
	for _, k := range []string{"CPB_SECRET_STOREX", "MY_CPB_SECRET_STORE_X"} {
		if err := ValidateEnvKey(k); err != nil {
			t.Errorf("%s: %v", k, err)
		}
	}
}

// CPB_SECRET_HELPER is a reserved key: no layer declares it, as a value,
// a reference or an --env-file line, and the refusal says where it
// belongs. A .playbook that already holds it still reads.
func TestSecretHelperIsReserved(t *testing.T) {
	err := ValidateEnvKey(SecretHelperEnv)
	if err == nil || !strings.Contains(err.Error(), "own shell") || !strings.Contains(err.Error(), "ALTER DEFAULTS SET secret_helper") {
		t.Fatalf("ValidateEnvKey: %v", err)
	}
	if err := ValidateRefs(map[string]string{SecretHelperEnv: "keychain:x"}, nil, nil); err == nil {
		t.Fatal("as a reference: accepted")
	}
	path := filepath.Join(t.TempDir(), "env")
	if err := os.WriteFile(path, []byte(SecretHelperEnv+"=/usr/local/bin/h\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseEnvFile(path); err == nil || !strings.Contains(err.Error(), SecretHelperEnv) {
		t.Fatalf("in an --env-file: %v", err)
	}
	// A .playbook that already holds it still reads, so it can be repaired:
	// a value or a reference is kept and named by DeclaredReserved (a
	// launch refuses it); a block entry, which has no effect, is dropped.
	dir := t.TempDir()
	for _, c := range []struct{ body, field string }{
		{"[env.set]\n" + SecretHelperEnv + " = \"/usr/local/bin/h\"\n", "set"},
		{"[env.refs]\n" + SecretHelperEnv + " = \"keychain:h\"\n", "refs"},
		{"[env]\nblock = [\"" + SecretHelperEnv + "\", \"" + SecretFileStoreEnv + "\", \"EDITOR\"]\n", ""},
	} {
		if err := os.WriteFile(filepath.Join(dir, ".playbook"), []byte("name = \"p\"\n"+c.body), 0o644); err != nil {
			t.Fatal(err)
		}
		m, err := Read(dir)
		if err != nil {
			t.Fatalf("a .playbook with %q: %v", c.body, err)
		}
		if key, field := DeclaredReserved(m.Env); field != c.field || (field != "" && key != SecretHelperEnv) {
			t.Errorf("a .playbook with %q: DeclaredReserved %q %q", c.body, key, field)
		}
		if c.field == "" && (len(m.Env.Block) != 1 || m.Env.Block[0] != "EDITOR") {
			t.Errorf("block read as %q", m.Env.Block)
		}
	}
	// A malformed name in a file is still refused, and not shown.
	if err := os.WriteFile(filepath.Join(dir, ".playbook"), []byte("name = \"p\"\n[env.set]\n\"CPB SECRET\" = \"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(dir); err == nil {
		t.Fatal("a malformed name was read")
	}
}

// The --env-file check names a key only once it is a well-formed name: a
// line that is not one is never echoed, reserved prefix or not.
func TestEnvFileShowsNoMalformedKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "env")
	if err := os.WriteFile(path, []byte(SecretStoreEnvPrefix+"NOTE pasted-text=abc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ParseEnvFile(path)
	if err == nil || strings.Contains(err.Error(), "pasted-text") || strings.Contains(err.Error(), "NOTE") {
		t.Fatalf("%v", err)
	}
}
