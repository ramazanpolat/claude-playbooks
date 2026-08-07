package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const (
	grantJSON = `"claudeAiOauth":{"accessToken":"STALE","refreshToken":"R","expiresAt":1}`
	mcpJSON   = `"mcpOAuth":{"notion|abc":{"accessToken":"MCP","serverName":"notion"}}`
)

// readStore returns the parsed credential store at path.
func readStore(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parsing %s: %v (%s)", path, err, data)
	}
	return m
}

// writeStore seeds a credential file with the given raw body.
func writeStore(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, CredentialsFileName)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// The whole point: the grant that can hijack a working token must be gone.
func TestQuarantineRemovesGrant(t *testing.T) {
	dir := t.TempDir()
	p := writeStore(t, dir, `{`+grantJSON+`}`)

	if err := QuarantineStoredOAuth(dir); err != nil {
		t.Fatalf("QuarantineStoredOAuth: %v", err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("store still present after quarantine (stat err = %v)", err)
	}
}

// MCP server logins live in the same file and have nothing to do with account
// auth. Removing the whole file would silently sign the user out of every MCP
// server they had connected.
func TestQuarantinePreservesSiblingKeys(t *testing.T) {
	dir := t.TempDir()
	p := writeStore(t, dir, `{`+grantJSON+`,`+mcpJSON+`}`)

	if err := QuarantineStoredOAuth(dir); err != nil {
		t.Fatalf("QuarantineStoredOAuth: %v", err)
	}

	store := readStore(t, p)
	if _, present := store[oauthCredentialKey]; present {
		t.Fatalf("grant survived quarantine: %v", store)
	}
	if _, present := store["mcpOAuth"]; !present {
		t.Fatalf("mcpOAuth was destroyed by quarantine: %v", store)
	}
}

// The critical safety property. A playbook's store is often a symlink to the
// global one; rewriting through it would strip the GLOBAL grant, destroying the
// copy that makes quarantine recoverable in the first place.
func TestQuarantineNeverWritesThroughSymlink(t *testing.T) {
	globalDir := t.TempDir()
	global := writeStore(t, globalDir, `{`+grantJSON+`,`+mcpJSON+`}`)

	dir := t.TempDir()
	link := filepath.Join(dir, CredentialsFileName)
	if err := os.Symlink(global, link); err != nil {
		t.Fatal(err)
	}

	if err := QuarantineStoredOAuth(dir); err != nil {
		t.Fatalf("QuarantineStoredOAuth: %v", err)
	}

	// The global store must be untouched, grant included.
	gstore := readStore(t, global)
	if _, present := gstore[oauthCredentialKey]; !present {
		t.Fatal("quarantine wrote through the symlink and stripped the GLOBAL grant")
	}

	// The playbook must no longer expose a grant.
	if info, err := os.Lstat(link); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			t.Fatal("playbook still symlinks to the global grant")
		}
		if _, present := readStore(t, link)[oauthCredentialKey]; present {
			t.Fatal("playbook store still carries a grant")
		}
	}
}

// Detaching a symlink must not lose the sibling keys it was providing.
func TestQuarantineSymlinkRematerialisesSiblings(t *testing.T) {
	globalDir := t.TempDir()
	global := writeStore(t, globalDir, `{`+grantJSON+`,`+mcpJSON+`}`)

	dir := t.TempDir()
	link := filepath.Join(dir, CredentialsFileName)
	if err := os.Symlink(global, link); err != nil {
		t.Fatal(err)
	}
	if err := QuarantineStoredOAuth(dir); err != nil {
		t.Fatalf("QuarantineStoredOAuth: %v", err)
	}

	if _, present := readStore(t, link)["mcpOAuth"]; !present {
		t.Fatal("detaching the symlink lost the MCP logins it had been providing")
	}
}

// A symlink whose target holds only a grant leaves nothing worth keeping, so no
// empty file should be left behind.
func TestQuarantineSymlinkGrantOnlyLeavesNoFile(t *testing.T) {
	globalDir := t.TempDir()
	global := writeStore(t, globalDir, `{`+grantJSON+`}`)

	dir := t.TempDir()
	link := filepath.Join(dir, CredentialsFileName)
	if err := os.Symlink(global, link); err != nil {
		t.Fatal(err)
	}
	if err := QuarantineStoredOAuth(dir); err != nil {
		t.Fatalf("QuarantineStoredOAuth: %v", err)
	}

	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatalf("expected no file at the link path (lstat err = %v)", err)
	}
	if _, present := readStore(t, global)[oauthCredentialKey]; !present {
		t.Fatal("global grant was destroyed")
	}
}

