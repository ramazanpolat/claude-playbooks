package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
)

// A label is key=value: a short key, and a one-line value that is never
// an e-mail address, a credential or a character a terminal does not show
// as written. A problem is refused, naming the key, never the value.
func TestParseTypedLabel(t *testing.T) {
	if k, v, err := parseTypedLabel("task=review the auth PR"); err != nil || k != "task" || v != "review the auth PR" {
		t.Fatalf("a plain label: %q %q %v", k, v, err)
	}
	if _, v, err := parseTypedLabel("empty="); err != nil || v != "" {
		t.Fatalf("an empty value: %q %v", v, err)
	}
	for in, want := range map[string]string{
		"no-equals":                        "no '='",
		"Task=x":                           "a label key is",
		"a b=x":                            "a label key is",
		strings.Repeat("k", 33) + "=x":     "a label key is",
		"long=" + strings.Repeat("x", 201): "at most 200",
		"two=one\ntwo":                     "control, direction or invisible",
		"bidi=a‮b":                         "control, direction or invisible",
		"mail=ask someone@example.com":     "e-mail",
		"key=sk-ant-api03-abcdefghijklmnopqrstuv": "credential",
	} {
		_, _, err := parseTypedLabel(in)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", in, err, want)
		}
		if err != nil && strings.Contains(in, "=") {
			if _, v, _ := strings.Cut(in, "="); len(v) > 3 && strings.Contains(err.Error(), v) {
				t.Errorf("%q: the error quotes the value: %v", in, err)
			}
		}
	}
	if _, _, err := parseTypedLabel("ok=" + strings.Repeat("x", 200)); err != nil {
		t.Fatalf("200 bytes: %v", err)
	}
}

// At most 16 labels per source, and 4 KiB together.
func TestLabelSetCaps(t *testing.T) {
	m := map[string]string{}
	for i := 0; i < 16; i++ {
		m[fmt.Sprintf("k%d", i)] = "v"
	}
	if err := checkLabelSet(m); err != nil {
		t.Fatalf("16: %v", err)
	}
	m["k16"] = "v"
	if err := checkLabelSet(m); err == nil {
		t.Fatal("17 labels accepted")
	}
	big := map[string]string{}
	for i := 0; i < 16; i++ {
		big[fmt.Sprintf("k%d", i)] = strings.Repeat("x", 200)
	}
	if err := checkLabelSet(big); err != nil {
		t.Fatalf("16 x 200 bytes: %v", err)
	}
}

// --label is a launch flag: checked when parsed, carried as a token, and
// never part of the environment.
func TestLaunchLabelFlag(t *testing.T) {
	rest, tokens, err := scanLaunchFlagsWith([]string{"--label", "task=a", "--label=name=x", "alpha"}, nil)
	if err != nil || len(rest) != 1 || rest[0] != "alpha" {
		t.Fatalf("%v %v %v", rest, tokens, err)
	}
	if got := launchLabelsOf(tokens); got["task"] != "a" || got["name"] != "x" {
		t.Fatalf("labels: %v", got)
	}
	layers, err := launchLayers(tokens)
	if err != nil || len(layers) != 2 || !layers[0].Empty() {
		t.Fatalf("layers: %v %v", layers, err)
	}
	if _, err := launchLayers([]launchToken{{flag: "--label", value: "mail=me@example.com"}}); err == nil || !strings.Contains(err.Error(), "--label") {
		t.Fatalf("a bad label: %v", err)
	}
	if got := launchLabelsOf([]launchToken{{flag: "--label", value: "k=1"}, {flag: "--label", value: "k=2"}}); got["k"] != "2" {
		t.Fatalf("a later label for a key wins: %v", got)
	}
}

// The labels a launch carries travel on every rebuilt launch: the failover
// relaunch and the way back, the by-hand line, and a sandbox host.
func TestLaunchLabelsTravel(t *testing.T) {
	tokens := []launchToken{{flag: "--label", value: "task=a b"}, {flag: "--account", value: "acc-a"}}
	sw := &failoverSwitch{From: "acc-a", To: "acc-b", sessionID: sidLive}
	if args := relaunchArgs(nil, tokens, "alpha", nil, sw); !strings.Contains(strings.Join(args, "\x00"), "--label=task=a b") {
		t.Fatalf("the relaunch: %q", args)
	}
	back := relaunchArgs(nil, tokens, "alpha", nil, &failoverSwitch{From: "acc-b", To: "acc-a", sessionID: sidLive, noPrompt: true})
	if line, _ := resumeByHand("/w", back); !strings.Contains(line, "'--label=task=a b'") {
		t.Fatalf("the by-hand line: %q", line)
	}
	root := sandboxDefaultRoot(t)
	writePlaybook(t, root, "alpha", nil)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var err error
	msg := captureStderr(t, func() { err = runRun(nil, []string{"--sandbox-host", "u@example-host", "--label", "task=x", "alpha"}) })
	if err != nil || !strings.Contains(msg, "--label=task=x") {
		t.Fatalf("a sandbox host: %v %q", err, msg)
	}
}

