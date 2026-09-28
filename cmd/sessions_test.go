package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// Sessions are faked: session files written as Claude Code writes them, and
// procStarts answering for the pids the test says are alive. No real
// process is involved (the arena check sessions-ok uses real ones).

const (
	sidLive  = "11111111-1111-4111-8111-111111111111"
	sidDead  = "22222222-2222-4222-8222-222222222222"
	sidReuse = "33333333-3333-4333-8333-333333333333"
	sidOld   = "44444444-4444-4444-8444-444444444444"
	sidSpare = "55555555-5555-4555-8555-555555555555"
)

// fakeProcs makes the given pids alive with the given start times.
func fakeProcs(t *testing.T, alive map[int]string) {
	t.Helper()
	old := procStarts
	procStarts = func(pids []int) map[int]string {
		out := map[int]string{}
		for _, p := range pids {
			if s, ok := alive[p]; ok {
				out[p] = s
			}
		}
		return out
	}
	t.Cleanup(func() { procStarts = old })
}

func writeSessionFile(t *testing.T, configDir string, pid int, id, cwd, kind, procStart string, started time.Time) string {
	t.Helper()
	dir := filepath.Join(configDir, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{
		"pid": pid, "sessionId": id, "cwd": cwd, "startedAt": started.UnixMilli(), "procStart": procStart,
		"version": "2.1.283", "kind": kind, "entrypoint": "cli", "pidDomain": pidDomain,
		"name": "s-" + id[:4], "status": "idle", "messagingSocketPath": "/tmp/x.sock",
	})
	p := filepath.Join(dir, fmt.Sprintf("%d.json", pid))
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeTranscript(t *testing.T, configDir, cwd, id, model, title string, mtime time.Time) string {
	t.Helper()
	dir := filepath.Join(configDir, "projects", encodeProjectDir(cwd))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		fmt.Sprintf(`{"type":"user","cwd":%q,"sessionId":%q,"message":{"role":"user","content":"hi"}}`, cwd, id),
		fmt.Sprintf(`{"type":"assistant","cwd":%q,"sessionId":%q,"message":{"model":%q,"role":"assistant"}}`, cwd, id, model),
		`{"type":"assistant","message":{"model":"<synthetic>"}}`,
	}
	if title != "" {
		lines = append(lines, fmt.Sprintf(`{"type":"ai-title","aiTitle":%q,"sessionId":%q}`, title, id))
	}
	p := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return p
}

const liveStart = "Mon Sep 28 07:29:55 2026"

// sessionFixture: playbook alpha (launcher "al") with a live session, a dead
// one, a reused pid and a spare; beta with an older dead session in the
// same folder.
func sessionFixture(t *testing.T) (root, work string, files []string) {
	t.Helper()
	root = sandboxDefaultRoot(t)
	writePlaybook(t, root, "alpha", &manifest.Manifest{Alias: "al"})
	writePlaybook(t, root, "beta", nil)
	work, _ = filepath.EvalSymlinks(t.TempDir())
	a, b := filepath.Join(root, "alpha"), filepath.Join(root, "beta")
	now := time.Now()
	files = []string{
		writeSessionFile(t, a, 101, sidLive, work, "interactive", liveStart, now.Add(-time.Hour)),
		writeSessionFile(t, a, 102, sidDead, work, "interactive", liveStart, now.Add(-2*time.Hour)),
		writeSessionFile(t, a, 103, sidReuse, work, "interactive", liveStart, now.Add(-3*time.Hour)),
		writeSessionFile(t, a, 104, sidSpare, work, "spare", liveStart, now.Add(-4*time.Hour)),
	}
	fakeProcs(t, map[int]string{101: liveStart, 103: "Tue Sep 29 01:00:00 2026", 104: liveStart})
	writeTranscript(t, a, work, sidLive, "claude-opus-5-5", "live one", now.Add(-time.Minute))
	writeTranscript(t, a, work, sidDead, "claude-sonnet-5", "dead one", now.Add(-10*time.Minute))
	writeTranscript(t, b, work, sidOld, "claude-haiku-4-5", "", now.Add(-2*time.Hour))
	return root, work, files
}

