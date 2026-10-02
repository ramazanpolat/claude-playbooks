package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ramazanpolat/claude-playbooks/internal/auth"
	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envprofile"
	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/play"
)

// cpb play, slice 2: running a played recipe. The preview, then the yes,
// then a typed confirmation for each model endpoint, proxy, TLS change and
// secret reference; then the exact bytes are applied to a throwaway
// playbook in a throwaway store, the session runs, and everything is removed
// on every way out.

var (
	playYes           bool
	playTrustEndpoint []string
	playTrustSecret   []string
	playEnvSets       []string
)

// playMarker, in a throwaway store's directory, names the cpb that owns
// it, so the next play can sweep one a killed cpb left behind.
const playMarker = ".cpb-play"

// playSweepAge is how old an orphan must be before it is swept: a session
// can run long, and its claude may outlive a killed cpb for a while.
const playSweepAge = 24 * time.Hour

// promptIn is where the prompts read from (a test replaces it).
var promptIn io.Reader = os.Stdin

// promptCancel, when set, ends a waiting prompt: the play guard's
// cancellation, so ^C at a prompt stops the play instead of being swallowed.
var promptCancel <-chan struct{}

// promptLine reads one line, a byte at a time: a buffered reader would
// take the following answers with it, and the next prompt would read
// nothing. It returns ok false when the play is cancelled while it waits.
func promptLine(prompt string) (string, bool) {
	fmt.Print(prompt)
	got := make(chan string, 1)
	go func() {
		var line []byte
		b := make([]byte, 1)
		for {
			n, err := promptIn.Read(b)
			if n == 1 {
				if b[0] == '\n' {
					break
				}
				line = append(line, b[0])
			}
			if err != nil {
				break
			}
		}
		got <- strings.TrimSpace(string(line))
	}()
	select {
	case line := <-got:
		return line, true
	case <-promptCancel:
		fmt.Println()
		return "", false
	}
}

// playConfirmations are what must be typed before a recipe runs: each
// distinct Confirm of its risks, in order.
func playConfirmations(res *play.Result) []play.Risk {
	var out []play.Risk
	seen := map[string]bool{}
	for _, r := range res.Risks {
		if r.Confirm == "" || seen[r.Code+"\x00"+r.Confirm] {
			continue
		}
		seen[r.Code+"\x00"+r.Confirm] = true
		out = append(out, r)
	}
	return out
}

// playConfirm asks for the yes and each typed confirmation, or, without a
// terminal, takes them from --yes, --trust-endpoint and --trust-secret.
// --yes never confirms what must be typed.
func playConfirm(res *play.Result, interactive bool) error {
	return playConfirmAsk(res, interactive, "\nRun this playbook? [y/N] ")
}

// playConfirmAsk is playConfirm with the yes question given (--keep and
// --update ask their own).
func playConfirmAsk(res *play.Result, interactive bool, question string) error {
	typed := playConfirmations(res)
	if !interactive {
		if !playYes {
			return errors.New("not on a terminal: confirm with --yes after reviewing the plan (cpb play <ref> --dry-run); nothing was written")
		}
		trusted := map[string]bool{}
		for _, h := range playTrustEndpoint {
			trusted["e\x00"+h] = true
		}
		for _, s := range playTrustSecret {
			trusted["s\x00"+s] = true
		}
		var missing []string
		for _, r := range typed {
			if r.Code == play.RiskUsesSecret {
				if !trusted["s\x00"+r.Confirm] {
					missing = append(missing, "--trust-secret "+r.Confirm)
				}
			} else if !trusted["e\x00"+r.Confirm] {
				missing = append(missing, "--trust-endpoint "+r.Confirm)
			}
		}
		if len(missing) > 0 {
			return fmt.Errorf("--yes does not confirm these: name each one, %s; nothing was written", strings.Join(missing, ", "))
		}
		return nil
	}
	if !playYes {
		a, ok := promptLine(question)
		if !ok {
			return errPlayCancelled
		}
		if a = strings.ToLower(a); a != "y" && a != "yes" {
			return errors.New("not confirmed; nothing was written")
		}
	}
	for _, r := range typed {
		var q string
		switch r.Code {
		case play.RiskUsesSecret:
			// A recipe with references never runs sandboxed: the reader is
			// this machine, as you, and the prompt says so where it is typed.
			q = fmt.Sprintf("Type the secret this playbook may read, on this machine, as you (%s): ", r.Confirm)
		case play.RiskTLSOrProxy:
			if r.Confirm == "TLS" {
				q = "Type TLS to let this playbook change which certificates are trusted: "
			} else {
				q = fmt.Sprintf("Type the proxy every request will go through (%s): ", r.Confirm)
			}
		default:
			q = fmt.Sprintf("Type the host this playbook will send your requests to (%s): ", r.Confirm)
		}
		a, ok := promptLine(q)
		if !ok {
			return errPlayCancelled
		}
		if a != r.Confirm {
			return fmt.Errorf("%s was not confirmed; nothing was written", r.Confirm)
		}
	}
	return nil
}

