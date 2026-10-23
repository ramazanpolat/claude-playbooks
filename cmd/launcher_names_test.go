package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/launcher"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

// pathWithCommand puts a directory holding an executable named cmd first on
// PATH and returns the executable's path.
func pathWithCommand(t *testing.T, cmd string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, cmd)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return p
}

// A new launcher never takes the name of the command cpb launches, in any
// case: as the default launcher of a playbook named so, or set explicitly.
func TestNewLauncherRefusesClaude(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CPB_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	if err := stmtErr(t, "CREATE PLAYBOOK claude"); err == nil || !strings.Contains(err.Error(), "the command cpb launches") {
		t.Fatalf("CREATE PLAYBOOK claude: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "claude")); err == nil {
		t.Fatal("the refused playbook joined the registry")
	}
	if err := stmtErr(t, "CREATE PLAYBOOK c SET launcher = 'CLAUDE'"); err == nil {
		t.Fatal("SET launcher = 'CLAUDE' accepted")
	}
	mustStmt(t, "CREATE PLAYBOOK claude SET launcher = ''")
	if _, err := os.Lstat(filepath.Join(root, "claude")); err != nil {
		t.Fatalf("a playbook named claude without a launcher: %v", err)
	}
}

// A new launcher never shadows a command on PATH: not as a playbook's
// default launcher, an explicit one, an ALTER, a RESET or a RENAME. Each
// refusal comes before anything is registered and names the command.
func TestNewLauncherRefusesCommandOnPath(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CPB_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	tool := pathWithCommand(t, "sometool")

	for _, line := range []string{
		"CREATE PLAYBOOK sometool",
		"CREATE PLAYBOOK p SET launcher = 'sometool'",
	} {
		if err := stmtErr(t, line); err == nil || !strings.Contains(err.Error(), tool) {
			t.Fatalf("%s: %v", line, err)
		}
	}
	if pbs, _ := playbook.Discover(root); len(pbs) != 0 {
		t.Fatalf("a refused CREATE registered %d playbook(s)", len(pbs))
	}

	mustStmt(t, "CREATE PLAYBOOK q")
	for _, line := range []string{
		"ALTER PLAYBOOK q SET launcher = 'sometool'",
		"ALTER PLAYBOOK q RENAME TO sometool",
	} {
		if err := stmtErr(t, line); err == nil || !strings.Contains(err.Error(), tool) {
			t.Fatalf("%s: %v", line, err)
		}
	}
	if _, err := playbook.Require(root, "q"); err != nil {
		t.Fatalf("a refused statement moved q: %v", err)
	}
	if _, exists, _ := launcher.Lookup(config.LauncherDir, "sometool"); exists {
		t.Fatal("a launcher shadowing a PATH command was written")
	}

	// The escape: no launcher, or another name.
	mustStmt(t, "CREATE PLAYBOOK sometool SET launcher = ''")
	mustStmt(t, "ALTER PLAYBOOK q RENAME TO q2 SET launcher = 'q2-run'")
	if err := stmtErr(t, "ALTER PLAYBOOK sometool RESET launcher"); err == nil || !strings.Contains(err.Error(), tool) {
		t.Fatalf("RESET launcher onto a PATH command: %v", err)
	}
}

// A launcher cpb already wrote is kept: an existing launcher on PATH is not
// a collision with itself, so re-applying and repairing it stay quiet.
func TestExistingLauncherIsNotItsOwnCollision(t *testing.T) {
	sandboxDefaultRoot(t)
	t.Setenv("CPB_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	mustStmt(t, "CREATE PLAYBOOK mine SET launcher = 'mine-run'")
	t.Setenv("PATH", config.LauncherDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	mustStmt(t, "CREATE OR ALTER PLAYBOOK mine SET launcher = 'mine-run'")
	mustStmt(t, "ALTER PLAYBOOK mine RESET launcher")
	mustStmt(t, "ALTER PLAYBOOK mine SET launcher = 'mine-run'")
}

// A source never chooses the launcher, and a stated login wins over the
// source's: the statement's values, or the playbook's own name, are what
// the install records.
func TestFromSourceStatementWinsLauncherAndLogin(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CPB_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	src := gitSource(t, map[string]string{
		".playbook": "name = \"src\"\nlauncher = \"src-l\"\nisolated_login = true\n",
		"CLAUDE.md": "# src\n",
	})
	read := func(name string) *manifest.Manifest {
		t.Helper()
		m, err := manifest.Read(filepath.Join(root, name))
		if err != nil || m == nil {
			t.Fatalf("manifest of %s: %v", name, err)
		}
		return m
	}

	// Unstated: the source's isolation stays, its launcher does not.
	mustStmt(t, "CREATE PLAYBOOK u FROM "+src)
	if m := read("u"); m.Launcher != "" || !m.IsolatedLogin {
		t.Fatalf("unstated: launcher %q, isolated %v", m.Launcher, m.IsolatedLogin)
	}
	if _, exists, _ := launcher.Lookup(config.LauncherDir, "src-l"); exists {
		t.Fatal("the source's launcher was written")
	}
	if _, exists, foreign := launcher.Lookup(config.LauncherDir, "u"); !exists || foreign {
		t.Fatal("no launcher u")
	}

	// Stated: login = 'shared' and the launcher win.
	mustStmt(t, "CREATE PLAYBOOK s FROM "+src+" SET login = 'shared', launcher = 's-run'")
	if m := read("s"); m.Launcher != "s-run" || m.IsolatedLogin {
		t.Fatalf("stated: launcher %q, isolated %v", m.Launcher, m.IsolatedLogin)
	}

	// No launcher at all.
	mustStmt(t, "CREATE PLAYBOOK n FROM "+src+" SET launcher = ''")
	if m := read("n"); m.Launcher != "" {
		t.Fatalf("launcher = '': %q", m.Launcher)
	}
	if _, exists, _ := launcher.Lookup(config.LauncherDir, "n"); exists {
		t.Fatal("launcher = '' wrote a launcher")
	}
}

// APPLY --dry-run refuses what the real run refuses about names, judged
// against the registry the file's own earlier statements leave: a rename
// onto an existing playbook, a launcher another playbook answers to, a
// launcher shadowing a command. A name an earlier statement frees is free.
func TestDryRunChecksNamesLikeTheRun(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CPB_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	pathWithCommand(t, "sometool")
	mustStmt(t, "CREATE PLAYBOOK a")
	mustStmt(t, "CREATE PLAYBOOK b SET launcher = 'bee'")

	for _, tc := range []struct{ text, want string }{
		{"ALTER PLAYBOOK a RENAME TO b;\n", "already exists"},
		{"CREATE PLAYBOOK c SET launcher = 'bee';\n", "addresses playbook \"b\""},
		{"CREATE PLAYBOOK sometool;\n", "sometool"},
		{"ALTER PLAYBOOK a SET launcher = 'sometool';\n", "sometool"},
		{"ALTER PLAYBOOK a RENAME TO bee;\n", "addresses playbook \"b\""},
		// Last: its real run creates e before it refuses f.
		{"CREATE PLAYBOOK e;\nCREATE PLAYBOOK f SET launcher = 'e';\n", "addresses playbook \"e\""},
	} {
		file := writePlaybookFile(t, tc.text)
		_, dryErr := apply(t, file, "--dry-run")
		if dryErr == nil || !strings.Contains(dryErr.Error(), tc.want) {
			t.Fatalf("dry run of %q: %v (want %q)", tc.text, dryErr, tc.want)
		}
		if _, err := apply(t, file); err == nil {
			t.Fatalf("the real run of %q passed where the dry run refused", tc.text)
		}
		if _, err := playbook.Require(root, "a"); err != nil {
			t.Fatalf("after %q: %v", tc.text, err)
		}
	}

	// Freed names: a dropped or renamed-away playbook claims nothing.
	for _, text := range []string{
		"DROP PLAYBOOK b;\nCREATE PLAYBOOK c SET launcher = 'bee';\n",
		"ALTER PLAYBOOK b RENAME TO b2 SET launcher = 'b2';\nCREATE PLAYBOOK c SET launcher = 'bee';\n",
	} {
		if out, err := apply(t, writePlaybookFile(t, text), "--dry-run", "--yes"); err != nil {
			t.Fatalf("dry run of %q: %v\n%s", text, err, out)
		}
	}
}

// A dry run sees a launcher an earlier ALTER gives a playbook on disk: the
// launcher it gives up is free, and the one it takes is taken.
func TestDryRunSeesAlteredLaunchers(t *testing.T) {
	sandboxDefaultRoot(t)
	t.Setenv("CPB_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	mustStmt(t, "CREATE PLAYBOOK a SET launcher = 'old-run'")

	free := "ALTER PLAYBOOK a SET launcher = 'new-run';\nCREATE PLAYBOOK b SET launcher = 'old-run';\n"
	if out, err := apply(t, writePlaybookFile(t, free), "--dry-run"); err != nil {
		t.Fatalf("a launcher an earlier ALTER gave up: %v\n%s", err, out)
	}
	taken := "ALTER PLAYBOOK a SET launcher = 'x-run';\nCREATE PLAYBOOK c SET launcher = 'x-run';\n"
	if _, err := apply(t, writePlaybookFile(t, taken), "--dry-run"); err == nil || !strings.Contains(err.Error(), "addresses playbook \"a\"") {
		t.Fatalf("a launcher an earlier ALTER took: %v", err)
	}
}

// RESET launcher onto a name that cannot be a launcher is refused before
// the launcher in place is retired: the playbook keeps it.
func TestRefusedResetKeepsTheLauncher(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CPB_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	mustStmt(t, "CREATE PLAYBOOK othertool SET launcher = 'ot-run'")
	tool := pathWithCommand(t, "othertool")
	if err := stmtErr(t, "ALTER PLAYBOOK othertool RESET launcher"); err == nil || !strings.Contains(err.Error(), tool) {
		t.Fatalf("RESET launcher onto a PATH command: %v", err)
	}
	pb, err := playbook.Require(root, "othertool")
	if err != nil {
		t.Fatal(err)
	}
	if pb.Alias() != "ot-run" {
		t.Fatalf("a refused RESET changed the launcher to %q", pb.Alias())
	}
	if _, exists, foreign := launcher.Lookup(config.LauncherDir, "ot-run"); !exists || foreign {
		t.Fatal("a refused RESET retired the launcher ot-run")
	}
}

// A launcher named claude that an earlier release wrote keeps working:
// statements that keep it parse and apply, and it can be removed. Only a
// new one is refused.
func TestExistingClaudeLauncherIsKept(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CPB_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	writePlaybook(t, root, "old", &manifest.Manifest{Launcher: "claude"})
	if _, err := launcher.Write(config.LauncherDir, "claude"); err != nil {
		t.Fatal(err)
	}
	mustStmt(t, "ALTER PLAYBOOK old SET launcher = 'claude'")
	mustStmt(t, "CREATE OR ALTER PLAYBOOK old SET launcher = 'claude'")
	file := writePlaybookFile(t, mustStmt(t, "SHOW CREATE PLAYBOOK old"))
	if out, err := apply(t, file); err != nil || !strings.Contains(out, "0 created, 0 changed") {
		t.Fatalf("re-applying the SHOW CREATE of a claude launcher: %v\n%s", err, out)
	}
	if err := stmtErr(t, "CREATE PLAYBOOK new SET launcher = 'claude'"); err == nil {
		t.Fatal("a second, new claude launcher was accepted")
	}
	mustStmt(t, "ALTER PLAYBOOK old SET launcher = ''")
	if _, exists, _ := launcher.Lookup(config.LauncherDir, "claude"); exists {
		t.Fatal("the claude launcher was not removed")
	}
}

// A RENAME TO keeps a launcher an earlier release made under older rules
// when the launcher's name does not change; only a new name is checked.
func TestRenameKeepsAnExistingLauncherName(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CPB_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	writePlaybook(t, root, "a", &manifest.Manifest{Launcher: "claude"})
	if _, err := launcher.Write(config.LauncherDir, "claude"); err != nil {
		t.Fatal(err)
	}
	file := writePlaybookFile(t, "ALTER PLAYBOOK a RENAME TO b SET launcher = 'claude';\n")
	if out, err := apply(t, file, "--dry-run"); err != nil {
		t.Fatalf("dry run of a rename keeping launcher claude: %v\n%s", err, out)
	}
	mustStmt(t, "ALTER PLAYBOOK a RENAME TO b SET launcher = 'claude'")
	pb, err := playbook.Require(root, "b")
	if err != nil {
		t.Fatal(err)
	}
	if pb.Alias() != "claude" {
		t.Fatalf("launcher after the rename: %q", pb.Alias())
	}
	if _, exists, foreign := launcher.Lookup(config.LauncherDir, "claude"); !exists || foreign {
		t.Fatal("the kept launcher claude is gone")
	}
	// An earlier statement that gives the launcher up makes it new again,
	// in the dry run as in the run.
	reset := writePlaybookFile(t, "ALTER PLAYBOOK b RESET launcher;\nALTER PLAYBOOK b RENAME TO b2 SET launcher = 'claude';\n")
	if _, err := apply(t, reset, "--dry-run"); err == nil {
		t.Fatal("the dry run kept a claude launcher an earlier RESET gave up")
	}
	if _, err := apply(t, reset); err == nil {
		t.Fatal("the run kept a claude launcher an earlier RESET gave up")
	}
	mustStmt(t, "ALTER PLAYBOOK b SET launcher = 'b-run'")
	// A new name is still checked.
	if err := stmtErr(t, "ALTER PLAYBOOK b RENAME TO c SET launcher = 'CLAUDE2'"); err != nil {
		t.Fatalf("an ordinary new launcher: %v", err)
	}
	mustStmt(t, "CREATE PLAYBOOK d SET launcher = 'd-run'")
	if err := stmtErr(t, "ALTER PLAYBOOK d RENAME TO e SET launcher = 'Claude'"); err == nil {
		t.Fatal("a rename onto a new claude launcher was accepted")
	}
}

// A new launcher name never starts with a dash or holds a control
// character.
func TestNewLauncherRefusesDashAndControlCharacters(t *testing.T) {
	for _, name := range []string{"-x", "--help", "a\x01b", "a\x7fb"} {
		if err := launcher.CheckNew(name); err == nil {
			t.Fatalf("CheckNew(%q) accepted", name)
		}
	}
	if err := launcher.CheckNew("ok-name"); err != nil {
		t.Fatal(err)
	}
	sandboxDefaultRoot(t)
	t.Setenv("CPB_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	if err := stmtErr(t, "CREATE PLAYBOOK p SET launcher = '-p'"); err == nil {
		t.Fatal("SET launcher = '-p' accepted")
	}
}

// A rename carries the launcher the playbook has. With none, or a custom
// one, the new name is only a directory name, so it may be a command on
// PATH; with the default launcher, the launcher follows the name and the
// new name must be free. The dry run and the run agree on each.
func TestRenameCarriesTheLauncherItHas(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CPB_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	tool := pathWithCommand(t, "sometool")
	noLauncher := func(name string) {
		t.Helper()
		if _, exists, _ := launcher.Lookup(config.LauncherDir, name); exists {
			t.Fatalf("a launcher %s was written", name)
		}
	}

	// None, created in the same file: nothing to check, nothing written.
	none := writePlaybookFile(t, "CREATE PLAYBOOK scratch SET launcher = '';\nALTER PLAYBOOK scratch RENAME TO sometool;\n")
	if out, err := apply(t, none, "--dry-run"); err != nil {
		t.Fatalf("dry run, no launcher: %v\n%s", err, out)
	}
	if out, err := apply(t, none); err != nil {
		t.Fatalf("run, no launcher: %v\n%s", err, out)
	}
	noLauncher("sometool")
	mustStmt(t, "DROP PLAYBOOK sometool --yes")

	// Custom, on disk: kept, and the new name gets no launcher.
	mustStmt(t, "CREATE PLAYBOOK c SET launcher = 'c-pb'")
	custom := writePlaybookFile(t, "ALTER PLAYBOOK c RENAME TO sometool;\n")
	if out, err := apply(t, custom, "--dry-run"); err != nil {
		t.Fatalf("dry run, custom launcher: %v\n%s", err, out)
	}
	if out, err := apply(t, custom); err != nil {
		t.Fatalf("run, custom launcher: %v\n%s", err, out)
	}
	noLauncher("sometool")
	pb, err := playbook.Require(root, "sometool")
	if err != nil {
		t.Fatal(err)
	}
	if pb.Alias() != "c-pb" {
		t.Fatalf("custom launcher after the rename: %q", pb.Alias())
	}
	if _, exists, foreign := launcher.Lookup(config.LauncherDir, "c-pb"); !exists || foreign {
		t.Fatal("the kept launcher c-pb is gone")
	}
	mustStmt(t, "DROP PLAYBOOK sometool --yes")

	// Default: the launcher follows the name, which a command holds.
	mustStmt(t, "CREATE PLAYBOOK d")
	def := writePlaybookFile(t, "ALTER PLAYBOOK d RENAME TO sometool;\n")
	if _, err := apply(t, def, "--dry-run"); err == nil || !strings.Contains(err.Error(), tool) {
		t.Fatalf("dry run, default launcher: %v", err)
	}
	if _, err := apply(t, def); err == nil || !strings.Contains(err.Error(), tool) {
		t.Fatalf("run, default launcher: %v", err)
	}
	if _, err := playbook.Require(root, "d"); err != nil {
		t.Fatalf("a refused rename moved d: %v", err)
	}
	// Created in the same file with its default launcher: refused too.
	inFile := writePlaybookFile(t, "CREATE PLAYBOOK e;\nALTER PLAYBOOK e RENAME TO sometool;\n")
	if _, err := apply(t, inFile, "--dry-run"); err == nil || !strings.Contains(err.Error(), tool) {
		t.Fatalf("dry run, default launcher created in the file: %v", err)
	}
}

// A playbook whose launcher is its own name keeps that launcher across a
// rename that restates it, made under whatever rules it was made under.
func TestRenameKeepsTheNameLauncherItRestates(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CPB_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	writePlaybook(t, root, "claude", &manifest.Manifest{})
	if _, err := launcher.Write(config.LauncherDir, "claude"); err != nil {
		t.Fatal(err)
	}
	file := writePlaybookFile(t, "ALTER PLAYBOOK claude RENAME TO old-claude SET launcher = 'claude';\n")
	if out, err := apply(t, file, "--dry-run"); err != nil {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	if out, err := apply(t, file); err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	pb, err := playbook.Require(root, "old-claude")
	if err != nil {
		t.Fatal(err)
	}
	if pb.Alias() != "claude" {
		t.Fatalf("launcher after the rename: %q", pb.Alias())
	}
	if _, exists, foreign := launcher.Lookup(config.LauncherDir, "claude"); !exists || foreign {
		t.Fatal("the kept launcher claude is gone")
	}
}

// The lifecycle a playbook with a custom launcher goes through (the arena's
// playbook-lifecycle suite): a rename retires the old name's launcher and
// gives the new name one, and the custom launcher stays.
func TestRenameWithCustomLauncherGivesTheNewNameALauncher(t *testing.T) {
	sandboxDefaultRoot(t)
	t.Setenv("CPB_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	exists := func(name string) bool {
		_, ok, foreign := launcher.Lookup(config.LauncherDir, name)
		return ok && !foreign
	}
	for _, steps := range [][]string{
		{"CREATE PLAYBOOK lifecycle-pb SET launcher = 'lcpb'"}, // the custom launcher only
		{"CREATE PLAYBOOK lifecycle-pb", "ALTER PLAYBOOK lifecycle-pb SET launcher = 'lcpb'"},
		{"CREATE PLAYBOOK lifecycle-pb", "ALTER PLAYBOOK lifecycle-pb SET launcher = 'lcpb'",
			"ALTER PLAYBOOK lifecycle-pb SET launcher = ''", "ALTER PLAYBOOK lifecycle-pb SET launcher = 'lcpb'"},
	} {
		for _, s := range steps {
			mustStmt(t, s)
		}
		file := writePlaybookFile(t, "ALTER PLAYBOOK lifecycle-pb RENAME TO lifecycle-pb2;\n")
		if out, err := apply(t, file, "--dry-run"); err != nil {
			t.Fatalf("dry run: %v\n%s", err, out)
		}
		mustStmt(t, "ALTER PLAYBOOK lifecycle-pb RENAME TO lifecycle-pb2")
		if exists("lifecycle-pb") || !exists("lifecycle-pb2") || !exists("lcpb") {
			t.Fatalf("after %v and the rename: lifecycle-pb %v, lifecycle-pb2 %v, lcpb %v",
				steps, exists("lifecycle-pb"), exists("lifecycle-pb2"), exists("lcpb"))
		}
		// Both commands dispatch to the renamed playbook.
		for _, cmd := range []string{"lcpb", "lifecycle-pb2"} {
			if got := dispatchesTo(t, cmd); got != "lifecycle-pb2" {
				t.Fatalf("launcher %s dispatches to %q", cmd, got)
			}
		}
		mustStmt(t, "DROP PLAYBOOK lifecycle-pb2 --yes")
		if exists("lifecycle-pb2") || exists("lcpb") {
			t.Fatal("DROP left a launcher")
		}
	}
}

// dispatchesTo is the playbook a launcher in the launcher directory runs.
func dispatchesTo(t *testing.T, cmd string) string {
	t.Helper()
	saved := os.Args
	defer func() { os.Args = saved }()
	os.Args = append([]string{filepath.Join(config.LauncherDir, cmd)}, saved[1:]...)
	name, ok, err := multicallPlaybook()
	if err != nil || !ok {
		t.Fatalf("launcher %s: %v (resolved %v)", cmd, err, ok)
	}
	return name
}
