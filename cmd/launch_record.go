package cmd

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ramazanpolat/claude-playbooks/internal/account"
	"github.com/ramazanpolat/claude-playbooks/internal/auth"
	"github.com/ramazanpolat/claude-playbooks/internal/config"
)

// Launch records (SPEC.md, "SHOW SESSIONS" and "Files and state"): which
// account a session runs on. Claude Code's session file does not say, and
// cpb reads no process's environment, so a host launch writes it down
// itself: <root>/.state/launches/<launch id>.json, for the process it
// started, written once that process runs and removed when the launch
// returns. The launch id is random and made before claude starts, so the
// status line step (statusline_step.go) can be told it on its command line
// and read the rest here: no name, identity or label travels through argv
// or the session's environment.
// SHOW SESSIONS matches a session to the record of its own pid, or of its
// parent's (a secret helper that starts claude as its child), and only when
// that process started when the record says, so a reused pid never borrows
// another launch's account.

// launchRecordVersion is the record's shape. A record of another version is
// ignored, so a later shape never reads as this one.
const launchRecordVersion = 1

// launchRecord is one launch's record.
type launchRecord struct {
	V int `json:"v"`
	// LaunchID names the record's file.
	LaunchID string `json:"launch_id,omitempty"`
	// PID and ProcStart identify the process the launch started (claude,
	// or the secret helper that starts it): ProcStart is its start time in
	// the form procStarts reads on this platform.
	PID       int    `json:"pid"`
	ProcStart string `json:"proc_start"`
	// Playbook is the playbook's name, or the directory for cpb start.
	Playbook  string `json:"playbook"`
	ConfigDir string `json:"config_dir"`
	// Account is the account the launch runs on (machine for the implicit
	// one, env:<set> and the like for a route a layer states), Why the
	// reason EXPLAIN gives for it.
	Account string `json:"account"`
	Why     string `json:"why"`
	// Key is whose allowance the session draws on (usageKey): the
	// account, or for a Claude login, the login it reaches.
	Key string `json:"key,omitempty"`
	// Identity is the account's identity label, or for a Claude login the
	// e-mail its config directory records; null when neither is known. The
	// file is the user's own state, mode 0600; it is never printed.
	Identity   *string `json:"identity"`
	LaunchedAt string  `json:"launched_at"`
	// Switches are the account switches that led to this launch (account
	// failover), oldest first.
	Switches []*failoverSwitch `json:"switches,omitempty"`
}

// launchRecordsDir is where launch records live.
func launchRecordsDir(root string) string {
	return filepath.Join(root, ".state", "launches")
}

// newLaunchRecord is the record of a launch of configDir (shown as label)
// on the selected account; its process is filled in once it starts.
func newLaunchRecord(label, configDir string, sel account.Selection) *launchRecord {
	r := &launchRecord{V: launchRecordVersion, LaunchID: newLaunchID(), Playbook: label, ConfigDir: configDir, Account: sel.Name, Why: sel.Why}
	if r.Account == "" {
		r.Account = account.Machine
	}
	r.Key = usageKey(sel, configDir, label)
	if sel.Account != nil && strings.TrimSpace(sel.Account.Identity) != "" {
		id := sel.Account.Identity
		r.Identity = &id
	} else if usesClaudeLogin(sel) {
		r.Identity = loginIdentity(configDir)
	}
	return r
}

// launchIDShape is a launch id: 16 lower-case hex digits.
var launchIDShape = regexp.MustCompile(`^[0-9a-f]{16}$`)

// newLaunchID is a random launch id.
func newLaunchID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// usesClaudeLogin reports a launch that runs on a stored Claude login, the
// machine's or the playbook's own: machine, or an account with auth =
// 'claude_login'.
func usesClaudeLogin(sel account.Selection) bool {
	return sel.Account == nil || sel.Account.Auth.Kind == account.ClaudeLogin
}

