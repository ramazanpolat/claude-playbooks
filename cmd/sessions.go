package cmd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
	"github.com/ramazanpolat/claude-playbooks/internal/shell"
)

// Sessions (docs/reference/cli-grammar.md, "Sessions"): the live Claude
// Code sessions of cpb's config dirs, SHOW SESSIONS, the SESSIONS table,
// RESUME and the line printed when a launch ends.
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
}

// liveState is what cpb can tell about a session file's process.
type liveState int

const (
	notLive liveState = iota
	live
	liveUnknown // another pid domain (a sandbox, another host): cpb cannot tell
)

// procStarts returns the start time of each pid that is alive, in the
// form Claude Code writes as procStart on this platform: on Linux the
// starttime field of /proc/<pid>/stat (clock ticks since boot, observed on
// 2.1.233), elsewhere ps's lstart read in UTC with the C locale ("Mon Sep
// 28 07:29:55 2026", observed on 2.1.282 and 2.1.283 on macOS). A pid
// missing from the map is not alive. It is a variable so tests can fake
// processes.
var procStarts = func(pids []int) map[int]string {
	if runtime.GOOS == "linux" {
		return procStatStarts(pids)
	}
	return psStarts(pids)
}

func procStatStarts(pids []int) map[int]string {
	out := map[int]string{}
	for _, pid := range pids {
		b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
		if err != nil {
			continue
		}
		// The command name (field 2) is in parentheses and may hold spaces;
		// the fields after it start at field 3, so starttime (22) is the 20th.
		i := bytes.LastIndexByte(b, ')')
		if i < 0 {
			continue
		}
		if f := strings.Fields(string(b[i+1:])); len(f) > 19 {
			out[pid] = f[19]
		}
	}
	return out
}

func psStarts(pids []int) map[int]string {
	out := map[int]string{}
	if len(pids) == 0 {
		return out
	}
	list := make([]string, len(pids))
	for i, p := range pids {
		list[i] = strconv.Itoa(p)
	}
	c := exec.Command("ps", "-o", "pid=,lstart=", "-p", strings.Join(list, ","))
	c.Env = append(os.Environ(), "TZ=UTC", "LC_ALL=C")
	b, _ := c.Output() // ps exits 1 when none of the pids is alive
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		out[pid] = strings.Join(f[1:], " ")
	}
	return out
}

// pidDomain is this host's pid domain, as Claude Code writes it.
var pidDomain = runtime.GOOS

// sessionDirs is every config dir whose sessions are listed: the playbooks
// and the plain directories in cpb's state. forName limits it to one
// playbook (an unknown one is refused).
func sessionDirs(forName string) ([]sessionDir, error) {
	root := config.ResolvePlaybooksDir()
	if forName != "" {
		pb, err := playbook.Require(root, forName)
		if err != nil {
			return nil, err
		}
		return []sessionDir{playbookSessionDir(pb)}, nil
	}
	pbs, err := playbook.Discover(root)
	if err != nil {
		return nil, err
	}
	var dirs []sessionDir
	for _, pb := range pbs {
		dirs = append(dirs, playbookSessionDir(pb))
	}
	if st, err := readDirState(); err == nil {
		var plain []string
		for d := range st.Dirs {
			plain = append(plain, d)
		}
		sort.Strings(plain)
		for _, d := range plain {
			dirs = append(dirs, sessionDir{label: d, path: d})
		}
	}
	return dirs, nil
}

func playbookSessionDir(pb *playbook.Playbook) sessionDir {
	d := sessionDir{label: pb.Name, path: pb.Path, pb: pb}
	if pb.Manifest != nil {
		d.launcher = pb.Manifest.Alias
	}
	return d
}

// cliName is how the pilot calls cpb, for the commands cpb prints.
func cliName() string {
	if b := filepath.Base(os.Args[0]); b == "cpb" || b == "claude-playbook" {
		return b
	}
	return "claude-playbook"
}

// resumeCommand is the command that resumes id in d: the launcher, cpb run,
// or, for a plain directory, claude under that config dir.
func (d sessionDir) resumeCommand(id string) string {
	switch {
	case d.launcher != "":
		return d.launcher + " --resume " + id
	case d.pb != nil:
		return cliName() + " run " + d.pb.Name + " --resume " + id
	}
	return "CLAUDE_CONFIG_DIR=" + shell.QuoteArg(d.path) + " claude --resume " + id
}

