package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
)

// An answered ask is appended to the history, and the next ask hands the
// model the index and the last record, after the installed state.
func TestAskStoreRecordsAndHandsBack(t *testing.T) {
	askFixture(t)
	root := config.ResolvePlaybooksDir()
	rec := fakeAskClaude(t, askListPlan)

	out, err := askRun(t, map[string]string{"json": "true"}, "list the env profiles")
	if err != nil {
		t.Fatal(err)
	}
	var p askPlan
	if err := json.Unmarshal([]byte(out), &p); err != nil || !strings.HasPrefix(p.Record, "r-") {
		t.Fatalf("--json record: %q %v\n%s", p.Record, err, out)
	}
	if strings.Contains(readRec(t, rec, "stdin"), "History of your earlier answers") {
		t.Fatal("the first ask had history to hand")
	}
	dir := askStoreDir(root)
	for path, want := range map[string]os.FileMode{dir: 0o700, filepath.Join(dir, askHistoryName): 0o600} {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != want {
			t.Fatalf("%s: %v, want mode %o", path, err, want)
		}
	}

	if _, err := askRun(t, nil, "work", "how many of your sessions are live?"); err == nil {
		t.Fatal("work routes away: an ask addressed to it is refused")
	}
	if _, err := askRun(t, nil, "I meant accounts"); err != nil {
		t.Fatal(err)
	}
	stdin := readRec(t, rec, "stdin")
	for _, want := range []string{"History of your earlier answers", " · - · " + p.Record + " · list the env profiles", "**Asked:** list the env profiles", "`cpb SHOW ENVS`", "Request: I meant accounts"} {
		if !strings.Contains(stdin, want) {
			t.Errorf("the second ask lacks %q:\n%s", want, stdin)
		}
	}
	if i, j := strings.Index(stdin, "Installed state:"), strings.Index(stdin, "History of"); i < 0 || j < i {
		t.Errorf("history before the installed state:\n%s", stdin)
	}
	entries, err := readAskHistory(root)
	if err != nil || len(entries) != 2 {
		t.Fatalf("records: %d %v (a refused ask is not recorded)", len(entries), err)
	}
}