// sweepStalePlays removes throwaway stores a killed cpb left behind: its
// marker names a cpb that is gone, and it is older than playSweepAge. A
// store whose cpb is alive, or that has no marker, is never touched.
func sweepStalePlays() {
	dirs, _ := filepath.Glob(filepath.Join(os.TempDir(), "cpb-play-*"))
	for _, d := range dirs {
		info, err := os.Stat(filepath.Join(d, playMarker))
		if err != nil || time.Since(info.ModTime()) < playSweepAge {
			continue
		}
		data, err := os.ReadFile(filepath.Join(d, playMarker))
		if err != nil {
			continue
		}
		// "pid=<cpb> [child=<session>]": both must be gone, since a session
		// can outlive a killed cpb.
		alive, parsed := false, false
		for _, f := range strings.Fields(string(data)) {
			k, v, _ := strings.Cut(f, "=")
			if pid, err := strconv.Atoi(v); err == nil && (k == "pid" || k == "child") {
				parsed = true
				alive = alive || pidAlive(pid)
			}
		}
		if !parsed || alive {
			continue
		}
		_ = os.RemoveAll(d)
	}
}

// pidAlive reports a live process (signal 0 checks without sending).
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// copyEnvSets copies the --env sets from the user's store into the
// throwaway one, so the played playbook can USE them; it returns the keys
// they set, which the credential BLOCK must leave alone.
func copyEnvSets(userStore, store string) (map[string]bool, error) {
	keys := map[string]bool{}
	for _, name := range playEnvSets {
		p, err := envprofile.Read(envprofile.Dir(userStore), name)
		if err != nil {
			return nil, err
		}
		if p == nil {
			return nil, fmt.Errorf("--env %s: no such env set (SHOW ENVS)", name)
		}
		if err := envprofile.Write(envprofile.Dir(store), p); err != nil {
			return nil, err
		}
		e := p.Env()
		for k := range e.Set {
			keys[k] = true
		}
		for k := range e.Refs {
			keys[k] = true
		}
	}
	return keys, nil
}

// playRun runs a played recipe: preview, confirm, apply, run, clean up.
func playRun(src *play.Source, rec *play.Recipe, res *play.Result, claudeArgs []string) error {
	sweepStalePlays()
	interactive := isTerminal(os.Stdin) && isTerminal(os.Stdout)
	name := playName(src)
	if len(res.Refused) > 0 {
		printPlayCheck(os.Stdout, src, rec, res)
		return &commandExitError{code: 1}
	}
	sb, err := choosePlaySandbox(res)
	if err != nil {
		return fmt.Errorf("%v; nothing was written", err)
	}
	if sb.Backend == "" {
		res.Risks = append(res.Risks, play.Risk{Code: play.RiskNoSandbox, Clause: "where it runs", Detail: sb.Note})
	}
	userStore := config.ResolvePlaybooksDir()
	// Installed before the store exists and released after it is gone.
	guard := newPlayGuard()
	defer guard.release()
	promptCancel = guard.cancelled
	defer func() { promptCancel = nil }()
	return withThrowawayStore(func(dir string) error {
		marker := filepath.Join(dir, playMarker)
		if err := os.WriteFile(marker, []byte("pid="+strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
			return err
		}
		keep, err := copyEnvSets(userStore, config.ResolvePlaybooksDir())
		if err != nil {
			return err
		}
		setup, recipe, err := writePlayFiles(dir, src, rec, playSetupFor(name, res, playEnvSets, keep))
		if err != nil {
			return err
		}
		files := []string{setup, recipe}

		// The preview: the recipe, its risks, and the plan of the exact bytes.
		printPlayCheck(os.Stdout, src, rec, res)
		fmt.Println("\nWhat it would do, in a throwaway playbook removed when the session ends:")
		if err := applyRun(&grammar.Stmt{Verb: grammar.Apply, Files: files, Target: name, DryRun: true, Yes: true}, nil); err != nil {
			return err
		}
		fmt.Println("\n" + sb.Note)
		if err := playConfirm(res, interactive); err != nil {
			return err
		}

		if guard.isCancelled() {
			return errPlayCancelled
		}
		// Apply the same bytes for real, in the throwaway store.
		stdout := os.Stdout
		os.Stdout = os.Stderr
		err = applyRun(&grammar.Stmt{Verb: grammar.Apply, Files: files, Target: name, Yes: true}, nil)
		os.Stdout = stdout
		if err != nil {
			return err
		}
		pbDir := filepath.Join(config.ResolvePlaybooksDir(), name)
		defer func() {
			// A shared login that Claude Code refreshed during the session
			// lives in this directory now; heal it back into the machine
			// login before the directory goes, or a rotated refresh token
			// would be lost with it.
			// Only on the shared-login path: an isolated playbook's login
			// (a moved endpoint's, or the recipe's own) never touches the
			// machine's.
			if auth.IsAuthIsolated(pbDir) {
				return
			}
			if err := auth.SyncCredentials(pbDir); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not hand the login back to your machine: %v\n", err)
			}
		}()
		return playSession(guard, marker, name, sb.Backend, claudeArgs)
	})
}

