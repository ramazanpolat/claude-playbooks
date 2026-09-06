package auth

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envprofile"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
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

// Profiles are resolved from the playbooks root at launch and flattened
// under the playbook's own entries; the token decision sees the result.
func TestManifestProfilesApplyAtLaunch(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "oauth-token")
	if err := os.WriteFile(tokenPath, []byte("sk-ant-oat01-FROMFILE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(oauthTokenFileEnv, tokenPath)
	os.Unsetenv(OAuthTokenEnv)
	t.Setenv("HOME", t.TempDir())

	root := t.TempDir()
	t.Setenv("CLAUDE_PLAYBOOKS_DIR", root)
	config.PlaybooksDir = ""
	if err := envprofile.Write(envprofile.Dir(root), &envprofile.Profile{
		Name:  "account",
		Set:   map[string]string{"ANTHROPIC_BASE_URL": "http://profile/v1", "MODEL": "profile"},
		Unset: []string{OAuthTokenEnv},
	}); err != nil {
		t.Fatal(err)
	}

	configDir := filepath.Join(root, "pb")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, configDir, "[env]\nprofiles = [\"account\"]\n\n[env.set]\nMODEL = \"own\"\n")
	store := writeStore(t, configDir, `{`+grantJSON+`}`)

	env, err := PrepareLaunchEnv(configDir)
	if err != nil {
		t.Fatalf("advisory error: %v", err)
	}
	if _, present := envValue(t, env, OAuthTokenEnv); present {
		t.Fatalf("profile's token unset was not honored: %v", env)
	}
	if _, present := readStore(t, store)["claudeAiOauth"]; !present {
		t.Fatal("grant quarantined although the profile makes the token inactive")
	}
	if got, _ := envValue(t, env, "ANTHROPIC_BASE_URL"); got != "http://profile/v1" {
		t.Fatalf("ANTHROPIC_BASE_URL = %q", got)
	}
	if got, _ := envValue(t, env, "MODEL"); got != "own" {
		t.Fatalf("playbook's own entry did not win over the profile: MODEL = %q", got)
	}
}

func TestMissingProfileIsReportedTyped(t *testing.T) {
	t.Setenv(oauthTokenFileEnv, filepath.Join(t.TempDir(), "absent"))
	os.Unsetenv(OAuthTokenEnv)
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	t.Setenv("CLAUDE_PLAYBOOKS_DIR", root)
	config.PlaybooksDir = ""

	configDir := filepath.Join(root, "pb")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, configDir, "[env]\nprofiles = [\"ghost\"]\n")

	_, err := PrepareLaunchEnv(configDir)
	var missing *envprofile.MissingError
	if !errors.As(err, &missing) || missing.Name != "ghost" {
		t.Fatalf("err = %v, want *envprofile.MissingError", err)
	}
}

