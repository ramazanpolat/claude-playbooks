package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ramazanpolat/claude-playbooks/internal/account"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// A host launch records which account its session runs on, for the
// session's process, while it runs, mode 0600 in a directory of 0700, and
// removes the record when the session ends. A real process: a stand-in
// claude that copies its own record (by $$, its pid) once it appears.
func TestRunRecordsTheLaunch(t *testing.T) {
	root := sandboxDefaultRoot(t)
	writePlaybook(t, root, "alpha", &manifest.Manifest{Launcher: "al"})
	fakeProcs(t, nil)
	dir := t.TempDir()
	out := filepath.Join(dir, "seen")
	// The record is named for the launch's id; it names this process.
	script := "#!/bin/sh\nd=\"$FAKE_ROOT/.state/launches\"\ni=0\nf=\nwhile [ -z \"$f\" ] && [ $i -lt 100 ]; do f=$(grep -l \"\\\"pid\\\":$$,\" \"$d\"/*.json 2>/dev/null | head -1); [ -n \"$f\" ] || sleep 0.05; i=$((i+1)); done\n" +
		"cp \"$f\" \"$FAKE_OUT\" && ls -ln \"$f\" | cut -c1-10 > \"$FAKE_OUT.mode\" && echo $$ > \"$FAKE_OUT.pid\"\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_ROOT", root)
	t.Setenv("FAKE_OUT", out)

	launch := func(args ...string) launchRecord {
		t.Helper()
		for _, p := range []string{out, out + ".mode", out + ".pid"} {
			_ = os.Remove(p)
		}
		var err error
		msg := captureStderr(t, func() { err = runRun(nil, args) })
		if err != nil {
			t.Fatalf("%v\n%s", err, msg)
		}
		b, err := os.ReadFile(out)
		if err != nil {
			t.Fatalf("the session saw no record of its launch: %v\n%s", err, msg)
		}
		var r launchRecord
		if err := json.Unmarshal(b, &r); err != nil {
			t.Fatalf("%v: %s", err, b)
		}
		pid, _ := os.ReadFile(out + ".pid")
		if strconv.Itoa(r.PID) != strings.TrimSpace(string(pid)) {
			t.Fatalf("the record is for pid %d, the session's is %s", r.PID, pid)
		}
		if mode, _ := os.ReadFile(out + ".mode"); strings.TrimSpace(string(mode)) != "-rw-------" {
			t.Fatalf("record mode %q, want -rw-------", mode)
		}
		if left, _ := filepath.Glob(filepath.Join(launchRecordsDir(root), "*.json")); len(left) != 0 {
			t.Fatalf("the record outlives its launch: %v", left)
		}
		if !launchIDShape.MatchString(r.LaunchID) || r.Key == "" {
			t.Fatalf("record id or key: %+v", r)
		}
		if r.V != 1 || r.ProcStart == "" || r.ConfigDir != filepath.Join(root, "alpha") || r.Playbook != "alpha" || r.LaunchedAt == "" {
			t.Fatalf("record: %+v", r)
		}
		return r
	}

	// The implicit account: machine, inherited, no identity.
	if r := launch("alpha"); r.Account != "machine" || r.Why != account.WhyInherited || r.Identity != nil {
		t.Fatalf("machine launch: %+v", r)
	}
	// A named account carries its identity label.
	putAccount(t, account.Account{Name: "acc", Provider: account.Anthropic, Identity: "someone@example.com",
		Auth: account.Auth{Kind: account.ClaudeToken, Value: "t"}})
	if r := launch("--account", "acc", "alpha"); r.Account != "acc" || r.Why != "--account" || r.Identity == nil || *r.Identity != "someone@example.com" {
		t.Fatalf("--account launch: %+v", r)
	}
	if fi, err := os.Stat(launchRecordsDir(root)); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("records directory: %v %v", fi.Mode(), err)
	}
}

// A record that cannot be written costs the launch a warning, never the
// session.
func TestLaunchRecordUnwritableWarns(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".state"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := newLaunchRecord("alpha", filepath.Join(root, "alpha"), account.Selection{Name: "acc", Why: "--account"})
	var done func()
	msg := captureStderr(t, func() { done = r.record(root, os.Getpid()) })
	done()
	if !strings.Contains(msg, "Warning: cannot record this launch's account") {
		t.Fatalf("no warning: %q", msg)
	}
}

