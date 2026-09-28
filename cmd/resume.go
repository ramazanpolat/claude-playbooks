package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// RESUME (docs/reference/cli-grammar.md, "Sessions"): resume a Claude Code
// session through its playbook's own launch path, with --resume <id>. It
// refuses a session that is still live in another process: two processes
// on one session id is the --continue hazard, and it corrupts the session.

// resumeCandidate is one transcript a RESUME could pick.
type resumeCandidate struct {
	dir   sessionDir
	id    string
	path  string
	mtime time.Time
	live  *liveSession // the live process holding it, nil for none
}

// resumeListLimit is how many sessions RESUME --list shows.
const resumeListLimit = 10

// resumeJSON is one row of RESUME --list --json: SHOW SESSIONS' fields that
// a transcript has, plus whether it is live and its title.
type resumeJSON struct {
	Playbook   string  `json:"playbook"`
	ConfigDir  string  `json:"config_dir"`
	SessionID  string  `json:"session_id"`
	Cwd        string  `json:"cwd"`
	LastActive string  `json:"last_active"`
	Model      *string `json:"model"`
	Title      *string `json:"title"`
	Launcher   *string `json:"launcher"`
	Live       bool    `json:"live"`
	PID        *int    `json:"pid"`
	Resume     string  `json:"resume"`
}

// liveByID indexes the live sessions of every config dir cpb knows by
// session id, whatever dir a RESUME is limited to: a transcript copied
// into another dir is still the same live session.
func liveByID() (map[string]*liveSession, error) {
	dirs, err := sessionDirs("")
	if err != nil {
		return nil, err
	}
	m := map[string]*liveSession{}
	for _, s := range readSessionFiles(dirs) {
		s := s
		m[s.f.SessionID] = &s
	}
	return m, nil
}

// resumeCandidates is every session of dirs in the working directory cwd,
// newest first.
func resumeCandidates(dirs []sessionDir, cwd string, lives map[string]*liveSession) []resumeCandidate {
	var out []resumeCandidate
	for _, d := range dirs {
		var m []string
		for _, spelling := range cwdSpellings(cwd) {
			more, _ := filepath.Glob(filepath.Join(d.path, "projects", encodeProjectDir(spelling), "*.jsonl"))
			m = append(m, more...)
		}
		for _, p := range m {
			id := strings.TrimSuffix(filepath.Base(p), ".jsonl")
			if !grammar.ValidSessionID(id) || strings.HasPrefix(id, "agent-") {
				continue
			}
			fi, err := os.Stat(p)
			if err != nil || !fi.Mode().IsRegular() {
				continue
			}
			out = append(out, resumeCandidate{dir: d, id: id, path: p, mtime: fi.ModTime(), live: lives[id]})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].mtime.After(out[j].mtime) })
	return out
}

// cwdSpellings are the names of the working directory a session there may
// have been recorded under: as cpb sees it, as the shell spelled it ($PWD),
// and with symlinks resolved (/tmp and /private/tmp on macOS).
func cwdSpellings(cwd string) []string {
	out := []string{cwd}
	add := func(p string) {
		if p == "" {
			return
		}
		for _, o := range out {
			if o == p {
				return
			}
		}
		out = append(out, p)
	}
	if pwd := os.Getenv("PWD"); pwd != "" {
		if a, err := os.Stat(pwd); err == nil {
			if b, err := os.Stat(cwd); err == nil && os.SameFile(a, b) {
				add(pwd)
			}
		}
	}
	if r, err := filepath.EvalSymlinks(cwd); err == nil {
		add(r)
	}
	return out
}

func (c resumeCandidate) json(cwd string) resumeJSON {
	t := readTranscriptTail(c.path)
	if t.cwd != "" {
		cwd = t.cwd
	}
	v := resumeJSON{
		Playbook: c.dir.label, ConfigDir: c.dir.path, SessionID: c.id, Cwd: cwd,
		LastActive: rfc3339(c.mtime), Model: optStr(t.model), Title: optStr(t.title),
		Launcher: optStr(c.dir.launcher), Live: c.live != nil, Resume: c.dir.resumeCommand(c.id),
	}
	if c.live != nil {
		pid := c.live.f.PID
		v.PID = &pid
	}
	return v
}

