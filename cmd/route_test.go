package cmd

import (
	"encoding/json"
	"os"
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

// clearRouteEnv empties every route variable the test process inherited,
// so the routes below are the playbooks' own. Empty is as good as unset:
// the route skips an empty value.
func clearRouteEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_MODEL", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN",
		"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX"} {
		t.Setenv(k, "")
	}
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); defaultModelVar.MatchString(k) {
			t.Setenv(k, "")
		}
	}
}

// The route from non-secret values only: a base URL without its userinfo,
// the model map, the auth state (never a value) and a derived egress.
func TestExplainRoute(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	clearRouteEnv(t)
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
	mustStmt(t, "CREATE PLAYBOOK routed NO LAUNCHER SETTINGS login=isolated")
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
	mustStmt(t, "CREATE PLAYBOOK iso NO LAUNCHER SETTINGS login=isolated")
	stubKeychainProbe(t, auth.KeychainPresent)
	if r := routeOf(t, "iso"); r.Auth != "oauth-login" {
		t.Fatalf("a Keychain login: %+v", r)
	}
	stubKeychainProbe(t, auth.KeychainUnknown)
	if r := routeOf(t, "iso"); r.Auth != "unknown" {
		t.Fatalf("an unknown login: %+v", r)
	}
}

// A launch starts from the environment it runs in: an exported base URL and
// key that no layer sets or blocks are part of the route; a BLOCK VAR or a
// layer's own value wins over them. The key's value never reaches the output.
func TestExplainRouteInherited(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	clearRouteEnv(t)
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	stubKeychainProbe(t, auth.KeychainAbsent)
	t.Setenv("ANTHROPIC_BASE_URL", "https://u:pw-inherited@proxy.example/v1")
	t.Setenv("ANTHROPIC_API_KEY", "sk-inherited-0123456789")
	t.Setenv("ANTHROPIC_MODEL", "proxy-model")
	t.Setenv("ANTHROPIC_DEFAULT_SONNET_MODEL", "proxy-sonnet")
	mustStmt(t, "CREATE PLAYBOOK plain NO LAUNCHER")
	out := mustStmt(t, "EXPLAIN PLAYBOOK plain --json")
	for _, leak := range []string{"sk-inherited", "0123456789", "pw-inherited"} {
		if strings.Contains(out, leak) {
			t.Fatalf("%q reached the output:\n%s", leak, out)
		}
	}
	r := routeOf(t, "plain")
	if r.BaseURL == nil || *r.BaseURL != "https://proxy.example/v1" || r.Host == nil || *r.Host != "proxy.example" ||
		r.Egress != "unknown" || r.Auth != "token-set" || r.Models["default"] != "proxy-model" || r.Models["sonnet"] != "proxy-sonnet" {
		t.Fatalf("an inherited route: %+v", r)
	}

	// Blocked by the playbook: the launch removes them, so the route is Anthropic's.
	mustStmt(t, "CREATE PLAYBOOK guarded NO LAUNCHER")
	mustStmt(t, "ALTER PLAYBOOK guarded BLOCK VAR ANTHROPIC_BASE_URL ANTHROPIC_API_KEY ANTHROPIC_MODEL ANTHROPIC_DEFAULT_SONNET_MODEL")
	if r := routeOf(t, "guarded"); r.BaseURL != nil || r.Egress != "anthropic" || r.Auth != "none" || len(r.Models) != 0 {
		t.Fatalf("blocked inherited variables: %+v", r)
	}

	// Set by a layer: the layer's value wins over the inherited one.
	mustStmt(t, "CREATE PLAYBOOK direct NO LAUNCHER")
	mustStmt(t, "ALTER PLAYBOOK direct SET VAR ANTHROPIC_BASE_URL=https://api.anthropic.com")
	if r := routeOf(t, "direct"); r.Host == nil || *r.Host != "api.anthropic.com" || r.Egress != "anthropic" {
		t.Fatalf("a layer over an inherited base URL: %+v", r)
	}

	// An inherited CLAUDE_CODE_OAUTH_TOKEN is the login report's to judge:
	// an isolated launch removes it, so it does not make the route token-set.
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "inherited-oauth-token")
	mustStmt(t, "CREATE PLAYBOOK iso NO LAUNCHER SETTINGS login=isolated")
	if r := routeOf(t, "iso"); r.Auth != "none" {
		t.Fatalf("an isolated launch strips the inherited OAuth token: %+v", r)
	}
	if r := routeOf(t, "plain"); r.Auth != "token-set" {
		t.Fatalf("a shared launch uses the inherited OAuth token: %+v", r)
	}
}
