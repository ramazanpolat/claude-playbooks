package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
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
# As cpb's default helper, the stub is run with its own argument first.
[ "$1" = __secret-helper ] && shift
case "$1" in
  --ready) [ -n "$READY_SLEEP" ] && sleep "$READY_SLEEP"
           [ -n "$READY_EXIT" ] && { echo "the test store is locked and cannot ask for its password here" >&2; exit "$READY_EXIT"; }; exit 0 ;;
  --check) [ -n "$CHECK_SLEEP" ] && sleep "$CHECK_SLEEP"
           [ -z "$FORCE_CHECK_FAIL" ] && [ -f "$STORE_DIR/$(key "${2#*=}")" ] && exit 0; exit "${CHECK_EXIT:-4}" ;;
  --store) if [ -n "$SLOW_STORE" ]; then sleep "${STORE_SLEEP:-5}"; fi
           if [ -n "$FAIL_STORE" ]; then cat >/dev/null; exit "${STORE_EXIT:-1}"; fi
           f="$STORE_DIR/$(key "$2")"; cat > "$f.part" && mv "$f.part" "$f"
           [ -n "$FAIL_AFTER_STORE" ] && exit "${STORE_EXIT:-1}"
           exit 0 ;;
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
	t.Setenv("STORE_SLEEP", "")
	t.Setenv("STORE_EXIT", "")
	t.Setenv("FAIL_AFTER_STORE", "")
	t.Setenv("READY_EXIT", "")
	t.Setenv("READY_SLEEP", "")
	t.Setenv("CHECK_SLEEP", "")
	t.Setenv(envset.SecretHelperEnv, path)
	for _, v := range agentSessionVars {
		t.Setenv(v, "") // restored after the test
		os.Unsetenv(v)
	}
	t.Cleanup(func() { refsPendingStore, storeAfterConfirm = nil, nil })
	return store, log
}