// usageKey is whose allowance a launch draws on. An account with a
// credential of its own is its own key. A Claude login is the login's: the
// machine's for a shared playbook (machine), the playbook's own for an
// isolated one (playbook:<name>), whichever account name reached it.
func usageKey(sel account.Selection, configDir, label string) string {
	if !usesClaudeLogin(sel) {
		if sel.Name == "" {
			return account.Machine
		}
		return sel.Name
	}
	if auth.IsAuthIsolated(configDir) {
		return blockName("playbook:" + filepath.Base(label))
	}
	return account.Machine
}

// loginIdentity is the e-mail Claude Code records for the login of
// configDir (.claude.json's oauthAccount.emailAddress; cpb keeps a shared
// playbook's in step with the machine's), nil when there is none. That one
// field is read: the file holds no credential.
func loginIdentity(configDir string) *string {
	f, err := os.Open(filepath.Join(configDir, auth.StateFileName))
	if err != nil {
		return nil
	}
	defer f.Close()
	var state struct {
		OAuthAccount struct {
			Email string `json:"emailAddress"`
		} `json:"oauthAccount"`
	}
	if json.NewDecoder(io.LimitReader(f, 32<<20)).Decode(&state) != nil {
		return nil
	}
	e := strings.TrimSpace(state.OAuthAccount.Email)
	if e == "" || !validRecordedAccount(e) {
		return nil
	}
	return &e
}

// procStartOf is pid's start time in the form procStarts reads, "" when it
// cannot be read: one process, so a launch never lists every process on
// the machine. A variable so tests can fake processes.
var procStartOf = func(pid int) string {
	if runtime.GOOS == "linux" {
		return procStatStarts([]int{pid})[pid].start
	}
	c := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid))
	c.Env = append(os.Environ(), "TZ=UTC", "LC_ALL=C")
	b, err := c.Output()
	if err != nil {
		return ""
	}
	return strings.Join(strings.Fields(string(b)), " ")
}

// launchRecordNow is the clock a record's launched_at reads; a variable for
// tests.
var launchRecordNow = time.Now

// record writes r for the process pid the launch started, and returns what
// removes it when the launch returns. A record that cannot be written costs
// the launch nothing but a warning: the session runs, and SHOW SESSIONS
// shows no account for it. Records of processes no longer alive are swept
// first, since a launch killed outright cannot remove its own.
func (r *launchRecord) record(root string, pid int) func() {
	dir := launchRecordsDir(root)
	sweepLaunchRecords(dir)
	sweepUsage(root, launchRecordNow())
	r.PID = pid
	r.ProcStart = procStartOf(pid)
	r.LaunchedAt = rfc3339(launchRecordNow())
	name := r.LaunchID
	if name == "" {
		name = strconv.Itoa(pid)
	}
	path := filepath.Join(dir, name+".json")
	if err := writeLaunchRecord(dir, path, r); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: cannot record this launch's account (SHOW SESSIONS will not show it): %v\n", err)
		return func() {}
	}
	return func() { _ = os.Remove(path) }
}

// writeLaunchRecord writes r to path atomically: a temporary file in the
// same directory, renamed over it, so a reader never sees half a record.
func writeLaunchRecord(dir, path string, r *launchRecord) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".launch-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.Write(append(b, '\n'))
	cerr := f.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmp, 0o600)
	}
	if werr == nil {
		werr = os.Rename(tmp, path)
	}
	if werr != nil {
		_ = os.Remove(tmp)
	}
	return werr
}

// sweepLaunchRecords removes the records of processes no longer alive, and
// temporary files a killed writer left behind. A record of a live process
// that is not its launch's (a reused pid) stays until that process ends;
// readers ignore it, since its start time differs.
func sweepLaunchRecords(dir string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		name := e.Name()
		if strings.HasPrefix(name, ".launch-") {
			if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > time.Minute {
				_ = os.Remove(filepath.Join(dir, name))
			}
			continue
		}
		if id, ok := strings.CutSuffix(name, ".json"); ok && launchIDShape.MatchString(id) {
			if r, ok := readLaunchRecordFile(filepath.Join(dir, name)); !ok || !pidAlive(r.PID) {
				_ = os.Remove(filepath.Join(dir, name))
			}
		}
	}
}