// liveSession is a session file with its dir and what cpb can tell about
// its process.
type liveSession struct {
	dir   sessionDir
	f     sessionFile
	state liveState
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
			start, ok := starts[s.f.PID]
			// A pid that is alive but started at another time is a reused
			// pid: the session it recorded has ended.
			if !ok || (s.f.ProcStart != "" && strings.Join(strings.Fields(s.f.ProcStart), " ") != start) {
				continue
			}
			s.state = live
		}
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].f.StartedAt > out[j].f.StartedAt })
	return out
}

// encodeProjectDir is Claude Code's directory name under projects/ for a
// working directory: every character outside [A-Za-z0-9] becomes '-'.
func encodeProjectDir(cwd string) string {
	b := []byte(cwd)
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			b[i] = '-'
		}
	}
	return string(b)
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
// model of its last assistant line, its last cwd and its last AI title.
type transcriptTail struct {
	model, cwd, title string
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
	var line struct {
		Type    string `json:"type"`
		Cwd     string `json:"cwd"`
		AITitle string `json:"aiTitle"`
		Message struct {
			Model string `json:"model"`
		} `json:"message"`
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64<<10), transcriptTailBytes+1)
	for sc.Scan() {
		line.Type, line.Cwd, line.AITitle, line.Message.Model = "", "", "", ""
		if json.Unmarshal(sc.Bytes(), &line) != nil {
			continue // the first line of a tail is usually cut
		}
		if line.Cwd != "" {
			t.cwd = line.Cwd
		}
		if line.AITitle != "" {
			t.title = line.AITitle
		}
		if line.Type == "assistant" && line.Message.Model != "" && !strings.HasPrefix(line.Message.Model, "<") {
			t.model = line.Message.Model
		}
	}
	return t
}

func rfc3339(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func (s liveSession) json() sessionJSON {
	v := sessionJSON{
		Playbook: s.dir.label, ConfigDir: s.dir.path, PID: s.f.PID, SessionID: s.f.SessionID,
		Cwd: s.f.Cwd, Kind: s.f.Kind, Status: optStr(s.f.Status), Name: optStr(s.f.Name),
		ClaudeVersion: optStr(s.f.Version), StartedAt: rfc3339(time.UnixMilli(s.f.StartedAt)),
		Launcher: optStr(s.dir.launcher), Resume: s.dir.resumeCommand(s.f.SessionID),
	}
	if p := transcriptPath(s.dir.path, s.f.Cwd, s.f.SessionID); p != "" {
		if fi, err := os.Stat(p); err == nil {
			v.LastActive = strPtr(rfc3339(fi.ModTime()))
		}
		v.Model = optStr(readTranscriptTail(p).model)
	}
	return v
}

// liveSessions is SHOW SESSIONS' rows.
func liveSessions(forName string) ([]sessionJSON, error) {
	dirs, err := sessionDirs(forName)
	if err != nil {
		return nil, err
	}
	rows := []sessionJSON{}
	for _, s := range readSessionFiles(dirs) {
		rows = append(rows, s.json())
	}
	return rows, nil
}

func sessionRows() ([]any, error) {
	rows, err := liveSessions("")
	if err != nil {
		return nil, err
	}
	out := make([]any, len(rows))
	for i, r := range rows {
		out[i] = r
	}
	return out, nil
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
	rows, err := liveSessions(st.For)
	if err != nil {
		return err
	}
	if st.JSON {
		return printJSON(rows)
	}
	if len(rows) == 0 {
		fmt.Println("No live Claude Code sessions.")
		return nil
	}
	t := newTable("PLAYBOOK", "PID", "KIND", "STATUS", "AGE", "ACTIVE", "MODEL", "SESSION", "CWD").flexible(8)
	for _, r := range rows {
		t.add(r.Playbook, strconv.Itoa(r.PID), r.Kind, deref(r.Status, "-"), ageOf(&r.StartedAt),
			ageOf(r.LastActive), deref(r.Model, "-"), r.SessionID, r.Cwd)
	}
	t.render(os.Stdout)
	return nil
}

// sessionsCmd is `cpb sessions`: the documented lowercase shorthand for
// exactly SHOW SESSIONS [--json].
var sessionsCmd = &cobra.Command{
	Use:   "sessions",
	Short: "List the live Claude Code sessions of your playbooks (SHOW SESSIONS)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		asJSON, _ := cmd.Flags().GetBool("json")
		return showSessions(&grammar.Stmt{Verb: grammar.Show, Object: grammar.Sessions, JSON: asJSON})
	},
}

func init() {
	sessionsCmd.Flags().Bool("json", false, "print the sessions as JSON, as SHOW SESSIONS --json")
}
