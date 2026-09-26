package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestToolsStatuslineModel(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	root := seedFlatPlaybook(t, "k")
	orig := `{"permissions": {"allow": ["Read(*)"], "ask": ["Bash(git push*)"]}, "statusLine": {"type": "command", "command": "old", "padding": 1}, "hooks": {}}`
	if err := os.WriteFile(filepath.Join(root, "settings.json"), []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	stmtText := "ALTER PLAYBOOK k ALLOW TOOL Bash(kommander-helper*) DENY TOOL Read(*) SET STATUSLINE ~/bin/status.sh SET MODEL claude-opus-5-5"
	mustStmt(t, stmtText)
	var s struct {
		Permissions map[string][]string `json:"permissions"`
		StatusLine  map[string]any      `json:"statusLine"`
		Model       string              `json:"model"`
		Hooks       map[string]any      `json:"hooks"`
	}
	data, _ := os.ReadFile(filepath.Join(root, "settings.json"))
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	if strings.Join(s.Permissions["allow"], ",") != "Bash(kommander-helper*)" || strings.Join(s.Permissions["deny"], ",") != "Read(*)" ||
		strings.Join(s.Permissions["ask"], ",") != "Bash(git push*)" {
		t.Fatalf("permissions: %v (a rule moves between lists; ask is kept)", s.Permissions)
	}
	if s.StatusLine["command"] != "~/bin/status.sh" || s.StatusLine["padding"] != float64(1) || s.Model != "claude-opus-5-5" || s.Hooks == nil {
		t.Fatalf("settings: %s", data)
	}
	if out := mustStmt(t, stmtText); !strings.Contains(out, "unchanged") {
		t.Fatalf("a repeat changed something:\n%s", out)
	}
	create := mustStmt(t, "SHOW CREATE PLAYBOOK k")
	for _, want := range []string{"ALLOW TOOL 'Bash(kommander-helper*)'", "DENY TOOL 'Read(*)'", "SET STATUSLINE '~/bin/status.sh'", "SET MODEL 'claude-opus-5-5'"} {
		if !strings.Contains(create, want) {
			t.Errorf("SHOW CREATE missing %q:\n%s", want, create)
		}
	}
	// ANTHROPIC_MODEL from a layer wins over the settings model, and EXPLAIN says so.
	mustStmt(t, "ALTER PLAYBOOK k SET VAR ANTHROPIC_MODEL=glm-5.3")
	if out := mustStmt(t, "EXPLAIN PLAYBOOK k"); !strings.Contains(out, "Model: glm-5.3 (ANTHROPIC_MODEL); the settings model claude-opus-5-5 is overridden") ||
		!strings.Contains(out, "Tools: allow Bash(kommander-helper*); deny Read(*)") {
		t.Fatalf("EXPLAIN:\n%s", out)
	}
	mustStmt(t, "ALTER PLAYBOOK k UNSET TOOL Read(*) UNSET STATUSLINE UNSET MODEL")
	data, _ = os.ReadFile(filepath.Join(root, "settings.json"))
	if strings.Contains(string(data), "Read(*)") || strings.Contains(string(data), "statusLine") || strings.Contains(string(data), `"model"`) {
		t.Fatalf("after UNSET: %s", data)
	}
}

// A dry run carries settings changes from statement to statement.
func TestSettingsDryRunCarries(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	seedFlatPlaybook(t, "k")
	path := writePlaybookFile(t, "ALTER PLAYBOOK k SET MODEL 'a';\nALTER PLAYBOOK k UNSET MODEL;\nALTER PLAYBOOK k ALLOW TOOL 'x';\nALTER PLAYBOOK k UNSET TOOL 'x';\n")
	out, err := apply(t, path, "--dry-run")
	if err != nil || !strings.Contains(out, "0 created, 4 changed, 0 unchanged") {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
}

// An ANTHROPIC_MODEL set by reference still decides the model; EXPLAIN says
// so without the value.
func TestExplainModelByReference(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	helper, _ := fakeHelper(t)
	seedFlatPlaybook(t, "k")
	mustStmt(t, "ALTER DEFAULTS SET SECRET HELPER "+helper)
	mustStmt(t, "ALTER PLAYBOOK k SET MODEL claude-opus-5-5 SET VAR ANTHROPIC_MODEL FROM keychain:ok/model")
	if out := mustStmt(t, "EXPLAIN PLAYBOOK k"); !strings.Contains(out, "Model: (by reference) (ANTHROPIC_MODEL); the settings model claude-opus-5-5 is overridden") {
		t.Fatalf("EXPLAIN:\n%s", out)
	}
}
