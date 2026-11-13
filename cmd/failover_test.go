package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ramazanpolat/claude-playbooks/internal/account"
	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

// fastFailover shortens the thresholds and gives the test a fresh episode.
func fastFailover(t *testing.T, quiet, minWait time.Duration) {
	t.Helper()
	oldPoll, oldQuiet, oldWait, oldEp := failoverPoll, failoverQuiet, failoverMinWait, activeEpisode
	failoverPoll, failoverQuiet, failoverMinWait = 20*time.Millisecond, quiet, minWait
	activeEpisode = &failoverEpisode{tried: map[string]bool{}}
	t.Cleanup(func() {
		failoverPoll, failoverQuiet, failoverMinWait, activeEpisode = oldPoll, oldQuiet, oldWait, oldEp
	})
}

func tokenAccount(t *testing.T, name, token string) {
	t.Helper()
	putAccount(t, account.Account{Name: name, Provider: account.Anthropic, Auth: account.Auth{Kind: account.ClaudeToken, Value: token}})
}

const failSID = "11111111-2222-4333-8444-555555555555"

// standIn is a stand-in claude. A resumed launch whose token is in
// FAKE_FAIL_TOKENS fails at once, writing nothing; one in
// FAKE_WRITE_FAIL_TOKENS writes a line to the transcript, then fails. A
// first launch, and a resumed one whose token is not in FAKE_OK_TOKENS,
// does what Claude Code does at a weekly limit: it appends the limit line (timestamped, with the folder the
// session runs in: FAKE_SESSION_DIR, else its working folder) to the
// transcript, writes its session file (the folder it started in, kind,
// status idle, its own start time) and waits, at most about 20 seconds, so
// a launch that never ends it fails the test instead of hanging it. A resumed launch whose token is listed appends one line
// to $FAKE_OUT.log (token, working folder, arguments ended by 0x1f; the
// line ended by 0x1e, since a prompt holds newlines), copies its own
// launch record, and exits.
const standIn = `#!/bin/sh
tok="$CLAUDE_CODE_OAUTH_TOKEN"
for a in "$@"; do [ "$a" = --resume ] && resumed=1; done
dir="${FAKE_SESSION_DIR:-$(pwd -P)}"
enc=$(printf %s "$dir" | sed 's/[^A-Za-z0-9]/-/g')
t="$CLAUDE_CONFIG_DIR/projects/$enc/$FAKE_SID.jsonl"
if [ -n "$resumed" ]; then
  case " $FAKE_FAIL_TOKENS " in *" $tok "*) exit 1;; esac
  case " $FAKE_WRITE_FAIL_TOKENS " in *" $tok "*) printf '{"type":"user"}\n' >> "$t"; exit 1;; esac
  case " $FAKE_RUN_FAIL_TOKENS " in *" $tok "*)
    mkdir -p "$CLAUDE_CONFIG_DIR/sessions"
    if [ -r "/proc/$$/stat" ]; then st=$(sed 's/.*) //' "/proc/$$/stat" | awk '{print $20}'); else st=$(TZ=UTC LC_ALL=C ps -o lstart= -p $$ | awk '{$1=$1; print}'); fi
    printf '{"pid":%s,"sessionId":"%s","cwd":"%s","procStart":"%s","kind":"interactive","status":"busy"}' $$ "$FAKE_SID" "$dir" "$st" > "$CLAUDE_CONFIG_DIR/sessions/$$.json"
    sleep 0.5; rm -f "$CLAUDE_CONFIG_DIR/sessions/$$.json"; exit 1;;
  esac
  case " $FAKE_OK_TOKENS " in *" $tok "*) ok=1;; esac
fi
if [ -n "$ok" ]; then
  { printf '%s|%s|' "$tok" "$(pwd -P)"; for a in "$@"; do printf '%s\037' "$a"; done; printf '\036'; } >> "$FAKE_OUT.log"
  f=$(grep -l "\"pid\":$$," "$FAKE_ROOT"/.state/launches/*.json 2>/dev/null | head -1)
  [ -n "$f" ] && cp "$f" "$FAKE_OUT.record"
  exit 0
fi
mkdir -p "$CLAUDE_CONFIG_DIR/projects/$enc" "$CLAUDE_CONFIG_DIR/sessions"
if [ -r "/proc/$$/stat" ]; then st=$(sed 's/.*) //' "/proc/$$/stat" | awk '{print $20}'); else st=$(TZ=UTC LC_ALL=C ps -o lstart= -p $$ | awk '{$1=$1; print}'); fi
sleep 1
ts=$(date -u +%Y-%m-%dT%H:%M:%S.000Z)
printf '{"type":"assistant","cwd":"%s","timestamp":"%s","message":{"model":"claude-opus-5-5","usage":{"input_tokens":10,"cache_read_input_tokens":300000,"cache_creation_input_tokens":2000}}}\n{"type":"assistant","cwd":"%s","timestamp":"%s","isApiErrorMessage":true,"error":"rate_limit","message":{"model":"<synthetic>"},"quotaLimits":{"status":"rejected","rateLimitType":"seven_day","resetsAt":%s}}\n' "$dir" "$ts" "$dir" "$ts" "$FAKE_RESETS" >> "$t"
sf="$CLAUDE_CONFIG_DIR/sessions/$$.json"
[ -n "$FAKE_NO_PROCSTART" ] && st=""
printf '{"pid":%s,"sessionId":"%s","cwd":"%s","startedAt":1,"procStart":"%s","kind":"interactive","status":"idle"}' $$ "$FAKE_SID" "$(pwd -P)" "$st" > "$sf"
if [ -n "$FAKE_IGNORE_TERM" ]; then trap '' TERM; else trap 'rm -f "$sf"; exit 143' TERM; fi
n=0; while [ $n -lt "${FAKE_WAIT:-200}" ]; do sleep 0.1; n=$((n+1)); done
rm -f "$sf"; exit 3
`

// standInRun is one launch the stand-in recorded as it ended.
type standInRun struct {
	token, pwd string
	args       []string
}

// failoverStage puts the stand-in on PATH and returns where it writes.
func failoverStage(t *testing.T, root, okTokens string) (out string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(standIn), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out = filepath.Join(dir, "out")
	t.Setenv("FAKE_OUT", out)
	t.Setenv("FAKE_ROOT", root)
	t.Setenv("FAKE_SID", failSID)
	t.Setenv("FAKE_OK_TOKENS", okTokens)
	t.Setenv("FAKE_SESSION_DIR", "")
	t.Setenv("FAKE_FAIL_TOKENS", "")
	t.Setenv("FAKE_WRITE_FAIL_TOKENS", "")
	t.Setenv("FAKE_RUN_FAIL_TOKENS", "")
	t.Setenv("FAKE_IGNORE_TERM", "")
	t.Setenv("FAKE_NO_PROCSTART", "")
	t.Setenv("FAKE_WAIT", "")
	t.Setenv("FAKE_RESETS", strconv.FormatInt(time.Now().Add(2*time.Hour).Unix(), 10))
	return out
}

// stageHelper puts a secret helper script named name on PATH and makes it
// the launch's helper.
func stageHelper(t *testing.T, name, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CPB_SECRET_HELPER", name)
}

func standInRuns(t *testing.T, out string) []standInRun {
	t.Helper()
	b, _ := os.ReadFile(out + ".log")
	var runs []standInRun
	for _, line := range strings.Split(string(b), "\x1e") {
		parts := strings.SplitN(line, "|", 3)
		if len(parts) != 3 {
			continue
		}
		runs = append(runs, standInRun{token: parts[0], pwd: parts[1], args: strings.Split(strings.TrimSuffix(parts[2], "\x1f"), "\x1f")})
	}
	return runs
}

func standInRecord(t *testing.T, out string) launchRecord {
	t.Helper()
	var r launchRecord
	b, err := os.ReadFile(out + ".record")
	if err != nil || json.Unmarshal(b, &r) != nil {
		t.Fatalf("no launch record: %v %s", err, b)
	}
	return r
}

// The whole switch, on a real process: a stand-in claude stops at a weekly
// limit and waits; the launch ends it and resumes the same session on the
// next listed account, with that account's token, from the folder the
// session ran in (not the launch's), with the continue prompt last, and
// says so.
func TestFailoverSwitchesAccount(t *testing.T) {
	root := sandboxDefaultRoot(t)
	writePlaybook(t, root, "alpha", &manifest.Manifest{Launcher: "al"})
	fakeProcs(t, nil)
	fastFailover(t, 0, time.Minute)
	tokenAccount(t, "acc-a", "tok-a")
	tokenAccount(t, "acc-b", "tok-b")
	if err := writeFailoverDefaults(root, failoverDefaults{Failover: "on", Accounts: []string{"acc-a", "acc-b"}}); err != nil {
		t.Fatal(err)
	}
	work, _ := filepath.EvalSymlinks(t.TempDir())
	session, _ := filepath.EvalSymlinks(t.TempDir())
	out := failoverStage(t, root, "tok-b")
	t.Setenv("FAKE_SESSION_DIR", session)
	t.Chdir(work)

	var err error
	msg := captureStderr(t, func() {
		err = runRun(nil, []string{"--account", "acc-a", "alpha", "--permission-mode", "auto", "first prompt"})
	})
	if err != nil {
		t.Fatalf("%v\n%s", err, msg)
	}
	runs := standInRuns(t, out)
	if len(runs) != 1 {
		t.Fatalf("resumes: %+v\n%s", runs, msg)
	}
	r := runs[0]
	joined := strings.Join(r.args, " ")
	if r.token != "tok-b" || r.pwd != session || !strings.HasPrefix(joined, "--resume "+failSID+" --model claude-opus-5-5 --permission-mode auto ") ||
		strings.Contains(joined, "first prompt") {
		t.Fatalf("resume: %+v", r)
	}
	resets, _ := strconv.ParseInt(os.Getenv("FAKE_RESETS"), 10, 64)
	want := "[cpb: this session moved from account acc-a (weekly limit, resets " + time.Unix(resets, 0).Local().Format("Mon 2 Jan 15:04") +
		") to acc-b; its context, about 302,010 tokens, is read again on acc-b with no cache.]\n\n" +
		"You can continue now. Continue the task you were working on when the usage limit was reached; do not repeat work that is already complete."
	if got := r.args[len(r.args)-1]; got != want {
		t.Fatalf("continue prompt:\n%q\nwant\n%q", got, want)
	}
	if rec := standInRecord(t, out); len(rec.Switches) != 1 || rec.Switches[0].From != "acc-a" || rec.Switches[0].To != "acc-b" || rec.Switches[0].Outcome != "moved" {
		t.Fatalf("record switches: %+v", rec.Switches)
	}
	if !strings.Contains(msg, "cpb: account acc-a hit its weekly limit") || !strings.Contains(msg, "resuming this session on acc-b") ||
		!strings.Contains(msg, "cpb: this session moved from acc-a to acc-b at ") {
		t.Fatalf("stderr:\n%s", msg)
	}
}