// playGuard keeps cpb alive through every way a play ends, until the
// throwaway store is removed. It is installed before the store exists and
// released after it is gone.
//   - Before the session (the preview, the prompts, the apply), a ^C, TERM or
//     HUP cancels: a waiting prompt returns, the run stops, and the store is
//     removed on the way out.
//   - During the session, ^C reaches claude from the terminal, so cpb only
//     waits. A TERM or HUP is passed to the session's own process, never to
//     a process group: the group can be the caller's (a script, an IDE), and
//     cpb would receive its own signal back.
//   - Signals stay caught through clean-up.
type playGuard struct {
	sigs      chan os.Signal
	done      chan struct{}
	cancelled chan struct{}
	once      sync.Once
	mu        sync.Mutex
	child     *os.Process
}

func newPlayGuard() *playGuard {
	g := &playGuard{sigs: make(chan os.Signal, 8), done: make(chan struct{}), cancelled: make(chan struct{})}
	signal.Notify(g.sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		for {
			select {
			case s := <-g.sigs:
				g.mu.Lock()
				child := g.child
				g.mu.Unlock()
				switch {
				case child == nil:
					g.once.Do(func() { close(g.cancelled) })
				case s == syscall.SIGTERM || s == syscall.SIGHUP:
					_ = child.Signal(s)
				}
			case <-g.done:
				return
			}
		}
	}()
	return g
}

func (g *playGuard) setChild(p *os.Process) {
	g.mu.Lock()
	g.child = p
	g.mu.Unlock()
}

func (g *playGuard) isCancelled() bool {
	select {
	case <-g.cancelled:
		return true
	default:
		return false
	}
}

// release ends the guard after clean-up. TERM and HUP stay ignored for the
// few instructions cpb has left; ^C goes back to its default.
func (g *playGuard) release() {
	close(g.done)
	signal.Ignore(syscall.SIGTERM, syscall.SIGHUP)
	signal.Reset(os.Interrupt)
}

// errPlayCancelled: a signal before the session; nothing ran.
var errPlayCancelled = errors.New("cancelled; nothing ran, and the throwaway playbook is removed")

// playSession runs the session under the guard, telling it the session's
// process, and naming that process in the sweep marker too.
func playSession(g *playGuard, marker, name, backend string, claudeArgs []string) error {
	if g.isCancelled() {
		return errPlayCancelled
	}
	playSessionRunning = true
	onLaunch = func(p *os.Process) {
		g.setChild(p)
		_ = os.WriteFile(marker, []byte("pid="+strconv.Itoa(os.Getpid())+" child="+strconv.Itoa(p.Pid)+"\n"), 0o600)
	}
	defer func() { playSessionRunning, onLaunch = false, nil; g.setChild(nil) }()
	args := append([]string{name}, claudeArgs...)
	if backend == "" {
		return runRun(nil, args)
	}
	// The typed confirmations ran before this point: no sandbox exists
	// until a recipe that moves the endpoint, or asks for a secret, has
	// been confirmed (the sandbox's proxy injects a key for whatever host
	// the endpoint names, which is the threat the confirmation guards).
	defer removeSandbox(backend, sandboxName(name))
	return runRun(nil, append([]string{"--sandbox=" + backend}, args...))
}

// playSessionRunning: run is launching a played playbook, whose resume line
// would name a playbook that is about to be removed.
var playSessionRunning bool

func playName(src *play.Source) string {
	return "play-" + src.Name + "-" + randomHex(3)
}

// writePlayFiles writes the setup statement and the recipe's exact bytes.
// "_" cannot start a recipe's file name (baseName keeps [a-z0-9-]), so the
// two never collide.
func writePlayFiles(dir string, src *play.Source, rec *play.Recipe, setupText string) (string, string, error) {
	setup := filepath.Join(dir, "_play-setup.cpb")
	recipe := filepath.Join(dir, src.Name+".cpb")
	if err := os.WriteFile(setup, []byte(setupText), 0o600); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(recipe, rec.Bytes, 0o600); err != nil {
		return "", "", err
	}
	return setup, recipe, nil
}

