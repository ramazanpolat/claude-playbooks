package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The login guard (v4.2.0): a launch whose requests go to an address that is
// not Anthropic's, with no provider key, is refused before anything changes,
// because Claude Code would send the Claude login or token there.

const routerEnv = "[env.set]\nANTHROPIC_BASE_URL = \"http://router.example/v1\"\n"

// A shared playbook routed to another provider with no key: refused, the
// fixes named, claude never launched, the stored login untouched.
func TestLoginGuardRefusesSharedLoginToARouter(t *testing.T) {
	root := t.TempDir()
	dir := playbookWithEnv(t, root, "router", "[env]\n"+routerEnv)
	creds := seedPlaybookCredentials(t, dir)

	out := launchFails(t, root, launch{args: []string{"run", "router"}})
	for _, want := range []string{"router.example", "not Anthropic", "no provider key", "Claude login",
		"SET ANTHROPIC_AUTH_TOKEN FROM", "SET login = 'isolated' BLOCK VAR CLAUDE_CODE_OAUTH_TOKEN"} {
		if !strings.Contains(out, want) {
			t.Errorf("the refusal lacks %q:\n%s", want, out)
		}
	}
	if _, present := storeKeys(t, creds)["claudeAiOauth"]; !present {
		t.Fatal("the refused launch quarantined the stored login")
	}
}

// A token chosen for the launch (here the machine's token file) is named.
func TestLoginGuardNamesTheToken(t *testing.T) {
	root := t.TempDir()
	playbookWithEnv(t, root, "router", "[env]\n"+routerEnv)
	out := launchFails(t, root, launch{
		env:  []string{tokenFile(t, "sk-ant-oat01-FROMFILE")},
		args: []string{"run", "router"},
	})
	if !strings.Contains(out, "Claude token") {
		t.Fatalf("the refusal does not name the token:\n%s", out)
	}
	if strings.Contains(out, "FROMFILE") {
		t.Fatalf("the refusal printed the token:\n%s", out)
	}
}

// The provider's own key lets the launch through, by value here (the
// helper path is covered in cmd); so does an isolated playbook with the
// token blocked and no login of its own.
func TestLoginGuardLetsAProviderKeyOrAnEmptyIsolatedPlaybookThrough(t *testing.T) {
	root := t.TempDir()
	playbookWithEnv(t, root, "keyed", "[env]\n"+routerEnv+"ANTHROPIC_AUTH_TOKEN = \"router-test-key\"\n")
	got := childEnv(t, root, launch{args: []string{"run", "keyed"}})
	if got["ANTHROPIC_BASE_URL"] != "http://router.example/v1" {
		t.Fatalf("keyed launch: %v", got)
	}

	dir := playbookWithEnv(t, root, "empty", "isolated_login = true\n[env]\nblock = [\"CLAUDE_CODE_OAUTH_TOKEN\"]\n\n"+routerEnv)
	childEnv(t, root, launch{env: []string{tokenFile(t, "sk-ant-oat01-FROMFILE")}, args: []string{"run", "empty"}})

	// Once it holds a login of its own, the same launch is refused.
	seedPlaybookCredentials(t, dir)
	if out := launchFails(t, root, launch{args: []string{"run", "empty"}}); !strings.Contains(out, "Claude login") {
		t.Fatalf("an isolated playbook holding a login was let through:\n%s", out)
	}
}

// Anthropic's own address, and no address at all, are never refused.
func TestLoginGuardIgnoresAnthropic(t *testing.T) {
	root := t.TempDir()
	playbookWithEnv(t, root, "direct", "[env]\n[env.set]\nANTHROPIC_BASE_URL = \"https://api.anthropic.com\"\n")
	playbook(t, root, "plain", false)
	for _, name := range []string{"direct", "plain"} {
		childEnv(t, root, launch{env: []string{tokenFile(t, "sk-ant-oat01-FROMFILE")}, args: []string{"run", name}})
	}
}

// settings.json's env block counts: Claude Code applies it over the
// environment, so a base URL there is guarded and a key there is a key.
func TestLoginGuardReadsSettingsEnv(t *testing.T) {
	root := t.TempDir()
	dir := playbook(t, root, "viasettings", false)
	writeSettings := func(body string) {
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeSettings(`{"env":{"ANTHROPIC_BASE_URL":"http://router.example/v1"}}`)
	if out := launchFails(t, root, launch{args: []string{"run", "viasettings"}}); !strings.Contains(out, "router.example") {
		t.Fatalf("a base URL in settings.json was not guarded:\n%s", out)
	}
	writeSettings(`{"env":{"ANTHROPIC_BASE_URL":"http://router.example/v1","ANTHROPIC_AUTH_TOKEN":"router-test-key"}}`)
	childEnv(t, root, launch{args: []string{"run", "viasettings"}})
	writeSettings(`{"apiKeyHelper":"/usr/local/bin/router-key","env":{"ANTHROPIC_BASE_URL":"http://router.example/v1"}}`)
	childEnv(t, root, launch{args: []string{"run", "viasettings"}})
}
