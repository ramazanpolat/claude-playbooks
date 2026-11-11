package cmd

import (
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"

	"github.com/ramazanpolat/claude-playbooks/internal/envset"
	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/play"
)

// storeHelper installs a secret helper with --store: values live as files
// under a temp directory (written whole, through a rename), its argv goes
// to a log, and FAIL_STORE makes --store fail without writing. Returns the
// store directory and the log.
func storeHelper(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	store := filepath.Join(dir, "store")
	if err := os.Mkdir(store, 0o700); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "helper.log")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$HELPER_LOG"
key() { printf '%s' "$1" | tr ':/' '__'; }
case "$1" in
  --check) [ -z "$FORCE_CHECK_FAIL" ] && [ -f "$STORE_DIR/$(key "${2#*=}")" ] && exit 0; exit "${CHECK_EXIT:-4}" ;;
  --store) if [ -n "$SLOW_STORE" ]; then sleep 5; fi
           if [ -n "$FAIL_STORE" ]; then cat >/dev/null; exit 1; fi
           f="$STORE_DIR/$(key "$2")"; cat > "$f.part" && mv "$f.part" "$f"; exit 0 ;;
esac
exit 2
`
	path := filepath.Join(dir, "store-helper")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HELPER_LOG", log)
	t.Setenv("STORE_DIR", store)
	t.Setenv("FAIL_STORE", "")
	t.Setenv("CHECK_EXIT", "")
	t.Setenv("FORCE_CHECK_FAIL", "")
	t.Setenv("SLOW_STORE", "")
	t.Setenv(envset.SecretHelperEnv, path)
	for _, v := range agentSessionVars {
		t.Setenv(v, "") // restored after the test
		os.Unsetenv(v)
	}
	t.Cleanup(func() { refsPendingStore, storeAfterConfirm = nil, nil })
	return store, log
}

// onPty gives the store step a pseudo-terminal: secretIn is its terminal
// end. typeIt types input on the other end once the read is in raw mode
// (and closes that end after, for a terminal that goes away), each call
// after the one before; finish
// restores secretIn and returns what the terminal showed.
func onPty(t *testing.T) (typeIt func(input string, closeAfter bool), finish func() string) {
	t.Helper()
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatalf("pty: %v", err)
	}
	saved := secretIn
	secretIn = tty
	// One writer types each input in the order given, after a pause, so a
	// second entry is typed while the second read waits for it.
	type typed struct {
		input      string
		closeAfter bool
	}
	queue := make(chan typed, 16)
	go func() {
		for in := range queue {
			time.Sleep(300 * time.Millisecond) // the read is in raw mode by now
			io.WriteString(ptmx, in.input)
			if in.closeAfter {
				time.Sleep(100 * time.Millisecond)
				ptmx.Close()
			}
		}
	}()
	typeIt = func(input string, closeAfter bool) { queue <- typed{input, closeAfter} }
	finish = func() string {
		secretIn = saved
		tty.Close()
		b, _ := io.ReadAll(ptmx)
		ptmx.Close()
		return string(b)
	}
	return typeIt, finish
}

func refClauses(key, ref string) []grammar.Clause {
	return []grammar.Clause{{Kind: grammar.SetRef, Vars: []grammar.Var{{Key: key, Ref: ref}}}}
}

const testRef = "vault:test/tok"
const testValue = "s3cr3t-Value-42"

func storedValue(store string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(store, "vault_test_tok"))
	return string(b), err == nil
}

func TestStoreStepStoresThroughStdinOnly(t *testing.T) {
	store, log := storeHelper(t)
	typeIt, finish := onPty(t)
	typeIt(testValue+"\r", false)
	typeIt(testValue+"\r", false)
	var err error
	out := captureStdout(t, func() { err = storeWithin(t, testRef) })
	shown := finish()
	if err != nil {
		t.Fatalf("store step: %v", err)
	}
	if !strings.Contains(out, "Type it again to confirm") {
		t.Fatalf("the value was not asked for twice: %q", out)
	}
	if n := strings.Count(readLog(t, log), "--store"); n != 1 {
		t.Fatalf("--store called %d times, want 1", n)
	}
	if strings.Contains(shown, testValue) {
		t.Fatal("the terminal echoed the value")
	}
	if v, ok := storedValue(store); !ok || v != testValue {
		t.Fatalf("stored %q (%v)", v, ok)
	}
	argv := readLog(t, log)
	if strings.Contains(argv, testValue) {
		t.Fatal("the value reached the helper's argv")
	}
	if !strings.Contains(argv, "--store "+testRef) || !strings.Contains(argv, "--check CPB_SECRET="+testRef) {
		t.Fatalf("the helper was not asked to store and check: %q", argv)
	}
	if strings.Contains(out, testValue) {
		t.Fatal("the value reached cpb's output")
	}
	if !strings.Contains(out, testRef) || !strings.Contains(out, "store-helper") {
		t.Fatalf("the prompt does not name the reference and where it goes: %q", out)
	}
}

func TestStoreStepInterruptedStoresNothing(t *testing.T) {
	for _, tc := range []struct {
		name        string
		inputs      []string
		closeAfter  bool
		failStore   bool
		storeCalled bool
	}{
		{name: "ctrl-c", inputs: []string{"abc\x03"}},
		{name: "empty answer", inputs: []string{"\r"}},
		{name: "ctrl-d", inputs: []string{"\x04"}},
		{name: "terminal closed", inputs: []string{"abc"}, closeAfter: true},
		{name: "ctrl-c at the confirmation", inputs: []string{testValue + "\r", "abc\x03"}},
		{name: "empty confirmation", inputs: []string{testValue + "\r", "\r"}},
		{name: "terminal closed at the confirmation", inputs: []string{testValue + "\r", "abc"}, closeAfter: true},
		{name: "helper fails", inputs: []string{testValue + "\r", testValue + "\r"}, failStore: true, storeCalled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, log := storeHelper(t)
			if tc.failStore {
				t.Setenv("FAIL_STORE", "1")
			}
			typeIt, finish := onPty(t)
			for i, in := range tc.inputs {
				typeIt(in, tc.closeAfter && i == len(tc.inputs)-1)
			}
			var err error
			out := captureStdout(t, func() { err = storeWithin(t, testRef) })
			shown := finish()
			if err == nil || !strings.Contains(err.Error(), "nothing was written") {
				t.Fatalf("store step: %v", err)
			}
			if _, ok := storedValue(store); ok {
				t.Fatal("something was stored")
			}
			entries, _ := os.ReadDir(store)
			if len(entries) != 0 {
				t.Fatalf("a half-written item is left: %v", entries)
			}
			if called := strings.Contains(readLog(t, log), "--store"); called != tc.storeCalled {
				t.Fatalf("--store called: %v", called)
			}
			if strings.Contains(out, testValue) || strings.Contains(err.Error(), testValue) || strings.Contains(shown, testValue) {
				t.Fatal("the value was shown")
			}
		})
	}
}

// A signal the play guard caught (promptCancel closed) ends the read at
// once, before Enter, and nothing is stored; one that came before the read
// ends it before any key.
func TestStoreStepCancelledByTheGuard(t *testing.T) {
	for _, early := range []bool{false, true} {
		t.Run(map[bool]string{false: "while reading", true: "before reading"}[early], func(t *testing.T) {
			store, log := storeHelper(t)
			cancel := make(chan struct{})
			promptCancel = cancel
			t.Cleanup(func() { promptCancel = nil })
			if early {
				close(cancel)
			} else {
				go func() { time.Sleep(600 * time.Millisecond); close(cancel) }()
			}
			typeIt, finish := onPty(t)
			typeIt("abc", false)
			var err error
			done := make(chan struct{})
			out := captureStdout(t, func() {
				go func() { defer close(done); err = storeSecret(testRef) }()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("the read did not end on cancel")
					typeIt("\r", false) // ends a read that ignores it
				}
			})
			<-done
			// A read left pending at the cancel takes this key and ends
			// (on macOS a terminal read blocks its close until it returns).
			typeIt("\r", false)
			time.Sleep(500 * time.Millisecond)
			finish()
			if err == nil || !strings.Contains(err.Error(), "nothing was written") {
				t.Fatalf("store step: %v", err)
			}
			if _, ok := storedValue(store); ok || strings.Contains(readLog(t, log), "--store") {
				t.Fatal("a cancelled read stored a value")
			}
			if strings.Contains(out, "abc") {
				t.Fatal("the typed text was shown")
			}
		})
	}
}

// The read takes no key after its own Enter: what is typed next is the
// next prompt's.
func TestReadHiddenReadsNothingAhead(t *testing.T) {
	typeIt, finish := onPty(t)
	defer finish()
	typeIt("abc\r", false)
	first, res := readHidden(secretIn, nil, testLimit)
	if res != hiddenOK || string(first) != "abc" {
		t.Fatalf("first read %q %v", first, res)
	}
	typeIt("def\r", false)
	second, res := readHidden(secretIn, nil, testLimit)
	if res != hiddenOK || string(second) != "def" {
		t.Fatalf("second read %q %v: a key went to the first read", second, res)
	}
}

func TestStoreStepRefusals(t *testing.T) {
	storeHelper(t)
	if w := storeStepRefusal(testRef, true, false, nil); w != "" {
		t.Fatalf("a person's own terminal refused: %s", w)
	}
	for _, tc := range []struct {
		name        string
		interactive bool
		yes         bool
		trusted     []string
		agentVar    string
		agentVal    string
	}{
		{name: "no terminal", interactive: false},
		{name: "--yes", interactive: true, yes: true},
		{name: "--trust-secret", interactive: true, trusted: []string{testRef}},
		{name: "CLAUDECODE", interactive: true, agentVar: "CLAUDECODE", agentVal: "1"},
		{name: "CLAUDE_CODE_ENTRYPOINT", interactive: true, agentVar: "CLAUDE_CODE_ENTRYPOINT", agentVal: "cli"},
		{name: "CLAUDECODE set empty", interactive: true, agentVar: "CLAUDECODE"},
		{name: "CLAUDE_CODE_ENTRYPOINT set empty", interactive: true, agentVar: "CLAUDE_CODE_ENTRYPOINT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.agentVar != "" {
				t.Setenv(tc.agentVar, tc.agentVal)
			}
			if w := storeStepRefusal(testRef, tc.interactive, tc.yes, tc.trusted); w == "" {
				t.Fatal("the store step was allowed")
			}
		})
	}
}

func TestPreparePendingStore(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	storeHelper(t)
	// A plan only shows it; it never arms the store step.
	pending, why := preparePendingStore([]string{testRef}, true, false, false, nil)
	if len(pending) != 1 || why != "" || !refsPendingStore[testRef] || storeAfterConfirm != nil {
		t.Fatalf("plan only: %v %q %v %v", pending, why, refsPendingStore, storeAfterConfirm)
	}
	// The pending reference passes the plan's check, and only then.
	if err := checkRefsWith(helperState{}, refClauses("TOKEN", testRef)); err != nil {
		t.Fatalf("the plan's check refused a pending reference: %v", err)
	}
	donePendingStore()
	if err := checkRefsWith(helperState{}, refClauses("TOKEN", testRef)); err == nil {
		t.Fatal("after the plan, a missing reference passed the check")
	}
	// On a person's own terminal it arms the store step.
	pending, why = preparePendingStore([]string{testRef}, false, true, false, nil)
	if len(pending) != 1 || why != "" || !storeAfterConfirm[testRef] {
		t.Fatalf("a terminal: %v %q %v", pending, why, storeAfterConfirm)
	}
}

// The update path maps its flags onto play's (cpb update --yes is
// playYes, --trust-secret is playTrustSecret) and runs the same two steps:
// started by an agent, the store step never runs.
func TestStoreStepNeverUnderAnAgentUpdate(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	store, log := storeHelper(t)
	t.Setenv("CLAUDECODE", "1")
	pending, why := preparePendingStore([]string{testRef}, false, true, false, nil)
	if pending != nil || why == "" || refsPendingStore != nil || storeAfterConfirm != nil {
		t.Fatalf("an agent's update armed the store step: %v %q", pending, why)
	}
	// Even armed by hand, the confirmation refuses it under an agent.
	storeAfterConfirm = map[string]bool{testRef: true}
	savedIn := promptIn
	promptIn = strings.NewReader("y\n" + testRef + "\n")
	t.Cleanup(func() { promptIn = savedIn })
	res := &play.Result{Risks: []play.Risk{{Code: play.RiskUsesSecret, Confirm: testRef}}}
	var err error
	captureStdout(t, func() { err = playConfirmAsk(res, true, "Update? [y/N] ") })
	if err == nil || !strings.Contains(err.Error(), "agent") {
		t.Fatalf("an agent's update: %v", err)
	}
	if strings.Contains(readLog(t, log), "--store") {
		t.Fatal("--store was called under an agent")
	}
	if _, ok := storedValue(store); ok {
		t.Fatal("something was stored under an agent")
	}
}

// A key that sends an escape sequence would leave printable bytes ("[A",
// "[3~") in a value no one can see: the line ends as escaped, and nothing
// of it is kept. Bracketed paste's markers are dropped and the paste kept.
func TestReadHiddenEscapeSequences(t *testing.T) {
	for _, tc := range []struct {
		name, input, want string
		escaped           bool
	}{
		{name: "an arrow", input: "ab\x1b[Acd\r", escaped: true},
		{name: "delete", input: "ab\x1b[3~\r", escaped: true},
		{name: "a function key", input: "ab\x1bOPcd\r", escaped: true},
		{name: "alt with a key", input: "ab\x1bxcd\r", escaped: true},
		{name: "a lone escape", input: "ab\x1b\r", escaped: true},
		{name: "a bracketed paste", input: "\x1b[200~s3cr3t-Value-42\x1b[201~\r", want: "s3cr3t-Value-42"},
		{name: "a bracketed paste, then an arrow", input: "\x1b[200~abc\x1b[201~\x1b[D\r", escaped: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			typeIt, finish := onPty(t)
			defer finish()
			typeIt(tc.input, false)
			got, res := readHidden(secretIn, nil, testLimit)
			if (res == hiddenEscaped) != tc.escaped {
				t.Fatalf("result %v, escaped wanted: %v", res, tc.escaped)
			}
			if tc.escaped {
				if got != nil {
					t.Fatalf("an escaped line was kept: %q", got)
				}
				return
			}
			if res != hiddenOK || string(got) != tc.want {
				t.Fatalf("read %q %v, want %q", got, res, tc.want)
			}
		})
	}
}

// A value typed with an arrow in it is not stored: the step says why and
// asks again, and the next value, entered twice, is stored once.
func TestStoreStepAsksAgainAfterAnEscapedValue(t *testing.T) {
	store, log := storeHelper(t)
	typeIt, finish := onPty(t)
	typeIt("s3cr3t\x1b[D-Value\r", false)
	typeIt(testValue+"\r", false)
	typeIt(testValue+"\r", false)
	var err error
	out := captureStdout(t, func() { err = storeWithin(t, testRef) })
	finish()
	if err != nil {
		t.Fatalf("store step: %v", err)
	}
	if !strings.Contains(out, "escape sequence") || !strings.Contains(out, "Try again (2 of 3)") {
		t.Fatalf("the step did not say why and ask again: %q", out)
	}
	if v, ok := storedValue(store); !ok || v != testValue {
		t.Fatalf("stored %q (%v)", v, ok)
	}
	if n := strings.Count(readLog(t, log), "--store"); n != 1 {
		t.Fatalf("--store called %d times, want 1", n)
	}
	if strings.Contains(out, "s3cr3t") || strings.Contains(out, testValue) {
		t.Fatal("a typed value was shown")
	}
}

// Two entries that differ store nothing and are asked for again; a match
// then stores once. The values never reach stdout or the helper's argv.
func TestStoreStepAsksAgainWhenTheEntriesDiffer(t *testing.T) {
	store, log := storeHelper(t)
	typeIt, finish := onPty(t)
	typeIt("first-try-1\r", false)
	typeIt("first-try-2\r", false)
	typeIt(testValue+"\r", false)
	typeIt(testValue+"\r", false)
	var err error
	out := captureStdout(t, func() { err = storeWithin(t, testRef) })
	finish()
	if err != nil {
		t.Fatalf("store step: %v", err)
	}
	if !strings.Contains(out, "the two entries differ") || !strings.Contains(out, "Try again (2 of 3)") {
		t.Fatalf("a mismatch was not reported: %q", out)
	}
	if v, ok := storedValue(store); !ok || v != testValue {
		t.Fatalf("stored %q (%v)", v, ok)
	}
	argv := readLog(t, log)
	if n := strings.Count(argv, "--store"); n != 1 {
		t.Fatalf("--store called %d times, want 1", n)
	}
	for _, v := range []string{"first-try", testValue} {
		if strings.Contains(out, v) || strings.Contains(argv, v) {
			t.Fatalf("%q reached stdout or argv", v)
		}
	}
}

// After three failed attempts (an escaped value, then two mismatches) the
// step stops, says to store the value with your own tool, and stores
// nothing.
func TestStoreStepStopsAfterThreeAttempts(t *testing.T) {
	store, log := storeHelper(t)
	typeIt, finish := onPty(t)
	// Input for a fourth attempt too: the step must stop after the third.
	for _, in := range []string{"abc\x1b[A\r", "aaa\r", "bbb\r", "ccc\r", "ddd\r", "eee\r", "fff\r"} {
		typeIt(in, false)
	}
	var err error
	out := captureStdout(t, func() { err = storeWithin(t, testRef) })
	finish()
	if err == nil || !strings.Contains(err.Error(), "after 3 attempts") || !strings.Contains(err.Error(), "Store the value with your own tool, then run this again") {
		t.Fatalf("store step: %v", err)
	}
	if n := strings.Count(out, "Type the value now"); n != 3 || strings.Contains(out, "(4 of 3)") {
		t.Fatalf("asked %d times, want 3: %q", n, out)
	}
	if _, ok := storedValue(store); ok || strings.Contains(readLog(t, log), "--store") {
		t.Fatal("something was stored")
	}
	for _, v := range []string{"abc", "aaa", "bbb", "ccc", "ddd"} {
		if strings.Contains(out, v) || strings.Contains(err.Error(), v) {
			t.Fatalf("%q was shown", v)
		}
	}
}

// A failed --store says to store the value with your own tool (SPEC,
// "The helper interface").
func TestStoreStepFailedStoreNamesTheWayOut(t *testing.T) {
	storeHelper(t)
	t.Setenv("FAIL_STORE", "1")
	typeIt, finish := onPty(t)
	typeIt(testValue+"\r", false)
	typeIt(testValue+"\r", false)
	var err error
	captureStdout(t, func() { err = storeWithin(t, testRef) })
	finish()
	if err == nil || !strings.Contains(err.Error(), "Store the value with your own tool, then run this again") {
		t.Fatalf("store step: %v", err)
	}
}

// Only "not stored" (--check exits 4) asks for a value. A store that
// cannot answer (locked, unreachable: any other failure) does not: the
// plan refuses the reference with the helper's message, as before.
func TestPreparePendingStoreOnlyWhenNotStored(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	storeHelper(t)
	for _, code := range []string{"1", "5"} {
		t.Setenv("CHECK_EXIT", code)
		pending, why := preparePendingStore([]string{testRef}, false, true, false, nil)
		if pending != nil || why != "" || refsPendingStore != nil || storeAfterConfirm != nil {
			t.Fatalf("--check exit %s armed the store step: %v %q", code, pending, why)
		}
		if err := checkRefsWith(helperState{}, refClauses("TOKEN", testRef)); err == nil {
			t.Fatalf("--check exit %s: the plan passed a reference that does not resolve", code)
		}
	}
	t.Setenv("CHECK_EXIT", "4")
	if pending, _ := preparePendingStore([]string{testRef}, false, true, false, nil); len(pending) != 1 {
		t.Fatalf("--check exit 4 did not arm the store step: %v", pending)
	}
}

// testLimit is the hidden reads' limit in tests that do not test it.
const testLimit = 64

// storeWithin runs the store step and fails the test, instead of hanging
// it, when the step does not end in time.
func storeWithin(t *testing.T, ref string) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- storeSecret(ref) }()
	select {
	case err := <-done:
		return err
	case <-time.After(30 * time.Second):
		t.Fatalf("the store step for %s did not end within 30 s", ref)
		return nil
	}
}

// readWithin is one hidden read that fails the test instead of hanging it.
func readWithin(t *testing.T, cancel <-chan struct{}, limit int) ([]byte, hiddenResult) {
	t.Helper()
	type got struct {
		v   []byte
		res hiddenResult
	}
	done := make(chan got, 1)
	go func() {
		v, res := readHidden(secretIn, cancel, limit)
		done <- got{v, res}
	}()
	select {
	case g := <-done:
		return g.v, g.res
	case <-time.After(10 * time.Second):
		t.Fatal("the hidden read did not end within 10 s")
		return nil, hiddenStopped
	}
}

// nothingPending fails when anything is left to read on the terminal: a
// read with a short cancel must end cancelled, having read nothing. The
// read it cancels leaves its byte reader waiting on the terminal, as a
// cancelled read does in cpb (where the run then ends); a key typed after
// lets it go, so the terminal can be closed.
func nothingPending(t *testing.T, typeIt func(string, bool)) {
	t.Helper()
	cancel := make(chan struct{})
	go func() { time.Sleep(400 * time.Millisecond); close(cancel) }()
	v, res := readWithin(t, cancel, testLimit)
	typeIt("\r", false)
	time.Sleep(500 * time.Millisecond)
	if res != hiddenStopped || v != nil {
		t.Fatalf("input was left on the terminal: %q (%v)", v, res)
	}
}

// Editing in the hidden read: Backspace removes a whole character, not one
// of its bytes; a control key the read does not edit with, ^D inside a
// line, or a line longer than the limit is refused, and nothing is kept.
func TestReadHiddenEditingAndRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		limit       int
		want        string
		res         hiddenResult
	}{
		{name: "backspace over a two-byte character", input: "aé\x7fb\r", limit: 16, want: "ab", res: hiddenOK},
		{name: "backspace over a four-byte character", input: "a\U0001F600\x08b\r", limit: 16, want: "ab", res: hiddenOK},
		{name: "^U clears the line", input: "abc\x15de\r", limit: 16, want: "de", res: hiddenOK},
		{name: "a tab is kept", input: "a\tb\r", limit: 16, want: "a\tb", res: hiddenOK},
		{name: "a control key", input: "ab\x01cd\r", limit: 16, res: hiddenControl},
		{name: "^W", input: "ab\x17cd\r", limit: 16, res: hiddenControl},
		{name: "^D inside a line", input: "ab\x04cd\r", limit: 16, res: hiddenControl},
		{name: "exactly the limit", input: "abcde\r", limit: 5, want: "abcde", res: hiddenOK},
		{name: "over the limit", input: "abcdef\r", limit: 5, res: hiddenTooLong},
	} {
		t.Run(tc.name, func(t *testing.T) {
			typeIt, finish := onPty(t)
			defer finish()
			typeIt(tc.input, false)
			got, res := readWithin(t, nil, tc.limit)
			if res != tc.res || string(got) != tc.want {
				t.Fatalf("read %q %v, want %q %v", got, res, tc.want, tc.res)
			}
		})
	}
}

// On every way out of a hidden read the terminal is back in the mode it
// had, and what was typed past the line is discarded.
func TestReadHiddenRestoresTheTerminalAndDiscardsInput(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		res         hiddenResult
	}{
		{name: "a line", input: "abc\rLEFT-1\rLEFT-2\r", res: hiddenOK},
		{name: "^C", input: "ab\x03LEFT\r", res: hiddenStopped},
		{name: "an escape sequence", input: "ab\x1b[Ac\rLEFT\r", res: hiddenEscaped},
		{name: "too long", input: strings.Repeat("x", testLimit+1) + "\rLEFT\r", res: hiddenTooLong},
	} {
		t.Run(tc.name, func(t *testing.T) {
			typeIt, finish := onPty(t)
			defer finish()
			before, err := term.GetState(int(secretIn.Fd()))
			if err != nil {
				t.Fatal(err)
			}
			typeIt(tc.input, false)
			if _, res := readWithin(t, nil, testLimit); res != tc.res {
				t.Fatalf("result %v, want %v", res, tc.res)
			}
			after, err := term.GetState(int(secretIn.Fd()))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("the terminal was not put back in its mode")
			}
			nothingPending(t, typeIt)
		})
	}
}

// A multi-line paste at the value prompt never reaches the next reader of
// the terminal, the shell included: only the first line is read, and the
// rest is discarded.
func TestStoreStepMultiLinePasteReachesNothing(t *testing.T) {
	store, log := storeHelper(t)
	typeIt, finish := onPty(t)
	defer finish()
	typeIt("l1\rl2\rl3\rl4\rl5\rl6\rl7\rl8\rl9\r", false)
	typeIt("\x03", false) // at the confirmation: the paste did not answer it
	var err error
	captureStdout(t, func() { err = storeWithin(t, testRef) })
	if err == nil || !strings.Contains(err.Error(), "nothing was written") {
		t.Fatalf("store step: %v", err)
	}
	if _, ok := storedValue(store); ok || strings.Contains(readLog(t, log), "--store") {
		t.Fatal("a paste was stored")
	}
	nothingPending(t, typeIt)
}

// With no guard in place (cpb play --keep, cpb update), the step guards
// itself: a TERM during the hidden read ends it, with nothing stored and
// the terminal put back.
func TestStoreStepGuardsItselfWithoutAPlayGuard(t *testing.T) {
	store, log := storeHelper(t)
	if promptCancel != nil {
		t.Fatal("a guard is already in place")
	}
	// The test's own channel keeps the test process alive should the step
	// install no guard; the step then never ends, and storeWithin fails.
	keep := make(chan os.Signal, 1)
	signal.Notify(keep, syscall.SIGTERM)
	defer signal.Stop(keep)
	typeIt, finish := onPty(t)
	defer finish()
	// On every way out, the failing ones included, the terminal is freed
	// before it is closed (deferred calls run last first): should the step
	// have no guard, ^C ends its read, which is still in raw mode; a read
	// left pending after a cancel waits on the terminal in its own mode,
	// where ^C is a signal and only a line (Enter) ends it.
	defer func() {
		typeIt("\x03\r", false)
		time.Sleep(500 * time.Millisecond)
	}()
	before, _ := term.GetState(int(secretIn.Fd()))
	typeIt("abc", false)
	go func() { time.Sleep(800 * time.Millisecond); syscall.Kill(os.Getpid(), syscall.SIGTERM) }()
	var err error
	captureStdout(t, func() { err = storeWithin(t, testRef) })
	if err == nil || !strings.Contains(err.Error(), "nothing was written") {
		t.Fatalf("store step: %v", err)
	}
	if _, ok := storedValue(store); ok || strings.Contains(readLog(t, log), "--store") {
		t.Fatal("a cancelled step stored a value")
	}
	if promptCancel != nil {
		t.Fatal("the step left its guard in place")
	}
	if after, _ := term.GetState(int(secretIn.Fd())); !reflect.DeepEqual(before, after) {
		t.Fatal("the terminal was not put back in its mode")
	}
}

// After --store the reference is checked again: a value the helper took
// but that does not resolve fails the step.
func TestStoreStepChecksAgainAfterStoring(t *testing.T) {
	storeHelper(t)
	t.Setenv("FORCE_CHECK_FAIL", "1")
	typeIt, finish := onPty(t)
	defer finish()
	typeIt(testValue+"\r", false)
	typeIt(testValue+"\r", false)
	var err error
	captureStdout(t, func() { err = storeWithin(t, testRef) })
	if err == nil || !strings.Contains(err.Error(), "was stored but does not resolve") {
		t.Fatalf("store step: %v", err)
	}
}

// A helper that does not answer is not waited on: the step ends with the
// time it waited, and nothing is written.
func TestStoreStepDoesNotWaitForeverOnAHelper(t *testing.T) {
	storeHelper(t)
	saved := helperTimeout
	helperTimeout = 300 * time.Millisecond
	t.Cleanup(func() { helperTimeout = saved })
	t.Setenv("SLOW_STORE", "1")
	typeIt, finish := onPty(t)
	defer finish()
	typeIt(testValue+"\r", false)
	typeIt(testValue+"\r", false)
	var err error
	start := time.Now()
	captureStdout(t, func() { err = storeWithin(t, testRef) })
	if err == nil || !strings.Contains(err.Error(), "did not answer within 300ms") || time.Since(start) > 4*time.Second {
		t.Fatalf("store step after %s: %v", time.Since(start), err)
	}
}

// A reference used twice is listed, checked and asked for once.
func TestPendingStoreListsEachReferenceOnce(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	_, log := storeHelper(t)
	pending, _ := preparePendingStore([]string{testRef, testRef}, true, false, false, nil)
	if len(pending) != 1 {
		t.Fatalf("pending %v", pending)
	}
	if n := strings.Count(readLog(t, log), "--check"); n != 1 {
		t.Fatalf("--check ran %d times, want 1", n)
	}
	res := &play.Result{Risks: []play.Risk{{Code: play.RiskUsesSecret, Confirm: testRef}, {Code: play.RiskUsesSecret, Confirm: testRef}}}
	if refs := playRefs(res); len(refs) != 1 {
		t.Fatalf("playRefs %v", refs)
	}
}
