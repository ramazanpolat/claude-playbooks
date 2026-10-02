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
	info := map[int]procInfo{}
	for p, s := range alive {
		info[p] = procInfo{start: s}
	}
	fakeProcInfo(t, info)
}

func fakeProcInfo(t *testing.T, alive map[int]procInfo) {
	t.Helper()
	old := procStarts
	procStarts = func(pids []int) map[int]procInfo {
		out := map[int]procInfo{}
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
		"config_dir": filepath.Join(root, "alpha"), "resume": "cd " + shellQuoteTest(work) + " && al --resume " + sidLive}
	for k, v := range want {
		if !reflect.DeepEqual(r[k], v) {
			t.Errorf("%s = %v, want %v", k, r[k], v)
		}
	}
	if la, _ := r["last_active"].(string); la == "" || !strings.HasSuffix(la, "Z") {
		t.Errorf("last_active = %v", r["last_active"])
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
	ep := &selectPlan{table: "ENVS", query: "SELECT 1"}
	if !strings.Contains(strings.Join(sp.clickhouseArgs(), " "), "--date_time_input_format best_effort") ||
		strings.Contains(strings.Join(ep.clickhouseArgs(), " "), "date_time_input_format") {
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

// runClaude runs cpb run with args against the fake claude of resumeClaude,
// and returns what claude logged, cpb's stderr and its error.
func runClaude(t *testing.T, log string, args ...string) (string, string, error) {
	t.Helper()
	_ = os.Remove(log)
	var err error
	msg := captureStderr(t, func() { err = runRun(nil, args) })
	return readSessLog(t, log), msg, err
}

func TestResumeTarget(t *testing.T) {
	for _, c := range []struct {
		args []string
		id   string
		cont bool
	}{
		{nil, "", false},
		{[]string{"--resume", sidLive}, sidLive, false},
		{[]string{"-r", sidLive, "-p", "hi"}, sidLive, false},
		{[]string{"--resume=" + sidLive}, sidLive, false},
		{[]string{"--resume"}, "", false},                  // Claude's picker
		{[]string{"--resume", "release notes"}, "", false}, // a search term
		{[]string{"--resume", "auth"}, "", false},          // one word is a search term too
		{[]string{"--resume=auth"}, "", false},
		{[]string{"--resume", sidLive, "--resume"}, "", false}, // the last --resume wins: the picker
		{[]string{"-r", sidOld, "--resume=" + sidLive}, sidLive, false},
		{[]string{"-c"}, "", true},
		{[]string{"--continue", "--model", "opus"}, "", true},
		{[]string{"--resume", sidLive, "--fork-session"}, "", false},
		{[]string{"--continue", "--fork-session"}, "", false},
		{[]string{"--", "--resume", sidLive}, "", false}, // a prompt
	} {
		if id, cont := resumeTarget(c.args); id != c.id || cont != c.cont {
			t.Errorf("%q: %q %v, want %q %v", c.args, id, cont, c.id, c.cont)
		}
	}
}

// cpb run and the launchers refuse a --resume of a session that is live in
// any config dir, before claude starts, and name the picker; a fork makes a
// new id, so it runs.
func TestRunRefusesResumingALiveSession(t *testing.T) {
	root, work, files := sessionFixture(t)
	log := resumeClaude(t)
	chdirT(t, work)
	before := fileBytes(t, files)
	for _, args := range [][]string{
		{"alpha", "--resume", sidLive}, {"alpha", "-r", sidLive}, {"alpha", "--resume=" + sidLive},
		{"beta", "--resume", sidLive}, // a copy in another dir is still the live session
	} {
		l, _, err := runClaude(t, log, args...)
		if err == nil || !strings.Contains(err.Error(), "still running") || !strings.Contains(err.Error(), "pid 101") ||
			!strings.Contains(err.Error(), "playbook alpha") || !strings.Contains(err.Error(), "pick another with ") || l != "" {
			t.Fatalf("%q: %v (claude: %q)", args, err, l)
		}
	}
	if _, _, err := runClaude(t, log, "alpha", "--resume", sidLive); !strings.Contains(err.Error(), "pick another with al --resume") {
		t.Errorf("the picker is the launcher's: %v", err)
	}
	if after := fileBytes(t, files); !reflect.DeepEqual(before, after) {
		t.Fatal("a session file changed")
	}
	if l, msg, err := runClaude(t, log, "alpha", "--resume", sidLive, "--fork-session"); err != nil || l != work+"|"+filepath.Join(root, "alpha")+"|--resume "+sidLive+" --fork-session\n" {
		t.Fatalf("fork: %v %q\n%s", err, l, msg)
	}
	// Not live, or not known to cpb: claude gets it as it is.
	for _, id := range []string{sidDead, "99999999-9999-4999-8999-999999999999"} {
		if l, msg, err := runClaude(t, log, "alpha", "--resume", id); err != nil || l != work+"|"+filepath.Join(root, "alpha")+"|--resume "+id+"\n" {
			t.Fatalf("%s: %v %q\n%s", id, err, l, msg)
		}
	}
}

// --continue resumes the newest session of the playbook in this folder;
// cpb refuses it when that one is live.
func TestRunGuardsContinue(t *testing.T) {
	root, work, _ := sessionFixture(t)
	log := resumeClaude(t)
	chdirT(t, work)
	if l, _, err := runClaude(t, log, "alpha", "--continue"); err == nil || !strings.Contains(err.Error(), "--continue resumes the newest session in this folder") ||
		!strings.Contains(err.Error(), sidLive) || l != "" {
		t.Fatalf("alpha's newest is live: %v (claude: %q)", err, l)
	}
	if l, msg, err := runClaude(t, log, "alpha", "-c", "--fork-session"); err != nil || l == "" {
		t.Fatalf("fork: %v %q\n%s", err, l, msg)
	}
	if l, msg, err := runClaude(t, log, "beta", "-c"); err != nil || l != work+"|"+filepath.Join(root, "beta")+"|-c\n" {
		t.Fatalf("beta's newest is not live: %v %q\n%s", err, l, msg)
	}
	chdirT(t, t.TempDir())
	if l, msg, err := runClaude(t, log, "alpha", "--continue"); err != nil || l == "" {
		t.Fatalf("no session here: %v %q\n%s", err, l, msg)
	}
}

// Claude Code finds a session by the folder it ran in: a --resume from
// elsewhere is refused with the cd that resumes it, and runs in its folder.
func TestRunResumeFromAnotherFolder(t *testing.T) {
	root, work, _ := sessionFixture(t)
	log := resumeClaude(t)
	chdirT(t, t.TempDir())
	want := "cd " + shellQuoteTest(work) + " && claude-playbook run beta --resume " + sidOld
	if l, _, err := runClaude(t, log, "beta", "--resume", sidOld); err == nil || !strings.Contains(err.Error(), "ran in "+work) || !strings.Contains(err.Error(), want) || l != "" {
		t.Fatalf("elsewhere: %v (claude: %q), want %q", err, l, want)
	}
	chdirT(t, work)
	if l, msg, err := runClaude(t, log, "beta", "--resume", sidOld); err != nil || l != work+"|"+filepath.Join(root, "beta")+"|--resume "+sidOld+"\n" {
		t.Fatalf("here: %v %q\n%s", err, l, msg)
	}
}

// A sandbox holds its sessions where cpb cannot see them: the launch says
// so instead of guarding.
func TestResumeNoteForASandbox(t *testing.T) {
	if msg := captureStderr(t, func() { resumeNote([]string{"--resume", sidOld}) }); !strings.Contains(msg, "cannot check") {
		t.Fatalf("resume: %q", msg)
	}
	if msg := captureStderr(t, func() { resumeNote([]string{"-p", "hi"}) }); msg != "" {
		t.Fatalf("a new session: %q", msg)
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
	if got := encodeProjectDir("/Users/me/src/.worktrees/app/main"); got != "-Users-me-src--worktrees-app-main" {
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
	if _, dead := got[1<<30]; got[os.Getpid()].start == "" || dead {
		t.Fatalf("%v", got)
	}
	v := got[os.Getpid()].start
	if runtime.GOOS == "linux" {
		if _, err := strconv.ParseUint(v, 10, 64); err != nil {
			t.Fatalf("not /proc/<pid>/stat's starttime: %q", v)
		}
	} else if _, err := time.Parse("Mon Jan 2 15:04:05 2006", v); err != nil {
		t.Fatalf("not lstart's format: %q", v)
	}
}

// A live pid whose session file records no start time is not known to be
// the same process: listed, never resumed (Codex, #132).
func TestNoProcStartIsNotConfirmedLive(t *testing.T) {
	root := sandboxDefaultRoot(t)
	writePlaybook(t, root, "alpha", nil)
	work, _ := filepath.EvalSymlinks(t.TempDir())
	a := filepath.Join(root, "alpha")
	writeSessionFile(t, a, 201, sidLive, work, "interactive", "", time.Now())
	writeTranscript(t, a, work, sidLive, "m", "", time.Now())
	fakeProcs(t, map[int]string{201: liveStart})
	log := resumeClaude(t)
	chdirT(t, work)
	if out := mustStmt(t, "SHOW SESSIONS --json"); !strings.Contains(out, sidLive) {
		t.Fatalf("listed:\n%s", out)
	}
	if l, _, err := runClaude(t, log, "alpha", "--resume", sidLive); err == nil || !strings.Contains(err.Error(), "records no start time") || l != "" {
		t.Fatalf("resumed an unconfirmed session: %v", err)
	}
	if l, _, err := runClaude(t, log, "alpha", "--continue"); err == nil || !strings.Contains(err.Error(), "may still be running") || l != "" {
		t.Fatalf("--continue: %v", err)
	}
}

// A registry that cannot be read fails the listing rather than leave its
// plain directories out unnoticed (Codex, #132).
func TestUnreadableDirRegistryFails(t *testing.T) {
	root, _, _ := sessionFixture(t)
	if err := os.MkdirAll(filepath.Join(root, ".state"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".state", "dirs.toml"), []byte("[dirs\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := stmt(t, "SHOW SESSIONS"); err == nil || !strings.Contains(err.Error(), "dirs.toml") {
		t.Fatalf("malformed registry: %v", err)
	}
}

// The folder a session ran in comes from its transcript's head when the tail
// is one oversized record; a transcript that records none is left to claude
// (Codex, #132).
func TestRunResumeCwdFromTheHead(t *testing.T) {
	root := sandboxDefaultRoot(t)
	writePlaybook(t, root, "alpha", nil)
	fakeProcs(t, nil)
	work, _ := filepath.EvalSymlinks(t.TempDir())
	a := filepath.Join(root, "alpha")
	dir := filepath.Join(a, "projects", encodeProjectDir(work))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	big := `{"type":"user","message":{"content":"` + strings.Repeat("x", transcriptTailBytes+1024) + `"}}`
	head := fmt.Sprintf(`{"type":"user","cwd":%q}`, work)
	if err := os.WriteFile(filepath.Join(dir, sidDead+".jsonl"), []byte(head+"\n"+big+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sidOld+".jsonl"), []byte(big+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	log := resumeClaude(t)
	chdirT(t, t.TempDir())
	if l, _, err := runClaude(t, log, "alpha", "--resume", sidDead); err == nil || !strings.Contains(err.Error(), "cd "+shellQuoteTest(work)+" && ") || l != "" {
		t.Fatalf("cwd from the head: %v (claude: %q)", err, l)
	}
	if l, msg, err := runClaude(t, log, "alpha", "--resume", sidOld); err != nil || l == "" {
		t.Fatalf("no cwd: %v %q\n%s", err, l, msg)
	}
	chdirT(t, work)
	if l, msg, err := runClaude(t, log, "alpha", "--resume", sidDead); err != nil || !strings.HasPrefix(l, work+"|") {
		t.Fatalf("here: %v %q\n%s", err, l, msg)
	}
}

// tty (v3.25.0): the controlling terminal, last in the object and the table,
// null for none.
func TestSessionTTY(t *testing.T) {
	root := sandboxDefaultRoot(t)
	writePlaybook(t, root, "alpha", nil)
	work, _ := filepath.EvalSymlinks(t.TempDir())
	a := filepath.Join(root, "alpha")
	writeSessionFile(t, a, 301, sidLive, work, "interactive", liveStart, time.Now())
	writeSessionFile(t, a, 302, sidOld, work, "bg", liveStart, time.Now().Add(-time.Hour))
	fakeProcInfo(t, map[int]procInfo{301: {start: liveStart, tty: "pts/4"}, 302: {start: liveStart}})
	out := mustStmt(t, "SHOW SESSIONS --json")
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 2 {
		t.Fatalf("%v\n%s", err, out)
	}
	if rows[0]["tty"] != "pts/4" || rows[1]["tty"] != nil {
		t.Fatalf("tty: %v / %v", rows[0]["tty"], rows[1]["tty"])
	}
	if !strings.Contains(out, "\"tty\": \"pts/4\"\n  }") || !strings.Contains(out, "\"tty\": null\n  }") {
		t.Fatalf("tty is the last field of each object:\n%s", out)
	}
	cols, _ := describeTable("SESSIONS")
	if last := cols[len(cols)-1]; last.Name != "tty" || last.Type != "Nullable(String)" {
		t.Fatalf("last column: %+v", last)
	}
	if h := mustStmt(t, "SHOW SESSIONS"); !strings.Contains(h, "TTY") || !strings.Contains(h, "pts/4") {
		t.Fatalf("human:\n%s", h)
	}
}

func TestLinuxTTY(t *testing.T) {
	for nr, want := range map[uint64]string{
		0:                      "",
		136<<8 | 4:             "pts/4",
		137<<8 | 2:             "pts/258",
		136<<8 | (1<<20 | 0x5): "pts/261", // devpts: major 136, minor 261 (0x105, its high bits at 20-31)
		137<<8 | 5:             "pts/261", // the legacy Unix98 layout procps also names this way
		4<<8 | 1:               "tty1",
		136<<8 | 0x80000<<12:   "pts/524288", // bit 31 set: /proc prints it negative
		4<<8 | 64:              "ttyS0",
		188<<8 | 0:             "",
	} {
		if got := linuxTTY(nr); got != want {
			t.Errorf("linuxTTY(%#x) = %q, want %q", nr, got, want)
		}
	}
}

// /proc/<pid>/stat prints tty_nr as a signed int; a large pts minor is
// negative there, and still names its terminal (Codex, #134).
func TestProcStatNegativeTTYNr(t *testing.T) {
	u := uint32(136<<8 | 0x80000<<12)
	nr := int32(u)
	if nr >= 0 {
		t.Fatal("the test value must set bit 31")
	}
	n, _ := strconv.ParseInt(strconv.Itoa(int(nr)), 10, 32)
	if got := linuxTTY(uint64(uint32(int32(n)))); got != "pts/524288" {
		t.Fatalf("got %q", got)
	}
}