// --no-history neither reads the history nor adds to it, and the tool is
// started so that it reads none either.
func TestAskStoreNoHistory(t *testing.T) {
	askFixture(t)
	root := config.ResolvePlaybooksDir()
	rec := fakeAskClaude(t, askListPlan)
	if _, err := askRun(t, nil, "list the env profiles"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(askStoreDir(root), askHistoryName)
	before, _ := os.ReadFile(path)
	t.Cleanup(func() { askCmd.Flags().Set("no-history", "false") })
	out, err := askRun(t, map[string]string{"no-history": "true", "json": "true"}, "list again")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(readRec(t, rec, "stdin"), "History of") || strings.Contains(out, `"record"`) {
		t.Fatalf("--no-history read or wrote the history:\n%s", out)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatal("--no-history changed the history")
	}
	if argv := readRec(t, rec, "argv"); !strings.Contains(argv, `"ask-tool","--no-history"`) {
		t.Fatalf("the tool was started without --no-history:\n%q", argv)
	}
}

// The user's standing context reaches the model; one holding a credential,
// or a link in its place, refuses the ask before anything is sent. A
// request refused for a credential is never recorded.
func TestAskStoreStandingContext(t *testing.T) {
	askFixture(t)
	root := config.ResolvePlaybooksDir()
	rec := fakeAskClaude(t, askListPlan)
	standing := filepath.Join(askStoreDir(root), askStandingName)
	writeFile(t, standing, "I call my router account \"the box\".\n")
	if _, err := askRun(t, nil, "list"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readRec(t, rec, "stdin"), "Your standing context, the user's own words (.ask/CLAUDE.md):\nI call my router account \"the box\".") {
		t.Fatalf("standing context missing:\n%s", readRec(t, rec, "stdin"))
	}

	os.Remove(filepath.Join(rec, "stdin"))
	writeFile(t, standing, "my key: ANTHROPIC_AUTH_TOKEN=sk-ant-api03-abcdefghijklmnopqrstuv\n")
	if _, err := askRun(t, nil, "list"); err == nil || !strings.Contains(err.Error(), "CLAUDE.md looks like it holds a credential") || strings.Contains(err.Error(), "abcdefghij") {
		t.Fatalf("a credential in the standing context: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rec, "stdin")); err == nil {
		t.Fatal("claude ran with a credential in the standing context")
	}

	os.Remove(standing)
	if err := os.Symlink(filepath.Join(t.TempDir(), "elsewhere"), standing); err != nil {
		t.Fatal(err)
	}
	if _, err := askRun(t, nil, "list"); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("a link in place of the standing context: %v", err)
	}
	os.Remove(standing)

	n := len(mustAskHistory(t, root))
	if _, err := askRun(t, nil, "create env set x with ANTHROPIC_AUTH_TOKEN=sk-ant-api03-abcdefghijklmnopqrstuv"); err == nil {
		t.Fatal("a credential in the request was sent")
	}
	if got := len(mustAskHistory(t, root)); got != n {
		t.Fatalf("a refused request was recorded: %d records, want %d", got, n)
	}
}

func mustAskHistory(t *testing.T, root string) []askEntry {
	t.Helper()
	e, err := readAskHistory(root)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// SHOW ASK HISTORY and SHOW ASK RECORD, over the tool as Claude Code calls
// it and from the command line: index lines newest first, by playbook,
// before an id, a bounded limit, and one record in full. A torn last
// append is skipped.
func TestShowAskHistory(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CPB_PLAYBOOKS_DIR", root)
	config.PlaybooksDir = ""
	plan := func(cmd string) *askPlan {
		return &askPlan{Answer: "ok", Steps: []askStep{{Command: cmd, Status: stepOK, Why: "does it"}}}
	}
	var ids []string
	for i, tag := range []string{"", "reviewer", "", "reviewer"} {
		id, err := appendAskRecord(root, tag, "ask "+string(rune('a'+i)), plan("cpb SHOW ENVS"))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		time.Sleep(time.Millisecond)
	}
	f, _ := os.OpenFile(filepath.Join(askStoreDir(root), askHistoryName), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("## 2026-11-02T11:39:12+03:00 · torn")
	f.Close()

	index := func(stmt string) []askEntry {
		t.Helper()
		text, err := askQuery(stmt)
		if err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
		var got []askEntry
		if err := json.Unmarshal([]byte(text), &got); err != nil {
			t.Fatalf("%s: %v\n%s", stmt, err, text)
		}
		return got
	}
	if got := index("SHOW ASK HISTORY"); len(got) != 4 || got[0].ID != ids[3] || got[0].Asked != "ask d" || got[3].Tag != "-" {
		t.Fatalf("SHOW ASK HISTORY: %+v", got)
	}
	if got := index("SHOW ASK HISTORY FOR PLAYBOOK reviewer"); len(got) != 2 || got[0].ID != ids[3] || got[1].ID != ids[1] {
		t.Fatalf("FOR PLAYBOOK reviewer: %+v", got)
	}
	if got := index("SHOW ASK HISTORY BEFORE " + ids[2] + " LIMIT 1"); len(got) != 1 || got[0].ID != ids[1] {
		t.Fatalf("BEFORE LIMIT: %+v", got)
	}
	text, err := askQuery("SHOW ASK RECORD " + ids[1])
	var rec askRecordJSON
	if err == nil {
		err = json.Unmarshal([]byte(text), &rec)
	}
	if err != nil || rec.ID != ids[1] || rec.Tag != "reviewer" || !strings.Contains(rec.Text, "**Asked:** ask b") || !strings.Contains(rec.Text, "`cpb SHOW ENVS`: does it") {
		t.Fatalf("SHOW ASK RECORD: %v\n%s", err, text)
	}
	for _, bad := range []string{
		"SHOW ASK RECORD r-00000000-000000-0000", "SHOW ASK HISTORY BEFORE r-00000000-000000-0000",
		"SHOW ASK HISTORY LIMIT 0", "SHOW ASK HISTORY SINCE x", "HISTORY RECORD " + ids[1],
	} {
		if _, err := askQuery(bad); err == nil {
			t.Errorf("%s: accepted", bad)
		}
	}

	// From the command line: a table, newest first, and the statement that
	// pages on; --json prints what the tool returns.
	run := func(args ...string) string {
		t.Helper()
		var err error
		out := captureStdout(t, func() { err = runStatement(args) })
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return out
	}
	out := run("SHOW", "ASK", "HISTORY", "LIMIT", "2")
	if !strings.Contains(out, "TIME") || !strings.Contains(out, "ASKED") || !strings.Contains(out, ids[3]) || !strings.Contains(out, ids[2]) ||
		strings.Contains(out, ids[1]+" ") || !strings.Contains(out, "2 older: cpb SHOW ASK HISTORY BEFORE "+ids[2]+" LIMIT 2") {
		t.Fatalf("SHOW ASK HISTORY LIMIT 2:\n%s", out)
	}
	if out := run("SHOW", "ASK", "HISTORY", "--json"); strings.TrimSpace(out) == "" {
		t.Fatal("no JSON")
	} else {
		var cli []askEntry
		if err := json.Unmarshal([]byte(out), &cli); err != nil || len(cli) != 4 || cli[0].ID != ids[3] {
			t.Fatalf("--json: %v\n%s", err, out)
		}
	}
	if out := run("SHOW", "ASK", "RECORD", ids[1]); !strings.HasPrefix(out, "## ") || !strings.Contains(out, "**Asked:** ask b") {
		t.Fatalf("SHOW ASK RECORD:\n%s", out)
	}
	if out := run("SHOW", "ASK", "HISTORY", "FOR", "PLAYBOOK", "gone"); !strings.Contains(out, "No asks addressed to playbook gone") {
		t.Fatalf("FOR an unknown playbook:\n%s", out)
	}
	t.Setenv("CPB_PLAYBOOKS_DIR", t.TempDir())
	if out := run("SHOW", "ASK", "HISTORY"); !strings.Contains(out, "No asks are recorded") {
		t.Fatalf("no history:\n%s", out)
	}
}

// A part of a record that looks like it holds a credential is withheld.
func TestAskRecordWithholdsCredential(t *testing.T) {
	p := &askPlan{Answer: "Set it.", Steps: []askStep{{Command: "cpb ALTER ENV x SET API_TOKEN=sk-ant-api03-abcdefghijklmnopqrstuv", Status: stepOK, Why: "sets it"}}}
	text := askRecordText(time.Now(), "", "r-20261102-113912-3f2a", "set my token", p)
	if strings.Contains(text, "abcdefghij") || !strings.Contains(text, "<withheld: it looked like it held a credential>") {
		t.Fatalf("record:\n%s", text)
	}
}

// Every part of a record is checked, a step's guards and the reason it was
// not proposed among them: a step whose command is withheld cannot repeat
// the value beside it.
func TestAskRecordWithholdsEveryPart(t *testing.T) {
	const token = "sk-ant-api03-abcdefghijklmnopqrstuv"
	p := &askPlan{Answer: "ok", Steps: []askStep{
		{Command: "cpb ALTER ENV x SET API_TOKEN=" + token, Status: stepRejected, Reason: "cpb would refuse API_TOKEN=" + token},
		{Command: "cpb ALTER ENV x SET API_TOKEN=<token>", Status: stepPlaceholder, Reason: "fill in API_TOKEN=" + token},
		{Command: "cpb CREATE PLAYBOOK p FROM 'https://example.com/p.cpb'", Status: stepOK, Why: "installs it",
			Guards: []askGuard{{Kind: guardFetch, What: "fetches https://user:hunter2pass@example.com/p.cpb"}}},
	}}
	text := askRecordText(time.Now(), "", "r-20261102-113912-3f2a", "set my token", p)
	if strings.Contains(text, "abcdefghij") || strings.Contains(text, "hunter2pass") {
		t.Fatalf("a value reached the record:\n%s", text)
	}
	if n := strings.Count(text, "<withheld: it looked like it held a credential>"); n != 4 {
		t.Fatalf("%d parts withheld, want 4 (the command, both reasons, the guard):\n%s", n, text)
	}
}

// A markdown heading in an answer stays in its record, and a line shaped
// like a real record heading cannot make a record of its own: lines that
// start with "#" are written escaped, and only a heading that parses
// starts a record.
func TestAskRecordHeadingsInAnswer(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CPB_PLAYBOOKS_DIR", root)
	config.PlaybooksDir = ""
	fake := "## 2026-01-01T00:00:00Z · - · r-20260101-000000-abcd"
	p := &askPlan{
		Answer: "Here it is:\n## Accounts\nUse the box.\n" + fake + "\n**Asked:** nobody asked this",
		Steps:  []askStep{{Command: "cpb SHOW ACCOUNTS", Status: stepOK, Why: "lists them"}},
		Notes:  []string{"# a note that looks like a heading"},
	}
	id, err := appendAskRecord(root, "", "show accounts", p)
	if err != nil {
		t.Fatal(err)
	}
	second, err := appendAskRecord(root, "", "and again", p)
	if err != nil {
		t.Fatal(err)
	}
	entries := mustAskHistory(t, root)
	if len(entries) != 2 || entries[0].ID != id || entries[1].ID != second || entries[0].Asked != "show accounts" {
		t.Fatalf("records: %+v", entries)
	}
	for _, want := range []string{"Use the box.", "\\## Accounts", "\\" + fake, "`cpb SHOW ACCOUNTS`: lists them", "**Notes:**", "- # a note"} {
		if !strings.Contains(entries[0].text, want) {
			t.Errorf("the record lost %q:\n%s", want, entries[0].text)
		}
	}
	if _, err := askRecordByID(root, "r-20260101-000000-abcd"); err == nil {
		t.Fatal("a line in an answer made a record of its own")
	}

	// A file written by hand: a plain markdown heading is part of the
	// record it is in, not the end of it.
	path := filepath.Join(askStoreDir(root), askHistoryName)
	writeFile(t, path, "## 2026-11-02T11:39:12+03:00 · - · r-20261102-113912-3f2a\n\n**Asked:** x\n\n**Answer:** Here:\n## Details\nmore\n\n**Steps:**\n- `cpb SHOW ENVS`: lists\n")
	if e := mustAskHistory(t, root); len(e) != 1 || !strings.Contains(e[0].text, "## Details\nmore") || !strings.Contains(e[0].text, "`cpb SHOW ENVS`") {
		t.Fatalf("a hand-written heading cut the record: %+v", e)
	}
}

// A write cut off before its last newline leaves that record short, and
// the next append still starts a record that reads back.
func TestAskRecordAppendAfterTear(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CPB_PLAYBOOKS_DIR", root)
	config.PlaybooksDir = ""
	p := &askPlan{Answer: "ok"}
	first, err := appendAskRecord(root, "", "first", p)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(askStoreDir(root), askHistoryName)
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("## 2026-11-02T11:39:12+03:00 · - · r-20261102-113912-3f2a\n\n**Asked:** torn\n\n**Answ")
	f.Close()
	next, err := appendAskRecord(root, "", "after the tear", p)
	if err != nil {
		t.Fatal(err)
	}
	r, err := askRecordByID(root, next)
	if err != nil || r.Asked != "after the tear" {
		t.Fatalf("the record after a tear: %+v %v", r, err)
	}
	if e := mustAskHistory(t, root); len(e) != 3 || e[0].ID != first || e[1].Asked != "torn" || e[2].ID != next {
		t.Fatalf("records: %+v", e)
	}
}

// A link at .ask itself is refused like a link at a file in it: nothing is
// read through it, and nothing is written through it.
func TestAskStoreDirLinkRefused(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CPB_PLAYBOOKS_DIR", root)
	config.PlaybooksDir = ""
	elsewhere := t.TempDir()
	if _, err := appendAskRecord(elsewhere, "", "outside", &askPlan{Answer: "ok"}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(askStoreDir(elsewhere), askStandingName), "outside context\n")
	if err := os.Symlink(askStoreDir(elsewhere), askStoreDir(root)); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(askStoreDir(elsewhere), askHistoryName))
	if _, err := readAskHistory(root); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("history through a linked .ask: %v", err)
	}
	if _, err := askStanding(root); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("standing context through a linked .ask: %v", err)
	}
	if _, err := askQuery("SHOW ASK HISTORY"); err == nil {
		t.Fatal("the tool read through a linked .ask")
	}
	if _, err := appendAskRecord(root, "", "inside", &askPlan{Answer: "ok"}); err == nil {
		t.Fatal("a record was written through a linked .ask")
	}
	if after, _ := os.ReadFile(filepath.Join(askStoreDir(elsewhere), askHistoryName)); string(after) != string(before) {
		t.Fatal("the linked directory's history changed")
	}
}