// asDefaultHelper makes the stub helper storeHelper wrote cpb's default
// helper: no helper is configured, the default helper's store is an
// in-memory one, and "cpb's own binary" is the stub, so the step runs it
// with __secret-helper first, --ready included.
func asDefaultHelper(t *testing.T, log string) *memStore {
	t.Helper()
	resetCommandTestState(t)
	aliasTestHome(t)
	m := useMemStore(t)
	stub := filepath.Join(filepath.Dir(log), "store-helper")
	selfExecutable = func() (string, error) { return stub, nil }
	t.Setenv(envset.SecretHelperEnv, "")
	return m
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
	// The drain waits for 50 ms of quiet in these tests, well under the
	// 300 ms between typed inputs, so a typed answer never lands in the
	// drain of the read before it. The drain's own tests set it back.
	savedQuiet := drainQuiet
	drainQuiet = 50 * time.Millisecond
	t.Cleanup(func() { drainQuiet = savedQuiet })
	// One writer types each input in the order given, after a pause, so a
	// second entry is typed while the second read waits for it.
	type typed struct {
		input      string
		closeAfter bool
		delay      time.Duration
	}
	var hangUp func() // set below, before anything is typed
	queue := make(chan typed, 64)
	go func() {
		for in := range queue {
			time.Sleep(in.delay) // the read is in raw mode by now
			io.WriteString(ptmx, in.input)
			if in.closeAfter {
				time.Sleep(100 * time.Millisecond)
				hangUp()
			}
		}
	}()
	// What the terminal shows is read as it comes, from the start: a ^C
	// typed in the terminal's own mode discards the output not read yet,
	// so reading it only at the end would find nothing, echo included.
	var shownMu sync.Mutex
	var shown bytes.Buffer
	readerDone := make(chan struct{})
	var stopReading atomic.Bool
	go func() {
		defer close(readerDone)
		b := make([]byte, 4096)
		for {
			n, err := ptmx.Read(b)
			if n > 0 {
				shownMu.Lock()
				shown.Write(b[:n])
				shownMu.Unlock()
			}
			if err != nil || stopReading.Load() {
				return
			}
		}
	}()
	// hangUp closes the terminal's other end, as a terminal that goes away:
	// the reader is stopped first (woken by a byte the terminal shows), so
	// no read of it holds the close back.
	hangUp = func() {
		stopReading.Store(true)
		tty.Write([]byte{0})
		select {
		case <-readerDone:
		case <-time.After(2 * time.Second):
		}
		ptmx.Close()
	}
	typeIt = func(input string, closeAfter bool) {
		d := 300 * time.Millisecond
		if typeDelay > 0 {
			d, typeDelay = typeDelay, 0
		}
		queue <- typed{input, closeAfter, d}
	}
	finish = func() string {
		secretIn = saved
		// Free a read still waiting on the terminal, so it can be closed
		// on every way out of a test, a failing one included: ^C ends a
		// read in raw mode; a read in the terminal's own mode, where ^C is
		// a signal, ends at a line.
		io.WriteString(ptmx, "\x03\r")
		time.Sleep(300 * time.Millisecond)
		tty.Close()
		select {
		case <-readerDone:
		case <-time.After(2 * time.Second):
		}
		ptmx.Close()
		<-readerDone
		shownMu.Lock()
		defer shownMu.Unlock()
		return shown.String()
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
	if strings.Contains(argv, "--ready") {
		t.Fatal("a configured helper was asked --ready, the default helper's own verb")
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

// typeDelay, when set, is the pause before the next typed input instead of
// 300 ms (it is used once).
var typeDelay time.Duration

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
// had, and what was typed past the line is discarded. A line followed by
// more than line ends is refused: it was not the whole of what came.
func TestReadHiddenRestoresTheTerminalAndDiscardsInput(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		res         hiddenResult
	}{
		{name: "a line, then more lines", input: "abc\rLEFT-1\rLEFT-2\r", res: hiddenMoreInput},
		{name: "a line, then a key typed right after Enter", input: "abc\rx", res: hiddenMoreInput},
		{name: "a line, then line ends only", input: "abc\r\n\r", res: hiddenOK},
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

// A multi-line paste at the value prompt, on a terminal without bracketed
// paste, is refused (more than line ends followed its first line) and
// never reaches the next reader of the terminal, the shell included.
func TestStoreStepMultiLinePasteReachesNothing(t *testing.T) {
	store, log := storeHelper(t)
	typeIt, finish := onPty(t)
	defer finish()
	typeIt("l1\rl2\rl3\rl4\rl5\rl6\rl7\rl8\rl9\r", false)
	typeIt("\x03", false) // at the prompt asked again: the paste did not answer it
	var err error
	out := captureStdout(t, func() { err = storeWithin(t, testRef) })
	if err == nil || !strings.Contains(err.Error(), "nothing was written") || !strings.Contains(out, "more input followed the line") || strings.Contains(out, "Type it again") {
		t.Fatalf("store step: %v %q", err, out)
	}
	if _, ok := storedValue(store); ok || strings.Contains(readLog(t, log), "--store") {
		t.Fatal("a paste was stored")
	}
	nothingPending(t, typeIt)
}

// A key pasted whole, at every prompt, on a terminal without bracketed
// paste: its first line is never taken, and nothing is stored.
func TestStoreStepPEMPastedAtEveryPromptIsNotStored(t *testing.T) {
	store, log := storeHelper(t)
	typeIt, finish := onPty(t)
	defer finish()
	pem := "-----BEGIN TEST KEY-----\rAAAAZqZqZq\rBBBBZqZqZq\r-----END TEST KEY-----\r"
	for i := 0; i < 3; i++ {
		typeIt(pem, false)
	}
	var err error
	out := captureStdout(t, func() { err = storeWithin(t, testRef) })
	if err == nil || !strings.Contains(err.Error(), "after 3 attempts") || !strings.Contains(err.Error(), "more input followed the line") || strings.Contains(out, "Stored "+testRef) {
		t.Fatalf("store step: %v %q", err, out)
	}
	if _, ok := storedValue(store); ok || strings.Contains(readLog(t, log), "--store") {
		t.Fatal("something was stored")
	}
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

// A multi-line paste longer than the terminal's input queue keeps
// arriving after its first line ends the read and after a single flush.
// The read takes input while echo is still off until the terminal is
// quiet, and only then restores it: the line is refused, since more
// followed it, nothing of the rest is shown, none of it answers the next
// prompt (only the ^C typed there does), and none is left for the shell.
func TestStoreStepLongPasteIsNeitherShownNorTaken(t *testing.T) {
	for _, size := range []int{2 << 10, 8 << 10, 33 << 10, 256 << 10} {
		t.Run(fmt.Sprintf("%d bytes", size), func(t *testing.T) {
			store, log := storeHelper(t)
			typeIt, finish := onPty(t)
			line := strings.Repeat("Zq", 31) + "\r" // 63 bytes a line
			paste := "first-line\r" + strings.Repeat(line, size/len(line))
			typeIt(paste, false)
			typeIt("\x03", false) // at the confirmation
			var err error
			out := captureStdout(t, func() { err = storeWithin(t, testRef) })
			nothingPending(t, typeIt)
			shown := finish()
			if err == nil || !strings.Contains(err.Error(), "was not stored; nothing was written") {
				t.Fatalf("store step: %v (the paste answered a prompt?)", err)
			}
			if !strings.Contains(out, "more input followed the line") || strings.Contains(out, "Type it again") || strings.Contains(out, "differ") {
				t.Fatalf("the paste was not refused, or answered a prompt: %q", out)
			}
			if strings.Contains(shown, "ZqZq") || strings.Contains(out, "ZqZq") {
				t.Fatalf("the pasted value was shown (%d bytes of output)", len(shown))
			}
			if _, ok := storedValue(store); ok || strings.Contains(readLog(t, log), "--store") {
				t.Fatal("something was stored")
			}
		})
	}
}

// A signal that arrives while the helper stores the value does not stop
// the helper; the step checks for it afterwards and stops there, saying
// whether the value was stored: when the helper failed after its signal
// (a ^C reaches the whole foreground group), the step asks the store once,
// and says only what that answer shows. The signal is sent once the
// helper has started its store (its log says so), not after a guessed
// time.
func TestStoreStepStopsAfterASignalDuringTheStore(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		failures   []string
		stored     bool
	}{
		{name: "the store succeeds", want: "cancelled: " + testRef + " was stored", stored: true},
		{name: "the helper fails after its write", failures: []string{"FAIL_AFTER_STORE"}, want: "cancelled: an item named " + testRef + " exists now", stored: true},
		{name: "the helper fails before writing", failures: []string{"FAIL_STORE"}, want: "cancelled: " + testRef + " was not stored"},
		{name: "the check after it does not answer", failures: []string{"FAIL_STORE", "FORCE_CHECK_FAIL"}, want: "cancelled: " + testRef + " may be stored"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, log := storeHelper(t)
			t.Setenv("SLOW_STORE", "1")
			t.Setenv("STORE_SLEEP", "2")
			for _, f := range tc.failures {
				t.Setenv(f, "1")
				if f == "FORCE_CHECK_FAIL" {
					t.Setenv("CHECK_EXIT", "5") // a check that cannot answer
				}
			}
			keep := make(chan os.Signal, 1)
			signal.Notify(keep, syscall.SIGTERM)
			defer signal.Stop(keep)
			// The signal is sent only while the test is still running, so
			// one that ends early never takes the test binary with it.
			sent := make(chan struct{})
			ended := make(chan struct{})
			defer close(ended)
			typeIt, finish := onPty(t)
			defer finish()
			typeIt(testValue+"\r", false)
			typeIt(testValue+"\r", false)
			go func() {
				defer close(sent)
				for !strings.Contains(readLog(t, log), "--store") {
					select {
					case <-ended:
						return
					case <-time.After(20 * time.Millisecond):
					}
				}
				syscall.Kill(os.Getpid(), syscall.SIGTERM) // the helper sleeps 2 s
			}()
			var err error
			out := captureStdout(t, func() { err = storeWithin(t, testRef) })
			<-sent
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("store step: %v", err)
			}
			if strings.Contains(out, "Stored "+testRef) {
				t.Fatal("the step went on after the signal")
			}
			if _, ok := storedValue(store); ok != tc.stored {
				t.Fatalf("stored: %v, want %v", ok, tc.stored)
			}
		})
	}
}

// A signal after the step's last check is not dropped: the step stops.
func TestStoreStepSignalAfterTheLastCheck(t *testing.T) {
	storeHelper(t)
	keep := make(chan os.Signal, 1)
	signal.Notify(keep, syscall.SIGTERM)
	defer signal.Stop(keep)
	storeStepChecked = func() {
		syscall.Kill(os.Getpid(), syscall.SIGTERM)
		<-keep
		time.Sleep(100 * time.Millisecond)
	}
	t.Cleanup(func() { storeStepChecked = nil })
	typeIt, finish := onPty(t)
	defer finish()
	typeIt(testValue+"\r", false)
	typeIt(testValue+"\r", false)
	var err error
	captureStdout(t, func() { err = storeWithin(t, testRef) })
	if err == nil || !strings.Contains(err.Error(), "cancelled: "+testRef+" was stored") {
		t.Fatalf("store step: %v", err)
	}
}

// A store the helper could not answer (exit 5) is followed by one check,
// and the step says what it shows: not stored, an item of the name
// exists, or (the check not answering either) that it may be stored. It
// never sends the person to store it elsewhere.
func TestStoreStepUnansweredStoreMayBeStored(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		failures   []string
	}{
		{name: "nothing was written", failures: []string{"FAIL_STORE"}, want: testRef + " was not stored: the store did not answer"},
		{name: "an item was written", failures: []string{"FAIL_AFTER_STORE"}, want: "an item of that name exists now"},
		{name: "the check does not answer either", failures: []string{"FAIL_STORE", "FORCE_CHECK_FAIL"}, want: "the value may be stored"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storeHelper(t)
			t.Setenv("STORE_EXIT", "5")
			for _, f := range tc.failures {
				t.Setenv(f, "1")
				if f == "FORCE_CHECK_FAIL" {
					t.Setenv("CHECK_EXIT", "5") // a check that cannot answer
				}
			}
			typeIt, finish := onPty(t)
			defer finish()
			typeIt(testValue+"\r", false)
			typeIt(testValue+"\r", false)
			var err error
			captureStdout(t, func() { err = storeWithin(t, testRef) })
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "with your own tool") {
				t.Fatalf("store step: %v", err)
			}
		})
	}
}

