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

// The whole switch, on a real process: a stand-in claude stops at a weekly
// limit and waits; the launch ends it and resumes the same session on the
// next listed account, with that account's token, the continue prompt last,
// and says so.
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
	t.Chdir(work)
	dir := t.TempDir()
	const sid = "11111111-2222-4333-8444-555555555555"
	resets := time.Now().Add(2 * time.Hour).Unix()
	script := `#!/bin/sh
for a in "$@"; do [ "$a" = --resume ] && resumed=1; done
if [ -n "$resumed" ]; then
  printf '%s\n' "$@" > "$FAKE_OUT.args"; printf '%s' "$CLAUDE_CODE_OAUTH_TOKEN" > "$FAKE_OUT.token"; exit 0
fi
sf="$CLAUDE_CONFIG_DIR/sessions/$$.json"; mkdir -p "$CLAUDE_CONFIG_DIR/sessions"
enc=$(printf %s "$(pwd -P)" | sed 's/[^A-Za-z0-9]/-/g'); mkdir -p "$CLAUDE_CONFIG_DIR/projects/$enc"
printf '{"type":"assistant","message":{"model":"claude-opus-5-5","usage":{"input_tokens":10,"cache_read_input_tokens":300000,"cache_creation_input_tokens":2000}}}\n{"type":"assistant","isApiErrorMessage":true,"error":"rate_limit","message":{"model":"<synthetic>"},"quotaLimits":{"status":"rejected","rateLimitType":"seven_day","resetsAt":%s}}\n' "$FAKE_RESETS" > "$CLAUDE_CONFIG_DIR/projects/$enc/$FAKE_SID.jsonl"
printf '{"pid":%s,"sessionId":"%s","cwd":"%s","startedAt":1,"kind":"interactive","status":"idle"}' $$ "$FAKE_SID" "$(pwd -P)" > "$sf"
trap 'rm -f "$sf"; exit 143' TERM
while :; do sleep 0.05; done
`
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out := filepath.Join(dir, "out")
	t.Setenv("FAKE_OUT", out)
	t.Setenv("FAKE_SID", sid)
	t.Setenv("FAKE_RESETS", strconv.FormatInt(resets, 10))

	var err error
	msg := captureStderr(t, func() {
		err = runRun(nil, []string{"--account", "acc-a", "alpha", "--permission-mode", "auto", "first prompt"})
	})
	if err != nil {
		t.Fatalf("%v\n%s", err, msg)
	}
	argsB, rerr := os.ReadFile(out + ".args")
	if rerr != nil {
		t.Fatalf("no resume ran: %v\n%s", rerr, msg)
	}
	args := strings.Split(strings.TrimRight(string(argsB), "\n"), "\n")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--resume "+sid) || !strings.Contains(joined, "--model claude-opus-5-5") || !strings.Contains(joined, "--permission-mode auto") || strings.Contains(joined, "first prompt") {
		t.Fatalf("resume args: %q", args)
	}
	last := args[len(args)-1]
	if !strings.HasPrefix(last, "[cpb: this session moved from account acc-a (weekly limit, resets ") || !strings.Contains(last, ") to acc-b; its context, about 302,010 tokens") ||
		!strings.Contains(last, "Continue the task you were working on when the usage limit was reached") {
		// The prompt's lines come back one per line: rejoin before judging.
		if !strings.Contains(joined, "[cpb: this session moved from account acc-a (weekly limit") || !strings.Contains(joined, "to acc-b; its context, about 302,010 tokens") {
			t.Fatalf("continue prompt: %q", joined)
		}
	}
	if tok, _ := os.ReadFile(out + ".token"); string(tok) != "tok-b" {
		t.Fatalf("resumed on token %q, want acc-b's", tok)
	}
	if !strings.Contains(msg, "cpb: account acc-a hit its weekly limit") || !strings.Contains(msg, "resuming this session on acc-b") ||
		!strings.Contains(msg, "cpb: this session moved from acc-a to acc-b at ") {
		t.Fatalf("stderr:\n%s", msg)
	}
}

