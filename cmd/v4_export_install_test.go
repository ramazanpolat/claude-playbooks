package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// codeLines is a SHOW CREATE text without its comment lines.
func codeLines(text string) string {
	var keep []string
	for _, l := range strings.Split(text, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(l), "--") {
			keep = append(keep, l)
		}
	}
	return strings.Join(keep, "\n")
}

// A --skip-secrets export applied back keeps the withheld value: the set is
// written as a create-if-missing plus an additive ALTER, never CREATE OR
// REPLACE, which would rebuild it without the value.
func TestSkipSecretsExportKeepsTheWithheldValue(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	const secret = "sk-live-0123456789abcdef"
	mustStmt(t, "CREATE ENV r SET ANTHROPIC_AUTH_TOKEN="+secret+" MODEL=glm AS PLAINTEXT")
	mustStmt(t, "ALTER ENV r BLOCK HTTP_PROXY DESCRIPTION router") // as the shell passes it, unquoted
	out, err := stmt(t, "SHOW CREATE ENV r --skip-secrets")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, secret) || strings.Contains(out, secret[:6]) {
		t.Fatalf("SHOW CREATE printed the secret:\n%s", out)
	}
	code := codeLines(out)
	for _, want := range []string{"CREATE ENV IF NOT EXISTS r;", "ALTER ENV r", "MODEL=glm", "HTTP_PROXY", "DESCRIPTION 'router'"} {
		if !strings.Contains(code, want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "CREATE OR REPLACE ENV r") || !strings.Contains(out, "keys added to it since are not removed") {
		t.Fatalf("not the keeping form, or no comment saying so:\n%s", out)
	}
	f := writePlaybookFile(t, out)
	for round := 1; round <= 2; round++ {
		if o, err := apply(t, f); err != nil {
			t.Fatalf("round %d: %v\n%s", round, err, o)
		}
		p := readProfile(t, "r")
		if p.Set["ANTHROPIC_AUTH_TOKEN"] != secret || p.Set["MODEL"] != "glm" || !slices.Contains(p.Block, "HTTP_PROXY") || p.Description != "router" {
			t.Fatalf("round %d: the set after APPLY: %+v", round, p)
		}
	}
	// A machine without the set gets it, without the withheld key.
	mustStmt(t, "DROP ENV r")
	if o, err := apply(t, f); err != nil {
		t.Fatalf("%v\n%s", err, o)
	}
	p := readProfile(t, "r")
	if _, ok := p.Set["ANTHROPIC_AUTH_TOKEN"]; ok || p.Set["MODEL"] != "glm" {
		t.Fatalf("a fresh set: %+v", p)
	}
}

// A set holding nothing but a withheld literal is a bare create: no empty
// ALTER, which would not parse.
func TestSkipSecretsExportOfAnAllWithheldSet(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	const secret = "sk-live-fedcba9876543210"
	mustStmt(t, "CREATE ENV s SET ANTHROPIC_AUTH_TOKEN="+secret+" AS PLAINTEXT")
	out, err := stmt(t, "SHOW CREATE ALL --skip-secrets")
	if err != nil {
		t.Fatal(err)
	}
	code := codeLines(out)
	if !strings.Contains(code, "CREATE ENV IF NOT EXISTS s;") || strings.Contains(code, "ALTER ENV s") {
		t.Fatalf("an all-withheld set:\n%s", out)
	}
	if o, err := apply(t, writePlaybookFile(t, out)); err != nil {
		t.Fatalf("%v\n%s", err, o)
	}
	if readProfile(t, "s").Set["ANTHROPIC_AUTH_TOKEN"] != secret {
		t.Fatal("the stored value was lost")
	}
}

// A set with nothing withheld keeps the exact form.
func TestShowCreateEnvWithoutWithheldStaysExact(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	mustStmt(t, "CREATE ENV plain SET MODEL=glm")
	out := mustStmt(t, "SHOW CREATE ENV plain")
	if !strings.Contains(out, "CREATE OR REPLACE ENV plain") || strings.Contains(out, "IF NOT EXISTS") {
		t.Fatalf("a set with nothing withheld:\n%s", out)
	}
}

