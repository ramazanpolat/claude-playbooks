package auth

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// envValue returns the value of key in env and whether it appears, failing
// when it appears more than once -- a duplicate is exactly the ambiguity
// removeEnv's comment refuses to rely on exec to resolve.
func envValue(t *testing.T, env []string, key string) (string, bool) {
	t.Helper()
	value, found := "", false
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			if found {
				t.Fatalf("%s appears more than once in %v", key, env)
			}
			value, found = v, true
		}
	}
	return value, found
}

func writeManifest(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".playbook"), []byte("name = \"pb\"\n"+body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The auth case the feature exists for: unsetting the token in the manifest
// must take the whole stored-credentials path -- no injection, no quarantine
// -- not merely drop the variable and leave the grant gone.
func TestManifestUnsetTokenTakesStoredCredentialsPath(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "oauth-token")
	if err := os.WriteFile(tokenPath, []byte("sk-ant-oat01-FROMFILE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(oauthTokenFileEnv, tokenPath)
	t.Setenv(OAuthTokenEnv, "sk-ant-oat01-INHERITED")
	t.Setenv("HOME", t.TempDir())

	configDir := t.TempDir()
	writeManifest(t, configDir, "[env]\nunset = [\"CLAUDE_CODE_OAUTH_TOKEN\"]\n")
	store := writeStore(t, configDir, `{`+grantJSON+`,`+mcpJSON+`}`)

	env, _ := PrepareLaunchEnv(configDir)

	if _, present := envValue(t, env, OAuthTokenEnv); present {
		t.Fatalf("token reached the child despite the manifest unset: %v", env)
	}
	if _, present := readStore(t, store)["claudeAiOauth"]; !present {
		t.Fatal("stored grant was quarantined although the token is inactive for this playbook; " +
			"nothing would authenticate the child")
	}
	if _, present := envValue(t, env, SubscriptionTypeEnv); present {
		t.Fatalf("plan descriptor injected on the stored-credentials path: %v", env)
	}
}

// Setting the token in the manifest is a per-install token: it is what gets
// injected, and the token path (quarantine included) applies.
func TestManifestSetTokenOverridesFile(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "oauth-token")
	if err := os.WriteFile(tokenPath, []byte("sk-ant-oat01-FROMFILE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(oauthTokenFileEnv, tokenPath)
	t.Setenv(OAuthTokenEnv, "sk-ant-oat01-INHERITED")
	t.Setenv("HOME", t.TempDir())

	configDir := t.TempDir()
	writeManifest(t, configDir, "[env.set]\nCLAUDE_CODE_OAUTH_TOKEN = \"sk-ant-oat01-PERINSTALL\"\n")
	store := writeStore(t, configDir, `{`+grantJSON+`,`+mcpJSON+`}`)

	env, _ := PrepareLaunchEnv(configDir)

	if got, _ := envValue(t, env, OAuthTokenEnv); got != "sk-ant-oat01-PERINSTALL" {
		t.Fatalf("token = %q, want the manifest's", got)
	}
	if _, present := readStore(t, store)["claudeAiOauth"]; present {
		t.Fatal("stored grant survived a token-auth launch")
	}
}

// Generic variables: set overrides an inherited value, unset removes one, and
// CLAUDE_CONFIG_DIR is bound exactly once regardless of what was inherited.
func TestManifestEnvSetAndUnset(t *testing.T) {
	t.Setenv(oauthTokenFileEnv, filepath.Join(t.TempDir(), "absent"))
	os.Unsetenv(OAuthTokenEnv)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ANTHROPIC_BASE_URL", "http://shell/v1")
	t.Setenv("NOISY", "1")
	t.Setenv("CLAUDE_CONFIG_DIR", "/inherited")

	configDir := t.TempDir()
	writeManifest(t, configDir, "[env]\nunset = [\"NOISY\"]\n\n[env.set]\nANTHROPIC_BASE_URL = \"http://proxy/v1\"\nNEW_VAR = \"yes\"\n")

	env, err := PrepareLaunchEnv(configDir)
	if err != nil {
		t.Fatalf("advisory error: %v", err)
	}

	if got, _ := envValue(t, env, "ANTHROPIC_BASE_URL"); got != "http://proxy/v1" {
		t.Fatalf("ANTHROPIC_BASE_URL = %q, want the manifest's", got)
	}
	if got, _ := envValue(t, env, "NEW_VAR"); got != "yes" {
		t.Fatalf("NEW_VAR = %q", got)
	}
	if _, present := envValue(t, env, "NOISY"); present {
		t.Fatalf("NOISY survived the manifest unset: %v", env)
	}
	if got, _ := envValue(t, env, "CLAUDE_CONFIG_DIR"); got != configDir {
		t.Fatalf("CLAUDE_CONFIG_DIR = %q, want %q", got, configDir)
	}
}

// Isolation still wins over the token, and the manifest overrides still
// apply on top of it.
func TestManifestEnvAppliesToIsolatedPlaybook(t *testing.T) {
	t.Setenv(oauthTokenFileEnv, filepath.Join(t.TempDir(), "absent"))
	t.Setenv(OAuthTokenEnv, "sk-ant-oat01-INHERITED")
	t.Setenv("HOME", t.TempDir())

	configDir := t.TempDir()
	writeManifest(t, configDir, "isolate_auth = true\n\n[env.set]\nANTHROPIC_BASE_URL = \"http://proxy/v1\"\n")

	env, _ := PrepareLaunchEnv(configDir)

	if slices.ContainsFunc(env, func(kv string) bool { return strings.HasPrefix(kv, OAuthTokenEnv+"=") }) {
		t.Fatalf("isolated playbook received the inherited token: %v", env)
	}
	if got, _ := envValue(t, env, "ANTHROPIC_BASE_URL"); got != "http://proxy/v1" {
		t.Fatalf("ANTHROPIC_BASE_URL = %q", got)
	}
}

// A manifest that cannot be parsed must not abort the launch; it is reported
// through the advisory error and treated as declaring nothing.
func TestUnreadableManifestIsAdvisory(t *testing.T) {
	t.Setenv(oauthTokenFileEnv, filepath.Join(t.TempDir(), "absent"))
	os.Unsetenv(OAuthTokenEnv)
	t.Setenv("HOME", t.TempDir())

	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, ".playbook"), []byte("name = [broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	env, err := PrepareLaunchEnv(configDir)
	if err == nil {
		t.Fatal("invalid manifest went unreported")
	}
	if got, _ := envValue(t, env, "CLAUDE_CONFIG_DIR"); got != configDir {
		t.Fatalf("launch env unusable after manifest error: %v", env)
	}
}
