package auth

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

const ownTokenManifest = "isolated_login = true\n\n[env.set]\nCLAUDE_CODE_OAUTH_TOKEN = \"sk-ant-oat01-OWN\"\n"

// captureNotices records Notice for the test and restores it after.
func captureNotices(t *testing.T) *[]string {
	t.Helper()
	var got []string
	prev := Notice
	Notice = func(m string) { got = append(got, m) }
	t.Cleanup(func() { Notice = prev })
	return &got
}

func asidePath(configDir string) string {
	return filepath.Join(configDir, CredentialsFileName) + asideSuffix
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// holdRunElsewhere takes the run lock shared through its own open file, as
// another cpb process running the playbook on a token would. The returned
// function ends that run.
func holdRunElsewhere(t *testing.T, configDir string) func() {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(configDir, CredentialsFileName)+runLockSuffix, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH); err != nil {
		t.Fatal(err)
	}
	return func() { f.Close() }
}

// Linux: the playbook's own login leaves the store for the token run, other
// keys stay, and the login comes back when the run ends -- next to whatever
// the run changed in the store meanwhile.
func TestTokenRunSetsOwnLoginAsideAndRestoresIt(t *testing.T) {
	configDir := t.TempDir()
	writeManifest(t, configDir, ownTokenManifest)
	store := writeStore(t, configDir, `{`+grantJSON+`,`+mcpJSON+`}`)

	env, err := PrepareLaunchEnv(configDir)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if got, _ := envValue(t, env, OAuthTokenEnv); got != "sk-ant-oat01-OWN" {
		t.Fatalf("token = %q, want the playbook's own", got)
	}
	keys := readStore(t, store)
	if _, present := keys["claudeAiOauth"]; present {
		t.Fatal("the own login stayed readable during the token run")
	}
	if _, present := keys["mcpOAuth"]; !present {
		t.Fatal("MCP logins left the store with the grant")
	}
	if !exists(asidePath(configDir)) {
		t.Fatal("the own login was not set aside")
	}
	if info, err := os.Stat(asidePath(configDir)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("aside file mode: %v %v", info, err)
	}
	if !OwnLoginGrant(configDir) {
		t.Fatal("a login set aside is still the playbook's own login")
	}

	// The run refreshes an MCP login meanwhile.
	writeStore(t, configDir, `{"mcpOAuth":{"notion|abc":{"accessToken":"MCP2"}}}`)

	EndLaunch(configDir)

	keys = readStore(t, store)
	var grant struct{ AccessToken string }
	if err := json.Unmarshal(keys["claudeAiOauth"], &grant); err != nil || grant.AccessToken != "STALE" {
		t.Fatalf("own login not restored: %s", keys["claudeAiOauth"])
	}
	if !strings.Contains(string(keys["mcpOAuth"]), "MCP2") {
		t.Fatalf("the run's own store changes were lost: %s", keys["mcpOAuth"])
	}
	if exists(asidePath(configDir)) {
		t.Fatal("aside file left behind after the restore")
	}
}

// Another token run of the same playbook still live: the login stays aside
// until the last one ends.
func TestOwnLoginWaitsForTheLastTokenRun(t *testing.T) {
	configDir := t.TempDir()
	writeManifest(t, configDir, ownTokenManifest)
	store := writeStore(t, configDir, `{`+grantJSON+`}`)

	if _, err := PrepareLaunchEnv(configDir); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	endOther := holdRunElsewhere(t, configDir)
	EndLaunch(configDir)
	if _, present := readStoreOrEmpty(t, store)["claudeAiOauth"]; present {
		t.Fatal("login restored while another token run was live")
	}
	if !exists(asidePath(configDir)) {
		t.Fatal("aside file gone while another token run was live")
	}

	endOther()
	EndLaunch(configDir)
	if _, present := readStore(t, store)["claudeAiOauth"]; !present {
		t.Fatal("login not restored after the last token run ended")
	}
}

