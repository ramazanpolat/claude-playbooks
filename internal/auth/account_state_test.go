package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const identityFixture = `{
  "numStartups": 3,
  "projects": {"/w": {"allowedTools": ["Bash"]}},
  "oauthAccount": {"emailAddress": "x@y", "organizationUuid": "o"},
  "cachedGrowthBookFeatures": {"tengu_x": true},
  "cachedGrowthBookFeaturesAt": 1,
  "cachedExperimentFeatures": {},
  "cachedExperimentData": {},
  "passesEligibilityCache": {"a": true},
  "cachedExtraUsageDisabledReason": "none"
}
`

func writeState(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, StateFileName)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestQuarantineAccountStateRemovesIdentityKeysOnly(t *testing.T) {
	dir := t.TempDir()
	p := writeState(t, dir, identityFixture)
	removed, err := QuarantineAccountState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != len(identityStateKeys) {
		t.Fatalf("removed %v, want all of %v", removed, identityStateKeys)
	}
	var after map[string]any
	if err := json.Unmarshal(readFile(t, p), &after); err != nil {
		t.Fatal(err)
	}
	for _, key := range identityStateKeys {
		if _, ok := after[key]; ok {
			t.Fatalf("%s survived", key)
		}
	}
	if after["numStartups"] != float64(3) {
		t.Fatalf("numStartups changed: %v", after["numStartups"])
	}
	if tools := after["projects"].(map[string]any)["/w"].(map[string]any)["allowedTools"].([]any); len(tools) != 1 || tools[0] != "Bash" {
		t.Fatalf("nested project state changed: %v", after["projects"])
	}
	if info, _ := os.Stat(p); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode().Perm())
	}
	// Idempotent, and quiet about it.
	if removed, err := QuarantineAccountState(dir); err != nil || removed != nil {
		t.Fatalf("second run: %v %v", removed, err)
	}
	if got := StaleIdentityState(dir); got != nil {
		t.Fatalf("StaleIdentityState after purge = %v", got)
	}
}

func TestQuarantineAccountStateEdgeFiles(t *testing.T) {
	dir := t.TempDir()
	if removed, err := QuarantineAccountState(dir); err != nil || removed != nil {
		t.Fatalf("absent file: %v %v", removed, err)
	}
	writeState(t, dir, "\n")
	if removed, err := QuarantineAccountState(dir); err != nil || removed != nil {
		t.Fatalf("empty file: %v %v", removed, err)
	}
	writeState(t, dir, "{not json")
	if _, err := QuarantineAccountState(dir); err == nil || !strings.Contains(err.Error(), "invalid "+StateFileName) {
		t.Fatalf("invalid JSON must be reported: %v", err)
	}
	if got := StaleIdentityState(dir); got != nil {
		t.Fatalf("unreadable state reports keys: %v", got)
	}
}

// The launch path purges only the isolated, login-less, token-less playbook.
func TestIsolatedLaunchPurgesStaleIdentity(t *testing.T) {
	t.Setenv(oauthTokenFileEnv, filepath.Join(t.TempDir(), "absent"))
	os.Unsetenv(OAuthTokenEnv)
	t.Setenv("HOME", t.TempDir())

	// 1. isolated, no store, no own token: purged
	dir := t.TempDir()
	writeManifest(t, dir, "isolate_auth = true\n")
	writeState(t, dir, identityFixture)
	if _, err := PrepareLaunchEnv(dir); err != nil {
		t.Fatalf("launch: %v", err)
	}
	if got := StaleIdentityState(dir); got != nil {
		t.Fatalf("isolated login-less playbook kept %v", got)
	}

	// 2. isolated with its own stored login: kept
	dir = t.TempDir()
	writeManifest(t, dir, "isolate_auth = true\n")
	writeState(t, dir, identityFixture)
	writeStore(t, dir, `{"claudeAiOauth":{"accessToken":"own","expiresAt":9999999999999}}`)
	PrepareLaunchEnv(dir)
	if got := StaleIdentityState(dir); len(got) != len(identityStateKeys) {
		t.Fatalf("logged-in isolated playbook lost state: %v", got)
	}

	// 3. isolated with an own token from the block: kept
	dir = t.TempDir()
	writeManifest(t, dir, "isolate_auth = true\n\n[env.set]\nCLAUDE_CODE_OAUTH_TOKEN = \"sk-ant-oat01-OWN\"\n")
	writeState(t, dir, identityFixture)
	PrepareLaunchEnv(dir)
	if got := StaleIdentityState(dir); len(got) != len(identityStateKeys) {
		t.Fatalf("own-token isolated playbook lost state: %v", got)
	}

	// 4. not isolated: untouched by this path
	dir = t.TempDir()
	writeManifest(t, dir, "name = \"pb\"\n")
	writeState(t, dir, identityFixture)
	PrepareLaunchEnv(dir)
	if got := StaleIdentityState(dir); len(got) != len(identityStateKeys) {
		t.Fatalf("non-isolated playbook lost state: %v", got)
	}

	// 5. isolated, unreadable state: launch proceeds, error advisory, file intact
	dir = t.TempDir()
	writeManifest(t, dir, "isolate_auth = true\n")
	p := writeState(t, dir, "{not json")
	env, err := PrepareLaunchEnv(dir)
	if len(env) == 0 || err == nil || !strings.Contains(err.Error(), "invalid "+StateFileName) {
		t.Fatalf("unreadable state: env=%d err=%v", len(env), err)
	}
	if string(readFile(t, p)) != "{not json" {
		t.Fatal("unreadable state was rewritten")
	}
}

