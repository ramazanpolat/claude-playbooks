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
		"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY", "ANTHROPIC_FOUNDRY_BASE_URL"} {
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
	mustStmt(t, "CREATE PLAYBOOK plain SET launcher = ''")
	stubKeychainProbe(t, auth.KeychainAbsent)
	r := routeOf(t, "plain")
	if r.BaseURL != nil || r.Host != nil || r.Egress != "anthropic" || r.Auth != "none" || len(r.Models) != 0 {
		t.Fatalf("a plain playbook: %+v", r)
	}

	// A routed playbook with a key, a model map and a password in its URL.
	helper, _ := fakeHelper(t)
	mustStmt(t, "ALTER DEFAULTS SET secret_helper = "+helper)
	mustStmt(t, "CREATE PLAYBOOK routed SET launcher = '' SET login=isolated")
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
	mustStmt(t, "CREATE PLAYBOOK direct SET launcher = ''")
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
	mustStmt(t, "CREATE PLAYBOOK iso SET launcher = '' SET login=isolated")
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
	mustStmt(t, "CREATE PLAYBOOK plain SET launcher = ''")
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
	mustStmt(t, "CREATE PLAYBOOK guarded SET launcher = ''")
	mustStmt(t, "ALTER PLAYBOOK guarded BLOCK VAR ANTHROPIC_BASE_URL ANTHROPIC_API_KEY ANTHROPIC_MODEL ANTHROPIC_DEFAULT_SONNET_MODEL")
	if r := routeOf(t, "guarded"); r.BaseURL != nil || r.Egress != "anthropic" || r.Auth != "none" || len(r.Models) != 0 {
		t.Fatalf("blocked inherited variables: %+v", r)
	}

	// Set by a layer: the layer's value wins over the inherited one.
	mustStmt(t, "CREATE PLAYBOOK direct SET launcher = ''")
	mustStmt(t, "ALTER PLAYBOOK direct SET VAR ANTHROPIC_BASE_URL=https://api.anthropic.com")
	if r := routeOf(t, "direct"); r.Host == nil || *r.Host != "api.anthropic.com" || r.Egress != "anthropic" {
		t.Fatalf("a layer over an inherited base URL: %+v", r)
	}

	// An inherited CLAUDE_CODE_OAUTH_TOKEN is the login report's to judge:
	// an isolated launch removes it, so it does not make the route token-set.
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "inherited-oauth-token")
	mustStmt(t, "CREATE PLAYBOOK iso SET launcher = '' SET login=isolated")
	if r := routeOf(t, "iso"); r.Auth != "none" {
		t.Fatalf("an isolated launch strips the inherited OAuth token: %+v", r)
	}
	if r := routeOf(t, "plain"); r.Auth != "token-set" {
		t.Fatalf("a shared launch uses the inherited OAuth token: %+v", r)
	}
}

// The login guard's verdict (v4.2.0): a shared playbook routed to another
// provider with no key is refused, naming the login; the provider's key
// makes it ok. The URL's userinfo never reaches the reason.
func TestExplainRouteGuard(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	clearRouteEnv(t)
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	stubKeychainProbe(t, auth.KeychainAbsent)
	mustStmt(t, "CREATE PLAYBOOK plain SET launcher = ''")
	if g := routeOf(t, "plain").Guard; g.Verdict != auth.GuardOK {
		t.Fatalf("a plain playbook: %+v", g)
	}
	mustStmt(t, "CREATE PLAYBOOK bare SET launcher = ''")
	mustStmt(t, "ALTER PLAYBOOK bare SET VAR ANTHROPIC_BASE_URL=http://user:s3cr3tpw@router.example/v1 AS PLAINTEXT")
	g := routeOf(t, "bare").Guard
	if g.Verdict != auth.GuardRefused || g.Sends != "login" || g.Host != "router.example" {
		t.Fatalf("a keyless routed playbook: %+v", g)
	}
	if strings.Contains(g.Reason, "s3cr3tpw") {
		t.Fatalf("the reason quotes the userinfo: %s", g.Reason)
	}
	if out := mustStmt(t, "EXPLAIN PLAYBOOK bare"); !strings.Contains(out, "Login guard: a launch would be refused") {
		t.Fatalf("the human EXPLAIN does not say so:\n%s", out)
	}
	mustStmt(t, "ALTER PLAYBOOK bare SET VAR ANTHROPIC_AUTH_TOKEN=router-key AS PLAINTEXT")
	if g := routeOf(t, "bare").Guard; g.Verdict != auth.GuardOK {
		t.Fatalf("with the provider's key: %+v", g)
	}
}