// writeRecord writes a launch record as a launch would.
func writeRecord(t *testing.T, root string, r launchRecord) {
	t.Helper()
	if r.V == 0 {
		r.V = launchRecordVersion
	}
	if r.LaunchID == "" {
		r.LaunchID = testLaunchID(r.PID)
	}
	dir := launchRecordsDir(root)
	if err := writeLaunchRecord(dir, filepath.Join(dir, r.LaunchID+".json"), &r); err != nil {
		t.Fatal(err)
	}
}

// testLaunchID is a launch id for a test record of pid.
func testLaunchID(pid int) string { return fmt.Sprintf("%016x", pid) }

// SHOW SESSIONS shows the account of a session's launch record: its own
// pid's, or its parent's (a secret helper that started claude), and only
// for the process the record names (same start time). A session cpb did not
// launch shows none.
func TestShowSessionsAccount(t *testing.T) {
	root, work, _ := sessionFixture(t)
	a := filepath.Join(root, "alpha")
	now := time.Now()
	const (
		sidChild = "66666666-6666-4666-8666-666666666666"
		sidOther = "77777777-7777-4777-8777-777777777777"
		sidNone  = "88888888-8888-4888-8888-888888888888"
		sidStale = "99999999-9999-4999-8999-999999999999"
		helper   = "Mon Sep 28 07:29:50 2026"
	)
	writeSessionFile(t, a, 105, sidChild, work, "interactive", liveStart, now.Add(-30*time.Minute))
	writeSessionFile(t, a, 106, sidOther, work, "interactive", liveStart, now.Add(-20*time.Minute))
	writeSessionFile(t, a, 107, sidNone, work, "interactive", liveStart, now.Add(-10*time.Minute))
	// A session file with no start time, on a pid a cpb launch now holds:
	// it may be a stale file, so the launch's account is not its.
	writeSessionFile(t, a, 108, sidStale, work, "interactive", "", now.Add(-5*time.Minute))
	fakeProcInfo(t, map[int]procInfo{
		101: {start: liveStart, ppid: 50},
		105: {start: liveStart, ppid: 900}, // started by a helper with a record
		106: {start: liveStart, ppid: 901}, // its parent's pid was reused
		107: {start: liveStart, ppid: 50},  // no record at all
		108: {start: liveStart, ppid: 900}, // no start time in its file
		900: {start: helper, ppid: 50},
		901: {start: "Tue Sep 29 01:00:00 2026", ppid: 50},
	})
	writeRecord(t, root, launchRecord{PID: 101, ProcStart: liveStart, Playbook: "alpha", Account: "claude-work"})
	writeRecord(t, root, launchRecord{PID: 900, ProcStart: helper, Playbook: "alpha", Account: "env:router"})
	writeRecord(t, root, launchRecord{PID: 901, ProcStart: helper, Playbook: "alpha", Account: "stale"})
	writeRecord(t, root, launchRecord{PID: 108, ProcStart: liveStart, Playbook: "alpha", Account: "claude-other"})

	rows, _, err := liveSessions("")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range rows {
		got[r.SessionID] = deref(r.Account, "<null>")
	}
	want := map[string]string{sidLive: "claude-work", sidChild: "env:router", sidOther: "<null>", sidNone: "<null>", sidStale: "<null>"}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("session %s: account %q, want %q", id[:8], got[id], w)
		}
	}

	// The record of the session's own pid, from another process that had
	// that pid before: not this session's.
	writeRecord(t, root, launchRecord{PID: 101, ProcStart: "Sun Sep 27 01:00:00 2026", Playbook: "alpha", Account: "claude-work"})
	rows, _, _ = liveSessions("alpha")
	for _, r := range rows {
		if r.SessionID == sidLive && r.Account != nil {
			t.Fatalf("a reused pid borrowed the account %q", *r.Account)
		}
	}

	// The human table has the column; JSON keeps every earlier field in
	// place and adds account and at_limit after tty.
	writeRecord(t, root, launchRecord{PID: 101, ProcStart: liveStart, Playbook: "alpha", Account: "claude-work"})
	out := mustStmt(t, "SHOW SESSIONS")
	if !strings.Contains(out, "PLAYBOOK  ACCOUNT") || !strings.Contains(out, "claude-work") {
		t.Fatalf("table:\n%s", out)
	}
	js := mustStmt(t, "SHOW SESSIONS --json")
	if i, j := strings.Index(js, `"tty"`), strings.Index(js, `"account"`); i < 0 || j < i || !strings.Contains(js, `"at_limit": null`) {
		t.Fatalf("json:\n%s", js)
	}
}

