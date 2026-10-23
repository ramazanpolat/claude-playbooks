package auth

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envset"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

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

// A token launch of an isolated playbook that holds, or may hold, a login of
// its own is refused before anything changes: the store and the account
// state are byte-identical afterwards, and the error names the remedies but
// no credential.
func TestTokenLaunchRefusedOverIsolatedOwnLogin(t *testing.T) {
	cases := []struct {
		name, store string
		keychain    KeychainState
		where       string
	}{
		{"a grant in the store", `{` + grantJSON + `,` + mcpJSON + `}`, KeychainAbsent, "in its .credentials.json"},
		{"an unreadable store", `{not json`, KeychainAbsent, "cannot read"},
		{"a Keychain item", `{` + mcpJSON + `}`, KeychainPresent, "in the Keychain"},
		{"a Keychain that cannot be checked", "", KeychainUnknown, "could not be checked"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, configDir := isolatedWithToken(t)
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
				for _, want := range []string{c.where, "--block CLAUDE_CODE_OAUTH_TOKEN", "UNSET VAR CLAUDE_CODE_OAUTH_TOKEN", "/logout"} {
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

	shared := filepath.Join(root, "shared")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	env, _ = PrepareLaunchEnv(shared)
	if got, _ := envValue(t, env, OAuthTokenEnv); got != "sk-ant-oat01-DEFAULT" {
		t.Fatalf("shared playbook token = %q, want the DEFAULTS one", got)
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
