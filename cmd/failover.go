package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode"

	"github.com/ramazanpolat/claude-playbooks/internal/account"
	"github.com/ramazanpolat/claude-playbooks/internal/auth"
	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

// Account failover (SPEC.md, "Account failover"): a launch with failover on
// watches its own session. When the session is stopped at a usage limit
// that resets more than 30 minutes from now, has been idle for two minutes,
// and runs nothing (Claude Code's status "idle": no turn, no background
// shell, agent or monitor), the launch ends that claude and resumes the
// same session on the next listed account the playbook may run on, in the
// same terminal and folder, with Claude Code's own continue prompt and a
// first line saying what moved. It acts on no session it did not launch,
// touches no credential beyond what --account does, and never loops: each
// account is tried once per episode, at most three switches per five
// hours, and it stops, saying so, when no listed account is left.

// failoverTarget is one listed account; anyProvider lets it take a
// session from an account of another kind (a Claude login to a gateway).
type failoverTarget struct {
	name        string
	anyProvider bool
}

// failoverConfig is what a launch does at a limit.
type failoverConfig struct {
	on         bool
	accounts   []failoverTarget
	maxContext int64 // 0: no cap
}

// The thresholds, variables so tests can shorten them.
var (
	failoverMinWait   = 30 * time.Minute // a sooner reset is left to Claude Code's own auto-resume
	failoverQuiet     = 2 * time.Minute  // no transcript write for this long
	failoverPoll      = 10 * time.Second
	failoverCap       = 3 // switches per session
	failoverCapWindow = 5 * time.Hour
	failoverNow       = time.Now
)

// failoverSwitch is one switch the watcher decided on.
type failoverSwitch struct {
	From     string `json:"from"`
	To       string `json:"to"`
	At       string `json:"at"`
	Window   string `json:"window"`
	ResetsAt string `json:"resets_at"`
	Context  int64  `json:"context_tokens"`
	// Outcome is moved, or failed when the launch on To did not run and
	// the session went back to From.
	Outcome   string `json:"outcome"`
	sessionID string
	model     string
	resetsAt  time.Time
	// folder is where the session ran: the resume runs from there.
	folder string
	// transcript and size tell, after the launch on To, whether it ran:
	// a session that starts writes to its transcript.
	transcript string
	size       int64
	// noPrompt resumes with no prompt: the way back to the account the
	// session came from, when the new one did not run.
	noPrompt bool
	// stranded: the way back did not run the session either, so nothing
	// runs it now.
	stranded bool
}

func (s *failoverSwitch) Error() string {
	return fmt.Sprintf("failover from %s to %s", s.From, s.To)
}

// failoverEpisode is what one chain of switches has done, across the
// relaunches of one cpb process.
type failoverEpisode struct {
	mu       sync.Mutex
	tried    map[string]bool
	switches []*failoverSwitch
	notes    []string
}

// activeEpisode is this process's episode; a relaunch keeps it.
var activeEpisode = &failoverEpisode{tried: map[string]bool{}}

func (e *failoverEpisode) note(msg string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, n := range e.notes {
		if n == msg {
			return
		}
	}
	e.notes = append(e.notes, msg)
}

func (e *failoverEpisode) recent(now time.Time) int {
	n := 0
	for _, s := range e.switches {
		if at, err := time.Parse(time.RFC3339, s.At); err == nil && now.Sub(at) < failoverCapWindow {
			n++
		}
	}
	return n
}

// failoverWatch watches one launch's session.
type failoverWatch struct {
	cfg       failoverConfig
	configDir string
	playbook  string
	from      string
	fromClass string
	// launchDir is the folder the launch started in.
	launchDir string
	// since is when the launch started: only a limit line written after it
	// is this launch's own to act on; an older one was the launch before's.
	since time.Time
	// canRunOn checks, without changing anything, that the playbook may
	// launch on an account from folder: every refusal a launch there gives.
	canRunOn func(name, folder string) error
	// fullAccounts lists the accounts known to be at a limit now.
	fullAccounts func() map[string]bool
	episode      *failoverEpisode
	// handled is the reset of the limit already decided on: a decision not
	// to switch is made once per limit; a switch marks it once it is done.
	handled time.Time
}

