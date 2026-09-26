package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

func makeSkill(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: x\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSkillLinkAndDrop(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	root := seedFlatPlaybook(t, "k")
	src, _ := filepath.EvalSymlinks(makeSkill(t, filepath.Join(t.TempDir(), "notes")))
	add := "ALTER PLAYBOOK k ADD SKILL notes FROM " + src
	mustStmt(t, add)
	if got, err := os.Readlink(filepath.Join(root, "skills", "notes")); err != nil || got != src {
		t.Fatalf("link: %q %v", got, err)
	}
	m, _ := manifest.Read(root)
	if r := m.Skills["notes"]; r == nil || r.Mode != "link" || r.Source != src {
		t.Fatalf("record: %+v", m.Skills)
	}
	if out := mustStmt(t, add); !strings.Contains(out, "unchanged") {
		t.Fatalf("a repeat changed something:\n%s", out)
	}
	if out := mustStmt(t, "SHOW CREATE PLAYBOOK k"); !strings.Contains(out, "ADD SKILL notes FROM '"+src+"'") {
		t.Fatalf("SHOW CREATE:\n%s", out)
	}
	mustStmt(t, "ALTER PLAYBOOK k DROP SKILL notes")
	if _, err := os.Lstat(filepath.Join(root, "skills", "notes")); !os.IsNotExist(err) {
		t.Fatal("DROP SKILL left the link")
	}
	if _, err := os.Stat(filepath.Join(src, "SKILL.md")); err != nil {
		t.Fatal("DROP SKILL touched the linked directory")
	}
	if m, _ := manifest.Read(root); m.Skills != nil {
		t.Fatalf("record kept: %+v", m.Skills)
	}
}

// A skill cpb did not add is never replaced or removed.
func TestSkillRefusesUnrecorded(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	root := seedFlatPlaybook(t, "k")
	makeSkill(t, filepath.Join(root, "skills", "mine"))
	src := makeSkill(t, filepath.Join(t.TempDir(), "other"))
	if _, err := stmt(t, "ALTER PLAYBOOK k ADD SKILL mine FROM "+src); err == nil || !strings.Contains(err.Error(), "cpb did not add it") {
		t.Fatalf("ADD over an unrecorded skill: %v", err)
	}
	if _, err := stmt(t, "ALTER PLAYBOOK k DROP SKILL mine"); err == nil || !strings.Contains(err.Error(), "was not added by cpb") {
		t.Fatalf("DROP of an unrecorded skill: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "skills", "mine", "SKILL.md")); err != nil {
		t.Fatal("the pilot's own skill was touched")
	}
}

// A git source is cloned and copied (without .git); a repeat clones
// nothing.
func TestSkillCopyFromGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	resetCommandTestState(t)
	aliasTestHome(t)
	root := seedFlatPlaybook(t, "k")
	repo := makeSkill(t, filepath.Join(t.TempDir(), "repo"))
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "s"}} {
		c := exec.Command("git", args...)
		c.Dir = repo
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	add := "ALTER PLAYBOOK k ADD SKILL pub FROM file://" + repo
	mustStmt(t, add)
	dst := filepath.Join(root, "skills", "pub")
	if info, err := os.Lstat(dst); err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("copy: %v %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(dst, "SKILL.md")); err != nil {
		t.Fatal("SKILL.md not copied")
	}
	if _, err := os.Stat(filepath.Join(dst, ".git")); !os.IsNotExist(err) {
		t.Fatal(".git copied")
	}
	if out := mustStmt(t, add); !strings.Contains(out, "unchanged") {
		t.Fatalf("a repeat re-copied:\n%s", out)
	}
}

