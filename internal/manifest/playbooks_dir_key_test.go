package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
)

// CPB_PLAYBOOKS_DIR is a reserved key: no layer declares it, and the refusal
// says where it belongs. A .playbook that already holds it still reads:
// as a value it is named by DeclaredReserved (a launch refuses it); in a
// block list it has no effect and is dropped.
func TestPlaybooksDirIsReserved(t *testing.T) {
	key := config.PlaybooksDirEnv
	if err := ValidateEnvKey(key); err == nil || !strings.Contains(err.Error(), "own shell") {
		t.Fatalf("ValidateEnvKey: %v", err)
	}
	if err := ValidateRefs(map[string]string{key: "keychain:x"}, nil, nil); err == nil {
		t.Fatal("as a reference: accepted")
	}
	path := filepath.Join(t.TempDir(), "env")
	if err := os.WriteFile(path, []byte(key+"=/srv/registry\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseEnvFile(path); err == nil || !strings.Contains(err.Error(), key) {
		t.Fatalf("in an --env-file: %v", err)
	}
	dir := t.TempDir()
	for _, c := range []struct{ body, field string }{
		{"[env.set]\n" + key + " = \"/srv/registry\"\n", "set"},
		{"[env]\nblock = [\"" + key + "\", \"EDITOR\"]\n", ""},
	} {
		if err := os.WriteFile(filepath.Join(dir, ".playbook"), []byte("name = \"p\"\n"+c.body), 0o644); err != nil {
			t.Fatal(err)
		}
		m, err := Read(dir)
		if err != nil {
			t.Fatalf("a .playbook with %q: %v", c.body, err)
		}
		if k, f := DeclaredReserved(m.Env); f != c.field || (f != "" && k != key) {
			t.Errorf("a .playbook with %q: DeclaredReserved %q %q", c.body, k, f)
		}
		if c.field == "" && (len(m.Env.Block) != 1 || m.Env.Block[0] != "EDITOR") {
			t.Errorf("block read as %q", m.Env.Block)
		}
	}
}
