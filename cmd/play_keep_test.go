package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/launcher"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// playKeepFlags sets slice 4's flags for one call, and resets them after.
func playKeepFlags(t *testing.T, keep bool, as string) {
	t.Helper()
	playKeep, playAs = keep, as
	t.Cleanup(func() { playKeep, playAs = false, "" })
}

func showPlaybook(t *testing.T, name string) map[string]any {
	t.Helper()
	var v map[string]any
	out := mustStmt(t, "SHOW PLAYBOOK "+name+" --json")
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return v
}

func playbookVars(t *testing.T, name string) map[string]string {
	t.Helper()
	vars := map[string]string{}
	for _, x := range showPlaybook(t, name)["vars"].([]any) {
		m := x.(map[string]any)
		vars[m["key"].(string)], _ = m["value"].(string)
		if b, _ := m["blocked"].(bool); b {
			vars[m["key"].(string)] = "(blocked)"
		}
	}
	return vars
}

const keepV1 = "-- title: Keeper\n-- description: Kept.\n\nALTER PLAYBOOK\n  SET VAR FOO=1 EDITOR=vim\n  SET model = 'claude-opus-5-5';\n"
const keepV2 = "-- title: Keeper\n-- description: Kept, v2.\n\nALTER PLAYBOOK\n  SET VAR BAR=2 EDITOR=vim\n  SET model = 'claude-sonnet-5';\n"

// --keep: the preview and the yes, then a playbook in the pilot's store with
// a launcher, the exact bytes and the [play]
// record; no session. --update: the same bytes change nothing; new ones
// need the yes again, show the diff, and undo what the old recipe set.
func TestPlayKeepAndUpdate(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	t.Setenv("TMPDIR", t.TempDir())
	log := stubClaude(t)
	dir := t.TempDir()
	p := writeRecipe(t, dir, "keeper.cpb", keepV1)
	playFlags(t, false, false, false, "")
	playKeepFlags(t, true, "")

	// No terminal, no --yes: refused, nothing kept.
	playRunFlags(t, false, nil, nil, nil)
	var err error
	captureStdout(t, func() { err = runPlay(playCmd, []string{p}) })
	if err == nil || !strings.Contains(err.Error(), "confirm with --yes") || strings.Contains(mustStmt(t, "SHOW PLAYBOOKS --json"), `"keeper"`) {
		t.Fatalf("no --yes: %v", err)
	}

	playRunFlags(t, true, nil, nil, nil)
	out := captureStdout(t, func() { err = runPlay(playCmd, []string{p}) })
	if err != nil || !strings.Contains(out, "Kept as keeper") || !strings.Contains(out, "Not sandboxed") {
		t.Fatalf("--keep: %v\n%s", err, out)
	}
	if args, _ := stubLog(t, log); args != "" {
		t.Fatalf("--keep ran a session: %q", args)
	}
	v := showPlaybook(t, "keeper")
	rec, _ := v["play"].(map[string]any)
	if rec == nil || rec["ref"] != canon(t, p) && rec["ref"] != p || len(rec["sha256"].(string)) != 64 {
		t.Fatalf("the kept playbook: %v", v)
	}
	if _, exists, foreign := launcher.Lookup(config.LauncherDir, "keeper"); !exists || foreign {
		t.Fatal("no launcher keeper")
	}
	pbDir := v["path"].(string)
	if b, err := os.ReadFile(filepath.Join(pbDir, playRecipeFile)); err != nil || string(b) != keepV1 {
		t.Fatalf("the recorded bytes: %q %v", b, err)
	}
	if vars := playbookVars(t, "keeper"); vars["FOO"] != "1" || vars["EDITOR"] != "vim" {
		t.Fatalf("vars: %v", vars)
	}

	// The record is a SELECT column too.
	js := captureStdout(t, func() { err = runStatement([]string{"SELECT name, play FROM PLAYBOOKS", "--json"}) })
	if err != nil || !strings.Contains(js, `"sha256": "`+rec["sha256"].(string)+`"`) {
		t.Fatalf("SELECT play: %v\n%s", err, js)
	}

	// Kept again under the same name: refused, naming --as and cpb update.
	captureStdout(t, func() { err = runPlay(playCmd, []string{p}) })
	if err == nil || !strings.Contains(err.Error(), "--as") || !strings.Contains(err.Error(), "cpb update keeper") {
		t.Fatalf("a second keep: %v", err)
	}

	// cpb update with the same bytes: unchanged. update dispatches a played
	// playbook to its recipe.
	out = captureStdout(t, func() { err = runUpdate(updateCmd, []string{"keeper"}) })
	if err != nil || !strings.Contains(out, "keeper is unchanged") {
		t.Fatalf("unchanged: %v\n%s", err, out)
	}

	// New bytes: refused without the yes; nothing changes.
	writeRecipe(t, dir, "keeper.cpb", keepV2)
	playRunFlags(t, false, nil, nil, nil)
	out = captureStdout(t, func() { err = playUpdateRun("keeper") })
	if err == nil || !strings.Contains(out, "- ") || !strings.Contains(out, "+ ") {
		t.Fatalf("update without --yes: %v\n%s", err, out)
	}
	if vars := playbookVars(t, "keeper"); vars["FOO"] != "1" {
		t.Fatalf("an unconfirmed update changed the playbook: %v", vars)
	}

	playRunFlags(t, true, nil, nil, nil)
	out = captureStdout(t, func() { err = playUpdateRun("keeper") })
	if err != nil || !strings.Contains(out, "-   SET VAR FOO=1 EDITOR=vim") || !strings.Contains(out, "+   SET VAR BAR=2 EDITOR=vim") || !strings.Contains(out, "Updated keeper") {
		t.Fatalf("update: %v\n%s", err, out)
	}
	vars := playbookVars(t, "keeper")
	if _, ok := vars["FOO"]; ok || vars["BAR"] != "2" || vars["EDITOR"] != "vim" {
		t.Fatalf("after the update: %v", vars)
	}
	v = showPlaybook(t, "keeper")
	if v["model"] != "claude-sonnet-5" {
		t.Fatalf("model: %v", v["model"])
	}
	if b, _ := os.ReadFile(filepath.Join(pbDir, playRecipeFile)); string(b) != keepV2 {
		t.Fatalf("the recorded bytes after the update: %q", b)
	}

	// A playbook neither played nor created FROM a source has nothing to
	// update.
	mustStmt(t, "CREATE PLAYBOOK plain SET launcher = ''")
	if err := runUpdate(updateCmd, []string{"plain"}); err == nil || !strings.Contains(err.Error(), "nothing to update from") {
		t.Fatalf("update of an unplayed playbook: %v", err)
	}
}