// Records are read defensively: another version, another pid than the file
// name, an account holding a control character, no start time, too large
// or not JSON is no record.
func TestReadLaunchRecordsDefensive(t *testing.T) {
	root := t.TempDir()
	dir := launchRecordsDir(root)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	put := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rec := func(pid int, rest string) string {
		return fmt.Sprintf(`{"v":1,"launch_id":%q,"pid":%d,%s}`, testLaunchID(pid), pid, rest)
	}
	id := func(pid int) string { return testLaunchID(pid) + ".json" }
	put(id(10), rec(10, `"proc_start":"x","account":"acc"`))
	put(id(11), strings.Replace(rec(11, `"proc_start":"x","account":"acc"`), `"v":1`, `"v":2`, 1))
	put(id(12), rec(13, `"proc_start":"x","account":"acc"`)) // named for another launch
	put(id(14), rec(14, `"proc_start":"x","account":"a\tb"`))
	put(id(15), rec(15, `"proc_start":"","account":"acc"`))
	put(id(16), `not json`)
	put(id(17), rec(17, `"proc_start":"x","account":"acc","pad":"`+strings.Repeat("x", maxLaunchRecordBytes)+`"`))
	put("18.json", `{"v":1,"pid":18,"proc_start":"x","account":"acc"}`) // not a launch id's name
	put(id(19), rec(19, `"proc_start":"x","account":"acc\u001b[31m"`))
	// Every form a launch writes reads back: a route's IPv6 host, a block
	// named for a directory with a space in it.
	put(id(20), rec(20, `"proc_start":"x","account":"flags:[::1]:8080"`))
	put(id(21), rec(21, `"proc_start":"x","account":"block:my dir"`))
	got := readLaunchRecords(root)
	if len(got) != 3 || got[10][0].Account != "acc" || got[20][0].Account != "flags:[::1]:8080" || got[21][0].Account != "block:my dir" {
		t.Fatalf("records: %+v", got)
	}
}