func recordsSource(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "skills", "s1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("source\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skills", "s1", "SKILL.md"), []byte("skill\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := &manifest.Manifest{
		Name:   "author",
		Play:   &manifest.Play{Ref: "author-recipe", SHA256: "aa", PlayedAt: "2026-01-01T00:00:00Z"},
		Apply:  &manifest.Apply{Files: []string{"/home/user/author.cpb"}, SHA256: "bb", AppliedAt: "2026-01-01T00:00:00Z"},
		Skills: map[string]*manifest.SkillRecord{"s1": {Source: "/home/user/skills/s1", Mode: "link"}},
		MCP:    map[string]*manifest.MCPRecord{"db": {Vars: []string{"DB_URL"}}},
	}
	if err := manifest.Write(dir, m); err != nil {
		t.Fatal(err)
	}
}

// An install drops the source's own records and keeps the files it ships.
func TestInstallDropsTheSourcesRecords(t *testing.T) {
	resetCommandTestState(t)
	root := t.TempDir()
	config.PlaybooksDir = filepath.Join(root, "playbooks")
	source := filepath.Join(root, "source")
	recordsSource(t, source)
	stderr := captureStderr(t, func() {
		if err := doInstall(installOpts{name: "pb", noLauncher: true}, []string{source}); err != nil {
			t.Fatal(err)
		}
	})
	m, err := manifest.Read(filepath.Join(config.PlaybooksDir, "pb"))
	if err != nil || m == nil {
		t.Fatalf("installed manifest: %v", err)
	}
	if m.Play != nil || m.Apply != nil || m.Skills != nil || m.MCP != nil {
		t.Fatalf("a source record was adopted: %+v", m)
	}
	for _, r := range []string{"[play]", "[apply]", "[skills]", "[mcp]"} {
		if !strings.Contains(stderr, "ignoring the "+r+" record") {
			t.Fatalf("no note for %s:\n%s", r, stderr)
		}
	}
	if _, err := os.Stat(filepath.Join(config.PlaybooksDir, "pb", "skills", "s1", "SKILL.md")); err != nil {
		t.Fatalf("a shipped skill file was dropped: %v", err)
	}
}

// update takes cpb's records from the live manifest, nil included: a
// source's record never fills one the install does not have.
func TestUpdateKeepsTheLiveRecords(t *testing.T) {
	resetCommandTestState(t)
	root := t.TempDir()
	config.PlaybooksDir = filepath.Join(root, "playbooks")
	source := filepath.Join(root, "source")
	recordsSource(t, source)
	installed := filepath.Join(config.PlaybooksDir, "pb")
	if err := os.MkdirAll(installed, 0o755); err != nil {
		t.Fatal(err)
	}
	live := &manifest.Apply{Files: []string{"/home/user/mine.cpb"}, SHA256: "cc", AppliedAt: "2026-02-02T00:00:00Z"}
	if err := manifest.Write(installed, &manifest.Manifest{Source: &manifest.Source{Repository: source}, Apply: live}); err != nil {
		t.Fatal(err)
	}
	if err := updateOnePlaybook("pb", false); err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Read(installed)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m.Apply, live) || m.Play != nil || m.Skills != nil || m.MCP != nil {
		t.Fatalf("after update: apply=%+v play=%+v skills=%v mcp=%v", m.Apply, m.Play, m.Skills, m.MCP)
	}
}