// A kept playbook lives in the pilot's store, so DEFAULTS layer into it. When
// the recipe moves the endpoint, their keys are blocked there (named in the
// preview), the login is its own, and an --env-set set's keys are not blocked.
func TestPlayKeepEndpointAndDefaults(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	t.Setenv("TMPDIR", t.TempDir())
	stubClaude(t)
	router := writeRecipe(t, t.TempDir(), "router.cpb", routerRecipe)
	mustStmt(t, "CREATE ENV mine SET EDITOR=vim")
	mustStmt(t, "ALTER DEFAULTS USE ENV mine")
	mustStmt(t, "CREATE ENV routerkey SET ANTHROPIC_AUTH_TOKEN=router-token AS PLAINTEXT")
	playFlags(t, false, false, false, "")
	playKeepFlags(t, true, "")

	// --yes alone does not confirm the endpoint.
	playRunFlags(t, true, nil, nil, nil)
	var err error
	captureStdout(t, func() { err = runPlay(playCmd, []string{router}) })
	if err == nil || !strings.Contains(err.Error(), "--trust-endpoint router.example.net") {
		t.Fatalf("unconfirmed endpoint: %v", err)
	}

	playRunFlags(t, true, []string{"router.example.net"}, nil, []string{"routerkey"})
	out := captureStdout(t, func() { err = runPlay(playCmd, []string{router}) })
	if err != nil || !strings.Contains(out, "Your DEFAULTS (mine) will NOT follow it to router.example.net") {
		t.Fatalf("keep a moved endpoint: %v\n%s", err, out)
	}
	v := showPlaybook(t, "router")
	if v["login"] != "isolated" {
		t.Fatalf("not isolated: %v", v)
	}
	m, err := manifest.Read(v["path"].(string))
	if err != nil {
		t.Fatal(err)
	}
	unset := strings.Join(m.Env.Block, " ")
	if !strings.Contains(unset, "EDITOR") || !strings.Contains(unset, "ANTHROPIC_API_KEY") || strings.Contains(unset, "ANTHROPIC_AUTH_TOKEN") || strings.Contains(unset, "ANTHROPIC_BASE_URL") {
		t.Fatalf("blocked: %q", unset)
	}
	if strings.Join(m.Env.Sets, " ") != "routerkey" {
		t.Fatalf("--env: %v", m.Env.Sets)
	}
}

