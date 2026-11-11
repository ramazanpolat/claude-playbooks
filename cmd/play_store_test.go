package cmd

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"

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
  --check) [ -f "$STORE_DIR/$(key "${2#*=}")" ] && exit 0; exit "${CHECK_EXIT:-4}" ;;
  --store) if [ -n "$FAIL_STORE" ]; then cat >/dev/null; exit 1; fi
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
// (and closes that end after, for a terminal that goes away); finish
// restores secretIn and returns what the terminal showed.
func onPty(t *testing.T) (typeIt func(input string, closeAfter bool), finish func() string) {
	t.Helper()
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatalf("pty: %v", err)
	}
	saved := secretIn
	secretIn = tty
	typeIt = func(input string, closeAfter bool) {
		go func() {
			time.Sleep(300 * time.Millisecond) // the read is in raw mode by now
			io.WriteString(ptmx, input)
			if closeAfter {
				time.Sleep(100 * time.Millisecond)
				ptmx.Close()
			}
		}()
	}
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
	var err error
	out := captureStdout(t, func() { err = storeSecret(testRef) })
	shown := finish()
	if err != nil {
		t.Fatalf("store step: %v", err)
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
		name, input string
		closeAfter  bool
		failStore   bool
		storeCalled bool
	}{
		{name: "ctrl-c", input: "abc\x03"},
		{name: "empty answer", input: "\r"},
		{name: "ctrl-d", input: "\x04"},
		{name: "terminal closed", input: "abc", closeAfter: true},
		{name: "helper fails", input: testValue + "\r", failStore: true, storeCalled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, log := storeHelper(t)
			if tc.failStore {
				t.Setenv("FAIL_STORE", "1")
			}
			typeIt, finish := onPty(t)
			typeIt(tc.input, tc.closeAfter)
			var err error
			out := captureStdout(t, func() { err = storeSecret(testRef) })
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
	first, ok, _ := readHidden(secretIn, nil)
	if !ok || string(first) != "abc" {
		t.Fatalf("first read %q %v", first, ok)
	}
	typeIt("def\r", false)
	second, ok, _ := readHidden(secretIn, nil)
	if !ok || string(second) != "def" {
		t.Fatalf("second read %q %v: a key went to the first read", second, ok)
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
			got, ok, escaped := readHidden(secretIn, nil)
			if escaped != tc.escaped {
				t.Fatalf("escaped %v, want %v", escaped, tc.escaped)
			}
			if tc.escaped {
				if ok || got != nil {
					t.Fatalf("an escaped line was kept: %q %v", got, ok)
				}
				return
			}
			if !ok || string(got) != tc.want {
				t.Fatalf("read %q %v, want %q", got, ok, tc.want)
			}
		})
	}
}

// The store step refuses a value typed with an arrow in it, says why, and
// stores nothing.
func TestStoreStepRefusesAnEscapedValue(t *testing.T) {
	store, log := storeHelper(t)
	typeIt, finish := onPty(t)
	typeIt("s3cr3t\x1b[D-Value\r", false)
	var err error
	out := captureStdout(t, func() { err = storeSecret(testRef) })
	finish()
	if err == nil || !strings.Contains(err.Error(), "escape sequence") || !strings.Contains(err.Error(), "nothing was written") {
		t.Fatalf("store step: %v", err)
	}
	if _, ok := storedValue(store); ok || strings.Contains(readLog(t, log), "--store") {
		t.Fatal("an escaped value was stored")
	}
	if strings.Contains(out, "s3cr3t") || strings.Contains(err.Error(), "s3cr3t") {
		t.Fatal("the typed text was shown")
	}
}

// A failed --store says to store the value with your own tool (SPEC,
// "The helper interface").
func TestStoreStepFailedStoreNamesTheWayOut(t *testing.T) {
	storeHelper(t)
	t.Setenv("FAIL_STORE", "1")
	typeIt, finish := onPty(t)
	typeIt(testValue+"\r", false)
	var err error
	captureStdout(t, func() { err = storeSecret(testRef) })
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