// update keeps the [mcp] and [skills] records and puts recorded skills back
// after its overlay replaces skills/.
func TestUpdateKeepsRecordsAndRestoresSkills(t *testing.T) {
	resetCommandTestState(t)
	root := t.TempDir()
	config.PlaybooksDir = filepath.Join(root, "playbooks")
	source := filepath.Join(root, "source")
	installed := filepath.Join(config.PlaybooksDir, "pb")
	makeSkill(t, filepath.Join(source, "skills", "shipped"))
	if err := manifest.Write(source, &manifest.Manifest{Version: "2.0.0", Name: "pb"}); err != nil {
		t.Fatal(err)
	}
	skill, _ := filepath.EvalSymlinks(makeSkill(t, filepath.Join(root, "mine")))
	if err := os.MkdirAll(filepath.Join(installed, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(skill, filepath.Join(installed, "skills", "mine")); err != nil {
		t.Fatal(err)
	}
	live := &manifest.Manifest{Version: "1.0.0", Name: "pb", Source: &manifest.Source{Repository: source},
		MCP:    map[string]*manifest.MCPRecord{"s": {Vars: []string{"CPB_MCP_S_E_T_00000000"}}},
		Skills: map[string]*manifest.SkillRecord{"mine": {Source: skill, Mode: "link"}}}
	if err := manifest.Write(installed, live); err != nil {
		t.Fatal(err)
	}
	if err := updateOnePlaybook("pb", false); err != nil {
		t.Fatal(err)
	}
	m, _ := manifest.Read(installed)
	if m.MCP["s"] == nil || m.Skills["mine"] == nil {
		t.Fatalf("records lost on update: %+v %+v", m.MCP, m.Skills)
	}
	if got, err := os.Readlink(filepath.Join(installed, "skills", "mine")); err != nil || got != skill {
		t.Fatalf("recorded skill not restored: %q %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(installed, "skills", "shipped", "SKILL.md")); err != nil {
		t.Fatal("the source's own skill is missing")
	}
}

// A manifest record cannot name a path outside skills/.
func TestSkillRecordNameValidated(t *testing.T) {
	dir := t.TempDir()
	bad := "version = \"0.1.0\"\nname = \"k\"\n\n[skills.\"../../victim\"]\nsource = \"/x\"\nmode = \"copy\"\n"
	if err := os.WriteFile(filepath.Join(dir, manifest.FileName), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := manifest.Read(dir); err == nil || !strings.Contains(err.Error(), "not a valid skill name") {
		t.Fatalf("a traversing record name was accepted: %v", err)
	}
}

// A statement that stops at a failed skill keeps the finished ones recorded.
func TestSkillFailureKeepsEarlierRecorded(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	root := seedFlatPlaybook(t, "k")
	src, _ := filepath.EvalSymlinks(makeSkill(t, filepath.Join(t.TempDir(), "a")))
	missing := "file://" + filepath.Join(t.TempDir(), "no-such-repo")
	if _, err := stmt(t, "ALTER PLAYBOOK k ADD SKILL a FROM "+src+" ADD SKILL b FROM "+missing); err == nil {
		t.Fatal("the failing clone was not reported")
	}
	m, _ := manifest.Read(root)
	if m == nil || m.Skills["a"] == nil || m.Skills["b"] != nil {
		t.Fatalf("records after a partial statement: %+v", m)
	}
	mustStmt(t, "ALTER PLAYBOOK k DROP SKILL a")
}

// After an update whose source ships a skill of the same name, the
// recorded skill is the one in place.
func TestUpdateRecordedSkillWinsOverShipped(t *testing.T) {
	resetCommandTestState(t)
	root := t.TempDir()
	config.PlaybooksDir = filepath.Join(root, "playbooks")
	source := filepath.Join(root, "source")
	installed := filepath.Join(config.PlaybooksDir, "pb")
	makeSkill(t, filepath.Join(source, "skills", "mine"))
	if err := manifest.Write(source, &manifest.Manifest{Version: "2.0.0", Name: "pb"}); err != nil {
		t.Fatal(err)
	}
	skill, _ := filepath.EvalSymlinks(makeSkill(t, filepath.Join(root, "mine")))
	if err := os.MkdirAll(filepath.Join(installed, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(skill, filepath.Join(installed, "skills", "mine")); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Write(installed, &manifest.Manifest{Version: "1.0.0", Name: "pb", Source: &manifest.Source{Repository: source},
		Skills: map[string]*manifest.SkillRecord{"mine": {Source: skill, Mode: "link"}}}); err != nil {
		t.Fatal(err)
	}
	if err := updateOnePlaybook("pb", false); err != nil {
		t.Fatal(err)
	}
	if got, err := os.Readlink(filepath.Join(installed, "skills", "mine")); err != nil || got != skill {
		t.Fatalf("the recorded skill did not win: %q %v", got, err)
	}
}

// Skill operations run in clause order with the MCP and plugin commands: a
// failed skill stops the statement before a later clause's command runs.
func TestSkillRunsInClauseOrder(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	calls := fakeMCP(t)
	seedFlatPlaybook(t, "k")
	missing := "file://" + filepath.Join(t.TempDir(), "no-such-repo")
	if _, err := stmt(t, "ALTER PLAYBOOK k ADD SKILL b FROM "+missing+" ADD MCP SERVER s COMMAND x"); err == nil {
		t.Fatal("the failing clone was not reported")
	}
	if len(*calls) != 0 {
		t.Fatalf("a later clause ran after the failed skill: %v", *calls)
	}
	path := writePlaybookFile(t, "ALTER PLAYBOOK k ADD SKILL b FROM '"+missing+"' ADD MCP SERVER s COMMAND x;\n")
	out, err := apply(t, path, "--dry-run")
	if i, j := strings.Index(out, "copy skills/b"), strings.Index(out, "claude mcp add-json"); err != nil || i < 0 || j < i {
		t.Fatalf("dry run order: %v\n%s", err, out)
	}
}

// A skill whose record cannot be written is taken away again, so running
// the statement again adds it rather than refusing an unrecorded skill.
func TestSkillRecordFailureTakesItAway(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only directory")
	}
	resetCommandTestState(t)
	aliasTestHome(t)
	root := seedFlatPlaybook(t, "k")
	src, _ := filepath.EvalSymlinks(makeSkill(t, filepath.Join(t.TempDir(), "notes")))
	if err := os.MkdirAll(filepath.Join(root, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o555); err != nil {
		t.Fatal(err)
	}
	add := "ALTER PLAYBOOK k ADD SKILL notes FROM " + src
	_, err := stmt(t, add)
	_ = os.Chmod(root, 0o755)
	if err == nil || !strings.Contains(err.Error(), "taken away again") {
		t.Fatalf("record failure: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "skills", "notes")); !os.IsNotExist(err) {
		t.Fatal("an unrecorded skill was left in place")
	}
	mustStmt(t, add)
	if m, _ := manifest.Read(root); m == nil || m.Skills["notes"] == nil {
		t.Fatal("the retry did not record the skill")
	}
}