// A launch sweeps the records of processes no longer alive and temporary
// files a killed writer left; a live process's record stays.
func TestSweepLaunchRecords(t *testing.T) {
	root := t.TempDir()
	dir := launchRecordsDir(root)
	writeRecord(t, root, launchRecord{PID: os.Getpid(), ProcStart: "x", Account: "acc"})
	dead := 1 << 30
	writeRecord(t, root, launchRecord{PID: dead, ProcStart: "x", Account: "acc"})
	old := filepath.Join(dir, ".launch-123")
	fresh := filepath.Join(dir, ".launch-456")
	for _, p := range []string{old, fresh} {
		if err := os.WriteFile(p, []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	sweepLaunchRecords(dir)
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	if !exists(filepath.Join(dir, testLaunchID(os.Getpid())+".json")) || exists(filepath.Join(dir, testLaunchID(dead)+".json")) || exists(old) || !exists(fresh) {
		t.Fatal("sweep kept or removed the wrong files")
	}
}

// writeLimitTranscript writes a transcript whose lines are given.
func writeLimitTranscript(t *testing.T, configDir, cwd, id string, lines ...string) {
	t.Helper()
	dir := filepath.Join(configDir, "projects", encodeProjectDir(cwd))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// limitLine is the line Claude Code writes when a session hits a limit
// (observed on 2.1.240 to 2.1.322), with quota as its quotaLimits.
func limitLine(quota string) string {
	return `{"type":"assistant","isApiErrorMessage":true,"error":"rate_limit","apiErrorStatus":429,` +
		`"message":{"model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"You've hit your limit"}]},"quotaLimits":` + quota + `}`
}

// A session is at a limit while its transcript's last assistant line is
// Claude Code's limit error and the limit has not reset; an answer after it,
// a reset in the past, or any other shape is no limit, never an error.
func TestSessionAtLimit(t *testing.T) {
	root, work, _ := sessionFixture(t)
	a := filepath.Join(root, "alpha")
	now := time.Unix(1794500000, 0)
	old := sessionsNow
	sessionsNow = func() time.Time { return now }
	t.Cleanup(func() { sessionsNow = old })
	reply := `{"type":"assistant","message":{"model":"claude-opus-5-5","role":"assistant"}}`
	user := `{"type":"user","message":{"role":"user","content":"go on"}}`
	future, past := now.Add(2*time.Hour).Unix(), now.Add(-time.Minute).Unix()
	q := func(status, kind string, resets any) string {
		b, _ := json.Marshal(map[string]any{"status": status, "rateLimitType": kind, "resetsAt": resets,
			"unifiedRateLimitFallbackAvailable": false, "overageStatus": "rejected", "isUsingOverage": false})
		return string(b)
	}
	cases := []struct {
		name  string
		lines []string
		want  string // the window, "" for no limit
	}{
		{"weekly limit, then the user typed", []string{reply, limitLine(q("rejected", "seven_day", future)), user}, "seven_day"},
		{"5-hour limit", []string{reply, limitLine(q("rejected", "five_hour", future))}, "five_hour"},
		{"a window Claude Code adds", []string{limitLine(q("rejected", "seven_day_opus", future))}, "seven_day_opus"},
		{"answered since", []string{limitLine(q("rejected", "seven_day", future)), reply}, ""},
		{"reset already", []string{limitLine(q("rejected", "seven_day", past))}, ""},
		{"not rejected", []string{limitLine(q("allowed_warning", "seven_day", future))}, ""},
		{"window not a name", []string{limitLine(q("rejected", "Seven Day", future))}, ""},
		{"reset as text", []string{limitLine(q("rejected", "seven_day", strconv.FormatInt(future, 10)))}, ""},
		{"reset in milliseconds", []string{limitLine(q("rejected", "seven_day", future*1000))}, ""},
		{"no quotaLimits", []string{`{"type":"assistant","isApiErrorMessage":true,"error":"rate_limit","message":{"model":"<synthetic>"}}`}, ""},
		{"quotaLimits not an object", []string{limitLine(`"rejected"`)}, ""},
		{"another error", []string{strings.Replace(limitLine(q("rejected", "seven_day", future)), `"rate_limit"`, `"overloaded"`, 1)}, ""},
		{"an assistant line of a shape cpb does not know", []string{limitLine(q("rejected", "seven_day", future)),
			`{"type":"assistant","isApiErrorMessage":"yes","error":7,"message":"text","cwd":5}`}, ""},
	}
	for _, c := range cases {
		writeLimitTranscript(t, a, work, sidLive, c.lines...)
		rows, _, err := liveSessions("alpha")
		if err != nil || len(rows) != 1 {
			t.Fatalf("%s: %v %d rows", c.name, err, len(rows))
		}
		got := ""
		if l := rows[0].AtLimit; l != nil {
			got = l.Window
			if want := rfc3339(time.Unix(future, 0)); l.ResetsAt != want {
				t.Errorf("%s: resets_at %s, want %s", c.name, l.ResetsAt, want)
			}
		}
		if got != c.want {
			t.Errorf("%s: at_limit %q, want %q", c.name, got, c.want)
		}
	}

	// The table shows it in STATUS.
	writeLimitTranscript(t, a, work, sidLive, reply, limitLine(q("rejected", "seven_day", future)))
	out := mustStmt(t, "SHOW SESSIONS FOR PLAYBOOK alpha")
	if !strings.Contains(out, "limit 7d") {
		t.Fatalf("table:\n%s", out)
	}
}

// On the real platform, the start time a launch records for a process is
// the one SHOW SESSIONS reads for it, and the parent's pid is read too.
func TestProcStartOfMatchesProcStarts(t *testing.T) {
	pid := os.Getpid()
	info, ok := procStarts([]int{pid})[pid]
	if !ok {
		t.Skip("no process table here")
	}
	if got := procStartOf(pid); !sameStart(got, info.start) {
		t.Fatalf("launch reads %q, SHOW SESSIONS reads %q", got, info.start)
	}
	if info.ppid != os.Getppid() {
		t.Fatalf("ppid %d, want %d", info.ppid, os.Getppid())
	}
}