// Without bracketed paste, a paste that stalls for 150 ms is still
// discarded: the terminal counts as quiet only after 300 ms.
func TestStoreStepPasteWithAStall(t *testing.T) {
	store, log := storeHelper(t)
	typeIt, finish := onPty(t)
	drainQuiet = 300 * time.Millisecond // the real window
	line := strings.Repeat("Zq", 31) + "\r"
	typeIt("first-line\r"+strings.Repeat(line, 64), false)
	typeDelay = 150 * time.Millisecond
	typeIt(strings.Repeat(line, 64), false)
	typeDelay = 900 * time.Millisecond
	typeIt("\x03", false) // at the confirmation, after the drain
	var err error
	out := captureStdout(t, func() { err = storeWithin(t, testRef) })
	nothingPending(t, typeIt)
	shown := finish()
	if err == nil || !strings.Contains(err.Error(), "was not stored; nothing was written") || strings.Contains(out, "differ") {
		t.Fatalf("store step: %v %q", err, out)
	}
	if strings.Contains(shown, "ZqZq") || strings.Contains(out, "ZqZq") {
		t.Fatal("the pasted text was shown")
	}
	if _, ok := storedValue(store); ok || strings.Contains(readLog(t, log), "--store") {
		t.Fatal("something was stored")
	}
}