// pidFileName is the pid a <pid>.json file name holds (Claude Code's session files).
func pidFileName(name string) (int, bool) {
	s, ok := strings.CutSuffix(name, ".json")
	if !ok {
		return 0, false
	}
	pid, err := strconv.Atoi(s)
	if err != nil || pid <= 0 || strconv.Itoa(pid) != s {
		return 0, false
	}
	return pid, true
}

// maxLaunchRecordBytes bounds what a reader takes from one record.
const maxLaunchRecordBytes = 16 << 10

// readLaunchRecordFile reads one record, defensively: a file that is not a
// regular file, is too large, does not decode, is of another version,
// names no process, or holds an account or key no launch writes is no
// record.
func readLaunchRecordFile(path string) (launchRecord, bool) {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxLaunchRecordBytes {
		return launchRecord{}, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return launchRecord{}, false
	}
	var r launchRecord
	if json.Unmarshal(b, &r) != nil || r.V != launchRecordVersion || r.PID <= 0 || r.ProcStart == "" || !validRecordedAccount(r.Account) ||
		(r.Key != "" && !validRecordedAccount(r.Key)) || (r.Identity != nil && !validRecordedAccount(*r.Identity)) {
		return launchRecord{}, false
	}
	return r, true
}

// readLaunchRecordByID is the record a launch id names.
func readLaunchRecordByID(root, id string) (launchRecord, bool) {
	if !launchIDShape.MatchString(id) {
		return launchRecord{}, false
	}
	r, ok := readLaunchRecordFile(filepath.Join(launchRecordsDir(root), id+".json"))
	if !ok || r.LaunchID != id {
		return launchRecord{}, false
	}
	return r, true
}

// readLaunchRecords reads every record under root, by the pid it names.
// A file named for a pid (a release before launch ids) must name that pid;
// one named for a launch id must hold that id. Two records may name one
// pid (one left by a launch killed outright, the pid since reused): the
// reader picks by start time.
func readLaunchRecords(root string) map[int][]launchRecord {
	out := map[int][]launchRecord{}
	dir := launchRecordsDir(root)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range ents {
		name := e.Name()
		r, ok := readLaunchRecordFile(filepath.Join(dir, name))
		if !ok {
			continue
		}
		if id, _ := strings.CutSuffix(name, ".json"); !launchIDShape.MatchString(id) || r.LaunchID != id {
			continue
		}
		out[r.PID] = append(out[r.PID], r)
	}
	return out
}

// recordStarted is the record among recs of the process that started at
// start, if any.
func recordStarted(recs []launchRecord, start string) (launchRecord, bool) {
	for _, r := range recs {
		if sameStart(r.ProcStart, start) {
			return r, true
		}
	}
	return launchRecord{}, false
}

// validRecordedAccount accepts what a launch records as its account: a
// stored account's name, machine, or a layer route's colon form (env:<set>,
// block:<directory>, flags:<host>, whose host may be a bracketed IPv6
// address with a port). It refuses only what no launch writes and a
// hand-edited file could hold to reach a terminal: invalid UTF-8, a control
// character, or an implausible length.
func validRecordedAccount(name string) bool {
	if name == "" || len(name) > 512 || !utf8.ValidString(name) {
		return false
	}
	for _, c := range name {
		if unicode.IsControl(c) {
			return false
		}
	}
	return true
}

// sameStart reports whether two start times, as procStarts and the record
// write them, name the same moment.
func sameStart(a, b string) bool {
	a, b = strings.Join(strings.Fields(a), " "), strings.Join(strings.Fields(b), " ")
	return a != "" && a == b
}

// launchRecordsRoot is the root whose records a launch writes and SHOW
// SESSIONS reads: the playbooks root.
func launchRecordsRoot() string { return config.ResolvePlaybooksDir() }
