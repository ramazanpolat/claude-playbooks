package auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

const CredentialsFileName = ".credentials.json"
const StateFileName = ".claude.json"

var accountStateKeys = []string{
	"oauthAccount",
	"userID",
	"hasCompletedOnboarding",
	"lastOnboardingVersion",
	"installMethod",
	"claudeCodeFirstTokenDate",
	"autoUpdates",
	"autoUpdatesProtectedForNative",
	"lastReleaseNotesSeen",
}

var findGenericPassword = func(service string) ([]byte, error) {
	return exec.Command("security", "find-generic-password", "-s", service, "-w").Output()
}

// SyncCredentials ensures the target config directory shares the global Claude Code authentication.
func SyncCredentials(targetDir string) error {
	if isAuthIsolated(targetDir) {
		return detachSharedCredentials(targetDir)
	}
	globalCreds, ownLeft, err := syncMachineStore(targetDir)
	if err != nil {
		return err
	}
	if globalCreds == "" || ownLeft {
		// No machine login to link, or the playbook's own login could not be
		// ordered against it: the playbook keeps what it holds.
		return SyncAccountMetadata(targetDir)
	}
	if err := LinkCredentials(targetDir, globalCreds); err != nil {
		return err
	}
	return SyncAccountMetadata(targetDir)
}

// EnsureGlobalCredentials returns the usable global Claude credentials file.
// On macOS it also writes the machine's Keychain item into that file when the
// item holds a newer login than the file, or the file is missing (see
// syncMachineStore).
func EnsureGlobalCredentials() (string, error) {
	path, _, err := syncMachineStore("")
	return path, err
}

// syncMachineStore brings the machine store (~/.claude/.credentials.json) to
// the newest login among its candidates: on macOS the machine's Keychain item,
// the store itself, and, when targetDir is a shared playbook of the machine's
// own account, the file a refresh or /login left in its place
// (sharedOwnLogin). It decides before it writes: the candidate whose grant
// expires last wins (newestLogin), and is written only when its bytes differ,
// through a temporary file renamed over the store (replaceFile). The Keychain
// is only ever read. When no candidate can be told to be the newest, nothing
// is written, one notice says which sources and why, and ownLeft reports
// whether the playbook's file was among them, so the caller leaves it in
// place. globalCreds is "" when the machine store holds no usable value.
func syncMachineStore(targetDir string) (globalCreds string, ownLeft bool, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false, err
	}
	globalClaude := filepath.Join(home, ".claude")
	globalCreds = StorePath(globalClaude)

	var cands []loginValue
	if hostOS == "darwin" {
		out, kerr := keychainSecret(machineKeychainService)
		out = bytes.TrimSpace(out)
		if kerr == nil && len(out) > 0 {
			if !json.Valid(out) {
				return "", false, fmt.Errorf("Keychain item %s did not contain valid JSON credentials", machineKeychainService)
			}
			cands = append(cands, loginValue{source: "the Keychain item " + machineKeychainService, data: out})
		}
	}
	current, err := os.ReadFile(globalCreds)
	if err != nil && !os.IsNotExist(err) {
		return "", false, err
	}
	current = bytes.TrimSpace(current)
	if len(current) > 0 {
		cands = append(cands, loginValue{source: globalCreds, data: current})
	}
	withOwn := false
	if len(cands) > 0 && targetDir != "" {
		if own := sharedOwnLogin(targetDir, globalCreds); own != nil {
			withOwn = true
			cands = append(cands, loginValue{source: StorePath(targetDir), data: own})
		}
	}
	if len(cands) == 0 {
		return "", false, nil
	}

	win, why := newestLogin(cands)
	if win < 0 {
		Notice(why)
		if !json.Valid(current) {
			return "", withOwn, nil
		}
		return globalCreds, withOwn, nil
	}
	value := cands[win].data
	if !json.Valid(value) {
		// The machine store is the only value there is, and it does not parse.
		return "", false, fmt.Errorf("invalid JSON")
	}
	if !bytes.Equal(value, current) {
		if err := os.MkdirAll(globalClaude, 0755); err != nil {
			return "", false, err
		}
		if _, err := replaceFile(globalCreds, value, 0600); err != nil {
			return "", false, err
		}
	}
	return globalCreds, false, nil
}

