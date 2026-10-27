package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/account"
	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// The account fields (v4.2.0): SELECT, SHOW --json and EXPLAIN say which
// account a launch runs on and which Claude token it uses, never the token.
func TestAccountReporting(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	if err := account.Write(account.Dir(config.ResolvePlaybooksDir()), &account.Account{
		Name: "acct-work", Provider: account.Anthropic,
		Auth: account.Auth{Kind: account.ClaudeToken, Ref: "keychain:pilot/claude-token-work"},
	}); err != nil {
		t.Fatal(err)
	}
	writePlaybook(t, root, "sre", &manifest.Manifest{Env: &manifest.Env{Accounts: []string{"acct-work"}}})
	writePlaybook(t, root, "plain", &manifest.Manifest{})

	out := mustStmt(t, "SELECT name, token_source, token_kind, account FROM PLAYBOOKS")
	for _, want := range []string{
		"plain\tnone\t-\tmachine",
		"sre\taccount\treference\tacct-work",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("SELECT lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "claude-token-work") {
		t.Fatalf("SELECT carries the reference:\n%s", out)
	}

	var ex struct {
		TokenSource string    `json:"token_source"`
		Route       routeJSON `json:"route"`
	}
	if err := json.Unmarshal([]byte(mustStmt(t, "EXPLAIN PLAYBOOK sre --json")), &ex); err != nil {
		t.Fatal(err)
	}
	if ex.Route.Account.Name != "acct-work" || ex.TokenSource != "account" || ex.Route.Auth != "token-set" {
		t.Fatalf("EXPLAIN --json: %+v", ex)
	}
	human := mustStmt(t, "EXPLAIN PLAYBOOK sre")
	if !strings.Contains(human, "Account: acct-work (anthropic, claude_token from a reference; the playbook's first)") {
		t.Fatalf("EXPLAIN lacks the account line:\n%s", human)
	}
}
