package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ramazanpolat/claude-playbooks/internal/account"
	"github.com/ramazanpolat/claude-playbooks/internal/config"
)

// Launch records (SPEC.md, "SHOW SESSIONS" and "Files and state"): which
// account a session runs on. Claude Code's session file does not say, and
// cpb reads no process's environment, so a host launch writes it down
// itself: <root>/.state/launches/<pid>.json, for the process it started,
// written once that process runs and removed when the launch returns.
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
	// Identity is the account's identity label, null when it has none. The
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
	r := &launchRecord{V: launchRecordVersion, Playbook: label, ConfigDir: configDir, Account: sel.Name, Why: sel.Why}
	if r.Account == "" {
		r.Account = account.Machine
	}
	if sel.Account != nil && strings.TrimSpace(sel.Account.Identity) != "" {
		id := sel.Account.Identity
		r.Identity = &id
	}
	return r
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
	r.PID = pid
	r.ProcStart = procStartOf(pid)
	r.LaunchedAt = rfc3339(launchRecordNow())
	path := filepath.Join(dir, strconv.Itoa(pid)+".json")
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
		pid, ok := launchRecordPID(name)
		if ok && !pidAlive(pid) {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}

// launchRecordPID is the pid a record's file name holds.
func launchRecordPID(name string) (int, bool) {
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

// readLaunchRecords reads every record under root, by pid. It only reads,
// and defensively: a file that does not decode, is of another version, is
// too large, or names another pid than its file is skipped.
func readLaunchRecords(root string) map[int]launchRecord {
	out := map[int]launchRecord{}
	dir := launchRecordsDir(root)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range ents {
		pid, ok := launchRecordPID(e.Name())
		if !ok || !e.Type().IsRegular() {
			continue
		}
		fi, err := e.Info()
		if err != nil || fi.Size() > maxLaunchRecordBytes {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var r launchRecord
		if json.Unmarshal(b, &r) != nil || r.V != launchRecordVersion || r.PID != pid || r.ProcStart == "" || !validRecordedAccount(r.Account) {
			continue
		}
		out[pid] = r
	}
	return out
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