// With bracketed paste, a paste is read to its end marker whatever its
// gaps: one of several lines is refused, and nothing of it is shown or
// left; one line with its newline is taken.
func TestStoreStepBracketedPaste(t *testing.T) {
	t.Run("several lines, with a long gap", func(t *testing.T) {
		store, log := storeHelper(t)
		typeIt, finish := onPty(t)
		typeIt("\x1b[200~line-one\rZqZqZq-line-two\r", false)
		typeDelay = 600 * time.Millisecond
		typeIt("ZqZqZq-line-three\r\x1b[201~", false)
		typeDelay = 600 * time.Millisecond
		typeIt("\x03", false)
		var err error
		out := captureStdout(t, func() { err = storeWithin(t, testRef) })
		nothingPending(t, typeIt)
		shown := finish()
		if err == nil || !strings.Contains(out, "more than one line") || !strings.Contains(err.Error(), "was not stored") {
			t.Fatalf("store step: %v %q", err, out)
		}
		if strings.Contains(shown, "ZqZq") || strings.Contains(out, "ZqZq") || strings.Contains(out, "line-one") {
			t.Fatal("the pasted text was shown")
		}
		if _, ok := storedValue(store); ok || strings.Contains(readLog(t, log), "--store") {
			t.Fatal("something was stored")
		}
	})
	t.Run("one line with its newline", func(t *testing.T) {
		store, _ := storeHelper(t)
		typeIt, finish := onPty(t)
		defer finish()
		typeIt("\x1b[200~"+testValue+"\r\x1b[201~", false)
		typeIt("\x1b[200~"+testValue+"\n\x1b[201~", false)
		var err error
		captureStdout(t, func() { err = storeWithin(t, testRef) })
		if v, _ := storedValue(store); err != nil || v != testValue {
			t.Fatalf("store step: %v, stored %q", err, v)
		}
	})
	// Backspace, ^U and ^D inside a paste would edit the value unseen.
	for _, edit := range []string{"\x7f", "\x08", "\x15", "\x04"} {
		t.Run(fmt.Sprintf("an edit inside the paste (%q)", edit), func(t *testing.T) {
			typeIt, finish := onPty(t)
			defer finish()
			typeIt("\x1b[200~abc"+edit+"de\r\x1b[201~", false)
			if v, res := readWithin(t, nil, testLimit); res != hiddenControl || v != nil {
				t.Fatalf("read %q %v", v, res)
			}
		})
	}
	t.Run("a paste that does not end", func(t *testing.T) {
		saved := pasteStall
		pasteStall = 200 * time.Millisecond
		t.Cleanup(func() { pasteStall = saved })
		storeHelper(t)
		typeIt, finish := onPty(t)
		defer finish()
		typeIt("\x1b[200~"+testValue+"\r", false)
		typeDelay = 800 * time.Millisecond
		typeIt("\r", false) // Enter: inside a paste, still a line end
		typeDelay = 800 * time.Millisecond
		typeIt("\x03", false)
		var err error
		out := captureStdout(t, func() { err = storeWithin(t, testRef) })
		if err == nil || !strings.Contains(err.Error(), "was not stored; nothing was written") || strings.Count(out, "The paste has not ended") != 1 || !strings.Contains(out, "Ctrl-C") {
			t.Fatalf("store step: %v %q", err, out)
		}
	})
}

