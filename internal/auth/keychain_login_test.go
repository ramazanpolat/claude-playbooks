package auth

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// stubKeychainItems answers every probe from items (service -> state), absent
// otherwise, and records the services asked.
func stubKeychainItems(t *testing.T, items map[string]KeychainState) *[]string {
	t.Helper()
	var asked []string
	saved := KeychainProbe
	KeychainProbe = func(account, service string) KeychainState {
		asked = append(asked, service)
		if s, ok := items[service]; ok {
			return s
		}
		return KeychainAbsent
	}
	t.Cleanup(func() { KeychainProbe = saved })
	return &asked
}

// The service name is Claude Code's (2.1.292): the machine name, "-", and
// the first 8 hex digits of sha256 of CLAUDE_CONFIG_DIR exactly as passed.
// The value below was confirmed present for a logged-in isolated playbook
// on 2026-10-07 by a presence-only probe; a trailing "/" names another item.
func TestConfigKeychainService(t *testing.T) {
	if s, ok := configKeychainService("/Users/polat/.claude-playbooks/kommander-dev"); !ok || s != "Claude Code-credentials-95a60dda" {
		t.Fatalf("%q %v", s, ok)
	}
	if s, _ := configKeychainService("/Users/polat/.claude-playbooks/kommander-dev/"); s == "Claude Code-credentials-95a60dda" {
		t.Fatal("a trailing slash is another item")
	}
	if _, ok := configKeychainService("/Users/çağ/.claude-playbooks/x"); ok {
		t.Fatal("a non-ASCII path must not be guessed at (Claude Code NFC-normalises it)")
	}
}

func TestKeychainLogin(t *testing.T) {
	dir := "/tmp/pb"
	own, _ := configKeychainService(dir)
	for _, c := range []struct {
		name            string
		items           map[string]KeychainState
		shared, machine bool
		want            KeychainState
	}{
		{"isolated, own item", map[string]KeychainState{own: KeychainPresent}, false, false, KeychainPresent},
		{"isolated, machine item does not count", map[string]KeychainState{machineKeychainService: KeychainPresent}, false, false, KeychainAbsent},
		{"shared, machine item", map[string]KeychainState{machineKeychainService: KeychainPresent}, true, false, KeychainPresent},
		{"shared, nothing", nil, true, false, KeychainAbsent},
		{"unknown is never absent", map[string]KeychainState{own: KeychainUnknown}, false, false, KeychainUnknown},
		{"present wins over unknown", map[string]KeychainState{own: KeychainUnknown, machineKeychainService: KeychainPresent}, true, false, KeychainPresent},
		{"machine dir: machine item only", map[string]KeychainState{own: KeychainPresent}, false, true, KeychainAbsent},
	} {
		stubKeychainItems(t, c.items)
		if got := keychainLogin(dir, c.shared, c.machine); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
	if got := keychainLogin("/tmp/çağ", false, false); got != KeychainUnknown {
		t.Errorf("an underivable name: %s, want unknown", got)
	}
}

// auth status: an isolated playbook whose login lives only in the Keychain
// is no longer "no login"; an unanswerable probe is "unknown", never none.
func TestInspectKeychainLogin(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_PLAYBOOKS_DIR", root)
	dir := filepath.Join(root, "iso")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".playbook"), []byte("isolated_login = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	own, _ := configKeychainService(dir)
	for _, c := range []struct {
		state KeychainState
		login string
		note  string
	}{
		{KeychainPresent, "keychain", "login in the Keychain (used at launch)"},
		{KeychainUnknown, "unknown", "login unknown (Keychain not readable)"},
		{KeychainAbsent, "none", "no login"},
	} {
		stubKeychainItems(t, map[string]KeychainState{own: c.state})
		r := Inspect("iso", dir, time.Now())
		if r.Mode != ModeIsolatedLogin || r.Keychain != c.state || r.Login() != c.login || r.NeedsAttention() != c.note {
			t.Errorf("%s: mode %s keychain %s login %q note %q", c.state, r.Mode, r.Keychain, r.Login(), r.NeedsAttention())
		}
	}
	// A grant in the file store is answered there: no probe at all.
	asked := stubKeychainItems(t, nil)
	if err := os.WriteFile(filepath.Join(dir, CredentialsFileName), []byte(`{"claudeAiOauth":{"expiresAt":0}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := Inspect("iso", dir, time.Now()); r.Login() != "store" || len(*asked) != 0 {
		t.Fatalf("a file grant: login %q, probes %v", r.Login(), *asked)
	}
}

// A store that exists but cannot be parsed may hold a login (the launch
// keeps it): the login is unknown, never none. A store known to hold no
// grant is none.
func TestInspectUnreadableStore(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_PLAYBOOKS_DIR", root)
	dir := filepath.Join(root, "iso")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".playbook"), []byte("isolated_login = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stubKeychainItems(t, nil)
	store := filepath.Join(dir, CredentialsFileName)
	for _, c := range []struct {
		content, login, note string
	}{
		{"{not json", "unknown", "login unknown (" + CredentialsFileName + " not readable)"},
		{`{"claudeAiOauth":{"accessToken":"x","expiresAt":"soon"}}`, "unknown", "login unknown (" + CredentialsFileName + " not readable)"},
		{`{}`, "none", "no login"},
		{"", "none", "no login"},
	} {
		if err := os.WriteFile(store, []byte(c.content), 0o600); err != nil {
			t.Fatal(err)
		}
		r := Inspect("iso", dir, time.Now())
		if r.Login() != c.login || r.NeedsAttention() != c.note {
			t.Errorf("store %q: login %q note %q, want %q %q", c.content, r.Login(), r.NeedsAttention(), c.login, c.note)
		}
	}
}

// An isolated playbook whose store is still a link to the shared one: the
// launch detaches it first, so the grant behind the link is not its login.
// Its own Keychain item is asked instead, and the shared one never is.
func TestInspectIsolatedSharedLink(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_PLAYBOOKS_DIR", root)
	shared := filepath.Join(t.TempDir(), CredentialsFileName)
	if err := os.WriteFile(shared, []byte(`{"claudeAiOauth":{"accessToken":"x","expiresAt":0}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "iso")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".playbook"), []byte("isolated_login = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(shared, filepath.Join(dir, CredentialsFileName)); err != nil {
		t.Fatal(err)
	}
	own, _ := configKeychainService(dir)
	for _, c := range []struct {
		state KeychainState
		login string
	}{
		{KeychainAbsent, "none"},
		{KeychainPresent, "keychain"},
		{KeychainUnknown, "unknown"},
	} {
		asked := stubKeychainItems(t, map[string]KeychainState{own: c.state})
		r := Inspect("iso", dir, time.Now())
		if r.Mode != ModeIsolatedLogin || r.Store != StoreSymlink || r.Login() != c.login {
			t.Errorf("%s: mode %s store %s login %q, want %q", c.state, r.Mode, r.Store, r.Login(), c.login)
		}
		if len(*asked) != 1 || (*asked)[0] != own {
			t.Errorf("%s: probes %v, want only the playbook's own item", c.state, *asked)
		}
	}
	// A dangling link is detached too: not unknown, nothing behind it counts.
	if err := os.Remove(shared); err != nil {
		t.Fatal(err)
	}
	stubKeychainItems(t, nil)
	if r := Inspect("iso", dir, time.Now()); r.Login() != "none" {
		t.Errorf("a dangling shared link: login %q, want none", r.Login())
	}
}
