package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
		"SLACK_WEBHOOK":           "may hold a credential",
		"DATABASE_DSN":            "may hold a credential",
		"PW_SALT":                 "may hold a credential",
		"SIGNING_CERT":            "may hold a credential",
		"USER":                    "says who or where someone is",
		"user":                    "says who or where someone is",
		"LOGNAME":                 "says who or where someone is",
		"USERNAME":                "says who or where someone is",
		"HOSTNAME":                "says who or where someone is",
		"HOST":                    "says who or where someone is",
		"GIT_AUTHOR_NAME":         "says who or where someone is",
		"GIT_AUTHOR_EMAIL":        "says who or where someone is",
		"GIT_COMMITTER_NAME":      "says who or where someone is",
		"GIT_COMMITTER_EMAIL":     "says who or where someone is",
		"GITHUB_ACTOR":            "says who or where someone is",
		"AKIAABCDEFGHIJKLMNOP":    "shaped like a credential",
	} {
		if p := labelVarNameProblem(n); !strings.Contains(p, want) {
			t.Errorf("%q: %q, want %q", n, p, want)
		}
	}
	for n, length := range map[string]string{"sk-ant-api03-xyz": "16 characters", "AKIAABCDEFGHIJKLMNOP": "20 characters"} {
		if p := labelVarNameProblem(n); strings.Contains(p, n[:6]) || !strings.Contains(p, length) {
			t.Fatalf("an entry that is not a name is echoed: %q", p)
		}
		if got := labelVarShown(n); got != "<an entry of "+length+">" {
			t.Fatalf("shown: %q", got)
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
// same key wins; an unset or empty variable, a refused value (by kind), a
// label over the caps and a name no statement would set are left out. The
// states name keys and kinds, never a value.
func TestLabelVarsAt(t *testing.T) {
	env := map[string]string{"TERM_PROGRAM": "kitty", "OWNER": "someone@example.com", "MULTI": "a\nb", "TASK": "from env", "BLANK": "",
		"HOOK_URL_X": "https://example.com/x", "MY_HOME": "/home/user", "MY_DIR": "~/work", "CONN": "10.0.0.1 52344 10.0.0.2 22", "V6": "[2001:db8::1]:22"}
	lookup := func(n string) (string, bool) { v, ok := env[n]; return v, ok }
	got, states := labelVarsAt(map[string]string{"env.task": "typed"}, []string{"TERM_PROGRAM", "OWNER", "MULTI", "MISSING", "TASK", "GITHUB_TOKEN",
		"term_program", "BLANK", "sk-ant-not-a-name", "HOOK_URL_X", "MY_HOME", "MY_DIR", "CONN", "V6"}, lookup)
	if len(got) != 2 || got["env.term_program"] != "kitty" || got["env.task"] != "typed" {
		t.Fatalf("labels: %v", got)
	}
	want := map[string]string{"TERM_PROGRAM": "copied", "OWNER": "refused value/an e-mail address", "MULTI": "refused value/not plain visible text",
		"MISSING": "unset", "TASK": "typed", "GITHUB_TOKEN": "refused name", "BLANK": "empty", "<an entry of 17 characters>": "refused name",
		"HOOK_URL_X": "refused value/a URL", "MY_HOME": "refused value/a path", "MY_DIR": "refused value/a path", "CONN": "refused value/an address", "V6": "refused value/an address"}
	if len(states) != len(want) {
		t.Fatalf("states: %+v", states)
	}
	for _, st := range states {
		w := st.State
		if st.Why != "" {
			w += "/" + st.Why
		}
		if want[st.Name] != w {
			t.Errorf("%s: %s, want %s", st.Name, w, want[st.Name])
		}
	}
	b, _ := json.Marshal(states)
	line := labelVarsLeftOut(states)
	for _, v := range []string{"someone@", "kitty", "a\nb", "sk-ant", "example.com", "/home/user", "10.0.0", "2001:db8"} {
		if strings.Contains(string(b), v) || strings.Contains(line, v) {
			t.Fatalf("a value shown: %s\n%s", b, line)
		}
	}
	// The launch line: refused names and values, never an unset or empty
	// variable, which is normal.
	if strings.Contains(line, "missing") || strings.Contains(line, "blank") || !strings.Contains(line, "env.hook_url_x (the value is a URL)") ||
		!strings.Contains(line, "GITHUB_TOKEN (not a name label_vars takes)") {
		t.Fatalf("line: %s", line)
	}
	full := map[string]string{}
	for i := 0; i < labelsPerSource; i++ {
		full[fmt.Sprintf("k%d", i)] = "v"
	}
	got, states = labelVarsAt(full, []string{"TERM_PROGRAM"}, lookup)
	if len(got) != labelsPerSource || len(states) != 1 || states[0].State != "over the caps" || labelVarsLeftOut(states) != "cpb: label_vars left out: env.term_program (over the label caps)" {
		t.Fatalf("over the caps: %d %+v", len(got), states)
	}
	if got, states := labelVarsAt(nil, []string{"MISSING"}, lookup); got != nil || labelVarsLeftOut(states) != "" {
		t.Fatalf("only unset: %v %+v", got, states)
	}
}

// What a value from the environment may not be, that a typed label may:
// a URL, a path, an address. A typed --label keeps the label rules alone.
func TestLabelVarValues(t *testing.T) {
	for v, kind := range map[string]string{
		"https://hooks.example.com/T0/B0/x": "a URL", "postgres://db.example.com/app": "a URL", "ssh://x": "a URL",
		"/home/user": "a path", "~/work": "a path", "~": "a path",
		"10.0.0.1": "an address", "10.0.0.1 52344 10.0.0.2 22": "an address", "at 192.168.1.20:8080": "an address",
		"fe80::1%en0": "an address", "[2001:db8::1]:22": "an address", "::1": "an address",
		"me@example.com": "an e-mail address", "password-ish sk-ant-api03-abcdefghijklmnop": "a credential",
	} {
		if got := labelVarValueProblem("env.x", v); got != kind {
			t.Errorf("%q: %q, want %q", v, got, kind)
		}
	}
	for _, ok := range []string{"kitty", "12:30:45", "v1.2.3", "feature/x", "2.1.322", "main"} {
		if got := labelVarValueProblem("env.x", ok); got != "" {
			t.Errorf("%q refused: %s", ok, got)
		}
	}
	for _, typed := range []string{"home=/home/user", "url=https://example.com/x", "addr=10.0.0.1"} {
		if _, _, err := parseTypedLabel(typed); err != nil {
			t.Errorf("--label %s: %v", typed, err)
		}
	}
}

// DEFAULTS' names first, then the playbook's that DEFAULTS does not list;
// the launch says in one line what it left out.
func TestLaunchLabelsFromTheEnvironment(t *testing.T) {
	resetCommandTestState(t)
	runLabels.set, runLabels.labels = false, nil
	t.Cleanup(func() { runLabels.set, runLabels.labels = false, nil })
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
	if strings.TrimSpace(msg) != "" {
		t.Fatalf("an unset variable said something: %q", msg)
	}
	// Read once per run: a relaunch carries the labels, whatever changed
	// in the list or the environment since.
	if err := writeLabelVarsDefaults(root, []string{"TERM_PROGRAM", "OTHER"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TERM_PROGRAM", "changed")
	t.Setenv("OTHER", "new")
	again, err := launchLabels(root, m, nil)
	if err != nil || len(again) != 3 || again["env.term_program"] != "kitty" || again["env.other"] != "" {
		t.Fatalf("the relaunch's labels: %v %v", again, err)
	}
	runLabels.set, runLabels.labels = false, nil
	// A plain directory takes DEFAULTS' only.
	if names, _ := launchLabelVars(root, nil); strings.Join(names, " ") != "TERM_PROGRAM OTHER" {
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
	t.Setenv("TERM_PROGRAM", "kitty")
	t.Setenv("HC_TERMINAL_ID", "https://example.com/hook")
	os.Unsetenv("GIT_BRANCH")
	var e struct {
		LabelVars []labelVarState `json:"label_vars"`
	}
	if err := json.Unmarshal([]byte(mustStmt(t, "EXPLAIN PLAYBOOK alpha --json")), &e); err != nil || len(e.LabelVars) != 3 ||
		e.LabelVars[0] != (labelVarState{Name: "TERM_PROGRAM", Label: "env.term_program", State: "copied"}) ||
		e.LabelVars[1] != (labelVarState{Name: "HC_TERMINAL_ID", Label: "env.hc_terminal_id", State: "refused value", Why: "a URL"}) ||
		e.LabelVars[2] != (labelVarState{Name: "GIT_BRANCH", Label: "env.git_branch", State: "unset"}) {
		t.Fatalf("EXPLAIN: %+v %v", e, err)
	}
	if out := mustStmt(t, "EXPLAIN PLAYBOOK alpha"); !strings.Contains(out, "Labels from the environment: TERM_PROGRAM as env.term_program, HC_TERMINAL_ID as env.hc_terminal_id (refused value: a URL, left out), GIT_BRANCH as env.git_branch (unset, left out)") ||
		strings.Contains(out, "example.com") || strings.Contains(out, "kitty") {
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

// A hand-edited entry that is not a variable's name is shown by its length
// only, in SHOW PLAYBOOK, SHOW DEFAULTS, EXPLAIN and SELECT: what stands
// where a name goes may be a value.
func TestLabelVarsHandEditedEntryShownByLength(t *testing.T) {
	root := sandboxDefaultRoot(t)
	const pasted = "sk-ant-api03-abcdefghijklmnop"
	writePlaybook(t, root, "alpha", &manifest.Manifest{LabelVars: []string{pasted, "TERM_PROGRAM"}})
	if err := writeLabelVarsDefaults(root, []string{pasted}); err != nil {
		t.Fatal(err)
	}
	shown := "<an entry of 29 characters>"
	for _, stmt := range []string{"SHOW PLAYBOOK alpha", "SHOW PLAYBOOK alpha --json", "SHOW DEFAULTS", "SHOW DEFAULTS --json", "EXPLAIN PLAYBOOK alpha", "EXPLAIN PLAYBOOK alpha --json",
		"SELECT label_vars FROM PLAYBOOKS --json", "SELECT label_vars FROM DEFAULTS --json"} {
		out := mustStmt(t, stmt)
		if strings.Contains(out, "sk-ant") || !strings.Contains(out, shown) {
			t.Errorf("%s:\n%s", stmt, out)
		}
	}
}

// The failover relaunch carries the launch's labels: a list changed in
// DEFAULTS while the session runs does not reach the relaunch's record.
func TestFailoverRelaunchCarriesTheLaunchLabels(t *testing.T) {
	root := sandboxDefaultRoot(t)
	writePlaybook(t, root, "alpha", &manifest.Manifest{Launcher: "al"})
	fakeProcs(t, nil)
	fastFailover(t, 0, time.Minute)
	tokenAccount(t, "acc-a", "tok-a")
	tokenAccount(t, "acc-b", "tok-b")
	if err := writeFailoverDefaults(root, failoverDefaults{Failover: "on", Accounts: []string{"acc-a", "acc-b"}}); err != nil {
		t.Fatal(err)
	}
	if err := writeLabelVarsDefaults(root, []string{"LV_A"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LV_A", "one")
	t.Setenv("LV_B", "two")
	work, _ := filepath.EvalSymlinks(t.TempDir())
	out := failoverStage(t, root, "tok-b")
	// The first claude changes DEFAULTS' list while it runs.
	t.Setenv("FAKE_LV_FILE", labelVarsDefaultsPath(root))
	first := `mkdir -p "$CLAUDE_CONFIG_DIR/projects/$enc" "$CLAUDE_CONFIG_DIR/sessions"`
	stand := strings.Replace(standIn, first, `printf 'names = ["LV_A", "LV_B"]\n' > "$FAKE_LV_FILE"`+"\n"+first, 1)
	if stand == standIn {
		t.Fatal("the stand-in has no first-launch line to hook")
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(out), "claude"), []byte(stand), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(work)
	var err error
	msg := captureStderr(t, func() { err = runRun(nil, []string{"--account", "acc-a", "--label", "name=x", "alpha"}) })
	if err != nil {
		t.Fatalf("%v\n%s", err, msg)
	}
	if names, _ := readLabelVarsDefaults(root); len(names) != 2 {
		t.Fatalf("the list did not change: %v", names)
	}
	rec := standInRecord(t, out)
	if len(rec.Switches) != 1 || len(rec.Labels) != 2 || rec.Labels["env.lv_a"] != "one" || rec.Labels["name"] != "x" {
		t.Fatalf("the relaunch's labels: %v (switches %d)\n%s", rec.Labels, len(rec.Switches), msg)
	}
}
