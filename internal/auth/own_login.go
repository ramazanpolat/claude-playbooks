package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envset"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// A token over a playbook's own login (v4.2.0, accounts). An isolated
// playbook can hold a /login of its own and still be launched on a token
// (its env set's, or --env-set's). The stored login must not stay readable
// during that run: on an error Claude Code swaps a stored login in over the
// token, and the session breaks. v4.0.0 refuses such a launch everywhere;
// earlier releases deleted the grant, and the playbook needed /login again.
//
// On Linux the login is a file: the grant is moved aside for the run
// (.credentials.json.cpb-aside) and put back when the last token run of the
// playbook ends; one left by a killed run comes back at the next launch. On
// macOS the login lives in the Keychain, which cpb cannot move without reading
// it, so such a launch is refused before anything changes (D5, kept for good).

// hostOS is runtime.GOOS; tests replace it.
var hostOS = runtime.GOOS

// The files next to the store, all named after it so one ignore pattern
// covers them: the grant set aside, the lock every token run holds shared for
// its whole run, and the lock that serialises moving the grant.
const (
	asideSuffix     = ".cpb-aside"
	runLockSuffix   = ".cpb-run.lock"
	asideLockSuffix = ".cpb-aside.lock"
	// runPidsSuffix lists the sessions token runs started, one pid a line:
	// a session can outlive the cpb that started it (cpb killed alone), and
	// its token run is live while it is.
	runPidsSuffix = ".cpb-run.pids"
)

// ErrOwnLogin marks the refusal of a token launch over a playbook's own
// login. Launch callers treat it as fatal, like an env set that cannot be
// resolved.
var ErrOwnLogin = errors.New("a token launch over the playbook's own login")

type ownLoginError struct{ msg string }

func (e *ownLoginError) Error() string        { return e.msg }
func (e *ownLoginError) Is(target error) bool { return target == ErrOwnLogin }

// OwnLoginRefusal refuses, on macOS, a launch that would carry a token over
// an isolated playbook's own login. nil everywhere else. eff is the launch's
// effective block (EffectiveBlock: a DEFAULTS token is not in it). It reads
// presence only and changes nothing. name is the playbook's name ("" when
// unknown).
func OwnLoginRefusal(configDir string, eff *manifest.Env, name string) error {
	if !isAuthIsolated(configDir) || !suppliesToken(eff) {
		return nil
	}
	return ownLoginRefusal(configDir, name)
}

// AccountLoginRefusal refuses, before anything is changed, a launch on an
// account that uses no Claude login (account is its name) when the login
// cannot be kept out of the run: a shared playbook, whose store is the
// machine's, or, on macOS, an isolated playbook that holds a login of its
// own. nil when the login can be kept out. name is the playbook's name (""
// when unknown).
func AccountLoginRefusal(configDir, name, account string) error {
	target := "<playbook>"
	if name != "" {
		target = name
	}
	if !isAuthIsolated(configDir) {
		return &ownLoginError{fmt.Sprintf("%s shares the machine's Claude login, and account %s does not use it: the login would stay reachable for the run, and Claude Code can fall back to it on an error and send it to the account's address. Fix one of:\n"+
			"  - give the playbook a login of its own: cpb ALTER PLAYBOOK %s SET login = 'isolated'\n"+
			"  - run it on an account that uses the Claude login: cpb run --account <account> %s",
			target, account, target, target)}
	}
	return accountOwnLoginRefusal(configDir, target, account)
}