// Session labels: set, unset, listed; the file is 0600 and holds only what
// cpb wrote.
func TestSessionLabelsFile(t *testing.T) {
	root := t.TempDir()
	got, err := updateSessionLabels(root, sidLive, map[string]string{"task": "a", "name": "n"}, nil)
	if err != nil || got["task"] != "a" || got["name"] != "n" {
		t.Fatalf("set: %v %v", got, err)
	}
	if fi, err := os.Stat(labelsPath(root, sidLive)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode: %v %v", fi, err)
	}
	got, err = updateSessionLabels(root, sidLive, map[string]string{"task": "b"}, []string{"name"})
	if err != nil || got["task"] != "b" || got["name"] != "" || len(readSessionLabels(root, sidLive)) != 1 {
		t.Fatalf("set and unset: %v %v", got, err)
	}
	if _, err := updateSessionLabels(root, sidLive, nil, []string{"task"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(labelsPath(root, sidLive)); !os.IsNotExist(err) {
		t.Fatalf("no labels left, yet a file: %v", err)
	}
	// A hand-edited file with a label cpb would not write is not read.
	b, _ := json.Marshal(sessionLabelsFile{V: 1, SessionID: sidLive, Labels: map[string]string{"k": "a\x1bb"}})
	if err := os.WriteFile(labelsPath(root, sidLive), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readSessionLabels(root, sidLive); got != nil {
		t.Fatalf("read a hand-edited label: %v", got)
	}
}

// Two writers of one session keep both their labels; a writer that cannot
// get the lock in time says so, without waiting long.
func TestSessionLabelsLock(t *testing.T) {
	root := t.TempDir()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := updateSessionLabels(root, sidLive, map[string]string{fmt.Sprintf("k%d", i): "v"}, nil); err != nil {
				t.Errorf("writer %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	if got := readSessionLabels(root, sidLive); len(got) != 8 {
		t.Fatalf("labels: %v", got)
	}
	lock, err := os.OpenFile(filepath.Join(labelsDir(root), "."+sidLive+".lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := updateSessionLabels(root, sidLive, map[string]string{"x": "y"}, nil); !errors.Is(err, errLabelsBusy) || time.Since(start) > time.Second {
		t.Fatalf("held lock: %v after %v", err, time.Since(start))
	}
}

// cpb session label finds the session it runs in from its ancestry: a
// session file of an ancestor with a matching start time. A start time
// that does not match, or no session at all, is "not a cpb session",
// with its own exit code.
func TestSessionLabelFindsTheCallersSession(t *testing.T) {
	root := sandboxDefaultRoot(t)
	writePlaybook(t, root, "alpha", nil)
	cfg := filepath.Join(root, "alpha")
	parent := os.Getppid()
	start := procStartOf(parent)
	if start == "" {
		t.Skip("no process start times here")
	}
	run := func(flags func(), args ...string) (int, string) {
		t.Helper()
		sessionLabelUnset, sessionLabelList, sessionLabelJSON, sessionLabelSession = nil, false, false, ""
		if flags != nil {
			flags()
		}
		var err error
		var out string
		captureStderr(t, func() { out = captureStdout(t, func() { err = runSessionLabel(nil, args) }) })
		if code, ok := exitCode(err); ok {
			return code, out
		}
		if err != nil {
			return 1, err.Error()
		}
		return 0, out
	}
	if code, _ := run(nil, "task=a"); code != exitNotASession {
		t.Fatalf("no session: exit %d", code)
	}
	writeSessionFile(t, cfg, parent, sidLive, root, "interactive", "Mon Jan 1 00:00:00 2001", time.Now())
	if code, _ := run(nil, "task=a"); code != exitNotASession {
		t.Fatalf("another start time: exit %d", code)
	}
	writeSessionFile(t, cfg, parent, sidLive, root, "interactive", start, time.Now())
	if code, out := run(nil, "task=a"); code != 0 {
		t.Fatalf("its session: exit %d %s", code, out)
	}
	if got := readSessionLabels(root, sidLive); got["task"] != "a" {
		t.Fatalf("labels: %v", got)
	}
	if code, out := run(func() { sessionLabelList, sessionLabelJSON = true, true }, "name=n"); code != 0 ||
		strings.TrimSpace(out) != `{"session_id":"`+sidLive+`","labels":{"name":"n","task":"a"}}` {
		t.Fatalf("--list --json: exit %d %s", code, out)
	}
	if code, out := run(func() { sessionLabelList = true; sessionLabelUnset = []string{"name"} }); code != 0 || out != "task=a\n" {
		t.Fatalf("--unset --list: exit %d %q", code, out)
	}
	if code, _ := run(nil, "bad key=x"); code != 1 {
		t.Fatalf("a bad label: exit %d", code)
	}
	// --session names a live session instead of the caller's.
	if code, _ := run(func() { sessionLabelSession = sidLive }, "via=id"); code != 0 || readSessionLabels(root, sidLive)["via"] != "id" {
		t.Fatalf("--session: exit %d", code)
	}
	if code, _ := run(func() { sessionLabelSession = "99999999-9999-4999-8999-999999999999" }, "via=id"); code != exitNotASession {
		t.Fatalf("--session of no live session: exit %d", code)
	}
}

// The labels reach the status line's cpb key and the usage log; a label
// set later is in the next line, not an earlier one.
func TestLabelsInTheStatusLine(t *testing.T) {
	root, rec := stepFixture(t, "work", "work", nil)
	rec.Labels = map[string]string{"task": "launch-task"}
	dir := launchRecordsDir(root)
	if err := writeLaunchRecord(dir, filepath.Join(dir, rec.LaunchID+".json"), &rec); err != nil {
		t.Fatal(err)
	}
	a := stepArgs{root: root, launch: rec.LaunchID, original: "true"}
	in := statusInput(stepSID, 10, 1, 0.1)
	out := stepRecord(a, in)
	if !strings.Contains(string(out), `"labels":{"launch":{"task":"launch-task"}}`) {
		t.Fatalf("the cpb key: %s", out)
	}
	if _, err := updateSessionLabels(root, stepSID, map[string]string{"name": "fixing ci"}, nil); err != nil {
		t.Fatal(err)
	}
	out = stepRecord(a, in)
	if !strings.Contains(string(out), `"labels":{"launch":{"task":"launch-task"},"session":{"name":"fixing ci"}}`) {
		t.Fatalf("the cpb key with a session label: %s", out)
	}
	b, _ := os.ReadFile(sessionLogPath(root, stepSID, time.Now()))
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 || strings.Contains(lines[0], "fixing ci") || !strings.Contains(lines[1], `"session_labels":{"name":"fixing ci"}`) ||
		!strings.Contains(lines[0], `"launch_labels":{"task":"launch-task"}`) {
		t.Fatalf("the log:\n%s", b)
	}
}

// SHOW SESSIONS: labels in --json; the SESSION column shows the name label.
func TestSessionShownByName(t *testing.T) {
	r := sessionJSON{SessionID: sidLive}
	if sessionShown(r) != sidLive {
		t.Fatal("no labels")
	}
	r.Labels = &blockLabelsJSON{Launch: map[string]string{"name": "from launch"}}
	if sessionShown(r) != "from launch" {
		t.Fatal("launch name")
	}
	r.Labels.Session = map[string]string{"name": "from session"}
	if sessionShown(r) != "from session" {
		t.Fatal("session name first")
	}
}

// A root from --playbooks-dir reaches the session as CPB_PLAYBOOKS_DIR,
// absolute; a root from the environment, or the default, adds nothing.
func TestPlaybooksRootReachesTheSession(t *testing.T) {
	resetCommandTestState(t)
	work := t.TempDir()
	t.Chdir(work)
	t.Setenv(config.PlaybooksDirEnv, "")
	config.PlaybooksDir = "rel/root"
	env := withPlaybooksRoot([]string{"A=1", config.PlaybooksDirEnv + "=stale"})
	want := config.PlaybooksDirEnv + "=" + filepath.Join(work, "rel", "root")
	if strings.Join(env, " ") != "A=1 "+want {
		t.Fatalf("env: %v", env)
	}
	t.Setenv(config.PlaybooksDirEnv, "/from/env")
	if env := withPlaybooksRoot([]string{"A=1"}); strings.Join(env, " ") != "A=1" {
		t.Fatalf("a root from the environment: %v", env)
	}
	t.Setenv(config.PlaybooksDirEnv, "")
	config.PlaybooksDir = ""
	if env := withPlaybooksRoot([]string{"A=1"}); strings.Join(env, " ") != "A=1" {
		t.Fatalf("the default root: %v", env)
	}
}