// Input that keeps arriving past drainMax: the person is told once, and
// the drain goes on, echo off. A ^C then ends the step, saying what
// arrives after may be shown; input that stops ends the drain, and the
// line is refused, since more than line ends followed it.
func TestStoreStepWhenInputKeepsArriving(t *testing.T) {
	stream := func(typeIt func(string, bool), n int) {
		for i := 0; i < n; i++ {
			typeDelay = 20 * time.Millisecond
			typeIt("x", false)
		}
	}
	short := func(t *testing.T) {
		savedMax := drainMax
		drainMax = 400 * time.Millisecond
		t.Cleanup(func() { drainMax = savedMax })
		drainQuiet = 150 * time.Millisecond
	}
	t.Run("stopped with ^C", func(t *testing.T) {
		store, log := storeHelper(t)
		typeIt, finish := onPty(t)
		defer finish()
		short(t)
		typeIt("abc\r", false)
		stream(typeIt, 50)
		typeDelay = 20 * time.Millisecond
		typeIt("\x03", false)
		var err error
		out := captureStdout(t, func() { err = storeWithin(t, testRef) })
		if err == nil || !strings.Contains(err.Error(), "while input was still arriving") || !strings.Contains(err.Error(), "may be shown") {
			t.Fatalf("store step: %v %q", err, out)
		}
		if strings.Count(out, "Input is still arriving") != 1 {
			t.Fatalf("the person was not told once: %q", out)
		}
		if _, ok := storedValue(store); ok || strings.Contains(readLog(t, log), "--store") {
			t.Fatal("something was stored")
		}
	})
	t.Run("input that stops", func(t *testing.T) {
		store, log := storeHelper(t)
		typeIt, finish := onPty(t)
		defer finish()
		short(t)
		typeIt("abc\r", false)
		stream(typeIt, 50)
		typeDelay = 600 * time.Millisecond
		typeIt("\x03", false) // at the prompt asked again
		var err error
		out := captureStdout(t, func() { err = storeWithin(t, testRef) })
		if err == nil || !strings.Contains(err.Error(), "was not stored; nothing was written") || !strings.Contains(out, "Input is still arriving") || !strings.Contains(out, "more input followed the line") {
			t.Fatalf("store step: %v %q", err, out)
		}
		if _, ok := storedValue(store); ok || strings.Contains(readLog(t, log), "--store") {
			t.Fatal("something was stored")
		}
	})
	t.Run("a signal ends the drain at once, with the caveat", func(t *testing.T) {
		typeIt, finish := onPty(t)
		defer finish()
		drainQuiet = 150 * time.Millisecond
		typeIt("abc\r", false)
		stream(typeIt, 60) // the typing queue holds 64
		cancel := make(chan struct{})
		go func() { time.Sleep(700 * time.Millisecond); close(cancel) }()
		start := time.Now()
		if v, res := readWithin(t, cancel, testLimit); res != hiddenNotQuiet || v != nil || time.Since(start) > 2*time.Second {
			t.Fatalf("read %q %v after %s", v, res, time.Since(start))
		}
	})
	// A ^C in the drain before the notice: with other input among it, the
	// caveat; alone, a plain stop.
	for _, tc := range []struct {
		name, input string
		res         hiddenResult
	}{
		{name: "a ^C among other input", input: "abc\rxyz\x03", res: hiddenNotQuiet},
		{name: "a ^C alone after Enter", input: "abc\r\x03", res: hiddenStopped},
	} {
		t.Run(tc.name, func(t *testing.T) {
			typeIt, finish := onPty(t)
			defer finish()
			typeIt(tc.input, false)
			if v, res := readWithin(t, nil, testLimit); res != tc.res || v != nil {
				t.Fatalf("read %q %v, want %v", v, res, tc.res)
			}
		})
	}
}

