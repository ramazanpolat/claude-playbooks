package cmd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
	"github.com/ramazanpolat/claude-playbooks/internal/shell"
)

// Sessions (SPEC.md, "Sessions"): the live Claude
// Code sessions of cpb's config dirs, SHOW SESSIONS, the SESSIONS table,
// the resume guard of a launch, and the line printed when it ends.
//
// Everything here reads Claude Code's own files and nothing else:
// <configDir>/sessions/<pid>.json, which Claude Code keeps for each live
// process and removes on exit, and the transcripts under
// <configDir>/projects/. No other process's environment is read, so no
// token another process holds can reach cpb. cpb never writes, deletes or
// renames anything under sessions/: a stale file is Claude Code's business.

// sessionFile is Claude Code's <configDir>/sessions/<pid>.json, as observed
// on 2.1.282 and 2.1.283. It is undocumented, so it is read defensively: a
// file that does not decode, or has no pid or session id, is skipped.
type sessionFile struct {
	PID       int    `json:"pid"`
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	StartedAt int64  `json:"startedAt"` // epoch milliseconds
	ProcStart string `json:"procStart"` // the process's start time, UTC, ps lstart's format
	Kind      string `json:"kind"`      // "interactive" or "bg"
	Status    string `json:"status"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	PIDDomain string `json:"pidDomain"`
}

// sessionKinds are the kinds listed. Anything else (a daemon's spare
// worker, should one ever write a file) is not a session a pilot resumes.
var sessionKinds = map[string]bool{"interactive": true, "bg": true}

// sessionDir is one config dir whose sessions cpb lists: a playbook, or a
// plain directory from cpb's state.
type sessionDir struct {
	label    string // the playbook name, or the plain directory's path
	path     string // the config dir
	pb       *playbook.Playbook
	launcher string // the playbook's launcher, "" for none
	// account is the --account the launch named, "" for none: the
	// commands cpb suggests for the launch keep it.
	account string
}

// sessionJSON is one row of SHOW SESSIONS --json and of the SESSIONS table.
type sessionJSON struct {
	Playbook      string  `json:"playbook"`
	ConfigDir     string  `json:"config_dir"`
	PID           int     `json:"pid"`
	SessionID     string  `json:"session_id"`
	Cwd           string  `json:"cwd"`
	Kind          string  `json:"kind"`
	Status        *string `json:"status"`
	Name          *string `json:"name"`
	ClaudeVersion *string `json:"claude_version"`
	StartedAt     string  `json:"started_at"`
	LastActive    *string `json:"last_active"`
	Model         *string `json:"model"`
	Launcher      *string `json:"launcher"`
	Resume        string  `json:"resume"`
	// TTY is the process's controlling terminal (v3.25.0), last so every
	// earlier field keeps its place.
	TTY *string `json:"tty"`
	// Account is the account cpb launched the session on, from its launch
	// record; null for a session cpb did not launch. AtLimit is the usage
	// limit the session is stopped at, from its transcript. Both follow tty,
	// so every earlier field keeps its place.
	Account *string       `json:"account"`
	AtLimit *sessionLimit `json:"at_limit"`
	// AccountAtLimit is the limit the session's account is at, from the
	// latest figure any of its sessions reported (SHOW USAGE), kept apart
	// from AtLimit: the session itself may not have tried since. Last.
	AccountAtLimit *sessionLimit `json:"account_at_limit"`
	// Labels are the session's launch labels (its launch record) and
	// session labels (cpb session label); null when it has neither. Last.
	Labels *blockLabelsJSON `json:"labels"`
	// key is whose allowance the session draws on (its launch record's
	// usage key), for SHOW USAGE; not printed.
	key string
}

// sessionLimit is the usage limit a session is stopped at: the window
// (five_hour, seven_day, or another Claude Code names) and when it resets.
type sessionLimit struct {
	Window   string `json:"window"`
	ResetsAt string `json:"resets_at"`
}

// liveState is what cpb can tell about a session file's process.
type liveState int

const (
	notLive liveState = iota
	live
	liveUnknown // another pid domain (a sandbox, another host): cpb cannot tell
)

// procInfo is what cpb reads about a live process: its start time, in the
// form Claude Code writes as procStart on this platform, its controlling
// terminal ("" for none), and its parent's pid, which matches a session to
// the launch record of a secret helper that started it (launch_record.go).
type procInfo struct {
	start, tty string
	ppid       int
}

// procStarts returns procInfo for each pid that is alive: on Linux from
// /proc/<pid>/stat (starttime, clock ticks since boot, observed as
// procStart on 2.1.233; tty_nr), elsewhere from ps's lstart read in UTC with
// the C locale ("Mon Sep 28 07:29:55 2026", observed on 2.1.282 and 2.1.283
// on macOS) and its tty. A pid missing from the map is not alive. Nothing
// else about a process is read. It is a variable so tests can fake
// processes.
var procStarts = func(pids []int) map[int]procInfo {
	if runtime.GOOS == "linux" {
		return procStatStarts(pids)
	}
	return psStarts(pids)
}

func procStatStarts(pids []int) map[int]procInfo {
	out := map[int]procInfo{}
	for _, pid := range pids {
		b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
		if err != nil {
			continue
		}
		// The command name (field 2) is in parentheses and may hold spaces;
		// the fields after it start at field 3, so tty_nr (7) is the 5th and
		// starttime (22) the 20th.
		i := bytes.LastIndexByte(b, ')')
		if i < 0 {
			continue
		}
		f := strings.Fields(string(b[i+1:]))
		if len(f) <= 19 {
			continue
		}
		// tty_nr is a signed %d: a pts minor of 0x80000 or more sets bit 31
		// and prints negative (Codex, #134). Its bits are the device number.
		n, _ := strconv.ParseInt(f[4], 10, 32)
		ppid, _ := strconv.Atoi(f[1])
		out[pid] = procInfo{start: f[19], tty: linuxTTY(uint64(uint32(int32(n)))), ppid: ppid}
	}
	return out
}

// linuxTTY names a tty_nr (a device number) as ps does, for the terminals
// a session runs in: a pseudo-terminal (pts/N), a virtual console (ttyN) or
// a serial line (ttySN). Anything else is "", as is no terminal.
func linuxTTY(nr uint64) string {
	major := (nr >> 8) & 0xfff
	minor := (nr & 0xff) | ((nr >> 12) & 0xfff00)
	switch {
	case nr == 0:
		return ""
	case major >= 136 && major <= 143:
		return "pts/" + strconv.FormatUint((major-136)*256+minor, 10)
	case major == 4 && minor < 64:
		return "tty" + strconv.FormatUint(minor, 10)
	case major == 4:
		return "ttyS" + strconv.FormatUint(minor-64, 10)
	}
	return ""
}

func psStarts(pids []int) map[int]procInfo {
	out := map[int]procInfo{}
	if len(pids) == 0 {
		return out
	}
	want := map[int]bool{}
	for _, p := range pids {
		want[p] = true
	}
	// Every process, filtered here: `ps -p` with one pid macOS's ps rejects
	// (out of range, from a malformed session file) prints nothing for the
	// others either.
	// tty last: lstart in the C locale is always five words, so the
	// columns cannot shift whatever the tty column holds.
	c := exec.Command("ps", "-A", "-o", "pid=,ppid=,lstart=,tty=")
	c.Env = append(os.Environ(), "TZ=UTC", "LC_ALL=C")
	b, _ := c.Output()
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 7 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil || !want[pid] {
			continue
		}
		ppid, _ := strconv.Atoi(f[1])
		tty := ""
		if len(f) > 7 && strings.Trim(f[7], "?-") != "" {
			tty = f[7]
		}
		out[pid] = procInfo{start: strings.Join(f[2:7], " "), tty: tty, ppid: ppid}
	}
	return out
}

// procWalker returns a reader of procInfo by pid for one walk up a
// process's ancestry (cpb session label): on Linux /proc, one small file
// per pid; elsewhere one ps table, read once on first use and without the
// tty column, which costs a lookup per process. A missing pid is not
// alive. It is a variable so tests can fake processes.
var procWalker = func() func(pid int) (procInfo, bool) {
	if runtime.GOOS == "linux" {
		return func(pid int) (procInfo, bool) {
			info, ok := procStatStarts([]int{pid})[pid]
			return info, ok
		}
	}
	var table map[int]procInfo
	return func(pid int) (procInfo, bool) {
		if table == nil {
			table = psTable()
		}
		info, ok := table[pid]
		return info, ok
	}
}

// psTable is every process's parent and start time, from one ps call, in
// the form psStarts reads (lstart in UTC with the C locale).
func psTable() map[int]procInfo {
	out := map[int]procInfo{}
	c := exec.Command("ps", "-A", "-o", "pid=,ppid=,lstart=")
	c.Env = append(os.Environ(), "TZ=UTC", "LC_ALL=C")
	b, _ := c.Output()
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) != 7 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		ppid, _ := strconv.Atoi(f[1])
		out[pid] = procInfo{start: strings.Join(f[2:7], " "), ppid: ppid}
	}
	return out
}

// pidDomain is this host's pid domain, as Claude Code writes it.
var pidDomain = runtime.GOOS

// sessionDirs is every config dir whose sessions are listed: the playbooks
// and the plain directories in cpb's state, and apart, the playbooks whose
// manifests cannot be read. forName limits it to one playbook (an unknown or
// unreadable one is refused).
func sessionDirs(forName string) ([]sessionDir, []playbook.Unreadable, error) {
	root := config.ResolvePlaybooksDir()
	if forName != "" {
		pb, err := playbook.Require(root, forName)
		if err != nil {
			return nil, nil, err
		}
		return []sessionDir{playbookSessionDir(pb)}, nil, nil
	}
	pbs, bad, err := playbook.Scan(root)
	if err != nil {
		return nil, nil, err
	}
	var dirs []sessionDir
	for _, pb := range pbs {
		dirs = append(dirs, playbookSessionDir(pb))
	}
	// A registry that cannot be read is an error, not an empty list: an
	// answer without its plain directories would pass for a complete one.
	st, err := readDirState()
	if err != nil {
		return nil, nil, err
	}
	var plain []string
	for d := range st.Dirs {
		plain = append(plain, d)
	}
	sort.Strings(plain)
	for _, d := range plain {
		dirs = append(dirs, sessionDir{label: d, path: d})
	}
	return dirs, bad, nil
}

func playbookSessionDir(pb *playbook.Playbook) sessionDir {
	return sessionDir{label: pb.Name, path: pb.Path, pb: pb, launcher: effectiveLauncher(pb)}
}

// pickCommand is the command that opens Claude Code's session picker in d:
// the launcher, cpb run, or, for a plain directory, claude under that
// config dir, with --resume and no id.
func (d sessionDir) pickCommand() string {
	// An account name needs no quoting (manifest.ValidateAccountName).
	acct := ""
	if d.account != "" {
		acct = "--account " + d.account + " "
	}
	switch {
	case d.launcher != "":
		return d.launcher + " " + acct + "--resume"
	case d.pb != nil:
		return "cpb run " + acct + d.pb.Name + " --resume"
	}
	return "CLAUDE_CONFIG_DIR=" + shell.QuoteArg(d.path) + " claude --resume"
}

// resumeCommand is the command that resumes id in d from the folder it ran
// in.
func (d sessionDir) resumeCommand(id string) string { return d.pickCommand() + " " + id }

// resumeIn is the command that resumes id in d from anywhere: a cd into
// cwd, the folder it ran in, first. Claude Code finds a session by it.
func (d sessionDir) resumeIn(id, cwd string) string {
	if cwd == "" {
		return d.resumeCommand(id)
	}
	return "cd " + shell.QuoteArg(cwd) + " && " + d.resumeCommand(id)
}

// liveSession is a session file with its dir and what cpb can tell about
// its process.
type liveSession struct {
	dir   sessionDir
	f     sessionFile
	state liveState
	tty   string // the process's controlling terminal, "" for none
	// account is the account of its launch record, "" for none; key its
	// usage key; labels its launch labels.
	account, key string
	labels       map[string]string
}

// readSessionFiles reads the session files of dirs. It only reads.
func readSessionFiles(dirs []sessionDir) []liveSession {
	var all []liveSession
	for _, d := range dirs {
		ents, err := os.ReadDir(filepath.Join(d.path, "sessions"))
		if err != nil {
			continue // no sessions/: no live session
		}
		for _, e := range ents {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			b, err := os.ReadFile(filepath.Join(d.path, "sessions", e.Name()))
			if err != nil {
				continue
			}
			var f sessionFile
			if json.Unmarshal(b, &f) != nil || f.PID <= 0 || !grammar.ValidSessionID(f.SessionID) || !sessionKinds[f.Kind] {
				continue
			}
			all = append(all, liveSession{dir: d, f: f})
		}
	}
	var pids []int
	for _, s := range all {
		if s.f.PIDDomain == "" || s.f.PIDDomain == pidDomain {
			pids = append(pids, s.f.PID)
		}
	}
	starts := procStarts(pids)
	var out []liveSession
	for _, s := range all {
		switch {
		case s.f.PIDDomain != "" && s.f.PIDDomain != pidDomain:
			s.state = liveUnknown
		default:
			info, ok := starts[s.f.PID]
			start := info.start
			s.tty = info.tty
			switch {
			case !ok:
				continue
			case s.f.ProcStart == "":
				// No start time to compare: the pid may be reused, so the
				// session is not known to be live, and not known to be over.
				s.state = liveUnknown
			case strings.Join(strings.Fields(s.f.ProcStart), " ") != start:
				// Alive, but started at another time: a reused pid, and the
				// session it recorded has ended.
				continue
			default:
				s.state = live
			}
		}
		out = append(out, s)
	}
	matchLaunchRecords(out, starts)
	sort.SliceStable(out, func(i, j int) bool { return out[i].f.StartedAt > out[j].f.StartedAt })
	return out
}

// matchLaunchRecords gives each confirmed live session the account of its
// launch record: the record of its own pid, or else of its parent's (a
// secret helper that started claude as its child), and only when that
// process started when the record says, so a reused pid never borrows
// another launch's account. A session whose file records no start time is
// not confirmed to be the process now on its pid (it may be a stale file on
// a reused pid), so it gets no account. starts is what procStarts read for
// the sessions' own pids; a parent's start is read only when a record names
// it.
func matchLaunchRecords(sessions []liveSession, starts map[int]procInfo) {
	if len(sessions) == 0 {
		return
	}
	recs := readLaunchRecords(launchRecordsRoot())
	if len(recs) == 0 {
		return
	}
	var parents []int
	for i := range sessions {
		s := &sessions[i]
		if s.state != live {
			continue
		}
		info, ok := starts[s.f.PID]
		if !ok {
			continue
		}
		if r, ok := recordStarted(recs[s.f.PID], info.start); ok {
			s.account, s.key, s.labels = r.Account, r.Key, r.Labels
			continue
		}
		if _, ok := recs[info.ppid]; ok && info.ppid > 1 {
			parents = append(parents, info.ppid)
		}
	}
	if len(parents) == 0 {
		return
	}
	pstarts := procStarts(parents)
	for i := range sessions {
		s := &sessions[i]
		if s.account != "" || s.state != live {
			continue
		}
		info, ok := starts[s.f.PID]
		if !ok || info.ppid <= 1 {
			continue
		}
		p, alive := pstarts[info.ppid]
		if !alive {
			continue
		}
		if r, ok := recordStarted(recs[info.ppid], p.start); ok {
			s.account, s.key, s.labels = r.Account, r.Key, r.Labels
		}
	}
}

// encodeProjectDir is Claude Code's directory name under projects/ for a
// working directory: every character outside [A-Za-z0-9] becomes '-'.
func encodeProjectDir(cwd string) string {
	// Claude Code replaces each UTF-16 code unit that is not an ASCII
	// letter or digit: one '-' per character, two for one outside the
	// Basic Multilingual Plane (checked on Claude Code 2.1.234).
	var b strings.Builder
	for _, r := range cwd {
		switch {
		case r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9':
			b.WriteRune(r)
		case r > 0xFFFF:
			b.WriteString("--")
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// transcriptPath finds id's transcript in configDir: under the encoded cwd
// first, then under any project directory, so a change in how Claude Code
// names that directory costs a glob, not a wrong answer.
func transcriptPath(configDir, cwd, id string) string {
	if cwd != "" {
		p := filepath.Join(configDir, "projects", encodeProjectDir(cwd), id+".jsonl")
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			return p
		}
	}
	m, _ := filepath.Glob(filepath.Join(configDir, "projects", "*", id+".jsonl"))
	for _, p := range m {
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			return p
		}
	}
	return ""
}

// transcriptTail is what cpb reads from a transcript's last 256 KiB: the
// model of its last assistant line, its last cwd, its last AI title, and
// the usage limit its last assistant line reports, if that line is one.
type transcriptTail struct {
	model, cwd, title string
	// cwds are the distinct cwd values in the tail, the last first.
	cwds  []string
	limit *quotaStop
	// context is the size of the context at the last answer: its input,
	// cache reads and cache writes, in tokens; 0 when unknown.
	context int64
}

// quotaStop is a usage limit a session ran into: the window Claude Code
// names and when it resets.
type quotaStop struct {
	window   string
	resetsAt time.Time
	// at is when Claude Code wrote the line (its timestamp); zero when the
	// line has none.
	at time.Time
}

// limitWindow is the shape of a window name cpb shows: Claude Code's own
// (five_hour, seven_day, and any it adds), never free text.
var limitWindow = regexp.MustCompile(`^[a-z0-9_]{1,40}$`)

// quotaStopOf reads the usage limit an assistant line reports. Claude Code
// (observed on 2.1.240 to 2.1.322; undocumented) writes a session that
// hit a limit as an assistant line with isApiErrorMessage, error
// "rate_limit" and quotaLimits {status "rejected", rateLimitType, resetsAt
// in epoch seconds}. Anything else, or any part of that shape missing or of
// another type, is no limit: an unknown shape is never shown, and never an
// error.
func quotaStopOf(isAPIError bool, errKind string, quota json.RawMessage) *quotaStop {
	if !isAPIError || errKind != "rate_limit" || len(quota) == 0 {
		return nil
	}
	var q struct {
		Status        *string  `json:"status"`
		RateLimitType *string  `json:"rateLimitType"`
		ResetsAt      *float64 `json:"resetsAt"`
	}
	if json.Unmarshal(quota, &q) != nil || q.Status == nil || q.RateLimitType == nil || q.ResetsAt == nil {
		return nil
	}
	r := *q.ResetsAt
	if *q.Status != "rejected" || !limitWindow.MatchString(*q.RateLimitType) || r <= 0 || r > 1e11 || r != math.Trunc(r) {
		return nil
	}
	return &quotaStop{window: *q.RateLimitType, resetsAt: time.Unix(int64(r), 0)}
}

const transcriptTailBytes = 256 << 10

func readTranscriptTail(path string) transcriptTail {
	var t transcriptTail
	f, err := os.Open(path)
	if err != nil {
		return t
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Size() > transcriptTailBytes {
		if _, err := f.Seek(-transcriptTailBytes, io.SeekEnd); err != nil {
			return t
		}
	}
	b, _ := io.ReadAll(f)
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64<<10), transcriptTailBytes+1)
	for sc.Scan() {
		// Every field raw, so a field of an unexpected type costs that field
		// only, never the line: an assistant line of any shape still ends a
		// limit before it.
		var line struct {
			Type       json.RawMessage `json:"type"`
			Cwd        json.RawMessage `json:"cwd"`
			AITitle    json.RawMessage `json:"aiTitle"`
			IsAPIError json.RawMessage `json:"isApiErrorMessage"`
			Timestamp  json.RawMessage `json:"timestamp"`
			Error      json.RawMessage `json:"error"`
			Quota      json.RawMessage `json:"quotaLimits"`
			Message    json.RawMessage `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &line) != nil {
			continue // the first line of a tail is usually cut
		}
		if c := rawString(line.Cwd); c != "" {
			t.cwd = c
			if len(t.cwds) == 0 || t.cwds[0] != c {
				kept := []string{c}
				for _, o := range t.cwds {
					if o != c {
						kept = append(kept, o)
					}
				}
				t.cwds = kept
			}
		}
		if a := rawString(line.AITitle); a != "" {
			t.title = a
		}
		if rawString(line.Type) == "assistant" {
			// Every assistant line decides: a limit, or (an answer, or any
			// other error, or a shape cpb does not know) no limit any more.
			var isErr bool
			apiErr := json.Unmarshal(line.IsAPIError, &isErr) == nil && isErr
			t.limit = quotaStopOf(apiErr, rawString(line.Error), line.Quota)
			if t.limit != nil {
				if at, err := time.Parse(time.RFC3339Nano, rawString(line.Timestamp)); err == nil {
					t.limit.at = at
				}
			}
			var msg struct {
				Model json.RawMessage `json:"model"`
				Usage json.RawMessage `json:"usage"`
			}
			if json.Unmarshal(line.Message, &msg) == nil {
				if m := rawString(msg.Model); m != "" && !strings.HasPrefix(m, "<") {
					t.model = m
				}
				var u struct {
					Input       int64 `json:"input_tokens"`
					CacheRead   int64 `json:"cache_read_input_tokens"`
					CacheCreate int64 `json:"cache_creation_input_tokens"`
				}
				if len(msg.Usage) > 0 && json.Unmarshal(msg.Usage, &u) == nil {
					if n := u.Input + u.CacheRead + u.CacheCreate; n > 0 {
						t.context = n
					}
				}
			}
		}
	}
	return t
}

