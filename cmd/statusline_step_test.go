package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ramazanpolat/claude-playbooks/internal/account"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/settings"
	"github.com/ramazanpolat/claude-playbooks/internal/shell"
)

const stepSID = "d9c690fc-1a83-4be9-b6aa-000000000001"

// statusInput is a status line input shaped like Claude Code 2.1.322's.
func statusInput(sid string, five, seven float64, cost float64) []byte {
	return []byte(fmt.Sprintf(`{"session_id":%q,"session_name":"work","version":"2.1.322","model":{"id":"claude-opus-5-5","display_name":"Opus"},`+
		`"workspace":{"current_dir":"/home/user/app","project_dir":"/home/user/app","added_dirs":[]},"effort":{"level":"high"},"thinking":{"enabled":true},"fast_mode":false,`+
		`"cost":{"total_cost_usd":%g,"total_duration_ms":10},"context_window":{"context_window_size":1000000,"total_input_tokens":34055,"used_percentage":3},`+
		`"prompt_cache":{"requests":1,"misses":0,"hit_ratio":0.7,"last_miss_cause":null},`+
		`"rate_limits":{"five_hour":{"used_percentage":%g,"resets_at":%d},"seven_day":{"used_percentage":%g,"resets_at":%d}}}`+"\n",
		sid, cost, five, time.Now().Add(time.Hour).Unix(), seven, time.Now().Add(72*time.Hour).Unix()))
}

// stepFixture is a playbooks root with one launch record and its id.
func stepFixture(t *testing.T, acct, key string, _ *string) (root string, rec launchRecord) {
	t.Helper()
	root = sandboxDefaultRoot(t)
	rec = launchRecord{V: launchRecordVersion, LaunchID: "0123456789abcdef", PID: os.Getpid(), ProcStart: "x", Playbook: "alpha",
		ConfigDir: filepath.Join(root, "alpha"), Account: acct, Key: key, LaunchedAt: rfc3339(time.Now())}
	dir := launchRecordsDir(root)
	if err := writeLaunchRecord(dir, filepath.Join(dir, rec.LaunchID+".json"), &rec); err != nil {
		t.Fatal(err)
	}
	return root, rec
}

