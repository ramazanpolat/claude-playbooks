package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ramazanpolat/claude-playbooks/internal/account"
)

// rateInput is a status line input of session sid with fixed figures, as
// Claude Code sends them: a window's reset does not move between renders.
func rateInput(sid string, five float64, fiveResets int64, seven float64, sevenResets int64) []byte {
	return []byte(fmt.Sprintf(`{"session_id":%q,"model":{"id":"claude-opus-5-5"},`+
		`"rate_limits":{"five_hour":{"used_percentage":%g,"resets_at":%d},"seven_day":{"used_percentage":%g,"resets_at":%d}}}`+"\n",
		sid, five, fiveResets, seven, sevenResets))
}

func latestFive(t *testing.T, root, key string) float64 {
	t.Helper()
	l, ok := readLatestFigures(root)[key]
	if !ok {
		t.Fatalf("no latest figure for %s", key)
	}
	five, _, _ := windowsOf(l.RateLimits)
	if five == nil {
		t.Fatalf("no 5-hour window: %s", l.RateLimits)
	}
	return five.UsedPercentage
}

// An idle session re-rendering its old figure never replaces a newer one
// another session saw; for a window that resets at the same time, the
// higher use stands.
func TestUsageLatestKeepsTheNewerFigure(t *testing.T) {
	root, rec := stepFixture(t, "work", "work", nil)
	a := stepArgs{root: root, launch: rec.LaunchID, original: "true"}
	five, seven := time.Now().Add(time.Hour).Unix(), time.Now().Add(72*time.Hour).Unix()
	sidA, sidB := "d9c690fc-1a83-4be9-b6aa-00000000000a", "d9c690fc-1a83-4be9-b6aa-00000000000b"
	stepRecord(a, rateInput(sidB, 40, five, 10, seven))
	stepRecord(a, rateInput(sidA, 100, five, 12, seven))
	if got := latestFive(t, root, "work"); got != 100 {
		t.Fatalf("after A: %v", got)
	}
	stepRecord(a, rateInput(sidB, 40, five, 10, seven)) // B re-renders its old figure
	if got := latestFive(t, root, "work"); got != 100 {
		t.Fatalf("a re-render replaced the newer figure: %v", got)
	}
	stepRecord(a, rateInput(sidB, 45, five, 10, seven)) // B's own new figure, same window
	if got := latestFive(t, root, "work"); got != 100 {
		t.Fatalf("a lower use of the same window replaced a higher one: %v", got)
	}
	later := five + 5*3600
	stepRecord(a, rateInput(sidB, 3, later, 10, seven)) // a new window
	if got := latestFive(t, root, "work"); got != 3 {
		t.Fatalf("a new window's figure was kept out: %v", got)
	}
}

// After a window resets, an idle session still showing the old window's
// figure does not bring it back over the new window's.
func TestUsageStaleWindowDoesNotReturn(t *testing.T) {
	root, rec := stepFixture(t, "work", "work", nil)
	a := stepArgs{root: root, launch: rec.LaunchID, original: "true"}
	old, fresh := time.Now().Add(-10*time.Minute).Unix(), time.Now().Add(5*time.Hour).Unix()
	seven := time.Now().Add(72 * time.Hour).Unix()
	sidA, sidB := "d9c690fc-1a83-4be9-b6aa-00000000000a", "d9c690fc-1a83-4be9-b6aa-00000000000b"
	stepRecord(a, rateInput(sidB, 100, old, 10, seven))
	stepRecord(a, rateInput(sidA, 3, fresh, 10, seven))
	stepRecord(a, rateInput(sidB, 100, old, 10, seven)) // B, idle, re-renders
	if got := latestFive(t, root, "work"); got != 3 {
		t.Fatalf("the old window's figure came back: %v", got)
	}
}