// rawString is a raw JSON value as a string, "" when it is absent or not a
// string.
func rawString(raw json.RawMessage) string {
	var s string
	if len(raw) == 0 || json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

// transcriptCwd is the directory a transcript's session ran in: the last
// cwd in its tail, else the first in its head (the tail may be one record
// too large to hold). "" when neither has one.
func transcriptCwd(path string) string {
	if c := readTranscriptTail(path).cwd; c != "" {
		return c
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(io.LimitReader(f, transcriptTailBytes))
	sc.Buffer(make([]byte, 0, 64<<10), transcriptTailBytes+1)
	var line struct {
		Cwd string `json:"cwd"`
	}
	for sc.Scan() {
		if json.Unmarshal(sc.Bytes(), &line) == nil && line.Cwd != "" {
			return line.Cwd
		}
	}
	return ""
}

func rfc3339(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func (s liveSession) json() sessionJSON {
	v := sessionJSON{
		Playbook: s.dir.label, ConfigDir: s.dir.path, PID: s.f.PID, SessionID: s.f.SessionID,
		Cwd: s.f.Cwd, Kind: s.f.Kind, Status: optStr(s.f.Status), Name: optStr(s.f.Name),
		ClaudeVersion: optStr(s.f.Version), StartedAt: rfc3339(time.UnixMilli(s.f.StartedAt)),
		Launcher: optStr(s.dir.launcher), Resume: s.dir.resumeIn(s.f.SessionID, s.f.Cwd),
		TTY: optStr(s.tty), Account: optStr(s.account), key: s.key,
	}
	if p := transcriptPath(s.dir.path, s.f.Cwd, s.f.SessionID); p != "" {
		if fi, err := os.Stat(p); err == nil {
			v.LastActive = strPtr(rfc3339(fi.ModTime()))
		}
		tail := readTranscriptTail(p)
		v.Model = optStr(tail.model)
		// A limit that has reset no longer stops the session: its next
		// message goes through.
		if l := tail.limit; l != nil && l.resetsAt.After(sessionsNow()) {
			v.AtLimit = &sessionLimit{Window: l.window, ResetsAt: rfc3339(l.resetsAt)}
		}
	}
	if sl := readSessionLabels(config.ResolvePlaybooksDir(), s.f.SessionID); len(s.labels) > 0 || len(sl) > 0 {
		v.Labels = &blockLabelsJSON{Launch: s.labels, Session: sl}
	}
	return v
}

// sessionsNow is the clock a limit's reset is compared with; a variable
// for tests.
var sessionsNow = time.Now

// limitLabel is the short form of a limit for the human tables: limit 5h,
// limit 7d, or the window Claude Code names.
func limitLabel(l *sessionLimit) string {
	switch l.Window {
	case "five_hour":
		return "limit 5h"
	case "seven_day":
		return "limit 7d"
	}
	return "limit " + l.Window
}

// liveSessions is SHOW SESSIONS' rows, and the playbooks it left out
// because their manifests cannot be read.
func liveSessions(forName string) ([]sessionJSON, []playbook.Unreadable, error) {
	dirs, bad, err := sessionDirs(forName)
	if err != nil {
		return nil, nil, err
	}
	rows := []sessionJSON{}
	latest := readLatestFigures(launchRecordsRoot())
	now := sessionsNow()
	for _, s := range readSessionFiles(dirs) {
		v := s.json()
		if l, ok := latest[s.key]; ok && s.key != "" {
			v.AccountAtLimit = accountLimitOf(l, now)
		}
		rows = append(rows, v)
	}
	return rows, bad, nil
}

func sessionRows() ([]any, []playbook.Unreadable, error) {
	rows, bad, err := liveSessions("")
	if err != nil {
		return nil, nil, err
	}
	out := make([]any, len(rows))
	for i, r := range rows {
		out[i] = r
	}
	return out, bad, nil
}

// shortAge is a compact age for the human tables: 45s, 12m, 3h, 2d.
func shortAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", max(0, int(d.Seconds())))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func ageOf(ts *string) string {
	if ts == nil {
		return "-"
	}
	t, err := time.Parse(time.RFC3339, *ts)
	if err != nil {
		return "-"
	}
	return shortAge(t)
}

func showSessions(st *grammar.Stmt) error {
	rows, bad, err := liveSessions(st.For)
	if err != nil {
		return err
	}
	if st.JSON {
		if err := printJSON(rows); err != nil {
			return err
		}
		return leftOut(bad)
	}
	if len(rows) == 0 {
		fmt.Println("No live Claude Code sessions.")
		return leftOut(bad)
	}
	t := newTable("PLAYBOOK", "ACCOUNT", "PID", "TTY", "KIND", "STATUS", "AGE", "ACTIVE", "MODEL", "SESSION", "CWD", "LABELS").flexible(10)
	for _, r := range rows {
		status := deref(r.Status, "-")
		switch {
		case r.AtLimit != nil:
			status = limitLabel(r.AtLimit)
		case r.AccountAtLimit != nil:
			status = "account " + limitLabel(r.AccountAtLimit)
		}
		t.add(r.Playbook, deref(r.Account, "-"), strconv.Itoa(r.PID), deref(r.TTY, "-"), r.Kind, status, ageOf(&r.StartedAt),
			ageOf(r.LastActive), deref(r.Model, "-"), r.SessionID, r.Cwd, labelsCell(r.Labels))
	}
	t.render(os.Stdout)
	return leftOut(bad)
}

// labelsCell is the LABELS column, last so a value's spaces split nothing
// before it: `launch k=v, k=v; session k=v`, each source left out when it
// has none, "-" for no labels.
func labelsCell(l *blockLabelsJSON) string {
	if l == nil {
		return "-"
	}
	var parts []string
	for _, src := range []struct {
		name string
		m    map[string]string
	}{{"launch", l.Launch}, {"session", l.Session}} {
		if len(src.m) == 0 {
			continue
		}
		pairs := make([]string, 0, len(src.m))
		for _, k := range sortedLabelKeys(src.m) {
			pairs = append(pairs, k+"="+src.m[k])
		}
		parts = append(parts, src.name+" "+strings.Join(pairs, ", "))
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, "; ")
}