// A source URL carrying a credential is refused before anything is
// fetched, with the URL masked; a user name alone on ssh is an account.
func TestCredentialSourceURLIsRefused(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	const token = "ghp_synthetic0123456789abcdef"
	for _, src := range []string{
		"https://" + token + "@git.example/o/r.git",
		"https://user:" + token + "@git.example/o/r.git",
	} {
		for _, dry := range []bool{false, true} {
			line := "CREATE PLAYBOOK p FROM " + src + " SET launcher = ''"
			var err error
			if dry {
				_, err = apply(t, writePlaybookFile(t, line+";\n"), "--dry-run")
			} else {
				err = stmtErr(t, line)
			}
			if err == nil || !strings.Contains(err.Error(), "carries a credential") || strings.Contains(err.Error(), token) {
				t.Fatalf("%s (dry %v): %v", src, dry, err)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(config.ResolvePlaybooksDir(), "p")); !os.IsNotExist(err) {
		t.Fatal("a refused source was installed")
	}
	for _, ok := range []string{"ssh://git@git.example/o/r.git", "git@git.example:o/r.git", "https://git.example/o/r.git"} {
		if err := refuseCredentialSource(ok); err != nil {
			t.Fatalf("%s: %v", ok, err)
		}
	}
}

// An install recorded with a credential URL (before the refusal) never
// shows it: SHOW masks it and SHOW CREATE withholds the statement.
func TestRecordedCredentialSourceIsMasked(t *testing.T) {
	resetCommandTestState(t)
	root := sandboxDefaultRoot(t)
	const token = "ghp_synthetic0123456789abcdef"
	writePlaybook(t, root, "old", &manifest.Manifest{Source: &manifest.Source{Repository: "https://" + token + "@git.example/o/r.git"}})
	for _, line := range []string{"SHOW PLAYBOOK old", "SHOW PLAYBOOK old --json", "SHOW PLAYBOOKS", "SHOW CREATE PLAYBOOK old --skip-secrets"} {
		out, _ := stmt(t, line)
		if strings.Contains(out, token) {
			t.Fatalf("%s printed the token:\n%s", line, out)
		}
	}
	out, _ := stmt(t, "SHOW CREATE PLAYBOOK old --skip-secrets")
	if !strings.Contains(out, "-- withheld: the source URL of PLAYBOOK old carries credentials") {
		t.Fatalf("SHOW CREATE did not withhold the statement:\n%s", out)
	}
	if got := redactSourceURL("ssh://git@git.example/o/r.git"); got != "ssh://git@git.example/o/r.git" {
		t.Fatalf("an ssh account name was masked: %s", got)
	}
}

// After an update the backup holds only replaced files: no manifest, no
// preserved file, and no earlier update's backup.
func TestUpdateKeepsOneBackupWithoutValues(t *testing.T) {
	resetCommandTestState(t)
	root := t.TempDir()
	config.PlaybooksDir = filepath.Join(root, "playbooks")
	source := filepath.Join(root, "source")
	installed := filepath.Join(config.PlaybooksDir, "pb")
	for _, d := range []string{source, installed} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(source, "CLAUDE.md"), "new\n")
	write(filepath.Join(source, "settings.json"), "{}\n")
	write(filepath.Join(installed, "CLAUDE.md"), "old\n")
	write(filepath.Join(installed, "settings.json"), "{\"env\":{\"K\":\"v\"}}\n")
	const value = "plain-value-0123"
	m := &manifest.Manifest{Source: &manifest.Source{Repository: source}, Env: &manifest.Env{Set: map[string]string{"API_KEY": value}}}
	if err := manifest.Write(installed, m); err != nil {
		t.Fatal(err)
	}
	// An earlier update's backup, and another playbook's that only shares
	// the prefix.
	stale := filepath.Join(config.PlaybooksDir, ".pb.bak.20200101T000000.000000000")
	other := filepath.Join(config.PlaybooksDir, ".pb.bak.bak.20200101T000000.000000000")
	for _, d := range []string{stale, other} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for round := 1; round <= 2; round++ {
		if err := updateOnePlaybook("pb", false); err != nil {
			t.Fatal(err)
		}
		backups := updateBackups(installed)
		if len(backups) != 1 || backups[0] == stale {
			t.Fatalf("round %d: backups %v", round, backups)
		}
		for _, gone := range []string{manifest.FileName, "settings.json"} {
			if _, err := os.Lstat(filepath.Join(backups[0], gone)); !os.IsNotExist(err) {
				t.Fatalf("round %d: the backup kept %s", round, gone)
			}
		}
		if got, err := os.ReadFile(filepath.Join(backups[0], "CLAUDE.md")); err != nil || (round == 1 && string(got) != "old\n") {
			t.Fatalf("round %d: the replaced file is not in the backup: %q %v", round, got, err)
		}
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("another playbook's backup was removed: %v", err)
	}
	if live, _ := manifest.Read(installed); live == nil || live.Env == nil || live.Env.Set["API_KEY"] != value {
		t.Fatal("the update lost the install's [env] values")
	}
	// DROP takes the last backup with it.
	if err := doDelete(deleteOpts{yes: true}, []string{"pb"}); err != nil {
		t.Fatal(err)
	}
	if b := updateBackups(installed); len(b) != 0 {
		t.Fatalf("DROP left backups: %v", b)
	}
}

// A LINKed playbook whose target is gone can be dropped: the link goes,
// the (missing) target is not touched.
func TestDropDanglingLink(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, manifest.FileName), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	mustStmt(t, "CREATE PLAYBOOK dev LINK "+target+" SET launcher = ''")
	if err := os.RemoveAll(target); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(config.ResolvePlaybooksDir(), "dev")
	if out, err := stmt(t, "DROP PLAYBOOK dev --yes"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatalf("the dangling link is still there: %v", err)
	}
	if err := stmtErr(t, "DROP PLAYBOOK dev --yes"); err == nil {
		t.Fatal("a second drop of the same name must still say unknown")
	}
}

