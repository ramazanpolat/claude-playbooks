package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/term"

	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/play"
)

// The store step (SPEC.md, "cpb play", "Secrets"): when a played recipe's
// secret reference is not stored yet, `cpb play` (and --keep, and cpb update)
// asks the person for its value right after they typed the reference, reads
// it twice with echo off from standard input, which must be a terminal, and
// hands it to the helper's `--store REF` on stdin. The value never reaches an
// argument list, cpb's output, or a file of cpb's, and no flag, variable or
// file supplies it.
//
// What the step does not do: hide the value from the person's own
// processes. A program that drives a terminal can pass the terminal test,
// the agent markers are a courtesy check, not a boundary, and any process
// of the same user can ask the OS store for the value.

// refsPendingStore are the references the preview treats as not stored yet:
// checkRefsWith passes them, so the plan can be shown, and the real apply
// (after the store step) checks them again.
var refsPendingStore map[string]bool

// storeAfterConfirm are the references the store step asks for, each right
// after the person typed it (playConfirmAsk); empty in a plan-only run.
var storeAfterConfirm map[string]bool

// secretIn is the terminal the value is read from: standard input, which
// the step requires to be a terminal (a test gives a pty).
var secretIn = os.Stdin

// agentSessionVars mark a session an agent runs: a value typed there would
// go through that agent's own terminal. Set at all, even empty, counts.
// This is a courtesy check, not a boundary: a program that clears them
// passes it.
var agentSessionVars = []string{"CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT"}

// helperTimeout bounds each call cpb makes to a secret helper (--check,
// --store); a helper that does not answer in time fails the step with
// nothing written (a test shortens it).
var helperTimeout = 90 * time.Second

// storeStepRefusal says why the store step cannot run here for ref, or ""
// when it can: both ends are a terminal, no --yes answered for the person,
// no --trust-secret confirmed this reference without them, and no agent
// session is in the environment.
func storeStepRefusal(ref string, interactive, yes bool, trusted []string) string {
	switch {
	case !flushSupported:
		return "cpb cannot discard terminal input on this system, so a paste could reach the shell"
	case !interactive:
		return "not on a terminal"
	case yes:
		return "--yes was given"
	case slices.Contains(trusted, ref):
		return "--trust-secret " + ref + " was given"
	}
	for _, v := range agentSessionVars {
		if _, set := os.LookupEnv(v); set {
			return "this runs inside an agent's session (" + v + " is set)"
		}
	}
	return ""
}

// runHelper runs one helper call bounded by helperTimeout. A call that
// does not end in time is killed, and the error says so.
func runHelper(x helperExe, stdin io.Reader, stdout, stderr io.Writer, env []string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), helperTimeout)
	defer cancel()
	c := exec.CommandContext(ctx, x.path, append(append([]string{}, x.pre...), args...)...)
	c.Stdin, c.Stdout, c.Stderr, c.Env = stdin, stdout, stderr, env
	c.WaitDelay = 2 * time.Second
	err := c.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("it did not answer within %s", helperTimeout)
	}
	return err
}

// helperCheck is `<helper> --check KEY=REF`, quiet unless out is given.
func helperCheck(x helperExe, key, ref string, out io.Writer) error {
	return runHelper(x, nil, out, out, withoutKeys(os.Environ(), []string{key}), "--check", key+"="+ref)
}