// watchFixture is a config dir with one idle session at a weekly limit.
func watchFixture(t *testing.T, resets time.Time, status string, contextTokens int) (configDir string, pid int) {
	t.Helper()
	configDir = t.TempDir()
	work := t.TempDir()
	pid = 4242
	writeSessionFile(t, configDir, pid, sidLive, work, "interactive", liveStart, time.Now())
	b, _ := os.ReadFile(filepath.Join(configDir, "sessions", "4242.json"))
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	m["status"] = status
	b, _ = json.Marshal(m)
	if err := os.WriteFile(filepath.Join(configDir, "sessions", "4242.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	usage := fmt.Sprintf(`{"type":"assistant","message":{"model":"claude-opus-5-5","usage":{"input_tokens":%d}}}`, contextTokens)
	writeLimitTranscript(t, configDir, work, sidLive, usage, limitLine(fmt.Sprintf(`{"status":"rejected","rateLimitType":"seven_day","resetsAt":%d}`, resets.Unix())))
	return configDir, pid
}

func TestFailoverCheck(t *testing.T) {
	sandboxDefaultRoot(t)
	tokenAccount(t, "acc-a", "a")
	tokenAccount(t, "acc-b", "b")
	tokenAccount(t, "acc-c", "c")
	putAccount(t, account.Account{Name: "router", Provider: account.Gateway, BaseURL: "http://localhost:4000/v1", Auth: account.Auth{Kind: account.None}})
	far := time.Now().Add(3 * time.Hour)
	newWatch := func(targets ...string) *failoverWatch {
		var ts []failoverTarget
		for _, s := range targets {
			tg, err := parseFailoverTarget(s)
			if err != nil {
				t.Fatal(err)
			}
			ts = append(ts, tg)
		}
		return &failoverWatch{cfg: failoverConfig{on: true, accounts: ts}, from: "acc-a", fromClass: "claude",
			episode: &failoverEpisode{tried: map[string]bool{}}}
	}

	t.Run("switches to the first account it can use", func(t *testing.T) {
		fastFailover(t, 0, 30*time.Minute)
		dir, pid := watchFixture(t, far, "idle", 1000)
		w := newWatch("acc-a", "router", "acc-b", "acc-c")
		w.configDir = dir
		w.fullAccounts = func() map[string]bool { return map[string]bool{"acc-b": true} }
		sw := w.check(pid)
		if sw == nil || sw.To != "acc-c" || sw.From != "acc-a" || sw.sessionID != sidLive || sw.model != "claude-opus-5-5" || sw.Context != 1000 {
			t.Fatalf("switch: %+v notes %v", sw, w.episode.notes)
		}
		// The same limit is decided once.
		if again := w.check(pid); again != nil {
			t.Fatalf("decided twice: %+v", again)
		}
	})
	t.Run("another kind only when marked", func(t *testing.T) {
		fastFailover(t, 0, 30*time.Minute)
		dir, pid := watchFixture(t, far, "idle", 1000)
		w := newWatch("router (any provider)")
		w.configDir = dir
		if sw := w.check(pid); sw == nil || sw.To != "router" {
			t.Fatalf("marked entry: %+v %v", sw, w.episode.notes)
		}
	})
	t.Run("nothing while not idle, too soon, or not quiet", func(t *testing.T) {
		fastFailover(t, 0, 30*time.Minute)
		for _, c := range []struct {
			name   string
			resets time.Time
			status string
			quiet  time.Duration
		}{
			{"busy", far, "busy", 0},
			{"a background shell", far, "shell", 0},
			{"resets in 10 minutes", time.Now().Add(10 * time.Minute), "idle", 0},
			{"written just now", far, "idle", time.Hour},
		} {
			failoverQuiet = c.quiet
			dir, pid := watchFixture(t, c.resets, c.status, 1000)
			w := newWatch("acc-b")
			w.configDir = dir
			if sw := w.check(pid); sw != nil {
				t.Errorf("%s: switched to %s", c.name, sw.To)
			}
		}
	})
	t.Run("refusals and caps say why, once", func(t *testing.T) {
		fastFailover(t, 0, 30*time.Minute)
		dir, pid := watchFixture(t, far, "idle", 500000)
		w := newWatch("acc-b", "router")
		w.configDir = dir
		w.cfg.maxContext = 400000
		if sw := w.check(pid); sw != nil || len(w.episode.notes) != 1 || !strings.Contains(w.episode.notes[0], "500,000 tokens, is above failover_max_context (400,000)") {
			t.Fatalf("cap: %+v %v", sw, w.episode.notes)
		}
		dir, pid = watchFixture(t, far.Add(time.Hour), "idle", 1000)
		w = newWatch("acc-b", "router")
		w.configDir = dir
		w.canRunOn = func(name string) error { return fmt.Errorf("playbook alpha may not run on %s\nmore", name) }
		if sw := w.check(pid); sw != nil || !strings.Contains(strings.Join(w.episode.notes, "\n"), "acc-b: playbook alpha may not run on acc-b; router is another kind of account") {
			t.Fatalf("none left: %+v %v", sw, w.episode.notes)
		}
		dir, pid = watchFixture(t, far.Add(2*time.Hour), "idle", 1000)
		w = newWatch("acc-b")
		w.configDir = dir
		now := time.Now().UTC().Format(time.RFC3339)
		w.episode.switches = []*failoverSwitch{{At: now}, {At: now}, {At: now}}
		if sw := w.check(pid); sw != nil || !strings.Contains(strings.Join(w.episode.notes, "\n"), "3 switches in the last 5 hours") {
			t.Fatalf("switch cap: %+v %v", sw, w.episode.notes)
		}
	})
	t.Run("each account once per episode", func(t *testing.T) {
		fastFailover(t, 0, 30*time.Minute)
		w := newWatch("acc-b", "acc-c")
		w.episode.tried["acc-b"] = true
		if to, _ := w.pick(); to != "acc-c" {
			t.Fatalf("picked %q", to)
		}
		if to, why := w.pick(); to != "" || !strings.Contains(why, "every listed account was tried") {
			t.Fatalf("picked %q: %s", to, why)
		}
	})
}

// A resume carries the flags it can carry safely and never a prompt.
func TestResumeClaudeArgs(t *testing.T) {
	sw := &failoverSwitch{From: "a", To: "b", Window: "five_hour", sessionID: sidLive, model: "claude-fable-5-2", resetsAt: time.Now().Add(time.Hour)}
	got := resumeClaudeArgs([]string{"--model", "opus", "--permission-mode", "auto", "--allowedTools", "Bash", "Read",
		"--effort=high", "--dangerously-skip-permissions", "--unknown", "x", "the first prompt"}, sw)
	want := []string{"--resume", sidLive, "--model", "claude-fable-5-2", "--permission-mode", "auto", "--effort=high", "--dangerously-skip-permissions"}
	if len(got) != len(want)+1 || strings.Join(got[:len(want)], " ") != strings.Join(want, " ") || !strings.HasPrefix(got[len(got)-1], "[cpb: this session moved from account a (5-hour limit") {
		t.Fatalf("got %q", got)
	}
	args := relaunchArgs([]launchToken{{flag: "--env-set", value: "tracing"}, {flag: "--account", value: "a"}}, "alpha", nil, sw)
	if strings.Join(args[:4], " ") != "--env-set=tracing --account b alpha" {
		t.Fatalf("relaunch: %q", args)
	}
}

// The launch flag wins, then the playbook's own setting, then DEFAULTS'.
func TestFailoverConfigPrecedence(t *testing.T) {
	root := sandboxDefaultRoot(t)
	tokenAccount(t, "acc-b", "b")
	on := func(d string, pb string, flagOn, flagOff bool) bool {
		t.Helper()
		if err := writeFailoverDefaults(root, failoverDefaults{Failover: d, Accounts: []string{"acc-b (any provider)"}, MaxContext: 9}); err != nil {
			t.Fatal(err)
		}
		cfg, err := failoverConfigFor(root, &manifest.Manifest{Failover: pb}, flagOn, flagOff)
		if err != nil {
			t.Fatal(err)
		}
		if len(cfg.accounts) != 1 || cfg.accounts[0] != (failoverTarget{name: "acc-b", anyProvider: true}) || cfg.maxContext != 9 {
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
