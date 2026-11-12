package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ramazanpolat/claude-playbooks/internal/account"
	"github.com/ramazanpolat/claude-playbooks/internal/config"
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
	From      string `json:"from"`
	To        string `json:"to"`
	At        string `json:"at"`
	Window    string `json:"window"`
	ResetsAt  string `json:"resets_at"`
	Context   int64  `json:"context_tokens"`
	sessionID string
	model     string
	resetsAt  time.Time
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
	// canRunOn checks, without changing anything, that the playbook may
	// launch on an account: every refusal a launch on it would give.
	canRunOn func(name string) error
	// fullAccounts lists the accounts known to be at a limit now.
	fullAccounts func() map[string]bool
	episode      *failoverEpisode
	// handled is the reset of the limit already decided on, so a decision
	// not to switch is made once per limit.
	handled time.Time
}

// accountClass is the kind of allowance an account draws on: a Claude
// login or token (one subscription kind), or its provider.
func accountClass(name string) string {
	if name == account.Machine {
		return "claude"
	}
	a, err := account.Read(account.Dir(config.ResolvePlaybooksDir()), name)
	if err != nil || a == nil {
		return "unknown"
	}
	if a.Auth.Kind == account.ClaudeLogin || a.Auth.Kind == account.ClaudeToken {
		return "claude"
	}
	return a.Provider
}

// watch polls until done closes or it ends claude to switch, which it
// returns. child is the process the launch started: claude itself, or a
// secret helper that runs claude as its child.
func (w *failoverWatch) watch(done <-chan struct{}, childPID int) *failoverSwitch {
	t := time.NewTicker(failoverPoll)
	defer t.Stop()
	claudePID := 0
	for {
		select {
		case <-done:
			return nil
		case <-t.C:
		}
		if claudePID == 0 {
			claudePID = sessionPIDOf(w.configDir, childPID)
			if claudePID == 0 {
				continue
			}
		}
		if sw := w.check(claudePID); sw != nil {
			// Once more, right before acting: a message may have come in.
			if f, ok := readSessionFileOf(w.configDir, claudePID); !ok || f.Status != "idle" {
				continue
			}
			if err := syscall.Kill(claudePID, syscall.SIGTERM); err != nil {
				w.episode.note("cpb: could not end the session to switch accounts: " + err.Error())
				continue
			}
			return sw
		}
	}
}

// check decides, for one poll, whether to switch now.
func (w *failoverWatch) check(claudePID int) *failoverSwitch {
	f, ok := readSessionFileOf(w.configDir, claudePID)
	if !ok || f.Status != "idle" {
		return nil
	}
	path := transcriptPath(w.configDir, f.Cwd, f.SessionID)
	if path == "" {
		return nil
	}
	fi, err := os.Stat(path)
	now := failoverNow()
	if err != nil || now.Sub(fi.ModTime()) < failoverQuiet {
		return nil
	}
	tail := readTranscriptTail(path)
	l := tail.limit
	if l == nil || l.resetsAt.Sub(now) <= failoverMinWait || l.resetsAt.Equal(w.handled) {
		return nil
	}
	w.handled = l.resetsAt
	limit := fmt.Sprintf("%s limit, resets %s", windowWords(l.window), l.resetsAt.Local().Format("Mon 2 Jan 15:04"))
	if w.cfg.maxContext > 0 && tail.context > w.cfg.maxContext {
		w.episode.note(fmt.Sprintf("cpb: did not switch accounts at the %s: the context, %s tokens, is above failover_max_context (%s)", limit, groupDigits(tail.context), groupDigits(w.cfg.maxContext)))
		return nil
	}
	w.episode.mu.Lock()
	w.episode.tried[w.from] = true
	recent := w.episode.recent(now)
	w.episode.mu.Unlock()
	if recent >= failoverCap {
		w.episode.note(fmt.Sprintf("cpb: did not switch accounts at the %s: %d switches in the last %d hours already", limit, recent, int(failoverCapWindow.Hours())))
		return nil
	}
	to, why := w.pick()
	if to == "" {
		w.episode.note(fmt.Sprintf("cpb: did not switch accounts at the %s: %s", limit, why))
		return nil
	}
	return &failoverSwitch{From: w.from, To: to, At: now.UTC().Format(time.RFC3339), Window: l.window,
		ResetsAt: rfc3339(l.resetsAt), Context: tail.context, sessionID: f.SessionID, model: tail.model, resetsAt: l.resetsAt}
}