// sharedOwnLogin returns the login a shared playbook holds in a file of its
// own, trimmed, when it is a candidate for the machine store: a regular file,
// not the machine store itself, that parses and carries an account grant of
// the machine's own account. nil otherwise; LinkCredentials then deals with
// it as before (another account's login is set aside, an unusable file is an
// error).
func sharedOwnLogin(targetDir, globalCreds string) []byte {
	if sameDir(targetDir, filepath.Dir(globalCreds)) {
		return nil
	}
	store := StorePath(targetDir)
	info, err := os.Lstat(store)
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	if gi, err := os.Stat(globalCreds); err == nil && os.SameFile(info, gi) {
		return nil
	}
	if usable, err := credentialsFileUsable(store); err != nil || !usable || !storeHasOAuthGrant(store) {
		return nil
	}
	if !sameAccount(targetDir) {
		return nil
	}
	data, err := os.ReadFile(store)
	if err != nil {
		return nil
	}
	return bytes.TrimSpace(data)
}

// loginValue is one candidate value for the machine store: where it comes
// from, and its bytes, trimmed. Nothing but the grant's expiry is ever read
// out of it, and only the source is ever printed.
type loginValue struct {
	source string
	data   []byte
}

// newestLogin returns the index of the candidate holding the newest login,
// or -1 and a one-line reason when that cannot be told. Identical values are
// one value, and a single value is the newest. Of values that differ, the
// newest is the one whose grant expires last (claudeAiOauth.expiresAt): a
// refresh or a /login issues a grant that expires later. A value with no
// readable expiry, or two differing values with the same expiry, cannot be
// ordered, and then none is the newest. File times are never consulted: a
// rewrite of an unchanged or older value moves them too.
func newestLogin(cands []loginValue) (int, string) {
	var distinct []int
	for i, c := range cands {
		if len(c.data) == 0 {
			continue
		}
		dup := false
		for _, j := range distinct {
			if bytes.Equal(cands[j].data, c.data) {
				dup = true
				break
			}
		}
		if !dup {
			distinct = append(distinct, i)
		}
	}
	switch len(distinct) {
	case 0:
		return -1, ""
	case 1:
		return distinct[0], ""
	}
	names := make([]string, len(distinct))
	for k, i := range distinct {
		names[k] = cands[i].source
	}
	undecided := func(reason string) (int, string) {
		return -1, fmt.Sprintf("cannot tell which login is newest (%s): %s; wrote nothing and left them as they are",
			strings.Join(names, ", "), reason)
	}
	best, tie := -1, -1
	var bestExp int64
	for _, i := range distinct {
		exp, ok := grantExpiry(cands[i].data)
		if !ok {
			what := " has no grant expiry"
			if !json.Valid(cands[i].data) {
				what = " does not parse"
			}
			return undecided(cands[i].source + what)
		}
		switch {
		case best < 0 || exp > bestExp:
			best, bestExp, tie = i, exp, -1
		case exp == bestExp:
			tie = i
		}
	}
	if tie >= 0 {
		return undecided(cands[best].source + " and " + cands[tie].source + " differ but their grants expire at the same time")
	}
	return best, ""
}

// grantExpiry is claudeAiOauth.expiresAt (epoch milliseconds) in a store's
// bytes; ok is false when they do not parse, hold no grant, or the grant has
// no positive integer expiry.
func grantExpiry(data []byte) (int64, bool) {
	var store struct {
		Grant *struct {
			ExpiresAt json.Number `json:"expiresAt"`
		} `json:"claudeAiOauth"`
	}
	if json.Unmarshal(data, &store) != nil || store.Grant == nil {
		return 0, false
	}
	exp, err := store.Grant.ExpiresAt.Int64()
	if err != nil || exp <= 0 {
		return 0, false
	}
	return exp, true
}