// playSetupFor is playSetup with the --env sets attached and their keys
// left out of the credential BLOCK.
func playSetupFor(name string, res *play.Result, envSets []string, keep map[string]bool) string {
	text := playSetupKeeping(name, res, keep)
	if len(envSets) > 0 {
		// In the order given: a later set wins, as for USE ENV anywhere.
		text += fmt.Sprintf("ALTER PLAYBOOK %s USE ENV %s;\n", name, strings.Join(envSets, " "))
	}
	return text
}

var (
	// playSandboxFlag: "" (not given: auto), "auto", or a backend name.
	playSandboxFlag string
	playNoSandbox   bool
)

// playSandboxAvailable reports whether a backend can run here (a test
// replaces it): sbx on PATH, or OpenShell's preflight.
var playSandboxAvailable = func(kind string) error {
	switch kind {
	case "sbx":
		_, err := exec.LookPath("sbx")
		if err != nil {
			return errors.New("'sbx' (Docker Sandboxes) is not installed")
		}
		return nil
	case "openshell":
		_, err := openshellPreflight()
		return err
	}
	return fmt.Errorf("unknown sandbox backend %q", kind)
}

// playSandbox is where a play runs: a backend, or "" for this machine,
// with the sentence the preview says.
type playSandbox struct {
	Backend string `json:"backend"`
	Note    string `json:"note"`
}

// choosePlaySandbox decides where a play runs. The sandbox is the default
// wherever a backend is available (sbx, or OpenShell where its preflight
// passes); --no-sandbox opts out, said plainly; a recipe that asks for a
// sandbox (create-with: SANDBOX) is refused where none is available. A
// recipe with secret references cannot run sandboxed yet (a sandboxed
// launch cannot resolve them), so it is refused there too, never quietly
// run on the host: --no-sandbox runs it on this machine.
func choosePlaySandbox(res *play.Result) (playSandbox, error) {
	wants := res.Header.WantsSandbox()
	if playNoSandbox {
		if playSandboxFlag != "" {
			return playSandbox{}, errors.New("--sandbox and --no-sandbox together: pick one")
		}
		note := "Sandbox off (--no-sandbox): this agent runs on your machine, as you."
		if wants {
			note = "Sandbox off (--no-sandbox), although the recipe asks for one (create-with: SANDBOX): this agent runs on your machine, as you."
		}
		return playSandbox{Note: note}, nil
	}
	var backend string
	switch playSandboxFlag {
	case "", "auto":
		for _, k := range []string{"sbx", "openshell"} {
			if playSandboxAvailable(k) == nil {
				backend = k
				break
			}
		}
	default:
		if err := playSandboxAvailable(playSandboxFlag); err != nil {
			return playSandbox{}, fmt.Errorf("--sandbox=%s: %v", playSandboxFlag, err)
		}
		backend = playSandboxFlag
	}
	if backend == "" {
		if wants {
			return playSandbox{}, errors.New("the recipe asks to run sandboxed (create-with: SANDBOX), and no sandbox is available here (sbx, or OpenShell on Linux): install one, or run it on this machine with --no-sandbox")
		}
		return playSandbox{Note: "No sandbox available here (sbx, or OpenShell on Linux): this agent will run on your machine, as you."}, nil
	}
	var refs []string
	for _, r := range res.Risks {
		if r.Code == play.RiskUsesSecret {
			refs = append(refs, r.Confirm)
		}
	}
	if len(refs) > 0 {
		return playSandbox{}, fmt.Errorf("the recipe uses secret references (%s), which a sandboxed launch cannot resolve yet: run it on this machine with --no-sandbox, and the preview will say so", strings.Join(refs, ", "))
	}
	return playSandbox{Backend: backend, Note: "Sandboxed (" + backend + "): the agent sees this folder and its own playbook, not your home or ~/.claude; your keys stay outside by default. The sandbox is removed when the session ends."}, nil
}

// playSandboxDecision is choosePlaySandbox for a plan: a refusal joins the
// recipe's refusals, and a play that would run on this machine carries the
// no_sandbox risk.
func playSandboxDecision(res *play.Result) *playSandbox {
	sb, err := choosePlaySandbox(res)
	if err != nil {
		res.Refused = append(res.Refused, play.Refusal{What: "the sandbox", Reason: err.Error()})
		return &playSandbox{Note: err.Error()}
	}
	if sb.Backend == "" {
		res.Risks = append(res.Risks, play.Risk{Code: play.RiskNoSandbox, Clause: "where it runs", Detail: sb.Note})
	}
	return &sb
}
