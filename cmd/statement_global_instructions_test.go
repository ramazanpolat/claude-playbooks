package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
)

func excludesOf(t *testing.T, dir string) []string {
	t.Helper()
	var s struct {
		Excludes []string `json:"claudeMdExcludes"`
	}
	data, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	return s.Excludes
}

// The global_instructions property: a new playbook keeps ~/.claude's CLAUDE.md and rules
// out (one absolute claudeMdExcludes entry in its settings.json); SET and
// RESET move it and keep every other entry; SHOW,
// EXPLAIN, SELECT and SHOW CREATE read it back from the file.
func TestGlobalInstructionsSetting(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	home, _ := os.UserHomeDir()
	entry := filepath.Join(home, ".claude") + "/**"
	root := config.ResolvePlaybooksDir()

	mustStmt(t, "CREATE PLAYBOOK m SET launcher = ''")
	dir := filepath.Join(root, "m")
	if got := excludesOf(t, dir); !slices.Equal(got, []string{entry}) {
		t.Fatalf("a new playbook: claudeMdExcludes %q, want [%s]", got, entry)
	}
	var v struct {
		Login              string `json:"login"`
		GlobalInstructions string `json:"global_instructions"`
	}
	if err := json.Unmarshal([]byte(mustStmt(t, "SHOW PLAYBOOK m --json")), &v); err != nil || v.Login != "shared" || v.GlobalInstructions != "excluded" {
		t.Fatalf("SHOW --json: %v %+v", err, v)
	}
	if out := mustStmt(t, "SHOW PLAYBOOK m"); !strings.Contains(out, "excluded (default): ~/.claude's CLAUDE.md and rules are not loaded") {
		t.Fatalf("SHOW:\n%s", out)
	}
	if out := mustStmt(t, "EXPLAIN PLAYBOOK m"); !strings.Contains(out, "Global instructions: excluded") {
		t.Fatalf("EXPLAIN:\n%s", out)
	}
	if err := json.Unmarshal([]byte(mustStmt(t, "EXPLAIN PLAYBOOK m --json")), &v); err != nil || v.GlobalInstructions != "excluded" {
		t.Fatalf("EXPLAIN --json: %v %+v", err, v)
	}
	var rows []map[string]any
	js := mustStmt(t, "SELECT name, login, global_instructions FROM PLAYBOOKS --json")
	if json.Unmarshal([]byte(js), &rows) != nil || len(rows) != 1 || rows[0]["global_instructions"] != "excluded" || rows[0]["login"] != "shared" {
		t.Fatalf("SELECT: %s", js)
	}
	created := mustStmt(t, "SHOW CREATE PLAYBOOK m")
	if !strings.Contains(created, "CREATE OR ALTER PLAYBOOK m\n  SET launcher = '', login = 'shared', global_instructions = 'excluded'\n  RESET sandbox,") {
		t.Fatalf("SHOW CREATE:\n%s", created)
	}
	if out, err := apply(t, writePlaybookFile(t, created)); err != nil || !strings.Contains(out, " 0 created, 0 changed,") {
		t.Fatalf("SHOW CREATE did not re-apply unchanged: %v\n%s", err, out)
	}

	// Other entries, and the rest of settings.json, are the pilot's: kept.
	other := `{"model": "opus", "claudeMdExcludes": ["/opt/notes/**", "` + entry + `"]}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(other), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := mustStmt(t, "ALTER PLAYBOOK m SET global_instructions=inherited"); !strings.Contains(out, "global_instructions inherited") {
		t.Fatalf("SET report:\n%s", out)
	}
	if got := excludesOf(t, dir); !slices.Equal(got, []string{"/opt/notes/**"}) {
		t.Fatalf("after 'inherited': %q", got)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "settings.json")); !strings.Contains(string(data), `"model": "opus"`) {
		t.Fatalf("settings.json lost a key:\n%s", data)
	}
	if out := mustStmt(t, "SHOW CREATE PLAYBOOK m"); !strings.Contains(out, "login = 'shared', global_instructions = 'inherited'\n  RESET sandbox,") {
		t.Fatalf("SHOW CREATE of a shared playbook:\n%s", out)
	}
	mustStmt(t, "ALTER PLAYBOOK m RESET global_instructions")
	if got := excludesOf(t, dir); !slices.Equal(got, []string{"/opt/notes/**", entry}) {
		t.Fatalf("RESET global_instructions (back to 'excluded'): %q", got)
	}
	if out := mustStmt(t, "ALTER PLAYBOOK m SET global_instructions=excluded"); !strings.Contains(out, "unchanged") {
		t.Fatalf("a repeat:\n%s", out)
	}

	// The last entry going removes the key.
	mustStmt(t, "CREATE PLAYBOOK s SET launcher = '' SET global_instructions=inherited")
	if got := excludesOf(t, filepath.Join(root, "s")); got != nil {
		t.Fatalf("SET global_instructions = 'inherited': %q", got)
	}
	mustStmt(t, "ALTER PLAYBOOK s SET global_instructions=excluded")
	mustStmt(t, "ALTER PLAYBOOK s SET global_instructions=inherited")
	if data, _ := os.ReadFile(filepath.Join(root, "s", "settings.json")); strings.Contains(string(data), "claudeMdExcludes") {
		t.Fatalf("an emptied list stays:\n%s", data)
	}

	// Another home's entry, from a copy, excludes nothing here: SHOW says so.
	if err := os.WriteFile(filepath.Join(root, "s", "settings.json"), []byte(`{"claudeMdExcludes": ["/Users/someone/.claude/**"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := mustStmt(t, "SHOW PLAYBOOK s"); !strings.Contains(out, "excludes /Users/someone/.claude/**, another home's") {
		t.Fatalf("SHOW with another home's entry:\n%s", out)
	}

	// Never on the machine's own ~/.claude: there the exclude would hide
	// its own instructions. And a plain directory has no manifest for a login.
	machine := filepath.Join(home, ".claude")
	if err := os.MkdirAll(machine, 0o755); err != nil {
		t.Fatal(err)
	}
	f := writePlaybookFile(t, "ALTER PLAYBOOK SET global_instructions = 'excluded';\n")
	if _, err := apply(t, f, "TO", machine, "--yes"); err == nil || !strings.Contains(err.Error(), "the machine's own Claude Code configuration") {
		t.Fatalf("global_instructions on ~/.claude: %v", err)
	}
	if got := excludesOf(t, machine); got != nil {
		t.Fatalf("a refused global_instructions setting wrote ~/.claude: %q", got)
	}
	// D4: refused before anything in the run is written, RESET included,
	// and by identity: a symlink to ~/.claude is ~/.claude.
	link := filepath.Join(t.TempDir(), "claude-link")
	if err := os.Symlink(machine, link); err != nil {
		t.Fatal(err)
	}
	f2 := writePlaybookFile(t, "ALTER PLAYBOOK SET model = 'first';\nALTER PLAYBOOK RESET global_instructions;\n")
	for _, target := range []string{machine, link} {
		if _, err := apply(t, f2, "TO", target, "--yes"); err == nil || !strings.Contains(err.Error(), "the machine's own Claude Code configuration") {
			t.Fatalf("RESET global_instructions on %s: %v", target, err)
		}
	}
	if _, err := os.Stat(filepath.Join(machine, "settings.json")); !os.IsNotExist(err) {
		t.Fatalf("a refused file wrote ~/.claude's settings.json: %v", err)
	}
}

// A dry run sees the settings.json CREATE writes: SHOW CREATE's own
// output plans as one CREATE and nothing else changed.
func TestGlobalInstructionsSettingDryRun(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	f := writePlaybookFile(t, "CREATE PLAYBOOK d SET launcher = '';\nALTER PLAYBOOK d SET global_instructions = 'excluded';\n")
	out, err := apply(t, f, "--dry-run")
	if err != nil || !strings.Contains(out, "1 created, 0 changed, 1 unchanged") {
		t.Fatalf("dry run:\n%v\n%s", err, out)
	}
}
