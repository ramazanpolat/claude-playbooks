package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// runUpdateFor runs cpb update <name>, with --dry-run and --json as given.
func runUpdateFor(t *testing.T, name string, dryRun, jsonOut bool) (string, error) {
	t.Helper()
	updateDryRun, updateJSON = dryRun, jsonOut
	defer func() { updateDryRun, updateJSON = false, false }()
	var err error
	out := captureStdout(t, func() { err = runUpdate(updateCmd, []string{name}) })
	return out, err
}

func readApplyRecord(t *testing.T, name string) (*manifest.Apply, string) {
	t.Helper()
	dir := filepath.Join(config.ResolvePlaybooksDir(), name)
	m, err := manifest.Read(dir)
	if err != nil || m == nil {
		t.Fatalf("%s: no manifest: %v", name, err)
	}
	text, _ := os.ReadFile(filepath.Join(dir, applyRecordFile))
	return m.Apply, string(text)
}

func pbVars(t *testing.T, name string) map[string]string {
	t.Helper()
	vars := map[string]string{}
	for _, x := range describePlaybookByName(t, name).Vars {
		if x.Value != nil {
			vars[x.Key] = *x.Value
		}
	}
	return vars
}

// A base drops a clause: cpb update removes it from the child, and keeps
// everything the files still set.
func TestApplyRecordUpdateRemovesDropped(t *testing.T) {
	root := sandboxDefaultRoot(t)
	writePlaybook(t, root, "p", nil)
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	writeCpb(t, dir, "base.cpb", "ALTER PLAYBOOK\n  SET VAR TEAM=one LOG_LEVEL=info\n  SET model = 'base-model'\n  ALLOW TOOL 'Bash(x)' 'Read';\n")
	child := writeCpb(t, dir, "child.cpb", "INCLUDE 'base.cpb';\nALTER PLAYBOOK\n  SET VAR LOG_LEVEL=debug;\n")
	if _, err := apply(t, child, "TO", "p"); err != nil {
		t.Fatal(err)
	}
	rec, text := readApplyRecord(t, "p")
	if rec == nil || len(rec.Files) != 1 || rec.Files[0] != child || !rec.To || rec.AppliedAt == "" {
		t.Fatalf("the [apply] record: %+v", rec)
	}
	if sum := sha256.Sum256([]byte(text)); hex.EncodeToString(sum[:]) != rec.SHA256 {
		t.Fatalf("the record's sha256 is not its file's")
	}
	if info, err := os.Stat(filepath.Join(root, "p", applyRecordFile)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the record file is not private: %v %v", info, err)
	}
	// The record is what was written after the fold: LOG_LEVEL once, the
	// child's value.
	if strings.Contains(text, "LOG_LEVEL=info") || !strings.Contains(text, "LOG_LEVEL=debug") {
		t.Fatalf("the record is not the folded statements:\n%s", text)
	}

	writeCpb(t, dir, "base.cpb", "ALTER PLAYBOOK\n  SET VAR LOG_LEVEL=info\n  SET model = 'base-model'\n  ALLOW TOOL 'Read';\n")
	plan, err := runUpdateFor(t, "p", true, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Changes since the last APPLY", "- ", "undo ", "changed", "ALTER PLAYBOOK p"} {
		if !strings.Contains(plan, want) {
			t.Fatalf("the dry run lacks %q:\n%s", want, plan)
		}
	}
	if v := describePlaybookByName(t, "p"); len(v.Tools.Allow) != 2 || pbVars(t, "p")["TEAM"] != "one" {
		t.Fatalf("the dry run wrote: allow=%v vars=%v", v.Tools.Allow, pbVars(t, "p"))
	}
	shaBefore := rec.SHA256

	out, err := runUpdateFor(t, "p", false, false)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	v := describePlaybookByName(t, "p")
	vars := pbVars(t, "p")
	if _, ok := vars["TEAM"]; ok || vars["LOG_LEVEL"] != "debug" || len(v.Tools.Allow) != 1 || v.Tools.Allow[0] != "Read" ||
		v.Model == nil || *v.Model != "base-model" {
		t.Fatalf("after the update: vars=%v allow=%v model=%v\n%s", vars, v.Tools.Allow, v.Model, out)
	}
	rec, text = readApplyRecord(t, "p")
	if rec.SHA256 == shaBefore || strings.Contains(text, "TEAM") || strings.Contains(text, "Bash(x)") {
		t.Fatalf("the record was not rewritten: %+v\n%s", rec, text)
	}
	again, err := runUpdateFor(t, "p", false, false)
	if err != nil || !strings.Contains(again, " 0 created, 0 changed, 2 unchanged,") || !strings.Contains(again, "the statements they gave it at the last APPLY") {
		t.Fatalf("an update with nothing new must change nothing: %v\n%s", err, again)
	}
}

