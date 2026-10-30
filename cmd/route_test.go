package cmd

import (
	"encoding/json"
	"github.com/ramazanpolat/claude-playbooks/internal/account"
	"os"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/auth"
	"github.com/ramazanpolat/claude-playbooks/internal/envset"
)

func routeOf(t *testing.T, name string) routeJSON {
	t.Helper()
	out := mustStmt(t, "EXPLAIN PLAYBOOK "+name+" --json")
	var v struct {
		Route routeJSON `json:"route"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return v.Route
}

func stubKeychainProbe(t *testing.T, s auth.KeychainState) {
	t.Helper()
	saved := auth.KeychainProbe
	auth.KeychainProbe = func(string, string) auth.KeychainState { return s }
	t.Cleanup(func() { auth.KeychainProbe = saved })
}

// clearRouteEnv empties every route variable the test process inherited,
// so the routes below are the playbooks' own. Empty is as good as unset:
// the route skips an empty value.
func clearRouteEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_MODEL", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN",
		"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY", "ANTHROPIC_FOUNDRY_BASE_URL"} {
		t.Setenv(k, "")
	}
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); defaultModelVar.MatchString(k) {
			t.Setenv(k, "")
		}
	}
}

// The route comes from the account a launch runs on: a base URL, the model
// map, the auth state (never a value), a derived egress, and the account.
func TestExplainRouteAccount(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	clearRouteEnv(t)
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	stubKeychainProbe(t, auth.KeychainAbsent)
	helper, _ := fakeHelper(t)
	t.Setenv(envset.SecretHelperEnv, helper)

	mustStmt(t, "CREATE PLAYBOOK plain SET launcher = ''")
	r := routeOf(t, "plain")
	if r.BaseURL != nil || r.Egress != "anthropic" || r.Auth != "none" || r.Account.Name != "machine" || r.Account.Why != account.WhyInherited || r.Refused != nil {
		t.Fatalf("a plain playbook: %+v", r)
	}

	mustStmt(t, "CREATE ACCOUNT glm SET provider=gateway, base_url=http://127.0.0.1:4000/v1, auth_token FROM keychain:ok/router-token, model=glm-5.3, haiku_model=glm-5.3-flash")
	mustStmt(t, "CREATE PLAYBOOK routed SET launcher = '', login = 'isolated' USE ACCOUNT glm")
	r = routeOf(t, "routed")
	if r.BaseURL == nil || *r.BaseURL != "http://127.0.0.1:4000/v1" || r.Host == nil || *r.Host != "127.0.0.1:4000" ||
		r.Egress != "unknown" || r.Auth != "token-set" || r.Account.Name != "glm" || r.Account.Provider != "gateway" ||
		r.Models["default"] != "glm-5.3" || r.Models["haiku"] != "glm-5.3-flash" {
		t.Fatalf("on the gateway account: %+v", r)
	}
	if out := mustStmt(t, "EXPLAIN PLAYBOOK routed"); !strings.Contains(out, "Account: glm (gateway http://127.0.0.1:4000/v1, auth_token from a reference; the playbook's first)") {
		t.Fatalf("the human EXPLAIN:\n%s", out)
	}

	// Inherited routing is removed at launch, so it is not the route.
	t.Setenv("ANTHROPIC_BASE_URL", "https://u:pw-inherited@proxy.example/v1")
	t.Setenv("ANTHROPIC_API_KEY", "sk-inherited-0123456789")
	if r := routeOf(t, "plain"); r.BaseURL != nil || r.Egress != "anthropic" || r.Auth != "none" {
		t.Fatalf("inherited routing on the machine account: %+v", r)
	}
	out := mustStmt(t, "EXPLAIN PLAYBOOK plain --json")
	if strings.Contains(out, "pw-inherited") || strings.Contains(out, "sk-inherited") {
		t.Fatalf("a credential reached EXPLAIN:\n%s", out)
	}
	t.Setenv("ANTHROPIC_BASE_URL", "")
	t.Setenv("ANTHROPIC_API_KEY", "")

	// A helper account authenticates with its helper.
	mustStmt(t, "CREATE ACCOUNT console SET provider=anthropic, helper=my-key-helper")
	mustStmt(t, "CREATE PLAYBOOK hp SET launcher = '', login = 'isolated' USE ACCOUNT console")
	if r := routeOf(t, "hp"); r.Auth != "key-helper" || r.Egress != "anthropic" {
		t.Fatalf("a helper account: %+v", r)
	}
	// A cloud account's requests do not go to Anthropic's API.
	mustStmt(t, "CREATE ACCOUNT fdy SET provider=foundry, base_url=https://foundry.example.test/anthropic, auth=cloud")
	mustStmt(t, "CREATE PLAYBOOK fp SET launcher = '', login = 'isolated' USE ACCOUNT fdy")
	if r := routeOf(t, "fp"); r.Egress != "unknown" || r.Account.Provider != "foundry" {
		t.Fatalf("a Foundry account: %+v", r)
	}
}

// A launch that would be refused says why, in the route and in the human
// EXPLAIN.
func TestExplainRouteRefused(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	clearRouteEnv(t)
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	stubKeychainProbe(t, auth.KeychainAbsent)
	mustStmt(t, "CREATE ACCOUNT local SET provider=gateway, base_url=http://localhost:4000/v1, auth=none")
	mustStmt(t, "CREATE PLAYBOOK shared SET launcher = '' USE ACCOUNT local")
	if r := routeOf(t, "shared"); r.Refused == nil || !strings.Contains(*r.Refused, "shares the machine's Claude login") || r.Egress != "unknown" {
		t.Fatalf("a shared playbook on a keyless gateway: %+v", r)
	}
	mustStmt(t, "CREATE PLAYBOOK loose SET launcher = ''")
	mustStmt(t, "ALTER PLAYBOOK loose SET VAR ANTHROPIC_BASE_URL=http://user:s3cr3tpw@router.example/v1 AS PLAINTEXT")
	r := routeOf(t, "loose")
	if r.Refused == nil || !strings.Contains(*r.Refused, "ANTHROPIC_BASE_URL, set by the playbook's own block (SET VAR), comes with no key") || strings.Contains(*r.Refused, "s3cr3tpw") {
		t.Fatalf("a loose base URL: %+v", r)
	}
	// With its key, a route a layer states runs, under the account's rules:
	// an address with a password in it is still refused, never printed.
	mustStmt(t, "ALTER PLAYBOOK loose SET VAR ANTHROPIC_AUTH_TOKEN=router-key-1 AS PLAINTEXT")
	if r := routeOf(t, "loose"); r.Refused == nil || strings.Contains(*r.Refused, "s3cr3tpw") || strings.Contains(*r.Refused, "router-key-1") {
		t.Fatalf("a routed block with a password in its address: %+v", r)
	}
	out := mustStmt(t, "EXPLAIN PLAYBOOK loose")
	if !strings.Contains(out, "Account: a launch would be refused") || strings.Contains(out, "s3cr3tpw") {
		t.Fatalf("the human EXPLAIN:\n%s", out)
	}
}