// accountClass is the kind of allowance an account draws on: a Claude
// login or token sent to Anthropic is one kind; any other account is its
// provider at its address, so two gateways at different addresses, or a
// gateway trusted with a Claude login and Anthropic itself, are not one
// kind.
func accountClass(name string) string {
	if name == account.Machine {
		return "claude"
	}
	a, err := account.Read(account.Dir(config.ResolvePlaybooksDir()), name)
	if err != nil || a == nil {
		return "unknown " + name
	}
	address := strings.TrimRight(strings.ToLower(a.BaseURL), "/")
	if a.Auth.Kind == account.ClaudeLogin || a.Auth.Kind == account.ClaudeToken {
		if a.Provider == account.Anthropic {
			return "claude"
		}
		return "claude " + address
	}
	return a.Provider + " " + address
}

// failoverKillAfter is how long claude has to end after SIGTERM before it
// is killed.
var failoverKillAfter = 10 * time.Second

// failoverHandover is the switch whose launch on the new account (or back
// on the old one, when failoverBack) is running, nil otherwise.
var (
	failoverHandover *failoverSwitch
	failoverBack     bool
)

// handoverClaudeStarted is set once the running launch's claude has
// written its session file.
var handoverClaudeStarted atomic.Bool

// failoverTerm sends SIGTERM to the session's claude: through the launch's
// own process handle when it is claude, else by pid. A variable for tests.
var failoverTerm = func(child *os.Process, claudePID int) error {
	if claudePID == child.Pid {
		return child.Signal(syscall.SIGTERM)
	}
	return syscall.Kill(claudePID, syscall.SIGTERM)
}

// handoverSigs catches Ctrl-C between two claudes of a switch: from just
// before cpb ends the old one until the next one has written its session
// file (or cpb run returns), so a secret helper's prompt is covered too. The terminal is in its ordinary mode then, so a Ctrl-C would
// end cpb and leave the session ended; inside claude it is claude's.
var (
	handoverMu   sync.Mutex
	handoverSigs chan os.Signal
)

func catchHandover() {
	handoverMu.Lock()
	defer handoverMu.Unlock()
	if handoverSigs == nil {
		handoverSigs = make(chan os.Signal, 1)
		signal.Notify(handoverSigs, os.Interrupt)
	}
}

func releaseHandover() {
	handoverMu.Lock()
	defer handoverMu.Unlock()
	if handoverSigs != nil {
		signal.Stop(handoverSigs)
		handoverSigs = nil
	}
}

// notRun reports whether a launch on the new account that ended with err
// never ran the session: it failed, and the transcript is as it was when
// the switch was decided.
func (sw *failoverSwitch) notRun(err error) bool {
	return err != nil && fileSize(sw.transcript) == sw.size
}

// watch polls until done closes or it ends claude to switch, which it
// returns. child is the process the launch started: claude itself, or a
// secret helper that runs claude as its child.
func (w *failoverWatch) watch(done <-chan struct{}, child *os.Process) *failoverSwitch {
	t := time.NewTicker(failoverPoll)
	defer t.Stop()
	claudePID, confirmed := 0, false
	for {
		select {
		case <-done:
			return nil
		case <-t.C:
		}
		if !confirmed {
			pid, ok := sessionPIDOf(w.configDir, child.Pid)
			if pid != 0 {
				claudePID = pid
			}
			if ok {
				// This launch's claude is running: a Ctrl-C is its own again.
				confirmed = true
				releaseHandover()
				handoverClaudeStarted.Store(true)
			}
		}
		if claudePID == 0 {
			continue
		}
		// An unconfirmed session file still reaches check, which says once
		// that it does not switch at this limit.
		c := w.check(claudePID)
		if c == nil || !w.act(c, claudePID, child) {
			continue
		}
		go killAfter(done, claudePID, c.procStart, failoverKillAfter)
		return c.sw
	}
}

