package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The status line history: every replaced or removed status line is kept
// (whole object: padding and refreshInterval too); REVERT STATUSLINE
// restores the newest and pushes the current one, so twice toggles back;
// REFRESH-only changes are not history; with no history PREVIOUS is refused;
// SHOW --json and EXPLAIN read it; SHOW CREATE never writes it.
func TestStatuslineHistoryAndPrevious(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	root := seedFlatPlaybook(t, "k")
	sl := func() map[string]any {
		m, _ := settingsOf(t, root)["statusLine"].(map[string]any)
		return m
	}
	if _, err := quotedStmt(t, "ALTER PLAYBOOK k REVERT STATUSLINE"); err == nil || !strings.Contains(err.Error(), "no earlier status line is recorded") {
		t.Fatalf("PREVIOUS with no history: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "settings.json"), []byte(`{"statusLine":{"type":"command","command":"bash a.sh","padding":2,"refreshInterval":7}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	mustQuoted := func(line string) string {
		t.Helper()
		out, err := quotedStmt(t, line)
		if err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		return out
	}
	mustQuoted("ALTER PLAYBOOK k SET statusline = 'bash b.sh', statusline_refresh = 3")
	mustQuoted("ALTER PLAYBOOK k SET statusline_refresh = 5") // not history
	var v struct {
		History []struct {
			Command string `json:"command"`
			Refresh *int   `json:"refresh"`
		} `json:"statusline_history"`
	}
	if err := json.Unmarshal([]byte(mustStmt(t, "SHOW PLAYBOOK k --json")), &v); err != nil || len(v.History) != 1 || v.History[0].Command != "bash a.sh" || v.History[0].Refresh == nil || *v.History[0].Refresh != 7 {
		t.Fatalf("history after one replacement: %v %+v", err, v)
	}
	if out := mustStmt(t, "EXPLAIN PLAYBOOK k"); !strings.Contains(out, "Status line history: 1 earlier (REVERT STATUSLINE restores bash a.sh)") {
		t.Fatalf("EXPLAIN:\n%s", out)
	}
	// PREVIOUS restores the whole object (padding, interval) ...
	mustQuoted("ALTER PLAYBOOK k REVERT STATUSLINE")
	if m := sl(); m["command"] != "bash a.sh" || m["padding"] != float64(2) || m["refreshInterval"] != float64(7) {
		t.Fatalf("PREVIOUS: %v", m)
	}
	// ... and twice toggles back.
	mustQuoted("ALTER PLAYBOOK k REVERT STATUSLINE")
	if m := sl(); m["command"] != "bash b.sh" || m["refreshInterval"] != float64(5) {
		t.Fatalf("PREVIOUS twice: %v", m)
	}
	// UNSET is history too; PREVIOUS brings it back.
	mustQuoted("ALTER PLAYBOOK k DELETE statusline")
	mustQuoted("ALTER PLAYBOOK k REVERT STATUSLINE")
	if m := sl(); m["command"] != "bash b.sh" {
		t.Fatalf("PREVIOUS after UNSET: %v", m)
	}
	if created := mustStmt(t, "SHOW CREATE PLAYBOOK k"); strings.Contains(created, "PREVIOUS") {
		t.Fatalf("SHOW CREATE wrote history:\n%s", created)
	}
	for line, want := range map[string]string{
		"ALTER PLAYBOOK k REVERT STATUSLINE SET statusline = 'x'": "cannot be combined",
		"ALTER PLAYBOOK k REVERT STATUSLINE DELETE statusline":    "cannot be combined",
	} {
		if _, err := quotedStmt(t, line); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", line, err)
		}
	}
	// The history is cpb's own state, mode 0600, never in the playbook.
	info, err := os.Stat(filepath.Join(filepath.Dir(root), ".state", "statusline-history.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("history file: %v %v", err, info)
	}
}

// At most slHistoryMax entries are kept, newest first.
func TestStatuslineHistoryIsCapped(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	seedFlatPlaybook(t, "k")
	for i := 0; i < slHistoryMax+3; i++ {
		if _, err := quotedStmt(t, "ALTER PLAYBOOK k SET statusline = 'echo "+string(rune('a'+i))+"'"); err != nil {
			t.Fatal(err)
		}
	}
	var v struct {
		History []struct {
			Command string `json:"command"`
		} `json:"statusline_history"`
	}
	_ = json.Unmarshal([]byte(mustStmt(t, "SHOW PLAYBOOK k --json")), &v)
	if len(v.History) != slHistoryMax || v.History[0].Command != "echo "+string(rune('a'+slHistoryMax+1)) {
		t.Fatalf("capped history: %+v", v.History)
	}
}

// A dry run judges PREVIOUS from the history earlier statements would
// leave, and writes nothing.
func TestStatuslineHistoryDryRun(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	seedFlatPlaybook(t, "k")
	mustStmt(t, "CREATE ENV noop SET A=1") // something unrelated on disk
	f := writePlaybookFile(t, "ALTER PLAYBOOK k SET statusline = 'echo a';\nALTER PLAYBOOK k SET statusline = 'echo b';\nALTER PLAYBOOK k REVERT STATUSLINE;\n")
	if out, err := apply(t, f, "--dry-run"); err != nil || !strings.Contains(out, "3 changed") {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".claude-playbooks", ".state", "statusline-history.json")); !os.IsNotExist(err) {
		t.Fatalf("the dry run wrote history: %v", err)
	}
	f = writePlaybookFile(t, "ALTER PLAYBOOK k REVERT STATUSLINE;\n")
	if _, err := apply(t, f, "--dry-run"); err == nil {
		t.Fatal("dry run PREVIOUS with no history was accepted")
	}
}

// A plain config directory keeps its history too (TO '<dir>').
func TestStatuslineHistoryOnDir(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	cfg := t.TempDir()
	if err := os.WriteFile(filepath.Join(cfg, "settings.json"), []byte(`{"statusLine":{"type":"command","command":"echo old"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"ALTER PLAYBOOK SET statusline = 'echo new';\n", "ALTER PLAYBOOK REVERT STATUSLINE;\n"} {
		var err error
		captureStderr(t, func() { _, err = apply(t, writePlaybookFile(t, text), "TO", cfg, "--yes") })
		if err != nil {
			t.Fatalf("%s: %v", text, err)
		}
	}
	data, _ := os.ReadFile(filepath.Join(cfg, "settings.json"))
	if !strings.Contains(string(data), `"echo old"`) {
		t.Fatalf("PREVIOUS on a plain directory:\n%s", data)
	}
}