// accountOwnLoginRefusal is AccountLoginRefusal's macOS half, for an
// isolated playbook.
func accountOwnLoginRefusal(configDir, target, account string) error {
	// The machine's own directory keeps its login where it is: nothing sets
	// it aside, on any system, so a login there refuses before anything moves.
	// A store that is the machine's credentials file (the target of the
	// machine's own link, or a link on that chain) counts too; the ordinary
	// outgoing link to it is the playbook's, which isolation detaches.
	store := StorePath(configDir)
	info, lerr := os.Lstat(store)
	isLink := lerr == nil && info.Mode()&os.ModeSymlink != 0
	if IsGlobalConfigDir(configDir) || (!isLink && isGlobalCredentialsFile(store)) || (isLink && inGlobalCredentialsChain(store)) {
		where := ownLoginWhere(configDir)
		if where == "" && isLink {
			where = "its " + CredentialsFileName + " links to a login"
		}
		if where != "" {
			return &ownLoginError{fmt.Sprintf("%s is the machine's own Claude directory and holds its login (%s), which account %s does not use and cpb never moves. Run a playbook with a login of its own on that account instead (cpb ALTER PLAYBOOK <playbook> SET login = 'isolated')", target, where, account)}
		}
	}
	if hostOS != "darwin" {
		return nil
	}
	where := ownLoginWhere(configDir)
	if where == "" {
		return nil
	}
	return &ownLoginError{fmt.Sprintf("%s holds a login of its own (%s), and account %s does not use it. On macOS cpb cannot keep that login aside for the run, and Claude Code could fall back to it on an error. Fix one of:\n"+
		"  - run it on an account that uses its login (auth = 'claude_login'): cpb run --account <account> %s\n"+
		"  - /logout in it once, so it holds no login of its own",
		target, where, account, target)}
}

// loginLeftReachable refuses a launch on an account that uses no Claude
// login when the playbook's own login could not be set aside (setAsideErr),
// or is still reachable through its store afterwards: a store that cannot be
// read, or one that still holds a grant (a link to another login, the
// machine's own directory). Elsewhere than macOS that is the only guard.
func loginLeftReachable(configDir, account string, setAsideErr error) error {
	fix := "Fix one of:\n" +
		"  - run it on an account that uses its login (auth = 'claude_login'): cpb run --account <account> <playbook>\n" +
		"  - /logout in it once, so it holds no login of its own"
	if setAsideErr != nil {
		return &ownLoginError{fmt.Sprintf("%s holds a login of its own, and account %s does not use it; cpb could not set it aside for the run (%v), and Claude Code could fall back to it on an error. %s", configDir, account, setAsideErr, fix)}
	}
	store := StorePath(configDir)
	absent, err := storeGrantAbsent(store)
	if err != nil {
		return &ownLoginError{fmt.Sprintf("%s cannot be read (%v): it may hold a login, and account %s does not use one. %s", store, err, account, fix)}
	}
	if !absent {
		return &ownLoginError{fmt.Sprintf("%s still holds a login after it was set aside (a link to another login, or the machine's own directory), and account %s does not use it. %s", store, account, fix)}
	}
	return nil
}

// SandboxAccountLoginRefusal refuses a sandboxed launch on an account that
// uses no Claude login (account) when the playbook holds a login of its own
// in any form: its store with a grant, a login set aside or any other copy
// beside the store, or one in the Keychain. The playbook's directory is
// mounted into the sandbox, so a login set aside there, or a link that
// resolves inside, would stay within reach of the run. target names the
// playbook for the message.
func SandboxAccountLoginRefusal(configDir, target, account string) error {
	where := ownLoginWhere(configDir)
	if where == "" {
		// Copies of a login beside the store (set aside, kept, ignored at
		// install) are on the mount too. A link is judged after isolation
		// has detached the shared one (cmd/sandbox.go).
		copies, _ := filepath.Glob(StorePath(configDir) + ".*")
		var names []string
		for _, c := range copies {
			// cpb's own lock and pid files hold no login: matched by their
			// exact names, so any other copy is read.
			switch filepath.Base(c) {
			case CredentialsFileName + runLockSuffix, CredentialsFileName + asideLockSuffix, CredentialsFileName + runPidsSuffix:
				continue
			}
			// An empty file holds no grant; one that cannot be read may.
			if absent, err := storeGrantAbsent(c); err != nil || !absent {
				names = append(names, filepath.Base(c))
			}
		}
		if len(names) > 0 {
			where = "copies of a login beside its store: " + strings.Join(names, ", ") + "; move them out of its directory"
		}
	}
	if where == "" {
		return nil
	}
	return &ownLoginError{fmt.Sprintf("%s holds a login of its own (%s), and account %s does not use it. Its directory is mounted into the sandbox, so the login would stay within reach of the run. Fix one of:\n"+
		"  - /logout in it once, so it holds no login of its own\n"+
		"  - run it on an account that uses its login (auth = 'claude_login'): cpb run --account <account> <playbook>\n"+
		"  - run it unsandboxed (on Linux cpb sets the login aside for the run)",
		target, where, account)}
}