// The cpb key is added last and every other byte stays; input that is not
// one object, or has a cpb key, passes through as it came.
func TestWithCpbKey(t *testing.T) {
	block := []byte(`{"v":1}`)
	for in, want := range map[string]string{
		`{"a":1}` + "\n":   `{"a":1,"cpb":{"v":1}}` + "\n",
		` { "a" : [1,2] }`: ` { "a" : [1,2] ,"cpb":{"v":1}}`,
		`{}`:               `{"cpb":{"v":1}}`,
		`[1,2]`:            `[1,2]`,
		`{"a":1`:           `{"a":1`,
		`{"cpb":2}`:        `{"cpb":2}`,
		``:                 ``,
		`"text"`:           `"text"`,
	} {
		if got := string(withCpbKey([]byte(in), block)); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

// One render: a snapshot line under the month of the session, the latest
// figure of its key, and the block, names only, its own entry first.
func TestStepRecordsAndAddsTheBlock(t *testing.T) {
	id := "someone@example.com"
	root, rec := stepFixture(t, "acc-a", "acc-a", &id)
	putAccount(t, account.Account{Name: "acc-a", Provider: account.Anthropic, Identity: id, Auth: account.Auth{Kind: account.ClaudeToken, Value: "t"}})
	putAccount(t, account.Account{Name: "acc-b", Provider: account.Anthropic, Auth: account.Auth{Kind: account.ClaudeToken, Value: "t"}})
	putAccount(t, account.Account{Name: "router", Provider: account.Gateway, BaseURL: "http://localhost:4000/v1", Auth: account.Auth{Kind: account.None}})
	in := statusInput(stepSID, 26, 5, 0.5)
	out := stepRecord(stepArgs{root: root, launch: rec.LaunchID, original: "true"}, in)

	if !bytes.HasPrefix(out, bytes.TrimRight(in, "\n}")) || bytes.Contains(out, []byte("example.com")) {
		t.Fatalf("the block changed the input, or carries an identity:\n%s", out)
	}
	var top struct {
		Cpb statuslineBlockJSON `json:"cpb"`
	}
	if err := json.Unmarshal(out, &top); err != nil {
		t.Fatal(err)
	}
	b := top.Cpb
	if b.V != 1 || b.Account == nil || b.Account.Name != "acc-a" || len(b.Usage) != 3 || !b.Usage[0].Self || b.Usage[0].Account != "acc-a" ||
		b.Usage[0].State != "reported" || b.Usage[0].FiveHour == nil || b.Usage[0].FiveHour.UsedPercentage != 26 || b.Usage[0].SeenAt == nil ||
		b.Usage[1].Account != "acc-b" || b.Usage[1].State != "not_seen" || b.Usage[1].FiveHour != nil ||
		b.Usage[2].Account != "router" || b.Usage[2].State != "not_reported" {
		t.Fatalf("block: %+v", b)
	}
	// The log: one self-describing line, under this month.
	path := filepath.Join(usageDir(root), "sessions", time.Now().UTC().Format("2006-01"), stepSID+".jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var s usageSnapshot
	// No identity in the line, or in the account's latest figure: SHOW
	// USAGE reads it from the account when a statement asks.
	if strings.Contains(string(data), id) || strings.Contains(string(data), `"identity"`) {
		t.Fatalf("the line holds an identity: %s", data)
	}
	if lb, _ := os.ReadFile(latestPath(root, "acc-a")); len(lb) == 0 || strings.Contains(string(lb), id) || strings.Contains(string(lb), `"identity"`) {
		t.Fatalf("the latest figure holds an identity, or is missing: %s", lb)
	}
	if err := json.Unmarshal(bytes.TrimSpace(data), &s); err != nil || s.Account != "acc-a" || s.Key != "acc-a" ||
		s.ConfigDir != rec.ConfigDir || s.Playbook != "alpha" || s.Model == nil || *s.Model != "claude-opus-5-5" || len(s.RateLimits) == 0 {
		t.Fatalf("line: %v %s", err, data)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("log mode %v", fi.Mode())
	}
	// An unchanged render writes nothing; a new figure writes a line; cost
	// alone within the minute does not.
	stepRecord(stepArgs{root: root, launch: rec.LaunchID, original: "true"}, in)
	stepRecord(stepArgs{root: root, launch: rec.LaunchID, original: "true"}, statusInput(stepSID, 26, 5, 0.9))
	stepRecord(stepArgs{root: root, launch: rec.LaunchID, original: "true"}, statusInput(stepSID, 27, 5, 0.9))
	data, _ = os.ReadFile(path)
	if n := bytes.Count(data, []byte("\n")); n != 2 {
		t.Fatalf("%d lines, want 2:\n%s", n, data)
	}
}

// Nothing is recorded for an input with no Claude Code session id, an
// unknown launch, or no rate_limits beyond its own line; nothing breaks.
func TestStepRecordsNothingItCannotTrust(t *testing.T) {
	root, rec := stepFixture(t, "acc-a", "acc-a", nil)
	for name, in := range map[string][]byte{
		"a session id of another shape": statusInput("../../etc", 1, 1, 0),
		"malformed":                     []byte(`{"session_id":`),
		"not an object":                 []byte(`[1]`),
	} {
		out := stepRecord(stepArgs{root: root, launch: rec.LaunchID, original: "true"}, in)
		if name != "a session id of another shape" && !bytes.Equal(out, in) {
			t.Errorf("%s: input changed: %q", name, out)
		}
	}
	if _, err := os.Stat(filepath.Join(usageDir(root), "sessions")); !os.IsNotExist(err) {
		t.Fatalf("something was recorded: %v", err)
	}
	in := statusInput(stepSID, 1, 1, 0)
	if out := stepRecord(stepArgs{root: root, launch: "ffffffffffffffff", original: "true"}, in); !bytes.Equal(out, in) {
		t.Fatal("an unknown launch changed the input")
	}
	noLimits := bytes.Replace(in, []byte(`,"rate_limits"`), []byte(`,"x"`), 1)
	stepRecord(stepArgs{root: root, launch: rec.LaunchID, original: "true"}, noLimits)
	if l := readLatestFigures(root); len(l) != 0 {
		t.Fatalf("a latest figure from an input with no rate_limits: %+v", l)
	}
}

// Two sessions of one account and two renders of one session, at once:
// every log stays whole, one line each, and the latest figure is whole.
func TestStepConcurrentWriters(t *testing.T) {
	root, rec := stepFixture(t, "acc-a", "acc-a", nil)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sid := fmt.Sprintf("d9c690fc-1a83-4be9-b6aa-%012d", i%4)
			stepRecord(stepArgs{root: root, launch: rec.LaunchID, original: "true"}, statusInput(sid, float64(10+i), 5, 0))
		}(i)
	}
	wg.Wait()
	files, _ := filepath.Glob(filepath.Join(usageDir(root), "sessions", "*", "*.jsonl"))
	if len(files) != 4 {
		t.Fatalf("%d logs, want 4", len(files))
	}
	for _, f := range files {
		b, _ := os.ReadFile(f)
		for _, line := range bytes.Split(bytes.TrimSpace(b), []byte("\n")) {
			if !json.Valid(line) {
				t.Fatalf("%s: a line is not whole: %q", f, line)
			}
		}
	}
	if l := readLatestFigures(root); len(l) != 1 || l["acc-a"].SessionID == "" {
		t.Fatalf("latest: %+v", l)
	}
}

// A full log is rotated; logs and figures nobody wrote for 30 days go.
func TestUsageRotationAndSweep(t *testing.T) {
	root, rec := stepFixture(t, "acc-a", "acc-a", nil)
	stepRecord(stepArgs{root: root, launch: rec.LaunchID, original: "true"}, statusInput(stepSID, 1, 1, 0))
	path := sessionLogPath(root, stepSID, time.Now())
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	pad := bytes.Repeat([]byte(`{"v":0}`+"\n"), usageRotateBytes/8+1)
	_, _ = f.Write(pad)
	f.Close()
	stepRecord(stepArgs{root: root, launch: rec.LaunchID, original: "true"}, statusInput(stepSID, 2, 1, 0))
	if _, err := os.Stat(strings.TrimSuffix(path, ".jsonl") + ".1.jsonl"); err != nil {
		t.Fatalf("not rotated: %v", err)
	}
	if b, _ := os.ReadFile(path); bytes.Count(b, []byte("\n")) != 1 {
		t.Fatalf("the new log: %q", b)
	}
	old := time.Now().Add(-31 * 24 * time.Hour)
	for _, p := range []string{path, strings.TrimSuffix(path, ".jsonl") + ".1.jsonl", latestPath(root, "acc-a")} {
		_ = os.Chtimes(p, old, old)
	}
	sweepUsage(root, time.Now())
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("an old month is still there: %v", err)
	}
	if len(readLatestFigures(root)) != 0 {
		t.Fatal("an old figure is still there")
	}
}

