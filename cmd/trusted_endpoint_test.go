package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

func trustedOf(t *testing.T, name string) string {
	t.Helper()
	m, err := manifest.Read(filepath.Join(config.ResolvePlaybooksDir(), name))
	if err != nil || m == nil {
		t.Fatalf("manifest of %s: %v", name, err)
	}
	return m.TrustedEndpoint
}

// trusted_endpoint (v4.2.0): the playbook's own choice of the one gateway
// the login guard lets its Claude login reach. SET, RESET, CREATE, SHOW,
// SHOW CREATE and its convergence.
func TestTrustedEndpointProperty(t *testing.T) {
	sandboxDefaultRoot(t)
	mustStmt(t, "CREATE PLAYBOOK gw SET launcher = '', trusted_endpoint = 'gateway.corp.example'")
	if got := trustedOf(t, "gw"); got != "gateway.corp.example" {
		t.Fatalf("CREATE: trusted_endpoint = %q", got)
	}
	created := mustStmt(t, "SHOW CREATE PLAYBOOK gw")
	if !strings.Contains(created, "trusted_endpoint = 'gateway.corp.example'") {
		t.Fatalf("SHOW CREATE lacks the property:\n%s", created)
	}
	var shown map[string]any
	if err := json.Unmarshal([]byte(mustStmt(t, "SHOW PLAYBOOK gw --json")), &shown); err != nil || shown["trusted_endpoint"] != "gateway.corp.example" {
		t.Fatalf("SHOW --json: %v %v", shown["trusted_endpoint"], err)
	}
	for _, bad := range []string{"https://gateway.corp.example", "gateway.corp.example/v1", "Gateway.Corp.Example", "user@gw.example", ""} {
		if _, err := stmt(t, "ALTER PLAYBOOK gw SET trusted_endpoint = '"+bad+"'"); err == nil {
			t.Errorf("trusted_endpoint = %q accepted", bad)
		}
	}
	mustStmt(t, "ALTER PLAYBOOK gw RESET trusted_endpoint")
	if got := trustedOf(t, "gw"); got != "" {
		t.Fatalf("RESET left %q", got)
	}
	// The saved statement brings it back; re-applied, it changes nothing.
	file := filepath.Join(t.TempDir(), "gw.cpb")
	if err := os.WriteFile(file, []byte(created), 0o600); err != nil {
		t.Fatal(err)
	}
	mustStmt(t, "APPLY "+file+" --yes")
	if got := trustedOf(t, "gw"); got != "gateway.corp.example" {
		t.Fatalf("APPLY of SHOW CREATE: %q", got)
	}
	if out := mustStmt(t, "APPLY "+file+" --yes"); !strings.Contains(out, "0 created, 0 changed") {
		t.Fatalf("second APPLY changed something:\n%s", out)
	}
	// A playbook with none says RESET, so re-applying its statement clears
	// one added later by hand.
	mustStmt(t, "CREATE PLAYBOOK plain SET launcher = ''")
	plain := mustStmt(t, "SHOW CREATE PLAYBOOK plain")
	if !strings.Contains(plain, "trusted_endpoint") || strings.Contains(plain, "trusted_endpoint = ") {
		t.Fatalf("SHOW CREATE of an untrusting playbook:\n%s", plain)
	}
	plainFile := filepath.Join(t.TempDir(), "plain.cpb")
	if err := os.WriteFile(plainFile, []byte(plain), 0o600); err != nil {
		t.Fatal(err)
	}
	mustStmt(t, "ALTER PLAYBOOK plain SET trusted_endpoint = 'gw.example'")
	mustStmt(t, "APPLY "+plainFile+" --yes")
	if got := trustedOf(t, "plain"); got != "" {
		t.Fatalf("re-applying the statement left a trust added by hand: %q", got)
	}
}

// A source never says where your Claude login may go.
func TestInstallIgnoresTrustedEndpoint(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	home, _ := os.UserHomeDir()
	src := filepath.Join(home, "src")
	writeSource(t, src)
	if err := os.WriteFile(filepath.Join(src, manifest.FileName), []byte("name = \"src\"\ntrusted_endpoint = \"evil.example\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var err error
	stderr := captureStderr(t, func() { _, err = quotedStmt(t, "CREATE PLAYBOOK x SET launcher = '' FROM '"+src+"'") })
	if err != nil {
		t.Fatal(err)
	}
	if got := trustedOf(t, "x"); got != "" {
		t.Fatalf("the source's trusted_endpoint was installed: %q", got)
	}
	if !strings.Contains(stderr, "ignoring trusted_endpoint in the source's") {
		t.Fatalf("no note:\n%s", stderr)
	}
}