// The backup an incomplete rollback kept is the only copy of what could not
// be put back: no later update removes it, and DROP names it and keeps it.
func TestIncompleteRollbackBackupSurvives(t *testing.T) {
	resetCommandTestState(t)
	root := t.TempDir()
	config.PlaybooksDir = filepath.Join(root, "playbooks")
	source := filepath.Join(root, "source")
	installed := filepath.Join(config.PlaybooksDir, "pb")
	for _, d := range []string{source, installed} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "CLAUDE.md"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installed, "CLAUDE.md"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Write(installed, &manifest.Manifest{Source: &manifest.Source{Repository: source}}); err != nil {
		t.Fatal(err)
	}
	// One kept by name, one by its marker (the rename failed).
	renamed := keepIncompleteBackup(mkdirAll(t, filepath.Join(config.PlaybooksDir, ".pb.bak.20200101T000000.000000000")))
	if !strings.HasSuffix(renamed, incompleteSuffix) {
		t.Fatalf("not marked by name: %s", renamed)
	}
	marked := mkdirAll(t, filepath.Join(config.PlaybooksDir, ".pb.bak.20200102T000000.000000000"))
	if err := os.WriteFile(filepath.Join(marked, incompleteMarker), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := updateOnePlaybook("pb", false); err != nil {
		t.Fatal(err)
	}
	if got := incompleteBackups(installed); len(got) != 2 {
		t.Fatalf("incomplete backups after an update: %v", got)
	}
	if got := updateBackups(installed); len(got) != 1 || got[0] == renamed || got[0] == marked {
		t.Fatalf("the update's own backups: %v", got)
	}
	stderr := captureStderr(t, func() {
		if err := doDelete(deleteOpts{yes: true}, []string{"pb"}); err != nil {
			t.Fatal(err)
		}
	})
	for _, b := range []string{renamed, marked} {
		if _, err := os.Stat(b); err != nil {
			t.Fatalf("DROP removed %s: %v", b, err)
		}
		if !strings.Contains(stderr, "Kept "+b+": the backup of an update whose rollback did not complete") {
			t.Fatalf("DROP did not name %s:\n%s", b, stderr)
		}
	}
	if got := updateBackups(installed); len(got) != 0 {
		t.Fatalf("DROP left the update's backup: %v", got)
	}
}

func mkdirAll(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A nested preserved path is removed from the backup too, never through a
// symlinked ancestor.
func TestUpdateBackupDropsNestedPreservedPaths(t *testing.T) {
	resetCommandTestState(t)
	root := t.TempDir()
	config.PlaybooksDir = filepath.Join(root, "playbooks")
	source := filepath.Join(root, "source")
	installed := filepath.Join(config.PlaybooksDir, "pb")
	outside := mkdirAll(t, filepath.Join(root, "outside"))
	for _, d := range []string{filepath.Join(source, "config"), filepath.Join(installed, "config"), filepath.Join(source, "linked"), filepath.Join(installed, "linked")} {
		mkdirAll(t, d)
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(source, "config", "local.json"), "stock\n")
	write(filepath.Join(source, "config", "other"), "new\n")
	write(filepath.Join(installed, "config", "local.json"), "mine\n")
	write(filepath.Join(installed, "config", "other"), "old\n")
	write(filepath.Join(outside, "keep.json"), "outside\n")
	m := &manifest.Manifest{Source: &manifest.Source{Repository: source}, Update: &manifest.Update{Preserve: []string{"config/local.json"}}}
	if err := manifest.Write(installed, m); err != nil {
		t.Fatal(err)
	}
	if err := updateOnePlaybook("pb", false); err != nil {
		t.Fatal(err)
	}
	b := updateBackups(installed)
	if len(b) != 1 {
		t.Fatalf("backups: %v", b)
	}
	if _, err := os.Lstat(filepath.Join(b[0], "config", "local.json")); !os.IsNotExist(err) {
		t.Fatal("the nested preserved file stayed in the backup")
	}
	if got, _ := os.ReadFile(filepath.Join(b[0], "config", "other")); string(got) != "old\n" {
		t.Fatalf("a replaced file left the backup: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(installed, "config", "local.json")); string(got) != "mine\n" {
		t.Fatalf("the preserved file: %q", got)
	}
	// A backup whose entry is a symlink out of it: nothing below is touched.
	evil := mkdirAll(t, filepath.Join(root, "evil-backup"))
	if err := os.Symlink(outside, filepath.Join(evil, "config")); err != nil {
		t.Fatal(err)
	}
	if _, err := trimUpdateBackup(evil, []string{"config/keep.json"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(outside, "keep.json")); err != nil {
		t.Fatalf("the trim followed a symlink out of the backup: %v", err)
	}
}

// writeUsedSource is a source directory that has been used as a config
// directory: session records, set-aside logins, backups and cpb's records
// beside its real content.
func writeUsedSource(t *testing.T, dir string) {
	t.Helper()
	for _, d := range []string{"projects/-home-user-work", "todos", "shell-snapshots", "file-history", "session-env", ".apply", "skills/s1"} {
		mkdirAll(t, filepath.Join(dir, d))
	}
	for name, body := range map[string]string{
		"CLAUDE.md":                        "content\n",
		"skills/s1/SKILL.md":               "skill\n",
		"history.jsonl":                    "{\"display\":\"a prompt\"}\n",
		"projects/-home-user-work/s.jsonl": "{}\n",
		".apply/recipe.cpb":                "ALTER PLAYBOOK SET VAR A=1;\n",
		".credentials.json.cpb-own-2026-01-01-00_00_00":     "{\"claudeAiOauth\":{}}\n",
		".credentials.json.cpb-ignored-2026-01-01-00_00_00": "{\"claudeAiOauth\":{}}\n",
		".credentials.json.cpb-aside":                       "{\"claudeAiOauth\":{}}\n",
		".claude.json.cpb-backup-2026-01-01-00_00_00":       "{\"oauthAccount\":{}}\n",
		".claude.json.backup":                               "{\"oauthAccount\":{}}\n",
		"settings.json.cpb-backup-2026-01-01-00_00_00":      "{}\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

var usedSourceArtifacts = []string{"projects", "todos", "shell-snapshots", "file-history", "session-env", ".apply", "history.jsonl",
	".credentials.json.cpb-own-2026-01-01-00_00_00", ".credentials.json.cpb-ignored-2026-01-01-00_00_00", ".credentials.json.cpb-aside",
	".claude.json.cpb-backup-2026-01-01-00_00_00", ".claude.json.backup", "settings.json.cpb-backup-2026-01-01-00_00_00"}

func assertNoRuntime(t *testing.T, dir string) {
	t.Helper()
	for _, a := range usedSourceArtifacts {
		if _, err := os.Lstat(filepath.Join(dir, a)); !os.IsNotExist(err) {
			t.Fatalf("%s reached %s", a, dir)
		}
	}
	for _, keep := range []string{"CLAUDE.md", "skills/s1/SKILL.md"} {
		if _, err := os.Stat(filepath.Join(dir, keep)); err != nil {
			t.Fatalf("content %s was dropped: %v", keep, err)
		}
	}
}

// An install from a used directory, or a git repository holding the same,
// takes the playbook's content and none of the machine's runtime state.
func TestInstallLeavesSourceRuntimeOut(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	home, _ := os.UserHomeDir()
	src := filepath.Join(home, "used")
	writeUsedSource(t, src)
	var err error
	stderr := captureStderr(t, func() { _, err = quotedStmt(t, "CREATE PLAYBOOK x SET launcher = '' FROM '"+src+"'") })
	if err != nil {
		t.Fatal(err)
	}
	assertNoRuntime(t, filepath.Join(config.ResolvePlaybooksDir(), "x"))
	if !strings.Contains(stderr, "Note: left "+src+"'s ") || !strings.Contains(stderr, "history.jsonl") {
		t.Fatalf("no note:\n%s", stderr)
	}
	if _, err := os.Stat(filepath.Join(src, "history.jsonl")); err != nil {
		t.Fatal("the source itself was changed")
	}

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := filepath.Join(home, "repo")
	writeUsedSource(t, repo)
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "initial"}} {
		c := exec.Command("git", args...)
		c.Dir = repo
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	captureStderr(t, func() { _, err = quotedStmt(t, "CREATE PLAYBOOK y SET launcher = '' FROM 'file://"+repo+"'") })
	if err != nil {
		t.Fatal(err)
	}
	assertNoRuntime(t, filepath.Join(config.ResolvePlaybooksDir(), "y"))
}

// update never lets a source's runtime state replace the install's own.
func TestUpdateLeavesSourceRuntimeOut(t *testing.T) {
	resetCommandTestState(t)
	root := t.TempDir()
	config.PlaybooksDir = filepath.Join(root, "playbooks")
	source := filepath.Join(root, "source")
	installed := mkdirAll(t, filepath.Join(config.PlaybooksDir, "pb"))
	writeUsedSource(t, source)
	if err := os.WriteFile(filepath.Join(installed, "history.jsonl"), []byte("mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Write(installed, &manifest.Manifest{Source: &manifest.Source{Repository: source}}); err != nil {
		t.Fatal(err)
	}
	captureStderr(t, func() {
		if err := updateOnePlaybook("pb", false); err != nil {
			t.Fatal(err)
		}
	})
	if got, _ := os.ReadFile(filepath.Join(installed, "history.jsonl")); string(got) != "mine\n" {
		t.Fatalf("the install's history was replaced: %q", got)
	}
	for _, a := range usedSourceArtifacts {
		if a == "history.jsonl" {
			continue
		}
		if _, err := os.Lstat(filepath.Join(installed, a)); !os.IsNotExist(err) {
			t.Fatalf("%s reached the install", a)
		}
	}
	if _, err := os.Stat(filepath.Join(source, "history.jsonl")); err != nil {
		t.Fatal("the source itself was changed")
	}
}