func TestInspectReportsStaleIdentity(t *testing.T) {
	t.Setenv(oauthTokenFileEnv, filepath.Join(t.TempDir(), "absent"))
	os.Unsetenv(OAuthTokenEnv)
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	writeManifest(t, dir, "isolate_auth = true\n")
	writeState(t, dir, identityFixture)
	r := Inspect("iso", dir, time.Now())
	if r.Mode != ModeIsolated || len(r.StaleIdentity) != len(identityStateKeys) {
		t.Fatalf("mode=%s stale=%v", r.Mode, r.StaleIdentity)
	}
	if note := r.NeedsAttention(); note != "no login; stale account state, purged at launch" {
		t.Fatalf("note = %q", note)
	}
	out, _ := json.Marshal(r)
	if !strings.Contains(string(out), `"stale_identity":["oauthAccount"`) {
		t.Fatalf("json lacks stale_identity: %s", out)
	}
	// Once logged in, the note and the list go away even with the keys present.
	writeStore(t, dir, `{"claudeAiOauth":{"accessToken":"own","expiresAt":9999999999999}}`)
	r = Inspect("iso", dir, time.Now())
	if len(r.StaleIdentity) != 0 || strings.Contains(r.NeedsAttention(), "stale") {
		t.Fatalf("logged-in isolated playbook flagged: %v %q", r.StaleIdentity, r.NeedsAttention())
	}
	// Inspect is read-only: the keys are still there.
	if got := StaleIdentityState(dir); len(got) != len(identityStateKeys) {
		t.Fatalf("Inspect mutated state: %v", got)
	}
}

func readFile(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// An unreadable store may hold a login: the launch must keep the account
// state and report, not purge. A 0400 state file stays 0400. A grant reached
// only through a symlinked shared store does not count as the playbook's own.
func TestIdentityQuarantineSafeSides(t *testing.T) {
	t.Setenv(oauthTokenFileEnv, filepath.Join(t.TempDir(), "absent"))
	os.Unsetenv(OAuthTokenEnv)
	t.Setenv("HOME", t.TempDir())

	// unreadable store
	if os.Geteuid() != 0 {
		dir := t.TempDir()
		writeManifest(t, dir, "isolate_auth = true\n")
		writeState(t, dir, identityFixture)
		store := writeStore(t, dir, `{"claudeAiOauth":{"accessToken":"own"}}`)
		if err := os.Chmod(store, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(store, 0o600) })
		env, err := PrepareLaunchEnv(dir)
		if len(env) == 0 || err == nil {
			t.Fatalf("unreadable store: env=%d err=%v (want advisory error)", len(env), err)
		}
		if got := StaleIdentityState(dir); len(got) != len(identityStateKeys) {
			t.Fatalf("state purged behind an unreadable store: %v", got)
		}
	}

	// unparsable store: same
	dir := t.TempDir()
	writeManifest(t, dir, "isolate_auth = true\n")
	writeState(t, dir, identityFixture)
	writeStore(t, dir, "{not json")
	if _, err := PrepareLaunchEnv(dir); err == nil || !strings.Contains(err.Error(), "invalid "+CredentialsFileName) {
		t.Fatalf("unparsable store: %v", err)
	}
	if got := StaleIdentityState(dir); len(got) != len(identityStateKeys) {
		t.Fatalf("state purged behind an unparsable store: %v", got)
	}

	// read-only state file keeps its mode
	dir = t.TempDir()
	p := writeState(t, dir, identityFixture)
	if err := os.Chmod(p, 0o400); err != nil {
		t.Fatal(err)
	}
	if _, err := QuarantineAccountState(dir); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(p); info.Mode().Perm() != 0o400 {
		t.Fatalf("mode widened to %v", info.Mode().Perm())
	}
	if got := StaleIdentityState(dir); got != nil {
		t.Fatalf("not purged: %v", got)
	}

	// symlinked shared store: Inspect reports the pending removal
	home := t.TempDir()
	t.Setenv("HOME", home)
	global := filepath.Join(home, ".claude")
	if err := os.MkdirAll(global, 0o755); err != nil {
		t.Fatal(err)
	}
	writeStore(t, global, `{"claudeAiOauth":{"accessToken":"g","expiresAt":9999999999999}}`)
	dir = t.TempDir()
	writeManifest(t, dir, "isolate_auth = true\n")
	writeState(t, dir, identityFixture)
	if err := os.Symlink(filepath.Join(global, CredentialsFileName), filepath.Join(dir, CredentialsFileName)); err != nil {
		t.Fatal(err)
	}
	r := Inspect("iso", dir, time.Now())
	if r.Store != StoreSymlink || len(r.StaleIdentity) != len(identityStateKeys) || !strings.Contains(r.NeedsAttention(), "stale account state") {
		t.Fatalf("symlinked store: store=%s stale=%v note=%q", r.Store, r.StaleIdentity, r.NeedsAttention())
	}
	// and the launch does detach and purge
	if _, err := PrepareLaunchEnv(dir); err != nil {
		t.Fatalf("launch: %v", err)
	}
	if got := StaleIdentityState(dir); got != nil {
		t.Fatalf("state kept behind a detached link: %v", got)
	}
	if _, err := os.Lstat(filepath.Join(dir, CredentialsFileName)); !os.IsNotExist(err) {
		t.Fatal("shared link not detached")
	}
}
