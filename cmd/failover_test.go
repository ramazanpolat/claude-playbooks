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

// standIn is a stand-in claude. A first launch, and a resumed one whose
// token is not in FAKE_OK_TOKENS, does what Claude Code does at a weekly
// limit: it appends the limit line (timestamped, with the folder the
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
if [ -n "$resumed" ]; then case " $FAKE_OK_TOKENS " in *" $tok "*) ok=1;; esac; fi
if [ -n "$ok" ]; then
  { printf '%s|%s|' "$tok" "$(pwd -P)"; for a in "$@"; do printf '%s\037' "$a"; done; printf '\036'; } >> "$FAKE_OUT.log"
  cp "$FAKE_ROOT/.state/launches/$$.json" "$FAKE_OUT.record" 2>/dev/null
  exit 0
fi
dir="${FAKE_SESSION_DIR:-$(pwd -P)}"
enc=$(printf %s "$dir" | sed 's/[^A-Za-z0-9]/-/g')
mkdir -p "$CLAUDE_CONFIG_DIR/projects/$enc" "$CLAUDE_CONFIG_DIR/sessions"
if [ -r "/proc/$$/stat" ]; then st=$(sed 's/.*) //' "/proc/$$/stat" | awk '{print $20}'); else st=$(TZ=UTC LC_ALL=C ps -o lstart= -p $$ | awk '{$1=$1; print}'); fi
sleep 1
ts=$(date -u +%Y-%m-%dT%H:%M:%S.000Z)
printf '{"type":"assistant","cwd":"%s","timestamp":"%s","message":{"model":"claude-opus-5-5","usage":{"input_tokens":10,"cache_read_input_tokens":300000,"cache_creation_input_tokens":2000}}}\n{"type":"assistant","cwd":"%s","timestamp":"%s","isApiErrorMessage":true,"error":"rate_limit","message":{"model":"<synthetic>"},"quotaLimits":{"status":"rejected","rateLimitType":"seven_day","resetsAt":%s}}\n' "$dir" "$ts" "$dir" "$ts" "$FAKE_RESETS" >> "$CLAUDE_CONFIG_DIR/projects/$enc/$FAKE_SID.jsonl"
sf="$CLAUDE_CONFIG_DIR/sessions/$$.json"
printf '{"pid":%s,"sessionId":"%s","cwd":"%s","startedAt":1,"procStart":"%s","kind":"interactive","status":"idle"}' $$ "$FAKE_SID" "$(pwd -P)" "$st" > "$sf"
trap 'rm -f "$sf"; exit 143' TERM
n=0; while [ $n -lt 200 ]; do sleep 0.1; n=$((n+1)); done
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
	far := time.Now().Add(3 * time.Hour)
	now := time.Now()

	t.Run("the first account it can use, from the session's folder", func(t *testing.T) {
		fastFailover(t, 0, 30*time.Minute)
		dir, _, folder := writeWatched(t, failSID, "interactive", "idle", 4242, "x", far, now, "seven_day", 1000)
		w := newFailoverWatch(t, "acc-a", "acc-a", "router", "acc-b", "acc-c")
		w.configDir = dir
		w.fullAccounts = func() map[string]bool { return map[string]bool{"acc-b": true} }
		var folders []string
		w.canRunOn = func(name, f string) error { folders = append(folders, f); return nil }
		c := w.check(4242)
		if c == nil || c.sw.To != "acc-c" || c.sw.From != "acc-a" || c.sw.sessionID != failSID || c.sw.model != "claude-opus-5-5" ||
			c.sw.Context != 1000 || c.sw.folder != folder || strings.Join(folders, ",") != folder || c.procStart != "x" {
			t.Fatalf("candidate: %+v notes %v folders %v", c, w.episode.notes, folders)
		}
		// A check marks nothing: until a switch is made, the next poll
		// decides again.
		if again := w.check(4242); again == nil || len(w.episode.tried) != 0 || !w.handled.IsZero() {
			t.Fatalf("a check that made no switch was kept: %+v %v %v", again, w.episode.tried, w.handled)
		}
	})
	t.Run("the 5-hour window too", func(t *testing.T) {
		fastFailover(t, 0, 30*time.Minute)
		dir, _, _ := writeWatched(t, failSID, "interactive", "idle", 4242, "x", far, now, "five_hour", 1000)
		w := newFailoverWatch(t, "acc-a", "acc-b")
		w.configDir = dir
		if c := w.check(4242); c == nil || c.sw.Window != "five_hour" {
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
			dir, _, _ := writeWatched(t, c.sid, c.kind, c.status, 4242, "x", c.resets, c.at, "seven_day", 1000)
			w := newFailoverWatch(t, "acc-a", "acc-b")
			w.configDir = dir
			if got := w.check(4242); got != nil {
				t.Errorf("%s: a switch to %s", c.name, got.sw.To)
			}
		}
	})
	t.Run("kinds by address; no model across kinds", func(t *testing.T) {
		fastFailover(t, 0, 30*time.Minute)
		dir, _, _ := writeWatched(t, failSID, "interactive", "idle", 4242, "x", far, now, "seven_day", 1000)
		w := newFailoverWatch(t, "router", "router2")
		w.configDir = dir
		if c := w.check(4242); c != nil || !strings.Contains(notes(w), "router2 is another kind of account") {
			t.Fatalf("two gateways at two addresses: %+v %v", c, w.episode.notes)
		}
		w = newFailoverWatch(t, "acc-a", "router (any provider)")
		w.configDir = dir
		if c := w.check(4242); c == nil || c.sw.To != "router" || c.sw.model != "" {
			t.Fatalf("a marked entry of another kind: %+v", c)
		}
	})
	t.Run("a model id of another shape is not passed on", func(t *testing.T) {
		fastFailover(t, 0, 30*time.Minute)
		dir, transcript, _ := writeWatched(t, failSID, "interactive", "idle", 4242, "x", far, now, "seven_day", 1000)
		b, _ := os.ReadFile(transcript)
		if err := os.WriteFile(transcript, []byte(strings.Replace(string(b), `"model":"claude-opus-5-5"`, `"model":"--dangerously-skip-permissions"`, 1)), 0o600); err != nil {
			t.Fatal(err)
		}
		w := newFailoverWatch(t, "acc-a", "acc-b")
		w.configDir = dir
		if c := w.check(4242); c == nil || c.sw.model != "" {
			t.Fatalf("model: %+v", c)
		}
	})
	t.Run("refusals and caps say why, once", func(t *testing.T) {
		fastFailover(t, 0, 30*time.Minute)
		dir, _, _ := writeWatched(t, failSID, "interactive", "idle", 4242, "x", far, now, "seven_day", 500000)
		w := newFailoverWatch(t, "acc-a", "acc-b", "router")
		w.configDir = dir
		w.cfg.maxContext = 400000
		if c := w.check(4242); c != nil || len(w.episode.notes) != 1 || !strings.Contains(w.episode.notes[0], "500,000 tokens, is above failover_max_context (400,000)") {
			t.Fatalf("cap: %+v %v", c, w.episode.notes)
		}
		if again := w.check(4242); again != nil || len(w.episode.notes) != 1 {
			t.Fatalf("decided twice: %v", w.episode.notes)
		}
		// Under a cap, a size the tail does not hold is no switch either.
		dir, _, _ = writeWatched(t, failSID, "interactive", "idle", 4242, "x", far, now, "seven_day", 0)
		w = newFailoverWatch(t, "acc-a", "acc-b")
		w.configDir, w.cfg.maxContext = dir, 400000
		if c := w.check(4242); c != nil || !strings.Contains(notes(w), "the context's size is not in the transcript") {
			t.Fatalf("unknown size under a cap: %+v %v", c, w.episode.notes)
		}
		dir, _, _ = writeWatched(t, failSID, "interactive", "idle", 4242, "x", far, now, "seven_day", 1000)
		w = newFailoverWatch(t, "acc-a", "acc-b", "router")
		w.configDir = dir
		calls := 0
		w.canRunOn = func(name, folder string) error {
			calls++
			return fmt.Errorf("playbook alpha may not run on %s\nmore", name)
		}
		if c := w.check(4242); c != nil || !strings.Contains(notes(w), "acc-b: playbook alpha may not run on acc-b; router is another kind of account") {
			t.Fatalf("none left: %+v %v", c, w.episode.notes)
		}
		// A limit it declined is not weighed again on the next poll.
		if c := w.check(4242); c != nil || calls != 1 {
			t.Fatalf("weighed again: %+v, %d checks of the target", c, calls)
		}
		w = newFailoverWatch(t, "acc-a", "acc-b")
		w.configDir = dir
		at := time.Now().UTC().Format(time.RFC3339)
		w.episode.switches = []*failoverSwitch{{At: at}, {At: at}, {At: at}}
		if c := w.check(4242); c != nil || !strings.Contains(notes(w), "3 switches in the last 5 hours") {
			t.Fatalf("switch cap: %+v %v", c, w.episode.notes)
		}
		// A resume that would be refused is refused before anything ends:
		// here the folder the session ran in is gone.
		dir, _, folder := writeWatched(t, failSID, "interactive", "idle", 4242, "x", far, now, "seven_day", 1000)
		if err := os.Remove(folder); err != nil {
			t.Fatal(err)
		}
		w = newFailoverWatch(t, "acc-a", "acc-b")
		w.configDir = dir
		if c := w.check(4242); c != nil || !strings.Contains(notes(w), "is not there") {
			t.Fatalf("folder gone: %+v %v", c, w.episode.notes)
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
	w, c, _ := setup("idle", started)
	if !w.act(c, pid, child.Process) {
		t.Fatal("did not act on its own idle session")
	}
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
	if line := explainFailoverLine(explainFailover(pb)); line != "Failover: off (a sandboxed launch's session runs where cpb cannot watch it)" {
		t.Fatalf("EXPLAIN: %q", line)
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
