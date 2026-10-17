package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/launcher"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// CREATE OR ALTER PLAYBOOK on a playbook that exists applies its SET list as
// ALTER … SET would, launcher included, and applied again changes nothing.
// A dry run plans it and writes nothing. IF NOT EXISTS leaves it as it is.
func TestCreateOrAlterConverges(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CPB_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	has := func(cmd string) bool {
		_, exists, _ := launcher.Lookup(config.LauncherDir, cmd)
		return exists
	}
	mustStmt(t, "CREATE PLAYBOOK cv SET launcher = 'cv1', model = 'a'")

	// IF NOT EXISTS: the playbook exists, so nothing changes, its SET list
	// included.
	skip := writePlaybookFile(t, "CREATE PLAYBOOK IF NOT EXISTS cv SET launcher = 'cv2', model = 'b';\n")
	before := snapshot(t, root)
	if out, err := apply(t, skip); err != nil || !strings.Contains(out, "0 created, 0 changed, 1 unchanged") || snapshot(t, root) != before {
		t.Fatalf("IF NOT EXISTS changed something: %v\n%s", err, out)
	}
	file := writePlaybookFile(t, "CREATE OR ALTER PLAYBOOK cv\n  SET launcher = 'cv2', global_instructions = 'inherited', model = 'b', statusline.command = 'echo cv';\n")
	out, err := apply(t, file, "--dry-run")
	if err != nil || !strings.Contains(out, "0 created, 1 changed, 0 unchanged") {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	if snapshot(t, root) != before || !has("cv1") || has("cv2") {
		t.Fatal("the dry run wrote something")
	}
	if out, err = apply(t, file); err != nil || !strings.Contains(out, "0 created, 1 changed, 0 unchanged") {
		t.Fatalf("APPLY: %v\n%s", err, out)
	}
	s := settingsOf(t, filepath.Join(root, "cv"))
	if s["model"] != "b" || s["statusLine"] == nil {
		t.Fatalf("the SET list was not applied: %v", s)
	}
	if v := showPlaybook(t, "cv"); v["global_instructions"] != "inherited" {
		t.Fatalf("global_instructions = %v", v["global_instructions"])
	}
	if has("cv1") || !has("cv2") {
		t.Fatalf("launcher: cv1=%v cv2=%v", has("cv1"), has("cv2"))
	}
	// Applied again: nothing differs, nothing changes.
	if out, err = apply(t, file); err != nil || !strings.Contains(out, "0 created, 0 changed, 1 unchanged") || !strings.Contains(out, "already exists; unchanged") {
		t.Fatalf("again: %v\n%s", err, out)
	}
	if out, err = apply(t, file, "--dry-run"); err != nil || !strings.Contains(out, "0 created, 0 changed, 1 unchanged") {
		t.Fatalf("dry run again: %v\n%s", err, out)
	}
	// A CREATE with no SET list leaves it as it is.
	if out := mustStmt(t, "CREATE OR ALTER PLAYBOOK cv"); !strings.Contains(out, "PLAYBOOK cv already exists; unchanged") {
		t.Fatalf("no SET list:\n%s", out)
	}
	// SHOW CREATE is one converging statement per property list: applied to
	// the playbook it describes, it changes nothing.
	created := mustStmt(t, "SHOW CREATE PLAYBOOK cv")
	if !strings.Contains(created, "CREATE OR ALTER PLAYBOOK cv\n  SET launcher = 'cv2', login = 'shared', global_instructions = 'inherited'\n  RESET sandbox, agent, statusline.refresh, model_picker.mode\n  USE ENV '';") {
		t.Fatalf("SHOW CREATE:\n%s", created)
	}
	if out, err := apply(t, writePlaybookFile(t, created)); err != nil || !strings.Contains(out, " 0 created, 0 changed,") {
		t.Fatalf("SHOW CREATE did not re-apply unchanged: %v\n%s", err, out)
	}
	// What the playbook has but the list leaves out stays: converging is
	// SET, never a reset.
	if out, err := apply(t, writePlaybookFile(t, "CREATE OR ALTER PLAYBOOK cv SET global_instructions = 'excluded';\n")); err != nil || !strings.Contains(out, "0 created, 1 changed") {
		t.Fatalf("global_instructions back: %v\n%s", err, out)
	}
	if s := settingsOf(t, filepath.Join(root, "cv")); s["model"] != "b" {
		t.Fatalf("a key the list left out changed: %v", s)
	}
	// The launcher the list gives where the playbook has another: a launcher
	// change, which refuses a name another playbook answers to.
	mustStmt(t, "CREATE PLAYBOOK other SET launcher = 'taken'")
	if _, err := apply(t, writePlaybookFile(t, "CREATE OR ALTER PLAYBOOK cv SET launcher = 'taken';\n")); err == nil || !strings.Contains(err.Error(), "taken") {
		t.Fatalf("a taken launcher: %v", err)
	}
}

// On a new playbook every pair of the SET list is applied, the ones written
// by an ALTER of the new playbook too (the status line, the picker mode).
func TestCreateSetListOnNewPlaybook(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CPB_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	out, err := apply(t, writePlaybookFile(t, "CREATE PLAYBOOK IF NOT EXISTS nv\n  SET launcher = '', statusline.command = 'echo nv', statusline.refresh = 5, model_picker.mode = 'only';\n"))
	if err != nil || !strings.Contains(out, "1 created") {
		t.Fatalf("%v\n%s", err, out)
	}
	s := settingsOf(t, filepath.Join(root, "nv"))
	sl, _ := s["statusLine"].(map[string]any)
	mp, _ := s["modelPicker"].(map[string]any)
	if sl["command"] != "echo nv" || sl["refreshInterval"] != float64(5) || mp["replaceBuiltInOptions"] != true {
		t.Fatalf("settings: %v", s)
	}
}

// CREATE [OR ALTER] PLAYBOOK takes USE ENV, the whole list: on a new
// playbook it attaches the sets; on one that exists, OR ALTER replaces the
// list only where it differs, and an omitted list keeps the attachments.
func TestCreateUseEnv(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CPB_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	for _, n := range []string{"acct-work", "acct-personal", "corp"} {
		mustStmt(t, "CREATE ENV "+n)
	}
	sets := func() []string {
		if e := readEnv(t, filepath.Join(root, "ue")); e != nil {
			return e.Sets
		}
		return nil
	}
	mustStmt(t, "CREATE OR ALTER PLAYBOOK ue SET launcher = '' USE ENV acct-work corp")
	if got := strings.Join(sets(), ","); got != "acct-work,corp" {
		t.Fatalf("new playbook: %q", got)
	}
	file := writePlaybookFile(t, "CREATE OR ALTER PLAYBOOK ue SET launcher = '' USE ENV acct-personal corp;\n")
	if out, err := apply(t, file); err != nil || !strings.Contains(out, "0 created, 1 changed") {
		t.Fatalf("replace the list: %v\n%s", err, out)
	}
	if got := strings.Join(sets(), ","); got != "acct-personal,corp" {
		t.Fatalf("after OR ALTER: %q", got)
	}
	if out, err := apply(t, file); err != nil || !strings.Contains(out, "0 created, 0 changed, 1 unchanged") {
		t.Fatalf("again: %v\n%s", err, out)
	}
	// No USE ENV: the attachments stay.
	mustStmt(t, "CREATE OR ALTER PLAYBOOK ue SET model = 'm'")
	if got := strings.Join(sets(), ","); got != "acct-personal,corp" {
		t.Fatalf("an omitted list changed the attachments: %q", got)
	}
	// An unknown set refuses the whole statement, on create too.
	if err := stmtErr(t, "CREATE PLAYBOOK ue2 USE ENV ghost"); err == nil || !strings.Contains(err.Error(), `no env set "ghost"`) {
		t.Fatalf("unknown set: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "ue2")); !os.IsNotExist(err) {
		t.Fatalf("a refused CREATE left a playbook behind: %v", err)
	}
	// SHOW CREATE states the list on the CREATE.
	if show := mustStmt(t, "SHOW CREATE PLAYBOOK ue"); !strings.Contains(show, "USE ENV acct-personal corp;") || strings.Contains(show, "ALTER PLAYBOOK ue\n  USE ENV") {
		t.Fatalf("SHOW CREATE:\n%s", show)
	}
}

// The launcher is an ordinary key (D1): it combines with any other in one
// ALTER. A taken name refuses the whole statement before anything is
// written; RENAME TO stays a statement of its own.
func TestLauncherWithOtherKeys(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CPB_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	has := func(cmd string) bool {
		_, exists, _ := launcher.Lookup(config.LauncherDir, cmd)
		return exists
	}
	mustStmt(t, "CREATE PLAYBOOK lk SET launcher = 'lk1'")
	mustStmt(t, "CREATE PLAYBOOK other SET launcher = 'taken'")

	mustStmt(t, "ALTER PLAYBOOK lk SET launcher = 'lk2', model = 'a'")
	if s := settingsOf(t, filepath.Join(root, "lk")); s["model"] != "a" || has("lk1") || !has("lk2") {
		t.Fatalf("model %v, lk1=%v lk2=%v", s["model"], has("lk1"), has("lk2"))
	}
	// The same launcher again: only the model changes.
	mustStmt(t, "ALTER PLAYBOOK lk SET model = 'b', launcher = 'lk2'")
	if s := settingsOf(t, filepath.Join(root, "lk")); s["model"] != "b" || !has("lk2") {
		t.Fatalf("model %v, lk2=%v", s["model"], has("lk2"))
	}
	if err := stmtErr(t, "ALTER PLAYBOOK lk SET launcher = 'taken', model = 'c'"); err == nil || !strings.Contains(err.Error(), "taken") {
		t.Fatalf("a taken launcher: %v", err)
	}
	if s := settingsOf(t, filepath.Join(root, "lk")); s["model"] != "b" || !has("lk2") {
		t.Fatalf("a refused statement wrote something: model %v, lk2=%v", s["model"], has("lk2"))
	}
	if err := stmtErr(t, "ALTER PLAYBOOK lk RENAME TO lk3 SET model = 'c'"); err == nil || !strings.Contains(err.Error(), "RENAME TO is a statement of its own") {
		t.Fatalf("RENAME TO with another key: %v", err)
	}
}

// D2, exact convergence: a playbook's own SHOW CREATE puts back every
// property and its env list, so a model or an env set added elsewhere is
// undone; collections (tool rules here) stay additive.
func TestShowCreateConvergesExactly(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CPB_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	mustStmt(t, "CREATE ENV extra")
	mustStmt(t, "CREATE PLAYBOOK ex SET launcher = ''")
	file := writePlaybookFile(t, mustStmt(t, "SHOW CREATE PLAYBOOK ex"))
	if out, err := apply(t, file); err != nil || !strings.Contains(out, " 0 created, 0 changed,") {
		t.Fatalf("own SHOW CREATE: %v\n%s", err, out)
	}
	mustStmt(t, "ALTER PLAYBOOK ex SET model = 'm', statusline.command = 'true' USE ENV extra")
	mustStmt(t, "ALTER PLAYBOOK ex ALLOW TOOL 'Bash(ls)'")
	if out, err := apply(t, file); err != nil || !strings.Contains(out, "0 created, 1 changed") {
		t.Fatalf("converge back: %v\n%s", err, out)
	}
	s := settingsOf(t, filepath.Join(root, "ex"))
	if _, ok := s["model"]; ok {
		t.Fatalf("the model survived: %v", s)
	}
	if _, ok := s["statusLine"]; ok {
		t.Fatalf("the status line survived: %v", s)
	}
	if e := readEnv(t, filepath.Join(root, "ex")); e != nil && len(e.Sets) > 0 {
		t.Fatalf("the env set survived: %q", e.Sets)
	}
	if perms, _ := s["permissions"].(map[string]any); perms == nil || len(perms["allow"].([]any)) != 1 {
		t.Fatalf("a collection is additive, the tool rule stays: %v", s)
	}
	if out, err := apply(t, file); err != nil || !strings.Contains(out, " 0 created, 0 changed,") {
		t.Fatalf("again: %v\n%s", err, out)
	}
	// RESET in CREATE OR ALTER is a no-op on a new playbook.
	mustStmt(t, "CREATE OR ALTER PLAYBOOK ex2 SET launcher = '' RESET model, sandbox USE ENV ''")
	if _, err := os.Stat(filepath.Join(root, "ex2")); err != nil {
		t.Fatalf("CREATE OR ALTER with RESET did not create: %v", err)
	}
}

// SET IF UNSET applies whole, and only when none of its keys is set (each
// at the value RESET gives it); else it changes nothing and says which key
// was set.
func TestSetIfUnsetAppliesWhole(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CPB_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	mustStmt(t, "CREATE PLAYBOOK iu SET launcher = ''")
	mustStmt(t, "ALTER PLAYBOOK iu SET model = 'mine'")

	out, err := quotedStmt(t, "ALTER PLAYBOOK iu SET IF UNSET model = 'r', agent = 'x'")
	if err != nil || !strings.Contains(out, "PLAYBOOK iu: model is set; SET IF UNSET changed nothing") {
		t.Fatalf("one key set: %v\n%s", err, out)
	}
	s := settingsOf(t, filepath.Join(root, "iu"))
	if s["model"] != "mine" || s["agent"] != nil {
		t.Fatalf("a skipped list wrote: %v", s)
	}
	if _, err := quotedStmt(t, "ALTER PLAYBOOK iu SET IF UNSET agent = 'x', statusline.command = 'echo r', sandbox.host = 'me@box'"); err != nil {
		t.Fatal(err)
	}
	s = settingsOf(t, filepath.Join(root, "iu"))
	if s["agent"] != "x" || s["statusLine"] == nil {
		t.Fatalf("none set, but not applied: %v", s)
	}
	if m, _ := manifest.Read(filepath.Join(root, "iu")); m == nil || m.Sandbox == nil || m.Sandbox.Host != "me@box" {
		t.Fatalf("sandbox.host: %+v", m)
	}
	if out, _ := quotedStmt(t, "ALTER PLAYBOOK iu SET IF UNSET statusline.command = 'echo other'"); !strings.Contains(out, "statusline.command is set") {
		t.Fatalf("again:\n%s", out)
	}
	// D3: a key whose default is a value is never unset, so SET IF UNSET
	// refuses it.
	for _, key := range []string{"login = 'isolated'", "launcher = 'iu2'", "global_instructions = 'inherited'", "sandbox.always = false"} {
		if _, err := quotedStmt(t, "ALTER PLAYBOOK iu SET IF UNSET "+key); err == nil || !strings.Contains(err.Error(), "never unset; use SET") {
			t.Fatalf("SET IF UNSET %s: %v", key, err)
		}
	}
	// CREATE OR ALTER takes SET IF UNSET too: applied on a new playbook,
	// judged on one that exists.
	mustStmt(t, "CREATE OR ALTER PLAYBOOK iu2 SET launcher = '' SET IF UNSET model = 'n'")
	if s := settingsOf(t, filepath.Join(root, "iu2")); s["model"] != "n" {
		t.Fatalf("SET IF UNSET on a new playbook: %v", s)
	}
	if out := mustStmt(t, "CREATE OR ALTER PLAYBOOK iu2 SET IF UNSET model = 'o'"); !strings.Contains(out, "model is set") {
		t.Fatalf("SET IF UNSET on an existing playbook:\n%s", out)
	}
	if err := stmtErr(t, "CREATE PLAYBOOK iu3 SET IF UNSET model = 'o'"); err == nil || !strings.Contains(err.Error(), "SET IF UNSET is for ALTER PLAYBOOK and CREATE OR ALTER PLAYBOOK") {
		t.Fatalf("SET IF UNSET in a plain CREATE: %v", err)
	}
	// A dry run judges by what earlier statements would leave.
	file := writePlaybookFile(t, "ALTER PLAYBOOK iu RESET model;\nALTER PLAYBOOK iu SET IF UNSET model = 'y';\nALTER PLAYBOOK iu SET IF UNSET model = 'z';\n")
	if out, err := apply(t, file, "--dry-run"); err != nil || !strings.Contains(out, "0 created, 2 changed, 1 unchanged") {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
}

// An update that drops a SET IF UNSET list removes what the list wrote
// only where the playbook still holds it: a value the playbook had, which
// the list deferred to, stays.
func TestApplyRecordDeferredIfUnset(t *testing.T) {
	root := sandboxDefaultRoot(t)
	writePlaybook(t, root, "own", nil)
	writePlaybook(t, root, "fresh", nil)
	mustStmt(t, "ALTER PLAYBOOK own SET model = 'mine'")
	dir := t.TempDir()
	f := writeCpb(t, dir, "base.cpb", "ALTER PLAYBOOK\n  SET IF UNSET model = 'base', agent = 'helper'\n  SET VAR X=1;\n")
	for _, n := range []string{"own", "fresh"} {
		if out, err := apply(t, f, "TO", n); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
	}
	if v := describePlaybookByName(t, "fresh"); v.Model == nil || *v.Model != "base" || v.Agent == nil || *v.Agent != "helper" {
		t.Fatalf("fresh: %+v", v)
	}
	writeCpb(t, dir, "base.cpb", "ALTER PLAYBOOK\n  SET VAR X=1;\n")
	for _, n := range []string{"own", "fresh"} {
		if out, err := runUpdateFor(t, n, false, false); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
	}
	if v := describePlaybookByName(t, "own"); v.Model == nil || *v.Model != "mine" || v.Agent != nil {
		t.Fatalf("own: model %v, agent %v (want mine, none)", v.Model, v.Agent)
	}
	if v := describePlaybookByName(t, "fresh"); v.Model != nil || v.Agent != nil {
		t.Fatalf("fresh kept what the files wrote: model %v, agent %v", v.Model, v.Agent)
	}
}

// RESET model_picker is the mode: the rows are a collection, which DROP
// MODEL empties.
func TestDeleteModelPickerKeepsRows(t *testing.T) {
	root := sandboxDefaultRoot(t)
	mustStmt(t, "CREATE PLAYBOOK mp SET launcher = ''")
	if _, err := quotedStmt(t, "ALTER PLAYBOOK mp ADD MODEL 'glm-5.3' SET model_picker.mode = 'only'"); err != nil {
		t.Fatal(err)
	}
	mustStmt(t, "ALTER PLAYBOOK mp RESET model_picker")
	mp, _ := settingsOf(t, filepath.Join(root, "mp"))["modelPicker"].(map[string]any)
	if mp == nil || mp["replaceBuiltInOptions"] != nil || len(mp["options"].([]any)) != 1 {
		t.Fatalf("modelPicker: %v", mp)
	}
}

// SET IF UNSET in a recipe applied TO a plain config directory judges the
// directory's settings.json.
func TestSetIfUnsetToDirectory(t *testing.T) {
	sandboxDefaultRoot(t)
	cfg, _ := filepath.EvalSymlinks(t.TempDir())
	if err := os.WriteFile(filepath.Join(cfg, "settings.json"), []byte(`{"model": "mine"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	recipe := writeCpb(t, dir, "r.cpb", "ALTER PLAYBOOK\n  SET IF UNSET model = 'r', statusline.command = 'echo r';\nALTER PLAYBOOK\n  SET IF UNSET statusline.command = 'echo s';\n")
	out, err := apply(t, recipe, "TO", cfg, "--yes")
	if err != nil || !strings.Contains(out, cfg+": model is set; SET IF UNSET changed nothing") {
		t.Fatalf("%v\n%s", err, out)
	}
	s := settingsOf(t, cfg)
	if sl, _ := s["statusLine"].(map[string]any); s["model"] != "mine" || sl["command"] != "echo s" {
		t.Fatalf("settings: %v", s)
	}
}