// liveRefusal is the refusal for a session still live in another process.
func liveRefusal(id string, s *liveSession) error {
	if s.state == liveUnknown {
		return fmt.Errorf("session %s may still be running (playbook %s, pid %d in another pid domain, %q): cpb cannot tell, so it does not resume it. Two processes on one session id corrupt it; close that one first, or pick another with RESUME --list",
			id, s.dir.label, s.f.PID, s.f.PIDDomain)
	}
	return fmt.Errorf("session %s is still running (playbook %s, pid %d, since %s). Two processes on one session id corrupt it; close that one first, or pick another with RESUME --list",
		id, s.dir.label, s.f.PID, formatAge(time.UnixMilli(s.f.StartedAt)))
}

func runResume(st *grammar.Stmt) error {
	if _, override, err := config.ResolveConfigDirOverride(); err != nil {
		return err
	} else if override && !st.List {
		return fmt.Errorf("RESUME resumes a session under the config dir it was recorded in; unset %s first", config.ConfigDirOverrideEnv)
	}
	dirs, err := sessionDirs(st.For)
	if err != nil {
		return err
	}
	lives, err := liveByID()
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if st.List {
		return listResumable(dirs, cwd, lives, st.JSON)
	}

	var target resumeCandidate
	if st.Session != "" {
		var found []resumeCandidate
		for _, d := range dirs {
			if p := transcriptPath(d.path, cwd, st.Session); p != "" {
				fi, err := os.Stat(p)
				if err != nil {
					continue
				}
				found = append(found, resumeCandidate{dir: d, id: st.Session, path: p, mtime: fi.ModTime(), live: lives[st.Session]})
			}
		}
		switch len(found) {
		case 0:
			if s := lives[st.Session]; s != nil {
				return liveRefusal(st.Session, s)
			}
			return fmt.Errorf("no session %s was found in any playbook's transcripts", st.Session)
		case 1:
			target = found[0]
		default:
			var in []string
			for _, c := range found {
				in = append(in, c.dir.label)
			}
			return fmt.Errorf("session %s is in more than one config dir (%s): name one with FOR PLAYBOOK <name>", st.Session, strings.Join(in, ", "))
		}
		if target.live != nil {
			return liveRefusal(target.id, target.live)
		}
		fmt.Fprintf(os.Stderr, "Resuming %s of %s (last active %s)\n", target.id, target.dir.label, formatAge(target.mtime))
	} else {
		cands := resumeCandidates(dirs, cwd, lives)
		if len(cands) == 0 {
			return fmt.Errorf("no Claude Code session was found in %s for any playbook", cwd)
		}
		var newer []resumeCandidate
		for _, c := range cands {
			if c.live == nil {
				target = c
				break
			}
			newer = append(newer, c)
		}
		if target.id == "" {
			return fmt.Errorf("every session in %s is still running (%s). Two processes on one session id corrupt it; close one first", cwd, pidList(newer))
		}
		if len(newer) > 0 {
			noun := "sessions are"
			if len(newer) == 1 {
				noun = "session is"
			}
			fmt.Fprintf(os.Stderr, "%d newer %s live (%s); resuming %s of %s (last active %s)\n",
				len(newer), noun, pidList(newer), target.id, target.dir.label, formatAge(target.mtime))
		} else {
			fmt.Fprintf(os.Stderr, "Resuming %s of %s (last active %s)\n", target.id, target.dir.label, formatAge(target.mtime))
		}
	}
	return launchResume(target, cwd)
}

func pidList(cs []resumeCandidate) string {
	var ps []string
	for _, c := range cs {
		ps = append(ps, strconv.Itoa(c.live.f.PID))
	}
	if len(ps) == 1 {
		return "pid " + ps[0]
	}
	return "pids " + strings.Join(ps, ", ")
}

