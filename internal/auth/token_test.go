package auth

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
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

// hasTokenEntry reports whether env carries any CLAUDE_CODE_OAUTH_TOKEN entry,
// and how many. Count matters: a duplicated key is resolved by exec differently
// across platforms, so "exactly one" is the assertion worth making.
func tokenEntries(env []string) []string {
	var out []string
	for _, e := range env {
		if strings.HasPrefix(e, OAuthTokenEnv+"=") {
			out = append(out, e)
		}
	}
	return out
}

// An isolated playbook must never be launched under the global OAuth token: the
// isolate_auth contract in SPEC-v4.md exists precisely so two accounts can run
// side by side. Before the isolation check was hoisted above token discovery,
// this test failed on both counts -- the token was injected and the shared
// credentials symlink survived, because SyncCredentials (the only caller of
// isAuthIsolated) was skipped entirely.
func TestPrepareLaunchEnvIsolatedPlaybookRejectsTokenFromFile(t *testing.T) {
	t.Setenv("CLAUDE_PLAYBOOKS_ISOLATE_AUTH", "true")

	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "oauth-token")
	if err := os.WriteFile(tokenFile, []byte("sk-ant-oat01-GLOBAL\n"), 0600); err != nil {
		t.Fatal(err)
	}
	os.Unsetenv(OAuthTokenEnv)
	t.Setenv(oauthTokenFileEnv, tokenFile)

	// Stage the state isolation is supposed to undo: a config dir whose
	// credentials file is a symlink into the shared global credentials.
	home := t.TempDir()
	t.Setenv("HOME", home)
	globalDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(globalDir, 0755); err != nil {
		t.Fatal(err)
	}
	globalCreds := filepath.Join(globalDir, CredentialsFileName)
	if err := os.WriteFile(globalCreds, []byte(testCreds), 0600); err != nil {
		t.Fatal(err)
	}
	configDir := t.TempDir()
	linked := filepath.Join(configDir, CredentialsFileName)
	if err := os.Symlink(globalCreds, linked); err != nil {
		t.Fatal(err)
	}

	env, err := PrepareLaunchEnv(configDir)
	if err != nil {
		t.Fatalf("PrepareLaunchEnv: %v", err)
	}

	if got := tokenEntries(env); len(got) != 0 {
		t.Fatalf("isolated playbook must not receive the global token, got %v", got)
	}
	if _, err := os.Lstat(linked); !os.IsNotExist(err) {
		t.Fatalf("shared credentials symlink was not detached (lstat err = %v)", err)
	}
}

// The env-var branch is the subtle half: TokenActive returns inject=="" when the
// token is already exported, relying on the child inheriting it. For an isolated
// playbook, inheriting is exactly the failure -- so it must be stripped, not
// merely left uninjected.
func TestPrepareLaunchEnvIsolatedStripsInheritedToken(t *testing.T) {
	t.Setenv("CLAUDE_PLAYBOOKS_ISOLATE_AUTH", "true")
	t.Setenv(OAuthTokenEnv, "sk-ant-oat01-INHERITED")

	dir := t.TempDir()
	t.Setenv(oauthTokenFileEnv, filepath.Join(dir, "absent"))
	configDir := t.TempDir()

	env, err := PrepareLaunchEnv(configDir)
	if err != nil {
		t.Fatalf("PrepareLaunchEnv: %v", err)
	}

	if got := tokenEntries(env); len(got) != 0 {
		t.Fatalf("inherited token must be stripped for an isolated playbook, got %v", got)
	}
	// The rest of the environment must survive the strip.
	if !slices.Contains(env, "CLAUDE_CONFIG_DIR="+configDir) {
		t.Fatal("CLAUDE_CONFIG_DIR was lost while stripping the token")
	}
	// os.Environ() must not have been mutated for this process.
	if os.Getenv(OAuthTokenEnv) != "sk-ant-oat01-INHERITED" {
		t.Fatal("removeEnv mutated the parent process environment")
	}
}

// A non-isolated playbook whose token is already exported must inherit exactly
// one entry -- not a second, appended copy whose resolution would depend on
// exec's duplicate-key behaviour.
func TestPrepareLaunchEnvInheritedTokenNotDuplicated(t *testing.T) {
	t.Setenv("CLAUDE_PLAYBOOKS_ISOLATE_AUTH", "")
	os.Unsetenv("CLAUDE_PLAYBOOKS_ISOLATE_AUTH")
	t.Setenv(OAuthTokenEnv, "sk-ant-oat01-FROMENV")

	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "oauth-token")
	if err := os.WriteFile(tokenFile, []byte("sk-ant-oat01-FROMFILE\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(oauthTokenFileEnv, tokenFile)
	configDir := t.TempDir()

	env, _ := PrepareLaunchEnv(configDir)

	got := tokenEntries(env)
	if len(got) != 1 {
		t.Fatalf("expected exactly one token entry, got %v", got)
	}
	if got[0] != OAuthTokenEnv+"=sk-ant-oat01-FROMENV" {
		t.Fatalf("exported token must win over the file, got %q", got[0])
	}
}

// `export CLAUDE_CODE_OAUTH_TOKEN=` is present in the environment yet reads as
// unset to os.Getenv, so TokenActive falls through to the token file while the
// inherited empty entry stays in os.Environ(). Appending the file's token
// without stripping first left TWO entries for the key.
//
// This is asserted on PrepareLaunchEnv's return value, not on a child process:
// exec resolves duplicates before the child can observe them (last wins on
// Unix), so a child-level assertion passes either way and proves nothing. The
// duplicate is only visible here -- and removeEnv's own comment rejects relying
// on that resolution order in the first place.
//
// The defect predates the plan-descriptor work; it is fixed alongside it
// because the same function had to change.
func TestPrepareLaunchEnvReplacesEmptyExportedToken(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "oauth-token")
	if err := os.WriteFile(tokenFile, []byte("sk-ant-oat01-FROMFILE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(oauthTokenFileEnv, tokenFile)
	t.Setenv(OAuthTokenEnv, "") // exported, but empty
	t.Setenv("HOME", t.TempDir())

	env, _ := PrepareLaunchEnv(t.TempDir())

	got := tokenEntries(env)
	if len(got) != 1 {
		t.Fatalf("env carries %d %s entries (%q), want exactly 1",
			len(got), OAuthTokenEnv, got)
	}
	if got[0] != OAuthTokenEnv+"=sk-ant-oat01-FROMFILE" {
		t.Fatalf("token entry = %q, want the file's token", got[0])
	}
}
