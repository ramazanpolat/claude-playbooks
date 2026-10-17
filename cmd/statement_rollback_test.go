package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/launcher"
)

// A CREATE whose ALTER after the creation fails removes what it created:
// the directory, its manifest, its launcher and the launcher's receipt line.
// The failure here is real, not injected: the source ships a settings.json
// that is not JSON, so the creation succeeds (the install copies it as it
// is) and the model the SET list carries cannot be written into it.
func TestCreateRollsBackAfterALaterStepFails(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CPB_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	src := gitSource(t, map[string]string{"settings.json": "{not json\n", "CLAUDE.md": "# src\n"})

	err := stmtErr(t, "CREATE PLAYBOOK rb FROM "+src+" SET launcher = 'rbl', global_instructions = 'inherited', model = 'm'")
	if err == nil {
		t.Fatal("CREATE over an unreadable settings.json succeeded")
	}
	if !strings.Contains(err.Error(), "nothing was created") {
		t.Fatalf("the refusal does not say the creation was undone: %v", err)
	}
	if _, serr := os.Lstat(filepath.Join(root, "rb")); !os.IsNotExist(serr) {
		t.Fatalf("the playbook directory survived the rollback: %v", serr)
	}
	if _, exists, _ := launcher.Lookup(config.LauncherDir, "rbl"); exists {
		t.Fatal("the launcher survived the rollback")
	}
	for _, line := range launcher.Recorded() {
		if filepath.Base(line) == "rbl" {
			t.Fatalf("the receipt still lists the launcher: %v", launcher.Recorded())
		}
	}
}

// The rollback is doDelete, and for a linked playbook doDelete removes only
// the link: the directory it points at survives. A CREATE … LINK takes no
// clause that runs after the creation (only the launcher), so it never
// reaches the rollback itself; this pins the half of it that would matter
// if it did.
func TestRollbackOfALinkKeepsItsTarget(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CPB_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, ".playbook"), []byte("name = \"tgt\"\nlauncher = \"lnl\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "CLAUDE.md"), []byte("# keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustStmt(t, "CREATE PLAYBOOK ln LINK "+target)
	captureStdout(t, func() {
		if err := doDelete(deleteOpts{yes: true}, []string{"ln"}); err != nil {
			t.Fatal(err)
		}
	})
	if _, err := os.Lstat(filepath.Join(root, "ln")); !os.IsNotExist(err) {
		t.Fatalf("the link survived: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(target, "CLAUDE.md")); err != nil || string(b) != "# keep me\n" {
		t.Fatalf("the link's target was touched: %v %q", err, b)
	}
	if _, exists, _ := launcher.Lookup(config.LauncherDir, "lnl"); exists {
		t.Fatal("the link's launcher survived")
	}
}