// Quarantine runs on every token-auth launch, so it must be a no-op the second
// time rather than churning the file or erroring.
func TestQuarantineIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	p := writeStore(t, dir, `{`+grantJSON+`,`+mcpJSON+`}`)

	if err := QuarantineStoredOAuth(dir); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	first, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := QuarantineStoredOAuth(dir); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	second, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if !first.ModTime().Equal(second.ModTime()) {
		t.Fatal("second pass rewrote a store that already had no grant")
	}
}

// The rewritten store holds MCP tokens; it must not become world-readable.
func TestQuarantineKeepsFilePrivate(t *testing.T) {
	dir := t.TempDir()
	p := writeStore(t, dir, `{`+grantJSON+`,`+mcpJSON+`}`)

	if err := QuarantineStoredOAuth(dir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("quarantined store mode = %o, want 600", perm)
	}
}

// Absent, empty and unparseable stores must not abort a launch.
func TestQuarantineDegradesGracefully(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		if err := QuarantineStoredOAuth(t.TempDir()); err != nil {
			t.Fatalf("absent store: %v", err)
		}
	})

	t.Run("empty file", func(t *testing.T) {
		dir := t.TempDir()
		writeStore(t, dir, "")
		if err := QuarantineStoredOAuth(dir); err != nil {
			t.Fatalf("empty store: %v", err)
		}
	})

	t.Run("no grant key", func(t *testing.T) {
		dir := t.TempDir()
		p := writeStore(t, dir, `{`+mcpJSON+`}`)
		if err := QuarantineStoredOAuth(dir); err != nil {
			t.Fatalf("grantless store: %v", err)
		}
		if _, present := readStore(t, p)["mcpOAuth"]; !present {
			t.Fatal("grantless store was damaged")
		}
	})

	t.Run("malformed json is left alone", func(t *testing.T) {
		dir := t.TempDir()
		p := writeStore(t, dir, `{"claudeAiOauth":{`)
		if err := QuarantineStoredOAuth(dir); err == nil {
			t.Fatal("malformed store: want an advisory error, got nil")
		}
		// Unreadable is not the same as disposable: it must still be there for
		// the user to inspect.
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("malformed store was deleted: %v", err)
		}
	})

	t.Run("dangling symlink", func(t *testing.T) {
		dir := t.TempDir()
		link := filepath.Join(dir, CredentialsFileName)
		if err := os.Symlink(filepath.Join(t.TempDir(), "gone"), link); err != nil {
			t.Fatal(err)
		}
		if err := QuarantineStoredOAuth(dir); err != nil {
			t.Fatalf("dangling symlink: %v", err)
		}
	})
}

// storeHasOAuthGrant gates the copy-to-global path in LinkCredentials, so a
// store that merely parses must not read as one carrying an account grant.
func TestStoreHasOAuthGrant(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"full grant", `{` + grantJSON + `}`, true},
		{"grant plus siblings", `{` + grantJSON + `,` + mcpJSON + `}`, true},
		{"siblings only", `{` + mcpJSON + `}`, false},
		{"empty object", `{}`, false},
		{"null grant", `{"claudeAiOauth":null}`, false},
		{"grant without access token", `{"claudeAiOauth":{"refreshToken":"R"}}`, false},
		{"empty access token", `{"claudeAiOauth":{"accessToken":""}}`, false},
		{"malformed", `{"claudeAiOauth":{`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			p := writeStore(t, dir, tc.body)
			if got := storeHasOAuthGrant(p); got != tc.want {
				t.Fatalf("storeHasOAuthGrant(%s) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
	t.Run("absent file", func(t *testing.T) {
		if storeHasOAuthGrant(filepath.Join(t.TempDir(), "nope")) {
			t.Fatal("absent store reported a grant")
		}
	})
}

// The regression this fix could otherwise cause: after quarantine a playbook
// store holds only sibling keys with a fresh mtime. A later no-token launch
// runs LinkCredentials, whose copy-to-global branch triggers on "newer than
// global" -- and would overwrite the account's real credentials with a store
// that has no grant at all.
func TestLinkCredentialsNeverOverwritesGlobalWithGrantlessStore(t *testing.T) {
	globalDir := t.TempDir()
	global := writeStore(t, globalDir, `{`+grantJSON+`}`)

	target := t.TempDir()
	// Grantless, and deliberately newer than the global store.
	writeStore(t, target, `{`+mcpJSON+`}`)

	if err := LinkCredentials(target, global); err != nil {
		t.Fatalf("LinkCredentials: %v", err)
	}

	if _, present := readStore(t, global)[oauthCredentialKey]; !present {
		t.Fatal("global grant was overwritten by a grantless playbook store")
	}
}