// uniqueRefs is refs in order, each once: a reference used twice is one
// secret, stored and asked for once.
func uniqueRefs(refs []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, r := range refs {
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	return out
}

// Only --check's notStoredExit (secret_default.go; cpb's default helper's,
// and the usual one of helpers that tell "missing" from "cannot answer")
// arms the store step. Any other failure, such as a locked or unreachable
// store (unavailableExit), is not a reason to ask for a value: the plan
// refuses it with the helper's message, as before the store step.

// refsNotStored asks the helper which of refs are not stored (--check exits
// notStoredExit), quietly; it returns nil and the helper's error when no
// helper can answer, so the plan reports that as it always has.
func refsNotStored(refs []string) ([]string, error) {
	refs = uniqueRefs(refs)
	if len(refs) == 0 {
		return nil, nil
	}
	x, err := resolveHelperFor(refs)
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, r := range refs {
		if manifest.ValidateCPBRef(r) != nil {
			continue // the plan refuses it with its own message
		}
		var ee *exec.ExitError
		if err := helperCheck(x, "CPB_SECRET", r, nil); errors.As(err, &ee) && ee.ExitCode() == notStoredExit {
			missing = append(missing, r)
		}
	}
	return missing, nil
}

// preparePendingStore is called before a play's plan: it marks the
// references that are not stored yet as pending when the store step can
// run (or the run only plans, which stores nothing), and otherwise leaves
// them, so the plan refuses as before. It returns the pending ones and the
// note the preview prints, and why the step cannot run when it cannot.
func preparePendingStore(refs []string, planOnly, interactive, yes bool, trusted []string) (pending []string, why string) {
	refsPendingStore, storeAfterConfirm = nil, nil
	missing, err := refsNotStored(refs)
	if err != nil || len(missing) == 0 {
		return nil, ""
	}
	for _, r := range missing {
		if w := storeStepRefusal(r, interactive, yes, trusted); w != "" && !planOnly {
			fmt.Fprintf(os.Stderr, "%s is not stored on this machine, and cpb asks for a value only from your own terminal (%s). Run this command there yourself, without --yes or --trust-secret, or store it with your own tool.\n", r, w)
			return nil, w
		}
	}
	refsPendingStore = map[string]bool{}
	for _, r := range missing {
		refsPendingStore[r] = true
	}
	if !planOnly {
		storeAfterConfirm = map[string]bool{}
		for _, r := range missing {
			storeAfterConfirm[r] = true
		}
	}
	return missing, ""
}

// donePendingStore ends the preview's exception: the real apply checks
// every reference.
func donePendingStore() { refsPendingStore = nil }

// printPendingNote names the references the run will ask to store, and
// where each value would be kept.
func printPendingNote(pending []string, planOnly bool) {
	for _, r := range pending {
		where := ""
		if x, err := resolveHelperFor([]string{r}); err == nil {
			where = " It would be kept in " + storeWhere(x, r) + "."
		}
		if planOnly {
			fmt.Printf("Not stored on this machine yet: %s. Run without --dry-run, from your own terminal, and cpb asks for its value.%s\n", r, where)
		} else {
			fmt.Printf("Not stored on this machine yet: %s. You will be asked for its value after you confirm it.%s\n", r, where)
		}
	}
}

// playSecretJSON is one secret reference in `cpb play`'s --check and
// --dry-run JSON (SPEC.md, "cpb play"): whether it is stored on this
// machine (null when the helper could not answer), and where its value is,
// or would be kept: for cpb's default helper the store (keychain,
// secret-service or file) and, for the file store, the file; for a
// configured helper, the helper; "none" when no helper can take it.
type playSecretJSON struct {
	Ref    string `json:"ref"`
	Stored *bool  `json:"stored"`
	Store  string `json:"store"`
	Path   string `json:"path,omitempty"`
	Helper string `json:"helper,omitempty"`
	Note   string `json:"note,omitempty"`
}

// blockRefs are the secret references a play block's risks name.
func blockRefs(block *playJSON) []string {
	var refs []string
	for _, r := range block.Risks {
		if r.Code == play.RiskUsesSecret {
			refs = append(refs, r.Confirm)
		}
	}
	return refs
}

// secretReport answers, for each reference, presence through the helper's
// --check and the store from cpb's default store (target), which reads no
// value. Nothing here can print or hold a value.
func secretReport(refs []string) []playSecretJSON {
	var out []playSecretJSON
	for _, r := range refs {
		e := playSecretJSON{Ref: r, Store: "none"}
		if err := manifest.ValidateCPBRef(r); err != nil {
			e.Note = err.Error()
			out = append(out, e)
			continue
		}
		x, err := resolveHelperFor([]string{r})
		if err != nil {
			e.Note = err.Error()
			out = append(out, e)
			continue
		}
		if x.def {
			if st, err := defaultSecretStore(); err == nil {
				e.Store, e.Path = st.target(strings.TrimPrefix(r, manifest.CPBRefPrefix))
			}
		} else {
			e.Store, e.Helper = "helper", x.name
		}
		// Bounded like every other check (helperCheck): a helper that does
		// not answer leaves "stored" unknown, never the report waiting.
		var ee *exec.ExitError
		switch err := helperCheck(x, "CPB_SECRET", r, nil); {
		case err == nil:
			e.Stored = new(bool)
			*e.Stored = true
		case errors.As(err, &ee) && ee.ExitCode() == notStoredExit:
			e.Stored = new(bool)
		default:
			e.Note = "the helper could not say whether it is stored (" + err.Error() + ")"
		}
		out = append(out, e)
	}
	return out
}

// printSecretReport is secretReport for a person, in --check's text report.
func printSecretReport(report []playSecretJSON) {
	for _, e := range report {
		state := "not known whether stored here: " + e.Note
		switch {
		case e.Store == "none":
			state = "no secret helper can take it here: " + e.Note
		case e.Stored != nil && *e.Stored:
			state = "stored here"
		case e.Stored != nil:
			state = "not stored here yet"
		}
		where := ""
		switch e.Store {
		case storeFile:
			where = "; kept in the file " + e.Path + " (readable by you and root: no keyring)"
		case storeSecretService:
			where = "; kept in your Secret Service keyring"
		case storeKeychain:
			where = "; kept in your macOS Keychain"
		case "helper":
			where = "; kept by your secret helper " + e.Helper
		}
		fmt.Printf("Secret:  %s: %s%s\n", e.Ref, state, where)
	}
}

// storeAttempts is how many times the store step asks for a value that it
// could not take (entries that differ, or a line it refused) before it stops.
const storeAttempts = 3

// storeLimit is the longest value the step reads for x: the default
// helper's store's own limit (each store keeps only so many bytes whole),
// else the bound every helper's --store reads.
func storeLimit(x helperExe) int {
	if x.def {
		if st, err := defaultSecretStore(); err == nil {
			return st.maxValue()
		}
	}
	return maxSecretBytes
}

// storeSecret is the store step for one reference: it names where the
// value goes, reads it twice with echo off from the terminal (a stored
// value is never replaced, so a typo nobody saw would be permanent), hands
// it to the helper's --store on stdin, and checks the reference again.
// Entries that differ, or a line the read refused (hiddenResult), are
// asked for again, storeAttempts times in all. An empty answer, ^C, a
// closed terminal, a signal, or a helper that fails or does not answer
// stops the run with nothing stored.
//
// When no guard is in place (cpb play installs one; --keep and cpb update
// do not), the step installs its own for the reads, so a signal ends the
// read with the terminal restored instead of leaving it raw.
func storeSecret(ref string) (err error) {
	x, err := resolveHelperFor([]string{ref})
	if err != nil {
		return err
	}
	if promptCancel == nil {
		g := newPlayGuard()
		promptCancel = g.cancelled
		defer func() {
			// A signal after the step's last check is still the step's,
			// also one that reached the guard just as it stopped: it
			// stops the run here rather than being dropped.
			signalled := g.stop()
			if err == nil && (signalled || isCancelled(promptCancel)) {
				err = fmt.Errorf("cancelled: %s was stored before the signal was handled; nothing else was written", ref)
			}
			promptCancel = nil
		}()
	}
	if storeStepReading != nil {
		storeStepReading()
	}
	limit := storeLimit(x)
	stopped := fmt.Errorf("%s was not stored; nothing was written", ref)
	notQuiet := fmt.Errorf("%s was not stored: the step was stopped while input was still arriving on the terminal (a paste still running?), and what arrives from now on may be shown and reach your shell; nothing was written. Run this again once it has stopped", ref)
	undiscarded := fmt.Errorf("%s was not stored: this terminal's waiting input could not be discarded, so what was typed or pasted past the value could be shown or reach your shell; nothing was written. Use another terminal, or store the value with your own tool", ref)
	fmt.Printf("%s is not stored on this machine yet. Its value will be written to %s.\n", ref, storeWhere(x, ref))
	if x.def {
		// Before anyone types a value, the store is asked whether it can
		// take one, with nothing that carries a value: a Keychain locked
		// where it cannot ask for its password stops the step here, and
		// one that can ask brings up its unlock dialog now.
		// It may take a while (an unlock dialog waits for its answer), so
		// the person is told, and the terminal's echo is off meanwhile:
		// what is typed then is not shown, and is discarded before the
		// first prompt rather than read as the value.
		fmt.Println("Asking the store whether it can take a value (it may ask for its own password first). Type nothing here until you are asked.")
		fd := int(secretIn.Fd())
		restoreEcho := echoOff(fd)
		rerr := runHelper(x, nil, os.Stderr, os.Stderr, os.Environ(), "--ready", ref)
		typed, perr := pendingCount(fd)
		flushTerminal(fd)
		restoreEcho()
		if perr == nil && typed > 0 {
			fmt.Println("What was typed meanwhile was discarded, unseen.")
		}
		var re *exec.ExitError
		switch {
		case isCancelled(promptCancel):
			return stopped
		case errors.As(rerr, &re) && re.ExitCode() == unavailableExit:
			return fmt.Errorf("%s was not stored: the store cannot take a value now (the reason is above); nothing was asked for or written", ref)
		case rerr != nil:
			return fmt.Errorf("%s was not stored: the store could not be asked whether it can take a value (secret helper %s: %v); nothing was asked for or written", ref, x.name, rerr)
		}
	}
	var value []byte
	defer func() { clear(value) }()
	for attempt := 1; ; attempt++ {
		fmt.Print("Type the value now (it is not shown), or press Enter to stop: ")
		first, res := readHidden(secretIn, promptCancel, limit)
		switch res {
		case hiddenNotQuiet:
			return notQuiet
		case hiddenUndiscarded:
			return undiscarded
		}
		if isCancelled(promptCancel) || res == hiddenStopped || (res == hiddenOK && len(first) == 0) {
			clear(first)
			return stopped
		}
		why := res.why(limit)
		if res == hiddenOK {
			fmt.Print("Type it again to confirm, or press Enter to stop: ")
			second, res2 := readHidden(secretIn, promptCancel, limit)
			switch {
			case res2 == hiddenNotQuiet:
				clear(first)
				return notQuiet
			case res2 == hiddenUndiscarded:
				clear(first)
				return undiscarded
			case isCancelled(promptCancel) || res2 == hiddenStopped || (res2 == hiddenOK && len(second) == 0):
				clear(first)
				clear(second)
				return stopped
			case res2 != hiddenOK:
				why = res2.why(limit)
			case !bytes.Equal(first, second):
				why = "the two entries differ"
			}
			clear(second)
		}
		if why == "" {
			value = first
			break
		}
		clear(first)
		if attempt == storeAttempts {
			return fmt.Errorf("%s was not stored after %d attempts (the last: %s); nothing was written. Store the value with your own tool, then run this again", ref, storeAttempts, why)
		}
		fmt.Printf("Not stored: %s. Try again (%d of %d).\n", why, attempt+1, storeAttempts)
	}
	// A signal that arrives while a helper runs is caught by the guard and
	// does not stop the helper; the step checks for it after each call, so
	// the run stops there and says how far it got.
	err = runHelper(x, bytes.NewReader(value), os.Stderr, os.Stderr, os.Environ(), "--store", ref)
	var ee *exec.ExitError
	switch {
	case isCancelled(promptCancel) && err == nil:
		return fmt.Errorf("cancelled: %s was stored before the signal was handled; nothing else was written", ref)
	case isCancelled(promptCancel):
		// The signal may have reached the helper too (a ^C goes to the
		// whole foreground group), after its write: ask once. That an
		// item of the name exists says no more than that.
		switch storedNow(x, ref) {
		case refExists:
			return fmt.Errorf("cancelled: an item named %s exists now; nothing else was written. Run this again: cpb checks it before going on", ref)
		case refAbsent:
			return fmt.Errorf("cancelled: %s was not stored; nothing was written", ref)
		}
		return fmt.Errorf("cancelled: %s may be stored: run this again to see; nothing else was written", ref)
	case errors.As(err, &ee) && ee.ExitCode() == unavailableExit:
		// The store did not answer: it may have stored nothing (a lock it
		// did not get, a Keychain it found locked) or the value (a
		// read-back it did not get). Ask once.
		switch storedNow(x, ref) {
		case refExists:
			return fmt.Errorf("%s: the store did not answer (secret helper %s: %v), and an item of that name exists now; nothing else was written. Run this again: cpb checks it before going on", ref, x.name, err)
		case refAbsent:
			return fmt.Errorf("%s was not stored: the store did not answer (secret helper %s: %v); nothing was written. Run this again once it can", ref, x.name, err)
		}
		return fmt.Errorf("%s: the store did not answer (secret helper %s: %v), and the value may be stored: run this again to see; nothing else was written", ref, x.name, err)
	case err != nil:
		return fmt.Errorf("%s could not be stored (secret helper %s: %v); nothing was written. Store the value with your own tool, then run this again", ref, x.name, err)
	}
	err = helperCheck(x, "CPB_SECRET", ref, nil)
	switch {
	case isCancelled(promptCancel):
		return fmt.Errorf("cancelled: %s was stored before the signal was handled; nothing else was written", ref)
	case err != nil:
		return fmt.Errorf("%s was stored but does not resolve (secret helper %s: %v); nothing was written", ref, x.name, err)
	}
	if storeStepChecked != nil {
		storeStepChecked()
	}
	fmt.Printf("Stored %s.\n", ref)
	return nil
}

// What one --check after an unfinished store says.
const (
	refUnknown = iota // the check did not answer either
	refExists         // an item of that name exists: no more is known
	refAbsent         // not stored (notStoredExit)
)

// storedNow asks the helper once whether ref is stored.
func storedNow(x helperExe, ref string) int {
	var ce *exec.ExitError
	switch cerr := helperCheck(x, "CPB_SECRET", ref, nil); {
	case cerr == nil:
		return refExists
	case errors.As(cerr, &ce) && ce.ExitCode() == notStoredExit:
		return refAbsent
	}
	return refUnknown
}

// storeStepChecked, when set, runs after the step's last check (a test
// sends a signal there).
var storeStepChecked func()

// storeStepReading, when set, runs as the step is about to read the value
// (a test checks the guard is in place by then).
var storeStepReading func()

// isCancelled reports whether cancel (nil: never) is closed.
func isCancelled(cancel <-chan struct{}) bool {
	select {
	case <-cancel:
		return true
	default:
		return false
	}
}

// The drain's pace (a test changes it): the terminal counts as quiet once
// nothing has come for drainQuiet, looked at every drainPoll; after
// drainMax of input that never pauses that long, the person is told once,
// and the drain goes on.
var (
	drainPoll  = 25 * time.Millisecond
	drainQuiet = 300 * time.Millisecond
	drainMax   = 10 * time.Second
)

// flushTerminal is flushInput, and pendingCount pendingInput (a test makes
// them fail).
var (
	flushTerminal = flushInput
	pendingCount  = pendingInput
)

// drainEnd is how drainInput ended.
type drainEnd int

const (
	drainWentQuiet       drainEnd = iota // nothing came for drainQuiet
	drainStopped                         // a ^C among the input
	drainStoppedArriving                 // a ^C after drainMax, input still arriving
	drainCancelled                       // a signal: cancel closed
)

// drainInput runs after every hidden read, echo still off: it takes the
// input waiting on the terminal f, keeping none of it, until nothing has
// come for drainQuiet. It always looks before it discards: where the
// waiting count can be read, it reads those bytes (so the read never
// blocks) and more reports anything in them but line ends, so a paste of
// several lines, or keys typed right after Enter, are seen even on a
// terminal without bracketed paste; a ^C among them ends the drain. Only
// where the count cannot be read does it discard unseen, once per poll,
// until drainQuiet has passed. After drainMax it says once that input is
// still arriving, and goes on until it stops, a ^C or a signal. A signal
// (cancel) ends it at once, with one discard and no read: a read of the
// key reader may still be pending then, and the drain must not take a byte
// from under it. A ^C or a signal while other input was arriving (after
// the notice, among taken input, or waiting at a signal) ends it as
// drainStoppedArriving, whose wording warns that what comes after may be
// shown. err is a terminal whose input cannot be discarded where its
// waiting count cannot be read either.
func drainInput(f *os.File, cancel <-chan struct{}) (end drainEnd, more bool, err error) {
	fd := int(f.Fd())
	start := time.Now()
	since := start
	told := false
	look := func(n int) (bool, error) {
		other, intr, rerr := takeWaiting(f, n)
		more = more || other
		if rerr != nil {
			return false, flushTerminal(fd)
		}
		return intr, nil
	}
	// cancelled ends the drain on a signal: the warning wording when input
	// was arriving (taken already, or waiting, which is counted, not read).
	cancelled := func() (drainEnd, bool, error) {
		n, perr := pendingCount(fd)
		ferr := flushTerminal(fd)
		if told || more || (perr == nil && n > 0) {
			return drainStoppedArriving, more, ferr
		}
		return drainCancelled, more, ferr
	}
	// stopped ends it on a ^C, with the warning wording when other input
	// came too.
	stopped := func() (drainEnd, bool, error) {
		if told || more {
			return drainStoppedArriving, more, flushTerminal(fd)
		}
		return drainStopped, more, flushTerminal(fd)
	}
	if isCancelled(cancel) {
		return cancelled()
	}
	for {
		if isCancelled(cancel) {
			return cancelled()
		}
		if !told && time.Since(start) >= drainMax {
			told = true
			fmt.Print("Input is still arriving on the terminal; cpb takes it, unseen, until it stops. Press Ctrl-C to stop now (what comes after that may be shown).\r\n")
		}
		// Look before discarding, every time: input is read and looked at
		// where the waiting count can be read, and discarded unseen only
		// where it cannot.
		n, perr := pendingCount(fd)
		switch {
		case perr != nil:
			if err := flushTerminal(fd); err != nil {
				return drainWentQuiet, more, err
			}
		case n > 0:
			intr, err := look(n)
			if err != nil {
				return drainWentQuiet, more, err
			}
			if intr {
				return stopped()
			}
			since = time.Now()
			continue
		}
		if time.Since(since) >= drainQuiet {
			return drainWentQuiet, more, nil
		}
		time.Sleep(drainPoll)
	}
}

// takeWaiting reads the n bytes waiting on f, keeps none of them, and
// reports whether they held anything but line ends, and a ^C.
func takeWaiting(f *os.File, n int) (other, interrupt bool, err error) {
	buf := make([]byte, min(n, 4096))
	defer clear(buf)
	for n > 0 {
		m, err := f.Read(buf[:min(n, len(buf))])
		for _, c := range buf[:m] {
			switch c {
			case '\r', '\n':
			case 3:
				interrupt = true
			default:
				other = true
			}
		}
		clear(buf[:m])
		if err != nil {
			return other, interrupt, err
		}
		if m == 0 {
			break
		}
		n -= m
	}
	return other, interrupt, nil
}

// pasteStall is how long a paste that has not ended (no end marker) may
// pause after a line end before the read says so (a test shortens it).
var pasteStall = time.Second

// Bracketed paste: while it is on, the terminal wraps whatever is pasted
// in ESC [200~ … ESC [201~, so the read knows where a paste ends whatever
// its gaps. Terminals without it ignore the switch.
const (
	bracketedPasteOn  = "\x1b[?2004h"
	bracketedPasteOff = "\x1b[?2004l"
)

// hiddenResult is how a hidden read ended.
type hiddenResult int

const (
	// hiddenOK: Enter, with the line.
	hiddenOK hiddenResult = iota
	// hiddenStopped: ^C (also one typed just after the line), ^D on an
	// empty line, end of input, an error, or a cancel.
	hiddenStopped
	// hiddenEscaped: a key that sends an escape sequence was pressed.
	hiddenEscaped
	// hiddenControl: a control key the read does not edit with was pressed.
	hiddenControl
	// hiddenTooLong: the line was longer than the limit.
	hiddenTooLong
	// hiddenMultiLine: a paste of more than one line.
	hiddenMultiLine
	// hiddenMoreInput: more than line ends followed the line (a paste of
	// several lines where the terminal has no bracketed paste, or keys
	// typed just after Enter).
	hiddenMoreInput
	// hiddenNotQuiet: a ^C or a signal while input was still arriving.
	hiddenNotQuiet
	// hiddenUndiscarded: the terminal's input cannot be discarded.
	hiddenUndiscarded
)

// why is the reason a refused line was not taken, for the person; "" for
// hiddenOK, hiddenStopped, hiddenNotQuiet and hiddenUndiscarded.
func (r hiddenResult) why(limit int) string {
	switch r {
	case hiddenEscaped:
		return "a key that sends an escape sequence (an arrow, Delete, Home, a function key or Alt) was pressed, so the value may not be the one you meant"
	case hiddenControl:
		return "a control key (Ctrl with a letter) was pressed, so the value may not be the one you meant"
	case hiddenTooLong:
		return fmt.Sprintf("the value is longer than the %d bytes this store keeps", limit)
	case hiddenMultiLine:
		return "the paste held more than one line, and a value is one line"
	case hiddenMoreInput:
		return "more input followed the line (a paste of several lines, or keys typed right after Enter), and a value is one line"
	}
	return ""
}

// readHidden reads one line from the terminal f in raw mode, echoing
// nothing, keeping at most limit bytes: Enter ends it; ^C, ^D on an empty
// line, an error, end of input or cancel closing stop it. In raw mode a
// typed ^C arrives as a byte, so stopping is the read's own decision; a
// signal sent to cpb is the guard's, which closes cancel. Each byte is read
// in a goroutine, and only when asked for, so a cancel never waits for a
// key; a read pending at a cancel ends with the run.
//
// Pastes: the read turns bracketed paste on, so a paste is read through
// its end marker, gaps and all; a paste of more than one line is refused,
// a single line with its newline is taken. A paste whose end marker has
// not come pasteStall after a line end is named, with how to stop. On
// every way out, while echo is still off, the terminal's waiting input is
// taken until it has been quiet (drainInput), and only then is its mode
// restored: so what was typed or pasted past the line is never shown,
// never answers the next prompt and never reaches the shell, unless the
// person stops the drain with ^C while input still arrives
// (hiddenNotQuiet). A line followed by more than line ends is refused
// (hiddenMoreInput); a terminal whose input cannot be discarded ends the
// read as hiddenUndiscarded.
//
// Editing: Backspace removes the last character (all of its bytes), ^U
// the whole line; inside a paste they, and ^D, are refused as control
// keys, since what they removed is not seen. A line is refused, and
// nothing of it kept, when a key
// that sends an escape sequence (an arrow, Delete, Home, a function key,
// Alt with a key) or any other control key was pressed, since echo-off
// would hide what it added or dropped, or when it is longer than limit.
func readHidden(f *os.File, cancel <-chan struct{}, limit int) (value []byte, res hiddenResult) {
	fd := int(f.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		return nil, hiddenStopped
	}
	io.WriteString(f, bracketedPasteOn)
	defer func() {
		io.WriteString(f, bracketedPasteOff)
		end, more, derr := drainInput(f, cancel)
		term.Restore(fd, old)
		flushTerminal(fd)
		switch {
		case derr != nil:
			res = hiddenUndiscarded
		case end == drainStoppedArriving:
			res = hiddenNotQuiet
		case end == drainStopped || end == drainCancelled:
			res = hiddenStopped
		case res == hiddenOK && more:
			res = hiddenMoreInput
		default:
			return
		}
		clear(value)
		value = nil
	}()
	type key struct {
		c   byte
		err error
	}
	want, keys, done := make(chan struct{}), make(chan key), make(chan struct{})
	defer close(done)
	go func() {
		b := make([]byte, 1)
		for {
			select {
			case <-want:
			case <-done:
				return
			}
			var k key
			for {
				n, err := f.Read(b)
				if err != nil {
					k = key{err: err}
					break
				}
				if n == 1 {
					k = key{c: b[0]}
					break
				}
			}
			select {
			case keys <- k:
			case <-done:
				return
			}
		}
	}()
	// One backing array of the limit's size, so no copy of the value is
	// left behind by a growing slice.
	buf := make([]byte, 0, min(limit, maxSecretBytes))
	stop := func() ([]byte, hiddenResult) {
		clear(buf[:cap(buf)])
		return nil, hiddenStopped
	}
	var escaped, control, tooLong bool
	// The paste state: inPaste between the markers; lineDone once a line
	// ended inside the paste, which is then read to its end marker; and
	// multiLine once anything but line ends followed that line.
	var inPaste, lineDone, multiLine bool
	// hinted: the stalled-paste hint was shown (once per read).
	var hinted bool
	finish := func() ([]byte, hiddenResult) {
		fmt.Print("\r\n")
		switch {
		case escaped:
			stop()
			return nil, hiddenEscaped
		case control:
			stop()
			return nil, hiddenControl
		case tooLong:
			stop()
			return nil, hiddenTooLong
		case multiLine:
			stop()
			return nil, hiddenMultiLine
		}
		return buf, hiddenOK
	}
	// The escape-sequence state: escNone, or inside one after ESC, after
	// ESC [ (a CSI sequence, its parameter bytes in seq), or after ESC O.
	const (
		escNone = iota
		escStart
		escCSI
		escSS3
	)
	esc := escNone
	var seq []byte
	for {
		if isCancelled(cancel) {
			fmt.Print("\r\n")
			return stop()
		}
		var k key
		select {
		case want <- struct{}{}:
		case <-cancel:
			continue
		}
		var stall <-chan time.Time
		var stallTimer *time.Timer
		if inPaste && lineDone && !hinted {
			stallTimer = time.NewTimer(pasteStall)
			stall = stallTimer.C
		}
		got := false
		for !got {
			select {
			case k = <-keys:
				got = true
			case <-cancel:
				fmt.Print("\r\n")
				return stop()
			case <-stall:
				stall, hinted = nil, true
				fmt.Print("\r\nThe paste has not ended: the terminal did not send its end. Press Ctrl-C to stop, then try again.\r\n")
			}
		}
		if stallTimer != nil {
			stallTimer.Stop()
		}
		if k.err != nil {
			return stop()
		}
		c := k.c
		if c == 0x1b {
			if esc != escNone {
				escaped = true
			}
			esc = escStart
			continue
		}
		if esc != escNone {
			if c < 0x20 {
				// A control key inside a sequence ends it, and is
				// handled as itself below.
				escaped, esc = true, escNone
			} else {
				switch esc {
				case escStart:
					switch c {
					case '[':
						esc, seq = escCSI, seq[:0]
					case 'O':
						esc = escSS3
					default: // Alt with a key
						escaped, esc = true, escNone
					}
				case escCSI:
					if c >= 0x40 && c <= 0x7e { // the final byte
						switch {
						case c == '~' && string(seq) == "200":
							inPaste = true
						case c == '~' && string(seq) == "201":
							inPaste = false
							if lineDone {
								return finish()
							}
						default:
							escaped = true
						}
						esc = escNone
					} else if len(seq) < 8 {
						seq = append(seq, c)
					} else {
						escaped = true
					}
				case escSS3:
					escaped, esc = true, escNone
				}
				continue
			}
		}
		if lineDone {
			// Inside a paste, past its first line: read to the end marker,
			// keeping nothing. Anything but more line ends makes it a
			// paste of several lines.
			if c == 3 { // ^C
				fmt.Print("^C\r\n")
				return stop()
			}
			if c != '\r' && c != '\n' {
				multiLine = true
			}
			continue
		}
		switch c {
		case '\r', '\n':
			if inPaste {
				lineDone = true
				continue
			}
			return finish()
		case 3: // ^C
			fmt.Print("^C\r\n")
			return stop()
		case 4: // ^D: on an empty line it stops; inside a line it is refused
			if len(buf) == 0 && !escaped && !control && !tooLong && !inPaste {
				fmt.Print("\r\n")
				return stop()
			}
			control = true
		case 0x7f, 8: // Backspace: the last character, all of its bytes
			if inPaste {
				control = true
				break
			}
			if _, size := utf8.DecodeLastRune(buf); size > 0 {
				clear(buf[len(buf)-size:])
				buf = buf[:len(buf)-size]
			}
		case 0x15: // ^U
			if inPaste {
				control = true
				break
			}
			clear(buf)
			buf = buf[:0]
		case '\t':
			if len(buf) < limit {
				buf = append(buf, c)
			} else {
				tooLong = true
			}
		default:
			switch {
			case c < 0x20:
				control = true
			case len(buf) < limit:
				buf = append(buf, c)
			default:
				tooLong = true
			}
		}
	}
}