// ownLoginRefusal is OwnLoginRefusal for a launch already known to carry a
// token of the playbook's own.
func ownLoginRefusal(configDir, name string) error {
	if hostOS != "darwin" {
		return nil
	}
	where := ownLoginWhere(configDir)
	if where == "" {
		return nil
	}
	target := "<playbook>"
	if name != "" {
		target = name
	}
	return &ownLoginError{fmt.Sprintf("%s holds a login of its own (%s), and this launch would carry a token. On macOS cpb cannot keep that login aside for the run, and Claude Code would swap it in over the token on an error. Fix one of:\n"+
		"  - this launch on its own login: cpb run --account <account> %s, an account with auth = 'claude_login' (or machine) that it lists\n"+
		"  - always on the token: /logout in it once, so it holds no login of its own",
		target, where, target)}
}

// ownLoginWhere names where an isolated playbook keeps a login of its own:
// its store file, the Keychain, or "" for none. A store or Keychain that
// cannot be read counts as holding one.
func ownLoginWhere(configDir string) string {
	store := StorePath(configDir)
	if info, err := os.Lstat(store); err == nil && info.Mode().IsRegular() {
		if absent, err := storeGrantAbsent(store); err != nil || !absent {
			return "in its " + CredentialsFileName
		}
	} else if err != nil && !os.IsNotExist(err) {
		return "possibly, in a " + CredentialsFileName + " that cannot be read"
	}
	if _, err := os.Lstat(store + asideSuffix); err == nil {
		return "set aside in " + CredentialsFileName + asideSuffix
	}
	switch keychainLogin(configDir, false, false) {
	case KeychainPresent:
		return "in the Keychain"
	case KeychainUnknown:
		return "possibly, in a Keychain that cannot be read"
	}
	return ""
}

// ownToken is the token an isolated playbook supplies itself: the one its
// own block, the env sets it uses, or a launch flag sets. DEFAULTS never
// counts: it is the machine's layer, and an isolated login has no machine
// token. The value is "" when none is set, or it is set empty or blocked;
// ref reports a token supplied by reference instead, which the secret
// helper sets at exec.
func ownToken(configDir string, layers []*manifest.Env) (tok string, ref bool, err error) {
	dir := envset.Dir(config.ResolvePlaybooksDir())
	var block *manifest.Env
	if m, _ := manifest.Nearest(configDir); m != nil {
		block = m.Env
	}
	own, err := envset.Expand(dir, block)
	if err != nil {
		return "", false, err
	}
	flat := []*manifest.Env{own}
	for _, layer := range layers {
		expanded, err := envset.Expand(dir, layer)
		if err != nil {
			return "", false, err
		}
		flat = append(flat, expanded)
	}
	menv := manifest.MergeEnv(flat...)
	if menv.Blocks(OAuthTokenEnv) {
		return "", false, nil
	}
	return manifestToken(menv), tokenRef(menv), nil
}