// The real command a launch writes, run as Claude Code runs it: the
// original gets the whole input plus the cpb key, however large the input
// and however slowly it reads, and its output and exit code are its own.
func TestStepCommandRunsTheOriginal(t *testing.T) {
	root, rec := stepFixture(t, "acc-a", "acc-a", nil)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	got := filepath.Join(dir, "got")
	original := "sleep 0.2; cat > '" + got + "'; echo shown; exit 3"
	run := func(in []byte) (string, int) {
		t.Helper()
		c := exec.Command("/bin/sh", "-c", stepCommand(self, root, rec.LaunchID, original))
		c.Stdin = bytes.NewReader(in)
		out, err := c.Output()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		return string(out), code
	}
	in := statusInput(stepSID, 30, 5, 0)
	if out, code := run(in); out != "shown\n" || code != 3 {
		t.Fatalf("output %q, exit %d", out, code)
	}
	b, _ := os.ReadFile(got)
	if !bytes.HasPrefix(b, bytes.TrimRight(in, "\n}")) || !bytes.Contains(b, []byte(`"cpb":{"v":1,`)) {
		t.Fatalf("the original read %q", b)
	}
	// Over the pipe's size, and not an object: whole and unchanged.
	big := append([]byte("["), bytes.Repeat([]byte(`"x",`), 40000)...)
	big = append(big, []byte(`"x"]`)...)
	if out, code := run(big); out != "shown\n" || code != 3 {
		t.Fatalf("big input: %q %d", out, code)
	}
	if b, _ := os.ReadFile(got); !bytes.Equal(b, big) {
		t.Fatalf("the original read %d bytes of %d", len(b), len(big))
	}
}