// launchResume starts claude --resume <id> for c: through the playbook's own
// launch path (cpb run, as its launcher does), or, for a plain directory,
// claude under that config dir with nothing added.
func launchResume(c resumeCandidate, cwd string) error {
	if c.dir.pb != nil {
		var sbm *manifest.Sandbox
		if c.dir.pb.Manifest != nil {
			sbm = c.dir.pb.Manifest.Sandbox
		}
		if on, _, err := resolveSandbox(sbm, &sandboxOpts{}, fmt.Sprintf("playbook %q", c.dir.pb.Name)); err == nil && on {
			return fmt.Errorf("playbook %s runs in a sandbox, whose sessions RESUME does not reach yet; resume it inside the sandbox", c.dir.pb.Name)
		}
	}
	// Claude Code finds a session by the working directory it ran in.
	if dir := readTranscriptTail(c.path).cwd; dir != "" && dir != cwd {
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			return fmt.Errorf("session %s ran in %s, which no longer exists", c.id, dir)
		}
		if err := os.Chdir(dir); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "In %s, where the session ran\n", dir)
	}
	if c.dir.pb != nil {
		return runRun(nil, []string{c.dir.pb.Name, "--resume", c.id})
	}
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		return fmt.Errorf("'claude' command not found. Install Claude Code first: https://claude.ai/download")
	}
	cmd := exec.Command(claudePath, "--resume", c.id)
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR="+c.dir.path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return preserveExitCode(cmd.Run())
}

func listResumable(dirs []sessionDir, cwd string, lives map[string]*liveSession, asJSON bool) error {
	cands := resumeCandidates(dirs, cwd, lives)
	if len(cands) > resumeListLimit {
		cands = cands[:resumeListLimit]
	}
	rows := make([]resumeJSON, 0, len(cands))
	for _, c := range cands {
		rows = append(rows, c.json(cwd))
	}
	if asJSON {
		return printJSON(rows)
	}
	if len(rows) == 0 {
		fmt.Printf("No Claude Code session was found in %s.\n", cwd)
		return nil
	}
	t := newTable("SESSION", "PLAYBOOK", "ACTIVE", "MODEL", "LIVE", "TITLE").flexible(5)
	for _, r := range rows {
		liveCell := "-"
		if r.PID != nil {
			liveCell = "pid " + strconv.Itoa(*r.PID)
		}
		t.add(r.SessionID, r.Playbook, ageOf(&r.LastActive), deref(r.Model, "-"), liveCell, deref(r.Title, "-"))
	}
	t.render(os.Stdout)
	return nil
}

// The exit line: after claude exits under cpb run or a launcher, cpb names
// the command that resumes the session in this playbook. Claude Code's own
// "claude --resume" line would open it under ~/.claude instead.

// stderrTTY is whether stderr is a terminal; a variable for tests.
var stderrTTY = func() bool { return isTerminal(os.Stderr) }

// printMode reports whether claude ran non-interactively (-p / --print).
func printMode(claudeArgs []string) bool {
	for _, a := range claudeArgs {
		if a == "--" {
			return false
		}
		if a == "-p" || a == "--print" || strings.HasPrefix(a, "--print=") {
			return true
		}
	}
	return false
}

// sessionSince is the session a launch that started at since left in
// configDir: the newest transcript written since then that no other live
// process holds. "" when there is none (the session never got a message).
func sessionSince(configDir string, since time.Time) string {
	m, _ := filepath.Glob(filepath.Join(configDir, "projects", "*", "*.jsonl"))
	held := map[string]bool{}
	for _, s := range readSessionFiles([]sessionDir{{path: configDir}}) {
		held[s.f.SessionID] = true
	}
	var best string
	var bestT time.Time
	for _, p := range m {
		id := strings.TrimSuffix(filepath.Base(p), ".jsonl")
		if !grammar.ValidSessionID(id) || strings.HasPrefix(id, "agent-") || held[id] {
			continue
		}
		fi, err := os.Stat(p)
		if err != nil || !fi.Mode().IsRegular() || fi.ModTime().Before(since.Truncate(time.Second)) {
			continue
		}
		if best == "" || fi.ModTime().After(bestT) {
			best, bestT = id, fi.ModTime()
		}
	}
	return best
}

// printResumeLine prints the exit line for a local launch of d that started
// at since, when stderr is a terminal and claude was interactive.
func printResumeLine(d sessionDir, claudeArgs []string, since time.Time) {
	if !stderrTTY() || printMode(claudeArgs) {
		return
	}
	if id := sessionSince(d.path, since); id != "" {
		fmt.Fprintf(os.Stderr, "Resume this playbook's session with: %s\n", d.resumeCommand(id))
	}
}