// Two hops on a launch that turned failover on by its flag alone: the flag
// is kept on the relaunch, so the second account's limit moves the session
// on again, and the first account is not picked again. The first account's
// credential goes through a secret helper that runs claude as its child,
// which the watcher finds by its parent.
func TestFailoverChain(t *testing.T) {
	root := sandboxDefaultRoot(t)
	writePlaybook(t, root, "alpha", &manifest.Manifest{Launcher: "al"})
	fastFailover(t, 0, time.Minute)
	putAccount(t, account.Account{Name: "acc-a", Provider: account.Anthropic, Auth: account.Auth{Kind: account.ClaudeToken, Ref: "stub:a"}})
	tokenAccount(t, "acc-b", "tok-b")
	tokenAccount(t, "acc-c", "tok-c")
	if err := writeFailoverDefaults(root, failoverDefaults{Accounts: []string{"acc-a", "acc-b", "acc-c"}}); err != nil {
		t.Fatal(err)
	}
	work, _ := filepath.EvalSymlinks(t.TempDir())
	out := failoverStage(t, root, "tok-c")
	stageHelper(t, "forking-helper", "#!/bin/sh\n[ \"$1\" = --check ] && exit 0\n"+
		"while [ \"$1\" != -- ]; do export \"${1%%=*}=tok-${1#*=stub:}\"; shift; done\nshift\n\"$@\"\nexit $?\n")
	t.Chdir(work)

	var err error
	msg := captureStderr(t, func() { err = runRun(nil, []string{"--failover", "--account", "acc-a", "alpha"}) })
	if err != nil {
		t.Fatalf("%v\n%s", err, msg)
	}
	runs := standInRuns(t, out)
	if len(runs) != 1 || runs[0].token != "tok-c" || runs[0].pwd != work {
		t.Fatalf("runs: %+v\n%s", runs, msg)
	}
	if strings.Count(msg, "hit its weekly limit") != 2 || !strings.Contains(msg, "resuming this session on acc-b") || !strings.Contains(msg, "resuming this session on acc-c") {
		t.Fatalf("stderr:\n%s", msg)
	}
	rec := standInRecord(t, out)
	if len(rec.Switches) != 2 || rec.Switches[0].From != "acc-a" || rec.Switches[0].To != "acc-b" || rec.Switches[1].From != "acc-b" || rec.Switches[1].To != "acc-c" {
		t.Fatalf("record switches: %+v", rec.Switches)
	}
}

// A launch on the new account that never runs the session (its helper fails
// before claude starts; the transcript is as it was) resumes the session on
// the account it came from, with no prompt, and the summary and the record
// say it did not move.
func TestFailoverFallsBackWhenTheNewAccountFails(t *testing.T) {
	root := sandboxDefaultRoot(t)
	writePlaybook(t, root, "alpha", &manifest.Manifest{Launcher: "al"})
	fakeProcs(t, nil)
	fastFailover(t, 0, time.Minute)
	tokenAccount(t, "acc-a", "tok-a")
	putAccount(t, account.Account{Name: "acc-ref", Provider: account.Anthropic, Auth: account.Auth{Kind: account.ClaudeToken, Ref: "stub:ref"}})
	if err := writeFailoverDefaults(root, failoverDefaults{Failover: "on", Accounts: []string{"acc-ref"}}); err != nil {
		t.Fatal(err)
	}
	work, _ := filepath.EvalSymlinks(t.TempDir())
	out := failoverStage(t, root, "tok-a")
	// A helper that is there but cannot resolve anything.
	stageHelper(t, "failing-helper", "#!/bin/sh\n[ \"$1\" = --check ] && exit 0\necho 'cannot resolve' >&2\nexit 7\n")
	t.Chdir(work)

	var err error
	msg := captureStderr(t, func() { err = runRun(nil, []string{"--account", "acc-a", "alpha"}) })
	if err != nil {
		t.Fatalf("%v\n%s", err, msg)
	}
	runs := standInRuns(t, out)
	if len(runs) != 1 || runs[0].token != "tok-a" || strings.Join(runs[0].args, " ") != "--resume "+failSID+" --model claude-opus-5-5" {
		t.Fatalf("the way back: %+v\n%s", runs, msg)
	}
	if !strings.Contains(msg, "could not resume this session on acc-ref") || !strings.Contains(msg, "resuming it on acc-a instead") ||
		!strings.Contains(msg, "could not move from acc-a to acc-ref, and stayed on acc-a") || strings.Contains(msg, "moved from acc-a to acc-ref") {
		t.Fatalf("stderr:\n%s", msg)
	}
	if rec := standInRecord(t, out); len(rec.Switches) != 1 || rec.Switches[0].Outcome != "failed" {
		t.Fatalf("record switches: %+v", rec.Switches)
	}
}

// failoverE2E is the common stage of an end-to-end switch: playbook alpha,
// acc-a and acc-b with tokens, failover on to acc-b, the stand-in on PATH.
func failoverE2E(t *testing.T, okTokens string) (root, out string) {
	t.Helper()
	root = sandboxDefaultRoot(t)
	writePlaybook(t, root, "alpha", &manifest.Manifest{Launcher: "al"})
	fakeProcs(t, nil)
	fastFailover(t, 0, time.Minute)
	tokenAccount(t, "acc-a", "tok-a")
	tokenAccount(t, "acc-b", "tok-b")
	if err := writeFailoverDefaults(root, failoverDefaults{Failover: "on", Accounts: []string{"acc-b"}}); err != nil {
		t.Fatal(err)
	}
	return root, failoverStage(t, root, okTokens)
}

// A relaunch from the session's folder keeps the launch's relative paths:
// an --env-file and the playbooks root, given relative to a launch folder
// that is not next to the session's.
func TestFailoverRelativeLaunchPaths(t *testing.T) {
	root, out := failoverE2E(t, "tok-b")
	base, _ := filepath.EvalSymlinks(t.TempDir())
	work := filepath.Join(base, "a", "b", "c")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "rel.env"), []byte("FAKE_FROM_ENV_FILE=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	session, _ := filepath.EvalSymlinks(t.TempDir())
	// Both sides resolved, so the relative root climbs only to their
	// common folder and reads differently from the session's folder.
	realRoot, _ := filepath.EvalSymlinks(root)
	relRoot, err := filepath.Rel(work, realRoot)
	if err != nil || strings.Count(relRoot, "..") > 4 {
		t.Fatalf("relative root %q: %v", relRoot, err)
	}
	t.Setenv("FAKE_SESSION_DIR", session)
	t.Chdir(work)
	t.Cleanup(func() { config.PlaybooksDir = root })
	msg := captureStderr(t, func() {
		err = runRun(nil, []string{"--playbooks-dir", relRoot, "--env-file", "rel.env", "--account", "acc-a", "alpha"})
	})
	if err != nil {
		t.Fatalf("%v\n%s", err, msg)
	}
	if runs := standInRuns(t, out); len(runs) != 1 || runs[0].token != "tok-b" || runs[0].pwd != session || strings.Contains(msg, "could not resume") {
		t.Fatalf("runs: %+v\n%s", runs, msg)
	}
}

// A launch on the new account that ran the session (wrote to its
// transcript) and then failed is a move: there is no way back, and its
// error is the launch's.
func TestFailoverNewLaunchRanThenFailed(t *testing.T) {
	_, out := failoverE2E(t, "tok-a")
	work, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("FAKE_WRITE_FAIL_TOKENS", "tok-b")
	t.Chdir(work)
	var err error
	msg := captureStderr(t, func() { err = runRun(nil, []string{"--account", "acc-a", "alpha"}) })
	if err == nil || len(standInRuns(t, out)) != 0 || strings.Contains(msg, "could not resume") || !strings.Contains(msg, "this session moved from acc-a to acc-b at ") {
		t.Fatalf("err %v, runs %+v\n%s", err, standInRuns(t, out), msg)
	}
}

// When the way back fails too, cpb says nothing runs the session, never
// that it stayed, and prints the command that resumes it by hand, without
// an --env value.
func TestFailoverWayBackFailsToo(t *testing.T) {
	_, out := failoverE2E(t, "")
	work, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("FAKE_FAIL_TOKENS", "tok-b tok-a")
	t.Chdir(work)
	var err error
	msg := captureStderr(t, func() {
		err = runRun(nil, []string{"--env", "FAKE_SECRETISH=do-not-print", "--account", "acc-a", "alpha"})
	})
	want := "# cd " + work + " && cpb run --playbooks-dir " + shellWord(config.ResolvePlaybooksDir()) + " '--env=FAKE_SECRETISH=<your value>' --account acc-a alpha --resume " + failSID + " --model claude-opus-5-5"
	if err == nil || len(standInRuns(t, out)) != 0 || strings.Contains(msg, "stayed on") || strings.Contains(msg, "do-not-print") ||
		!strings.Contains(msg, "nor be resumed on acc-a: nothing runs it now") ||
		!strings.Contains(msg, "to resume it by hand, replace each <...> with its value and remove the leading #, then run:\n  "+want+"\n") {
		t.Fatalf("err %v\n%s\nwant %s", err, msg, want)
	}
	// No claude ran after the switch: the catch lasted until cpb run
	// returned, and ends there.
	if handoverSigs != nil {
		t.Fatal("Ctrl-C is still caught after cpb run returned")
	}
}

