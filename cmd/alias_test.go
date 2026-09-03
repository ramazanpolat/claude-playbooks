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
	// The own-name case is no longer an error: it repairs the name launcher.
	if err := runAlias(nil, []string{"deploy", "deploy"}); err != nil {
		t.Fatalf("own-name repair must not fail: %v", err)
	}
}

// The dead end from the kommander-dev handoff: create --alias, drop the
// alias, and the playbook has no command — with no way to get one back,
// because "alias <name> <name>" used to refuse.
func TestAliasNameRepairsNameLauncher(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	seedFlatPlaybook(t, "deploy")

	if err := runAlias(nil, []string{"deploy", "d"}); err != nil {
		t.Fatal(err)
	}
	aliasRemove = true
	err := runAlias(nil, []string{"deploy"})
	aliasRemove = false
	if err != nil {
		t.Fatal(err)
	}
	if _, exists, _ := launcher.Lookup(config.LauncherDir, "deploy"); exists {
		t.Fatal("name launcher unexpectedly present after alias removal")
	}

	// The repair spelling: name-as-alias ensures the name launcher exists.
	if err := runAlias(nil, []string{"deploy", "deploy"}); err != nil {
		t.Fatalf("name repair failed: %v", err)
	}
	if e, exists, foreign := launcher.Lookup(config.LauncherDir, "deploy"); !exists || foreign {
		t.Fatalf("name launcher not repaired: %+v exists=%v foreign=%v", e, exists, foreign)
	}

	// Idempotent, and the manifest stays untouched (name is not an alias).
	before, err := os.ReadFile(filepath.Join(config.ResolvePlaybooksDir(), "deploy", manifest.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if err := runAlias(nil, []string{"deploy", "deploy"}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(config.ResolvePlaybooksDir(), "deploy", manifest.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("manifest changed during name repair:\n%s\n%s", before, after)
	}
	m, err := manifest.Read(filepath.Join(config.ResolvePlaybooksDir(), "deploy"))
	if err != nil || m == nil || m.Alias != "" {
		t.Fatalf("name repair must not record an alias: %+v err=%v", m, err)
	}
}

// The repair must not hand the playbook a name that another playbook now
// owns as an alias, and must refuse a foreign file squatting the path.
func TestAliasNameRepairRespectsOwnership(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	seedFlatPlaybook(t, "deploy")
	seedFlatPlaybook(t, "other")

	// The tool refuses to CREATE this collision, so reach it the only way it
	// exists: a hand-edited manifest giving `other` the alias `deploy`.
	if err := manifest.Write(filepath.Join(config.ResolvePlaybooksDir(), "other"), &manifest.Manifest{Name: "other", Alias: "deploy"}); err != nil {
		t.Fatal(err)
	}
	err := runAlias(nil, []string{"deploy", "deploy"})
	if err == nil || !strings.Contains(err.Error(), "other") {
		t.Fatalf("expected ownership refusal naming the other playbook, got: %v", err)
	}
}

// A foreign file squatting the launcher path must refuse the repair; a
// linked registration must be able to repair its local name launcher (the
// shared manifest is never touched).
func TestAliasNameRepairForeignFileAndLinked(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	seedFlatPlaybook(t, "deploy")
	ldir := config.LauncherDir
	if err := os.MkdirAll(ldir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ldir, "deploy"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runAlias(nil, []string{"deploy", "deploy"}); err == nil || !strings.Contains(err.Error(), "did not generate") {
		t.Fatalf("expected foreign-file refusal, got: %v", err)
	}
	if err := os.Remove(filepath.Join(ldir, "deploy")); err != nil {
		t.Fatal(err)
	}

	external := t.TempDir()
	if err := os.WriteFile(filepath.Join(external, manifest.FileName), []byte("version = \"0.1.0\"\nname = \"ext\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(config.ResolvePlaybooksDir(), "linked")); err != nil {
		t.Fatal(err)
	}
	if err := runAlias(nil, []string{"linked", "linked"}); err != nil {
		t.Fatalf("linked registration must repair its own name launcher: %v", err)
	}
	if e, exists, foreign := launcher.Lookup(ldir, "linked"); !exists || foreign {
		t.Fatalf("linked name launcher not written: %+v exists=%v foreign=%v", e, exists, foreign)
	}
}

func TestAliasUnchangedOnLinkedPlaybookDoesNotRewriteSharedManifest(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	external := t.TempDir()
	// Comments and unknown fields prove a rewrite: manifest.Write would
	// drop both.
	content := "# hand-authored comment\nversion = \"0.1.0\"\nname = \"ext\"\nalias = \"x\"\ncustom_field = \"kept\"\n"
	if err := os.WriteFile(filepath.Join(external, manifest.FileName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(config.ResolvePlaybooksDir(), "linked")); err != nil {
		t.Fatal(err)
	}

	// Same alias: allowed (ensures the launcher), but the shared manifest
	// must remain byte-identical.
	if err := runAlias(nil, []string{"linked", "x"}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(external, manifest.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Fatalf("shared manifest rewritten:\n%s", got)
	}
}

func TestAliasForeignFilePreflightLeavesStateUntouched(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	root := seedFlatPlaybook(t, "deploy")

	if err := runAlias(nil, []string{"deploy", "d"}); err != nil {
		t.Fatal(err)
	}
	// A foreign file squats on the requested replacement name.
	if err := os.WriteFile(filepath.Join(config.LauncherDir, "taken"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := runAlias(nil, []string{"deploy", "taken"}); err == nil {
		t.Fatal("foreign file on the new name must fail the alias change")
	}
	// Nothing mutated: manifest still says d, old launcher still present.
	m, err := manifest.Read(root)
	if err != nil || m == nil || m.Alias != "d" {
		t.Fatalf("manifest mutated despite preflight failure: %+v err=%v", m, err)
	}
	if _, exists, foreign := launcher.Lookup(config.LauncherDir, "d"); !exists || foreign {
		t.Fatal("old launcher lost despite preflight failure")
	}
}

func TestAliasLauncherWriteFailureRollsBackManifest(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	root := seedFlatPlaybook(t, "deploy")

	if err := runAlias(nil, []string{"deploy", "d"}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(root, manifest.FileName))
	if err != nil {
		t.Fatal(err)
	}

	// Break the launcher directory: an unwritable parent makes
	// launcher.Write's MkdirAll fail after every preflight has passed.
	ro := filepath.Join(t.TempDir(), "ro")
	if err := os.MkdirAll(ro, 0o555); err != nil {
		t.Fatal(err)
	}
	config.LauncherDir = filepath.Join(ro, "sub")

	err = runAlias(nil, []string{"deploy", "d2"})
	if err == nil {
		t.Fatal("launcher write failure must fail the alias change")
	}
	after, rerr := os.ReadFile(filepath.Join(root, manifest.FileName))
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(after) != string(before) {
		t.Fatalf("manifest not rolled back:\n%s", after)
	}
}

func TestAliasRepairUnchangedFailsWhenLauncherUnwritable(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	seedFlatPlaybook(t, "deploy")

	if err := runAlias(nil, []string{"deploy", "d"}); err != nil {
		t.Fatal(err)
	}
	ro := filepath.Join(t.TempDir(), "ro")
	if err := os.MkdirAll(ro, 0o555); err != nil {
		t.Fatal(err)
	}
	config.LauncherDir = filepath.Join(ro, "sub")

	// Re-setting the SAME alias is a repair; an unwritable launcher dir
	// must surface as an error, not a warning behind exit 0.
	if err := runAlias(nil, []string{"deploy", "d"}); err == nil {
		t.Fatal("unwritable launcher dir must fail the unchanged-alias repair")
	}
}
