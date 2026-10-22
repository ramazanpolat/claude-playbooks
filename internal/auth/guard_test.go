package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func guardOf(t *testing.T, configDir string) GuardResult {
	t.Helper()
	eff, err := EffectiveBlock(configDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	return Guard(configDir, eff, nil)
}

// A token settings.json supplies is sent whatever the layers say: Claude
// Code applies that env block over the launch environment, a blocked
// variable included.
func TestGuardCountsASettingsToken(t *testing.T) {
	configDir := t.TempDir()
	writeManifest(t, configDir, "isolated_login = true\n\n[env]\nblock = [\"CLAUDE_CODE_OAUTH_TOKEN\"]\n")
	settings := `{"env":{"ANTHROPIC_BASE_URL":"http://router.example/v1","CLAUDE_CODE_OAUTH_TOKEN":"sk-ant-oat01-SETTINGS"}}`
	if err := os.WriteFile(filepath.Join(configDir, "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	g := guardOf(t, configDir)
	if g.Verdict != GuardRefused || g.Sends != "token" {
		t.Fatalf("guard = %+v, want refused for the settings token", g)
	}
}

// An isolated playbook whose store is still a link to the shared one: the
// launch detaches the link before Claude Code starts, so the grant it
// reaches is not this playbook's login.
func TestGuardIgnoresALinkIsolationDetaches(t *testing.T) {
	configDir := t.TempDir()
	writeManifest(t, configDir, "isolated_login = true\n\n[env]\nblock = [\"CLAUDE_CODE_OAUTH_TOKEN\"]\n\n[env.set]\nANTHROPIC_BASE_URL = \"http://router.example/v1\"\n")
	shared := filepath.Join(t.TempDir(), CredentialsFileName)
	if err := os.WriteFile(shared, []byte(`{`+grantJSON+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(shared, filepath.Join(configDir, CredentialsFileName)); err != nil {
		t.Fatal(err)
	}
	if g := guardOf(t, configDir); g.Verdict != GuardOK {
		t.Fatalf("guard = %+v, want ok: the shared login is detached at launch", g)
	}
	// The same grant in a file of its own is the playbook's login.
	os.Remove(filepath.Join(configDir, CredentialsFileName))
	writeStore(t, configDir, `{`+grantJSON+`}`)
	if g := guardOf(t, configDir); g.Verdict != GuardRefused || g.Sends != "login" {
		t.Fatalf("guard = %+v, want refused for the own login", g)
	}
}

// trusted_endpoint: the one host the playbook names lets its Claude login
// through, over https only, and only that host.
func TestGuardTrustedEndpoint(t *testing.T) {
	for _, tc := range []struct {
		name, base, trusted, want string
	}{
		{"the trusted host, https", "https://gateway.corp.example/v1", "gateway.corp.example", GuardTrusted},
		{"case does not matter", "https://Gateway.Corp.Example/v1", "gateway.corp.example", GuardTrusted},
		{"with its port", "https://gw.example:8443/v1", "gw.example:8443", GuardTrusted},
		{"another port is another endpoint", "https://gw.example:9443/v1", "gw.example:8443", GuardRefused},
		{"plain http", "http://gateway.corp.example/v1", "gateway.corp.example", GuardRefused},
		{"another host", "https://router.example/v1", "gateway.corp.example", GuardRefused},
		{"a lookalike", "https://gateway.corp.example.evil.example/v1", "gateway.corp.example", GuardRefused},
		{"nothing trusted", "https://gateway.corp.example/v1", "", GuardRefused},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configDir := t.TempDir()
			body := "[env.set]\nANTHROPIC_BASE_URL = \"" + tc.base + "\"\n"
			if tc.trusted != "" {
				body = "trusted_endpoint = \"" + tc.trusted + "\"\n\n" + body
			}
			writeManifest(t, configDir, body)
			g := guardOf(t, configDir)
			if g.Verdict != tc.want {
				t.Fatalf("guard = %+v, want %s", g, tc.want)
			}
			if tc.want == GuardRefused && !strings.Contains(GuardError(g, "pb").Error(), "SET trusted_endpoint = '") {
				t.Fatalf("the refusal does not name the opt-in: %v", GuardError(g, "pb"))
			}
		})
	}
	// An address from a secret reference is never matched: its host is
	// unknown to cpb.
	configDir := t.TempDir()
	writeManifest(t, configDir, "trusted_endpoint = \"gateway.corp.example\"\n\n[env.refs]\nANTHROPIC_BASE_URL = \"keychain:gw-url\"\n")
	if g := guardOf(t, configDir); g.Verdict != GuardRefused {
		t.Fatalf("a referenced address was trusted: %+v", g)
	}
}
