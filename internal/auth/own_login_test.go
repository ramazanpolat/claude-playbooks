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
	"time"

	"github.com/ramazanpolat/claude-playbooks/internal/account"
	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envset"
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
			for _, want := range []string{tc.where, "cpb run --account <account>", "auth = 'claude_login'", "/logout"} {
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
	if err == nil || !strings.Contains(err.Error(), "cpb run --account <account> sre") {
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

// A restore that finds no grant aside once it holds the lock (another
// restore took it meanwhile, or the file is empty) drops the file quietly:
// a missing file is not an error.
func TestRestoreWithNoGrantIsQuiet(t *testing.T) {
	configDir := t.TempDir()
	writeManifest(t, configDir, ownTokenManifest)
	if err := os.WriteFile(asidePath(configDir), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := restoreOwnLogin(configDir); err != nil || exists(asidePath(configDir)) {
		t.Fatalf("empty aside: %v", err)
	}
}

// isolatedWithToken: an isolated playbook whose own block sets a token, under
// a scratch HOME and registry, with a machine token file the launch must
// ignore.
func isolatedWithToken(t *testing.T) (root, configDir string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	tokenPath := filepath.Join(t.TempDir(), "oauth-token")
	if err := os.WriteFile(tokenPath, []byte("sk-ant-oat01-MACHINE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(oauthTokenFileEnv, tokenPath)
	root = t.TempDir()
	t.Setenv("CPB_PLAYBOOKS_DIR", root)
	config.PlaybooksDir = ""
	configDir = filepath.Join(root, "pb")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, configDir, "isolated_login = true\n\n[env.set]\nCLAUDE_CODE_OAUTH_TOKEN = \"sk-ant-oat01-OWN\"\n")
	return root, configDir
}

// useTokenAccount moves the playbook's token onto an account that supplies
// it (claude_token), the v4.2.0 form of the same launch.
func useTokenAccount(t *testing.T, root, configDir string) {
	t.Helper()
	a := &account.Account{Name: "own", Provider: account.Anthropic, Auth: account.Auth{Kind: account.ClaudeToken, Ref: "keychain:own-token"}}
	if err := account.Write(account.Dir(root), a); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, configDir, "isolated_login = true\n\n[env]\naccounts = [\"own\"]\n")
}

// On macOS a token launch of an isolated playbook that holds, or may hold, a
// login of its own is refused before anything changes, for the playbook's
// own token and a launch flag's alike: the store and the account state are
// byte-identical afterwards, and the error names the remedies but no
// credential.
func TestTokenLaunchRefusedOverIsolatedOwnLogin(t *testing.T) {
	hostOS = "darwin"
	t.Cleanup(func() { hostOS = "linux" })
	cases := []struct {
		name, store string
		keychain    KeychainState
		where       string
	}{
		{"a grant in the store", `{` + grantJSON + `,` + mcpJSON + `}`, KeychainAbsent, "in its .credentials.json"},
		{"an unreadable store", `{not json`, KeychainAbsent, "in its .credentials.json"},
		{"a Keychain item", `{` + mcpJSON + `}`, KeychainPresent, "in the Keychain"},
		{"a Keychain that cannot be checked", "", KeychainUnknown, "a Keychain that cannot be read"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root, configDir := isolatedWithToken(t)
			old := KeychainProbe
			KeychainProbe = func(string, string) KeychainState { return c.keychain }
			t.Cleanup(func() { KeychainProbe = old })
			store := filepath.Join(configDir, CredentialsFileName)
			if c.store != "" {
				writeStore(t, configDir, c.store)
			}
			state := filepath.Join(configDir, StateFileName)
			stateBody := `{"oauthAccount":{"accountUuid":"own-acct"}}`
			if err := os.WriteFile(state, []byte(stateBody), 0o600); err != nil {
				t.Fatal(err)
			}

			for _, layers := range [][]*manifest.Env{nil, {{Set: map[string]string{OAuthTokenEnv: "sk-ant-oat01-FLAG"}}}} {
				_, err := PrepareLaunchEnvWith(configDir, layers)
				if !errors.Is(err, ErrOwnLogin) {
					t.Fatalf("err = %v, want ErrOwnLogin", err)
				}
				msg := err.Error()
				for _, want := range []string{c.where, "cpb run --account <account>", "/logout"} {
					if !strings.Contains(msg, want) {
						t.Fatalf("refusal lacks %q: %s", want, msg)
					}
				}
				for _, secret := range []string{"STALE", "OWN", "FLAG", "MACHINE", "own-acct"} {
					if strings.Contains(msg, secret) {
						t.Fatalf("refusal carries %q: %s", secret, msg)
					}
				}
			}
			if c.store != "" {
				if data, _ := os.ReadFile(store); string(data) != c.store {
					t.Fatalf("store changed: %s", data)
				}
			}
			if data, _ := os.ReadFile(state); string(data) != stateBody {
				t.Fatalf("account state changed: %s", data)
			}
			// auth status judges the same token on the account that
			// supplies it since v4.2.0 (a layer's token refuses the launch).
			useTokenAccount(t, root, configDir)
			if r := Inspect("pb", configDir, time.Now()); r.Mode != ModeError || !strings.Contains(r.ModeError, c.where) {
				t.Fatalf("inspect mode = %s (%s), want error naming the login", r.Mode, r.ModeError)
			}
		})
	}
}

// A link to the shared store is not the playbook's own login: isolation
// detaches it, and the token launch goes ahead with the machine store intact.
func TestTokenLaunchOverIsolatedSharedLinkGoesAhead(t *testing.T) {
	_, configDir := isolatedWithToken(t)
	home, _ := os.UserHomeDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	global := writeStore(t, filepath.Join(home, ".claude"), `{`+grantJSON+`}`)
	store := filepath.Join(configDir, CredentialsFileName)
	if err := os.Symlink(global, store); err != nil {
		t.Fatal(err)
	}
	env, err := PrepareLaunchEnv(configDir)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := envValue(t, env, OAuthTokenEnv); got != "sk-ant-oat01-OWN" {
		t.Fatalf("token = %q, want the playbook's own", got)
	}
	if _, err := os.Lstat(store); !os.IsNotExist(err) {
		t.Fatalf("the shared link was not detached: %v", err)
	}
	if _, present := readStore(t, global)["claudeAiOauth"]; !present {
		t.Fatal("the machine store lost its grant")
	}
}

// DEFAULTS is the machine's layer: a token it sets is never an isolated
// playbook's token. Its login survives, nothing is injected, and auth status
// agrees. A shared playbook still gets the DEFAULTS token.
func TestDefaultsTokenNeverReachesIsolatedPlaybook(t *testing.T) {
	root, configDir := isolatedWithToken(t)
	writeManifest(t, configDir, "isolated_login = true\n")
	if err := envset.Write(envset.Dir(root), &envset.Set{Name: "base", Set: map[string]string{OAuthTokenEnv: "sk-ant-oat01-DEFAULT", "FROM_DEFAULT": "yes"}}); err != nil {
		t.Fatal(err)
	}
	if err := envset.WriteDefaults(envset.Dir(root), []string{"base"}); err != nil {
		t.Fatal(err)
	}
	body := `{` + grantJSON + `}`
	store := writeStore(t, configDir, body)

	env, err := PrepareLaunchEnv(configDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := envValue(t, env, OAuthTokenEnv); present {
		t.Fatalf("the DEFAULTS token reached an isolated playbook: %v", tokenEntries(env))
	}
	if got, _ := envValue(t, env, "FROM_DEFAULT"); got != "yes" {
		t.Fatal("the rest of DEFAULTS was dropped")
	}
	if data, _ := os.ReadFile(store); string(data) != body {
		t.Fatalf("the isolated playbook's login changed: %s", data)
	}
	if r := Inspect("pb", configDir, time.Now()); r.Mode != ModeIsolatedLogin {
		t.Fatalf("inspect mode = %s, want isolated-login", r.Mode)
	}
	// The launch plan's view agrees: the DEFAULTS token is not in it.
	eff, err := EffectiveBlock(configDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := eff.Set[OAuthTokenEnv]; present {
		t.Fatal("the effective block of an isolated playbook holds the DEFAULTS token")
	}
	if eff.Set["FROM_DEFAULT"] != "yes" {
		t.Fatal("the effective block dropped the rest of DEFAULTS")
	}

	shared := filepath.Join(root, "shared")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	env, _ = PrepareLaunchEnv(shared)
	if got, _ := envValue(t, env, OAuthTokenEnv); got != "sk-ant-oat01-DEFAULT" {
		t.Fatalf("shared playbook token = %q, want the DEFAULTS one", got)
	}
}

// A token reference follows the same rule as a value: one only DEFAULTS
// holds never reaches an isolated playbook (the launch plan's effective block
// leaves it out, so the secret helper never sets it), while one the playbook
// supplies itself is kept, and a shared playbook keeps the DEFAULTS one.
func TestDefaultsTokenRefNeverReachesIsolatedPlaybook(t *testing.T) {
	root, configDir := isolatedWithToken(t)
	writeManifest(t, configDir, "isolated_login = true\n")
	if err := envset.Write(envset.Dir(root), &envset.Set{Name: "base", Refs: map[string]string{OAuthTokenEnv: "keychain:default-token"}}); err != nil {
		t.Fatal(err)
	}
	if err := envset.WriteDefaults(envset.Dir(root), []string{"base"}); err != nil {
		t.Fatal(err)
	}
	eff, err := EffectiveBlock(configDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ref, present := eff.Refs[OAuthTokenEnv]; present {
		t.Fatalf("the DEFAULTS token reference reached an isolated playbook: %q", ref)
	}
	if r := Inspect("pb", configDir, time.Now()); r.Mode != ModeIsolatedLogin {
		t.Fatalf("inspect mode = %s, want isolated-login", r.Mode)
	}

	writeManifest(t, configDir, "isolated_login = true\n\n[env.refs]\nCLAUDE_CODE_OAUTH_TOKEN = \"keychain:own-token\"\n")
	if eff, err = EffectiveBlock(configDir, nil); err != nil {
		t.Fatal(err)
	}
	if got := eff.Refs[OAuthTokenEnv]; got != "keychain:own-token" {
		t.Fatalf("own token reference = %q, want keychain:own-token", got)
	}
	// A launch now refuses a token a layer sets; on an account that supplies
	// it, auth status reports the playbook's own token.
	if r := Inspect("pb", configDir, time.Now()); r.Mode != ModeError {
		t.Fatalf("inspect mode = %s, want error", r.Mode)
	}
	useTokenAccount(t, root, configDir)
	if r := Inspect("pb", configDir, time.Now()); r.Mode != ModePlaybookToken {
		t.Fatalf("inspect mode = %s, want playbook-token", r.Mode)
	}

	shared := filepath.Join(root, "shared")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	if eff, err = EffectiveBlock(shared, nil); err != nil {
		t.Fatal(err)
	}
	if got := eff.Refs[OAuthTokenEnv]; got != "keychain:default-token" {
		t.Fatalf("shared playbook token reference = %q, want the DEFAULTS one", got)
	}
}

// On the shared token path the quarantine strips the store's grant. A grant
// that is another account's, or not confirmed as the machine's, is set aside
// first instead of being lost; the machine's own account's newer grant goes
// over the machine store first.
func TestSharedTokenLaunchKeepsTheStoredGrant(t *testing.T) {
	activate := func(t *testing.T) {
		tokenPath := filepath.Join(t.TempDir(), "oauth-token")
		if err := os.WriteFile(tokenPath, []byte("sk-ant-oat01-MACHINE\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(oauthTokenFileEnv, tokenPath)
	}
	for _, c := range []struct{ name, machine, own, why string }{
		{"another account", "machine-acct", "own-acct", "another account's login"},
		{"an unconfirmed account", "machine-acct", "", "cannot be confirmed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newSyncFixture(t, c.machine, c.own)
			activate(t)
			if _, err := PrepareLaunchEnv(f.target); err != nil {
				t.Fatal(err)
			}
			if data, _ := os.ReadFile(f.global); string(data) != machineGrant {
				t.Fatalf("the machine store changed: %s", data)
			}
			kept := f.local + ".cpb-own-2026-09-27-16_00_00"
			if data, _ := os.ReadFile(kept); string(data) != ownGrant {
				t.Fatalf("the playbook's login was not kept aside: %q", data)
			}
			if storeHasGrant(t, f.local) {
				t.Fatal("the token launch kept a stored grant")
			}
			if len(f.notices) != 1 || !strings.Contains(f.notices[0], c.why) || !strings.Contains(f.notices[0], "launched on the token") {
				t.Fatalf("notices: %q", f.notices)
			}
		})
	}
	t.Run("the machine's account, newer", func(t *testing.T) {
		f := newSyncFixture(t, "acct", "acct")
		activate(t)
		if _, err := PrepareLaunchEnv(f.target); err != nil {
			t.Fatal(err)
		}
		if data, _ := os.ReadFile(f.global); string(data) != ownGrant {
			t.Fatalf("the newer grant did not reach the machine store: %s", data)
		}
		if storeHasGrant(t, f.local) {
			t.Fatal("the token launch kept a stored grant")
		}
		if len(f.notices) != 0 {
			t.Fatalf("notices: %q", f.notices)
		}
	})
}

// storeHasGrant: whether the store at path holds an account grant; a store
// the quarantine removed whole holds none.
func storeHasGrant(t *testing.T, path string) bool {
	t.Helper()
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return false
	}
	_, present := readStore(t, path)["claudeAiOauth"]
	return present
}

// A config directory whose path is not ASCII has a Keychain item name cpb
// cannot derive. Off macOS there is no Keychain, so an isolated playbook
// there launches on its own token; on macOS the item may exist, and the
// launch is refused as unknown.
func TestNonASCIIPathKeychainOnlyCountsOnDarwin(t *testing.T) {
	for _, c := range []struct {
		os      string
		refused bool
	}{{"linux", false}, {"darwin", true}} {
		t.Run(c.os, func(t *testing.T) {
			_, base := isolatedWithToken(t)
			configDir := filepath.Join(filepath.Dir(base), "çalışma")
			if err := os.Rename(base, configDir); err != nil {
				t.Fatal(err)
			}
			oldOS, oldProbe := hostOS, KeychainProbe
			hostOS = c.os
			KeychainProbe = func(string, string) KeychainState { return KeychainAbsent }
			t.Cleanup(func() { hostOS, KeychainProbe = oldOS, oldProbe })

			env, err := PrepareLaunchEnv(configDir)
			if c.refused {
				if !errors.Is(err, ErrOwnLogin) || !strings.Contains(err.Error(), "a Keychain that cannot be read") {
					t.Fatalf("err = %v, want the unknown-Keychain refusal", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got, _ := envValue(t, env, OAuthTokenEnv); got != "sk-ant-oat01-OWN" {
				t.Fatalf("token = %q, want the playbook's own", got)
			}
		})
	}
}

// On macOS a login that arrives between the own-login check and the launch
// (a concurrent /login in a session of the same playbook) is refused, never
// stripped. Elsewhere it is set aside for the run like any other.
func TestLoginArrivingAfterTheCheckIsRefused(t *testing.T) {
	hostOS = "darwin"
	t.Cleanup(func() { hostOS = "linux" })
	_, configDir := isolatedWithToken(t)
	store := filepath.Join(configDir, CredentialsFileName)
	body := `{` + grantJSON + `}`
	old := KeychainProbe
	calls := 0
	// The first own-login check probes the Keychain once the store shows no
	// grant; the login lands right then.
	KeychainProbe = func(string, string) KeychainState {
		calls++
		if calls == 1 {
			if err := os.WriteFile(store, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return KeychainAbsent
	}
	t.Cleanup(func() { KeychainProbe = old })

	_, err := PrepareLaunchEnv(configDir)
	if !errors.Is(err, ErrOwnLogin) {
		t.Fatalf("err = %v, want ErrOwnLogin", err)
	}
	if calls != 1 {
		t.Fatalf("Keychain probed %d times, want once (the re-check finds the store's grant first)", calls)
	}
	if data, _ := os.ReadFile(store); string(data) != body {
		t.Fatalf("the login that arrived was changed: %s", data)
	}
}

// The machine store is a recovery copy only when it holds a grant: an
// MCP-only machine store, even a newer one, leaves the playbook's grant its
// only copy, so it is set aside before the token launch strips it, and the
// notice says why. The same holds when there is no machine store at all.
func TestSharedTokenLaunchSetsAsideWhenTheMachineHoldsNoGrant(t *testing.T) {
	for _, c := range []struct {
		name    string
		machine string // "" removes the machine store
	}{
		{"an MCP-only machine store, newer", `{` + mcpJSON + `}`},
		{"no machine store", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newSyncFixture(t, "acct", "acct")
			if c.machine == "" {
				if err := os.Remove(f.global); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(f.global, []byte(c.machine), 0o600); err != nil {
					t.Fatal(err)
				}
				later := time.Now().Add(time.Hour)
				if err := os.Chtimes(f.global, later, later); err != nil {
					t.Fatal(err)
				}
			}
			tokenPath := filepath.Join(t.TempDir(), "oauth-token")
			if err := os.WriteFile(tokenPath, []byte("sk-ant-oat01-MACHINE\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv(oauthTokenFileEnv, tokenPath)

			if _, err := PrepareLaunchEnv(f.target); err != nil {
				t.Fatal(err)
			}
			if data, _ := os.ReadFile(f.local + ".cpb-own-2026-09-27-16_00_00"); string(data) != ownGrant {
				t.Fatalf("the only copy of the grant was not kept aside: %q", data)
			}
			if storeHasGrant(t, f.local) {
				t.Fatal("the token launch kept a stored grant")
			}
			if c.machine != "" {
				if data, _ := os.ReadFile(f.global); string(data) != c.machine {
					t.Fatalf("the machine store changed: %s", data)
				}
			}
			if len(f.notices) != 1 || !strings.Contains(f.notices[0], "the machine store does not hold") {
				t.Fatalf("notices: %q", f.notices)
			}
		})
	}
}

// On Linux a launch on an account that uses no Claude login proceeds only
// once the playbook's own login is out of reach. The machine's own directory,
// made isolated by the override, keeps its login where it is (the set-aside
// never empties it): refused. A set-aside that failed refuses too.
func TestNoLoginAccountRefusesAReachableLogin(t *testing.T) {
	hostOS = "linux"
	isolatedWithToken(t)
	home, _ := os.UserHomeDir()
	global := filepath.Join(home, ".claude")
	if err := os.MkdirAll(global, 0o700); err != nil {
		t.Fatal(err)
	}
	store := writeStore(t, global, `{`+grantJSON+`}`)
	t.Setenv("CPB_ISOLATED_LOGIN", "true")
	_, err := PrepareLaunchEnvFor(global, nil, "router")
	EndLaunch(global)
	if matches, _ := filepath.Glob(store + asideSuffix + "*"); len(matches) > 0 {
		t.Fatalf("the refused launch left a copy of the login: %v", matches)
	}
	if !errors.Is(err, ErrOwnLogin) || !strings.Contains(err.Error(), "router") || strings.Contains(err.Error(), "STALE") {
		t.Fatalf("err = %v, want a refusal naming the account and no credential", err)
	}
	if data, _ := os.ReadFile(store); !strings.Contains(string(data), "STALE") {
		t.Fatal("the machine's login was changed")
	}
	// The machine's store as a link: refused, and the link stays.
	elsewhere := writeStore(t, t.TempDir(), `{`+grantJSON+`}`)
	if err := os.Remove(store); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, store); err != nil {
		t.Fatal(err)
	}
	_, err = PrepareLaunchEnvFor(global, nil, "router")
	EndLaunch(global)
	if !errors.Is(err, ErrOwnLogin) {
		t.Fatalf("a linked machine store: err = %v, want a refusal", err)
	}
	if target, err := os.Readlink(store); err != nil || target != elsewhere {
		t.Fatalf("the machine's link was changed: %q %v", target, err)
	}

	_, configDir := isolatedWithToken(t)
	if err := loginLeftReachable(configDir, "router", errors.New("cannot write")); !errors.Is(err, ErrOwnLogin) {
		t.Fatalf("a failed set-aside: err = %v, want a refusal", err)
	}
	if err := loginLeftReachable(configDir, "router", nil); err != nil {
		t.Fatalf("no login left: %v", err)
	}
}

// The ordinary outgoing link to the machine's store is the playbook's own
// link, which isolation detaches: a playbook that still has it after
// SET login = 'isolated' is not the machine's directory, and its launch on
// such an account goes ahead, leaving the machine's login as it was.
func TestNoLoginAccountDetachesTheSharedLink(t *testing.T) {
	hostOS = "linux"
	_, configDir := isolatedWithToken(t)
	writeManifest(t, configDir, "isolated_login = true\n")
	home, _ := os.UserHomeDir()
	global := filepath.Join(home, ".claude")
	if err := os.MkdirAll(global, 0o700); err != nil {
		t.Fatal(err)
	}
	machine := writeStore(t, global, `{`+grantJSON+`}`)
	if err := os.Symlink(machine, StorePath(configDir)); err != nil {
		t.Fatal(err)
	}
	if err := AccountLoginRefusal(configDir, "pb", "router"); err != nil {
		t.Fatalf("refused over the shared link: %v", err)
	}
	_, err := PrepareLaunchEnvFor(configDir, nil, "router")
	EndLaunch(configDir)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if _, err := os.Lstat(StorePath(configDir)); !os.IsNotExist(err) {
		t.Fatalf("the shared link is still there: %v", err)
	}
	if data, _ := os.ReadFile(machine); !strings.Contains(string(data), "STALE") {
		t.Fatal("the machine's login was changed")
	}
}

// A sandboxed launch on such an account refuses a login of the playbook's
// own in any form: the directory is mounted into the sandbox. A link is
// left to isolation, which detaches the shared one first (cmd/sandbox.go
// refuses one that is still there).
func TestSandboxAccountLoginRefusal(t *testing.T) {
	hostOS = "linux"
	_, configDir := isolatedWithToken(t)
	if err := SandboxAccountLoginRefusal(configDir, "pb", "router"); err != nil {
		t.Fatalf("no login: %v", err)
	}
	store := StorePath(configDir)
	if err := os.Symlink("/nonexistent/.credentials.json", store); err != nil {
		t.Fatal(err)
	}
	if err := SandboxAccountLoginRefusal(configDir, "pb", "router"); err != nil {
		t.Fatalf("a shared link, detached at launch: %v", err)
	}
	for name, make := range map[string]func(){
		"a grant": func() { writeStore(t, configDir, `{`+grantJSON+`}`) },
		"a login set aside": func() {
			if err := os.WriteFile(store+asideSuffix, []byte(`{`+grantJSON+`}`), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"a dated copy": func() {
			if err := os.WriteFile(store+asideSuffix+"-2026-01-01-00_00_00", []byte(`{`+grantJSON+`}`), 0o600); err != nil {
				t.Fatal(err)
			}
		},
	} {
		old, _ := filepath.Glob(store + "*")
		for _, f := range old {
			os.Remove(f)
		}
		make()
		err := SandboxAccountLoginRefusal(configDir, "pb", "router")
		if !errors.Is(err, ErrOwnLogin) || strings.Contains(err.Error(), "STALE") {
			t.Errorf("%s: err = %v, want a refusal with no credential", name, err)
		}
	}
}
