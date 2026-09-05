package auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// oauthCredentialKey is the object inside .credentials.json that holds the
// account's own OAuth grant. Sibling keys (notably mcpOAuth, which holds MCP
// server logins) are unrelated to account auth and must survive untouched.
const oauthCredentialKey = "claudeAiOauth"

// QuarantineStoredOAuth removes the account OAuth grant from configDir's
// credential store, leaving every sibling key in place.
//
// This exists because of how Claude Code recovers from a 401 while a long-lived
// token governs the session. Its recovery path re-reads the config dir's
// credential store and, finding an accessToken different from the one in use,
// assigns that stored value to process.env.CLAUDE_CODE_OAUTH_TOKEN:
//
//	if (o?.accessToken && o.accessToken !== e) {
//	  if (te.CLAUDE_CODE_OAUTH_TOKEN) process.env.CLAUDE_CODE_OAUTH_TOKEN = o.accessToken
//
// The intent is to pick up a token some sibling process refreshed. Under token
// auth nothing ever refreshes: an env-var token is resolved with a synthesized
// refreshToken of null, so the refresh driver returns "not_needed" and the
// stored grant is never renewed. It therefore expires and stays expired, and
// the first transient 401 swaps a working long-lived token for a dead one --
// which cannot itself be refreshed either. The session is then unrecoverable
// and demands an interactive /login, which is the very thing token auth is
// adopted to prevent.
//
// So under token auth the stored grant is pure liability: never used to
// authenticate (the resolver prefers the environment), never refreshed, and
// able to displace a working credential. Removing it makes the recovery path a
// no-op -- it finds no accessToken and leaves the environment alone.
//
// This is recoverable by construction. The grant is only ever removed from a
// playbook's own config dir, never from the global store, and a later launch
// without a token runs the full SyncCredentials path, which re-links the config
// dir to the global credentials.
//
// The returned error is advisory; callers should warn rather than abort, since
// a playbook still launches correctly on the environment token.
func QuarantineStoredOAuth(configDir string) error {
	// The global store is the recovery path, not a playbook's own copy: a later
	// launch without a token re-links config dirs to it, and on darwin it is
	// also what the Keychain is materialised into. Stripping it would remove the
	// grant every playbook falls back to and leave nothing to re-link. Reachable
	// via `start ~/.claude`, which binds an arbitrary config dir.
	if isGlobalConfigDir(configDir) {
		return nil
	}

	path := filepath.Join(configDir, CredentialsFileName)

	// Lstat, not Stat: a symlink must be recognised before anything is written,
	// or the rewrite below would follow it and strip the GLOBAL credentials --
	// destroying the very copy that makes this operation recoverable.
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	isLink := info.Mode()&os.ModeSymlink != 0

	// The reverse alias: ~/.claude/.credentials.json may itself be a symlink
	// INTO this config dir (a shared store kept elsewhere), in which case
	// this dir's REGULAR file is the global store's target and stripping it
	// strips the global grant. Only a regular file can be that target: the
	// ordinary outgoing playbook link (.credentials.json -> ~/.claude/...)
	// resolves to the same file too, and must still be detached below.
	if !isLink && isGlobalCredentialsFile(path) {
		return nil
	}

	data, err := os.ReadFile(path) // follows the link deliberately: we need its content
	if err != nil {
		if os.IsNotExist(err) {
			return nil // dangling symlink; nothing to quarantine
		}
		return err
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil
	}

	var store map[string]json.RawMessage
	if err := json.Unmarshal(data, &store); err != nil {
		// Not a shape we understand. Leaving it alone is safer than deleting
		// it: an unreadable store cannot be reconstructed, and Claude Code's
		// own reader will reject it the same way.
		return fmt.Errorf("credentials at %s are not valid JSON: %w", path, err)
	}
	if _, present := store[oauthCredentialKey]; !present {
		return nil // already quarantined, or never held a grant
	}
	delete(store, oauthCredentialKey)

	// A symlink is removed rather than written through, so the global store it
	// points at keeps its grant. Any sibling keys are re-materialised below as
	// this config dir's own file.
	if isLink {
		if err := os.Remove(path); err != nil {
			return err
		}
	}

	// Nothing left worth keeping: drop the file entirely rather than leave an
	// empty object behind.
	if len(store) == 0 {
		if isLink {
			return nil // link already removed above
		}
		return os.Remove(path)
	}

	out, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	return writeFilePrivate(path, out)
}

// globalClaudeDir returns the absolute path of the global Claude config
// directory, the one EnsureGlobalCredentials treats as the shared source.
func globalClaudeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Abs(filepath.Join(home, ".claude"))
}

// isGlobalConfigDir reports whether dir IS the global Claude config directory,
// by filesystem identity rather than by spelling: a symlink to ~/.claude (or a
// symlinked ancestor, or a symlinked ~/.claude itself) names the same
// directory, and a credential mutation through it lands on the global store.
// String comparison of absolute paths missed every one of those.
func isGlobalConfigDir(dir string) bool {
	global, err := globalClaudeDir()
	if err != nil || global == "" {
		return false
	}
	return sameDir(dir, global)
}

// isGlobalCredentialsFile reports whether p and the global credentials file
// are the same file once every symlink on either side is resolved.
func isGlobalCredentialsFile(p string) bool {
	global, err := globalClaudeDir()
	if err != nil || global == "" {
		return false
	}
	gi, err := os.Stat(filepath.Join(global, CredentialsFileName))
	if err != nil {
		return false
	}
	pi, err := os.Stat(p)
	if err != nil {
		return false
	}
	return os.SameFile(gi, pi)
}

// sameDir reports whether a and b resolve to the same directory. Symlinks are
// evaluated on both sides and os.SameFile decides, so no path spelling can
// disguise identity. A path that does not exist is never the same as one that
// does.
func sameDir(a, b string) bool {
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		return false
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		return false
	}
	if ra == rb {
		return true
	}
	ia, err := os.Stat(ra)
	if err != nil {
		return false
	}
	ib, err := os.Stat(rb)
	if err != nil {
		return false
	}
	return os.SameFile(ia, ib)
}

// writeFilePrivate writes data at mode 0600 via a temporary file and a rename,
// so a concurrent reader never observes a half-written credential store.
func writeFilePrivate(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".credentials-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds

	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// storeHasOAuthGrant reports whether the credential store at path carries an
// account OAuth grant.
func storeHasOAuthGrant(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var store map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimSpace(data), &store); err != nil {
		return false
	}
	raw, present := store[oauthCredentialKey]
	if !present {
		return false
	}
	// An explicit null, or a grant with no access token, is not a grant worth
	// propagating to the global store.
	var grant struct {
		AccessToken string `json:"accessToken"`
	}
	if err := json.Unmarshal(raw, &grant); err != nil {
		return false
	}
	return grant.AccessToken != ""
}