// An older figure never replaces a newer one.
func TestUsageWriteLatestKeepsNewer(t *testing.T) {
	root := t.TempDir()
	rates := func(p float64) json.RawMessage {
		return json.RawMessage(fmt.Sprintf(`{"five_hour":{"used_percentage":%g,"resets_at":%d}}`, p, time.Now().Add(time.Hour).Unix()+int64(p)))
	}
	now := time.Now().UTC()
	writeLatest(root, usageSnapshot{Key: "k", Account: "k", At: rfc3339(now), SessionID: stepSID, RateLimits: rates(50)})
	writeLatest(root, usageSnapshot{Key: "k", Account: "k", At: rfc3339(now.Add(-time.Minute)), SessionID: stepSID, RateLimits: rates(10)})
	if got := latestFive(t, root, "k"); got != 50 {
		t.Fatalf("an older figure replaced a newer one: %v", got)
	}
}

// The latest figures are read defensively: only a regular file named by
// its key's hash, every name in it as a launch writes it, never one seen in
// the future, its numbers bounded.
func TestUsageReadLatestIsDefensive(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(usageDir(root), "accounts")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	put := func(name string, l latestFigure) {
		t.Helper()
		l.V = usageVersion
		if l.SeenAt == "" {
			l.SeenAt = rfc3339(now)
		}
		if l.RateLimits == nil {
			l.RateLimits = json.RawMessage(fmt.Sprintf(`{"five_hour":{"used_percentage":30,"resets_at":%d}}`, now.Add(time.Hour).Unix()))
		}
		b, _ := json.Marshal(l)
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	at := func(key string) string { return filepath.Base(latestPath(root, key)) }
	put(at("good"), latestFigure{Key: "good", Account: "good"})
	put(at("esc"), latestFigure{Key: "esc", Account: "esc\x1b]0;x\x07"})
	put(at("bad\x1bkey"), latestFigure{Key: "bad\x1bkey", Account: "a"})
	put(at("pb"), latestFigure{Key: "pb", Account: "pb", Playbook: "p\rq"})
	put(at("sid"), latestFigure{Key: "sid", Account: "sid", SessionID: "--flag"})
	put("0000000000000000.json", latestFigure{Key: "elsewhere", Account: "elsewhere"}) // not its key's file
	put(at("future"), latestFigure{Key: "future", Account: "future", SeenAt: rfc3339(now.Add(24 * time.Hour))})
	put(at("huge"), latestFigure{Key: "huge", Account: "huge",
		RateLimits: json.RawMessage(`{"five_hour":{"used_percentage":1e300,"resets_at":1e300},"seven_day":{"used_percentage":-5,"resets_at":4102444800}}`)})
	// A symbolic link at a key's file is not followed.
	put("target.json", latestFigure{Key: "linked", Account: "linked"})
	if err := os.Symlink(filepath.Join(dir, "target.json"), filepath.Join(dir, at("linked"))); err != nil {
		t.Fatal(err)
	}
	got := readLatestFigures(root)
	keys := []string{}
	for k := range got {
		keys = append(keys, k)
	}
	if len(got) != 2 || got["good"].Key != "good" || got["huge"].Key != "huge" {
		t.Fatalf("read: %v", keys)
	}
	five, seven, _ := windowsOf(got["huge"].RateLimits)
	if five == nil || five.UsedPercentage != 1000 || five.ResetsAt > now.Add(5*time.Hour+time.Minute).Unix() ||
		seven == nil || seven.UsedPercentage != 0 || seven.ResetsAt > now.Add(7*24*time.Hour+time.Minute).Unix() {
		t.Fatalf("not bounded: %s", got["huge"].RateLimits)
	}
}

// A window whose reset has passed is no limit, whatever its use.
func TestUsageAccountLimitOfPastReset(t *testing.T) {
	past := time.Now().Add(-time.Minute).Unix()
	l := latestFigure{RateLimits: json.RawMessage(fmt.Sprintf(`{"five_hour":{"used_percentage":100,"resets_at":%d}}`, past))}
	if lim := accountLimitOf(l, time.Now()); lim != nil {
		t.Fatalf("a past reset is a limit: %+v", lim)
	}
	l.RateLimits = json.RawMessage(fmt.Sprintf(`{"five_hour":{"used_percentage":100,"resets_at":%d}}`, time.Now().Add(time.Hour).Unix()))
	if lim := accountLimitOf(l, time.Now()); lim == nil {
		t.Fatal("a window at 100% is no limit")
	}
}

// A managed status line outranks the playbook's: no step.
func TestStatuslineStepManagedSettings(t *testing.T) {
	root := sandboxDefaultRoot(t)
	writePlaybook(t, root, "alpha", nil)
	cfg := filepath.Join(root, "alpha")
	if err := os.WriteFile(filepath.Join(cfg, "settings.json"), []byte(`{"statusLine":{"type":"command","command":"x"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	managed := filepath.Join(t.TempDir(), "managed-settings.json")
	if err := os.WriteFile(managed, []byte(`{"statusLine":{"type":"command","command":"policy"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	old := managedSettingsPaths
	managedSettingsPaths = func() []string { return []string{managed} }
	t.Cleanup(func() { managedSettingsPaths = old })
	if s := statuslineStepFor(root, cfg, t.TempDir(), "0123456789abcdef", nil); s.wrapped != nil || !strings.Contains(s.reason, "managed settings") {
		t.Fatalf("step: %+v", s)
	}
}

// A launch record is read only by an id of a launch id's shape, from the
// file that id names, holding that id.
func TestReadLaunchRecordByIDChecks(t *testing.T) {
	root := t.TempDir()
	dir := launchRecordsDir(root)
	write := func(name, id string) {
		t.Helper()
		r := launchRecord{V: launchRecordVersion, LaunchID: id, PID: os.Getpid(), ProcStart: "x", Account: "a", LaunchedAt: rfc3339(time.Now())}
		if err := writeLaunchRecord(dir, filepath.Join(dir, name+".json"), &r); err != nil {
			t.Fatal(err)
		}
	}
	write("0123456789abcdef", "0123456789abcdef")
	write("1111111111111111", "2222222222222222")
	write("ABCDEF0123456789", "ABCDEF0123456789")
	if _, ok := readLaunchRecordByID(root, "0123456789abcdef"); !ok {
		t.Fatal("a good record was not read")
	}
	if _, ok := readLaunchRecordByID(root, "1111111111111111"); ok {
		t.Fatal("a record holding another id was read")
	}
	if _, ok := readLaunchRecordByID(root, "ABCDEF0123456789"); ok {
		t.Fatal("an id of another shape was read")
	}
}

// The step records nothing for a root that is not absolute, and passes the
// input through.
func TestStatuslineStepRelativeRoot(t *testing.T) {
	root, rec := stepFixture(t, "work", "work", nil)
	t.Chdir(filepath.Dir(root))
	in := rateInput(stepSID, 10, time.Now().Add(time.Hour).Unix(), 1, time.Now().Add(72*time.Hour).Unix())
	if out := stepRecord(stepArgs{root: filepath.Base(root), launch: rec.LaunchID, original: "true"}, in); string(out) != string(in) {
		t.Fatalf("changed: %s", out)
	}
	if _, err := os.Stat(usageDir(root)); !os.IsNotExist(err) {
		t.Fatalf("recorded under a relative root: %v", err)
	}
}

// A render that cannot take the log's lock at once writes nothing.
func TestUsageAppendTakesTheLockOrSkips(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	s := usageSnapshot{V: usageVersion, At: rfc3339(now), SessionID: stepSID, Key: "k", Account: "k",
		RateLimits: json.RawMessage(`{"five_hour":{"used_percentage":1,"resets_at":1}}`)}
	path := sessionLogPath(root, stepSID, now)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	held, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	if wrote, _ := appendSnapshot(root, s, now); wrote {
		t.Fatal("wrote without the lock")
	}
	if b, _ := os.ReadFile(path); len(b) != 0 {
		t.Fatalf("the log changed: %s", b)
	}
}

// A render that opened the log just before another rotated it writes
// nothing into the old generation.
func TestUsageAppendAfterAnotherRotated(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	s := usageSnapshot{V: usageVersion, At: rfc3339(now), SessionID: stepSID, Key: "k", Account: "k",
		RateLimits: json.RawMessage(`{"five_hour":{"used_percentage":1,"resets_at":1}}`)}
	path := sessionLogPath(root, stepSID, now)
	old := strings.TrimSuffix(path, ".jsonl") + ".1.jsonl"
	appendOpened = func(p string) {
		_ = os.Rename(p, old)
		_ = os.WriteFile(p, nil, 0o600)
	}
	t.Cleanup(func() { appendOpened = func(string) {} })
	if wrote, _ := appendSnapshot(root, s, now); wrote {
		t.Fatal("wrote after the log was rotated under it")
	}
	if b, _ := os.ReadFile(old); len(b) != 0 {
		t.Fatalf("wrote into the old generation: %s", b)
	}
}

// Nothing is written or swept through a symbolic link under .state/usage.
func TestUsageNoSymlinks(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	outside := t.TempDir()
	s := usageSnapshot{V: usageVersion, At: rfc3339(now), SessionID: stepSID, Key: "k", Account: "k",
		RateLimits: json.RawMessage(`{"five_hour":{"used_percentage":1,"resets_at":1}}`)}
	path := sessionLogPath(root, stepSID, now)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(outside, "log")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if wrote, _ := appendSnapshot(root, s, now); wrote {
		t.Fatal("wrote through a symbolic link")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("the link's target was written")
	}
	// A symbolic link in place of the accounts folder: nothing read or
	// swept through it.
	victim := filepath.Join(outside, "old.json")
	if err := os.WriteFile(victim, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	long := now.Add(-60 * 24 * time.Hour)
	_ = os.Chtimes(victim, long, long)
	if err := os.Symlink(outside, filepath.Join(usageDir(root), "accounts")); err != nil {
		t.Fatal(err)
	}
	sweepUsage(root, now)
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("swept through a link: %v", err)
	}
}

// Rows of one identity get the same name every time.
func TestUsageRowNameIsStable(t *testing.T) {
	root := sandboxDefaultRoot(t)
	id := "someone@example.com"
	future := time.Now().Add(time.Hour).Unix()
	latest := map[string]latestFigure{}
	for _, k := range []string{"machine", "playbook:foo", "playbook:bar"} {
		latest[k] = latestFigure{V: 1, Key: k, Account: k, SeenAt: rfc3339(time.Now()),
			RateLimits: json.RawMessage(fmt.Sprintf(`{"five_hour":{"used_percentage":5,"resets_at":%d}}`, future))}
	}
	opts := usageRowsOpts{now: time.Now(), loginIdentity: func(string) *string { return &id }}
	first := ""
	for i := 0; i < 30; i++ {
		rows := usageRows(root, latest, opts)
		if len(rows) != 1 {
			t.Fatalf("rows: %+v", rows)
		}
		if first == "" {
			first = rows[0].name
		} else if rows[0].name != first {
			t.Fatalf("name %q, then %q", first, rows[0].name)
		}
	}
}

// With cpb's binary gone, the status line command still runs the
// original, with its input.
func TestStatuslineStepCommandWithoutCpb(t *testing.T) {
	cmd := stepCommand(filepath.Join(t.TempDir(), "gone", "cpb"), "/root", "0123456789abcdef", "echo seen; cat")
	c := exec.Command("/bin/sh", "-c", cmd)
	c.Stdin = strings.NewReader("input")
	out, err := c.Output()
	if err != nil || string(out) != "seen\ninput" {
		t.Fatalf("%v %q", err, out)
	}
}

// A record named for its pid, as builds before launch ids left it, goes
// once its process is gone.
func TestSweepPidNamedRecords(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "launches")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	dead, live := filepath.Join(dir, fmt.Sprintf("%d.json", 1<<30)), filepath.Join(dir, fmt.Sprintf("%d.json", os.Getpid()))
	for _, p := range []string{dead, live} {
		if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sweepLaunchRecords(dir)
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Fatal("a dead pid's record was kept")
	}
	if _, err := os.Stat(live); err != nil {
		t.Fatal("a live pid's record was removed")
	}
}

// Dropping an account, or giving it another credential, removes its
// latest figure.
func TestUsageForgetsADroppedAccount(t *testing.T) {
	root := sandboxDefaultRoot(t)
	putAccount(t, account.Account{Name: "acc", Provider: account.Anthropic, Auth: account.Auth{Kind: account.ClaudeToken, Value: "one"}})
	seed := func() {
		t.Helper()
		writeLatest(root, usageSnapshot{Key: "acc", Account: "acc", At: rfc3339(time.Now()), SessionID: stepSID,
			RateLimits: json.RawMessage(fmt.Sprintf(`{"five_hour":{"used_percentage":100,"resets_at":%d}}`, time.Now().Add(time.Hour).Unix()))})
		if _, ok := readLatestFigures(root)["acc"]; !ok {
			t.Fatal("no figure seeded")
		}
	}
	seed()
	mustStmt(t, "ALTER ACCOUNT acc SET model = claude-opus-5-5")
	if _, ok := readLatestFigures(root)["acc"]; !ok {
		t.Fatal("a change of model removed the figure")
	}
	helper, _ := fakeHelper(t)
	mustStmt(t, "ALTER DEFAULTS SET secret_helper = "+helper)
	mustStmt(t, "ALTER ACCOUNT acc SET claude_token FROM keychain:ok/two")
	if _, ok := readLatestFigures(root)["acc"]; ok {
		t.Fatal("another credential kept the old figure")
	}
	seed()
	mustStmt(t, "DROP ACCOUNT acc")
	if _, ok := readLatestFigures(root)["acc"]; ok {
		t.Fatal("a dropped account kept its figure")
	}
}

// putLatest writes a latest figure file for key as given, at its own name.
func putLatest(t *testing.T, root string, l latestFigure) {
	t.Helper()
	l.V = usageVersion
	b, _ := json.Marshal(l)
	p := latestPath(root, l.Key)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A figure seen longer ago than its window lasts no longer holds the
// account at its limit, however far its reset claims to be.
func TestUsageResetCappedBySeenTime(t *testing.T) {
	root := t.TempDir()
	far := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	for key, c := range map[string]struct {
		seen   time.Duration
		window string
		full   bool
	}{
		"five-6h":   {6 * time.Hour, "five_hour", false},
		"five-1h":   {time.Hour, "five_hour", true},
		"seven-20d": {20 * 24 * time.Hour, "seven_day", false},
		"seven-3d":  {3 * 24 * time.Hour, "seven_day", true},
	} {
		putLatest(t, root, latestFigure{Key: key, Account: key, SeenAt: rfc3339(time.Now().Add(-c.seen)),
			RateLimits: json.RawMessage(fmt.Sprintf(`{%q:{"used_percentage":100,"resets_at":%d}}`, c.window, far))})
	}
	latest := readLatestFigures(root)
	full := fullKeys(latest, time.Now())
	for key, want := range map[string]bool{"five-6h": false, "five-1h": true, "seven-20d": false, "seven-3d": true} {
		if full[key] != want {
			t.Errorf("%s: full %v, want %v", key, full[key], want)
		}
		if lim := accountLimitOf(latest[key], time.Now()); (lim != nil) != want {
			t.Errorf("%s: limit %+v", key, lim)
		}
	}
}

// A stored figure claiming a seen time in the future never holds back a
// session's real figures.
func TestUsageFutureLatestIsReplaced(t *testing.T) {
	root, rec := stepFixture(t, "work", "work", nil)
	putLatest(t, root, latestFigure{Key: "work", Account: "work", SeenAt: rfc3339(time.Now().Add(30 * 24 * time.Hour)),
		RateLimits: json.RawMessage(fmt.Sprintf(`{"five_hour":{"used_percentage":99,"resets_at":%d}}`, time.Now().Add(time.Hour).Unix()))})
	a := stepArgs{root: root, launch: rec.LaunchID, original: "true"}
	five, seven := time.Now().Add(2*time.Hour).Unix(), time.Now().Add(72*time.Hour).Unix()
	stepRecord(a, rateInput(stepSID, 12, five, 1, seven))
	if got := latestFive(t, root, "work"); got != 12 {
		t.Fatalf("latest: %v", got)
	}
}

// A new session's first render carrying a window that has already reset
// does not bring it back over the newer window stored.
func TestUsageFirstRenderKeepsTheNewerWindow(t *testing.T) {
	root, rec := stepFixture(t, "work", "work", nil)
	a := stepArgs{root: root, launch: rec.LaunchID, original: "true"}
	older, newer := time.Now().Add(-time.Minute).Unix(), time.Now().Add(4*time.Hour).Unix()
	seven := time.Now().Add(72 * time.Hour).Unix()
	stepRecord(a, rateInput("d9c690fc-1a83-4be9-b6aa-00000000000a", 5, newer, 1, seven))
	stepRecord(a, rateInput("d9c690fc-1a83-4be9-b6aa-00000000000c", 97, older, 1, seven))
	if got := latestFive(t, root, "work"); got != 5 {
		t.Fatalf("an older window came back: %v", got)
	}
}

// An identity label a terminal would not show as written is not shown:
// SHOW USAGE groups and prints without it; SHOW ACCOUNT shows none; a
// statement cannot set one.
func TestUsageIdentityLabelShape(t *testing.T) {
	root := sandboxDefaultRoot(t)
	putAccount(t, account.Account{Name: "acc", Provider: account.Anthropic, Identity: "me\x1b]0;x\x07", Auth: account.Auth{Kind: account.ClaudeToken, Value: "t"}})
	putAccount(t, account.Account{Name: "acc2", Provider: account.Anthropic, Identity: "me‮x", Auth: account.Auth{Kind: account.ClaudeToken, Value: "t"}})
	for _, r := range usageRows(root, nil, usageRowsOpts{now: time.Now()}) {
		if r.identity != nil {
			t.Fatalf("%s shows identity %q", r.name, *r.identity)
		}
	}
	if v := describeAccount(&account.Account{Name: "acc", Identity: "me\x1b[31m"}, nil, false); v.Identity != nil {
		t.Fatalf("SHOW ACCOUNT shows %q", *v.Identity)
	}
	if err := stmtErr(t, "ALTER ACCOUNT acc SET identity = 'a​b'"); err == nil || !strings.Contains(err.Error(), "control or invisible") {
		t.Fatalf("set: %v", err)
	}
}

// Names hold no direction marks, overrides, zero-width characters or
// separators.
func TestValidRecordedAccountRefusesInvisible(t *testing.T) {
	for _, r := range []rune{0x200B, 0x200F, 0x202A, 0x202E, 0x2066, 0x2069, 0x2028, 0x2029} {
		if validRecordedAccount("a" + string(r) + "b") {
			t.Errorf("U+%04X accepted", r)
		}
	}
	if !validRecordedAccount("block:my dir") || !validRecordedAccount("çalışma") {
		t.Fatal("a plain name refused")
	}
}

// Through the child path the step ends as a shell would: the original's
// code, or 128 plus the signal that ended it; a SIGTERM to the step ends
// the original too.
func TestStatuslineStepMainExitCodes(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	big := strings.Repeat("x", 3*stepPipeBytes)
	run := func(original string) int {
		return statuslineStepMain([]string{"--", original}, strings.NewReader(big))
	}
	if code := run("cat >/dev/null; exit 7"); code != 7 {
		t.Fatalf("exit 7: %d", code)
	}
	if code := run("cat >/dev/null; kill -TERM $$"); code != 128+int(syscall.SIGTERM) {
		t.Fatalf("a signal: %d", code)
	}
	if code := run("cat >/dev/null"); code != 0 {
		t.Fatalf("success: %d", code)
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
	}()
	start := time.Now()
	if code := run("cat >/dev/null; sleep 20"); code != 128+int(syscall.SIGTERM) || time.Since(start) > 10*time.Second {
		t.Fatalf("a SIGTERM to the step: %d after %v", code, time.Since(start))
	}
}

// A render whose own figures did not change (here only the model did)
// writes its line but leaves the account's latest figure as it was.
func TestUsageModelChangeKeepsLatest(t *testing.T) {
	root, rec := stepFixture(t, "work", "work", nil)
	a := stepArgs{root: root, launch: rec.LaunchID, original: "true"}
	five, seven := time.Now().Add(time.Hour).Unix(), time.Now().Add(72*time.Hour).Unix()
	in := rateInput(stepSID, 20, five, 1, seven)
	stepRecord(a, in)
	before, _ := os.ReadFile(latestPath(root, "work"))
	stepNow = func() time.Time { return time.Now().Add(time.Second) }
	t.Cleanup(func() { stepNow = time.Now })
	stepRecord(a, []byte(strings.Replace(string(in), "claude-opus-5-5", "claude-sonnet-5-5", 1)))
	after, _ := os.ReadFile(latestPath(root, "work"))
	logb, _ := os.ReadFile(sessionLogPath(root, stepSID, time.Now()))
	if strings.Count(string(logb), "\n") != 2 || string(before) != string(after) {
		t.Fatalf("lines %d; latest before %s after %s", strings.Count(string(logb), "\n"), before, after)
	}
}

// A latest figure whose seen time does not parse is not read.
func TestUsageReadLatestSeenAtParses(t *testing.T) {
	root := t.TempDir()
	putLatest(t, root, latestFigure{Key: "k", Account: "k", SeenAt: "yesterday",
		RateLimits: json.RawMessage(`{"five_hour":{"used_percentage":1,"resets_at":1}}`)})
	if _, ok := readLatestFigures(root)["k"]; ok {
		t.Fatal("read a figure with no seen time")
	}
}

// No log, latest figure or sweep goes through a usage folder that is a
// symbolic link.
func TestUsageRealFolders(t *testing.T) {
	now := time.Now()
	s := usageSnapshot{V: usageVersion, At: rfc3339(now), SessionID: stepSID, Key: "k", Account: "k",
		RateLimits: json.RawMessage(`{"five_hour":{"used_percentage":1,"resets_at":1}}`)}
	link := func(root, name string) string {
		t.Helper()
		out := t.TempDir()
		if err := os.MkdirAll(usageDir(root), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(out, filepath.Join(usageDir(root), name)); err != nil {
			t.Fatal(err)
		}
		return out
	}
	empty := func(dir string) bool {
		ents, _ := os.ReadDir(dir)
		return len(ents) == 0
	}
	// The sessions folder.
	root := t.TempDir()
	out := link(root, "sessions")
	if wrote, _ := appendSnapshot(root, s, now); wrote || !empty(out) {
		t.Fatal("a log written through a linked sessions folder")
	}
	month := filepath.Join(out, now.UTC().Format("2006-01"))
	if err := os.MkdirAll(month, 0o700); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(month, "old.jsonl")
	_ = os.WriteFile(old, nil, 0o600)
	long := now.Add(-60 * 24 * time.Hour)
	_ = os.Chtimes(old, long, long)
	sweepUsage(root, now)
	if _, err := os.Stat(old); err != nil {
		t.Fatal("swept through a linked sessions folder")
	}
	// The accounts folder: neither written nor read.
	root = t.TempDir()
	out = link(root, "accounts")
	writeLatest(root, s)
	if !empty(out) {
		t.Fatal("a latest figure written through a linked accounts folder")
	}
	b, _ := json.Marshal(latestFigure{V: usageVersion, Key: "k", Account: "k", SeenAt: rfc3339(now), RateLimits: s.RateLimits})
	_ = os.WriteFile(filepath.Join(out, filepath.Base(latestPath(root, "k"))), b, 0o600)
	if len(readLatestFigures(root)) != 0 {
		t.Fatal("read through a linked accounts folder")
	}
}

// A stored window whose reset claims to be far off counts only up to its
// seen time plus the window's length: it does not hold back a real figure.
func TestUsageStoredFarResetDoesNotHoldBack(t *testing.T) {
	root, rec := stepFixture(t, "work", "work", nil)
	putLatest(t, root, latestFigure{Key: "work", Account: "work", SeenAt: rfc3339(time.Now().Add(-6 * time.Hour)),
		RateLimits: json.RawMessage(fmt.Sprintf(`{"five_hour":{"used_percentage":100,"resets_at":%d}}`, time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC).Unix()))})
	a := stepArgs{root: root, launch: rec.LaunchID, original: "true"}
	stepRecord(a, rateInput(stepSID, 3, time.Now().Add(4*time.Hour).Unix(), 1, time.Now().Add(72*time.Hour).Unix()))
	if got := latestFive(t, root, "work"); got != 3 {
		t.Fatalf("held back: %v", got)
	}
}
