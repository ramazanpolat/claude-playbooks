package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
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

// promptLine reads one line, a byte at a time: a buffered reader would
// take the following answers with it, and the next prompt would read
// nothing.
func promptLine(prompt string) string {
	fmt.Print(prompt)
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
	return strings.TrimSpace(string(line))
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
		if a := strings.ToLower(promptLine("\nRun this playbook? [y/N] ")); a != "y" && a != "yes" {
			return errors.New("not confirmed; nothing was written")
		}
	}
	for _, r := range typed {
		var q string
		switch r.Code {
		case play.RiskUsesSecret:
			q = fmt.Sprintf("Type the secret this playbook may read (%s): ", r.Confirm)
		case play.RiskTLSOrProxy:
			if r.Confirm == "TLS" {
				q = "Type TLS to let this playbook change which certificates are trusted: "
			} else {
				q = fmt.Sprintf("Type the proxy every request will go through (%s): ", r.Confirm)
			}
		default:
			q = fmt.Sprintf("Type the host this playbook will send your requests to (%s): ", r.Confirm)
		}
		if promptLine(q) != r.Confirm {
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
		pid, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(string(data)), "pid="))
		if err != nil || pidAlive(pid) {
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
	userStore := config.ResolvePlaybooksDir()
	// Installed before the store exists and released after it is gone.
	guard := newPlayGuard()
	defer guard.release()
	return withThrowawayStore(func(dir string) error {
		if err := os.WriteFile(filepath.Join(dir, playMarker), []byte("pid="+strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
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
		fmt.Println("\nNo sandbox yet: this agent runs on your machine, as you.")
		if err := playConfirm(res, interactive); err != nil {
			return err
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
			if err := auth.SyncCredentials(pbDir); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not hand the login back to your machine: %v\n", err)
			}
		}()
		return playSession(guard, name, claudeArgs)
	})
}

// playGuard keeps cpb alive through every way a session ends, until the
// throwaway store is removed. ^C reaches claude from the terminal (it is in
// cpb's process group), so cpb only waits for it; SIGTERM and SIGHUP are
// passed on to the group while the session is live, for a kill aimed at
// cpb alone. The signals stay caught until clean-up is done: a forwarded
// signal reaches cpb too, and with the default action restored it could
// arrive after the session and end cpb before the store is gone.
type playGuard struct {
	sigs chan os.Signal
	live atomic.Bool
	done chan struct{}
}

func newPlayGuard() *playGuard {
	g := &playGuard{sigs: make(chan os.Signal, 8), done: make(chan struct{})}
	signal.Notify(g.sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		for {
			select {
			case s := <-g.sigs:
				if g.live.Load() && (s == syscall.SIGTERM || s == syscall.SIGHUP) {
					pgid, _ := syscall.Getpgid(0)
					_ = syscall.Kill(-pgid, s.(syscall.Signal))
				}
			case <-g.done:
				return
			}
		}
	}()
	return g
}

// release ends the guard after clean-up. TERM and HUP stay ignored for the
// few instructions cpb has left, so a late forwarded one cannot end it with
// a half-written exit; ^C goes back to its default.
func (g *playGuard) release() {
	close(g.done)
	signal.Ignore(syscall.SIGTERM, syscall.SIGHUP)
	signal.Reset(os.Interrupt)
}

// playSession runs the session under the guard.
func playSession(g *playGuard, name string, claudeArgs []string) error {
	g.live.Store(true)
	defer g.live.Store(false)
	playSessionRunning = true
	defer func() { playSessionRunning = false }()
	return runRun(nil, append([]string{name}, claudeArgs...))
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
