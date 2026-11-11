package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"

	"golang.org/x/term"

	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/play"
)

// The store step (SPEC.md, "cpb play", "Secrets"): when a played recipe's
// secret reference is not stored yet, `cpb play` (and --keep, and cpb update)
// asks the person for its value right after they typed the reference, reads
// it with echo off from their own terminal, and hands it to the helper's
// `--store REF` on stdin. The value never reaches an argument list, cpb's
// output, or a file of cpb's, and no flag, variable or file supplies it.
//
// What the step does not do: hide the value from the person's own
// processes. A program that drives a terminal can pass the terminal test,
// and any process of the same user can ask the OS store for the value.

// refsPendingStore are the references the preview treats as not stored yet:
// checkRefsWith passes them, so the plan can be shown, and the real apply
// (after the store step) checks them again.
var refsPendingStore map[string]bool

// storeAfterConfirm are the references the store step asks for, each right
// after the person typed it (playConfirmAsk); empty in a plan-only run.
var storeAfterConfirm map[string]bool

// secretIn is the terminal the value is read from (a test gives a pty).
var secretIn = os.Stdin

// agentSessionVars mark a session an agent runs: a value typed there would
// go through that agent's own terminal. Set at all, even empty, counts.
var agentSessionVars = []string{"CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT"}

// storeStepRefusal says why the store step cannot run here for ref, or ""
// when it can: both ends are a terminal, no --yes answered for the person,
// no --trust-secret confirmed this reference without them, and no agent
// session is in the environment.
func storeStepRefusal(ref string, interactive, yes bool, trusted []string) string {
	switch {
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

// Only --check's notStoredExit (secret_default.go; cpb's default helper's,
// and the usual one of helpers that tell "missing" from "cannot answer")
// arms the store step. Any other failure, such as a locked or unreachable
// store (unavailableExit), is not a reason to ask for a value: the plan
// refuses it with the helper's message, as before the store step.

// refsNotStored asks the helper which of refs are not stored (--check exits
// notStoredExit), quietly; it returns nil and the helper's error when no
// helper can answer, so the plan reports that as it always has.
func refsNotStored(refs []string) ([]string, error) {
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
		c := x.command("--check", "CPB_SECRET="+r)
		c.Env = withoutKeys(os.Environ(), []string{"CPB_SECRET"})
		var ee *exec.ExitError
		if err := c.Run(); errors.As(err, &ee) && ee.ExitCode() == notStoredExit {
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

// storeAttempts is how many times the store step asks for a value whose
// two entries differ (or that held an escape sequence) before it stops.
const storeAttempts = 3

// storeSecret is the store step for one reference: it names where the
// value goes, reads it twice with echo off from the terminal (a stored
// value is never replaced, so a typo nobody saw would be permanent), hands
// it to the helper's --store on stdin, and checks the reference again.
// Entries that differ, or one with an escape sequence, are asked for again,
// storeAttempts times in all. An empty answer, ^C, a closed terminal, a
// signal the play guard caught (promptCancel) or a helper that fails stops
// the run with nothing stored.
func storeSecret(ref string) error {
	x, err := resolveHelperFor([]string{ref})
	if err != nil {
		return err
	}
	stopped := fmt.Errorf("%s was not stored; nothing was written", ref)
	fmt.Printf("%s is not stored on this machine yet. Its value will be written to %s.\n", ref, storeWhere(x, ref))
	var value []byte
	defer func() { clear(value) }()
	for attempt := 1; ; attempt++ {
		fmt.Print("Type the value now (it is not shown), or press Enter to stop: ")
		first, ok, escaped := readHidden(secretIn, promptCancel)
		if isCancelled(promptCancel) || (!ok && !escaped) || (ok && len(first) == 0) {
			clear(first)
			return stopped
		}
		why := ""
		if escaped {
			why = "a key that sends an escape sequence (an arrow, Delete, Home, a function key or Alt) was pressed, so the value may not be the one you meant"
		} else {
			fmt.Print("Type it again to confirm, or press Enter to stop: ")
			second, ok2, escaped2 := readHidden(secretIn, promptCancel)
			switch {
			case isCancelled(promptCancel) || (!ok2 && !escaped2) || (ok2 && len(second) == 0):
				clear(first)
				clear(second)
				return stopped
			case escaped2:
				why = "a key that sends an escape sequence (an arrow, Delete, Home, a function key or Alt) was pressed, so the value may not be the one you meant"
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
	c := x.command("--store", ref)
	c.Stdin = bytes.NewReader(value)
	c.Stdout, c.Stderr = os.Stderr, os.Stderr
	c.Env = os.Environ()
	if err := c.Run(); err != nil {
		return fmt.Errorf("%s could not be stored (secret helper %s: %v); nothing was written. Store the value with your own tool, then run this again", ref, x.name, err)
	}
	chk := x.command("--check", "CPB_SECRET="+ref)
	chk.Env = withoutKeys(os.Environ(), []string{"CPB_SECRET"})
	if err := chk.Run(); err != nil {
		return fmt.Errorf("%s was stored but does not resolve (secret helper %s: %v); nothing was written", ref, x.name, err)
	}
	fmt.Printf("Stored %s.\n", ref)
	return nil
}

// isCancelled reports whether cancel (nil: never) is closed.
func isCancelled(cancel <-chan struct{}) bool {
	select {
	case <-cancel:
		return true
	default:
		return false
	}
}

// readHidden reads one line from the terminal f in raw mode, echoing
// nothing: Enter ends it; ^C, ^D on an empty line, an error, end of input
// or cancel closing cancel it; and the terminal's mode is restored on every
// way out. In raw mode a typed ^C arrives as a byte, so cancelling is the
// read's own decision; a signal sent to cpb is the play guard's, which
// closes cancel. Each byte is read in a goroutine, and only when asked
// for, so a cancel never waits for a key and nothing is read ahead of the
// next prompt; a read pending at a cancel ends with the run.
//
// A key that sends an escape sequence (an arrow, Delete, Home, a function
// key, Alt with a key) would otherwise leave the sequence's printable bytes
// in the value, which echo-off hides: such a line ends as not ok, with
// escaped true, and nothing of it is kept. Bracketed paste's markers
// (ESC [200~ and ESC [201~) are the one exception: they are dropped and
// the paste between them is kept, so a terminal that has the mode on
// still takes a pasted value.
func readHidden(f *os.File, cancel <-chan struct{}) (value []byte, ok, escaped bool) {
	fd := int(f.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		return nil, false, false
	}
	defer term.Restore(fd, old)
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
	var buf []byte
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
			clear(buf)
			return nil, false, false
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
			clear(buf)
			return nil, false, false
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
			if escaped {
				clear(buf)
				return nil, false, true
			}
			return buf, true, false
		case 3: // ^C
			fmt.Print("^C\r\n")
			clear(buf)
			return nil, false, false
		case 4: // ^D
			if len(buf) == 0 {
				fmt.Print("\r\n")
				return nil, false, false
			}
		case 0x7f, 8: // backspace
			if len(buf) > 0 {
				buf[len(buf)-1] = 0
				buf = buf[:len(buf)-1]
			}
		case 0x15: // ^U
			clear(buf)
			buf = buf[:0]
		default:
			if c >= 0x20 || c == '\t' {
				buf = append(buf, c)
			}
		}
	}
}