// A standing context past 8 KB is cut on a letter's boundary.
func TestAskStandingCutsOnALetter(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(askStoreDir(root), askStandingName), "a"+strings.Repeat("ş", askStandingLimit))
	text, err := askStanding(root)
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(text) || !strings.HasSuffix(text, "[cut: the file is longer than 8 KB]") {
		t.Fatalf("cut: valid %v, tail %q", utf8.ValidString(text), text[len(text)-40:])
	}
}

// With --no-history the tool neither offers SHOW ASK nor runs it.
func TestAskToolNoHistory(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CPB_PLAYBOOKS_DIR", root)
	config.PlaybooksDir = ""
	if _, err := appendAskRecord(root, "", "earlier", &askPlan{Answer: "ok"}); err != nil {
		t.Fatal(err)
	}
	askToolNoHistory = true
	t.Cleanup(func() { askToolNoHistory = false })
	for _, stmt := range []string{"SHOW ASK HISTORY", "SHOW ASK RECORD r-20261102-113912-3f2a"} {
		if _, err := askQuery(stmt); err == nil || !strings.Contains(err.Error(), "--no-history") {
			t.Errorf("%s with --no-history: %v", stmt, err)
		}
	}
	if strings.Contains(askToolDesc(), "SHOW ASK") {
		t.Error("the tool offers SHOW ASK with --no-history")
	}
	if args := askToolArgs("/bin/cpb", root, nil, true); args[len(args)-1] != "--no-history" || args[len(args)-2] != "ask-tool" {
		t.Errorf("tool args: %q", args)
	}
	if args := askToolArgs("/bin/cpb", root, nil, false); args[len(args)-1] != "ask-tool" {
		t.Errorf("tool args: %q", args)
	}
}