// killAfter kills pid if it has not ended after the wait (done closes when
// it ends), and only while pid is still the process that was ended, by its
// start time: never a process that took the pid over.
func killAfter(done <-chan struct{}, pid int, start string, after time.Duration) {
	select {
	case <-done:
	case <-time.After(after):
		if sameStart(procStartOf(pid), start) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

// failoverCandidate is a switch check decided on, not yet made: what act
// confirms before it ends anything.
type failoverCandidate struct {
	sw        *failoverSwitch
	mtime     time.Time
	procStart string
}

// validModel is a model id cpb passes to a resume; any other is left out.
var validModel = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@/\[\]-]{0,127}$`)

// check decides, for one poll, whether to switch now. It changes nothing
// a later poll would decide differently on, except when it decides not to
// switch at this limit.
func (w *failoverWatch) check(claudePID int) *failoverCandidate {
	f, ok := readSessionFileOf(w.configDir, claudePID)
	if !ok || f.Status != "idle" || f.Kind != "interactive" || !grammar.ValidSessionID(f.SessionID) {
		return nil
	}
	path := transcriptPath(w.configDir, f.Cwd, f.SessionID)
	if path == "" {
		return nil
	}
	tfi, err := os.Stat(path)
	now := failoverNow()
	if err != nil || now.Sub(tfi.ModTime()) < failoverQuiet {
		return nil
	}
	tail := readTranscriptTail(path)
	l := tail.limit
	// Only a limit this launch's session ran into: a line written before the
	// launch started was the launch before's, and is not acted on again.
	if l == nil || l.at.IsZero() || !l.at.After(w.since) || l.resetsAt.Sub(now) <= failoverMinWait || l.resetsAt.Equal(w.handled) {
		return nil
	}
	limit := fmt.Sprintf("%s limit, resets %s", windowWords(l.window), l.resetsAt.Local().Format("Mon 2 Jan 15:04"))
	decline := func(why string) *failoverCandidate {
		w.handled = l.resetsAt
		w.episode.note(fmt.Sprintf("cpb: did not switch accounts at the %s: %s", limit, why))
		return nil
	}
	// The process is confirmed by its start time before anything else is
	// weighed; one that cannot be is declined once, not retried each poll.
	if f.ProcStart == "" || !sameStart(procStartOf(claudePID), f.ProcStart) {
		return decline("Claude Code's session file gives no start time that matches its process")
	}
	if w.cfg.maxContext > 0 && tail.context == 0 {
		return decline("failover_max_context is set and the context's size is not in the transcript's last 256 KiB")
	}
	if w.cfg.maxContext > 0 && tail.context > w.cfg.maxContext {
		return decline(fmt.Sprintf("the context, %s tokens, is above failover_max_context (%s)", groupDigits(tail.context), groupDigits(w.cfg.maxContext)))
	}
	w.episode.mu.Lock()
	recent := w.episode.recent(now)
	w.episode.mu.Unlock()
	if recent >= failoverCap {
		return decline(fmt.Sprintf("%d switches in the last %d hours already", recent, int(failoverCapWindow.Hours())))
	}
	// The resume runs from the folder whose project directory holds the
	// transcript, where Claude Code finds the session; the same checks a
	// resume there makes come first.
	// The launch folder first: two folders can share one encoded name
	// (my-app, my/app), and the launch's own is the one it ran from.
	candidates := append(spellings(w.launchDir), spellings(f.Cwd)...)
	folder := resumeFolder(path, append(candidates, tail.cwds...)...)
	if folder == "" {
		return decline("the folder the session's transcript belongs to is not known (its project directory matches none of the session's folders)")
	}
	if hasControl(folder) {
		return decline(fmt.Sprintf("the session's folder %q holds a character cpb does not print as written", folder))
	}
	if err := w.resumeGuard(f.SessionID, folder, claudePID); err != nil {
		return decline(err.Error())
	}
	to, why := w.pick(folder)
	if to == "" {
		return decline(why)
	}
	model := tail.model
	if !validModel.MatchString(model) || accountClass(to) != w.fromClass {
		// Another kind of account maps its own models.
		model = ""
	}
	sw := &failoverSwitch{From: w.from, To: to, At: now.UTC().Format(time.RFC3339), Window: l.window, ResetsAt: rfc3339(l.resetsAt),
		Context: tail.context, Outcome: "moved", sessionID: f.SessionID, model: model, resetsAt: l.resetsAt, folder: folder,
		transcript: path, size: tfi.Size()}
	return &failoverCandidate{sw: sw, mtime: tfi.ModTime(), procStart: f.ProcStart}
}

// resumeGuard is what a resume of id from folder would refuse, checked
// before anything is ended: a folder that is not there, a playbook that
// holds no transcript of it, the session live in another process.
func (w *failoverWatch) resumeGuard(id, folder string, own int) error {
	if fi, err := os.Stat(folder); err != nil || !fi.IsDir() {
		return fmt.Errorf("the folder the session ran in, %q, is not there", folder)
	}
	if transcriptPath(w.configDir, "", id) == "" {
		return fmt.Errorf("the playbook holds no transcript of session %s", id)
	}
	dirs, err := knownSessionDirs()
	if err != nil {
		return err
	}
	for _, s := range readSessionFiles(dirs) {
		if s.f.SessionID == id && s.f.PID != own {
			return fmt.Errorf("session %s is live in another process (pid %d)", id, s.f.PID)
		}
	}
	return nil
}

// resumeFolder is the first of candidates whose encoded name is the name of
// the project directory that holds transcript: the folder a resume there
// finds the session from. "" when none is.
func resumeFolder(transcript string, candidates ...string) string {
	want := filepath.Base(filepath.Dir(transcript))
	for _, c := range candidates {
		if c != "" && filepath.IsAbs(c) && encodeProjectDir(c) == want {
			return c
		}
	}
	return ""
}

// spellings is p and p with symlinks resolved, when they differ: a
// launch through a symlink, and Claude Code naming the resolved folder.
func spellings(p string) []string {
	if p == "" {
		return nil
	}
	if r, err := filepath.EvalSymlinks(p); err == nil && r != p {
		return []string{p, r}
	}
	return []string{p}
}

// hidden reports whether c is a character cpb does not print as written:
// a control character, a direction mark (U+061C, U+200E, U+200F), an
// embedding, override or isolate, or a line or paragraph separator.
// Zero-width joiners and spaces stay: folder names hold them, and a paste
// needs them.
func hidden(c rune) bool {
	return unicode.IsControl(c) || c == 0x061C || c == 0x200E || c == 0x200F || c >= 0x202A && c <= 0x202E || c >= 0x2066 && c <= 0x2069 ||
		c == 0x2028 || c == 0x2029
}

// hasControl reports whether s holds a character a terminal does not show
// as written (hidden).
func hasControl(s string) bool {
	return strings.IndexFunc(s, hidden) >= 0
}

// printable is s with each character cpb does not print as written
// (hidden) shown as '?', for a line on the terminal.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if hidden(r) {
			return '?'
		}
		return r
	}, s)
}

// act ends claude for c, once more confirming it is still the case: the
// same session, idle, the same process (its start time), the transcript
// not written since, and the process the launch's own. Only then are the
// limit and the accounts marked: a switch that did not happen leaves the
// next poll free to decide again.
func (w *failoverWatch) act(c *failoverCandidate, claudePID int, child *os.Process) bool {
	f, ok := readSessionFileOf(w.configDir, claudePID)
	if !ok || f.Status != "idle" || f.SessionID != c.sw.sessionID || f.ProcStart == "" || f.ProcStart != c.procStart ||
		!sameStart(procStartOf(claudePID), f.ProcStart) {
		return false
	}
	if fi, err := os.Stat(c.sw.transcript); err != nil || !fi.ModTime().Equal(c.mtime) {
		return false
	}
	if claudePID != child.Pid {
		if info, ok := procStarts([]int{claudePID})[claudePID]; !ok || info.ppid != child.Pid {
			return false
		}
	}
	catchHandover()
	if err := failoverTerm(child, claudePID); err != nil {
		releaseHandover()
		w.episode.note("cpb: could not end the session to switch accounts: " + err.Error())
		return false
	}
	w.handled = c.sw.resetsAt
	w.episode.mu.Lock()
	w.episode.tried[c.sw.From] = true
	w.episode.tried[c.sw.To] = true
	w.episode.mu.Unlock()
	return true
}

// pick is the first listed account the session can move to from folder,
// or "" and why there is none. It marks nothing: act does, once the switch
// is made.
func (w *failoverWatch) pick(folder string) (string, string) {
	if len(w.cfg.accounts) == 0 {
		return "", "no account is listed (ALTER DEFAULTS SET failover_accounts = [...])"
	}
	full := map[string]bool{}
	if w.fullAccounts != nil {
		full = w.fullAccounts()
	}
	var skipped []string
	for _, t := range w.cfg.accounts {
		w.episode.mu.Lock()
		tried := w.episode.tried[t.name]
		w.episode.mu.Unlock()
		switch {
		case t.name == w.from || tried:
			continue
		case full[t.name]:
			skipped = append(skipped, t.name+" is at its limit")
			continue
		case !t.anyProvider && accountClass(t.name) != w.fromClass:
			skipped = append(skipped, t.name+" is another kind of account (list it as '"+t.name+" (any provider)' to allow)")
			continue
		}
		if w.canRunOn != nil {
			if err := w.canRunOn(t.name, folder); err != nil {
				skipped = append(skipped, t.name+": "+firstLine(err.Error()))
				continue
			}
		}
		return t.name, ""
	}
	why := "every listed account was tried or is at its limit"
	if len(skipped) > 0 {
		why += " (" + strings.Join(skipped, "; ") + ")"
	}
	return "", why
}

func windowWords(w string) string {
	switch w {
	case "five_hour":
		return "5-hour"
	case "seven_day":
		return "weekly"
	}
	return strings.ReplaceAll(w, "_", " ")
}

func groupDigits(n int64) string {
	s := strconv.FormatInt(n, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// readSessionFileOf reads Claude Code's session file of pid in configDir.
func readSessionFileOf(configDir string, pid int) (sessionFile, bool) {
	b, err := os.ReadFile(filepath.Join(configDir, "sessions", strconv.Itoa(pid)+".json"))
	if err != nil {
		return sessionFile{}, false
	}
	var f sessionFile
	if json.Unmarshal(b, &f) != nil || f.PID != pid || f.SessionID == "" {
		return sessionFile{}, false
	}
	return f, true
}

// sessionPIDOf is the pid of the claude the launch started: the child
// itself when it has a session file, else the child's own child that does
// (a secret helper that does not exec); 0 while there is none yet. It also
// reports whether that file is confirmed, its start time matching its
// process: a file a process before left on the pid is not.
func sessionPIDOf(configDir string, child int) (int, bool) {
	if f, ok := readSessionFileOf(configDir, child); ok {
		return child, sameStart(procStartOf(child), f.ProcStart)
	}
	ents, err := os.ReadDir(filepath.Join(configDir, "sessions"))
	if err != nil {
		return 0, false
	}
	var pids []int
	for _, e := range ents {
		if p, ok := pidFileName(e.Name()); ok {
			pids = append(pids, p)
		}
	}
	for pid, info := range procStarts(pids) {
		if info.ppid != child {
			continue
		}
		if f, ok := readSessionFileOf(configDir, pid); ok {
			return pid, sameStart(info.start, f.ProcStart)
		}
	}
	return 0, false
}

// continuePrompt is the prompt the resumed session gets: what moved, then
// Claude Code's own continue text.
func continuePrompt(sw *failoverSwitch) string {
	return fmt.Sprintf("[cpb: this session moved from account %s (%s limit, resets %s) to %s; %s, is read again on %s with no cache.]\n\n"+
		"You can continue now. Continue the task you were working on when the usage limit was reached; do not repeat work that is already complete.",
		sw.From, windowWords(sw.Window), sw.resetsAt.Local().Format("Mon 2 Jan 15:04"), sw.To, contextWords(sw.Context), sw.To)
}

// contextWords says a context's size, or that it is not known.
func contextWords(n int64) string {
	if n <= 0 {
		return "its context, of a size cpb could not read"
	}
	return "its context, about " + groupDigits(n) + " tokens"
}

// carriedClaudeFlags are claude's flags a resume keeps, with whether each
// takes a value. Anything else (a prompt, a flag that takes a list, one cpb
// does not know) is not carried: the resume would otherwise risk sending a
// prompt again.
var carriedClaudeFlags = map[string]bool{
	"--permission-mode": true, "--effort": true, "--fallback-model": true, "--agent": true,
	"--append-system-prompt": true, "--system-prompt": true, "--settings": true,
	"--dangerously-skip-permissions": false, "--allow-dangerously-skip-permissions": false,
	"--verbose": false, "--strict-mcp-config": false,
}

// resumeClaudeArgs is the claude arguments of the resume: the session,
// its model, the carried flags, and the continue prompt last.
func resumeClaudeArgs(original []string, sw *failoverSwitch) []string {
	out := []string{"--resume", sw.sessionID}
	if sw.model != "" {
		out = append(out, "--model", sw.model)
	}
	for i := 0; i < len(original); i++ {
		a := original[i]
		if a == "--" {
			break
		}
		flag, value, inline := strings.Cut(a, "=")
		takesValue, known := carriedClaudeFlags[flag]
		if !known {
			continue
		}
		switch {
		case inline && flag == "--settings":
			out = append(out, flag+"="+settingsPath(value))
		case !takesValue || inline:
			out = append(out, a)
		case i+1 < len(original):
			v := original[i+1]
			if flag == "--settings" {
				v = settingsPath(v)
			}
			out = append(out, a, v)
			i++
		}
	}
	if sw.noPrompt {
		return out
	}
	return append(out, continuePrompt(sw))
}

// relaunchArgs is a cpb run command line for the same launch on to: the
// wrapper switches it was given (--failover, --no-sandbox), its launch flags
// without --account, --account to, the playbook, and the resume's claude
// arguments.
func relaunchArgs(switches []string, tokens []launchToken, name string, claudeArgs []string, sw *failoverSwitch) []string {
	out := append([]string(nil), switches...)
	for _, t := range tokens {
		switch t.flag {
		case "--account":
			continue
		case "--env-file":
			// The relaunch runs from the session's folder: a path is
			// made absolute against the folder this launch runs in.
			out = append(out, t.flag+"="+absPath(t.value))
			continue
		}
		out = append(out, t.flag+"="+t.value)
	}
	out = append(out, "--account", sw.To, name)
	return append(out, resumeClaudeArgs(claudeArgs, sw)...)
}

// absPath is p made absolute against this process's folder; p as it is when
// it is absolute or cannot be.
func absPath(p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}

// settingsPath is a --settings value for a resume from another folder:
// a file's path made absolute; JSON as it is.
func settingsPath(v string) string {
	if t := strings.TrimSpace(v); strings.HasPrefix(t, "{") || t == "~" || strings.HasPrefix(t, "~/") {
		return v
	}
	return absPath(v)
}

// absSettingsArgs is args with each --settings file's path made absolute.
func absSettingsArgs(args []string) []string {
	out := append([]string(nil), args...)
	for i := 0; i < len(out); i++ {
		if out[i] == "--" {
			break
		}
		if v, ok := strings.CutPrefix(out[i], "--settings="); ok {
			out[i] = "--settings=" + settingsPath(v)
		} else if out[i] == "--settings" && i+1 < len(out) {
			out[i+1] = settingsPath(out[i+1])
			i++
		}
	}
	return out
}

// printFailoverSummary says, on stderr, what this process's failover did.
func printFailoverSummary() {
	e := activeEpisode
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, s := range e.switches {
		at, _ := time.Parse(time.RFC3339, s.At)
		switch {
		case s.stranded:
			fmt.Fprintf(os.Stderr, "cpb: at %s this session could not move from %s to %s, nor be resumed on %s: nothing runs it now\n", at.Local().Format("15:04"), s.From, s.To, s.From)
			continue
		case s.Outcome == "failed":
			fmt.Fprintf(os.Stderr, "cpb: at %s this session could not move from %s to %s, and stayed on %s\n", at.Local().Format("15:04"), s.From, s.To, s.From)
			continue
		}
		fmt.Fprintf(os.Stderr, "cpb: this session moved from %s to %s at %s (%s limit; %s, read again)\n",
			s.From, s.To, at.Local().Format("15:04"), windowWords(s.Window), contextWords(s.Context))
	}
	for _, n := range e.notes {
		fmt.Fprintln(os.Stderr, n)
	}
}

func (e *failoverEpisode) switchesCopy() []*failoverSwitch {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]*failoverSwitch(nil), e.switches...)
}

// failoverWatchInput is what a launch knows that the watcher needs.
type failoverWatchInput struct {
	pb                    *playbook.Playbook
	configDir, wd, from   string
	tokens                []launchToken
	claudeArgs            []string
	flagOn, flagOff, skip bool
}

// failoverWatchFor is the watcher of a launch, nil when failover is off for
// it. skip is a launch failover never takes (a caller-supplied config
// directory, cpb play's throwaway playbook); --failover there is refused.
func failoverWatchFor(in failoverWatchInput) (*failoverWatch, error) {
	if in.skip {
		if in.flagOn {
			return nil, fmt.Errorf("--failover is for a registered playbook's own launch (not cpb play, not CPB_CONFIG_DIR)")
		}
		return nil, nil
	}
	cfg, err := failoverConfigFor(config.ResolvePlaybooksDir(), in.pb.Manifest, in.flagOn, in.flagOff)
	if err != nil || !cfg.on {
		return nil, err
	}
	if len(cfg.accounts) == 0 {
		if in.flagOn {
			return nil, fmt.Errorf("--failover: no account is listed to move a session to (ALTER DEFAULTS SET failover_accounts = [...])")
		}
		return nil, nil
	}
	// A resume keeps only some of claude's flags; a launch that passes any
	// other (a restriction, -p) is not moved, rather than moved with less.
	if flag := failoverBlockingFlag(in.claudeArgs); flag != "" {
		if in.flagOn {
			return nil, fmt.Errorf("--failover: this launch passes %s to claude, which a resume on another account would not keep", flag)
		}
		fmt.Fprintf(os.Stderr, "cpb: failover is off for this launch: it passes %s to claude, which a resume on another account would not keep\n", flag)
		return nil, nil
	}
	from := in.from
	if from == "" {
		from = account.Machine
	}
	w := &failoverWatch{cfg: cfg, configDir: in.configDir, playbook: in.pb.Name, from: from,
		fromClass: accountClass(from), launchDir: in.wd, episode: activeEpisode, since: failoverNow()}
	// The pre-check reads a --settings file as the relaunch will: by its
	// path from the launch folder, not from the session's.
	checkArgs := absSettingsArgs(in.claudeArgs)
	w.canRunOn = func(next, folder string) error {
		var tk []launchToken
		for _, t := range in.tokens {
			if t.flag != "--account" {
				tk = append(tk, t)
			}
		}
		tk = append(tk, launchToken{flag: "--account", value: next})
		layers, err := launchLayers(tk)
		if err != nil {
			return err
		}
		a, err := planAccount(in.configDir, in.pb.Name, layers, folder, checkArgs)
		if err != nil {
			return err
		}
		if err := liveLoginRefusal(in.pb.Name, in.configDir, a); err != nil {
			return err
		}
		// Every other guard a launch runs before it changes anything, on
		// the target's own layers: the secret helper of its references,
		// its request headers, a token over the playbook's own login.
		eff, err := auth.EffectiveBlock(in.configDir, a.layers)
		if err != nil {
			return err
		}
		plan, err := planLaunch(eff)
		if err != nil {
			return err
		}
		if err := plan.withHeaders(a.rendered.Headers); err != nil {
			return err
		}
		return auth.OwnLoginRefusal(in.configDir, eff, in.pb.Name)
	}
	w.fullAccounts = func() map[string]bool {
		// At a limit by the latest usage figure of the allowance it draws
		// on from this playbook (SHOW USAGE: a Claude login's figure is the
		// login's, machine or playbook:<name>, not the account name's), or
		// by a live session stopped there.
		out := map[string]bool{}
		root := config.ResolvePlaybooksDir()
		full := fullKeys(readLatestFigures(root), time.Now())
		for _, t := range cfg.accounts {
			sel := account.Selection{Name: t.name}
			if t.name != account.Machine {
				a, err := account.Read(account.Dir(root), t.name)
				if err != nil || a == nil {
					continue
				}
				sel.Account = a
			}
			if full[usageKey(sel, in.configDir, in.pb.Name)] {
				out[t.name] = true
			}
		}
		rows, _, err := liveSessions("")
		if err != nil {
			return out
		}
		for _, r := range rows {
			if r.Account != nil && r.AtLimit != nil {
				out[*r.Account] = true
			}
		}
		return out
	}
	return w, nil
}

// failoverBlockingFlag is the first of claude's flags in args that a
// resume would not keep, "" when there is none: every flag must be one a
// resume carries (carriedClaudeFlags), or the session's own (--resume,
// --continue, --fork-session, --model, which the resume sets itself).
func failoverBlockingFlag(args []string) string {
	own := map[string]bool{"--model": true, "--continue": false, "-c": false, "--fork-session": false}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return ""
		}
		if !strings.HasPrefix(a, "-") {
			continue // the first prompt: never sent again
		}
		flag, _, inline := strings.Cut(a, "=")
		if flag == "--resume" || flag == "-r" {
			if !inline && i+1 < len(args) && grammar.ValidSessionID(args[i+1]) {
				i++
			}
			continue
		}
		takes, ok := carriedClaudeFlags[flag]
		if !ok {
			takes, ok = own[flag]
		}
		if !ok {
			return flag
		}
		if takes && !inline {
			i++
		}
	}
	return ""
}