// --keep --dry-run --json plans against the pilot's store and writes
// nothing; create-with: SANDBOX keeps it sandboxed; usage errors.
func TestPlayKeepDryRunAndSandbox(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	dir := t.TempDir()
	p := writeRecipe(t, dir, "keeper.cpb", keepV1)
	playFlags(t, false, true, true, "")
	playKeepFlags(t, true, "kept")
	var err error
	out := captureStdout(t, func() { err = runPlay(playCmd, []string{p}) })
	var rep struct {
		OK   bool      `json:"ok"`
		Play *playJSON `json:"play"`
	}
	if err != nil || json.Unmarshal([]byte(out), &rep) != nil || !rep.OK || rep.Play == nil || !rep.Play.Keep || rep.Play.Playbook != "kept" {
		t.Fatalf("--keep --dry-run --json: %v\n%s", err, out)
	}
	if strings.Contains(mustStmt(t, "SHOW PLAYBOOKS --json"), `"kept"`) {
		t.Fatal("a dry run kept a playbook")
	}

	boxed := writeRecipe(t, dir, "boxed.cpb", "-- create-with: SANDBOX\n\nALTER PLAYBOOK SET model = 'm';\n")
	refs := writeRecipe(t, dir, "refs.cpb", "-- create-with: SANDBOX\n\nALTER PLAYBOOK SET VAR GH FROM 'keychain:gh';\n")
	playFlags(t, false, false, false, "")
	playKeepFlags(t, true, "")
	playRunFlags(t, true, nil, []string{"keychain:gh"}, nil)
	captureStdout(t, func() { err = runPlay(playCmd, []string{boxed}) })
	if err != nil || showPlaybook(t, "boxed")["sandbox"].(map[string]any)["always"] != true {
		t.Fatalf("create-with SANDBOX: %v", err)
	}
	captureStdout(t, func() { err = runPlay(playCmd, []string{refs}) })
	if err == nil || !strings.Contains(err.Error(), "keep it with --no-sandbox") {
		t.Fatalf("refs, sandboxed: %v", err)
	}

	// Usage.
	for _, c := range []struct {
		keep      bool
		as        string
		check     bool
		args      []string
		wantError string
	}{
		{false, "x", false, []string{p}, "--as names a kept playbook"},
		{true, "", true, []string{p}, "--check runs nothing to keep"},
	} {
		playKeepFlags(t, c.keep, c.as)
		playFlags(t, c.check, false, false, "")
		playCmd.Flags().Parse(c.args)
		if err := playCmd.Args(playCmd, c.args); err == nil || !strings.Contains(err.Error(), c.wantError) {
			t.Errorf("%+v: %v", c, err)
		}
	}
}

