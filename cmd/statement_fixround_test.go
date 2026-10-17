package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/launcher"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

// The #217 review round: each test fails without its fix.

// A launcher that cannot be had refuses the whole statement before the
// other keys are written: in ALTER and in CREATE OR ALTER, for a name
// another playbook answers to and for a file cpb did not write.
func TestLauncherWithOtherKeysWholeOrNothing(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CPB_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	mustStmt(t, "CREATE PLAYBOOK b SET launcher = 'k'")
	mustStmt(t, "CREATE PLAYBOOK a SET launcher = 'a1'")
	mustStmt(t, "ALTER PLAYBOOK a SET model = 'before'")
	model := func() any { return settingsOf(t, filepath.Join(root, "a"))["model"] }

	if err := stmtErr(t, "CREATE OR ALTER PLAYBOOK a SET launcher = 'k', model = 'm'"); err == nil || !strings.Contains(err.Error(), "k") {
		t.Fatalf("CREATE OR ALTER with a taken launcher: %v", err)
	}
	if model() != "before" {
		t.Fatalf("CREATE OR ALTER wrote the model before refusing the launcher: %v", model())
	}

	if err := os.MkdirAll(config.LauncherDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config.LauncherDir, "squat"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		"ALTER PLAYBOOK a SET launcher = 'squat', model = 'z'",
		"CREATE OR ALTER PLAYBOOK a SET launcher = 'squat', model = 'z'",
	} {
		if err := stmtErr(t, line); err == nil || !strings.Contains(err.Error(), "did not generate") {
			t.Fatalf("%s: %v", line, err)
		}
		if model() != "before" {
			t.Fatalf("%s wrote the model before refusing the launcher: %v", line, model())
		}
	}
}

// LINK refuses a property inside SET IF UNSET as it refuses one outside it,
// before the link or the launcher exists.
func TestLinkRefusesIfUnsetPropertyFirst(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CPB_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, ".playbook"), []byte("name = \"tgt\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := stmtErr(t, "CREATE OR ALTER PLAYBOOK ln LINK "+target+" SET IF UNSET model = 'm'")
	if err == nil || !strings.Contains(err.Error(), "does not apply to LINK") {
		t.Fatalf("LINK with SET IF UNSET model: %v", err)
	}
	if _, serr := os.Lstat(filepath.Join(root, "ln")); !os.IsNotExist(serr) {
		t.Fatalf("the link was created before the refusal: %v", serr)
	}
	if _, exists, _ := launcher.Lookup(config.LauncherDir, "ln"); exists {
		t.Fatal("the launcher was written before the refusal")
	}
}

// A dry run judges SET IF UNSET against the sandbox keys its earlier
// statements set.
func TestDryRunIfUnsetSeesPlannedSandboxKeys(t *testing.T) {
	sandboxDefaultRoot(t)
	mustStmt(t, "CREATE PLAYBOOK sb SET launcher = '', login = 'isolated'")
	f := writePlaybookFile(t, "ALTER PLAYBOOK sb SET sandbox.host = 'me@box';\nALTER PLAYBOOK sb SET IF UNSET sandbox.host = 'other@box';\n")
	out, err := apply(t, f, "--dry-run")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "sandbox.host is set") {
		t.Fatalf("the dry run planned SET IF UNSET over a key its first statement sets:\n%s", out)
	}
}

