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
		c := x.command("--check", "CPB_SECRET="+r)
		c.Env = withoutKeys(os.Environ(), []string{"CPB_SECRET"})
		var ee *exec.ExitError
		switch err := c.Run(); {
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
func storeSecret(ref string) error {
	x, err := resolveHelperFor([]string{ref})
	if err != nil {
		return err
	}
	if promptCancel == nil {
		g := newPlayGuard()
		promptCancel = g.cancelled
		defer func() {
			promptCancel = nil
			g.stop()
		}()
	}
	if storeStepReading != nil {
		storeStepReading()
	}
	limit := storeLimit(x)
	stopped := fmt.Errorf("%s was not stored; nothing was written", ref)
	fmt.Printf("%s is not stored on this machine yet. Its value will be written to %s.\n", ref, storeWhere(x, ref))
	var value []byte
	defer func() { clear(value) }()
	for attempt := 1; ; attempt++ {
		fmt.Print("Type the value now (it is not shown), or press Enter to stop: ")
		first, res := readHidden(secretIn, promptCancel, limit)
		if isCancelled(promptCancel) || res == hiddenStopped || (res == hiddenOK && len(first) == 0) {
			clear(first)
			return stopped
		}
		why := res.why(limit)
		if res == hiddenOK {
			fmt.Print("Type it again to confirm, or press Enter to stop: ")
			second, res2 := readHidden(secretIn, promptCancel, limit)
			switch {
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
	switch {
	case isCancelled(promptCancel) && err == nil:
		return fmt.Errorf("cancelled: %s was stored before the signal was handled; nothing else was written", ref)
	case isCancelled(promptCancel):
		return fmt.Errorf("cancelled: %s was not stored; nothing was written", ref)
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
	fmt.Printf("Stored %s.\n", ref)
	return nil
}

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

// The drain's pace: input is discarded every drainPoll until drainQuiet
// polls in a row find nothing waiting, for at most drainMax (a stream that
// never stops, such as a key held down, ends there).
var (
	drainPoll  = 25 * time.Millisecond
	drainQuiet = 3
	drainMax   = 3 * time.Second
)

// drainInput discards the terminal's input until it has been quiet for
// drainQuiet polls. Where the pending count cannot be read, it discards
// once per poll for the same number of polls.
func drainInput(fd int) {
	deadline := time.Now().Add(drainMax)
	for quiet := 0; quiet < drainQuiet && time.Now().Before(deadline); {
		flushInput(fd)
		time.Sleep(drainPoll)
		if n, err := pendingInput(fd); err != nil || n == 0 {
			quiet++
		} else {
			quiet = 0
		}
	}
	flushInput(fd)
}

// hiddenResult is how a hidden read ended.
type hiddenResult int

const (
	// hiddenOK: Enter, with the line.
	hiddenOK hiddenResult = iota
	// hiddenStopped: ^C, ^D on an empty line, end of input, an error, or a
	// cancel.
	hiddenStopped
	// hiddenEscaped: a key that sends an escape sequence was pressed.
	hiddenEscaped
	// hiddenControl: a control key the read does not edit with was pressed.
	hiddenControl
	// hiddenTooLong: the line was longer than the limit.
	hiddenTooLong
)

// why is the reason a refused line was not taken, for the person; "" for
// hiddenOK and hiddenStopped.
func (r hiddenResult) why(limit int) string {
	switch r {
	case hiddenEscaped:
		return "a key that sends an escape sequence (an arrow, Delete, Home, a function key or Alt) was pressed, so the value may not be the one you meant"
	case hiddenControl:
		return "a control key (Ctrl with a letter) was pressed, so the value may not be the one you meant"
	case hiddenTooLong:
		return fmt.Sprintf("the value is longer than the %d bytes this store keeps", limit)
	}
	return ""
}

// readHidden reads one line from the terminal f in raw mode, echoing
// nothing, keeping at most limit bytes: Enter ends it; ^C, ^D on an empty
// line, an error, end of input or cancel closing stop it. On every way out
// the terminal's mode is restored and its pending input is discarded
// (flushInput), so a multi-line paste never feeds the next prompt or, once
// cpb exits, the shell. In raw mode a typed ^C arrives as a byte, so
// stopping is the read's own decision; a signal sent to cpb is the guard's,
// which closes cancel. Each byte is read in a goroutine, and only when
// asked for, so a cancel never waits for a key; a read pending at a cancel
// ends with the run.
//
// Editing: Backspace removes the last character (all of its bytes), ^U
// the whole line. A line is refused, and nothing of it kept, when a key
// that sends an escape sequence (an arrow, Delete, Home, a function key,
// Alt with a key) or any other control key was pressed, since echo-off
// would hide what it added or dropped, or when it is longer than limit.
// Bracketed paste's markers (ESC [200~ and ESC [201~) are not refused:
// they are dropped, and the paste between them is kept.
func readHidden(f *os.File, cancel <-chan struct{}, limit int) ([]byte, hiddenResult) {
	fd := int(f.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		return nil, hiddenStopped
	}
	defer func() {
		// Discard input while echo is still off, until the terminal has
		// been quiet a while: a long paste keeps arriving after a single
		// flush, and once echo is back on it would be shown and could
		// answer the next prompt. Only then is the mode restored, and
		// anything that came in between is discarded too.
		drainInput(fd)
		term.Restore(fd, old)
		flushInput(fd)
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
		select {
		case k = <-keys:
		case <-cancel:
			continue
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
						if c != '~' || (string(seq) != "200" && string(seq) != "201") {
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
		switch c {
		case '\r', '\n':
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
			}
			return buf, hiddenOK
		case 3: // ^C
			fmt.Print("^C\r\n")
			return stop()
		case 4: // ^D: on an empty line it stops; inside a line it is refused
			if len(buf) == 0 && !escaped && !control && !tooLong {
				fmt.Print("\r\n")
				return stop()
			}
			control = true
		case 0x7f, 8: // Backspace: the last character, all of its bytes
			if _, size := utf8.DecodeLastRune(buf); size > 0 {
				clear(buf[len(buf)-size:])
				buf = buf[:len(buf)-size]
			}
		case 0x15: // ^U
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