// keepStoreGrant runs before the token path's quarantine strips a shared
// playbook's stored grant. A store that is a regular file holding a grant is
// that grant's only copy unless it is the machine's own account: then a
// newer copy goes over the machine store first, as the credential sync does,
// and the strip loses nothing. Any other login, or one that cannot be
// confirmed as the machine's, or one with no machine grant to fall back on,
// is copied aside first (the quarantine then strips only the grant, and the
// store's other logins stay). A link, an unreadable store and the machine's
// own store are left to the quarantine, which keeps them.
func keepStoreGrant(configDir string) error {
	if isGlobalConfigDir(configDir) {
		return nil
	}
	store := StorePath(configDir)
	info, err := os.Lstat(store)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || isGlobalCredentialsFile(store) {
		return nil
	}
	if usable, err := credentialsFileUsable(store); err != nil || !usable || !storeHasOAuthGrant(store) {
		return nil
	}
	global, err := globalClaudeDir()
	if err != nil {
		return err
	}
	globalStore := StorePath(global)
	globalInfo, err := os.Stat(globalStore)
	// The machine store is the recovery copy only when it holds a grant of
	// its own: one holding nothing but MCP logins would leave the stripped
	// grant nowhere.
	globalAbsent, gErr := storeGrantAbsent(globalStore)
	switch {
	case err == nil && gErr == nil && !globalAbsent && sameAccount(configDir):
		if info.ModTime().After(globalInfo.ModTime()) {
			if err := copyFile(store, globalStore, 0600); err != nil {
				return fmt.Errorf("failed to copy newer credentials to global: %w", err)
			}
		}
		return nil
	case err != nil && !os.IsNotExist(err):
		return err
	}
	return setAsideOwnLogin(configDir, "launched on the token", true)
}

// tokenRun is one token run this process holds: its shared run lock, and
// the session it started, once started.
type tokenRun struct {
	lock *os.File
	pid  int
}

// tokenRuns are the token runs this process holds, by config directory.
var (
	tokenRunsMu sync.Mutex
	tokenRuns   = map[string]*tokenRun{}
)

// setAsideForTokenRun moves an isolated playbook's own grant out of its
// store for a token run, and registers the run: the grant comes back when
// the last one ends (EndLaunch). Other keys in the store stay. When a grant
// is already aside and a newer one has appeared since (a /login meanwhile),
// both are kept, the earlier under a dated name. If the grant cannot be
// moved aside it stays in place: losing it would cost a /login.
func setAsideForTokenRun(configDir string) error {
	store := StorePath(configDir)
	unlock, err := lockFile(store+asideLockSuffix, syscall.LOCK_EX)
	if err != nil {
		return fmt.Errorf("cannot set the login in %s aside for the token run: %w", configDir, err)
	}
	defer unlock()
	if err := holdTokenRun(configDir); err != nil {
		return fmt.Errorf("cannot set the login in %s aside for the token run: %w", configDir, err)
	}
	info, err := os.Lstat(store)
	if os.IsNotExist(err) || (err == nil && !info.Mode().IsRegular()) {
		return nil
	}
	if err != nil {
		return err
	}
	grant, err := readGrant(store)
	if err != nil || grant == nil {
		return err
	}
	aside := store + asideSuffix
	if _, err := os.Lstat(aside); err == nil {
		kept, err := archiveAside(aside)
		if err != nil {
			return fmt.Errorf("cannot set the login in %s aside for the token run: %w", configDir, err)
		}
		Notice(fmt.Sprintf("%s was logged in again while an earlier login was set aside; kept the earlier one as %s", configDir, filepath.Base(kept)))
	}
	data, err := json.Marshal(map[string]json.RawMessage{oauthCredentialKey: grant})
	if err != nil {
		return err
	}
	if err := writeFilePrivate(aside, append(data, '\n')); err != nil {
		return fmt.Errorf("cannot set the login in %s aside for the token run: %w", configDir, err)
	}
	return QuarantineStoredOAuth(configDir)
}

// EndLaunch ends this process's token run of configDir, if it holds one,
// and puts the playbook's own login back when no other run holds it aside.
// Called when a launch returns, whatever its outcome; a failure is a
// warning.
func EndLaunch(configDir string) {
	tokenRunsMu.Lock()
	if run, ok := tokenRuns[configDir]; ok {
		if run.pid != 0 {
			store := StorePath(configDir)
			if unlock, err := lockFile(store+asideLockSuffix, syscall.LOCK_EX); err == nil {
				editPids(store+runPidsSuffix, func(pids []int) []int {
					out := pids[:0]
					for _, p := range pids {
						if p != run.pid {
							out = append(out, p)
						}
					}
					return out
				})
				unlock()
			}
		}
		run.lock.Close() // releases the shared lock
		delete(tokenRuns, configDir)
	}
	tokenRunsMu.Unlock()
	if err := restoreOwnLogin(configDir); err != nil {
		Notice(err.Error())
	}
}