// A way back whose claude ran and then ended with an error is reported as
// any launch: the session stayed on the old account; no by-hand line.
func TestFailoverWayBackRanThenFailed(t *testing.T) {
	_, out := failoverE2E(t, "")
	work, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("FAKE_FAIL_TOKENS", "tok-b")
	t.Setenv("FAKE_RUN_FAIL_TOKENS", "tok-a")
	t.Chdir(work)
	var err error
	msg := captureStderr(t, func() { err = runRun(nil, []string{"--account", "acc-a", "alpha"}) })
	if err == nil || len(standInRuns(t, out)) != 0 || strings.Contains(msg, "nothing runs it now") || strings.Contains(msg, "by hand") ||
		!strings.Contains(msg, "could not move from acc-a to acc-b, and stayed on acc-a") {
		t.Fatalf("err %v\n%s", err, msg)
	}
}

// A claude that does not end at SIGTERM is killed after the wait, and the
// switch goes on.
func TestFailoverKillsAfterTheWait(t *testing.T) {
	_, out := failoverE2E(t, "tok-b")
	old := failoverKillAfter
	failoverKillAfter = 300 * time.Millisecond
	t.Cleanup(func() { failoverKillAfter = old })
	work, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("FAKE_IGNORE_TERM", "1")
	t.Chdir(work)
	start := time.Now()
	var err error
	msg := captureStderr(t, func() { err = runRun(nil, []string{"--account", "acc-a", "alpha"}) })
	if err != nil || time.Since(start) > 10*time.Second {
		t.Fatalf("%v after %v\n%s", err, time.Since(start), msg)
	}
	if runs := standInRuns(t, out); len(runs) != 1 || runs[0].token != "tok-b" {
		t.Fatalf("runs: %+v\n%s", runs, msg)
	}
	if handoverSigs != nil {
		t.Fatal("Ctrl-C is still caught after cpb run returned")
	}
}

// killAfter kills only the process it ended: a pid that another process
// took over (another start time) is left alone.
func TestFailoverKillAfterChecksTheProcess(t *testing.T) {
	c := exec.Command("sleep", "30")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Process.Kill() })
	started := procStartOf(c.Process.Pid)
	if started == "" {
		t.Skip("no process start times here")
	}
	ended := make(chan error, 1)
	go func() { ended <- c.Wait() }()
	killAfter(make(chan struct{}), c.Process.Pid, "Mon Jan 1 00:00:00 2001", time.Millisecond)
	select {
	case err := <-ended:
		t.Fatalf("another process's pid was killed: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	killAfter(make(chan struct{}), c.Process.Pid, started, time.Millisecond)
	select {
	case err := <-ended:
		if ee, ok := err.(*exec.ExitError); !ok || ee.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
			t.Fatalf("the process ended with %v, want SIGKILL", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("not killed")
	}
}

// A resume is refused while the session is live in another process, which
// ever of two live processes on one id is this launch's own.
func TestFailoverLiveInAnotherProcess(t *testing.T) {
	root := sandboxDefaultRoot(t)
	writePlaybook(t, root, "alpha", nil)
	dir := filepath.Join(root, "alpha")
	folder, _ := filepath.EvalSymlinks(t.TempDir())
	writeLimitTranscript(t, dir, folder, failSID, `{"type":"user"}`)
	writeSessionFile(t, dir, 4242, failSID, folder, "interactive", liveStart, time.Now())
	w := newFailoverWatch(t, "acc-a")
	w.configDir = dir
	fakeProcs(t, map[int]string{4242: liveStart})
	if err := w.resumeGuard(failSID, folder, 4242); err != nil {
		t.Fatalf("its own process: %v", err)
	}
	writeSessionFile(t, dir, 5555, failSID, folder, "interactive", liveStart, time.Now())
	fakeProcs(t, map[int]string{4242: liveStart, 5555: liveStart})
	for own, other := range map[int]string{4242: "pid 5555", 5555: "pid 4242"} {
		if err := w.resumeGuard(failSID, folder, own); err == nil || !strings.Contains(err.Error(), "is live in another process ("+other+")") {
			t.Errorf("own %d: %v", own, err)
		}
	}
}

// The catch of a switch's Ctrl-C lasts until the next claude has written
// its session file, not only until the launch's first process starts (a
// secret helper may run first).
func TestFailoverHandoverEndsWhenTheSessionFileAppears(t *testing.T) {
	sandboxDefaultRoot(t)
	fastFailover(t, 0, time.Minute)
	c := exec.Command("sleep", "30")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Process.Kill(); _ = c.Wait() })
	dir := t.TempDir()
	w := newFailoverWatch(t, "acc-a", "acc-b")
	w.configDir = dir
	catchHandover()
	t.Cleanup(releaseHandover)
	handoverClaudeStarted.Store(false)
	done := make(chan struct{})
	ended := make(chan *failoverSwitch, 1)
	go func() { ended <- w.watch(done, c.Process) }()
	time.Sleep(150 * time.Millisecond)
	handoverMu.Lock()
	caught := handoverSigs != nil
	handoverMu.Unlock()
	if !caught || handoverClaudeStarted.Load() {
		t.Fatal("the catch ended before the session file appeared")
	}
	writeSessionFile(t, dir, c.Process.Pid, failSID, dir, "interactive", procStartOf(c.Process.Pid), time.Now())
	deadline := time.Now().Add(3 * time.Second)
	for {
		handoverMu.Lock()
		caught = handoverSigs != nil
		handoverMu.Unlock()
		if !caught && handoverClaudeStarted.Load() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the catch did not end when the session file appeared")
		}
		time.Sleep(20 * time.Millisecond)
	}
	close(done)
	<-ended
}

// When SIGTERM cannot be sent, the catch it began ends at once.
func TestFailoverCatchEndsWhenSigtermFails(t *testing.T) {
	sandboxDefaultRoot(t)
	c := exec.Command("sleep", "30")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Process.Kill(); _ = c.Wait() })
	started := procStartOf(c.Process.Pid)
	if started == "" {
		t.Skip("no process start times here")
	}
	old := failoverTerm
	failoverTerm = func(*os.Process, int) error { return errors.New("refused") }
	t.Cleanup(func() { failoverTerm = old })
	far := time.Now().Add(3 * time.Hour)
	dir, transcript, _ := writeWatched(t, failSID, "interactive", "idle", c.Process.Pid, started, far, time.Now(), "seven_day", 1000)
	fi, _ := os.Stat(transcript)
	w := newFailoverWatch(t, "acc-a", "acc-b")
	w.configDir = dir
	cand := &failoverCandidate{sw: &failoverSwitch{From: "acc-a", To: "acc-b", sessionID: failSID, transcript: transcript, resetsAt: far}, mtime: fi.ModTime(), procStart: started}
	if w.act(cand, c.Process.Pid, c.Process) || handoverSigs != nil || !strings.Contains(notes(w), "could not end the session") {
		t.Fatalf("act with a refused SIGTERM: caught %v, notes %v", handoverSigs != nil, w.episode.notes)
	}
}

