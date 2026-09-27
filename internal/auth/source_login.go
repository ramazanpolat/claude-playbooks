package auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// A playbook source never carries a login. A .credentials.json shipped in a
// source became the playbook's store, and the first shared sync copied it,
// as the newer store, over ~/.claude/.credentials.json: installing a source
// could replace the machine's login with the source's account
// (docs/known-issues/shared-launch-copies-own-login-over-machine-login.md).
// Account state in a shipped .claude.json seeds an identity the same way:
// the metadata sync only fills keys that are absent.

// SourceStateKeys are the .claude.json keys a source must not carry: who is
// logged in (accountStateKeys) and what Claude Code fetched for that account
// (identityStateKeys), in that order, each once.
func SourceStateKeys() []string {
	keys := slices.Clone(accountStateKeys)
	for _, k := range identityStateKeys {
		if !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	return keys
}

// StripSourceLogin removes, from a staged copy of a source (never the source
// itself), its .credentials.json, whatever it is, and SourceStateKeys from
// its .claude.json. A .claude.json that is a link (the copy keeps a link
// that leaves the source) is removed, never written through: it could
// point at the pilot's own state. It reports what it removed, by name only.
func StripSourceLogin(dir string) (credentials, stateLink bool, keys []string, err error) {
	creds := filepath.Join(dir, CredentialsFileName)
	if _, lerr := os.Lstat(creds); lerr == nil {
		if err := os.Remove(creds); err != nil {
			return false, false, nil, err
		}
		credentials = true
	} else if !os.IsNotExist(lerr) {
		return false, false, nil, lerr
	}
	state := filepath.Join(dir, StateFileName)
	if info, lerr := os.Lstat(state); lerr == nil && info.Mode()&os.ModeSymlink != 0 {
		if err := os.Remove(state); err != nil {
			return credentials, false, nil, err
		}
		return credentials, true, nil, nil
	}
	keys, err = removeStateKeys(state, "")
	return credentials, false, keys, err
}

// SetAsideSourceLogin is StripSourceLogin for a directory linked in place,
// whose files are the pilot's: nothing is deleted. A .credentials.json file
// (a link is the shared store, left to the sync) is renamed to
// .credentials.json.cpb-ignored-<stamp>, and .claude.json is copied to
// .claude.json.cpb-backup-<stamp> before SourceStateKeys leave it. An
// isolated directory keeps both: its login is its own.
func SetAsideSourceLogin(dir string, now time.Time) (credsTo string, keys []string, backup string, err error) {
	if isAuthIsolated(dir) {
		return "", nil, "", nil
	}
	stamp := now.Format("2006-01-02-15_04_05")
	creds := filepath.Join(dir, CredentialsFileName)
	if info, lerr := os.Lstat(creds); lerr == nil && info.Mode().IsRegular() {
		credsTo = creds + ".cpb-ignored-" + stamp
		if err := os.Rename(creds, credsTo); err != nil {
			return "", nil, "", err
		}
	} else if lerr != nil && !os.IsNotExist(lerr) {
		return "", nil, "", lerr
	}
	state := filepath.Join(dir, StateFileName)
	backup = state + ".cpb-backup-" + stamp
	keys, err = removeStateKeys(state, backup)
	if len(keys) == 0 {
		backup = ""
	}
	return credsTo, keys, backup, err
}

// removeStateKeys removes SourceStateKeys from the .claude.json at path,
// first copying it to backup when backup is set, and returns the keys it
// removed. An absent or empty file is nothing to do, and so is anything but
// a regular file: a link is never written through. Invalid JSON is an error
// (a state file cpb cannot read is not guessed at).
func removeStateKeys(path, backup string) ([]string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	var state map[string]json.RawMessage
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("invalid %s at %s: %w", StateFileName, path, err)
	}
	var removed []string
	for _, key := range SourceStateKeys() {
		if _, ok := state[key]; ok {
			delete(state, key)
			removed = append(removed, key)
		}
	}
	if len(removed) == 0 {
		return nil, nil
	}
	if backup != "" {
		if err := copyFile(path, backup, 0o600); err != nil {
			return nil, err
		}
	}
	out, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return nil, err
	}
	out = append(out, '\n')
	if err := writeFileWithMode(path, out, info.Mode().Perm()&0o600); err != nil {
		return nil, err
	}
	return removed, nil
}
