package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/launcher"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// aliasTestHome sandboxes HOME so the DEFAULT playbooks root (the only root
// launcher ops are allowed for) lands in the test's tempdir.
func aliasTestHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	config.PlaybooksDir = ""
	if err := os.MkdirAll(filepath.Join(home, ".claude-playbooks"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func seedFlatPlaybook(t *testing.T, name string) string {
	t.Helper()
	root := filepath.Join(config.ResolvePlaybooksDir(), name)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestAliasSetBootstrapsManifestAndLauncher(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	root := seedFlatPlaybook(t, "deploy")

	if err := runAlias(nil, []string{"deploy", "d"}); err != nil {
		t.Fatal(err)
	}

	m, err := manifest.Read(root)
	if err != nil || m == nil {
		t.Fatalf("manifest not created: m=%#v err=%v", m, err)
	}
	if m.Alias != "d" || m.Name != "deploy" {
		t.Fatalf("manifest = %+v, want alias d for deploy", m)
	}
	if e, exists, foreign := launcher.Lookup(config.LauncherDir, "d"); !exists || foreign {
		t.Fatalf("launcher d not written: %+v exists=%v foreign=%v", e, exists, foreign)
	}
}

func TestAliasReplaceRetiresOldLauncher(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	seedFlatPlaybook(t, "deploy")

	if err := runAlias(nil, []string{"deploy", "d"}); err != nil {
		t.Fatal(err)
	}
	if err := runAlias(nil, []string{"deploy", "dep"}); err != nil {
		t.Fatal(err)
	}

	if _, exists, _ := launcher.Lookup(config.LauncherDir, "d"); exists {
		t.Fatal("old alias launcher d survived the replacement")
	}
	if _, exists, foreign := launcher.Lookup(config.LauncherDir, "dep"); !exists || foreign {
		t.Fatal("new alias launcher dep missing")
	}
}

func TestAliasRemoveClearsManifestAndLauncher(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	root := seedFlatPlaybook(t, "deploy")

	if err := runAlias(nil, []string{"deploy", "d"}); err != nil {
		t.Fatal(err)
	}
	aliasRemove = true
	err := runAlias(nil, []string{"deploy"})
	aliasRemove = false
	if err != nil {
		t.Fatal(err)
	}

	m, err := manifest.Read(root)
	if err != nil {
		t.Fatal(err)
	}
	if m != nil && m.Alias != "" {
		t.Fatalf("manifest alias survives --remove: %+v", m)
	}
	if _, exists, _ := launcher.Lookup(config.LauncherDir, "d"); exists {
		t.Fatal("alias launcher survives --remove")
	}
}

func TestAliasRejectsCollisionWithOtherPlaybook(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	seedFlatPlaybook(t, "one")
	seedFlatPlaybook(t, "two")

	if err := runAlias(nil, []string{"one", "two"}); err == nil {
		t.Fatal("alias equal to another playbook's name must be refused")
	}
	if err := runAlias(nil, []string{"one", "x"}); err != nil {
		t.Fatal(err)
	}
	if err := runAlias(nil, []string{"two", "x"}); err == nil {
		t.Fatal("alias already owned by another playbook must be refused")
	}
}

func TestAliasRefusedOnLinkedPlaybook(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	external := t.TempDir()
	if err := os.WriteFile(filepath.Join(external, manifest.FileName), []byte("version = \"0.1.0\"\nname = \"ext\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(config.ResolvePlaybooksDir(), "linked")); err != nil {
		t.Fatal(err)
	}

	if err := runAlias(nil, []string{"linked", "l"}); err == nil {
		t.Fatal("alias mutation on a linked playbook's shared manifest must be refused")
	}
}

func TestAliasRejectsReservedAndOwnName(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	seedFlatPlaybook(t, "deploy")

	if err := runAlias(nil, []string{"deploy", "cpb"}); err == nil {
		t.Fatal("reserved name must be refused")
	}
	if err := runAlias(nil, []string{"deploy", "deploy"}); err == nil {
		t.Fatal("alias equal to the playbook's own name must be refused")
	}
}
