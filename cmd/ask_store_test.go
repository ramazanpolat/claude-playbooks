package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// --no-history neither reads the history nor adds to it.
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

// The tool's HISTORY statements: index lines newest first, by playbook,
// before an id, a bounded limit, and one record in full. A torn last
// append is skipped.
func TestAskHistoryQuery(t *testing.T) {
	root := t.TempDir()
	plan := func(cmd string) *askPlan {
		return &askPlan{Answer: "ok", Steps: []askStep{{Command: cmd, Status: stepOK, Why: "does it"}}}
	}
	var ids []string
	for i, tag := range []string{"", "kommander", "", "kommander"} {
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

	index := func(words ...string) []askEntry {
		t.Helper()
		text, err := askHistoryQuery(root, words)
		if err != nil {
			t.Fatalf("HISTORY %v: %v", words, err)
		}
		var got struct{ Records []askEntry }
		if err := json.Unmarshal([]byte(text), &got); err != nil {
			t.Fatal(err)
		}
		return got.Records
	}
	if got := index(); len(got) != 4 || got[0].ID != ids[3] || got[0].Asked != "ask d" || got[3].Tag != "-" {
		t.Fatalf("HISTORY: %+v", got)
	}
	if got := index("FOR", "kommander"); len(got) != 2 || got[0].ID != ids[3] || got[1].ID != ids[1] {
		t.Fatalf("HISTORY FOR kommander: %+v", got)
	}
	if got := index("BEFORE", ids[2], "LIMIT", "1"); len(got) != 1 || got[0].ID != ids[1] {
		t.Fatalf("HISTORY BEFORE LIMIT: %+v", got)
	}
	text, err := askHistoryQuery(root, []string{"RECORD", ids[1]})
	if err != nil || !strings.Contains(text, "**Asked:** ask b") || !strings.Contains(text, "`cpb SHOW ENVS`: does it") {
		t.Fatalf("HISTORY RECORD: %v\n%s", err, text)
	}
	for _, bad := range [][]string{{"RECORD", "r-00000000-000000-0000"}, {"BEFORE", "nope"}, {"LIMIT", "0"}, {"SINCE", "x"}} {
		if _, err := askHistoryQuery(root, bad); err == nil {
			t.Errorf("HISTORY %v: accepted", bad)
		}
	}
	// Over the tool, as Claude Code calls it.
	t.Setenv("CPB_PLAYBOOKS_DIR", root)
	config.PlaybooksDir = ""
	if text, err := askQuery("HISTORY LIMIT 1"); err != nil || !strings.Contains(text, ids[3]) {
		t.Fatalf("the tool's HISTORY: %v %s", err, text)
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