// Input is read, not discarded unseen, wherever the waiting count can be
// read: a failing discard alone changes nothing. A terminal whose input
// can be neither counted nor discarded ends the read at once, and the step
// says why.
func TestReadHiddenStopsWhenInputCannotBeDiscarded(t *testing.T) {
	saved, savedCount := flushTerminal, pendingCount
	flushTerminal = func(int) error { return errors.New("not supported") }
	t.Cleanup(func() { flushTerminal, pendingCount = saved, savedCount })
	t.Run("the count still works", func(t *testing.T) {
		typeIt, finish := onPty(t)
		defer finish()
		typeIt("abc\r", false)
		if v, res := readWithin(t, nil, testLimit); res != hiddenOK || string(v) != "abc" {
			t.Fatalf("read %q %v", v, res)
		}
	})
	pendingCount = func(int) (int, error) { return 0, errors.New("not supported") }
	t.Run("the read", func(t *testing.T) {
		typeIt, finish := onPty(t)
		defer finish()
		typeIt("abc\r", false)
		start := time.Now()
		if v, res := readWithin(t, nil, testLimit); res != hiddenUndiscarded || v != nil || time.Since(start) > 2*time.Second {
			t.Fatalf("read %q %v after %s", v, res, time.Since(start))
		}
	})
	t.Run("the step", func(t *testing.T) {
		storeHelper(t)
		typeIt, finish := onPty(t)
		defer finish()
		typeIt("abc\r", false)
		var err error
		captureStdout(t, func() { err = storeWithin(t, testRef) })
		if err == nil || !strings.Contains(err.Error(), "could not be discarded") {
			t.Fatalf("store step: %v", err)
		}
	})
}

// The pty's transcript does capture what the terminal shows: in its own
// mode, typed text is echoed and found there (a control for the tests
// that look for a value in it).
func TestPtyTranscriptShowsEcho(t *testing.T) {
	typeIt, finish := onPty(t)
	typeIt("visible-echo\r", false)
	time.Sleep(600 * time.Millisecond)
	if shown := finish(); !strings.Contains(shown, "visible-echo") {
		t.Fatalf("the transcript holds no echo: %q", shown)
	}
}