// ownLoginNewer orders a playbook's own login at own against the machine
// store at machine (newestLogin): newer is true when own holds the newer
// login, decided is false when the two cannot be ordered, and why then says
// so. An absent or empty machine store holds no login, so own is newer.
func ownLoginNewer(own, machine string) (newer, decided bool, why string, err error) {
	ownData, err := os.ReadFile(own)
	if err != nil {
		return false, false, "", err
	}
	machineData, err := os.ReadFile(machine)
	if err != nil && !os.IsNotExist(err) {
		return false, false, "", err
	}
	cands := []loginValue{
		{source: machine, data: bytes.TrimSpace(machineData)},
		{source: own, data: bytes.TrimSpace(ownData)},
	}
	win, why := newestLogin(cands)
	if win < 0 {
		return false, false, why, nil
	}
	return win == 1, true, "", nil
}

// LinkCredentials points targetDir/.credentials.json at sourceCreds. Existing
// valid regular credentials are left intact only if isAuthIsolated is true.
// Otherwise, they are synchronized to the global credentials file (if newer) and
// replaced with a symlink to prevent authentication desync.
func LinkCredentials(targetDir, sourceCreds string) error {
	targetInfo, err := os.Stat(targetDir)
	if err != nil {
		return err
	}
	if !targetInfo.IsDir() {
		return fmt.Errorf("%s is not a directory", targetDir)
	}
	if isAuthIsolated(targetDir) {
		return detachSharedCredentials(targetDir)
	}

	sourceAbs, err := filepath.Abs(sourceCreds)
	if err != nil {
		return err
	}
	// The target must not BE the global directory, whatever it is called: a
	// symlink to ~/.claude passed the old string comparison, and linking then
	// replaced the global store with a symlink to itself.
	if sameDir(targetDir, filepath.Dir(sourceAbs)) {
		return nil
	}

	targetCreds := StorePath(targetDir)
	targetLinfo, err := os.Lstat(targetCreds)
	exists := err == nil
	if exists {
		// Whatever the target's store is spelled as -- a regular file that
		// IS the global store reached through an alias, or a symlink chain
		// the global store itself passes through (~/.claude/.credentials.json
		// -> here -> real store) -- if it already resolves to the same file
		// as the source there is nothing to link, and replacing it would
		// either destroy the store or close a symlink cycle.
		if ti, err := os.Stat(targetCreds); err == nil {
			if si, err := os.Stat(sourceAbs); err == nil && os.SameFile(ti, si) {
				return nil
			}
		}
	}

	if exists {
		if targetLinfo.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(targetCreds)
			if err == nil {
				linkAbs := link
				if !filepath.IsAbs(linkAbs) {
					linkAbs = filepath.Join(targetDir, linkAbs)
				}
				linkAbs, _ = filepath.Abs(linkAbs)
				if linkAbs == sourceAbs {
					return nil
				}
			}
			if err := os.Remove(targetCreds); err != nil {
				return err
			}
		} else {
			usable, err := credentialsFileUsable(targetCreds)
			if err != nil {
				return fmt.Errorf("existing credentials at %s are not usable: %w", targetCreds, err)
			}
			if isAuthIsolated(targetDir) {
				if usable {
					return nil
				}
				if err := os.Remove(targetCreds); err != nil {
					return err
				}
			} else {
				// "Usable" means parseable, which is not the same as "carries an
				// account grant". A store can be valid JSON holding only sibling
				// keys -- mcpOAuth, or the remainder left by QuarantineStoredOAuth
				// -- and copying that over the global store would erase the
				// account's credentials while looking like a routine sync.
				if usable && storeHasOAuthGrant(targetCreds) {
					// Claude Code writes its plaintext store by rename, so a
					// refresh or /login in a shared playbook leaves a file here.
					// Only the machine's own account may be copied over the
					// machine's store; any other login, or one that cannot be
					// told apart, is set aside
					// (CHANGELOG.md, v3.23.1).
					_, err := os.Stat(sourceAbs)
					switch {
					case os.IsNotExist(err):
						// No machine login to replace: this one becomes it.
						if err := copyFile(targetCreds, sourceAbs, 0600); err != nil {
							return fmt.Errorf("failed to copy credentials to global: %w", err)
						}
					case err != nil:
						return err
					case sameAccount(targetDir):
						// Only a newer login goes over the machine's; an older
						// one, or the same one, gives way to the link below.
						// Two that cannot be ordered are both left as they are.
						newer, decided, why, err := ownLoginNewer(targetCreds, sourceAbs)
						if err != nil {
							return err
						}
						if !decided {
							Notice(why)
							return nil
						}
						if newer {
							if err := copyFile(targetCreds, sourceAbs, 0600); err != nil {
								return fmt.Errorf("failed to copy newer credentials to global: %w", err)
							}
						}
					default:
						if err := setAsideOwnLogin(targetDir, "linked the machine's login", false, ""); err != nil {
							return err
						}
						return os.Symlink(sourceAbs, targetCreds)
					}
				}
				if err := os.Remove(targetCreds); err != nil {
					return err
				}
			}
		}
	}

	return os.Symlink(sourceAbs, targetCreds)
}