// Only the playbook's own status line is wrapped; a folder's, managed
// settings', or a launch's own --settings leave the launch as it is, and
// a folder's command is never copied.
func TestStatuslineStepFor(t *testing.T) {
	root := sandboxDefaultRoot(t)
	writePlaybook(t, root, "alpha", &manifest.Manifest{Launcher: "al"})
	cfg := filepath.Join(root, "alpha")
	old := managedSettingsPaths
	managedSettingsPaths = func() []string { return nil }
	t.Cleanup(func() { managedSettingsPaths = old })
	work, _ := filepath.EvalSymlinks(t.TempDir())
	write := func(p, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if s := statuslineStepFor(root, cfg, work, "0123456789abcdef", nil); s.wrapped != nil || s.reason != stepSkipNone {
		t.Fatalf("no status line: %+v", s)
	}
	write(filepath.Join(cfg, "settings.json"), `{"statusLine":{"type":"command","command":"my-line --x 'y'","padding":2,"refreshInterval":5}}`)
	s := statuslineStepFor(root, cfg, work, "0123456789abcdef", nil)
	if s.wrapped == nil {
		t.Fatalf("not wrapped: %s", s.reason)
	}
	var cmd string
	var pad, refresh int
	_, _ = s.wrapped.Get("command", &cmd)
	_, _ = s.wrapped.Get("padding", &pad)
	_, _ = s.wrapped.Get("refreshInterval", &refresh)
	if !strings.Contains(cmd, " "+statuslineStepArg+" --root ") || !strings.Contains(cmd, " --launch 0123456789abcdef -- ") ||
		!strings.HasSuffix(cmd, `'my-line --x '\''y'\'''`) || pad != 2 || refresh != 5 {
		t.Fatalf("wrapped: %q pad %d refresh %d", cmd, pad, refresh)
	}
	if s := statuslineStepFor(root, cfg, work, "0123456789abcdef", []string{"--settings", "{}"}); s.wrapped != nil || s.reason != stepSkipSettingsArg {
		t.Fatalf("own --settings: %+v", s)
	}
	// A worktree whose main checkout's local settings set a status line.
	main := t.TempDir()
	write(filepath.Join(main, ".claude", "settings.local.json"), `{"statusLine":{"type":"command","command":"folder-line"}}`)
	wt := filepath.Join(work, "wt")
	write(filepath.Join(wt, ".git"), "gitdir: "+filepath.Join(main, ".git", "worktrees", "wt")+"\n")
	if s := statuslineStepFor(root, cfg, wt, "0123456789abcdef", nil); s.wrapped != nil || !strings.Contains(s.reason, "settings.local.json sets a status line") {
		t.Fatalf("worktree's main checkout: %+v", s)
	}
	write(filepath.Join(work, ".claude", "settings.json"), `{"statusLine":{"type":"command","command":"folder-line"}}`)
	if s := statuslineStepFor(root, cfg, work, "0123456789abcdef", nil); s.wrapped != nil || !strings.Contains(s.reason, "sets a status line of the folder's") {
		t.Fatalf("folder's: %+v", s)
	}
	write(filepath.Join(work, ".claude", "settings.json"), `{not json`)
	if s := statuslineStepFor(root, cfg, work, "0123456789abcdef", nil); s.wrapped != nil || !strings.Contains(s.reason, "cannot be read") {
		t.Fatalf("unreadable folder file: %+v", s)
	}
	// The launch's --settings: the wrapped status line beside the rest.
	o := settings.NewObject()
	_ = o.Set("command", "x")
	if b := launchSettings("", pickerAtLaunch{}, o); !strings.Contains(string(b), `"statusLine":{"command":"x"}`) {
		t.Fatalf("launch settings: %s", b)
	}
}

// One row per real login: two keys of one identity are one row with both
// accounts; a reset window keeps its last figure; the block holds 16.
func TestUsageRowsGroupAndCap(t *testing.T) {
	root := sandboxDefaultRoot(t)
	id := "someone@example.com"
	putAccount(t, account.Account{Name: "acc-a", Provider: account.Anthropic, Identity: id, Auth: account.Auth{Kind: account.ClaudeToken, Value: "t"}})
	putAccount(t, account.Account{Name: "acc-b", Provider: account.Anthropic, Identity: id, Auth: account.Auth{Kind: account.ClaudeToken, Value: "t"}})
	past := time.Now().Add(-time.Hour).Unix()
	latest := map[string]latestFigure{
		"acc-b": {V: 1, Key: "acc-b", Account: "acc-b", SeenAt: rfc3339(time.Now()), RateLimits: json.RawMessage(fmt.Sprintf(`{"five_hour":{"used_percentage":101,"resets_at":%d}}`, past))},
	}
	rows := usageRows(root, latest, usageRowsOpts{now: time.Now()})
	if len(rows) != 1 || strings.Join(rows[0].accounts, ",") != "acc-a,acc-b" || rows[0].fiveHour == nil || rows[0].fiveHour.UsedPercentage != 101 || rows[0].state() != "reported" {
		t.Fatalf("rows: %+v", rows)
	}
	if fullKeys(latest, time.Now())["acc-b"] {
		t.Fatal("a reset window still counts as full")
	}
	for i := 0; i < 20; i++ {
		putAccount(t, account.Account{Name: fmt.Sprintf("x%02d", i), Provider: account.Anthropic, Auth: account.Auth{Kind: account.ClaudeToken, Value: "t"}})
	}
	_, rec := stepFixture(t, "x19", "x19", nil)
	_ = rec
	b, _ := statuslineBlock(root, launchRecord{Account: "x19", Key: "x19"}, nil, time.Now())
	var blk statuslineBlockJSON
	if err := json.Unmarshal(b, &blk); err != nil || len(blk.Usage) != statuslineBlockMax || blk.Usage[0].Account != "x19" || !blk.Usage[0].Self || blk.Account.Name != "x19" {
		t.Fatalf("block: %v %s", err, b)
	}
	if blockName("flags:[::1]:8080") != "flags:-::1-:8080" || blockName("Playbook:My Dir") != "playbook:my-dir" {
		t.Fatalf("names: %q %q", blockName("flags:[::1]:8080"), blockName("Playbook:My Dir"))
	}
}

// A playbooks root given relative to the launch folder is embedded in the
// step absolute: the step runs from wherever Claude Code runs it.
func TestStatuslineStepRootIsAbsolute(t *testing.T) {
	root := sandboxDefaultRoot(t)
	writePlaybook(t, root, "alpha", &manifest.Manifest{Launcher: "al"})
	cfg := filepath.Join(root, "alpha")
	old := managedSettingsPaths
	managedSettingsPaths = func() []string { return nil }
	t.Cleanup(func() { managedSettingsPaths = old })
	if err := os.WriteFile(filepath.Join(cfg, "settings.json"), []byte(`{"statusLine":{"type":"command","command":"x"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(filepath.Dir(root))
	s := statuslineStepFor(filepath.Base(root), cfg, filepath.Dir(root), "0123456789abcdef", nil)
	if s.wrapped == nil {
		t.Fatalf("not wrapped: %s", s.reason)
	}
	var cmd string
	_ = json.Unmarshal(s.wrapped.Raw("command"), &cmd)
	abs, _ := filepath.Abs(filepath.Base(root))
	if !strings.Contains(cmd, " --root "+shell.QuoteArg(abs)+" ") {
		t.Fatalf("command %q does not name the root %q", cmd, abs)
	}
}

// When no descriptor can hold the input for an exec (over a pipe's size,
// and no temporary folder), the original runs as a child, fed the whole
// input, and the step ends with its exit code.
func TestStatuslineStepFallsBackToAChild(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	execs := 0
	oldExec := stepExec
	stepExec = func(string, []string, []string) error { execs++; return nil }
	t.Cleanup(func() { stepExec = oldExec })
	out := filepath.Join(t.TempDir(), "got")
	data := []byte(`{"pad":"` + strings.Repeat("x", 3*stepPipeBytes) + `"}`)
	err := execOriginal("cat > "+shell.QuoteArg(out)+"; exit 3", data)
	if code, ok := exitCode(err); !ok || code != 3 || execs != 0 {
		t.Fatalf("err %v, execs %d", err, execs)
	}
	if got, _ := os.ReadFile(out); string(got) != string(data) {
		t.Fatalf("the original read %d bytes of %d", len(got), len(data))
	}
}

// A worktree whose .git file names its gitdir relative to the worktree
// (git's worktree.useRelativePaths), launched from a folder below it: the
// main checkout's settings are found where they are, not relative to the
// launch's folder.
func TestFolderSettingsRelativeGitdir(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	main := filepath.Join(base, "repo")
	wt := filepath.Join(base, "wt", "feature")
	sub := filepath.Join(wt, "app", "src")
	for _, d := range []string{filepath.Join(main, ".git", "worktrees", "feature"), filepath.Join(main, ".claude"), sub} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: ../../repo/.git/worktrees/feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)
	want := filepath.Join(main, ".claude", "settings.json")
	if files := folderSettingsFiles(sub); !slices.Contains(files, want) {
		t.Fatalf("files: %v", files)
	}
	if err := os.WriteFile(want, []byte(`{"statusLine":{"type":"command","command":"x"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if set, err := setsStatusLine(want); !set || err != nil {
		t.Fatalf("the main checkout's status line: %v %v", set, err)
	}
}

// A folder whose .claude is a file holds no settings, as Claude Code
// reads none from it: no reason to drop the step, and no reason to refuse
// a launch.
func TestDotClaudeFileIsNoSettings(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".claude"), []byte("not a folder"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(repo, "app")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range folderSettingsFiles(sub) {
		if set, err := setsStatusLine(p); set || err != nil {
			t.Fatalf("%s: %v %v", p, set, err)
		}
	}
	if err := reservedInSettings(t.TempDir(), repo); err != nil {
		t.Fatalf("a .claude file refused the launch: %v", err)
	}
}
