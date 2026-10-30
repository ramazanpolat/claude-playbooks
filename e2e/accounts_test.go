//go:build !windows

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Accounts (v4.2.0): a launch runs on one account, and where its requests go
// and how they authenticate are derived from it, never from a loose variable.

// writeAccount stores an account file under the playbooks root.
func writeAccount(t *testing.T, root, name, body string) {
	t.Helper()
	dir := filepath.Join(root, ".accounts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

const routerAccount = "provider = \"gateway\"\nbase_url = \"http://localhost:4000/v1\"\nmodel = \"glm-5.3\"\n\n[auth]\nkind = \"auth_token\"\nvalue = \"router-test-key\"\n"

// An isolated playbook on a gateway account: the address, key and model come
// from the account; no Claude credential and nothing inherited survives; no
// Anthropic profile is reachable.
func TestAccountRendersTheLaunch(t *testing.T) {
	root := t.TempDir()
	dir := playbookWithEnv(t, root, "router", "isolated_login = true\n\n[env]\naccounts = [\"glm\"]\n")
	writeAccount(t, root, "glm", routerAccount)

	got := childEnv(t, root, launch{
		env: []string{
			tokenFile(t, "sk-ant-oat01-FROMFILE"), tokenEnv + "=sk-ant-oat01-INHERITED",
			"ANTHROPIC_API_KEY=sk-ant-shell", "ANTHROPIC_BASE_URL=http://shell/v1", "ANTHROPIC_MODEL=shell-model",
		},
		args: []string{"run", "router"},
	})
	if got["ANTHROPIC_BASE_URL"] != "http://localhost:4000/v1" || got["ANTHROPIC_AUTH_TOKEN"] != "router-test-key" || got["ANTHROPIC_DEFAULT_MODEL"] != "glm-5.3" {
		t.Fatalf("the account was not rendered: %v", got)
	}
	for _, k := range []string{tokenEnv, "ANTHROPIC_API_KEY", "ANTHROPIC_MODEL"} {
		if v, present := got[k]; present {
			t.Errorf("%s=%q reached a launch on the gateway account", k, v)
		}
	}
	if got["ANTHROPIC_CONFIG_DIR"] != filepath.Join(dir, ".cpb-anthropic-config") {
		t.Fatalf("ANTHROPIC_CONFIG_DIR = %q", got["ANTHROPIC_CONFIG_DIR"])
	}
	if info, err := os.Stat(got["ANTHROPIC_CONFIG_DIR"]); err != nil || !info.IsDir() {
		t.Fatalf("the empty profile directory: %v", err)
	}
}

// A shared playbook on an account that uses no Claude login: refused, the
// fix named, claude never launched, the machine login untouched.
func TestAccountRefusesASharedLogin(t *testing.T) {
	root := t.TempDir()
	dir := playbookWithEnv(t, root, "router", "[env]\naccounts = [\"glm\"]\n")
	writeAccount(t, root, "glm", routerAccount)
	creds := seedPlaybookCredentials(t, dir)

	out := launchFails(t, root, launch{args: []string{"run", "router"}})
	for _, want := range []string{"shares the machine's Claude login", "account glm", "SET login = 'isolated'"} {
		if !strings.Contains(out, want) {
			t.Errorf("the refusal lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "router-test-key") {
		t.Fatalf("the refusal printed a credential:\n%s", out)
	}
	if _, present := storeKeys(t, creds)["claudeAiOauth"]; !present {
		t.Fatal("the refused launch touched the stored login")
	}
}

// A route stated in one env set, its address and its key together, runs as
// an account derived for the launch: the shell's values never stand in, and
// a playbook that shares the machine's login is refused on it, as on any
// account that uses no Claude login.
func TestOneLayerRouteRuns(t *testing.T) {
	root := t.TempDir()
	seedProfile(t, root, "glm", "[set]\nANTHROPIC_BASE_URL = \"http://localhost:4000/v1\"\nANTHROPIC_AUTH_TOKEN = \"router-test-key\"\nANTHROPIC_DEFAULT_OPUS_MODEL = \"glm-5.3\"\n")
	playbookWithEnv(t, root, "router", "isolated_login = true\n\n[env]\nsets = [\"glm\"]\n\n[env.set]\nANTHROPIC_DEFAULT_HAIKU_MODEL = \"glm-5.3-flash\"\n")
	got := childEnv(t, root, launch{
		env:  []string{tokenFile(t, "sk-ant-oat01-FROMFILE"), "ANTHROPIC_API_KEY=sk-ant-shell", "ANTHROPIC_BASE_URL=http://shell/v1"},
		args: []string{"run", "router"},
	})
	if got["ANTHROPIC_BASE_URL"] != "http://localhost:4000/v1" || got["ANTHROPIC_AUTH_TOKEN"] != "router-test-key" ||
		got["ANTHROPIC_DEFAULT_OPUS_MODEL"] != "glm-5.3" || got["ANTHROPIC_DEFAULT_HAIKU_MODEL"] != "glm-5.3-flash" {
		t.Fatalf("the route was not rendered: %v", got)
	}
	for _, k := range []string{tokenEnv, "ANTHROPIC_API_KEY"} {
		if v, present := got[k]; present {
			t.Errorf("%s=%q reached a launch on a route a layer states", k, v)
		}
	}
	playbookWithEnv(t, root, "shared", "[env]\nsets = [\"glm\"]\n")
	if out := launchFails(t, root, launch{args: []string{"run", "shared"}}); !strings.Contains(out, "shares the machine's Claude login") {
		t.Fatalf("a shared playbook on a route a layer states:\n%s", out)
	}
}

// An address from one layer and its key from another is refused, naming
// both layers and never a value; so is a route in DEFAULTS.
func TestAccountRefusesLooseRouting(t *testing.T) {
	root := t.TempDir()
	seedProfile(t, root, "addr", "[set]\nANTHROPIC_BASE_URL = \"http://localhost:4000/v1\"\n")
	playbookWithEnv(t, root, "router", "isolated_login = true\n\n[env]\nsets = [\"addr\"]\n\n[env.set]\nANTHROPIC_AUTH_TOKEN = \"router-test-key\"\n")
	out := launchFails(t, root, launch{args: []string{"run", "router"}})
	for _, want := range []string{"the address is stated in ENV addr", "ANTHROPIC_AUTH_TOKEN (set by the playbook's own block (SET VAR)"} {
		if !strings.Contains(out, want) {
			t.Errorf("the refusal lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "router-test-key") {
		t.Fatalf("the refusal printed a credential:\n%s", out)
	}
	seedProfile(t, root, "everyone", "[set]\nANTHROPIC_BASE_URL = \"http://localhost:4000/v1\"\nANTHROPIC_AUTH_TOKEN = \"router-test-key\"\n")
	if err := os.WriteFile(filepath.Join(root, ".env-sets", ".defaults"), []byte("everyone\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	playbook(t, root, "plain", true)
	if out := launchFails(t, root, launch{args: []string{"run", "plain"}}); !strings.Contains(out, "DEFAULTS reaches every playbook") {
		t.Fatalf("a route in DEFAULTS:\n%s", out)
	}
	os.Remove(filepath.Join(root, ".env-sets", ".defaults"))

	// The playbook's settings.json is refused too: Claude Code applies its
	// env over the account's.
	dir := playbook(t, root, "settings", true)
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"env":{"ANTHROPIC_BASE_URL":"http://elsewhere/v1"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := launchFails(t, root, launch{args: []string{"run", "settings"}}); !strings.Contains(out, "sets ANTHROPIC_BASE_URL") {
		t.Fatalf("settings.json routing was not refused:\n%s", out)
	}
}

// The implicit account keeps the machine's token file on a shared playbook,
// as before accounts, and still removes inherited routing.
func TestMachineAccountKeepsTheTokenFile(t *testing.T) {
	root := t.TempDir()
	playbook(t, root, "shared", false)
	got := childEnv(t, root, launch{
		env:  []string{tokenFile(t, "sk-ant-oat01-FROMFILE"), tokenEnv + "=sk-ant-oat01-SHELL", "ANTHROPIC_BASE_URL=http://shell/v1", "ANTHROPIC_API_KEY=sk-ant-shell"},
		args: []string{"run", "shared"},
	})
	if got[tokenEnv] != "sk-ant-oat01-FROMFILE" {
		t.Fatalf("token = %q, want the machine's file, never the shell's", got[tokenEnv])
	}
	// With no token file, the shell's token does not stand in.
	got = childEnv(t, root, launch{
		env:  []string{tokenFileEnv + "=" + filepath.Join(t.TempDir(), "absent"), tokenEnv + "=sk-ant-oat01-SHELL"},
		args: []string{"run", "shared"},
	})
	if v, present := got[tokenEnv]; present {
		t.Fatalf("the shell's token reached the launch: %q", v)
	}
	for _, k := range []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_API_KEY"} {
		if v, present := got[k]; present {
			t.Errorf("%s=%q survived on the machine account", k, v)
		}
	}
}

// A model given by reference on a route a layer states is refused, never
// dropped: a model name is not a secret.
func TestLayerRouteRefusesAModelByReference(t *testing.T) {
	root := t.TempDir()
	seedProfile(t, root, "glm", "[set]\nANTHROPIC_BASE_URL = \"http://localhost:4000/v1\"\nANTHROPIC_AUTH_TOKEN = \"router-test-key\"\n\n[refs]\nANTHROPIC_DEFAULT_OPUS_MODEL = \"keychain:model\"\n")
	playbookWithEnv(t, root, "router", "isolated_login = true\n\n[env]\nsets = [\"glm\"]\n")
	if out := launchFails(t, root, launch{args: []string{"run", "router"}}); !strings.Contains(out, "ANTHROPIC_DEFAULT_OPUS_MODEL by reference; a model name is never a secret") {
		t.Fatalf("a model by reference:\n%s", out)
	}
}

// Custom headers stated with a route in the same layer go along with it.
func TestLayerRouteCarriesItsHeaders(t *testing.T) {
	root := t.TempDir()
	seedProfile(t, root, "edge", "[set]\nANTHROPIC_BASE_URL = \"https://router.example.com/v1\"\nANTHROPIC_AUTH_TOKEN = \"router-test-key\"\nANTHROPIC_CUSTOM_HEADERS = \"X-Team: blue\"\n")
	playbookWithEnv(t, root, "router", "isolated_login = true\n\n[env]\nsets = [\"edge\"]\n")
	got := childEnv(t, root, launch{env: []string{"ANTHROPIC_CUSTOM_HEADERS=X-Shell: stale"}, args: []string{"run", "router"}})
	if got["ANTHROPIC_CUSTOM_HEADERS"] != "X-Team: blue" || got["ANTHROPIC_BASE_URL"] != "https://router.example.com/v1" {
		t.Fatalf("the route's headers: %v", got)
	}
}
