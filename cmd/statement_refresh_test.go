package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// REFRESH writes statusLine.refreshInterval; SET STATUSLINE without REFRESH
// keeps it (as it keeps padding); SET STATUSLINE REFRESH alone needs a
// status line; UNSET STATUSLINE REFRESH removes only the interval. SHOW,
// EXPLAIN, SHOW CREATE (round trip) and SELECT show it.
func TestStatuslineRefresh(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	root := seedFlatPlaybook(t, "k")
	sl := func() map[string]any {
		m, _ := settingsOf(t, root)["statusLine"].(map[string]any)
		return m
	}
	if _, err := quotedStmt(t, "ALTER PLAYBOOK k SET STATUSLINE REFRESH 5"); err == nil || !strings.Contains(err.Error(), "needs a status line") {
		t.Fatalf("an interval without a command: %v", err)
	}
	if _, err := quotedStmt(t, "ALTER PLAYBOOK k SET STATUSLINE 'bash sl.sh' REFRESH 10"); err != nil {
		t.Fatal(err)
	}
	if m := sl(); m["command"] != "bash sl.sh" || m["refreshInterval"] != float64(10) {
		t.Fatalf("SET … REFRESH: %v", m)
	}
	if out, err := quotedStmt(t, "ALTER PLAYBOOK k SET STATUSLINE 'bash sl.sh' REFRESH 10"); err != nil || !strings.Contains(out, "unchanged") {
		t.Fatalf("a repeat changed something: %v\n%s", err, out)
	}
	// A new command without REFRESH keeps the interval, and padding.
	data := `{"statusLine": {"type": "command", "command": "bash sl.sh", "padding": 2, "refreshInterval": 10}}`
	if err := os.WriteFile(filepath.Join(root, "settings.json"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := quotedStmt(t, "ALTER PLAYBOOK k SET STATUSLINE 'bash other.sh'"); err != nil {
		t.Fatal(err)
	}
	if m := sl(); m["command"] != "bash other.sh" || m["refreshInterval"] != float64(10) || m["padding"] != float64(2) {
		t.Fatalf("SET without REFRESH: %v", m)
	}
	var v struct {
		Statusline        *string `json:"statusline"`
		StatuslineRefresh *int    `json:"statusline_refresh"`
	}
	if err := json.Unmarshal([]byte(mustStmt(t, "SHOW PLAYBOOK k --json")), &v); err != nil || v.Statusline == nil || *v.Statusline != "bash other.sh" ||
		v.StatuslineRefresh == nil || *v.StatuslineRefresh != 10 {
		t.Fatalf("SHOW --json: %v %+v", err, v)
	}
	if out := mustStmt(t, "EXPLAIN PLAYBOOK k"); !strings.Contains(out, "Status line: bash other.sh (refreshes every 10 s)") {
		t.Fatalf("EXPLAIN:\n%s", out)
	}
	created := mustStmt(t, "SHOW CREATE PLAYBOOK k")
	if !strings.Contains(created, "SET STATUSLINE 'bash other.sh' REFRESH 10") {
		t.Fatalf("SHOW CREATE:\n%s", created)
	}
	if out, err := apply(t, writePlaybookFile(t, created)); err != nil || !strings.Contains(out, " 0 created, 0 changed,") {
		t.Fatalf("SHOW CREATE did not re-apply unchanged: %v\n%s", err, out)
	}
	var rows []map[string]any
	js := mustStmt(t, "SELECT name, statusline_refresh FROM PLAYBOOKS --json")
	if json.Unmarshal([]byte(js), &rows) != nil || len(rows) != 1 || rows[0]["statusline_refresh"] != float64(10) {
		t.Fatalf("SELECT: %s", js)
	}
	if _, err := quotedStmt(t, "ALTER PLAYBOOK k SET STATUSLINE REFRESH 30"); err != nil {
		t.Fatal(err)
	}
	if m := sl(); m["refreshInterval"] != float64(30) || m["command"] != "bash other.sh" {
		t.Fatalf("REFRESH alone: %v", m)
	}
	if _, err := quotedStmt(t, "ALTER PLAYBOOK k UNSET STATUSLINE REFRESH"); err != nil {
		t.Fatal(err)
	}
	if m := sl(); m["refreshInterval"] != nil || m["command"] != "bash other.sh" || m["padding"] != float64(2) {
		t.Fatalf("UNSET STATUSLINE REFRESH: %v", m)
	}
}

// A plain config directory takes it too.
func TestStatuslineRefreshDirTarget(t *testing.T) {
	sandboxDefaultRoot(t)
	cfg, _ := filepath.EvalSymlinks(t.TempDir())
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	recipe := writeCpb(t, dir, "sl.cpb", "ALTER PLAYBOOK SET STATUSLINE 'bash sl.sh' REFRESH 10;\n")
	if out, err := apply(t, recipe, "TO", cfg, "--yes"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if m, _ := settingsOf(t, cfg)["statusLine"].(map[string]any); m["refreshInterval"] != float64(10) {
		t.Fatalf("dir target: %v", m)
	}
}
