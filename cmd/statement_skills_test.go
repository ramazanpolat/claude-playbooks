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