// The step's read limit is the default helper's store's own, and a line
// over it is refused, not taken; a configured helper reads up to 64 KiB.
func TestStoreStepReadLimitFollowsTheStore(t *testing.T) {
	_, log := storeHelper(t)
	m := asDefaultHelper(t, log)
	m.max = 5
	if got := storeLimit(helperExe{def: true}); got != 5 {
		t.Fatalf("the default helper's limit: %d", got)
	}
	if got := storeLimit(helperExe{name: "mine"}); got != maxSecretBytes {
		t.Fatalf("a configured helper's limit: %d", got)
	}
	typeIt, finish := onPty(t)
	defer finish()
	for i := 0; i < 3; i++ {
		typeIt("abcdef\r", false)
	}
	var err error
	captureStdout(t, func() { err = storeWithin(t, "cpb:tok") })
	if err == nil || !strings.Contains(err.Error(), "after 3 attempts") || !strings.Contains(err.Error(), "longer than the 5 bytes") || m.writes != 0 {
		t.Fatalf("an over-long value: %v, %d writes", err, m.writes)
	}
}

// The default helper is asked --ready before the step asks for a value: a
// store that cannot take one stops the step there, with its reason, and
// nothing is read from the terminal; one that can lets the step go on.
func TestStoreStepAsksTheStoreBeforeTheValue(t *testing.T) {
	t.Run("not ready", func(t *testing.T) {
		_, log := storeHelper(t)
		m := asDefaultHelper(t, log)
		t.Setenv("READY_EXIT", "5")
		_, finish := onPty(t)
		defer finish()
		var err error
		var out string
		stderr := captureStderr(t, func() { out = captureStdout(t, func() { err = storeWithin(t, "cpb:tok") }) })
		if err == nil || !strings.Contains(err.Error(), "cannot take a value now") || !strings.Contains(err.Error(), "nothing was asked for") || strings.Contains(out, "Type the value") || !strings.Contains(stderr, "locked") {
			t.Fatalf("store step: %v %q %q", err, out, stderr)
		}
		if !strings.Contains(readLog(t, log), "__secret-helper --ready cpb:tok") || m.writes != 0 {
			t.Fatalf("the helper's log: %q, %d writes", readLog(t, log), m.writes)
		}
	})
	t.Run("ready", func(t *testing.T) {
		_, log := storeHelper(t)
		asDefaultHelper(t, log)
		typeIt, finish := onPty(t)
		defer finish()
		typeIt("\r", false) // an empty answer: the step reached the prompt
		var err error
		out := captureStdout(t, func() { err = storeWithin(t, "cpb:tok") })
		if err == nil || !strings.Contains(err.Error(), "was not stored; nothing was written") || !strings.Contains(out, "Type the value now") {
			t.Fatalf("store step: %v %q", err, out)
		}
		if !strings.Contains(readLog(t, log), "--ready cpb:tok") {
			t.Fatal("the default helper was not asked --ready")
		}
	})
}

// While --ready runs (an unlock dialog may wait for its answer), the
// person is told, the terminal's echo is off, and what is typed meanwhile
// is neither shown nor read as the value: it is discarded before the
// first prompt, saying so.
func TestStoreStepReadyKeepsTypingOut(t *testing.T) {
	store, log := storeHelper(t)
	asDefaultHelper(t, log)
	t.Setenv("READY_SLEEP", "1")
	typeIt, finish := onPty(t)
	typeIt("typed-during-the-question\r", false)
	typeDelay = 1200 * time.Millisecond // after --ready has answered
	typeIt(testValue+"\r", false)
	typeIt(testValue+"\r", false)
	var err error
	out := captureStdout(t, func() { err = storeWithin(t, "cpb:tok") })
	shown := finish()
	if err != nil || !strings.Contains(out, "Asking the store whether it can take a value") || !strings.Contains(out, "discarded, unseen") {
		t.Fatalf("store step: %v %q", err, out)
	}
	if strings.Contains(shown, "typed-during-the-question") {
		t.Fatal("what was typed during --ready was shown")
	}
	b, rerr := os.ReadFile(filepath.Join(store, "cpb_tok"))
	if rerr != nil || string(b) != testValue {
		t.Fatalf("stored %q (%v): what was typed during --ready was taken?", b, rerr)
	}
}

