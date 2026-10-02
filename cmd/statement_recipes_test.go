package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/envprofile"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

func TestApplyRecipes(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CPB_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	dir, _ := filepath.EvalSymlinks(t.TempDir())

	// TO fills the name-less statements; a missing target is created bare.
	recipe := writeCpb(t, dir, "recipe.cpb", "ALTER PLAYBOOK SET VAR A=1;\n")
	out, err := apply(t, recipe, "TO", "fresh", "--dry-run")
	if err != nil || !strings.Contains(out, "created   CREATE PLAYBOOK fresh") || !strings.Contains(out, "changed   ALTER PLAYBOOK fresh") {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	if _, err := apply(t, recipe, "TO", "fresh"); err != nil {
		t.Fatal(err)
	}
	if e := readEnv(t, filepath.Join(root, "fresh")); e.Set["A"] != "1" {
		t.Fatalf("TO fresh: %#v", e)
	}

	// USE PLAYBOOK sets the target; a shared recipe runs for each target,
	// its target-independent statements once.
	writeCpb(t, dir, "shared.cpb", "CREATE OR REPLACE ENV shared SET S=1;\nALTER PLAYBOOK USE ENV shared;\n")
	main := writeCpb(t, dir, "main.cpb", "USE PLAYBOOK a;\nINCLUDE 'shared.cpb';\nUSE PLAYBOOK b;\nINCLUDE 'shared.cpb';\n")
	out, err = apply(t, main)
	if err != nil || !strings.Contains(out, "3 created, 2 changed, 0 unchanged") {
		t.Fatalf("USE PLAYBOOK: %v\n%s", err, out)
	}
	for _, pb := range []string{"a", "b"} {
		if e := readEnv(t, filepath.Join(root, pb)); strings.Join(e.Sets, ",") != "shared" {
			t.Fatalf("%s: %#v", pb, e)
		}
	}

	// No target: refused before anything is written.
	if _, err := apply(t, recipe); err == nil || !strings.Contains(err.Error(), "no target") || !strings.Contains(err.Error(), "nothing was written") {
		t.Fatalf("no target: %v", err)
	}

	// TO wins over USE PLAYBOOK; a playbook's directory names it.
	if _, err := apply(t, writeCpb(t, dir, "use.cpb", "USE PLAYBOOK a;\nALTER PLAYBOOK SET VAR T=2;\n"), "TO", filepath.Join(root, "b")); err != nil {
		t.Fatal(err)
	}
	if readEnv(t, filepath.Join(root, "b")).Set["T"] != "2" || readEnv(t, filepath.Join(root, "a")).Set["T"] != "" {
		t.Fatal("TO did not win over USE PLAYBOOK")
	}
}

// A registry that cannot be read stops the apply before anything is
// written, rather than skipping the missing target's CREATE.
func TestApplyTargetDiscoveryError(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CPB_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	if err := os.MkdirAll(filepath.Join(root, "broken"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "broken", manifest.FileName), []byte("version = [\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	recipe := writeCpb(t, dir, "recipe.cpb", "CREATE OR REPLACE ENV e SET S=1;\nALTER PLAYBOOK SET VAR A=1;\n")
	if _, err := apply(t, recipe, "TO", "fresh"); err == nil || !strings.Contains(err.Error(), "nothing was written") {
		t.Fatalf("discovery error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(envprofile.Dir(root), "e.toml")); !os.IsNotExist(err) {
		t.Fatal("a statement ran before the registry error")
	}
}
