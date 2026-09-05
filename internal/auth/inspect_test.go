package auth

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envprofile"
)

func inspectFixture(t *testing.T) (root, configDir string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	root = filepath.Join(home, ".claude-playbooks")
	configDir = filepath.Join(root, "pb")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_PLAYBOOKS_DIR", root)
	config.PlaybooksDir = ""
	os.Unsetenv(OAuthTokenEnv)
	t.Setenv(oauthTokenFileEnv, filepath.Join(home, "no-token"))
	return root, configDir
}

func TestInspectModes(t *testing.T) {
	now := time.Now()
	t.Run("shared login", func(t *testing.T) {
		_, dir := inspectFixture(t)
		if r := Inspect("pb", dir, now); r.Mode != ModeSharedLogin || r.Store != StoreAbsent || r.NeedsAttention() != "no login" {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("token file", func(t *testing.T) {
		_, dir := inspectFixture(t)
		tf := filepath.Join(t.TempDir(), "oauth-token")
		if err := os.WriteFile(tf, []byte("sk-ant-oat01-X\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(oauthTokenFileEnv, tf)
		r := Inspect("pb", dir, now)
		if r.Mode != ModeToken || r.TokenFile != tf || r.NeedsAttention() != "" {
			t.Fatalf("%+v", r)
		}
		// ~/.claude is not launched by claude-playbook: the token FILE does
		// not make it token mode, only the exported variable does.
		home, _ := os.UserHomeDir()
		global := filepath.Join(home, ".claude")
		if err := os.MkdirAll(global, 0o755); err != nil {
			t.Fatal(err)
		}
		if g := InspectGlobal(global, now); g.Mode != ModeSharedLogin {
			t.Fatalf("global mode = %s, want shared-login", g.Mode)
		}
		t.Setenv(OAuthTokenEnv, "sk-ant-oat01-ENV")
		if g := InspectGlobal(global, now); g.Mode != ModeToken {
			t.Fatalf("global mode with exported token = %s, want token", g.Mode)
		}
	})
	t.Run("own login via manifest unset", func(t *testing.T) {
		_, dir := inspectFixture(t)
		writeManifest(t, dir, "[env]\nunset = [\"CLAUDE_CODE_OAUTH_TOKEN\"]\n")
		t.Setenv(OAuthTokenEnv, "sk-ant-oat01-ENV")
		if r := Inspect("pb", dir, now); r.Mode != ModeOwnLogin {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("own token via profile", func(t *testing.T) {
		root, dir := inspectFixture(t)
		if err := envprofile.Write(envprofile.Dir(root), &envprofile.Profile{Name: "acct", Set: map[string]string{OAuthTokenEnv: "sk-ant-oat01-OWN"}}); err != nil {
			t.Fatal(err)
		}
		writeManifest(t, dir, "[env]\nprofiles = [\"acct\"]\n")
		if r := Inspect("pb", dir, now); r.Mode != ModeOwnToken {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("isolated", func(t *testing.T) {
		_, dir := inspectFixture(t)
		writeManifest(t, dir, "isolate_auth = true\n")
		if r := Inspect("pb", dir, now); r.Mode != ModeIsolated {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("profile error", func(t *testing.T) {
		_, dir := inspectFixture(t)
		writeManifest(t, dir, "[env]\nprofiles = [\"ghost\"]\n")
		r := Inspect("pb", dir, now)
		if r.Mode != ModeError || r.ModeError == "" || r.NeedsAttention() != "launch refused" {
			t.Fatalf("%+v", r)
		}
	})
}

func TestInspectStoreAndDaemon(t *testing.T) {
	now := time.Now()
	_, dir := inspectFixture(t)
	home, _ := os.UserHomeDir()
	global := filepath.Join(home, ".claude")
	if err := os.MkdirAll(global, 0o755); err != nil {
		t.Fatal(err)
	}
	exp := now.Add(6 * time.Hour)
	store := writeStore(t, global, `{"claudeAiOauth":{"accessToken":"x","refreshToken":"r","expiresAt":`+itoa(exp.UnixMilli())+`}}`)
	if err := os.Symlink(store, filepath.Join(dir, CredentialsFileName)); err != nil {
		t.Fatal(err)
	}

	r := Inspect("pb", dir, now)
	if r.Store != StoreSymlink || r.StoreTarget != store || !r.HasGrant || r.Expired {
		t.Fatalf("%+v", r)
	}
	if got := r.ExpiresAt.Sub(now).Round(time.Minute); got != 6*time.Hour {
		t.Fatalf("expires in %v", got)
	}
	if r.NeedsAttention() != "" {
		t.Fatalf("healthy store flagged: %q", r.NeedsAttention())
	}

	// A daemon marker OLDER than the current grant's refresh instant is a
	// healed one; a NEWER one means re-auth is still required.
	stale := exp.Add(-daemonRefreshLead).Add(-time.Hour)
	if err := os.WriteFile(filepath.Join(dir, "daemon-auth-status.json"), []byte(`{"status":"auth_required","since":`+itoa(stale.UnixMilli())+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := Inspect("pb", dir, now); r.ReauthRequired || r.DaemonStatus != "auth_required" {
		t.Fatalf("stale marker treated as live: %+v", r)
	}
	fresh := exp.Add(-daemonRefreshLead).Add(time.Second)
	if err := os.WriteFile(filepath.Join(dir, "daemon-auth-status.json"), []byte(`{"status":"auth_required","since":`+itoa(fresh.UnixMilli())+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := Inspect("pb", dir, now); !r.ReauthRequired || r.NeedsAttention() != "re-auth required" {
		t.Fatalf("live marker missed: %+v", r)
	}

	// Expired grant.
	if err := os.WriteFile(store, []byte(`{"claudeAiOauth":{"accessToken":"x","expiresAt":`+itoa(now.Add(-time.Minute).UnixMilli())+`}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(dir, "daemon-auth-status.json"))
	if r := Inspect("pb", dir, now); !r.Expired || !strings.HasPrefix(r.NeedsAttention(), "grant expired") {
		t.Fatalf("%+v", r)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// A profile that fails to parse must not echo its content into the report:
// the offending text may be a credential.
func TestInspectSanitizesProfileErrors(t *testing.T) {
	root, dir := inspectFixture(t)
	if err := os.MkdirAll(envprofile.Dir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(envprofile.Dir(root), "bad.toml"), []byte("[set]\nKEY = sk-ant-SECRETVALUE-unquoted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, dir, "[env]\nprofiles = [\"bad\"]\n")
	r := Inspect("pb", dir, time.Now())
	if r.Mode != ModeError {
		t.Fatalf("%+v", r)
	}
	if strings.Contains(r.ModeError, "SECRETVALUE") || strings.Contains(r.ModeError, "toml:") {
		t.Fatalf("mode error echoes profile content: %q", r.ModeError)
	}
	if !strings.Contains(r.ModeError, `"bad"`) {
		t.Fatalf("mode error does not name the profile: %q", r.ModeError)
	}
}

// An explicitly empty token set by the manifest is inactive for the launch
// (resolveToken), so the mode is own-login even with a global token file.
func TestInspectEmptyManifestTokenIsOwnLogin(t *testing.T) {
	_, dir := inspectFixture(t)
	tf := filepath.Join(t.TempDir(), "oauth-token")
	if err := os.WriteFile(tf, []byte("sk-ant-oat01-X\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(oauthTokenFileEnv, tf)
	writeManifest(t, dir, "[env.set]\nCLAUDE_CODE_OAUTH_TOKEN = \"\"\n")
	if r := Inspect("pb", dir, time.Now()); r.Mode != ModeOwnLogin {
		t.Fatalf("mode = %s, want own-login", r.Mode)
	}
}

// The global row ignores playbook configuration entirely: the isolation
// override variable must not make ~/.claude "isolated".
func TestInspectGlobalIgnoresIsolationOverride(t *testing.T) {
	inspectFixture(t)
	home, _ := os.UserHomeDir()
	global := filepath.Join(home, ".claude")
	if err := os.MkdirAll(global, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_PLAYBOOKS_ISOLATE_AUTH", "true")
	if g := InspectGlobal(global, time.Now()); g.Mode != ModeSharedLogin {
		t.Fatalf("global mode = %s, want shared-login", g.Mode)
	}
}

// A live daemon marker on a TOKEN-mode playbook is informational: the launch
// quarantines the stored login, so nothing needs attention.
func TestInspectTokenModeIgnoresDaemonMarker(t *testing.T) {
	_, dir := inspectFixture(t)
	tf := filepath.Join(t.TempDir(), "oauth-token")
	if err := os.WriteFile(tf, []byte("sk-ant-oat01-X\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(oauthTokenFileEnv, tf)
	now := time.Now()
	if err := os.WriteFile(filepath.Join(dir, "daemon-auth-status.json"), []byte(`{"status":"auth_required","since":`+itoa(now.UnixMilli())+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r := Inspect("pb", dir, now)
	if r.Mode != ModeToken || r.ReauthRequired || r.NeedsAttention() != "" {
		t.Fatalf("%+v note=%q", r, r.NeedsAttention())
	}
	if r.DaemonStatus != "auth_required" {
		t.Fatalf("marker not surfaced informationally: %+v", r)
	}
}

// An isolated playbook whose manifest sets a token launches by that token
// (injected, grant quarantined): own-token, isolated, and no stored-login
// judgement even with a live daemon marker.
func TestInspectIsolatedWithOwnTokenIsOwnToken(t *testing.T) {
	_, dir := inspectFixture(t)
	writeManifest(t, dir, "isolate_auth = true\n\n[env.set]\nCLAUDE_CODE_OAUTH_TOKEN = \"sk-ant-oat01-OWN\"\n")
	now := time.Now()
	if err := os.WriteFile(filepath.Join(dir, "daemon-auth-status.json"), []byte(`{"status":"auth_required","since":`+itoa(now.UnixMilli())+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r := Inspect("pb", dir, now)
	if r.Mode != ModeOwnToken || !r.Isolated || r.ReauthRequired || r.NeedsAttention() != "" {
		t.Fatalf("%+v note=%q", r, r.NeedsAttention())
	}
}

// With no grant at all, a leftover daemon marker is not a live re-auth
// signal: the row already says "no login".
func TestInspectNoGrantIgnoresDaemonMarker(t *testing.T) {
	_, dir := inspectFixture(t)
	now := time.Now()
	if err := os.WriteFile(filepath.Join(dir, "daemon-auth-status.json"), []byte(`{"status":"auth_required","since":`+itoa(now.UnixMilli())+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r := Inspect("pb", dir, now)
	if r.Mode != ModeSharedLogin || r.HasGrant || r.ReauthRequired || r.NeedsAttention() != "no login" {
		t.Fatalf("%+v note=%q", r, r.NeedsAttention())
	}
}

// A marker that cannot be ordered against the grant (no expiresAt, or no
// since) is stale, never a live failure.
func TestInspectUnorderableDaemonMarkerIsStale(t *testing.T) {
	now := time.Now()
	for name, fixture := range map[string]struct{ store, marker string }{
		"grant without expiry": {`{"claudeAiOauth":{"accessToken":"x"}}`, `{"status":"auth_required","since":` + itoa(now.UnixMilli()) + `}`},
		"marker without since": {`{"claudeAiOauth":{"accessToken":"x","expiresAt":` + itoa(now.Add(time.Hour).UnixMilli()) + `}}`, `{"status":"auth_required"}`},
	} {
		t.Run(name, func(t *testing.T) {
			_, dir := inspectFixture(t)
			writeStore(t, dir, fixture.store)
			if err := os.WriteFile(filepath.Join(dir, "daemon-auth-status.json"), []byte(fixture.marker), 0o600); err != nil {
				t.Fatal(err)
			}
			r := Inspect("pb", dir, now)
			if r.ReauthRequired || r.DaemonStatus != "auth_required" {
				t.Fatalf("%+v", r)
			}
		})
	}
}