// A run killed before it could restore: the next launch, token or not, puts
// the login back first. A stored-login launch then finds it, and the account
// state is not purged as if the playbook were logged in nowhere.
func TestKilledTokenRunRecoveredAtNextLaunch(t *testing.T) {
	configDir := t.TempDir()
	writeManifest(t, configDir, "isolated_login = true\n")
	store := writeStore(t, configDir, `{`+mcpJSON+`}`)
	if err := os.WriteFile(asidePath(configDir), []byte(`{`+grantJSON+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(configDir, StateFileName)
	if err := os.WriteFile(state, []byte(`{"oauthAccount":{"accountUuid":"own"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := PrepareLaunchEnv(configDir); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	keys := readStore(t, store)
	if _, present := keys["claudeAiOauth"]; !present {
		t.Fatal("a killed run's set-aside login was not recovered")
	}
	if _, present := keys["mcpOAuth"]; !present {
		t.Fatal("recovery dropped the store's other keys")
	}
	if exists(asidePath(configDir)) {
		t.Fatal("aside file left behind after recovery")
	}
	if data, _ := os.ReadFile(state); !strings.Contains(string(data), "oauthAccount") {
		t.Fatal("the own login's account state was purged")
	}
}

// A stored-login launch while a token run is live: the login stays aside,
// and its account state is not purged.
func TestStoredLoginLaunchDuringTokenRunKeepsAccountState(t *testing.T) {
	configDir := t.TempDir()
	writeManifest(t, configDir, "isolated_login = true\n")
	writeStore(t, configDir, `{`+mcpJSON+`}`)
	if err := os.WriteFile(asidePath(configDir), []byte(`{`+grantJSON+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(configDir, StateFileName)
	if err := os.WriteFile(state, []byte(`{"oauthAccount":{"accountUuid":"own"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	defer holdRunElsewhere(t, configDir)()

	if _, err := PrepareLaunchEnv(configDir); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if !exists(asidePath(configDir)) {
		t.Fatal("login restored under a live token run")
	}
	if data, _ := os.ReadFile(state); !strings.Contains(string(data), "oauthAccount") {
		t.Fatal("account state purged while the login was only set aside")
	}
}

// A /login during the token run: the new login stays, and the earlier one is
// kept under a dated name, never overwritten.
func TestLoginDuringTokenRunKeepsBoth(t *testing.T) {
	notices := captureNotices(t)
	configDir := t.TempDir()
	writeManifest(t, configDir, ownTokenManifest)
	store := writeStore(t, configDir, `{`+grantJSON+`}`)

	if _, err := PrepareLaunchEnv(configDir); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	writeStore(t, configDir, `{"claudeAiOauth":{"accessToken":"NEW"}}`)
	EndLaunch(configDir)

	if !strings.Contains(string(readStore(t, store)["claudeAiOauth"]), "NEW") {
		t.Fatal("the newer login was overwritten")
	}
	kept, _ := filepath.Glob(asidePath(configDir) + "-*")
	if len(kept) != 1 {
		t.Fatalf("earlier login not kept under a dated name: %v", kept)
	}
	if data, _ := os.ReadFile(kept[0]); !strings.Contains(string(data), "STALE") {
		t.Fatal("the dated file does not hold the earlier login")
	}
	if len(*notices) != 1 || strings.Contains((*notices)[0], "STALE") {
		t.Fatalf("notices = %q", *notices)
	}
}

// macOS: refused before anything changes, wherever the own login is; never
// a credential character in the message.
func TestMacOSRefusesTokenOverOwnLogin(t *testing.T) {
	hostOS = "darwin"
	t.Cleanup(func() { hostOS = "linux" })

	for _, tc := range []struct {
		name     string
		store    string
		keychain KeychainState
		where    string
	}{
		{"store", `{` + grantJSON + `}`, KeychainAbsent, "in its .credentials.json"},
		{"keychain", "", KeychainPresent, "in the Keychain"},
		{"keychain unreadable", "", KeychainUnknown, "a Keychain that cannot be read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prev := KeychainProbe
			KeychainProbe = func(string, string) KeychainState { return tc.keychain }
			t.Cleanup(func() { KeychainProbe = prev })

			configDir := t.TempDir()
			writeManifest(t, configDir, ownTokenManifest)
			var store string
			if tc.store != "" {
				store = writeStore(t, configDir, tc.store)
			}

			_, err := PrepareLaunchEnv(configDir)
			if !errors.Is(err, ErrOwnLogin) {
				t.Fatalf("err = %v, want ErrOwnLogin", err)
			}
			msg := err.Error()
			for _, want := range []string{tc.where, "--block CLAUDE_CODE_OAUTH_TOKEN", "BLOCK VAR CLAUDE_CODE_OAUTH_TOKEN", "DROP ENV"} {
				if !strings.Contains(msg, want) {
					t.Errorf("refusal lacks %q:\n%s", want, msg)
				}
			}
			if strings.Contains(msg, "sk-ant") || strings.Contains(msg, "STALE") {
				t.Fatalf("refusal carries a credential: %s", msg)
			}
			if store != "" {
				if _, present := readStore(t, store)["claudeAiOauth"]; !present {
					t.Fatal("the refused launch touched the own login")
				}
			}
			if exists(asidePath(configDir)) {
				t.Fatal("the refused launch set something aside")
			}
		})
	}

	// No login of its own: the token launch goes ahead.
	configDir := t.TempDir()
	writeManifest(t, configDir, ownTokenManifest)
	env, err := PrepareLaunchEnv(configDir)
	if err != nil {
		t.Fatalf("prepare without an own login: %v", err)
	}
	if got, _ := envValue(t, env, OAuthTokenEnv); got != "sk-ant-oat01-OWN" {
		t.Fatalf("token = %q", got)
	}

	// The name, when known, goes into the fixes.
	writeStore(t, configDir, `{`+grantJSON+`}`)
	err = OwnLoginRefusal(configDir, mustBlock(t, configDir), "sre")
	if err == nil || !strings.Contains(err.Error(), "cpb run --block CLAUDE_CODE_OAUTH_TOKEN sre") {
		t.Fatalf("refusal = %v", err)
	}
}

// The fixes the refusal names are not refused: --block for one launch, an
// isolated playbook with no token. Neither is a shared playbook.
func TestOwnLoginRefusalScope(t *testing.T) {
	hostOS = "darwin"
	t.Cleanup(func() { hostOS = "linux" })
	block := &manifest.Env{Block: []string{OAuthTokenEnv}}
	for _, tc := range []struct {
		manifest string
		layers   []*manifest.Env
	}{
		{ownTokenManifest, []*manifest.Env{block}},
		{"isolated_login = true\n", nil},
		{"[env.set]\nCLAUDE_CODE_OAUTH_TOKEN = \"sk-ant-oat01-OWN\"\n", nil},
	} {
		configDir := t.TempDir()
		writeManifest(t, configDir, tc.manifest)
		writeStore(t, configDir, `{`+grantJSON+`}`)
		eff, err := EffectiveBlock(configDir, tc.layers)
		if err != nil {
			t.Fatal(err)
		}
		if err := OwnLoginRefusal(configDir, eff, "pb"); err != nil {
			t.Errorf("%q refused: %v", tc.manifest, err)
		}
	}
}

func mustBlock(t *testing.T, configDir string) *manifest.Env {
	t.Helper()
	eff, err := EffectiveBlock(configDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	return eff
}

func readStoreOrEmpty(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	if !exists(path) {
		return map[string]json.RawMessage{}
	}
	return readStore(t, path)
}

// cpb killed alone while its session keeps running on the token: the
// session's pid keeps the run live, so the next launch does not put the
// login back under it. Once the session ends, it does.
func TestSessionOutlivingCpbKeepsTheLoginAside(t *testing.T) {
	configDir := t.TempDir()
	writeManifest(t, configDir, ownTokenManifest)
	store := writeStore(t, configDir, `{`+grantJSON+`}`)
	if _, err := PrepareLaunchEnv(configDir); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	session := exec.Command("sleep", "60")
	if err := session.Start(); err != nil {
		t.Fatal(err)
	}
	defer session.Process.Kill()
	SessionStarted(session.Process.Pid)

	// cpb dies: its run lock goes with it, EndLaunch never runs.
	tokenRunsMu.Lock()
	tokenRuns[configDir].lock.Close()
	delete(tokenRuns, configDir)
	tokenRunsMu.Unlock()

	if err := restoreOwnLogin(configDir); err != nil {
		t.Fatal(err)
	}
	if _, present := readStoreOrEmpty(t, store)["claudeAiOauth"]; present {
		t.Fatal("login restored under a live session")
	}
	session.Process.Kill()
	session.Wait()
	if err := restoreOwnLogin(configDir); err != nil {
		t.Fatal(err)
	}
	if _, present := readStore(t, store)["claudeAiOauth"]; !present {
		t.Fatal("login not restored after the session ended")
	}
	if exists(filepath.Join(configDir, CredentialsFileName) + runPidsSuffix) {
		t.Fatal("pid list left behind")
	}
}

// Two archives in the same second never replace each other.
func TestArchiveNeverOverwrites(t *testing.T) {
	configDir := t.TempDir()
	aside := asidePath(configDir)
	for _, body := range []string{"first", "second"} {
		if err := os.WriteFile(aside, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := archiveAside(aside); err != nil {
			t.Fatal(err)
		}
	}
	kept, _ := filepath.Glob(aside + "-*")
	if len(kept) != 2 {
		t.Fatalf("archives = %v, want two", kept)
	}
}

// A grant a killed run left aside comes back at the next launch: the login
// guard counts it before that launch, routed to a router with no key.
func TestGuardCountsALoginSetAside(t *testing.T) {
	configDir := t.TempDir()
	writeManifest(t, configDir, "isolated_login = true\n\n[env]\nblock = [\"CLAUDE_CODE_OAUTH_TOKEN\"]\n\n[env.set]\nANTHROPIC_BASE_URL = \"http://router.example/v1\"\n")
	if err := os.WriteFile(asidePath(configDir), []byte(`{`+grantJSON+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if g := guardOf(t, configDir); g.Verdict != GuardRefused || g.Sends != "login" {
		t.Fatalf("guard = %+v, want refused for the login set aside", g)
	}
}