// The secret report (cpb play --check, --dry-run --json) waits for a helper
// no longer than every other check does: one that does not answer leaves
// "stored" unknown, with a note, and the report ends.
func TestSecretReportDoesNotWaitForeverOnAHelper(t *testing.T) {
	storeHelper(t)
	saved := helperTimeout
	helperTimeout = 300 * time.Millisecond
	t.Cleanup(func() { helperTimeout = saved })
	t.Setenv("CHECK_SLEEP", "5")
	start := time.Now()
	r := secretReport([]string{testRef})
	if len(r) != 1 || r[0].Stored != nil || !strings.Contains(r[0].Note, "did not answer within") || time.Since(start) > 4*time.Second {
		t.Fatalf("after %s: %+v", time.Since(start), r)
	}
}

// A line with a control key in it is not taken: the step says why and asks
// again, and stores only the next, clean value.
func TestStoreStepRefusesAControlKey(t *testing.T) {
	store, _ := storeHelper(t)
	typeIt, finish := onPty(t)
	defer finish()
	typeIt("ab\x01cd\r", false)
	typeIt(testValue+"\r", false)
	typeIt(testValue+"\r", false)
	var err error
	out := captureStdout(t, func() { err = storeWithin(t, testRef) })
	if err != nil || !strings.Contains(out, "control key") {
		t.Fatalf("store step: %v %q", err, out)
	}
	if v, _ := storedValue(store); v != testValue {
		t.Fatalf("stored %q", v)
	}
}

// The step has its guard in place by the time it reads, wherever it runs
// (checked directly, not by a signal).
func TestStoreStepIsGuardedWhenItReads(t *testing.T) {
	storeHelper(t)
	var guarded bool
	storeStepReading = func() { guarded = promptCancel != nil }
	t.Cleanup(func() { storeStepReading = nil })
	typeIt, finish := onPty(t)
	defer finish()
	typeIt("\x03", false)
	captureStdout(t, func() { storeWithin(t, testRef) })
	if !guarded || promptCancel != nil {
		t.Fatalf("guarded while reading: %v; guard left after: %v", guarded, promptCancel != nil)
	}
}

// The guard the step installs for itself gives its signals back when it
// ends: a signal after stop no longer reaches it.
func TestStepGuardStopGivesSignalsBack(t *testing.T) {
	keep := make(chan os.Signal, 1)
	signal.Notify(keep, syscall.SIGHUP)
	defer signal.Stop(keep)
	g := newPlayGuard()
	g.stop()
	// Let the guard's goroutine end first, so it cannot take the signal off
	// its channel and hide that the channel was still registered.
	time.Sleep(100 * time.Millisecond)
	syscall.Kill(os.Getpid(), syscall.SIGHUP)
	<-keep
	time.Sleep(100 * time.Millisecond)
	if n := len(g.sigs); n != 0 {
		t.Fatalf("a stopped guard still received %d signal(s)", n)
	}
}

// stop reports a signal the guard was sent, also one still waiting on its
// channel when it stopped; a guard sent nothing reports none.
func TestStepGuardStopReportsASignal(t *testing.T) {
	g := newPlayGuard()
	g.sigs <- syscall.SIGTERM
	if !g.stop() {
		t.Fatal("a signal sent to the guard was not reported")
	}
	if g := newPlayGuard(); g.stop() {
		t.Fatal("a guard sent nothing reported a signal")
	}
	// A signal its goroutine never handled (here: one that never ran) is
	// still reported, from the channel.
	idle := &playGuard{sigs: make(chan os.Signal, 8), done: make(chan struct{}), exited: make(chan struct{}), cancelled: make(chan struct{})}
	close(idle.exited)
	idle.sigs <- syscall.SIGTERM
	if !idle.stop() {
		t.Fatal("a signal left on the guard's channel was not reported")
	}
}

// The confirmation asks for the reference by that word, never inviting
// the value with echo on.
func TestConfirmationAsksForTheReference(t *testing.T) {
	storeHelper(t)
	saved := promptIn
	promptIn = strings.NewReader("y\n" + testRef + "\n")
	t.Cleanup(func() { promptIn = saved })
	res := &play.Result{Risks: []play.Risk{{Code: play.RiskUsesSecret, Confirm: testRef}}}
	var err error
	out := captureStdout(t, func() { err = playConfirmAsk(res, true, "Run this playbook? [y/N] ") })
	if err != nil {
		t.Fatalf("the confirmation: %v", err)
	}
	if !strings.Contains(out, "Type the reference of the secret this playbook may read") {
		t.Fatalf("the prompt: %q", out)
	}
}