func detachSharedCredentials(targetDir string) error {
	targetCreds := StorePath(targetDir)
	info, err := os.Lstat(targetCreds)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return os.Remove(targetCreds)
	}
	return nil
}

// IsolatedLoginEnv forces the isolation path for a launch when set to "true".
const IsolatedLoginEnv = "CPB_ISOLATED_LOGIN"

// IsAuthIsolated reports whether launches of targetDir run with isolated
// authentication (manifest isolated_login, or the override variable).
func IsAuthIsolated(targetDir string) bool { return isAuthIsolated(targetDir) }

func isAuthIsolated(targetDir string) bool {
	if os.Getenv(IsolatedLoginEnv) == "true" {
		return true
	}
	// A read error on the way up is not a reason to drop isolation: the
	// nearest valid manifest still decides, and only its absence means no.
	m, _ := manifest.Nearest(targetDir)
	if m == nil {
		return false
	}
	return m.IsolatedLogin
}

// copyFile copies src over dst with replaceFile: never in place, and not at
// all when dst already holds the same bytes.
func copyFile(src, dst string, perm os.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	_, err = replaceFile(dst, data, perm)
	return err
}

// replaceFile puts data at path unless the file there already holds exactly
// those bytes (then nothing is written and its times do not move). A write
// goes through writeFileAtomic, so a reader sees the old file or the new one,
// never an empty or partial one. Like os.WriteFile it writes through a
// symlink at path: the file the link resolves to is replaced, and the link
// stays.
func replaceFile(path string, data []byte, perm os.FileMode) (bool, error) {
	dest, err := linkDestination(path)
	if err != nil {
		return false, err
	}
	if cur, err := os.ReadFile(dest); err == nil && bytes.Equal(cur, data) {
		return false, nil
	}
	if err := writeFileAtomic(dest, data, perm); err != nil {
		return false, err
	}
	return true, nil
}