// The record keeps references only: a credential-looking literal and a
// URL's credential are withheld, the keys kept, so an unchanged file
// changes nothing and a dropped key is still removed.
func TestApplyRecordWithholdsSecrets(t *testing.T) {
	root := sandboxDefaultRoot(t)
	writePlaybook(t, root, "p", nil)
	const token, password = "sk-livevalue-0123456789", "hunter2pw"
	f := writeCpb(t, t.TempDir(), "r.cpb", "ALTER PLAYBOOK\n  SET VAR API_TOKEN='"+token+"' AS PLAINTEXT\n  SET VAR PROXY_URL='https://me:"+password+"@proxy.example/x' MODE=fast;\n")
	if _, err := apply(t, f, "TO", "p"); err != nil {
		t.Fatal(err)
	}
	m, err := os.ReadFile(filepath.Join(root, "p", ".playbook"))
	if err != nil {
		t.Fatal(err)
	}
	_, text := readApplyRecord(t, "p")
	applySection := string(m)[strings.Index(string(m), "[apply]"):]
	for _, where := range []string{text, applySection} {
		if strings.Contains(where, token) || strings.Contains(where, password) {
			t.Fatalf("a value reached the record:\n%s", where)
		}
	}
	for _, want := range []string{"API_TOKEN=" + withheldMark + " AS PLAINTEXT", "PROXY_URL=" + withheldMark, "MODE=fast"} {
		if !strings.Contains(text, want) {
			t.Fatalf("the record lacks %q:\n%s", want, text)
		}
	}
	if _, err := grammar.ParseFile(text); err != nil {
		t.Fatalf("the record does not parse: %v\n%s", err, text)
	}
	out, err := runUpdateFor(t, "p", false, false)
	if err != nil || !strings.Contains(out, " 0 created, 0 changed, 1 unchanged,") {
		t.Fatalf("an unchanged file with a withheld value must change nothing: %v\n%s", err, out)
	}
	if strings.Contains(out, token) || strings.Contains(out, password) {
		t.Fatalf("the update printed a value:\n%s", out)
	}
	writeCpb(t, filepath.Dir(f), "r.cpb", "ALTER PLAYBOOK\n  SET VAR MODE=fast;\n")
	if out, err := runUpdateFor(t, "p", false, false); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	vars := pbVars(t, "p")
	if _, ok := vars["API_TOKEN"]; ok {
		t.Fatalf("a withheld key the file dropped was not removed: %v", vars)
	}
	if _, ok := vars["PROXY_URL"]; ok || vars["MODE"] != "fast" {
		t.Fatalf("after the update: %v", vars)
	}
}

