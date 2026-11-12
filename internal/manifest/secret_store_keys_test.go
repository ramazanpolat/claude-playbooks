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

// CPB_SECRET_HELPER is the person's own: no layer declares it, as a value,
// a reference or an --env-file line, and a .playbook that does is refused
// when it is read. The refusal says where it belongs.
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
	dir := t.TempDir()
	for _, body := range []string{"[env.set]\n" + SecretHelperEnv + " = \"/usr/local/bin/h\"\n", "[env]\nblock = [\"" + SecretHelperEnv + "\"]\n"} {
		if err := os.WriteFile(filepath.Join(dir, ".playbook"), []byte("name = \"p\"\n"+body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Read(dir); err == nil || !strings.Contains(err.Error(), SecretHelperEnv) {
			t.Errorf("a .playbook with %q: %v", body, err)
		}
	}
}