// restoreOwnLogin puts a grant set aside back into the store, unless a
// token run of the playbook is still live (it holds the run lock shared, and
// the kernel drops the lock when that process dies, killed or not). A store
// that holds a grant again keeps it, and the one aside is kept under a dated
// name. A store that is a link (the playbook shares the machine's login now)
// is never written through.
func restoreOwnLogin(configDir string) error {
	store := StorePath(configDir)
	aside := store + asideSuffix
	if _, err := os.Lstat(aside); os.IsNotExist(err) {
		return nil
	}
	unlock, err := lockFile(store+asideLockSuffix, syscall.LOCK_EX)
	if err != nil {
		return fmt.Errorf("cannot put the login set aside in %s back: %w", configDir, err)
	}
	defer unlock()
	if live, err := tokenRunLive(store + runLockSuffix); err != nil || live {
		return err
	}
	// A session whose cpb was killed alone is still using its token.
	sessionLive := false
	if err := editPids(store+runPidsSuffix, func(pids []int) []int {
		sessionLive = len(pids) > 0
		return pids
	}); err != nil {
		return err
	}
	if sessionLive {
		return nil
	}
	grant, err := readGrant(aside)
	if err != nil {
		return fmt.Errorf("cannot put the login set aside in %s back: %w", configDir, err)
	}
	if grant == nil {
		// Gone already (another run restored it while this one waited for
		// the lock), or empty: nothing to put back.
		if err := os.Remove(aside); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	storeMap := map[string]json.RawMessage{}
	info, err := os.Lstat(store)
	switch {
	case err == nil && !info.Mode().IsRegular():
		return nil
	case err == nil:
		existing, err := readGrant(store)
		if err != nil {
			return fmt.Errorf("cannot put the login set aside in %s back: %w", configDir, err)
		}
		if existing != nil {
			kept, err := archiveAside(aside)
			if err != nil {
				return err
			}
			Notice(fmt.Sprintf("%s was logged in again during a token run; kept the earlier login as %s", configDir, filepath.Base(kept)))
			return nil
		}
		if data, err := os.ReadFile(store); err == nil && len(bytes.TrimSpace(data)) > 0 {
			if err := json.Unmarshal(bytes.TrimSpace(data), &storeMap); err != nil {
				return fmt.Errorf("cannot put the login set aside in %s back: %s is not valid JSON", configDir, CredentialsFileName)
			}
		}
	case !os.IsNotExist(err):
		return err
	}
	storeMap[oauthCredentialKey] = grant
	out, err := json.MarshalIndent(storeMap, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFilePrivate(store, append(out, '\n')); err != nil {
		return fmt.Errorf("cannot put the login set aside in %s back: %w", configDir, err)
	}
	return os.Remove(aside)
}

// archiveAside moves the grant at aside to a dated name that no other file
// holds: a hard link refuses an existing name, so an earlier archive is
// never replaced, then the original goes.
func archiveAside(aside string) (string, error) {
	base := aside + "-" + now().Format("2006-01-02-15_04_05")
	for i := 0; i < 1000; i++ {
		kept := base
		if i > 0 {
			kept = fmt.Sprintf("%s-%d", base, i)
		}
		err := os.Link(aside, kept)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		return kept, os.Remove(aside)
	}
	return "", fmt.Errorf("no free name for %s", base)
}

// loginHeldAside refuses a launch that uses the playbook's own login while
// a live run keeps that login aside (a token run, or one on an account that
// uses no Claude login): a /login or a refresh in the new session would put
// a login back in the store under the live run.
func loginHeldAside(configDir string) error {
	if !loginAside(configDir) {
		return nil
	}
	store := StorePath(configDir)
	live, err := tokenRunLive(store + runLockSuffix)
	if err == nil && !live {
		err = editPids(store+runPidsSuffix, func(pids []int) []int {
			live = len(pids) > 0
			return pids
		})
	}
	if err != nil {
		return &ownLoginError{fmt.Sprintf("cannot tell whether a run of %s keeps its login aside (%v): a launch that uses that login waits until it can", configDir, err)}
	}
	if live {
		return &ownLoginError{fmt.Sprintf("a run of %s that keeps its login aside (on a token, or on an account that uses no Claude login) is live: a launch that uses the login waits until that run ends", configDir)}
	}
	return nil
}

// StoreHoldsLogin reports whether configDir's store holds a login (or
// cannot be read, and may).
func StoreHoldsLogin(configDir string) bool {
	absent, err := storeGrantAbsent(StorePath(configDir))
	return err != nil || !absent
}

// loginAside reports whether configDir has a grant set aside by a token run.
func loginAside(configDir string) bool {
	_, err := os.Lstat(StorePath(configDir) + asideSuffix)
	return err == nil
}

// holdTokenRun takes the run lock shared and keeps it until EndLaunch.
func holdTokenRun(configDir string) error {
	tokenRunsMu.Lock()
	defer tokenRunsMu.Unlock()
	if _, ok := tokenRuns[configDir]; ok {
		return nil
	}
	f, err := os.OpenFile(StorePath(configDir)+runLockSuffix, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH); err != nil {
		f.Close()
		return err
	}
	tokenRuns[configDir] = &tokenRun{lock: f}
	return nil
}

// SessionStarted records the session a launch started (its pid) with every
// token run this process holds, so the run stays live for as long as the
// session does, even if this process is killed first. A no-op without one.
// A failure is a warning: the run lock still covers this process.
func SessionStarted(pid int) {
	tokenRunsMu.Lock()
	defer tokenRunsMu.Unlock()
	for configDir, run := range tokenRuns {
		store := StorePath(configDir)
		unlock, err := lockFile(store+asideLockSuffix, syscall.LOCK_EX)
		if err != nil {
			Notice(fmt.Sprintf("cannot record the session for the token run in %s: %v", configDir, err))
			continue
		}
		err = editPids(store+runPidsSuffix, func(pids []int) []int { return append(pids, pid) })
		unlock()
		if err != nil {
			Notice(fmt.Sprintf("cannot record the session for the token run in %s: %v", configDir, err))
			continue
		}
		run.pid = pid
	}
}

// editPids rewrites the pid list at path through edit, dropping pids no
// longer alive; an empty list removes the file. Callers hold the aside
// lock.
func editPids(path string, edit func([]int) []int) error {
	var pids []int
	if data, err := os.ReadFile(path); err == nil {
		for _, f := range strings.Fields(string(data)) {
			if pid, err := strconv.Atoi(f); err == nil && pidAlive(pid) {
				pids = append(pids, pid)
			}
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	pids = edit(pids)
	if len(pids) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	var b strings.Builder
	for _, pid := range pids {
		b.WriteString(strconv.Itoa(pid) + "\n")
	}
	return writeFilePrivate(path, []byte(b.String()))
}

// pidAlive reports whether a process with pid exists (EPERM: it does, as
// another user's).
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// tokenRunLive reports whether any process holds the run lock at path.
func tokenRunLive(path string) (bool, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0600)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return true, nil
		}
		return false, err
	}
	return false, nil
}

// lockFile takes a flock on path, creating it, and returns the release.
func lockFile(path string, how int) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), how); err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil
}

// readGrant returns the account grant in the store at path, nil when it
// holds none (missing, empty, no grant, or a grant with no access token).
func readGrant(path string) (json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if absent, err := storeGrantAbsent(path); err != nil || absent {
		return nil, err
	}
	var store map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimSpace(data), &store); err != nil {
		return nil, err
	}
	return store[oauthCredentialKey], nil
}
