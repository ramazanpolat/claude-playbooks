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
