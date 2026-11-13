package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// label_vars takes only exact names, and never one that is cpb's own, an
// account's, or may hold a credential, whatever its value.
func TestLabelVarNames(t *testing.T) {
	for _, ok := range []string{"TERM_PROGRAM", "HC_TERMINAL_ID", "_X", "lower_case", strings.Repeat("A", 28)} {
		if p := labelVarNameProblem(ok); p != "" {
			t.Errorf("%s refused: %s", ok, p)
		}
	}
	for n, want := range map[string]string{
		"1A":                      "exact name",
		"A-B":                     "exact name",
		"TERM_*":                  "exact name",
		"":                        "exact name",
		strings.Repeat("A", 29):   "exact name",
		"CPB_PLAYBOOKS_DIR":       "cpb's own or an account's",
		"CLAUDE_CONFIG_DIR":       "cpb's own or an account's",
		"ANTHROPIC_BASE_URL":      "cpb's own or an account's",
		"anthropic_base_url":      "cpb's own or an account's",
		"CLAUDE_CODE_OAUTH_TOKEN": "cpb's own or an account's",
		"GITHUB_TOKEN":            "may hold a credential",
		"MY_SECRET":               "may hold a credential",
		"DB_PASSWORD":             "may hold a credential",
		"OPENAI_API_KEY":          "may hold a credential",
		"PUBLIC_KEY":              "may hold a credential",
		"KEYBOARD_LAYOUT":         "may hold a credential",
		"MY_COOKIE_JAR":           "may hold a credential",
		"TERM_SESSION_ID":         "may hold a credential",
		"BASIC_AUTH":              "may hold a credential",
		"GITHUBTOKEN":             "may hold a credential",
	} {
		if p := labelVarNameProblem(n); !strings.Contains(p, want) {
			t.Errorf("%q: %q, want %q", n, p, want)
		}
	}
	if err := checkLabelVars([]string{"A", "B", "C", "D", "E", "F", "G", "H"}); err != nil {
		t.Fatalf("8 names: %v", err)
	}
	if err := checkLabelVars([]string{"A", "B", "C", "D", "E", "F", "G", "H", "I"}); err == nil || !strings.Contains(err.Error(), "at most 8") {
		t.Fatalf("9 names: %v", err)
	}
	if err := checkLabelVars([]string{"Term", "TERM"}); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("one label twice: %v", err)
	}
}

// At launch each listed name's value becomes env.<name>: a --label of the
// same key wins; an unset variable, a value a label refuses, a label over
// the caps and a name no statement would set are left out, each noted by
// key, never by value.
func TestWithLabelVars(t *testing.T) {
	env := map[string]string{"TERM_PROGRAM": "kitty", "OWNER": "someone@example.com", "MULTI": "a\nb", "TASK": "from env"}
	lookup := func(n string) (string, bool) { v, ok := env[n]; return v, ok }
	got, notes := withLabelVars(map[string]string{"env.task": "typed"}, []string{"TERM_PROGRAM", "OWNER", "MULTI", "MISSING", "TASK", "GITHUB_TOKEN", "term_program"}, lookup)
	if len(got) != 2 || got["env.term_program"] != "kitty" || got["env.task"] != "typed" {
		t.Fatalf("labels: %v", got)
	}
	line := strings.Join(notes, ", ")
	for _, want := range []string{"env.owner (the value looks like an e-mail address", "env.multi (the value holds a control", "env.missing (unset)", "github_token (not a name label_vars takes)"} {
		if !strings.Contains(line, want) {
			t.Errorf("notes lack %q: %s", want, line)
		}
	}
	if strings.Contains(line, "someone@") || strings.Contains(line, "kitty") || strings.Contains(line, "a\nb") {
		t.Fatalf("a note shows a value: %s", line)
	}
	full := map[string]string{}
	for i := 0; i < labelsPerSource; i++ {
		full[fmt.Sprintf("k%d", i)] = "v"
	}
	got, notes = withLabelVars(full, []string{"TERM_PROGRAM"}, lookup)
	if len(got) != labelsPerSource || len(notes) != 1 || notes[0] != "env.term_program (over the label caps)" {
		t.Fatalf("over the caps: %d %v", len(got), notes)
	}
	if got, notes := withLabelVars(nil, nil, lookup); got != nil || notes != nil {
		t.Fatalf("none: %v %v", got, notes)
	}
}