// linkDestination follows the symlinks at path, as a write through it would,
// to the file it names, which need not exist yet.
func linkDestination(path string) (string, error) {
	cur := path
	for hops := 0; hops < 40; hops++ {
		info, err := os.Lstat(cur)
		if os.IsNotExist(err) {
			return cur, nil
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			return cur, nil
		}
		target, err := os.Readlink(cur)
		if err != nil {
			return "", err
		}
		if cur, err = resolveLinkTarget(cur, target); err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("too many levels of symbolic links at %s", path)
}

// SyncAccountMetadata copies Claude Code's non-token account metadata into the
// target config. Interactive Claude startup uses this state to decide whether a
// config directory is already logged in.
func SyncAccountMetadata(targetDir string) error {
	if isAuthIsolated(targetDir) {
		return nil
	}
	sourceState, err := findAccountState(targetDir)
	if err != nil {
		return err
	}
	if sourceState == nil {
		return nil
	}

	targetPath := filepath.Join(targetDir, StateFileName)
	targetState := map[string]any{}
	if data, err := os.ReadFile(targetPath); err == nil {
		if len(bytes.TrimSpace(data)) > 0 {
			if err := json.Unmarshal(data, &targetState); err != nil {
				return fmt.Errorf("invalid %s at %s: %w", StateFileName, targetPath, err)
			}
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	changed := false
	for _, key := range accountStateKeys {
		value, ok := sourceState[key]
		if !ok {
			continue
		}
		if key == "oauthAccount" {
			if changedAccount := mergeMap(targetState, key, value); changedAccount {
				changed = true
			}
			continue
		}
		if _, exists := targetState[key]; !exists {
			targetState[key] = value
			changed = true
		}
	}
	if !changed {
		return nil
	}

	data, err := json.MarshalIndent(targetState, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.WriteFile(targetPath, data, 0600); err != nil {
		return err
	}
	return os.Chmod(targetPath, 0600)
}

func credentialsFileUsable(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return false, nil
	}
	if !json.Valid(data) {
		return false, fmt.Errorf("invalid JSON")
	}
	return true, nil
}

// machineStateFiles are the two places Claude Code keeps the machine's
// account state: ~/.claude.json when it runs with CLAUDE_CONFIG_DIR unset (a
// raw claude), ~/.claude/.claude.json when it runs with CLAUDE_CONFIG_DIR set
// to ~/.claude (cpb start ~/.claude). Both pair with the one machine store.
func machineStateFiles() (canonical, nested string, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	return filepath.Join(home, StateFileName), filepath.Join(home, ".claude", StateFileName), nil
}

// findAccountState returns the machine's own account state. ~/.claude.json
// decides whenever it exists, the file a raw claude keeps: the nested file is
// read only when it is absent, and an unreadable or malformed ~/.claude.json
// is an error, never a reason to fall back. Preferring the nested file let a
// stale one, left by an earlier account, pass for the machine's account and
// promote that account's grant over the machine store.
func findAccountState(targetDir string) (map[string]any, error) {
	canonical, nested, err := machineStateFiles()
	if err != nil {
		return nil, err
	}

	targetState, _ := filepath.Abs(filepath.Join(targetDir, StateFileName))
	// The machine's own state only. Other playbooks' state is never a
	// source: an isolated one may be another account, and seeding its
	// identity into a shared playbook is the cross-account bug this avoids.
	for _, path := range []string{canonical, nested} {
		abs, _ := filepath.Abs(path)
		if abs == targetState {
			continue
		}
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			continue
		}
		return readAccountState(path)
	}
	return nil, nil
}

func readAccountState(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	var state map[string]any
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("invalid %s at %s: %w", StateFileName, path, err)
	}
	if _, ok := state["oauthAccount"]; !ok {
		return nil, nil
	}
	return state, nil
}

func mergeMap(target map[string]any, key string, value any) bool {
	sourceMap, ok := value.(map[string]any)
	if !ok {
		if _, exists := target[key]; exists {
			return false
		}
		target[key] = value
		return true
	}

	existing, ok := target[key].(map[string]any)
	if !ok {
		target[key] = sourceMap
		return true
	}

	changed := false
	for k, v := range sourceMap {
		if _, exists := existing[k]; !exists {
			existing[k] = v
			changed = true
		}
	}
	return changed
}
