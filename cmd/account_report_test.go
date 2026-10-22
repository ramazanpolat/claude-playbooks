package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envset"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// The account fields (v4.1.0): SELECT, SHOW --json and EXPLAIN say which
// Claude token a launch uses and where it comes from, never the token.
func TestAccountReporting(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	if err := envset.Write(envset.Dir(config.ResolvePlaybooksDir()), &envset.Set{
		Name: "acct-work", Refs: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "keychain:pilot/claude-token-work"},
	}); err != nil {
		t.Fatal(err)
	}
	writePlaybook(t, root, "sre", &manifest.Manifest{Env: &manifest.Env{Sets: []string{"acct-work"}}})
	writePlaybook(t, root, "plain", &manifest.Manifest{})

	out := mustStmt(t, "SELECT name, auth_mode, token_source, token_kind, token_blocked, token_account FROM PLAYBOOKS")
	for _, want := range []string{
		"plain\tshared-login\tnone\t-\tfalse\t-",
		"sre\tplaybook-token\tenv-set:acct-work\treference\tfalse\tacct-work",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("SELECT lacks %q:\n%s", want, out)
		}
	}

	var ex map[string]any
	if err := json.Unmarshal([]byte(mustStmt(t, "EXPLAIN PLAYBOOK sre --json")), &ex); err != nil {
		t.Fatal(err)
	}
	if ex["token_account"] != "acct-work" || ex["auth_mode"] != "playbook-token" {
		t.Fatalf("EXPLAIN --json: %v", ex)
	}
	human := mustStmt(t, "EXPLAIN PLAYBOOK sre")
	if !strings.Contains(human, "Claude token: from env set acct-work (the account, by reference)") {
		t.Fatalf("EXPLAIN lacks the token line:\n%s", human)
	}
	if strings.Contains(out, "claude-token-work") {
		t.Fatalf("SELECT carries the reference:\n%s", out)
	}
}
