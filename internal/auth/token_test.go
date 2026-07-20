package auth

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestTokenActive(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "oauth-token")

	// Ensure no ambient env token interferes.
	t.Setenv(OAuthTokenEnv, "")
	os.Unsetenv(OAuthTokenEnv)
	t.Setenv(oauthTokenFileEnv, tokenFile)

	// No file -> not active.
	if inject, active := TokenActive(); active || inject != "" {
		t.Fatalf("no file: expected inactive, got inject=%q active=%v", inject, active)
	}

	// Empty/whitespace file -> not active.
	if err := os.WriteFile(tokenFile, []byte("  \n"), 0600); err != nil {
		t.Fatal(err)
	}
	if inject, active := TokenActive(); active || inject != "" {
		t.Fatalf("empty file: expected inactive, got inject=%q active=%v", inject, active)
	}

	// Non-empty file -> active, token trimmed and returned for injection.
	if err := os.WriteFile(tokenFile, []byte("  sk-ant-oat01-TESTTOKEN\n"), 0600); err != nil {
		t.Fatal(err)
	}
	inject, active := TokenActive()
	if !active {
		t.Fatal("non-empty file: expected active")
	}
	if inject != "sk-ant-oat01-TESTTOKEN" {
		t.Fatalf("expected trimmed token, got %q", inject)
	}

	// Env already set -> active but nothing to inject (children inherit it).
	t.Setenv(OAuthTokenEnv, "sk-ant-oat01-FROMENV")
	inject, active = TokenActive()
	if !active {
		t.Fatal("env set: expected active")
	}
	if inject != "" {
		t.Fatalf("env set: expected no injection, got %q", inject)
	}
}

func TestPrepareLaunchEnvInjectsToken(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "oauth-token")
	configDir := t.TempDir()

	os.Unsetenv(OAuthTokenEnv)
	t.Setenv(oauthTokenFileEnv, tokenFile)
	if err := os.WriteFile(tokenFile, []byte("sk-ant-oat01-XYZ\n"), 0600); err != nil {
		t.Fatal(err)
	}

	env, _ := PrepareLaunchEnv(configDir)

	if !slices.Contains(env, "CLAUDE_CONFIG_DIR="+configDir) {
		t.Fatalf("CLAUDE_CONFIG_DIR not set in env: %v", env)
	}
	if !slices.Contains(env, OAuthTokenEnv+"=sk-ant-oat01-XYZ") {
		t.Fatalf("token not injected into env")
	}
}

func TestPrepareLaunchEnvNoTokenNoInjection(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "oauth-token") // does not exist
	configDir := t.TempDir()

	os.Unsetenv(OAuthTokenEnv)
	t.Setenv(oauthTokenFileEnv, tokenFile)

	env, _ := PrepareLaunchEnv(configDir)

	if !slices.Contains(env, "CLAUDE_CONFIG_DIR="+configDir) {
		t.Fatalf("CLAUDE_CONFIG_DIR not set in env")
	}
	for _, e := range env {
		if len(e) >= len(OAuthTokenEnv)+1 && e[:len(OAuthTokenEnv)+1] == OAuthTokenEnv+"=" {
			t.Fatalf("unexpected token injection with no token file: %q", e)
		}
	}
}