// A fleet file: each target gets its own record, and cpb update of one
// runs only the statements the files give it.
func TestApplyRecordFleetUpdatesOneTarget(t *testing.T) {
	root := sandboxDefaultRoot(t)
	for _, n := range []string{"a", "b", "c"} {
		writePlaybook(t, root, n, nil)
	}
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	fleet := writeCpb(t, dir, "fleet.cpb", "USE PLAYBOOK a;\nALTER PLAYBOOK SET VAR X=1;\nUSE PLAYBOOK b;\nALTER PLAYBOOK SET VAR X=2;\nALTER PLAYBOOK c SET VAR Y=1;\n")
	if _, err := apply(t, fleet); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"a", "b"} {
		if rec, _ := readApplyRecord(t, n); rec == nil || rec.To || len(rec.Files) != 1 || rec.Files[0] != fleet {
			t.Fatalf("%s's record: %+v", n, rec)
		}
	}
	if rec, _ := readApplyRecord(t, "c"); rec != nil {
		t.Fatalf("a statement that names its playbook is not a target's: %+v", rec)
	}
	writeCpb(t, dir, "fleet.cpb", "USE PLAYBOOK a;\nALTER PLAYBOOK SET VAR X=10;\nUSE PLAYBOOK b;\nALTER PLAYBOOK SET VAR X=20;\nALTER PLAYBOOK c SET VAR Y=2;\n")
	if out, err := runUpdateFor(t, "a", false, false); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if pbVars(t, "a")["X"] != "10" || pbVars(t, "b")["X"] != "2" || pbVars(t, "c")["Y"] != "1" {
		t.Fatalf("cpb update a touched another playbook: a=%v b=%v c=%v", pbVars(t, "a"), pbVars(t, "b"), pbVars(t, "c"))
	}
	if _, err := runUpdateFor(t, "c", false, false); err == nil || !strings.Contains(err.Error(), "nothing to update from") {
		t.Fatalf("c has no record: %v", err)
	}
	// The files no longer target b.
	writeCpb(t, dir, "fleet.cpb", "USE PLAYBOOK a;\nALTER PLAYBOOK SET VAR X=10;\n")
	if _, err := runUpdateFor(t, "b", false, false); err == nil || !strings.Contains(err.Error(), "give b no name-less statements now") {
		t.Fatalf("files that no longer target b: %v", err)
	}
}

// A playbook another record updates is left to it; a directory has no
// manifest; a dry run records nothing.
func TestApplyRecordOnlyWhereItUpdates(t *testing.T) {
	root := sandboxDefaultRoot(t)
	writePlaybook(t, root, "src", &manifest.Manifest{Source: &manifest.Source{Repository: "https://example.invalid/x.git"}})
	writePlaybook(t, root, "p", nil)
	f := writeCpb(t, t.TempDir(), "r.cpb", "ALTER PLAYBOOK SET VAR X=1;\n")
	out, err := apply(t, f, "TO", "src")
	if err != nil || !strings.Contains(out, "Note: src updates from its [source], so this APPLY is not recorded for cpb update.") {
		t.Fatalf("%v\n%s", err, out)
	}
	if rec, _ := readApplyRecord(t, "src"); rec != nil {
		t.Fatalf("a [source] playbook got a record: %+v", rec)
	}
	if _, err := apply(t, f, "TO", "p", "--dry-run"); err != nil {
		t.Fatal(err)
	}
	if m, _ := manifest.Read(filepath.Join(root, "p")); m != nil && m.Apply != nil {
		t.Fatalf("a dry run recorded: %+v", m.Apply)
	}
	cfg := t.TempDir()
	if _, err := apply(t, f, "TO", cfg, "--yes"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cfg, applyRecordFile)); !os.IsNotExist(err) {
		t.Fatalf("a directory target got a record: %v", err)
	}
}