// What an update undoes: what the old recipe set and the new one does not
// set the same way; plugins before their marketplace; nothing for clauses
// that stay.
func TestUndoFor(t *testing.T) {
	parse := func(s string) []*grammar.Stmt {
		st, err := grammar.ParseFile(s)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	old := parse("ALTER PLAYBOOK\n  ADD MARKETPLACE m FROM 'github:acme/plugins'\n  ADD PLUGIN p@m\n  SET VAR A=1 B=2\n  ALLOW TOOL 'Read'\n  SET model = 'x';\n")
	if u := undoFor("kb", old, old); u != nil {
		t.Fatalf("the same recipe undoes %s", u.String())
	}
	u := undoFor("kb", old, parse("ALTER PLAYBOOK SET VAR A=1 B=3 SET model = 'x';\n"))
	got := u.String()
	want := "ALTER PLAYBOOK kb DROP PLUGIN p@m UNSET VAR B UNSET TOOL 'Read' DROP MARKETPLACE m"
	if got != want {
		t.Fatalf("undo:\n got %s\nwant %s", got, want)
	}
	if _, err := grammar.ParseFile(u.Pretty() + ";"); err != nil {
		t.Fatalf("the undo does not parse back: %v", err)
	}
}

// Env sets and sandbox settings are undone too: an env set by whether it is
// attached (not where), a sandbox setting by its key, the bare SET SANDBOX
// as always=true.
func TestUndoForEnvAndSandbox(t *testing.T) {
	parse := func(s string) []*grammar.Stmt {
		st, err := grammar.ParseFile(s)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	old := parse("ALTER PLAYBOOK USE ENV a b SET sandbox.always = true;\nALTER PLAYBOOK SET sandbox.backend = 'x', sandbox.workdir = '/w' ADD ENV c FIRST;\n")
	moved := parse("ALTER PLAYBOOK USE ENV b a SET sandbox.always = true;\nALTER PLAYBOOK SET sandbox.backend = 'x' ADD ENV c LAST;\n")
	if u := undoFor("kb", old, moved); u == nil || u.String() != "ALTER PLAYBOOK kb DELETE sandbox.workdir" {
		t.Fatalf("an order change undoes: %v", u)
	}
	u := undoFor("kb", old, parse("ALTER PLAYBOOK SET sandbox.backend = 'y';\n"))
	want := "ALTER PLAYBOOK kb DROP ENV a DROP ENV b DELETE sandbox.always, sandbox.backend, sandbox.workdir DROP ENV c"
	if u == nil || u.String() != want {
		t.Fatalf("undo:\n got %v\nwant %s", u, want)
	}
	if _, err := grammar.ParseFile(u.Pretty() + ";"); err != nil {
		t.Fatalf("the undo does not parse back: %v", err)
	}
	// One key set by two old statements is undone once (agy, #213): the
	// merged DELETE never names a key twice.
	twice := parse("ALTER PLAYBOOK SET sandbox.workdir = '/a';\nALTER PLAYBOOK SET sandbox.workdir = '/b', sandbox.host = 'h';\n")
	if u := undoFor("kb", twice, nil); u == nil || u.String() != "ALTER PLAYBOOK kb DELETE sandbox.workdir, sandbox.host" {
		t.Fatalf("a key set twice: %v", u)
	} else if _, err := grammar.ParseFile(u.Pretty() + ";"); err != nil {
		t.Fatalf("the undo does not parse back: %v", err)
	}
	// A status line and its refresh, both dropped: DELETE statusline alone,
	// which a statement may hold (the pair is refused together).
	sl := parse("ALTER PLAYBOOK SET statusline.command = 'x';\nALTER PLAYBOOK SET statusline.refresh = 5;\n")
	if u := undoFor("kb", sl, nil); u == nil || u.String() != "ALTER PLAYBOOK kb DELETE statusline" {
		t.Fatalf("status line undo: %v", u)
	}
}

func TestLineDiff(t *testing.T) {
	got := strings.Join(lineDiff([]string{"a", "b", "c"}, []string{"a", "x", "c", "d"}), "|")
	if got != "- b|+ x|+ d" { // what went, then what came
		t.Fatalf("%q", got)
	}
}

// A played recipe that drops both its status line and its refresh: the
// undo is DELETE statusline alone, since a statement may not hold it beside
// DELETE statusline.refresh, and play parses its undo back from a file. The
// update used to fail on that file.
func TestPlayUpdateDropsStatuslineAndRefresh(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	t.Setenv("TMPDIR", t.TempDir())
	stubClaude(t)
	dir := t.TempDir()
	p := writeRecipe(t, dir, "slr.cpb", "-- title: Slr\n\nALTER PLAYBOOK\n  SET VAR FOO=1\n  SET statusline.command = 'echo x';\nALTER PLAYBOOK\n  SET statusline.refresh = 5;\n")
	playFlags(t, false, false, false, "")
	playKeepFlags(t, true, "")
	playRunFlags(t, true, nil, nil, nil)
	var err error
	if out := captureStdout(t, func() { err = runPlay(playCmd, []string{p}) }); err != nil {
		t.Fatalf("--keep: %v\n%s", err, out)
	}
	if sl, _ := showPlaybook(t, "slr")["statusline"].(map[string]any); sl["command"] != "echo x" {
		t.Fatalf("kept status line: %v", showPlaybook(t, "slr")["statusline"])
	}
	writeRecipe(t, dir, "slr.cpb", "-- title: Slr\n\nALTER PLAYBOOK\n  SET VAR FOO=1;\n")
	if out := captureStdout(t, func() { err = playUpdateRun("slr") }); err != nil {
		t.Fatalf("update: %v\n%s", err, out)
	}
	if sl, _ := showPlaybook(t, "slr")["statusline"].(map[string]any); sl["command"] != nil || sl["refresh"] != nil {
		t.Fatalf("the dropped status line stayed: %v", sl)
	}
}