func fileBytes(t *testing.T, paths []string) map[string]string {
	t.Helper()
	m := map[string]string{}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		m[p] = string(b)
	}
	return m
}

func TestShowSessionsListsOnlyLiveOnes(t *testing.T) {
	root, work, files := sessionFixture(t)
	before := fileBytes(t, files)
	out, err := stmt(t, "SHOW SESSIONS --json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(rows) != 1 {
		t.Fatalf("want only the live session (not dead, reused pid or spare):\n%s", out)
	}
	r := rows[0]
	want := map[string]any{"playbook": "alpha", "pid": float64(101), "session_id": sidLive, "cwd": work, "kind": "interactive",
		"launcher": "al", "model": "claude-opus-5-5", "claude_version": "2.1.283", "status": "idle",
		"config_dir": filepath.Join(root, "alpha"), "resume": "al --resume " + sidLive}
	for k, v := range want {
		if !reflect.DeepEqual(r[k], v) {
			t.Errorf("%s = %v, want %v", k, r[k], v)
		}
	}
	if la, _ := r["last_active"].(string); la == "" || !strings.HasSuffix(la, "Z") {
		t.Errorf("last_active = %v", r["last_active"])
	}
	// The shorthand is exactly the statement.
	short := captureStdout(t, func() {
		rootCmd.SetArgs([]string{"sessions", "--json"})
		err = rootCmd.Execute()
	})
	rootCmd.SetArgs(nil)
	if err != nil || short != out {
		t.Fatalf("cpb sessions --json differs (%v):\n%s\nvs\n%s", err, short, out)
	}
	human := mustStmt(t, "SHOW SESSIONS")
	if !strings.Contains(human, "PLAYBOOK") || !strings.Contains(human, sidLive) || strings.Contains(human, sidDead) {
		t.Fatalf("human form:\n%s", human)
	}
	// cpb never touches Claude Code's session files, stale ones included.
	if after := fileBytes(t, files); !reflect.DeepEqual(before, after) {
		t.Fatal("a session file changed")
	}
}

func TestShowSessionsForPlaybook(t *testing.T) {
	sessionFixture(t)
	if out := mustStmt(t, "SHOW SESSIONS FOR PLAYBOOK beta --json"); strings.TrimSpace(out) != "[]" {
		t.Fatalf("beta has no live session:\n%s", out)
	}
	if out := mustStmt(t, "SHOW SESSIONS FOR PLAYBOOK alpha --json"); !strings.Contains(out, sidLive) {
		t.Fatalf("alpha:\n%s", out)
	}
	if _, err := stmt(t, "SHOW SESSIONS FOR PLAYBOOK nope"); err == nil {
		t.Fatal("an unknown playbook is refused")
	}
}

func TestSelectFromSessions(t *testing.T) {
	sessionFixture(t)
	var got []map[string]any
	var err error
	js := captureStdout(t, func() { err = runStatement([]string{"SELECT session_id, pid FROM SESSIONS", "--json"}) })
	if err != nil || json.Unmarshal([]byte(js), &got) != nil {
		t.Fatalf("%v\n%s", err, js)
	}
	if len(got) != 1 || got[0]["session_id"] != sidLive || got[0]["pid"] != float64(101) {
		t.Fatalf("%v", got)
	}
	cols, err := describeTable("sessions")
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]string{}
	for _, c := range cols {
		types[c.Name] = c.Type
	}
	if types["started_at"] != "DateTime64(3, 'UTC')" || types["pid"] != "UInt32" || types["last_active"] != "Nullable(DateTime64(3, 'UTC'))" {
		t.Fatalf("types: %v", types)
	}
	sp := &selectPlan{table: "SESSIONS", query: "SELECT 1"}
	pp := &selectPlan{table: "PLAYBOOKS", query: "SELECT 1"}
	if !strings.Contains(strings.Join(sp.clickhouseArgs(), " "), "--date_time_input_format best_effort") ||
		strings.Contains(strings.Join(pp.clickhouseArgs(), " "), "date_time_input_format") {
		t.Fatal("best_effort only for a table with DateTime columns")
	}
}

