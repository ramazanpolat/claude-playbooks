package auth

import (
	"os"
	"path/filepath"
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
