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
	"time"
	"unicode/utf8"

	"golang.org/x/term"

	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
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

// printPendingNote names the references the run will ask to store.
func printPendingNote(pending []string, planOnly bool) {
	for _, r := range pending {
		if planOnly {
			fmt.Printf("Not stored on this machine yet: %s. Run without --dry-run, from your own terminal, and cpb asks for its value.\n", r)
		} else {
			fmt.Printf("Not stored on this machine yet: %s. You will be asked for its value after you confirm it.\n", r)
		}
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
			// A signal after the step's last check is still the step's:
			// it stops the run here rather than being dropped.
			if err == nil && isCancelled(promptCancel) {
				err = fmt.Errorf("cancelled: %s was stored before the signal was handled; nothing else was written", ref)
			}
			promptCancel = nil
			g.stop()
		}()
	}
	if storeStepReading != nil {
		storeStepReading()
	}
	limit := storeLimit(x)
	stopped := fmt.Errorf("%s was not stored; nothing was written", ref)
	notQuiet := fmt.Errorf("%s was not stored: input kept arriving on the terminal after the value (a paste still running?), and cpb shows none of it; nothing was written. Run this again once it has stopped", ref)
	fmt.Printf("%s is not stored on this machine yet. Its value will be written to %s.\n", ref, storeWhere(x, ref))
	var value []byte
	defer func() { clear(value) }()
	for attempt := 1; ; attempt++ {
		fmt.Print("Type the value now (it is not shown), or press Enter to stop: ")
		first, res := readHidden(secretIn, promptCancel, limit)
		if res == hiddenNotQuiet {
			return notQuiet
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
		// whole foreground group), after its write: ask once.
		var ce *exec.ExitError
		switch cerr := helperCheck(x, "CPB_SECRET", ref, nil); {
		case cerr == nil:
			return fmt.Errorf("cancelled: %s was stored before the signal was handled; nothing else was written", ref)
		case errors.As(cerr, &ce) && ce.ExitCode() == notStoredExit:
			return fmt.Errorf("cancelled: %s was not stored; nothing was written", ref)
		}
		return fmt.Errorf("cancelled: %s may be stored: run this again to see; nothing else was written", ref)
	case errors.As(err, &ee) && ee.ExitCode() == unavailableExit:
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

// The drain's pace (a test changes it): input is discarded at once while
// any is waiting, and the terminal counts as quiet once nothing has come
// for drainQuiet, checked every drainPoll; a stream that never stops for
// that long, within drainMax, is reported as not quiet.
var (
	drainPoll  = 25 * time.Millisecond
	drainQuiet = 300 * time.Millisecond
	drainMax   = 10 * time.Second
)

// flushTerminal is flushInput (a test makes it fail).
var flushTerminal = flushInput

// drainInput discards the terminal's input until it has been quiet for
// drainQuiet: again at once whenever input is waiting, so a long paste is
// discarded as fast as it arrives. It reports whether the terminal went
// quiet within drainMax, and an error when input cannot be discarded at
// all. Where the waiting count cannot be read, it discards once per poll
// until drainQuiet has passed.
func drainInput(fd int) (quiet bool, err error) {
	if err := flushTerminal(fd); err != nil {
		return false, err
	}
	deadline := time.Now().Add(drainMax)
	since := time.Now()
	for time.Now().Before(deadline) {
		// Look before discarding: what arrived since the last look restarts
		// the quiet count. Where the count cannot be read, discard each
		// poll and let the window run.
		n, perr := pendingInput(fd)
		switch {
		case perr != nil:
			flushTerminal(fd)
		case n > 0:
			flushTerminal(fd)
			since = time.Now()
			continue
		}
		if time.Since(since) >= drainQuiet {
			return true, nil
		}
		time.Sleep(drainPoll)
	}
	return false, nil
}

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
	// hiddenStopped: ^C, ^D on an empty line, end of input, an error, a
	// cancel, or a terminal whose input cannot be discarded.
	hiddenStopped
	// hiddenEscaped: a key that sends an escape sequence was pressed.
	hiddenEscaped
	// hiddenControl: a control key the read does not edit with was pressed.
	hiddenControl
	// hiddenTooLong: the line was longer than the limit.
	hiddenTooLong
	// hiddenMultiLine: a paste of more than one line.
	hiddenMultiLine
	// hiddenNotQuiet: input kept arriving after the line, for longer than
	// the drain waits.
	hiddenNotQuiet
)

// why is the reason a refused line was not taken, for the person; "" for
// hiddenOK, hiddenStopped and hiddenNotQuiet.
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
// a single line with its newline is taken. On every way out, while echo is
// still off, the terminal's waiting input is discarded until it has been
// quiet (drainInput), and only then is its mode restored: so what was
// typed or pasted past the line is never shown, never answers the next
// prompt and never reaches the shell. A terminal that never goes quiet
// ends the read as hiddenNotQuiet, one whose input cannot be discarded as
// hiddenStopped.
//
// Editing: Backspace removes the last character (all of its bytes), ^U
// the whole line. A line is refused, and nothing of it kept, when a key
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
		quiet, derr := drainInput(fd)
		term.Restore(fd, old)
		flushTerminal(fd)
		switch {
		case derr != nil:
			clear(value)
			value, res = nil, hiddenStopped
		case !quiet:
			clear(value)
			value, res = nil, hiddenNotQuiet
		}
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