// pick is the first listed account the session can move to, or "" and why
// there is none.
func (w *failoverWatch) pick() (string, string) {
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
			if err := w.canRunOn(t.name); err != nil {
				skipped = append(skipped, t.name+": "+firstLine(err.Error()))
				continue
			}
		}
		w.episode.mu.Lock()
		w.episode.tried[t.name] = true
		w.episode.mu.Unlock()
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
// (a secret helper that does not exec). 0 while there is none yet.
func sessionPIDOf(configDir string, child int) int {
	if _, ok := readSessionFileOf(configDir, child); ok {
		return child
	}
	ents, err := os.ReadDir(filepath.Join(configDir, "sessions"))
	if err != nil {
		return 0
	}
	var pids []int
	for _, e := range ents {
		if p, ok := launchRecordPID(e.Name()); ok {
			pids = append(pids, p)
		}
	}
	for pid, info := range procStarts(pids) {
		if info.ppid == child {
			return pid
		}
	}
	return 0
}

// continuePrompt is the prompt the resumed session gets: what moved, then
// Claude Code's own continue text.
func continuePrompt(sw *failoverSwitch) string {
	return fmt.Sprintf("[cpb: this session moved from account %s (%s limit, resets %s) to %s; its context, about %s tokens, is read again on %s with no cache.]\n\n"+
		"You can continue now. Continue the task you were working on when the usage limit was reached; do not repeat work that is already complete.",
		sw.From, windowWords(sw.Window), sw.resetsAt.Local().Format("Mon 2 Jan 15:04"), sw.To, groupDigits(sw.Context), sw.To)
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
		flag, _, inline := strings.Cut(a, "=")
		takesValue, known := carriedClaudeFlags[flag]
		if !known {
			continue
		}
		if !takesValue || inline {
			out = append(out, a)
			continue
		}
		if i+1 < len(original) {
			out = append(out, a, original[i+1])
			i++
		}
	}
	return append(out, continuePrompt(sw))
}

// relaunchArgs is a cpb run command line for the same launch on to: its
// launch flags without --account, --account to, the playbook, and the
// resume's claude arguments.
func relaunchArgs(tokens []launchToken, name string, claudeArgs []string, sw *failoverSwitch) []string {
	var out []string
	for _, t := range tokens {
		if t.flag == "--account" {
			continue
		}
		out = append(out, t.flag+"="+t.value)
	}
	out = append(out, "--account", sw.To, name)
	return append(out, resumeClaudeArgs(claudeArgs, sw)...)
}

// printFailoverSummary says, on stderr, what this process's failover did.
func printFailoverSummary() {
	e := activeEpisode
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, s := range e.switches {
		at, _ := time.Parse(time.RFC3339, s.At)
		fmt.Fprintf(os.Stderr, "cpb: this session moved from %s to %s at %s (%s limit, %s tokens of context read again)\n",
			s.From, s.To, at.Local().Format("15:04"), windowWords(s.Window), groupDigits(s.Context))
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
	from := in.from
	if from == "" {
		from = account.Machine
	}
	w := &failoverWatch{cfg: cfg, configDir: in.configDir, playbook: in.pb.Name, from: from,
		fromClass: accountClass(from), episode: activeEpisode}
	w.canRunOn = func(next string) error {
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
		a, err := planAccount(in.configDir, in.pb.Name, layers, in.wd, in.claudeArgs)
		if err != nil {
			return err
		}
		return liveLoginRefusal(in.pb.Name, in.configDir, a)
	}
	w.fullAccounts = func() map[string]bool {
		out := map[string]bool{}
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
