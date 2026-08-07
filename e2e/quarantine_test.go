//go:build !windows

// Quarantine of the stored OAuth grant under long-lived token auth.
//
// Claude Code's 401-recovery path re-reads the config dir's credential store
// and assigns a differing stored accessToken to
// process.env.CLAUDE_CODE_OAUTH_TOKEN. Under token auth nothing refreshes that
// stored grant, so it expires and the first transient 401 swaps a working
// long-lived token for a dead one that cannot be refreshed either -- forcing an
// interactive /login. claude-playbook removes the grant so the recovery path
// finds nothing to adopt.
//
// The unit tests in internal/auth cover the file surgery. These assert the
// effect on disk after the real binary has launched, which is the claim that
// matters: between QuarantineStoredOAuth and a launched playbook sit
// PrepareLaunchEnv's branching and both the run and start entry points.
package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const credentialsFile = ".credentials.json"

// seedPlaybookCredentials writes a stored grant into a playbook's config dir,
// alongside an MCP login that must survive.
func seedPlaybookCredentials(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, credentialsFile)
	body := `{"claudeAiOauth":{"accessToken":"STALE","refreshToken":"R","expiresAt":1},` +
		`"mcpOAuth":{"notion|abc":{"accessToken":"MCP"}}}`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func storeKeys(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parsing %s: %v (%s)", path, err, data)
	}
	return m
}

// The fix itself, observed after a real launch.
func TestTokenLaunchQuarantinesStoredGrant(t *testing.T) {
	root := t.TempDir()
	dir := playbook(t, root, "shared", false)
	creds := seedPlaybookCredentials(t, dir)

	childEnv(t, root, launch{
		env:  []string{tokenFile(t, "sk-ant-oat01-FROMFILE")},
		args: []string{"run", "shared"},
	})

	store := storeKeys(t, creds)
	if _, present := store["claudeAiOauth"]; present {
		t.Fatal("stored grant survived a token-auth launch; a transient 401 can still " +
			"swap it in for the long-lived token")
	}
	if _, present := store["mcpOAuth"]; !present {
		t.Fatal("MCP logins were destroyed alongside the grant")
	}
}

// `start` binds a config dir directly and takes its own path into
// PrepareLaunchEnv; it has regressed independently before.
func TestStartQuarantinesStoredGrant(t *testing.T) {
	root := t.TempDir()
	cfg := filepath.Join(t.TempDir(), "startcfg")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, ".playbook"),
		[]byte("name = \"startcfg\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	creds := seedPlaybookCredentials(t, cfg)

	childEnv(t, root, launch{
		env:  []string{tokenFile(t, "sk-ant-oat01-FROMFILE")},
		args: []string{"start", cfg},
	})

	if _, present := storeKeys(t, creds)["claudeAiOauth"]; present {
		t.Fatal("start left the stored grant in place")
	}
}

// The control. Without a token the stored grant is what authenticates the
// session and Claude Code refreshes it normally, so removing it would be
// destructive -- it must be left completely alone.
func TestNoTokenLaunchLeavesStoredGrantIntact(t *testing.T) {
	root := t.TempDir()
	dir := playbook(t, root, "shared", false)
	creds := seedPlaybookCredentials(t, dir)

	childEnv(t, root, launch{
		env:  []string{tokenFileEnv + "=" + filepath.Join(t.TempDir(), "absent")},
		args: []string{"run", "shared"},
	})

	// SyncCredentials may replace the file with a symlink to the global store;
	// either way a grant must remain reachable at that path.
	if _, present := storeKeys(t, creds)["claudeAiOauth"]; !present {
		t.Fatal("a no-token launch removed the grant it depends on")
	}
}

// An isolated playbook authenticates as a different account, so its own stored
// grant is the point of the isolation -- the global token never applies to it
// and must not cause its credentials to be touched.
func TestIsolatedPlaybookKeepsItsOwnGrant(t *testing.T) {
	root := t.TempDir()
	dir := playbook(t, root, "iso", true)
	creds := seedPlaybookCredentials(t, dir)

	childEnv(t, root, launch{
		env:  []string{tokenEnv + "=sk-ant-oat01-INHERITED"},
		args: []string{"run", "iso"},
	})

	if _, present := storeKeys(t, creds)["claudeAiOauth"]; !present {
		t.Fatal("an isolated playbook's own grant was quarantined; the global token " +
			"does not apply to it and must not disturb its credentials")
	}
}

// Quarantine must never reach through a playbook's symlink into the global
// store -- that copy is what makes the operation recoverable.
func TestQuarantineLeavesGlobalStoreIntact(t *testing.T) {
	root := t.TempDir()
	dir := playbook(t, root, "shared", false)

	work := t.TempDir()
	globalDir := filepath.Join(work, ".claude")
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(globalDir, credentialsFile)
	if err := os.WriteFile(global,
		[]byte(`{"claudeAiOauth":{"accessToken":"GLOBAL","refreshToken":"R"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(global, filepath.Join(dir, credentialsFile)); err != nil {
		t.Fatal(err)
	}

	childEnv(t, root, launch{
		env:  []string{tokenFile(t, "sk-ant-oat01-FROMFILE")},
		args: []string{"run", "shared"},
	})

	if _, present := storeKeys(t, global)["claudeAiOauth"]; !present {
		t.Fatal("quarantine wrote through the symlink and stripped the GLOBAL grant")
	}
}