// A credential inside SET IF UNSET is withheld from the APPLY record like
// one anywhere else.
func TestApplyRecordWithholdsIfUnsetCredential(t *testing.T) {
	root := sandboxDefaultRoot(t)
	writePlaybook(t, root, "rec", nil)
	f := writeCpb(t, t.TempDir(), "r.cpb", "ALTER PLAYBOOK\n  SET IF UNSET statusline.command = 'curl https://u:p@h';\n")
	if out, err := apply(t, f, "TO", "rec"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	data, err := os.ReadFile(filepath.Join(root, "rec", applyRecordFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "u:p@") {
		t.Fatalf("the record holds the credential:\n%s", data)
	}
}

// A CREATE whose SET list holds a value the follow-on ALTER would refuse
// refuses whole: no directory, no launcher.
func TestCreateRefusesBadSandboxValueFirst(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CPB_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	for _, kv := range []string{"sandbox.backend = bogus", "sandbox.workdir = rel/dir"} {
		name := "bx" + strings.Fields(kv)[0][len("sandbox."):]
		if err := stmtErr(t, "CREATE PLAYBOOK "+name+" SET "+kv); err == nil {
			t.Fatalf("CREATE … SET %s was accepted", kv)
		}
		if _, err := os.Lstat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("CREATE … SET %s left the playbook behind: %v", kv, err)
		}
		if _, exists, _ := launcher.Lookup(config.LauncherDir, name); exists {
			t.Fatalf("CREATE … SET %s left its launcher behind", kv)
		}
	}
}

// RESET and SET IF UNSET on a playbook installed FROM a source act on what
// the source brought: SHOW CREATE's RESET model re-applies to no model, and
// a second APPLY changes nothing; SET IF UNSET keeps the source's model.
func TestFromInstallResetAndIfUnset(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CPB_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "settings.json"), []byte(`{"model": "x"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "CLAUDE.md"), []byte("# src\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustStmt(t, "CREATE PLAYBOOK fr FROM "+src+" SET launcher = ''")
	mustStmt(t, "ALTER PLAYBOOK fr RESET model")
	file := writePlaybookFile(t, mustStmt(t, "SHOW CREATE PLAYBOOK fr"))
	if !strings.Contains(readFile(t, file), "RESET") {
		t.Fatalf("SHOW CREATE does not reset the model:\n%s", readFile(t, file))
	}
	mustStmt(t, "DROP PLAYBOOK fr --yes")
	if out, err := apply(t, file); err != nil || !strings.Contains(out, "1 created") {
		t.Fatalf("first APPLY: %v\n%s", err, out)
	}
	if m, ok := settingsOf(t, filepath.Join(root, "fr"))["model"]; ok {
		t.Fatalf("the source's model survived RESET model: %v", m)
	}
	if out, err := apply(t, file); err != nil || !strings.Contains(out, " 0 created, 0 changed,") {
		t.Fatalf("second APPLY: %v\n%s", err, out)
	}

	// The statement itself, not only APPLY: RESET acts on the installed copy.
	mustStmt(t, "CREATE OR ALTER PLAYBOOK fr3 FROM "+src+" SET launcher = '' RESET model")
	if m, ok := settingsOf(t, filepath.Join(root, "fr3"))["model"]; ok {
		t.Fatalf("CREATE OR ALTER … FROM … RESET model kept the source's model: %v", m)
	}

	mustStmt(t, "CREATE OR ALTER PLAYBOOK fr2 FROM "+src+" SET launcher = '' SET IF UNSET model = 'y'")
	if m := settingsOf(t, filepath.Join(root, "fr2"))["model"]; m != "x" {
		t.Fatalf("SET IF UNSET overwrote the source's model: %v", m)
	}
}

// A playbook named with a keyword has a SHOW CREATE that parses, alone and
// in SHOW CREATE ALL: its default launcher is written as RESET launcher.
func TestShowCreateKeywordNamedPlaybook(t *testing.T) {
	sandboxDefaultRoot(t)
	t.Setenv("CPB_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	var err error
	captureStdout(t, func() { err = runStatement([]string{"CREATE PLAYBOOK 'server'"}) })
	if err != nil {
		t.Fatal(err)
	}
	file := writePlaybookFile(t, mustStmt(t, "SHOW CREATE ALL"))
	if strings.Contains(readFile(t, file), "launcher = 'server'") {
		t.Fatalf("SHOW CREATE writes the default launcher as a value:\n%s", readFile(t, file))
	}
	if out, err := apply(t, file); err != nil || !strings.Contains(out, " 0 created, 0 changed,") {
		t.Fatalf("SHOW CREATE ALL does not re-apply: %v\n%s", err, out)
	}
}

// A hint names only a form the statement accepts: DEFAULTS gets no
// playbook form, and CREATE no SET VAR.
func TestHintsFitTheStatement(t *testing.T) {
	sandboxDefaultRoot(t)
	for line, bad := range map[string]string{
		"ALTER DEFAULTS SET launcher = k":               "SET launcher",
		"ALTER DEFAULTS RESET sandbox":                  "sandbox.always",
		"ALTER DEFAULTS SET IF UNSET secret_helper = x": "IF UNSET goes right after SET",
		"CREATE PLAYBOOK hx SET MODEL=x":                "a variable is SET VAR",
	} {
		err := stmtErr(t, line)
		if err == nil {
			t.Fatalf("%s was accepted", line)
		}
		if strings.Contains(err.Error(), bad) {
			t.Fatalf("%s: the hint names a form this statement refuses: %v", line, err)
		}
	}
}

// Under a --playbooks-dir root, where cpb manages no launchers, a launcher
// beside other keys is recorded as the manifest's alias, as SET launcher
// alone is, and CREATE OR ALTER converges it.
func TestLauncherUnderCustomRoot(t *testing.T) {
	root := sandboxRoot(t, "playbooks")
	mustStmt(t, "CREATE PLAYBOOK cr")
	var err error
	stderr := captureStderr(t, func() {
		captureStdout(t, func() { err = runStatement(strings.Fields("ALTER PLAYBOOK cr SET launcher = 'k1', model = 'm'")) })
	})
	if err != nil {
		t.Fatal(err)
	}
	pb, perr := playbook.Require(root, "cr")
	if perr != nil {
		t.Fatal(perr)
	}
	if pb.Alias() != "k1" {
		t.Fatalf("the launcher was dropped: alias %q", pb.Alias())
	}
	if !strings.Contains(stderr, "default playbooks root") {
		t.Fatalf("no note about the default root: %q", stderr)
	}
	mustStmt(t, "CREATE OR ALTER PLAYBOOK cr SET launcher = 'k2'")
	if pb, _ := playbook.Require(root, "cr"); pb.Alias() != "k2" {
		t.Fatalf("CREATE OR ALTER did not converge the alias: %q", pb.Alias())
	}
	f := writePlaybookFile(t, "CREATE OR ALTER PLAYBOOK cr SET launcher = 'k2';\n")
	if out, err := apply(t, f); err != nil || !strings.Contains(out, " 0 created, 0 changed,") {
		t.Fatalf("again: %v\n%s", err, out)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