// resumeClaude puts a fake claude on PATH that records its working
// directory, config dir and arguments.
func resumeClaude(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	script := "#!/bin/sh\nprintf '%s|%s|%s\\n' \"$(pwd -P)\" \"$CLAUDE_CONFIG_DIR\" \"$*\" >> " + shellQuoteTest(log) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func shellQuoteTest(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func readSessLog(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

func chdirT(t *testing.T, dir string) {
	t.Helper()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
}

func TestResumeRefusesALiveSession(t *testing.T) {
	_, work, files := sessionFixture(t)
	log := resumeClaude(t)
	chdirT(t, work)
	before := fileBytes(t, files)
	var err error
	captureStderr(t, func() { err = runStatement([]string{"RESUME", "SESSION", sidLive}) })
	if err == nil || !strings.Contains(err.Error(), "still running") || !strings.Contains(err.Error(), "pid 101") || !strings.Contains(err.Error(), "alpha") {
		t.Fatalf("live: %v", err)
	}
	if l := readSessLog(t, log); l != "" {
		t.Fatalf("claude ran: %s", l)
	}
	if after := fileBytes(t, files); !reflect.DeepEqual(before, after) {
		t.Fatal("a session file changed")
	}
}

func TestResumePicksTheNewestNotLive(t *testing.T) {
	root, work, files := sessionFixture(t)
	log := resumeClaude(t)
	elsewhere := t.TempDir()
	chdirT(t, work)
	var err error
	msg := captureStderr(t, func() { err = runStatement([]string{"RESUME"}) })
	if err != nil {
		t.Fatalf("%v\n%s", err, msg)
	}
	if !strings.Contains(msg, "1 newer session is live (pid 101); resuming "+sidDead+" of alpha") {
		t.Fatalf("the pick is named: %s", msg)
	}
	realWork := work
	if l := readSessLog(t, log); l != realWork+"|"+filepath.Join(root, "alpha")+"|--resume "+sidDead+"\n" {
		t.Fatalf("launch: %q", l)
	}
	// SESSION from another folder: found by id, run where it ran.
	chdirT(t, elsewhere)
	_ = os.Remove(log)
	msg = captureStderr(t, func() { err = runStatement([]string{"RESUME", "SESSION", sidOld}) })
	if err != nil {
		t.Fatalf("%v\n%s", err, msg)
	}
	if l := readSessLog(t, log); l != realWork+"|"+filepath.Join(root, "beta")+"|--resume "+sidOld+"\n" || !strings.Contains(msg, "where the session ran") {
		t.Fatalf("launch: %q\n%s", l, msg)
	}
	for _, f := range files {
		if _, err := os.Stat(f); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
}

func TestResumeAmbiguousAndMissing(t *testing.T) {
	root, work, _ := sessionFixture(t)
	resumeClaude(t)
	chdirT(t, work)
	// The same id copied into beta.
	writeTranscript(t, filepath.Join(root, "beta"), work, sidDead, "m", "", time.Now())
	if _, err := stmt(t, "RESUME SESSION "+sidDead); err == nil || !strings.Contains(err.Error(), "more than one config dir") {
		t.Fatalf("ambiguous: %v", err)
	}
	var err error
	captureStderr(t, func() { err = runStatement([]string{"RESUME", "SESSION", sidDead, "FOR", "PLAYBOOK", "beta"}) })
	if err != nil {
		t.Fatalf("FOR PLAYBOOK picks one: %v", err)
	}
	if _, err := stmt(t, "RESUME SESSION 99999999-9999-4999-8999-999999999999"); err == nil || !strings.Contains(err.Error(), "no session") {
		t.Fatalf("missing: %v", err)
	}
	chdirT(t, t.TempDir())
	if _, err := stmt(t, "RESUME"); err == nil || !strings.Contains(err.Error(), "no Claude Code session was found") {
		t.Fatalf("empty folder: %v", err)
	}
}

func TestResumeList(t *testing.T) {
	_, work, _ := sessionFixture(t)
	chdirT(t, work)
	out := mustStmt(t, "RESUME --list --json")
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 3 {
		t.Fatalf("%v\n%s", err, out)
	}
	if rows[0]["session_id"] != sidLive || rows[0]["live"] != true || rows[0]["pid"] != float64(101) || rows[0]["title"] != "live one" {
		t.Fatalf("newest first, live marked: %v", rows[0])
	}
	if rows[1]["session_id"] != sidDead || rows[1]["live"] != false || rows[2]["playbook"] != "beta" {
		t.Fatalf("%v", rows)
	}
	human := mustStmt(t, "RESUME --list")
	if !strings.Contains(human, "pid 101") || !strings.Contains(human, "beta") {
		t.Fatalf("human:\n%s", human)
	}
}

func TestResumeGrammarRefusals(t *testing.T) {
	sessionFixture(t)
	for line, want := range map[string]string{
		"RESUME --list SESSION " + sidLive: "use one of them",
		"RESUME --json":                    "only with --list",
		"RESUME SESSION ../x":              "session id",
		"RESUME FOR alpha":                 "FOR PLAYBOOK",
	} {
		if _, err := stmt(t, line); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", line, err)
		}
	}
}

func TestResumeRefusesASandboxedPlaybook(t *testing.T) {
	root := sandboxDefaultRoot(t)
	writePlaybook(t, root, "sb", &manifest.Manifest{Sandbox: &manifest.Sandbox{Always: true}})
	work, _ := filepath.EvalSymlinks(t.TempDir())
	writeTranscript(t, filepath.Join(root, "sb"), work, sidOld, "m", "", time.Now())
	fakeProcs(t, nil)
	log := resumeClaude(t)
	chdirT(t, work)
	var err error
	captureStderr(t, func() { err = runStatement([]string{"RESUME"}) })
	if err == nil || !strings.Contains(err.Error(), "sandbox") || readSessLog(t, log) != "" {
		t.Fatalf("sandboxed: %v", err)
	}
}

// The exit line: after claude exits under cpb run, on a terminal, cpb names
// the launcher command that resumes the session.
func TestRunPrintsTheResumeLine(t *testing.T) {
	root := sandboxDefaultRoot(t)
	writePlaybook(t, root, "alpha", &manifest.Manifest{Alias: "al"})
	fakeProcs(t, nil)
	dir := t.TempDir()
	script := "#!/bin/sh\nd=\"$CLAUDE_CONFIG_DIR/projects/-w\"\nmkdir -p \"$d\"\necho '{}' > \"$d/" + sidLive + ".jsonl\"\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	tty := true
	old := stderrTTY
	stderrTTY = func() bool { return tty }
	t.Cleanup(func() { stderrTTY = old })

	var err error
	msg := captureStderr(t, func() { err = runRun(nil, []string{"alpha"}) })
	if err != nil || !strings.Contains(msg, "Resume this playbook's session with: al --resume "+sidLive) {
		t.Fatalf("%v\n%s", err, msg)
	}
	msg = captureStderr(t, func() { err = runRun(nil, []string{"alpha", "-p", "hi"}) })
	if err != nil || strings.Contains(msg, "Resume this") {
		t.Fatalf("print mode: %v\n%s", err, msg)
	}
	tty = false
	msg = captureStderr(t, func() { err = runRun(nil, []string{"alpha"}) })
	if err != nil || strings.Contains(msg, "Resume this") {
		t.Fatalf("no tty: %v\n%s", err, msg)
	}
}

func TestEncodeProjectDir(t *testing.T) {
	if got := encodeProjectDir("/Users/polat/agent-realm/.worktrees/agentmux/root"); got != "-Users-polat-agent-realm--worktrees-agentmux-root" {
		t.Fatal(got)
	}
}

// procStarts on this platform reads a live pid's start time, and none for a
// dead one.
func TestProcStartsReal(t *testing.T) {
	if _, err := exec.LookPath("ps"); err != nil && runtime.GOOS != "linux" {
		t.Skip("no ps")
	}
	got := procStarts([]int{os.Getpid(), 1 << 30})
	if got[os.Getpid()] == "" || got[1<<30] != "" {
		t.Fatalf("%v", got)
	}
	v := got[os.Getpid()]
	if runtime.GOOS == "linux" {
		if _, err := strconv.ParseUint(v, 10, 64); err != nil {
			t.Fatalf("not /proc/<pid>/stat's starttime: %q", v)
		}
	} else if _, err := time.Parse("Mon Jan 2 15:04:05 2006", v); err != nil {
		t.Fatalf("not lstart's format: %q", v)
	}
}