// DEFAULTS' names first, then the playbook's that DEFAULTS does not list;
// the launch says in one line what it left out.
func TestLaunchLabelsFromTheEnvironment(t *testing.T) {
	resetCommandTestState(t)
	root := t.TempDir()
	config.PlaybooksDir = root
	if err := writeLabelVarsDefaults(root, []string{"TERM_PROGRAM", "MISSING_ONE"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TERM_PROGRAM", "kitty")
	t.Setenv("HC_TERMINAL_ID", "t7")
	os.Unsetenv("MISSING_ONE")
	m := &manifest.Manifest{LabelVars: []string{"term_program", "HC_TERMINAL_ID"}}
	if names, err := launchLabelVars(root, m); err != nil || strings.Join(names, " ") != "TERM_PROGRAM MISSING_ONE HC_TERMINAL_ID" {
		t.Fatalf("names: %v %v", names, err)
	}
	var labels map[string]string
	var err error
	msg := captureStderr(t, func() {
		labels, err = launchLabels(root, m, []launchToken{{flag: "--label", value: "name=x"}})
	})
	if err != nil || len(labels) != 3 || labels["env.term_program"] != "kitty" || labels["env.hc_terminal_id"] != "t7" || labels["name"] != "x" {
		t.Fatalf("labels: %v %v", labels, err)
	}
	if strings.TrimSpace(msg) != "cpb: label_vars left out: env.missing_one (unset)" {
		t.Fatalf("stderr: %q", msg)
	}
	// A plain directory takes DEFAULTS' only.
	if names, _ := launchLabelVars(root, nil); strings.Join(names, " ") != "TERM_PROGRAM MISSING_ONE" {
		t.Fatalf("a directory: %v", names)
	}
}

// The statements: DEFAULTS' and a playbook's label_vars are set, shown,
// written back by SHOW CREATE, and RESET; a refused name changes nothing.
func TestLabelVarsStatements(t *testing.T) {
	root := sandboxDefaultRoot(t)
	writePlaybook(t, root, "alpha", nil)

	mustStmt(t, "ALTER DEFAULTS SET label_vars = ['TERM_PROGRAM', 'HC_TERMINAL_ID']")
	if fi, err := os.Stat(labelVarsDefaultsPath(root)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the DEFAULTS file: %v %v", fi, err)
	}
	var d struct {
		LabelVars []string `json:"label_vars"`
	}
	if err := json.Unmarshal([]byte(mustStmt(t, "SHOW DEFAULTS --json")), &d); err != nil || strings.Join(d.LabelVars, " ") != "TERM_PROGRAM HC_TERMINAL_ID" {
		t.Fatalf("SHOW DEFAULTS: %v %v", d, err)
	}
	if err := stmtErr(t, "ALTER DEFAULTS SET label_vars = ['GITHUB_TOKEN']"); err == nil || !strings.Contains(err.Error(), "may hold a credential") {
		t.Fatalf("a credential name: %v", err)
	}
	if names, _ := readLabelVarsDefaults(root); len(names) != 2 {
		t.Fatalf("a refused SET changed DEFAULTS: %v", names)
	}

	mustStmt(t, "ALTER PLAYBOOK alpha SET label_vars = ['GIT_BRANCH']")
	if m, err := manifest.Read(filepath.Join(root, "alpha")); err != nil || strings.Join(m.LabelVars, " ") != "GIT_BRANCH" {
		t.Fatalf("the manifest: %+v %v", m, err)
	}
	var p struct {
		LabelVars []string `json:"label_vars"`
	}
	if err := json.Unmarshal([]byte(mustStmt(t, "SHOW PLAYBOOK alpha --json")), &p); err != nil || strings.Join(p.LabelVars, " ") != "GIT_BRANCH" {
		t.Fatalf("SHOW PLAYBOOK: %v %v", p, err)
	}
	var e struct {
		LabelVars []string `json:"label_vars"`
	}
	if err := json.Unmarshal([]byte(mustStmt(t, "EXPLAIN PLAYBOOK alpha --json")), &e); err != nil || strings.Join(e.LabelVars, " ") != "TERM_PROGRAM HC_TERMINAL_ID GIT_BRANCH" {
		t.Fatalf("EXPLAIN: %v %v", e, err)
	}
	if out := mustStmt(t, "EXPLAIN PLAYBOOK alpha"); !strings.Contains(out, "Labels from the environment: TERM_PROGRAM as env.term_program, HC_TERMINAL_ID as env.hc_terminal_id, GIT_BRANCH as env.git_branch") {
		t.Fatalf("EXPLAIN:\n%s", out)
	}
	if out := mustStmt(t, "SELECT name, label_vars FROM PLAYBOOKS --json"); !strings.Contains(strings.Join(strings.Fields(out), ""), `"label_vars":["GIT_BRANCH"]`) {
		t.Fatalf("SELECT:\n%s", out)
	}

	// SHOW CREATE writes both back; applied again, nothing changes.
	created := mustStmt(t, "SHOW CREATE ALL")
	for _, want := range []string{"label_vars = ['TERM_PROGRAM', 'HC_TERMINAL_ID']", "label_vars = ['GIT_BRANCH']"} {
		if !strings.Contains(created, want) {
			t.Fatalf("SHOW CREATE lacks %q:\n%s", want, created)
		}
	}
	if out, err := apply(t, writePlaybookFile(t, created)); err != nil || !strings.Contains(out, " 0 created, 0 changed,") {
		t.Fatalf("SHOW CREATE did not re-apply unchanged: %v\n%s", err, out)
	}

	mustStmt(t, "ALTER PLAYBOOK alpha RESET label_vars")
	mustStmt(t, "ALTER DEFAULTS RESET label_vars")
	if m, _ := manifest.Read(filepath.Join(root, "alpha")); len(m.LabelVars) != 0 {
		t.Fatalf("RESET kept %v", m.LabelVars)
	}
	if _, err := os.Stat(labelVarsDefaultsPath(root)); !os.IsNotExist(err) {
		t.Fatalf("RESET kept the DEFAULTS file: %v", err)
	}
	if out := mustStmt(t, "SHOW CREATE PLAYBOOK alpha"); !strings.Contains(out, "failover, label_vars\n") {
		t.Fatalf("SHOW CREATE does not RESET label_vars:\n%s", out)
	}
}

// An install never takes a source's label_vars.
func TestInstallDropsLabelVars(t *testing.T) {
	resetCommandTestState(t)
	base := t.TempDir()
	config.PlaybooksDir = filepath.Join(base, "playbooks")
	source := filepath.Join(base, "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Write(source, &manifest.Manifest{Name: "pb", Version: "1", LabelVars: []string{"HOME"}}); err != nil {
		t.Fatal(err)
	}
	var err error
	msg := captureStderr(t, func() { err = doInstall(installOpts{name: "pb", noLauncher: true}, []string{source}) })
	if err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Read(filepath.Join(config.PlaybooksDir, "pb"))
	if err != nil || m == nil || len(m.LabelVars) != 0 || !strings.Contains(msg, "ignoring label_vars") {
		t.Fatalf("installed: %+v %v %q", m, err, msg)
	}
}

// An update keeps the installed label_vars and never takes the source's.
func TestUpdateKeepsTheInstalledLabelVars(t *testing.T) {
	installed, _ := migrateFixture(t, false)
	source := filepath.Join(filepath.Dir(config.PlaybooksDir), "source")
	if err := manifest.Write(source, &manifest.Manifest{Version: "2.0.0", LabelVars: []string{"HOME"}}); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Write(installed, &manifest.Manifest{Version: "1.0.0", Source: &manifest.Source{Repository: source}, LabelVars: []string{"TERM_PROGRAM"}}); err != nil {
		t.Fatal(err)
	}
	if err := runPlaybookUpdate(io.Discard, "pb", updateOpts{yes: true}); err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Read(installed)
	if err != nil || m.Version != "2.0.0" || strings.Join(m.LabelVars, " ") != "TERM_PROGRAM" {
		t.Fatalf("after the update: %+v %v", m, err)
	}
}