// An isolated playbook ignores the machine-global token, but a token its own
// manifest sets is that playbook's choice: injected, with the stored grant
// quarantined exactly as on the shared token path.
func TestIsolatedPlaybookHonoursManifestToken(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "oauth-token")
	if err := os.WriteFile(tokenPath, []byte("sk-ant-oat01-GLOBAL\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(oauthTokenFileEnv, tokenPath)
	t.Setenv(OAuthTokenEnv, "sk-ant-oat01-INHERITED")
	t.Setenv(SubscriptionTypeEnv, "max")
	t.Setenv("HOME", t.TempDir())

	configDir := t.TempDir()
	writeManifest(t, configDir, "isolate_auth = true\n\n[env.set]\nCLAUDE_CODE_OAUTH_TOKEN = \"sk-ant-oat01-OWN\"\n")
	store := writeStore(t, configDir, `{`+grantJSON+`,`+mcpJSON+`}`)

	env, _ := PrepareLaunchEnv(configDir)

	if got, _ := envValue(t, env, OAuthTokenEnv); got != "sk-ant-oat01-OWN" {
		t.Fatalf("token = %q, want the manifest's own", got)
	}
	if _, present := envValue(t, env, SubscriptionTypeEnv); present {
		t.Fatalf("global plan descriptor leaked into an isolated playbook: %v", env)
	}
	keys := readStore(t, store)
	if _, present := keys["claudeAiOauth"]; present {
		t.Fatal("stored grant survived a token-auth launch of an isolated playbook")
	}
	if _, present := keys["mcpOAuth"]; !present {
		t.Fatal("MCP logins destroyed alongside the grant")
	}
}

// A broken manifest in a subdir does not switch the install root's
// isolation off: the token must still be stripped.
func TestIsolationSurvivesBrokenSubdirManifest(t *testing.T) {
	t.Setenv(oauthTokenFileEnv, filepath.Join(t.TempDir(), "absent"))
	t.Setenv(OAuthTokenEnv, "sk-ant-oat01-INHERITED")
	t.Setenv("HOME", t.TempDir())

	root := t.TempDir()
	sub := filepath.Join(root, "playbook")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".playbook"), []byte("name = \"pb\"\nisolate_auth = true\nsubdir = \"playbook\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, ".playbook"), []byte("= [\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	env, err := PrepareLaunchEnv(sub)
	if _, present := envValue(t, env, OAuthTokenEnv); present {
		t.Fatalf("broken subdir manifest switched isolation off: %v", env)
	}
	if err == nil {
		t.Fatal("the broken manifest went unreported")
	}
}

// A profile error refuses the launch; the refusal must come BEFORE any
// credential mutation, or a broken profile meant to unset the token costs
// the playbook its stored grant on a launch that never happens.
func TestProfileErrorStopsBeforeAuthMutation(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "oauth-token")
	if err := os.WriteFile(tokenPath, []byte("sk-ant-oat01-FROMFILE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(oauthTokenFileEnv, tokenPath)
	os.Unsetenv(OAuthTokenEnv)
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	t.Setenv("CLAUDE_PLAYBOOKS_DIR", root)
	config.PlaybooksDir = ""
	if err := os.MkdirAll(envprofile.Dir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(envprofile.Dir(root), "broken.toml"), []byte("= [\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	configDir := filepath.Join(root, "pb")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, configDir, "[env]\nprofiles = [\"broken\"]\n")
	store := writeStore(t, configDir, `{`+grantJSON+`}`)

	env, err := PrepareLaunchEnv(configDir)
	if !errors.Is(err, envprofile.ErrProfile) {
		t.Fatalf("err = %v, want ErrProfile", err)
	}
	if _, present := readStore(t, store)["claudeAiOauth"]; !present {
		t.Fatal("stored grant quarantined although the launch is refused")
	}
	if got, _ := envValue(t, env, "CLAUDE_CONFIG_DIR"); got != configDir {
		t.Fatalf("returned env not well-formed: %v", env)
	}
}

// One-off layers sit on top of the playbook's block, in command-line order,
// and drive the token decision like the manifest would -- without writing
// anything.
func TestOneOffLayersApplyOnTopOfTheBlock(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "oauth-token")
	if err := os.WriteFile(tokenPath, []byte("sk-ant-oat01-FROMFILE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(oauthTokenFileEnv, tokenPath)
	os.Unsetenv(OAuthTokenEnv)
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	t.Setenv("CLAUDE_PLAYBOOKS_DIR", root)
	config.PlaybooksDir = ""
	if err := envprofile.Write(envprofile.Dir(root), &envprofile.Profile{
		Name: "work", Set: map[string]string{"MODEL": "profile", "FROM_PROFILE": "yes"}, Unset: []string{OAuthTokenEnv},
	}); err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Join(root, "pb")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, configDir, "[env.set]\nMODEL = \"manifest\"\nKEEP = \"manifest\"\n")
	store := writeStore(t, configDir, `{`+grantJSON+`}`)
	before, _ := os.ReadFile(filepath.Join(configDir, ".playbook"))

	layers := []*manifest.Env{
		{Profiles: []string{"work"}},
		{Set: map[string]string{"MODEL": "flag"}},
	}
	env, err := PrepareLaunchEnvWith(configDir, layers)
	if err != nil {
		t.Fatalf("advisory error: %v", err)
	}
	if got, _ := envValue(t, env, "MODEL"); got != "flag" {
		t.Fatalf("MODEL = %q, want the last layer's", got)
	}
	if got, _ := envValue(t, env, "FROM_PROFILE"); got != "yes" {
		t.Fatalf("one-off profile not applied: %v", env)
	}
	if got, _ := envValue(t, env, "KEEP"); got != "manifest" {
		t.Fatalf("manifest entry lost: KEEP=%q", got)
	}
	if _, present := envValue(t, env, OAuthTokenEnv); present {
		t.Fatal("one-off profile's token unset was not honoured")
	}
	if _, present := readStore(t, store)["claudeAiOauth"]; !present {
		t.Fatal("grant quarantined although the one-off layer made the token inactive")
	}
	if after, _ := os.ReadFile(filepath.Join(configDir, ".playbook")); string(after) != string(before) {
		t.Fatal("a one-off layer was written into the manifest")
	}

	// A one-off profile that does not exist refuses, before any mutation.
	_, err = PrepareLaunchEnvWith(configDir, []*manifest.Env{{Profiles: []string{"ghost"}}})
	if !errors.Is(err, envprofile.ErrProfile) {
		t.Fatalf("missing one-off profile: err = %v", err)
	}
}

// An OWN token (manifest or profile) means another account: the plan
// descriptors read from the global store must not be injected, and inherited
// ones are stripped; the block may set its own.
func TestOwnTokenDropsGlobalPlanDescriptors(t *testing.T) {
	t.Setenv(oauthTokenFileEnv, filepath.Join(t.TempDir(), "absent"))
	os.Unsetenv(OAuthTokenEnv)
	t.Setenv(SubscriptionTypeEnv, "max")
	t.Setenv(RateLimitTierEnv, "default_claude_max_20x")
	home := t.TempDir()
	t.Setenv("HOME", home)
	global := filepath.Join(home, ".claude")
	if err := os.MkdirAll(global, 0o755); err != nil {
		t.Fatal(err)
	}
	writeStore(t, global, `{"claudeAiOauth":{"accessToken":"g","subscriptionType":"max","rateLimitTier":"default_claude_max_20x"}}`)
	root := t.TempDir()
	t.Setenv("CLAUDE_PLAYBOOKS_DIR", root)
	config.PlaybooksDir = ""

	configDir := filepath.Join(root, "pb")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, configDir, "[env.set]\nCLAUDE_CODE_OAUTH_TOKEN = \"sk-ant-oat01-METU\"\n")
	env, _ := PrepareLaunchEnv(configDir)
	if got, _ := envValue(t, env, OAuthTokenEnv); got != "sk-ant-oat01-METU" {
		t.Fatalf("token = %q", got)
	}
	for _, key := range []string{SubscriptionTypeEnv, RateLimitTierEnv} {
		if v, present := envValue(t, env, key); present {
			t.Fatalf("%s=%q leaked into an own-token launch", key, v)
		}
	}

	// The block may supply the other account's descriptor itself.
	writeManifest(t, configDir, "[env.set]\nCLAUDE_CODE_OAUTH_TOKEN = \"sk-ant-oat01-METU\"\nCLAUDE_CODE_SUBSCRIPTION_TYPE = \"pro\"\n")
	env, _ = PrepareLaunchEnv(configDir)
	if got, _ := envValue(t, env, SubscriptionTypeEnv); got != "pro" {
		t.Fatalf("block-set descriptor lost: %q", got)
	}

	// The machine-global token still gets the global descriptors.
	writeManifest(t, configDir, "name = \"pb\"\n")
	tf := filepath.Join(t.TempDir(), "oauth-token")
	if err := os.WriteFile(tf, []byte("sk-ant-oat01-GLOBAL\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(oauthTokenFileEnv, tf)
	os.Unsetenv(SubscriptionTypeEnv)
	os.Unsetenv(RateLimitTierEnv)
	env, _ = PrepareLaunchEnv(configDir)
	if got, _ := envValue(t, env, SubscriptionTypeEnv); got != "max" {
		t.Fatalf("global token lost its descriptors: %q", got)
	}
}

// The registry default profile is the bottom layer of every launch, with or
// without a manifest, and drives the token decision like any other layer.
func TestRegistryDefaultProfileAppliesToEveryLaunch(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "oauth-token")
	if err := os.WriteFile(tokenPath, []byte("sk-ant-oat01-FROMFILE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(oauthTokenFileEnv, tokenPath)
	os.Unsetenv(OAuthTokenEnv)
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	t.Setenv("CLAUDE_PLAYBOOKS_DIR", root)
	config.PlaybooksDir = ""
	if err := envprofile.Write(envprofile.Dir(root), &envprofile.Profile{Name: "base", Set: map[string]string{"FROM_DEFAULT": "yes", "MODEL": "default"}, Unset: []string{OAuthTokenEnv}}); err != nil {
		t.Fatal(err)
	}
	if err := envprofile.SetDefault(envprofile.Dir(root), "base"); err != nil {
		t.Fatal(err)
	}

	// No manifest at all.
	plain := filepath.Join(root, "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	store := writeStore(t, plain, `{`+grantJSON+`}`)
	env, err := PrepareLaunchEnv(plain)
	if err != nil {
		t.Fatalf("advisory: %v", err)
	}
	if got, _ := envValue(t, env, "FROM_DEFAULT"); got != "yes" {
		t.Fatalf("default not applied without a manifest: %v", env)
	}
	if _, present := envValue(t, env, OAuthTokenEnv); present {
		t.Fatal("default's token unset ignored")
	}
	if _, present := readStore(t, store)["claudeAiOauth"]; !present {
		t.Fatal("grant quarantined although the default made the token inactive")
	}

	// A manifest with its own entry wins over the default.
	own := filepath.Join(root, "own")
	if err := os.MkdirAll(own, 0o755); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, own, "[env.set]\nMODEL = \"own\"\n")
	env, _ = PrepareLaunchEnv(own)
	if got, _ := envValue(t, env, "MODEL"); got != "own" {
		t.Fatalf("MODEL = %q, want the playbook's", got)
	}
	if got, _ := envValue(t, env, "FROM_DEFAULT"); got != "yes" {
		t.Fatal("default layer lost under a manifest")
	}

	// Inspect sees the default too.
	if r := Inspect("plain", plain, time.Now()); r.Mode != ModeOwnLogin {
		t.Fatalf("inspect mode = %s, want own-login from the default's unset", r.Mode)
	}
}