// Claude Code names a project directory per character: one '-' for each
// that is not an ASCII letter or digit, two for one outside the Basic
// Multilingual Plane.
func TestEncodeProjectDirPerCharacter(t *testing.T) {
	for in, want := range map[string]string{
		"/a/b-c":         "-a-b-c",
		"/enc/çx-ü":      "-enc--x--",
		"/x/\U0001F600y": "-x---y",
	} {
		if got := encodeProjectDir(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

// The pre-check reads a relative --settings file from the launch folder,
// as the relaunch does, not from the session's folder.
func TestFailoverPreCheckReadsSettingsFromTheLaunchFolder(t *testing.T) {
	root := sandboxDefaultRoot(t)
	writePlaybook(t, root, "alpha", nil)
	tokenAccount(t, "acc-a", "a")
	tokenAccount(t, "acc-b", "b")
	if err := writeFailoverDefaults(root, failoverDefaults{Failover: "on", Accounts: []string{"acc-b"}}); err != nil {
		t.Fatal(err)
	}
	work, _ := filepath.EvalSymlinks(t.TempDir())
	if err := os.WriteFile(filepath.Join(work, "s.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(work)
	pb, err := playbook.Find(root, "alpha")
	if err != nil || pb == nil {
		t.Fatal(err)
	}
	w, err := failoverWatchFor(failoverWatchInput{pb: pb, configDir: pb.Path, wd: work, from: "acc-a", claudeArgs: []string{"--settings", "s.json"}})
	if err != nil || w == nil {
		t.Fatalf("watch: %v %v", w, err)
	}
	if err := w.canRunOn("acc-b", t.TempDir()); err != nil {
		t.Fatalf("checked against the session's folder: %v", err)
	}
}

// A relative CPB_PLAYBOOKS_DIR is made absolute before the folder changes.
func TestPinPlaybooksDirFromTheEnvironment(t *testing.T) {
	resetCommandTestState(t)
	work, _ := filepath.EvalSymlinks(t.TempDir())
	t.Chdir(work)
	t.Setenv(config.PlaybooksDirEnv, "rel/root")
	pinPlaybooksDir()
	want := filepath.Join(work, "rel", "root")
	// Absolute in the environment too, which the launch's processes see.
	if os.Getenv(config.PlaybooksDirEnv) != want || config.ResolvePlaybooksDir() != want {
		t.Fatalf("env %q, root %q, want %q", os.Getenv(config.PlaybooksDirEnv), config.ResolvePlaybooksDir(), want)
	}
}

// The line said when the new account did not run the session shows a
// control character in the error as '?'.
func TestRelaunchFailedLineHasNoControlCharacter(t *testing.T) {
	line := relaunchFailedLine(&failoverSwitch{From: "acc-a", To: "acc-b"}, errors.New("bad \x1b[31mthing\nmore"))
	if line != "cpb: could not resume this session on acc-b (bad ?[31mthing); resuming it on acc-a instead\n" {
		t.Fatalf("%q", line)
	}
}

// The by-hand line runs as typed in a POSIX shell: the playbooks root when
// one was given, no --env value, no inline settings JSON, a control
// character as an escape, and a word a shell would expand quoted.
func TestResumeByHand(t *testing.T) {
	resetCommandTestState(t)
	config.PlaybooksDir = "/srv/pb"
	got, filled := resumeByHand("/w/my dir", []string{"--env=K=v", `--settings={"env":{"A":"b"}}`, "--settings", `{"x":1}`, "--account", "a", "alpha",
		"--resume", sidLive, "--append-system-prompt", "one\ntwo", "--system-prompt", "bad\x1b[31m", "--agent=x\r", "--settings=s\u202e.json", "=x", "~y", "zw\u200dj"})
	want := `# cd '/w/my dir' && cpb run --playbooks-dir /srv/pb '--env=K=<your value>' '--settings=<your settings JSON>' --settings '<your settings JSON>' --account a alpha --resume ` +
		sidLive + " --append-system-prompt '<a value with control characters, not shown>' --system-prompt '<a value with control characters, not shown>'" +
		" '--agent=<a value with control characters, not shown>' '--settings=<a value with control characters, not shown>' '=x' '~y' 'zw\u200dj'"
	if got != want || !filled || strings.IndexFunc(got, func(r rune) bool { return hidden(r) && r != '\n' }) >= 0 {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	// Unmarked, a newline stands inside single quotes as itself.
	if line, filled := resumeByHand("/w", []string{"--append-system-prompt", "one\ntwo", "--account", "a", "alpha", "--resume", sidLive}); filled ||
		strings.HasPrefix(line, "#") || !strings.Contains(line, "'one\ntwo'") {
		t.Fatalf("nothing hidden, yet marked: %q", line)
	}
	// Each way a value is hidden marks the line on its own: a separate
	// word, an inline value.
	for _, args := range [][]string{{"--system-prompt", "bad\x1b[31m", "alpha"}, {"--agent=x\r", "alpha"}} {
		if line, filled := resumeByHand("/w", args); !filled || !strings.HasPrefix(line, "# ") || strings.Contains(line, "\n") {
			t.Errorf("%q: %q %v", args, line, filled)
		}
	}
	// Every word reads back as itself in the system's POSIX shell.
	for _, v := range []string{"plain", "my dir", "it's", "one\ntwo\n", "tab\there", "=x", "~y", "$(echo no)", "`no`", "a\\b"} {
		out, err := exec.Command("/bin/sh", "-c", "printf %s "+shellWord(v)).Output()
		if err != nil || string(out) != v {
			t.Errorf("%q came back as %q (%v)", v, out, err)
		}
	}
}

// A by-hand line with a value not shown, pasted as it is, starts nothing,
// in every shell a person types it into; with nothing hidden it runs.
func TestResumeByHandPasteRunsNothing(t *testing.T) {
	resetCommandTestState(t)
	bin := t.TempDir()
	marker := filepath.Join(t.TempDir(), "ran")
	if err := os.WriteFile(filepath.Join(bin, "cpb"), []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	folder := t.TempDir()
	second := filepath.Join(t.TempDir(), "second")
	// A value shown in an unmarked line would keep its newlines; in a
	// marked one it is not shown, so its second line never reaches a shell.
	hiddenLine, filled := resumeByHand(folder, []string{"--env=K=v", "--append-system-prompt", "first\ntouch " + second + "\nthird", "--account", "a", "alpha"})
	plainLine, plain := resumeByHand(folder, []string{"--account", "a", "alpha"})
	if !filled || plain {
		t.Fatalf("marks: %v %v", filled, plain)
	}
	shells := [][]string{{"/bin/sh", "-c"}}
	for _, sh := range [][]string{{"dash", "-c"}, {"bash", "--norc", "-i", "-c"}, {"zsh", "-f", "-i", "-c"}} {
		if p, err := exec.LookPath(sh[0]); err == nil {
			shells = append(shells, append([]string{p}, sh[1:]...))
		}
	}
	for _, sh := range shells {
		_ = os.Remove(marker)
		// As one argument, and as pasted text on the shell's input.
		for _, c := range []*exec.Cmd{exec.Command(sh[0], append(sh[1:], hiddenLine)...), exec.Command(sh[0])} {
			if len(c.Args) == 1 {
				c.Stdin = strings.NewReader(hiddenLine + "\n")
			}
			c.Env = append(os.Environ(), "PATH="+bin+":/usr/bin:/bin")
			_ = c.Run()
		}
		if _, err := os.Stat(marker); err == nil {
			t.Errorf("%s: the line with a hidden value ran cpb", sh[0])
		}
		if _, err := os.Stat(second); err == nil {
			t.Errorf("%s: a hidden value's second line ran", sh[0])
		}
		c := exec.Command(sh[0], append(sh[1:], plainLine)...)
		c.Env = append(os.Environ(), "PATH="+bin+":/usr/bin:/bin")
		_ = c.Run()
		if _, err := os.Stat(marker); err != nil {
			t.Errorf("%s: the plain line did not run cpb", sh[0])
		}
	}
}

// A first claude whose session file gives no start time, stopped at a
// limit: cpb says once that it does not switch, and does not.
func TestFailoverUnconfirmedSessionSaysSoOnce(t *testing.T) {
	_, out := failoverE2E(t, "tok-b")
	work, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("FAKE_NO_PROCSTART", "1")
	t.Setenv("FAKE_WAIT", "15")
	t.Chdir(work)
	var err error
	msg := captureStderr(t, func() { err = runRun(nil, []string{"--account", "acc-a", "alpha"}) })
	if len(standInRuns(t, out)) != 0 || strings.Contains(msg, "resuming this session") ||
		strings.Count(msg, "gives no start time that matches its process") != 1 {
		t.Fatalf("err %v\n%s", err, msg)
	}
}

// The new account's claude ran (its session file confirmed) and failed
// before writing; the way back then never ran: that is still reported as
// nothing running the session, the first claude's start not counted for it.
func TestFailoverStrandedAfterTheNewClaudeRan(t *testing.T) {
	_, out := failoverE2E(t, "")
	work, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("FAKE_RUN_FAIL_TOKENS", "tok-b")
	t.Setenv("FAKE_FAIL_TOKENS", "tok-a")
	t.Chdir(work)
	var err error
	msg := captureStderr(t, func() { err = runRun(nil, []string{"--account", "acc-a", "alpha"}) })
	if err == nil || len(standInRuns(t, out)) != 0 || !strings.Contains(msg, "nor be resumed on acc-a: nothing runs it now") {
		t.Fatalf("err %v\n%s", err, msg)
	}
}

// The same for the child of a secret helper: a stale file on its pid is
// not the next claude.
func TestFailoverHandoverIgnoresAStaleHelperChildFile(t *testing.T) {
	sandboxDefaultRoot(t)
	fastFailover(t, 0, time.Minute)
	pidFile := filepath.Join(t.TempDir(), "pid")
	helper := exec.Command("/bin/sh", "-c", "sleep 30 & echo $! > "+pidFile+"; wait")
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = helper.Process.Kill(); _ = helper.Wait() })
	var child int
	for i := 0; i < 100 && child == 0; i++ {
		b, _ := os.ReadFile(pidFile)
		child, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		time.Sleep(10 * time.Millisecond)
	}
	if child == 0 {
		t.Fatal("the helper's child did not start")
	}
	t.Cleanup(func() { _ = syscall.Kill(child, syscall.SIGKILL) })
	dir := t.TempDir()
	writeSessionFile(t, dir, child, failSID, dir, "interactive", "Mon Jan 1 00:00:00 2001", time.Now())
	w := newFailoverWatch(t, "acc-a", "acc-b")
	w.configDir = dir
	catchHandover()
	t.Cleanup(releaseHandover)
	handoverClaudeStarted.Store(false)
	done := make(chan struct{})
	ended := make(chan *failoverSwitch, 1)
	go func() { ended <- w.watch(done, helper.Process) }()
	time.Sleep(200 * time.Millisecond)
	close(done)
	<-ended
	if handoverClaudeStarted.Load() {
		t.Fatal("a stale session file of the helper's child counted as the next claude")
	}
}

// A session file a process before left on the launch's pid (another start
// time) is not the next claude: the catch stays.
func TestFailoverHandoverIgnoresAStaleSessionFile(t *testing.T) {
	sandboxDefaultRoot(t)
	fastFailover(t, 0, time.Minute)
	c := exec.Command("sleep", "30")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Process.Kill(); _ = c.Wait() })
	dir := t.TempDir()
	writeSessionFile(t, dir, c.Process.Pid, failSID, dir, "interactive", "Mon Jan 1 00:00:00 2001", time.Now())
	w := newFailoverWatch(t, "acc-a", "acc-b")
	w.configDir = dir
	catchHandover()
	t.Cleanup(releaseHandover)
	handoverClaudeStarted.Store(false)
	done := make(chan struct{})
	ended := make(chan *failoverSwitch, 1)
	go func() { ended <- w.watch(done, c.Process) }()
	time.Sleep(200 * time.Millisecond)
	close(done)
	<-ended
	handoverMu.Lock()
	caught := handoverSigs != nil
	handoverMu.Unlock()
	if !caught || handoverClaudeStarted.Load() {
		t.Fatal("a stale session file counted as the next claude")
	}
}

// writeWatched writes, in a config dir of its own, a session file of pid
// and a transcript whose last line is a limit written at at, in a folder
// of its own.
func writeWatched(t *testing.T, sid, kind, status string, pid int, procStart string, resets, at time.Time, window string, contextTokens int) (configDir, transcript, folder string) {
	t.Helper()
	configDir = t.TempDir()
	folder, _ = filepath.EvalSymlinks(t.TempDir())
	if err := os.MkdirAll(filepath.Join(configDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"pid": pid, "sessionId": sid, "cwd": folder, "startedAt": 1, "procStart": procStart, "kind": kind, "status": status})
	if err := os.WriteFile(filepath.Join(configDir, "sessions", strconv.Itoa(pid)+".json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(configDir, "projects", encodeProjectDir(folder))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ts := at.UTC().Format("2006-01-02T15:04:05.000Z")
	lines := fmt.Sprintf(`{"type":"assistant","cwd":%q,"timestamp":%q,"message":{"model":"claude-opus-5-5","usage":{"input_tokens":%d}}}`+"\n"+
		`{"type":"assistant","cwd":%q,"timestamp":%q,"isApiErrorMessage":true,"error":"rate_limit","message":{"model":"<synthetic>"},"quotaLimits":{"status":"rejected","rateLimitType":%q,"resetsAt":%d}}`+"\n",
		folder, ts, contextTokens, folder, ts, window, resets.Unix())
	transcript = filepath.Join(dir, sid+".jsonl")
	if err := os.WriteFile(transcript, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	return configDir, transcript, folder
}

// moveWatched points what writeWatched wrote elsewhere: the session file's
// cwd, the cwd of every transcript line, and the project directory that
// holds the transcript (that of folder). It returns the transcript's path.
func moveWatched(t *testing.T, configDir, transcript, from string, pid int, sessionCwd, lineCwd, folder string) string {
	t.Helper()
	sf := filepath.Join(configDir, "sessions", strconv.Itoa(pid)+".json")
	b, _ := os.ReadFile(sf)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	m["cwd"] = sessionCwd
	b, _ = json.Marshal(m)
	if err := os.WriteFile(sf, b, 0o600); err != nil {
		t.Fatal(err)
	}
	q, _ := json.Marshal(from)
	r, _ := json.Marshal(lineCwd)
	lines, _ := os.ReadFile(transcript)
	dir := filepath.Join(configDir, "projects", encodeProjectDir(folder))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(transcript); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(dir, filepath.Base(transcript))
	if err := os.WriteFile(moved, []byte(strings.ReplaceAll(string(lines), `"cwd":`+string(q), `"cwd":`+string(r))), 0o600); err != nil {
		t.Fatal(err)
	}
	return moved
}

// newFailoverWatch is a watcher of a launch on from, started an hour ago.
func newFailoverWatch(t *testing.T, from string, targets ...string) *failoverWatch {
	t.Helper()
	var ts []failoverTarget
	for _, s := range targets {
		tg, err := parseFailoverTarget(s)
		if err != nil {
			t.Fatal(err)
		}
		ts = append(ts, tg)
	}
	return &failoverWatch{cfg: failoverConfig{on: true, accounts: ts}, from: from, fromClass: accountClass(from),
		episode: &failoverEpisode{tried: map[string]bool{}}, since: time.Now().Add(-time.Hour)}
}

func notes(w *failoverWatch) string { return strings.Join(w.episode.notes, "\n") }

func TestFailoverCheck(t *testing.T) {
	sandboxDefaultRoot(t)
	tokenAccount(t, "acc-a", "a")
	tokenAccount(t, "acc-b", "b")
	tokenAccount(t, "acc-c", "c")
	putAccount(t, account.Account{Name: "router", Provider: account.Gateway, BaseURL: "http://localhost:4000/v1", Auth: account.Auth{Kind: account.None}})
	putAccount(t, account.Account{Name: "router2", Provider: account.Gateway, BaseURL: "http://localhost:5000/v1", Auth: account.Auth{Kind: account.None}})
	putAccount(t, account.Account{Name: "trusted", Provider: account.Gateway, BaseURL: "https://gw.example.com/v1", ClaudeLoginTrusted: true,
		Auth: account.Auth{Kind: account.ClaudeToken, Value: "t"}})
	// A gateway trusted with a Claude credential is another kind than
	// Anthropic itself.
	if accountClass("trusted") == accountClass("acc-a") || accountClass("acc-a") != accountClass("acc-b") {
		t.Fatalf("kinds: %q %q %q", accountClass("trusted"), accountClass("acc-a"), accountClass("acc-b"))
	}
	far := time.Now().Add(3 * time.Hour)
	now := time.Now()
	// The session's process is this test's own: its start time is real.
	me := os.Getpid()
	meStart := procStartOf(me)
	if meStart == "" {
		t.Skip("no process start times here")
	}

	t.Run("the first account it can use, from the session's folder", func(t *testing.T) {
		fastFailover(t, 0, 30*time.Minute)
		dir, _, folder := writeWatched(t, failSID, "interactive", "idle", me, meStart, far, now, "seven_day", 1000)
		w := newFailoverWatch(t, "acc-a", "acc-a", "router", "acc-b", "acc-c")
		w.configDir = dir
		w.fullAccounts = func() map[string]bool { return map[string]bool{"acc-b": true} }
		var folders []string
		w.canRunOn = func(name, f string) error { folders = append(folders, f); return nil }
		c := w.check(me)
		if c == nil || c.sw.To != "acc-c" || c.sw.From != "acc-a" || c.sw.sessionID != failSID || c.sw.model != "claude-opus-5-5" ||
			c.sw.Context != 1000 || c.sw.folder != folder || strings.Join(folders, ",") != folder || c.procStart != meStart {
			t.Fatalf("candidate: %+v notes %v folders %v", c, w.episode.notes, folders)
		}
		// A check marks nothing: until a switch is made, the next poll
		// decides again.
		if again := w.check(me); again == nil || len(w.episode.tried) != 0 || !w.handled.IsZero() {
			t.Fatalf("a check that made no switch was kept: %+v %v %v", again, w.episode.tried, w.handled)
		}
	})
	t.Run("the 5-hour window too", func(t *testing.T) {
		fastFailover(t, 0, 30*time.Minute)
		dir, _, _ := writeWatched(t, failSID, "interactive", "idle", me, meStart, far, now, "five_hour", 1000)
		w := newFailoverWatch(t, "acc-a", "acc-b")
		w.configDir = dir
		if c := w.check(me); c == nil || c.sw.Window != "five_hour" {
			t.Fatalf("five_hour: %+v", c)
		}
	})
	t.Run("nothing it may not act on", func(t *testing.T) {
		fastFailover(t, 0, 30*time.Minute)
		for _, c := range []struct {
			name, sid, kind, status string
			resets, at              time.Time
			quiet                   time.Duration
		}{
			{"busy", failSID, "interactive", "busy", far, now, 0},
			{"a background shell", failSID, "interactive", "shell", far, now, 0},
			{"a session that is not interactive", failSID, "bg", "idle", far, now, 0},
			{"a session id of another shape", "--resume-me", "interactive", "idle", far, now, 0},
			{"resets in 10 minutes", failSID, "interactive", "idle", now.Add(10 * time.Minute), now, 0},
			{"written just now", failSID, "interactive", "idle", far, now, time.Hour},
			{"a limit from before the launch", failSID, "interactive", "idle", far, now.Add(-2 * time.Hour), 0},
		} {
			failoverQuiet = c.quiet
			dir, _, _ := writeWatched(t, c.sid, c.kind, c.status, me, meStart, c.resets, c.at, "seven_day", 1000)
			w := newFailoverWatch(t, "acc-a", "acc-b")
			w.configDir = dir
			if got := w.check(me); got != nil {
				t.Errorf("%s: a switch to %s", c.name, got.sw.To)
			}
		}
	})
	t.Run("kinds by address; no model across kinds", func(t *testing.T) {
		fastFailover(t, 0, 30*time.Minute)
		dir, _, _ := writeWatched(t, failSID, "interactive", "idle", me, meStart, far, now, "seven_day", 1000)
		w := newFailoverWatch(t, "router", "router2")
		w.configDir = dir
		if c := w.check(me); c != nil || !strings.Contains(notes(w), "router2 is another kind of account") {
			t.Fatalf("two gateways at two addresses: %+v %v", c, w.episode.notes)
		}
		w = newFailoverWatch(t, "acc-a", "router (any provider)")
		w.configDir = dir
		if c := w.check(me); c == nil || c.sw.To != "router" || c.sw.model != "" {
			t.Fatalf("a marked entry of another kind: %+v", c)
		}
	})
	t.Run("a model id of another shape is not passed on", func(t *testing.T) {
		fastFailover(t, 0, 30*time.Minute)
		dir, transcript, _ := writeWatched(t, failSID, "interactive", "idle", me, meStart, far, now, "seven_day", 1000)
		b, _ := os.ReadFile(transcript)
		if err := os.WriteFile(transcript, []byte(strings.Replace(string(b), `"model":"claude-opus-5-5"`, `"model":"--dangerously-skip-permissions"`, 1)), 0o600); err != nil {
			t.Fatal(err)
		}
		w := newFailoverWatch(t, "acc-a", "acc-b")
		w.configDir = dir
		if c := w.check(me); c == nil || c.sw.model != "" {
			t.Fatalf("model: %+v", c)
		}
	})
	t.Run("refusals and caps say why, once", func(t *testing.T) {
		fastFailover(t, 0, 30*time.Minute)
		dir, _, _ := writeWatched(t, failSID, "interactive", "idle", me, meStart, far, now, "seven_day", 500000)
		w := newFailoverWatch(t, "acc-a", "acc-b", "router")
		w.configDir = dir
		w.cfg.maxContext = 400000
		if c := w.check(me); c != nil || len(w.episode.notes) != 1 || !strings.Contains(w.episode.notes[0], "500,000 tokens, is above failover_max_context (400,000)") {
			t.Fatalf("cap: %+v %v", c, w.episode.notes)
		}
		if again := w.check(me); again != nil || len(w.episode.notes) != 1 {
			t.Fatalf("decided twice: %v", w.episode.notes)
		}
		// Under a cap, a size the tail does not hold is no switch either.
		dir, _, _ = writeWatched(t, failSID, "interactive", "idle", me, meStart, far, now, "seven_day", 0)
		w = newFailoverWatch(t, "acc-a", "acc-b")
		w.configDir, w.cfg.maxContext = dir, 400000
		if c := w.check(me); c != nil || !strings.Contains(notes(w), "the context's size is not in the transcript") {
			t.Fatalf("unknown size under a cap: %+v %v", c, w.episode.notes)
		}
		dir, _, _ = writeWatched(t, failSID, "interactive", "idle", me, meStart, far, now, "seven_day", 1000)
		w = newFailoverWatch(t, "acc-a", "acc-b", "router")
		w.configDir = dir
		calls := 0
		w.canRunOn = func(name, folder string) error {
			calls++
			return fmt.Errorf("playbook alpha may not run on %s\nmore", name)
		}
		if c := w.check(me); c != nil || !strings.Contains(notes(w), "acc-b: playbook alpha may not run on acc-b; router is another kind of account") {
			t.Fatalf("none left: %+v %v", c, w.episode.notes)
		}
		// A limit it declined is not weighed again on the next poll.
		if c := w.check(me); c != nil || calls != 1 {
			t.Fatalf("weighed again: %+v, %d checks of the target", c, calls)
		}
		w = newFailoverWatch(t, "acc-a", "acc-b")
		w.configDir = dir
		at := time.Now().UTC().Format(time.RFC3339)
		w.episode.switches = []*failoverSwitch{{At: at}, {At: at}, {At: at}}
		if c := w.check(me); c != nil || !strings.Contains(notes(w), "3 switches in the last 5 hours") {
			t.Fatalf("switch cap: %+v %v", c, w.episode.notes)
		}
		// A resume that would be refused is refused before anything ends:
		// here the folder the session ran in is gone.
		dir, _, folder := writeWatched(t, failSID, "interactive", "idle", me, meStart, far, now, "seven_day", 1000)
		if err := os.Remove(folder); err != nil {
			t.Fatal(err)
		}
		w = newFailoverWatch(t, "acc-a", "acc-b")
		w.configDir = dir
		if c := w.check(me); c != nil || !strings.Contains(notes(w), "is not there") {
			t.Fatalf("folder gone: %+v %v", c, w.episode.notes)
		}
	})
	t.Run("the resume folder is the one the transcript's project directory names", func(t *testing.T) {
		fastFailover(t, 0, 30*time.Minute)
		// The agent changed directory: the session file and the last line
		// say <folder>/sub; the transcript is under <folder>'s project
		// directory, which is the launch folder.
		dir, transcript, folder := writeWatched(t, failSID, "interactive", "idle", me, meStart, far, now, "seven_day", 1000)
		moveWatched(t, dir, transcript, folder, me, folder+"/sub", folder+"/sub", folder)
		w := newFailoverWatch(t, "acc-a", "acc-b")
		w.configDir, w.launchDir = dir, folder
		if c := w.check(me); c == nil || c.sw.folder != folder {
			t.Fatalf("resumed from: %+v %v", c, w.episode.notes)
		}
		// No candidate names that project directory: no switch, said once.
		w = newFailoverWatch(t, "acc-a", "acc-b")
		w.configDir, w.launchDir = dir, t.TempDir()
		if c := w.check(me); c != nil || !strings.Contains(notes(w), "the folder the session's transcript belongs to is not known") {
			t.Fatalf("no match: %+v %v", c, w.episode.notes)
		}
	})
	t.Run("a folder with a control character is not resumed from, and is printed quoted", func(t *testing.T) {
		fastFailover(t, 0, 30*time.Minute)
		bad := filepath.Join(t.TempDir(), "bad\x1bname")
		if err := os.Mkdir(bad, 0o755); err != nil {
			t.Fatal(err)
		}
		dir, transcript, folder := writeWatched(t, failSID, "interactive", "idle", me, meStart, far, now, "seven_day", 1000)
		moveWatched(t, dir, transcript, folder, me, bad, bad, bad)
		w := newFailoverWatch(t, "acc-a", "acc-b")
		w.configDir = dir
		if c := w.check(me); c != nil || !strings.Contains(notes(w), `bad\x1bname" holds a character cpb does not print as written`) || strings.ContainsRune(notes(w), 0x1b) {
			t.Fatalf("control character: %+v %q", c, w.episode.notes)
		}
	})
	t.Run("only switches of the last 5 hours count toward the cap", func(t *testing.T) {
		fastFailover(t, 0, 30*time.Minute)
		dir, _, _ := writeWatched(t, failSID, "interactive", "idle", me, meStart, far, now, "seven_day", 1000)
		w := newFailoverWatch(t, "acc-a", "acc-b")
		w.configDir = dir
		old := time.Now().Add(-6 * time.Hour).UTC().Format(time.RFC3339)
		w.episode.switches = []*failoverSwitch{{At: old}, {At: old}, {At: old}}
		if c := w.check(me); c == nil {
			t.Fatalf("three switches six hours ago capped it: %v", w.episode.notes)
		}
	})
	t.Run("a process it cannot confirm is declined once", func(t *testing.T) {
		fastFailover(t, 0, 30*time.Minute)
		dir, _, _ := writeWatched(t, failSID, "interactive", "idle", me, "Mon Jan 1 00:00:00 2001", far, now, "seven_day", 1000)
		w := newFailoverWatch(t, "acc-a", "acc-b")
		w.configDir = dir
		calls := 0
		w.canRunOn = func(name, folder string) error { calls++; return nil }
		for i := 0; i < 3; i++ {
			if c := w.check(me); c != nil {
				t.Fatalf("a switch: %+v", c)
			}
		}
		if calls != 0 || len(w.episode.notes) != 1 || w.handled.Unix() != far.Unix() || !strings.Contains(notes(w), "no start time that matches its process") {
			t.Fatalf("calls %d, notes %v, handled %v", calls, w.episode.notes, w.handled)
		}
	})
	t.Run("the launch folder before the session file's: two folders, one encoded name", func(t *testing.T) {
		fastFailover(t, 0, 30*time.Minute)
		base, _ := filepath.EvalSymlinks(t.TempDir())
		dash, slash := filepath.Join(base, "my-app"), filepath.Join(base, "my", "app")
		for _, d := range []string{dash, slash} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		dir, transcript, folder := writeWatched(t, failSID, "interactive", "idle", me, meStart, far, now, "seven_day", 1000)
		moveWatched(t, dir, transcript, folder, me, slash, slash, dash)
		w := newFailoverWatch(t, "acc-a", "acc-b")
		w.configDir, w.launchDir = dir, dash
		if c := w.check(me); c == nil || c.sw.folder != dash {
			t.Fatalf("resumed from: %+v %v", c, w.episode.notes)
		}
	})
	t.Run("a launch through a symlink finds the resolved folder", func(t *testing.T) {
		fastFailover(t, 0, 30*time.Minute)
		dir, transcript, folder := writeWatched(t, failSID, "interactive", "idle", me, meStart, far, now, "seven_day", 1000)
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(folder, link); err != nil {
			t.Fatal(err)
		}
		moveWatched(t, dir, transcript, folder, me, folder+"/sub", folder+"/sub", folder)
		w := newFailoverWatch(t, "acc-a", "acc-b")
		w.configDir, w.launchDir = dir, link
		if c := w.check(me); c == nil || c.sw.folder != folder {
			t.Fatalf("resumed from: %+v %v", c, w.episode.notes)
		}
	})
	t.Run("the session file's folder is a candidate", func(t *testing.T) {
		fastFailover(t, 0, 30*time.Minute)
		dir, transcript, folder := writeWatched(t, failSID, "interactive", "idle", me, meStart, far, now, "seven_day", 1000)
		moveWatched(t, dir, transcript, folder, me, folder, folder+"/sub", folder)
		w := newFailoverWatch(t, "acc-a", "acc-b")
		w.configDir, w.launchDir = dir, t.TempDir()
		if c := w.check(me); c == nil || c.sw.folder != folder {
			t.Fatalf("resumed from: %+v %v", c, w.episode.notes)
		}
	})
	t.Run("only an absolute folder is a candidate", func(t *testing.T) {
		fastFailover(t, 0, 30*time.Minute)
		dir, transcript, folder := writeWatched(t, failSID, "interactive", "idle", me, meStart, far, now, "seven_day", 1000)
		// The project directory is named for "rel/dir", which only a
		// relative cwd in the transcript spells.
		moved := moveWatched(t, dir, transcript, folder, me, folder+"/sub", "rel/dir", folder)
		named := filepath.Join(dir, "projects", encodeProjectDir("rel/dir"))
		if err := os.MkdirAll(named, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(moved, filepath.Join(named, filepath.Base(moved))); err != nil {
			t.Fatal(err)
		}
		w := newFailoverWatch(t, "acc-a", "acc-b")
		w.configDir, w.launchDir = dir, t.TempDir()
		if c := w.check(me); c != nil || !strings.Contains(notes(w), "is not known") {
			t.Fatalf("a relative folder: %+v %v", c, w.episode.notes)
		}
	})
	t.Run("each account once per episode", func(t *testing.T) {
		w := newFailoverWatch(t, "acc-a", "acc-b", "acc-c")
		w.episode.tried["acc-b"] = true
		if to, _ := w.pick(""); to != "acc-c" {
			t.Fatalf("picked %q", to)
		}
		w.episode.tried["acc-c"] = true
		if to, why := w.pick(""); to != "" || !strings.Contains(why, "every listed account was tried") {
			t.Fatalf("picked %q: %s", to, why)
		}
	})
}

// act ends claude only while the session is still what check saw, and
// only a process of the launch's own; then, and only then, the limit and
// both accounts are marked.
func TestFailoverAct(t *testing.T) {
	sandboxDefaultRoot(t)
	start := func() *exec.Cmd {
		t.Helper()
		c := exec.Command("sleep", "30")
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Process.Kill(); _ = c.Wait() })
		return c
	}
	child := start()
	pid := child.Process.Pid
	started := procStartOf(pid)
	if started == "" {
		t.Skip("no process start times here")
	}
	far := time.Now().Add(3 * time.Hour)
	setup := func(status, procStart string) (*failoverWatch, *failoverCandidate, string) {
		t.Helper()
		dir, transcript, _ := writeWatched(t, failSID, "interactive", status, pid, procStart, far, time.Now(), "seven_day", 1000)
		fi, _ := os.Stat(transcript)
		w := newFailoverWatch(t, "acc-a", "acc-b")
		w.configDir = dir
		c := &failoverCandidate{sw: &failoverSwitch{From: "acc-a", To: "acc-b", sessionID: failSID, transcript: transcript, resetsAt: far},
			mtime: fi.ModTime(), procStart: procStart}
		return w, c, transcript
	}
	for name, f := range map[string]func() (*failoverWatch, *failoverCandidate, *os.Process){
		"busy": func() (*failoverWatch, *failoverCandidate, *os.Process) {
			w, c, _ := setup("busy", started)
			return w, c, child.Process
		},
		"another process on the pid": func() (*failoverWatch, *failoverCandidate, *os.Process) {
			w, c, _ := setup("idle", "Mon Jan 1 00:00:00 2001")
			return w, c, child.Process
		},
		"another session in the file": func() (*failoverWatch, *failoverCandidate, *os.Process) {
			w, c, _ := setup("idle", started)
			c.sw.sessionID = sidLive
			return w, c, child.Process
		},
		"written since": func() (*failoverWatch, *failoverCandidate, *os.Process) {
			w, c, tr := setup("idle", started)
			later := time.Now().Add(time.Minute)
			_ = os.Chtimes(tr, later, later)
			return w, c, child.Process
		},
		"not the launch's own": func() (*failoverWatch, *failoverCandidate, *os.Process) {
			w, c, _ := setup("idle", started)
			return w, c, start().Process
		},
	} {
		w, c, p := f()
		if w.act(c, pid, p) || len(w.episode.tried) != 0 || !w.handled.IsZero() {
			t.Fatalf("%s: acted, or marked: %v %v", name, w.episode.tried, w.handled)
		}
	}
	if handoverSigs != nil {
		t.Fatal("Ctrl-C is caught after an act that ended nothing")
	}
	w, c, _ := setup("idle", started)
	if !w.act(c, pid, child.Process) {
		t.Fatal("did not act on its own idle session")
	}
	// From the act until the next claude starts, a Ctrl-C is caught.
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case <-handoverSigs:
	case <-time.After(2 * time.Second):
		t.Fatal("Ctrl-C was not caught")
	}
	releaseHandover()
	err := child.Wait()
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.Sys().(syscall.WaitStatus).Signal() != syscall.SIGTERM {
		t.Fatalf("the child ended with %v, want SIGTERM", err)
	}
	if !w.episode.tried["acc-a"] || !w.episode.tried["acc-b"] || !w.handled.Equal(far) {
		t.Fatalf("not marked after the switch: %v %v", w.episode.tried, w.handled)
	}
}

// A launch whose claude flags a resume would not keep is not moved;
// --failover there, or with no account listed, is refused.
func TestFailoverBlockingFlags(t *testing.T) {
	for args, want := range map[string]string{
		"--permission-mode auto do-this":                                  "",
		"--resume " + failSID + " --model claude-opus-5-5 --fork-session": "",
		"--continue --effort=high":                                        "",
		"--permission-mode auto -- x":                                     "",
		"--disallowedTools Bash":                                          "--disallowedTools",
		"-p hi":                                                           "-p",
		"--add-dir /tmp --verbose":                                        "--add-dir",
	} {
		if got := failoverBlockingFlag(strings.Fields(args)); got != want {
			t.Errorf("%s: %q, want %q", args, got, want)
		}
	}
	root := sandboxDefaultRoot(t)
	writePlaybook(t, root, "alpha", nil)
	tokenAccount(t, "acc-b", "b")
	pb, err := playbook.Find(root, "alpha")
	if err != nil || pb == nil {
		t.Fatal(err)
	}
	if err := writeFailoverDefaults(root, failoverDefaults{Failover: "on", Accounts: []string{"acc-b"}}); err != nil {
		t.Fatal(err)
	}
	in := failoverWatchInput{pb: pb, configDir: pb.Path, claudeArgs: []string{"--disallowedTools", "Bash"}}
	var w *failoverWatch
	msg := captureStderr(t, func() { w, err = failoverWatchFor(in) })
	if w != nil || err != nil || !strings.Contains(msg, "failover is off for this launch: it passes --disallowedTools") {
		t.Fatalf("on from DEFAULTS: %v %v %q", w, err, msg)
	}
	in.flagOn = true
	if _, err := failoverWatchFor(in); err == nil || !strings.Contains(err.Error(), "--disallowedTools") {
		t.Fatalf("--failover: %v", err)
	}
	if err := writeFailoverDefaults(root, failoverDefaults{}); err != nil {
		t.Fatal(err)
	}
	in.claudeArgs = nil
	if _, err := failoverWatchFor(in); err == nil || !strings.Contains(err.Error(), "no account is listed") {
		t.Fatalf("--failover with no list: %v", err)
	}
}

// The check before ending claude runs every guard a launch on the target
// runs: a reference with no helper on PATH refuses the target.
func TestFailoverPreflightRunsLaunchGuards(t *testing.T) {
	root := sandboxDefaultRoot(t)
	writePlaybook(t, root, "alpha", &manifest.Manifest{Launcher: "al"})
	tokenAccount(t, "acc-a", "tok-a")
	putAccount(t, account.Account{Name: "acc-ref", Provider: account.Anthropic, Auth: account.Auth{Kind: account.ClaudeToken, Ref: "stub:ref"}})
	t.Setenv("CPB_SECRET_HELPER", "no-such-helper-anywhere")
	if err := writeFailoverDefaults(root, failoverDefaults{Failover: "on", Accounts: []string{"acc-ref"}}); err != nil {
		t.Fatal(err)
	}
	pb, err := playbook.Find(root, "alpha")
	if err != nil || pb == nil {
		t.Fatal(err)
	}
	w, err := failoverWatchFor(failoverWatchInput{pb: pb, configDir: pb.Path, wd: t.TempDir(), from: "acc-a"})
	if err != nil || w == nil {
		t.Fatalf("watch: %v %v", w, err)
	}
	if err := w.canRunOn("acc-ref", t.TempDir()); err == nil || !strings.Contains(err.Error(), "no-such-helper-anywhere") {
		t.Fatalf("a launch whose helper is missing passed the check: %v", err)
	}
}

// --failover is refused where it cannot act: with --no-failover, on a
// caller-supplied config directory, on a sandboxed launch. --no-failover
// reads nothing, so a setting that cannot be read does not stop it.
func TestFailoverRefusals(t *testing.T) {
	root := sandboxDefaultRoot(t)
	writePlaybook(t, root, "alpha", nil)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, c := range []struct {
		name string
		args []string
		env  string
		want string
	}{
		{"with --no-failover", []string{"--failover", "--no-failover", "alpha"}, "", "pick one"},
		{"on CPB_CONFIG_DIR", []string{"--failover", "alpha"}, t.TempDir(), "not CPB_CONFIG_DIR"},
		{"on --sandbox-host", []string{"--failover", "--sandbox-host", "u@example-host", "alpha"}, "", "a sandboxed launch"},
	} {
		t.Setenv("CPB_CONFIG_DIR", c.env)
		if err := runRun(nil, c.args); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
	t.Setenv("CPB_CONFIG_DIR", "")
	if err := os.MkdirAll(filepath.Dir(failoverDefaultsPath(root)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(failoverDefaultsPath(root), []byte("failover = 'sometimes'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runRun(nil, []string{"alpha"}); err == nil || !strings.Contains(err.Error(), ".failover") {
		t.Fatalf("a setting that cannot be read: %v", err)
	}
	if err := runRun(nil, []string{"--no-failover", "alpha"}); err != nil {
		t.Fatalf("--no-failover with a setting that cannot be read: %v", err)
	}
}

// An always-sandboxed playbook never moves: EXPLAIN says why, and
// --failover on its launch is refused.
func TestFailoverAlwaysSandboxed(t *testing.T) {
	root := sandboxDefaultRoot(t)
	writePlaybook(t, root, "boxed", &manifest.Manifest{Sandbox: &manifest.Sandbox{Always: true}})
	tokenAccount(t, "acc-b", "b")
	if err := writeFailoverDefaults(root, failoverDefaults{Failover: "on", Accounts: []string{"acc-b"}}); err != nil {
		t.Fatal(err)
	}
	pb, err := playbook.Find(root, "boxed")
	if err != nil || pb == nil {
		t.Fatal(err)
	}
	v := explainFailover(pb)
	if line := explainFailoverLine(v); line != "Failover: off (a sandboxed launch's session runs where cpb cannot watch it); with --no-sandbox, on (DEFAULTS); at a usage limit, the session moves to acc-b" {
		t.Fatalf("EXPLAIN: %q", line)
	}
	if v.On || v.NoSandbox == nil || !v.NoSandbox.On || v.NoSandbox.From != "DEFAULTS" {
		t.Fatalf("EXPLAIN --json: %+v %+v", v, v.NoSandbox)
	}
	if err := runRun(nil, []string{"--failover", "boxed"}); !errors.Is(err, errFailoverSandbox) {
		t.Fatalf("--failover on an always-sandboxed playbook: %v", err)
	}
}

// A session file is read only for the pid it is named for.
func TestReadSessionFileOfChecksThePID(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"pid": 9999, "sessionId": failSID, "kind": "interactive", "status": "idle"})
	if err := os.WriteFile(filepath.Join(dir, "sessions", "4242.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := readSessionFileOf(dir, 4242); ok {
		t.Fatal("a file named for 4242 that names 9999 was read")
	}
	if _, ok := readSessionFileOf(dir, 9999); ok {
		t.Fatal("a pid with no file of its own was read")
	}
}

// An install never takes a source's failover.
func TestInstallDropsFailover(t *testing.T) {
	resetCommandTestState(t)
	base := t.TempDir()
	config.PlaybooksDir = filepath.Join(base, "playbooks")
	source := filepath.Join(base, "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "CLAUDE.md"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Write(source, &manifest.Manifest{Name: "pb", Version: "1", Failover: "on"}); err != nil {
		t.Fatal(err)
	}
	var err error
	msg := captureStderr(t, func() { err = doInstall(installOpts{name: "pb", noLauncher: true}, []string{source}) })
	if err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Read(filepath.Join(config.PlaybooksDir, "pb"))
	if err != nil || m == nil || m.Failover != "" || !strings.Contains(msg, "ignoring failover") {
		t.Fatalf("installed: %+v %v %q", m, err, msg)
	}
}

// A resume carries the flags it can carry safely and never a prompt.
func TestResumeClaudeArgs(t *testing.T) {
	sw := &failoverSwitch{From: "a", To: "b", Window: "five_hour", sessionID: sidLive, model: "claude-fable-5-2", resetsAt: time.Now().Add(time.Hour)}
	got := resumeClaudeArgs([]string{"--model", "opus", "--permission-mode", "auto", "--effort=high", "--dangerously-skip-permissions", "the first prompt"}, sw)
	want := []string{"--resume", sidLive, "--model", "claude-fable-5-2", "--permission-mode", "auto", "--effort=high", "--dangerously-skip-permissions"}
	if len(got) != len(want)+1 || strings.Join(got[:len(want)], " ") != strings.Join(want, " ") ||
		!strings.HasPrefix(got[len(got)-1], "[cpb: this session moved from account a (5-hour limit") {
		t.Fatalf("got %q", got)
	}
	args := relaunchArgs([]string{"--failover", "--no-sandbox"}, []launchToken{{flag: "--env-set", value: "tracing"}, {flag: "--account", value: "a"}}, "alpha", nil, sw)
	if strings.Join(args[:6], " ") != "--failover --no-sandbox --env-set=tracing --account b alpha" {
		t.Fatalf("relaunch: %q", args)
	}
	// The way back to the account the session came from sends no prompt.
	back := resumeClaudeArgs(nil, &failoverSwitch{From: "b", To: "a", sessionID: sidLive, noPrompt: true})
	if strings.Join(back, " ") != "--resume "+sidLive {
		t.Fatalf("back: %q", back)
	}
	if !strings.Contains(continuePrompt(&failoverSwitch{From: "a", To: "b", Window: "seven_day", resetsAt: time.Now()}), "its context, of a size cpb could not read") {
		t.Fatal("an unknown size reads as a number")
	}
	// The resume runs from another folder: a settings file's path is made
	// absolute against this one; JSON stays as it is.
	here, _ := os.Getwd()
	got = resumeClaudeArgs([]string{"--settings", "s.json", "--settings={\"a\":1}", "--settings=t.json", "--settings=~/u.json"}, &failoverSwitch{sessionID: sidLive, noPrompt: true})
	want = []string{"--resume", sidLive, "--settings", filepath.Join(here, "s.json"), `--settings={"a":1}`, "--settings=" + filepath.Join(here, "t.json"), "--settings=~/u.json"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("settings: %q", got)
	}
}

// Once the next claude has started, a Ctrl-C is its own again.
func TestFailoverHandoverEndsWhenClaudeStarts(t *testing.T) {
	catchHandover()
	t.Cleanup(releaseHandover)
	if err := runRecorded(exec.Command("true"), nil, nil); err != nil {
		t.Fatal(err)
	}
	if handoverSigs != nil {
		t.Fatal("Ctrl-C still caught while the next claude runs")
	}
}

// The launch flag wins, then the playbook's own setting, then DEFAULTS'.
func TestFailoverConfigPrecedence(t *testing.T) {
	root := sandboxDefaultRoot(t)
	tokenAccount(t, "acc-b", "b")
	on := func(d, pb string, flagOn, flagOff bool) bool {
		t.Helper()
		if err := writeFailoverDefaults(root, failoverDefaults{Failover: d, Accounts: []string{"acc-b (any provider)"}, MaxContext: 9}); err != nil {
			t.Fatal(err)
		}
		cfg, err := failoverConfigFor(root, &manifest.Manifest{Failover: pb}, flagOn, flagOff)
		if err != nil {
			t.Fatal(err)
		}
		// --no-failover reads nothing.
		if !flagOff && (len(cfg.accounts) != 1 || cfg.accounts[0] != (failoverTarget{name: "acc-b", anyProvider: true}) || cfg.maxContext != 9) {
			t.Fatalf("config: %+v", cfg)
		}
		return cfg.on
	}
	cases := []struct {
		d, pb           string
		flagOn, flagOff bool
		want            bool
	}{
		{"", "", false, false, false},
		{"on", "", false, false, true},
		{"on", "inherited", false, false, true},
		{"on", "off", false, false, false},
		{"off", "on", false, false, true},
		{"on", "on", false, true, false},
		{"", "off", true, false, true},
	}
	for _, c := range cases {
		if got := on(c.d, c.pb, c.flagOn, c.flagOff); got != c.want {
			t.Errorf("defaults %q playbook %q flags %v/%v: on=%v, want %v", c.d, c.pb, c.flagOn, c.flagOff, got, c.want)
		}
	}
	if _, err := failoverConfigFor(root, &manifest.Manifest{Failover: "sometimes"}, false, false); err == nil {
		t.Fatal("a manifest failover of another value was accepted")
	}
	// Nothing set removes the file; the file is 0600.
	if err := writeFailoverDefaults(root, failoverDefaults{Failover: "on"}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(failoverDefaultsPath(root)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode: %v %v", fi, err)
	}
	if err := writeFailoverDefaults(root, failoverDefaults{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(failoverDefaultsPath(root)); !os.IsNotExist(err) {
		t.Fatalf("an empty setting left its file: %v", err)
	}
	if err := checkFailoverAccounts(root, []string{"acc-b", "acc-b (any provider)"}, nil); err == nil {
		t.Fatal("an account named twice was accepted")
	}
	if err := checkFailoverAccounts(root, []string{"nope"}, nil); err == nil {
		t.Fatal("an unknown account was accepted")
	}
}

// A session that ran in a folder with a non-ASCII name is found under the
// project directory Claude Code names for it, so a resume from there is
// not refused as one from another folder.
func TestTranscriptHereNonASCIIFolder(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	folder := filepath.Join(base, "çx")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	configDir := t.TempDir()
	// Claude Code's name for <base>/çx: <base>'s, then '-' for '/', '-'
	// for 'ç', then 'x'.
	dir := filepath.Join(configDir, "projects", encodeProjectDir(base)+"--x")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, failSID+".jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !transcriptHere(configDir, folder, failSID) {
		t.Fatal("the session's transcript is not found from its own folder")
	}
}

// A launch with a watcher does not end the catch when its first process
// starts (a secret helper may run first): only the session file does.
func TestFailoverHandoverOutlivesAHelperStart(t *testing.T) {
	sandboxDefaultRoot(t)
	fastFailover(t, 0, time.Minute)
	w := newFailoverWatch(t, "acc-a", "acc-b")
	w.configDir = t.TempDir()
	catchHandover()
	t.Cleanup(releaseHandover)
	if err := runRecorded(exec.Command("sleep", "0.2"), nil, w); err != nil {
		t.Fatal(err)
	}
	handoverMu.Lock()
	caught := handoverSigs != nil
	handoverMu.Unlock()
	if !caught {
		t.Fatal("the catch ended when the launch's first process started")
	}
}

// An account whose latest usage figure is at 100% of a window that has not
// reset is skipped like one a live session shows at its limit.
func TestFailoverSkipsAccountsFullByUsage(t *testing.T) {
	root := sandboxDefaultRoot(t)
	writePlaybook(t, root, "alpha", nil)
	tokenAccount(t, "acc-a", "a")
	tokenAccount(t, "acc-b", "b")
	tokenAccount(t, "acc-c", "c")
	// A named account on the Claude login: its figure is the login's
	// (machine, for a shared playbook), not under its own name.
	putAccount(t, account.Account{Name: "acc-login", Provider: account.Anthropic, Auth: account.Auth{Kind: account.ClaudeLogin}})
	if err := writeFailoverDefaults(root, failoverDefaults{Failover: "on", Accounts: []string{"acc-b", "acc-login", "acc-c"}}); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour).Unix()
	b, _ := json.Marshal(latestFigure{V: usageVersion, Key: "acc-b", Account: "acc-b", SeenAt: rfc3339(time.Now()),
		RateLimits: json.RawMessage(fmt.Sprintf(`{"seven_day":{"used_percentage":100,"resets_at":%d}}`, future))})
	put := func(key string, b []byte) {
		t.Helper()
		p := latestPath(root, key)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	put("acc-b", b)
	m, _ := json.Marshal(latestFigure{V: usageVersion, Key: account.Machine, Account: "acc-login", SeenAt: rfc3339(time.Now()),
		RateLimits: json.RawMessage(fmt.Sprintf(`{"five_hour":{"used_percentage":100,"resets_at":%d}}`, future))})
	put(account.Machine, m)
	pb, err := playbook.Find(root, "alpha")
	if err != nil || pb == nil {
		t.Fatal(err)
	}
	w, err := failoverWatchFor(failoverWatchInput{pb: pb, configDir: pb.Path, wd: t.TempDir(), from: "acc-a"})
	if err != nil || w == nil {
		t.Fatalf("watch: %v %v", w, err)
	}
	if full := w.fullAccounts(); !full["acc-b"] || !full["acc-login"] || full["acc-c"] {
		t.Fatalf("full: %v", full)
	}
	w.canRunOn = nil
	if to, why := w.pick(""); to != "acc-c" {
		t.Fatalf("picked %q: %s", to, why)
	}
}

// Every character of the set the by-hand line and folder notes do not
// print is hidden; zero-width joiners and spaces are shown.
func TestHiddenCharacters(t *testing.T) {
	for _, r := range []rune{0x061C, 0x200E, 0x200F, 0x202A, 0x202E, 0x2066, 0x2069, 0x2028, 0x2029, 0x1b, 0x7f} {
		if !mustHide("a"+string(r)+"b") || !strings.Contains(printable("a"+string(r)+"b"), "?") {
			t.Errorf("U+%04X shown", r)
		}
	}
	for _, r := range []rune{0x200B, 0x200C, 0x200D} {
		if mustHide("a"+string(r)+"b") || printable("a"+string(r)+"b") != "a"+string(r)+"b" {
			t.Errorf("U+%04X hidden", r)
		}
	}
}

// In a marked by-hand line a newline is hidden in an --env name and in an
// inline value too, so the line stays one line.
func TestResumeByHandNewlineInEnvNameAndInlineValue(t *testing.T) {
	resetCommandTestState(t)
	for _, c := range []struct {
		args []string
		flag string
	}{
		{[]string{"--env=K\nX=v", "alpha"}, "'--env=<a value"},
		{[]string{"--env=K=v", "--append-system-prompt=one\ntwo", "alpha"}, "'--append-system-prompt=<a value"},
	} {
		args, flag := c.args, c.flag
		line, filled := resumeByHand("/w", args)
		if !filled || strings.Contains(line, "\n") || !strings.HasPrefix(line, "# ") || !strings.Contains(line, flag) {
			t.Errorf("%q: %q %v", args, line, filled)
		}
	}
}
