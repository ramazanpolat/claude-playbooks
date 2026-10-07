package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/auth"
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

// The route from non-secret values only: a base URL without its userinfo,
// the model map, the auth state (never a value) and a derived egress.
func TestExplainRoute(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	mustStmt(t, "CREATE PLAYBOOK plain NO LAUNCHER")
	stubKeychainProbe(t, auth.KeychainAbsent)
	r := routeOf(t, "plain")
	if r.BaseURL != nil || r.Host != nil || r.Egress != "anthropic" || r.Auth != "none" || len(r.Models) != 0 {
		t.Fatalf("a plain playbook: %+v", r)
	}

	// A routed playbook with a key, a model map and a password in its URL.
	helper, _ := fakeHelper(t)
	mustStmt(t, "ALTER DEFAULTS SET SECRET HELPER "+helper)
	mustStmt(t, "CREATE PLAYBOOK routed NO LAUNCHER ISOLATED LOGIN")
	mustStmt(t, "ALTER PLAYBOOK routed SET VAR ANTHROPIC_BASE_URL=http://user:s3cr3tpw@127.0.0.1:20128/v1 AS PLAINTEXT")
	mustStmt(t, "ALTER PLAYBOOK routed SET VAR ANTHROPIC_AUTH_TOKEN FROM keychain:ok/router-token")
	mustStmt(t, "ALTER PLAYBOOK routed SET VAR ANTHROPIC_MODEL=glm-5.3 ANTHROPIC_DEFAULT_OPUS_MODEL=evren/glm-5.3 ANTHROPIC_DEFAULT_HAIKU_MODEL=evren/qwen")
	out := mustStmt(t, "EXPLAIN PLAYBOOK routed --json")
	if strings.Contains(out, "s3cr3tpw") || strings.Contains(out, "user:") {
		t.Fatalf("the URL's userinfo reached the output:\n%s", out)
	}
	r = routeOf(t, "routed")
	if r.BaseURL == nil || *r.BaseURL != "http://127.0.0.1:20128/v1" || r.Host == nil || *r.Host != "127.0.0.1:20128" ||
		r.Egress != "unknown" || r.Auth != "token-set" ||
		r.Models["default"] != "glm-5.3" || r.Models["opus"] != "evren/glm-5.3" || r.Models["haiku"] != "evren/qwen" {
		t.Fatalf("a routed playbook: %+v", r)
	}

	// Anthropic's own endpoint over https is anthropic; a Bedrock switch is not.
	mustStmt(t, "CREATE PLAYBOOK direct NO LAUNCHER")
	mustStmt(t, "ALTER PLAYBOOK direct SET VAR ANTHROPIC_BASE_URL=https://api.anthropic.com")
	if r := routeOf(t, "direct"); r.Egress != "anthropic" {
		t.Fatalf("api.anthropic.com: %+v", r)
	}
	mustStmt(t, "ALTER PLAYBOOK direct SET VAR CLAUDE_CODE_USE_BEDROCK=1")
	if r := routeOf(t, "direct"); r.Egress != "unknown" {
		t.Fatalf("bedrock: %+v", r)
	}

	// The login, as auth status reports it: a Keychain item is oauth-login,
	// an unanswerable probe is unknown, never none.
	mustStmt(t, "CREATE PLAYBOOK iso NO LAUNCHER ISOLATED LOGIN")
	stubKeychainProbe(t, auth.KeychainPresent)
	if r := routeOf(t, "iso"); r.Auth != "oauth-login" {
		t.Fatalf("a Keychain login: %+v", r)
	}
	stubKeychainProbe(t, auth.KeychainUnknown)
	if r := routeOf(t, "iso"); r.Auth != "unknown" {
		t.Fatalf("an unknown login: %+v", r)
	}
}