// The record is shown: SHOW PLAYBOOK's apply field and its human line;
// cpb update --dry-run --json is the APPLY plan, the undo first.
func TestApplyRecordShownAndPlanned(t *testing.T) {
	root := sandboxDefaultRoot(t)
	writePlaybook(t, root, "p", nil)
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	f := writeCpb(t, dir, "r.cpb", "ALTER PLAYBOOK\n  SET VAR X=1\n  ALLOW TOOL 'Read';\n")
	if _, err := apply(t, f, "TO", "p"); err != nil {
		t.Fatal(err)
	}
	v := describePlaybookByName(t, "p")
	if v.Apply == nil || len(v.Apply.Files) != 1 || v.Apply.Files[0] != f || !v.Apply.To {
		t.Fatalf("SHOW's apply: %+v", v.Apply)
	}
	human := captureStdout(t, func() { _ = runStatement([]string{"SHOW", "PLAYBOOK", "p"}) })
	if !strings.Contains(human, "Applied from:") || !strings.Contains(human, f) || !strings.Contains(human, "cpb update p") {
		t.Fatalf("SHOW's human form:\n%s", human)
	}
	writeCpb(t, dir, "r.cpb", "ALTER PLAYBOOK\n  SET VAR X=1;\n")
	out, err := runUpdateFor(t, "p", true, true)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var rep struct {
		Statements []struct {
			File      string `json:"file"`
			Line      int    `json:"line"`
			Statement string `json:"statement"`
			Verdict   string `json:"verdict"`
		} `json:"statements"`
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(rep.Statements) != 2 || rep.Statements[0].Line != 0 || filepath.Base(rep.Statements[0].File) != "recipe.cpb" ||
		rep.Statements[0].Verdict != outChanged || rep.Statements[1].File != f {
		t.Fatalf("the plan: %+v", rep.Statements)
	}
	if v := describePlaybookByName(t, "p"); len(v.Tools.Allow) != 1 {
		t.Fatalf("the JSON plan wrote: %v", v.Tools.Allow)
	}
}

// The record withholds what SHOW CREATE withholds, in every clause that can
// carry it, and changes nothing in the clause it was given. (The parser
// refuses a marketplace or skill source carrying a credential; the record
// withholds one all the same.)
func TestWithheldClause(t *testing.T) {
	in := []grammar.Clause{
		{Kind: grammar.AddMarketplace, Names: []string{"m"}, Arg: "https://tok:x@git.example/o/r.git"},
		{Kind: grammar.AddMCP, Names: []string{"s"}, MCP: &grammar.MCP{URL: "https://u:pw@mcp.example/v1",
			Env: []grammar.Var{{Key: "MODE", Value: "https://a:b@h.example"}}, Headers: []grammar.Var{{Key: "X-Mode", Value: "https://c:d@h.example"}}}},
		{Kind: grammar.AddMCP, Names: []string{"t"}, MCP: &grammar.MCP{Command: "run", Args: []string{"--db", "postgres://e:f@db.example/x"}}},
		{Kind: grammar.AddSkill, Names: []string{"k"}, Skill: &grammar.Skill{From: "https://t0k@git.example/s.git"}},
		{Kind: grammar.SetVar, Plaintext: true, Vars: []grammar.Var{{Key: "GITHUB_TOKEN", Value: "ghp_abcdefghij"}, {Key: "MODE", Value: "fast"}}},
		{Kind: grammar.SetSandboxKeys, Settings: []grammar.Var{{Key: "secrets", Value: "https://g:h@vault.example"}}},
	}
	before := (&grammar.Stmt{Verb: grammar.Alter, Object: grammar.Playbook, Recipe: true, Clauses: in}).String()
	r := grammar.Stmt{Verb: grammar.Alter, Object: grammar.Playbook, Recipe: true}
	for _, c := range in {
		r.Clauses = append(r.Clauses, withheldClause(c))
	}
	got := r.String()
	for _, secret := range []string{"tok:x", "u:pw", "a:b", "c:d", "e:f", "t0k", "ghp_abcdefghij", "g:h"} {
		if strings.Contains(got, secret) {
			t.Fatalf("%q survived: %s", secret, got)
		}
	}
	for _, kept := range []string{"MODE=fast", "GITHUB_TOKEN=" + withheldMark, "withheld@git.example/o/r.git", "--db", "secrets=" + withheldMark} {
		if !strings.Contains(got, kept) {
			t.Fatalf("%q is gone: %s", kept, got)
		}
	}
	if after := (&grammar.Stmt{Verb: grammar.Alter, Object: grammar.Playbook, Recipe: true, Clauses: in}).String(); after != before {
		t.Fatalf("withheldClause changed its input:\n%s\n%s", before, after)
	}
}

// A base's SET STATUSLINE … IF UNSET defers to a status line the playbook
// has: when the base drops it, cpb update removes it only where the files
// wrote it.
func TestApplyRecordDeferredStatusline(t *testing.T) {
	root := sandboxDefaultRoot(t)
	writePlaybook(t, root, "own", nil)
	writePlaybook(t, root, "fresh", nil)
	if err := runStatement([]string{"ALTER", "PLAYBOOK", "own", "SET", "STATUSLINE", "echo mine"}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	f := writeCpb(t, dir, "base.cpb", "ALTER PLAYBOOK\n  SET STATUSLINE 'echo base' IF UNSET\n  SET VAR X=1;\n")
	for _, n := range []string{"own", "fresh"} {
		if out, err := apply(t, f, "TO", n); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
	}
	writeCpb(t, dir, "base.cpb", "ALTER PLAYBOOK\n  SET VAR X=1;\n")
	for _, n := range []string{"own", "fresh"} {
		if out, err := runUpdateFor(t, n, false, false); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
	}
	if sl := describePlaybookByName(t, "own").Statusline; sl == nil || *sl != "echo mine" {
		t.Fatalf("the playbook's own status line was removed: %v", sl)
	}
	if sl := describePlaybookByName(t, "fresh").Statusline; sl != nil {
		t.Fatalf("the status line the files wrote was kept: %v", *sl)
	}
}

// The deferred status line beside a refresh the files did write (Codex,
// #213): dropping both keeps the playbook's own command and removes the
// refresh.
func TestApplyRecordDeferredStatuslineKeepsRefreshUndo(t *testing.T) {
	root := sandboxDefaultRoot(t)
	writePlaybook(t, root, "own", nil)
	if err := runStatement([]string{"ALTER", "PLAYBOOK", "own", "SET", "STATUSLINE", "echo mine"}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	f := writeCpb(t, dir, "base.cpb", "ALTER PLAYBOOK\n  SET STATUSLINE 'echo base' IF UNSET\n  SET VAR X=1;\nALTER PLAYBOOK\n  SET STATUSLINE REFRESH 5;\n")
	if out, err := apply(t, f, "TO", "own"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if v := describePlaybookByName(t, "own"); v.Statusline == nil || *v.Statusline != "echo mine" || v.StatuslineRefresh == nil || *v.StatuslineRefresh != 5 {
		t.Fatalf("after APPLY: %v %v", v.Statusline, v.StatuslineRefresh)
	}
	writeCpb(t, dir, "base.cpb", "ALTER PLAYBOOK\n  SET VAR X=1;\n")
	if out, err := runUpdateFor(t, "own", false, false); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if v := describePlaybookByName(t, "own"); v.Statusline == nil || *v.Statusline != "echo mine" || v.StatuslineRefresh != nil {
		t.Fatalf("after the update: command %v, refresh %v (want echo mine, none)", v.Statusline, v.StatuslineRefresh)
	}
}

// A linked playbook's manifest belongs to its target: an APPLY that runs
// there (only a lifecycle clause does) records nothing in it.
func TestApplyRecordNotForLinked(t *testing.T) {
	sandboxDefaultRoot(t)
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "CLAUDE.md"), []byte("# target\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Write(target, &manifest.Manifest{Name: "target"}); err != nil {
		t.Fatal(err)
	}
	var err error
	captureStderr(t, func() { _, err = quotedStmt(t, "CREATE PLAYBOOK lk SET launcher = '' LINK '"+target+"'") })
	if err != nil {
		t.Fatal(err)
	}
	f := writeCpb(t, t.TempDir(), "r.cpb", "ALTER PLAYBOOK SET launcher = '';\n")
	out, err := apply(t, f, "TO", "lk")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "Note: lk is linked, and its manifest belongs to the target, so this APPLY is not recorded for cpb update.") {
		t.Fatalf("no note:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(target, filepath.Dir(applyRecordFile))); !os.IsNotExist(err) {
		t.Fatalf("the record reached the link's target: %v", err)
	}
	if m, _ := manifest.Read(target); m != nil && m.Apply != nil {
		t.Fatalf("the target's manifest got [apply]: %+v", m.Apply)
	}
}

// A playbook with no record says so, whatever the flags (agy, #212): a
// flag is not answered with "updates from its [source]".
func TestUpdateNoRecordWithFlags(t *testing.T) {
	root := sandboxDefaultRoot(t)
	writePlaybook(t, root, "bare", nil)
	if err := updateCmd.Flags().Set("sha256", "abc"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		updateSHA256 = ""
		updateCmd.Flags().Lookup("sha256").Changed = false
	})
	err := runUpdate(updateCmd, []string{"bare"})
	if err == nil || !strings.Contains(err.Error(), `"bare" has no [source], [play] or [apply] record`) {
		t.Fatalf("err = %v", err)
	}
}

// A base that stops attaching an env set, or stops setting a sandbox key,
// takes them out of the playbook at cpb update; an env set the playbook
// attached itself stays.
func TestApplyRecordUndoesEnvAndSandbox(t *testing.T) {
	root := sandboxDefaultRoot(t)
	writePlaybook(t, root, "p", nil)
	for _, line := range []string{"CREATE ENV route SET R=1", "CREATE ENV mine SET M=1", "ALTER PLAYBOOK p ADD ENV mine"} {
		if _, err := quotedStmt(t, line); err != nil {
			t.Fatalf("%s: %v", line, err)
		}
	}
	dir := t.TempDir()
	f := writeCpb(t, dir, "base.cpb", "ALTER PLAYBOOK\n  ADD ENV route\n  SET SANDBOX workdir=/srv/w share_skills=true;\n")
	if out, err := apply(t, f, "TO", "p"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	v := describePlaybookByName(t, "p")
	if strings.Join(v.Envs, " ") != "mine route" || v.Sandbox.Workdir == nil || !v.Sandbox.ShareSkills {
		t.Fatalf("after APPLY: envs=%v sandbox=%+v", v.Envs, v.Sandbox)
	}
	writeCpb(t, dir, "base.cpb", "ALTER PLAYBOOK\n  SET VAR X=1;\n")
	if out, err := runUpdateFor(t, "p", false, false); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	v = describePlaybookByName(t, "p")
	if strings.Join(v.Envs, " ") != "mine" || v.Sandbox.Workdir != nil || v.Sandbox.ShareSkills {
		t.Fatalf("after the update: envs=%v sandbox=%+v", v.Envs, v.Sandbox)
	}
}

// One sandbox key changes and another is dropped (agy, #213): the fold
// drops only the changed key from the merged UNSET SANDBOX, so the dropped
// one is still unset.
func TestApplyRecordSandboxKeyChangedAndDropped(t *testing.T) {
	root := sandboxDefaultRoot(t)
	writePlaybook(t, root, "p", nil)
	dir := t.TempDir()
	f := writeCpb(t, dir, "base.cpb", "ALTER PLAYBOOK SET SANDBOX workdir=/srv/w host=box;\n")
	if out, err := apply(t, f, "TO", "p"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	writeCpb(t, dir, "base.cpb", "ALTER PLAYBOOK SET SANDBOX workdir=/srv/x;\n")
	out, err := runUpdateFor(t, "p", false, false)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	sb := describePlaybookByName(t, "p").Sandbox
	if sb.Host != nil || sb.Workdir == nil || *sb.Workdir != "/srv/x" {
		t.Fatalf("after the update: host=%v workdir=%v\n%s", sb.Host, sb.Workdir, out)
	}
	if !strings.Contains(out, "overridden: UNSET SANDBOX workdir") {
		t.Fatalf("the fold did not name the changed key alone:\n%s", out)
	}
}
