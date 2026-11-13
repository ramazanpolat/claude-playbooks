package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
)

// A label is key=value: a short key, and a one-line value of plain
// visible text that is never empty, an e-mail address or a credential (key
// and value checked together). A problem is refused naming a valid key,
// never a value, and never an invalid key (a value typed where the key
// goes).
func TestParseTypedLabel(t *testing.T) {
	if k, v, err := parseTypedLabel("task=review the auth PR"); err != nil || k != "task" || v != "review the auth PR" {
		t.Fatalf("a plain label: %q %q %v", k, v, err)
	}
	for _, ok := range []string{"commit=0123456789abcdef0123456789abcdef01234567", "lang=Türkçe", "emoji=ok 🎉", "ok=" + strings.Repeat("x", 200)} {
		if _, _, err := parseTypedLabel(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for in, want := range map[string]string{
		"no-equals":                        "no '='",
		"Task=x":                           "the label key is not valid (4 characters)",
		"a b=x":                            "the label key is not valid (3 characters)",
		strings.Repeat("k", 33) + "=x":     "the label key is not valid (33 characters)",
		"empty=":                           "is empty (--unset <key> removes a label)",
		"long=" + strings.Repeat("x", 201): "at most 200",
		"two=one\ntwo":                     "not plain visible text",
		"mail=ask someone@example.com":     "e-mail",
		"tok=sk-ant-api03-abcdefghijklmnopqrstuv": "credential",
		"password=hunter22":                       "credential (password=...)",
		"token=abc123":                            "credential (token=...)",
		"api_key=abc123":                          "credential (api_key=...)",
		"secret=abc123":                           "credential (secret=...)",
		"auth=Bearer abc123":                      "credential (auth=...)",
		"key=main":                                "credential (key=...)",
	} {
		_, _, err := parseTypedLabel(in)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", in, err, want)
		}
		if err == nil {
			continue
		}
		k, v, _ := strings.Cut(in, "=")
		if len(v) > 3 && strings.Contains(err.Error(), v) {
			t.Errorf("%q: the error quotes the value: %v", in, err)
		}
		if labelKeyProblem(k) != "" && strings.Contains(err.Error(), k) {
			t.Errorf("%q: the error echoes an invalid key: %v", in, err)
		}
	}
}

// A label value is plain visible text: one test per class of what is not.
func TestLabelValueInvisible(t *testing.T) {
	for class, rs := range map[string][]rune{
		"Cc, control":               {0x07, 0x1b, 0x7f, 0x85},
		"Cf, format":                {0x00AD, 0x061C, 0x200B, 0x200C, 0x200D, 0x200E, 0x202E, 0x2060, 0x2066, 0xFEFF},
		"tag characters":            {0xE0000, 0xE0001, 0xE0041, 0xE007F},
		"Zl, line separator":        {0x2028},
		"Zp, paragraph separator":   {0x2029},
		"Co, private use":           {0xE000, 0xF8FF, 0xF0000},
		"variation selectors":       {0xFE00, 0xFE0F, 0xE0100, 0xE01EF},
		"combining grapheme joiner": {0x034F},
		"Hangul fillers":            {0x115F, 0x1160, 0x3164, 0xFFA0},
		"Mongolian variation, MVS":  {0x180B, 0x180C, 0x180D, 0x180E, 0x180F},
	} {
		for _, r := range rs {
			if p := labelValueProblem("a" + string(r) + "b"); !strings.Contains(p, "not plain visible text") {
				t.Errorf("%s U+%04X: %q", class, r, p)
			}
		}
	}
	// Read from a file, the same: a labels file or a launch record holding
	// one is not read.
	if validLabels(map[string]string{"k": "a\u200bb"}) {
		t.Fatal("a zero-width space read from a file")
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
	// Over the caps together: refused before anything launches.
	var many []launchToken
	for i := 0; i < labelsPerSource+1; i++ {
		many = append(many, launchToken{flag: "--label", value: fmt.Sprintf("k%d=v", i)})
	}
	if _, err := launchLayers(many); err == nil || !strings.Contains(err.Error(), "--label: 17 labels; at most 16") {
		t.Fatalf("17 labels: %v", err)
	}
	if _, err := launchLayers(many[:labelsPerSource]); err != nil {
		t.Fatalf("16 labels: %v", err)
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
	// Inside a session, --session may name only that session: another one,
	// live or not, is refused (exit 1), and its labels are not touched.
	if code, _ := run(func() { sessionLabelSession = sidLive }, "via=id"); code != 0 || readSessionLabels(root, sidLive)["via"] != "id" {
		t.Fatalf("--session of its own session: exit %d", code)
	}
	other := exec.Command("sleep", "30")
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Process.Kill(); _ = other.Wait() })
	const sidOther = "55555555-5555-4555-8555-555555555555"
	writeSessionFile(t, cfg, other.Process.Pid, sidOther, root, "interactive", procStartOf(other.Process.Pid), time.Now())
	if code, _ := run(func() { sessionLabelSession = sidOther }, "via=id"); code != 1 || readSessionLabels(root, sidOther) != nil {
		t.Fatalf("--session of another session from inside one: exit %d", code)
	}
	// Outside any session, --session names a live session by its id; an id
	// of no live session, or of one not confirmed live, is refused (exit 1:
	// 3 stays "not in a session").
	if err := os.Remove(filepath.Join(cfg, "sessions", fmt.Sprintf("%d.json", parent))); err != nil {
		t.Fatal(err)
	}
	if code, _ := run(func() { sessionLabelSession = sidOther }, "via=id"); code != 0 || readSessionLabels(root, sidOther)["via"] != "id" {
		t.Fatalf("--session from outside: exit %d", code)
	}
	if code, _ := run(func() { sessionLabelSession = "99999999-9999-4999-8999-999999999999" }, "via=id"); code != 1 {
		t.Fatalf("--session of no live session: exit %d", code)
	}
	const unconfirmed = "44444444-4444-4444-8444-444444444444"
	writeSessionFile(t, cfg, parent, unconfirmed, root, "interactive", "", time.Now())
	if code, _ := run(func() { sessionLabelSession = unconfirmed }, "via=id"); code != 1 || readSessionLabels(root, unconfirmed) != nil {
		t.Fatalf("--session of an unconfirmed session: exit %d", code)
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

// SHOW SESSIONS: the SESSION column is always the id; the labels are the
// last column, so a value's spaces split nothing before it.
func TestSessionsTableLabels(t *testing.T) {
	if labelsCell(nil) != "-" || labelsCell(&blockLabelsJSON{}) != "-" {
		t.Fatal("no labels")
	}
	l := &blockLabelsJSON{Launch: map[string]string{"name": "review auth", "a": "1"}, Session: map[string]string{"task": "triage"}}
	if got := labelsCell(l); got != "launch a=1, name=review auth; session task=triage" {
		t.Fatalf("cell: %q", got)
	}
	root := sandboxDefaultRoot(t)
	writePlaybook(t, root, "alpha", nil)
	work, _ := filepath.EvalSymlinks(t.TempDir())
	writeSessionFile(t, filepath.Join(root, "alpha"), 301, sidLive, work, "interactive", liveStart, time.Now())
	fakeProcInfo(t, map[int]procInfo{301: {start: liveStart}})
	if _, err := updateSessionLabels(root, sidLive, map[string]string{"name": "fixing ci"}, nil); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(mustStmt(t, "SHOW SESSIONS")), "\n")
	head := strings.Fields(lines[0])
	row := lines[len(lines)-1]
	if head[len(head)-1] != "LABELS" || head[len(head)-3] != "SESSION" || !strings.Contains(row, " "+sidLive+" ") || !strings.HasSuffix(row, "session name=fixing ci") {
		t.Fatalf("table:\n%s", strings.Join(lines, "\n"))
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
	// The flag outranks an inherited root: the session gets the flag's.
	t.Setenv(config.PlaybooksDirEnv, "/from/env")
	if env := withPlaybooksRoot([]string{"A=1", config.PlaybooksDirEnv + "=/from/env"}); strings.Join(env, " ") != "A=1 "+want {
		t.Fatalf("a flag over an inherited root: %v", env)
	}
	config.PlaybooksDir = ""
	if env := withPlaybooksRoot([]string{"A=1", config.PlaybooksDirEnv + "=/from/env"}); strings.Join(env, " ") != "A=1 "+config.PlaybooksDirEnv+"=/from/env" {
		t.Fatalf("a root from the environment: %v", env)
	}
	t.Setenv(config.PlaybooksDirEnv, "")
	config.PlaybooksDir = ""
	if env := withPlaybooksRoot([]string{"A=1"}); strings.Join(env, " ") != "A=1" {
		t.Fatalf("the default root: %v", env)
	}
}

// Labels nobody wrote for 30 days, their lock, and a writer's leftover
// temporary file are swept with the rest of the usage files.
func TestSweepSessionLabels(t *testing.T) {
	root := t.TempDir()
	const other = "33333333-3333-4333-8333-333333333333"
	for _, sid := range []string{sidLive, other} {
		if _, err := updateSessionLabels(root, sid, map[string]string{"k": "v"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	tmp := filepath.Join(labelsDir(root), ".labels-123")
	if err := os.WriteFile(tmp, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	long := time.Now().Add(-31 * 24 * time.Hour)
	for _, p := range []string{labelsPath(root, other), filepath.Join(labelsDir(root), "."+other+".lock")} {
		if err := os.Chtimes(p, long, long); err != nil {
			t.Fatal(err)
		}
	}
	minute := time.Now().Add(-2 * time.Minute)
	if err := os.Chtimes(tmp, minute, minute); err != nil {
		t.Fatal(err)
	}
	// A session whose lock is old but whose labels were just written keeps
	// both: a write touches the lock.
	if err := os.Chtimes(filepath.Join(labelsDir(root), "."+sidLive+".lock"), long, long); err != nil {
		t.Fatal(err)
	}
	if _, err := updateSessionLabels(root, sidLive, map[string]string{"k": "w"}, nil); err != nil {
		t.Fatal(err)
	}
	sweepUsage(root, time.Now())
	ents, _ := os.ReadDir(labelsDir(root))
	var left []string
	for _, e := range ents {
		left = append(left, e.Name())
	}
	if strings.Join(left, " ") != "."+sidLive+".lock "+sidLive+".json" {
		t.Fatalf("left: %v", left)
	}
}

// A launch on a sandbox host checks its labels here, each and together,
// before any is printed or handed to ssh: run and start alike.
func TestSandboxHostChecksLabels(t *testing.T) {
	root := sandboxDefaultRoot(t)
	writePlaybook(t, root, "alpha", nil)
	bin := t.TempDir()
	marker := filepath.Join(bin, "ssh-ran")
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var many []string
	for i := 0; i < labelsPerSource+1; i++ {
		many = append(many, "--label", fmt.Sprintf("k%d=v", i))
	}
	dir := t.TempDir()
	for _, bad := range [][]string{
		{"--label", "owner=someone@example.com"},
		{"--label", "tok=sk-ant-api03-abcdefghijklmnopqrstuv"},
		{"--label", "password=hunter22"},
		{"--label", "line=a\x1b[2Jb"},
		many,
	} {
		for _, cmd := range []string{"run", "start"} {
			args := append([]string{"--sandbox-host", "u@example-host"}, bad...)
			var err error
			msg := captureStderr(t, func() {
				if cmd == "run" {
					err = runRun(nil, append(args, "alpha"))
				} else {
					err = runStart(nil, append(args, dir))
				}
			})
			if err == nil || !strings.Contains(err.Error(), "--label") || strings.Contains(msg, "Sandbox on") {
				t.Errorf("%s %q: %v %q", cmd, bad[1], err, msg)
			}
			if _, serr := os.Stat(marker); serr == nil {
				t.Fatalf("%s %q: ssh ran", cmd, bad[1])
			}
			for _, v := range []string{"someone@", "sk-ant-", "hunter22", "\x1b"} {
				if strings.Contains(msg, v) || (err != nil && strings.Contains(err.Error(), v)) {
					t.Errorf("%s: a value reached the output: %q %v", cmd, msg, err)
				}
			}
		}
	}
}