// A CLAUDE_CODE_OAUTH_TOKEN that only DEFAULTS sets is the machine's: an
// isolated launch drops it, so it does not make that route token-set, while
// a shared launch still uses it. One the playbook sets itself counts.
func TestExplainRouteDefaultsTokenIsolated(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	clearRouteEnv(t)
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	stubKeychainProbe(t, auth.KeychainAbsent)
	mustStmt(t, "CREATE ENV machine SET CLAUDE_CODE_OAUTH_TOKEN=machine-token AS PLAINTEXT")
	mustStmt(t, "ALTER DEFAULTS USE ENV machine")
	mustStmt(t, "CREATE PLAYBOOK iso SET launcher = '' SET login=isolated")
	if r := routeOf(t, "iso"); r.Auth != "none" {
		t.Fatalf("an isolated launch drops a DEFAULTS OAuth token: %+v", r)
	}
	mustStmt(t, "CREATE PLAYBOOK shared SET launcher = ''")
	if r := routeOf(t, "shared"); r.Auth != "token-set" {
		t.Fatalf("a shared launch uses a DEFAULTS OAuth token: %+v", r)
	}
	mustStmt(t, "CREATE ENV own SET CLAUDE_CODE_OAUTH_TOKEN=own-token AS PLAINTEXT")
	mustStmt(t, "ALTER PLAYBOOK iso ADD ENV own")
	if r := routeOf(t, "iso"); r.Auth != "token-set" {
		t.Fatalf("an isolated playbook's own env set supplies its token: %+v", r)
	}
}

// A Foundry route is not Anthropic's: its switch or its base URL, from a
// layer or from the environment, makes the egress unknown, as Bedrock's and
// Vertex's do; a switch that is off, or a BLOCK VAR, does not.
func TestExplainRouteFoundry(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	clearRouteEnv(t)
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	stubKeychainProbe(t, auth.KeychainAbsent)
	mustStmt(t, "CREATE PLAYBOOK fsw SET launcher = ''")
	mustStmt(t, "ALTER PLAYBOOK fsw SET VAR CLAUDE_CODE_USE_FOUNDRY=1")
	if r := routeOf(t, "fsw"); r.Egress != "unknown" {
		t.Fatalf("the Foundry switch: %+v", r)
	}
	mustStmt(t, "ALTER PLAYBOOK fsw SET VAR CLAUDE_CODE_USE_FOUNDRY=0")
	if r := routeOf(t, "fsw"); r.Egress != "anthropic" {
		t.Fatalf("the Foundry switch off: %+v", r)
	}
	mustStmt(t, "CREATE PLAYBOOK furl SET launcher = ''")
	mustStmt(t, "ALTER PLAYBOOK furl SET VAR ANTHROPIC_FOUNDRY_BASE_URL=https://foundry.example.test/anthropic")
	if r := routeOf(t, "furl"); r.Egress != "unknown" {
		t.Fatalf("a Foundry base URL: %+v", r)
	}

	// From the environment: exported and not blocked, it is part of the route.
	mustStmt(t, "CREATE PLAYBOOK fenv SET launcher = ''")
	for _, kv := range [][2]string{{"CLAUDE_CODE_USE_FOUNDRY", "true"}, {"ANTHROPIC_FOUNDRY_BASE_URL", "https://foundry.example.test/anthropic"}} {
		t.Setenv(kv[0], kv[1])
		if r := routeOf(t, "fenv"); r.Egress != "unknown" {
			t.Fatalf("exported %s: %+v", kv[0], r)
		}
		mustStmt(t, "ALTER PLAYBOOK fenv BLOCK VAR "+kv[0])
		if r := routeOf(t, "fenv"); r.Egress != "anthropic" {
			t.Fatalf("exported and blocked %s: %+v", kv[0], r)
		}
		t.Setenv(kv[0], "")
	}
}
