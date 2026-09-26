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

func TestStatementCreateAndDropPlaybook(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CLAUDE_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))

	mustStmt(t, "CREATE PLAYBOOK fresh ALIAS fr SANDBOX")
	m, err := manifest.Read(filepath.Join(root, "fresh"))
	if err != nil || m == nil || m.Alias != "fr" || m.Sandbox == nil || !m.Sandbox.Always {
		t.Fatalf("created manifest: %#v %v", m, err)
	}
	if _, exists, _ := launcher.Lookup(config.LauncherDir, "fr"); !exists {
		t.Fatal("launcher fr not written")
	}
	// The flags a statement set do not leak into the next command.
	if createAlias != "" || createSandbox || createNoAlias {
		t.Fatalf("create flags leaked: %q %v %v", createAlias, createSandbox, createNoAlias)
	}
	if _, err := stmt(t, "CREATE PLAYBOOK fresh"); err == nil {
		t.Fatal("CREATE over an existing playbook succeeded")
	}
	if out := mustStmt(t, "CREATE PLAYBOOK IF NOT EXISTS fresh"); !strings.Contains(out, "unchanged") {
		t.Fatalf("IF NOT EXISTS: %s", out)
	}

	if out := mustStmt(t, "DROP PLAYBOOK IF EXISTS ghost"); !strings.Contains(out, "nothing to drop") {
		t.Fatalf("DROP IF EXISTS of a missing playbook: %s", out)
	}
	mustStmt(t, "DROP PLAYBOOK fresh --yes")
	if _, err := os.Stat(filepath.Join(root, "fresh")); !os.IsNotExist(err) {
		t.Fatalf("DROP PLAYBOOK left the playbook: %v", err)
	}
	if deleteYes {
		t.Fatal("--yes leaked into the delete command's flag")
	}
}

func TestStatementCreatePlaybookFromSource(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CLAUDE_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	src := t.TempDir()
	if err := manifest.Write(src, &manifest.Manifest{Version: "1.0.0", Name: "upstream", Alias: "up"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "CLAUDE.md"), []byte("# upstream\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustStmt(t, "CREATE PLAYBOOK mine FROM "+src+" NO ALIAS")
	m, err := manifest.Read(filepath.Join(root, "mine"))
	if err != nil || m == nil || m.Version != "1.0.0" {
		t.Fatalf("installed manifest: %#v %v", m, err)
	}
	if _, exists, _ := launcher.Lookup(config.LauncherDir, "up"); exists {
		t.Fatal("NO ALIAS still wrote the source's launcher")
	}
	if installName != "" || installNoAlias {
		t.Fatalf("install flags leaked: %q %v", installName, installNoAlias)
	}
}

func TestStatementCreatePlaybookLink(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CLAUDE_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	bare := t.TempDir()
	if _, err := stmt(t, "CREATE PLAYBOOK dev LINK "+bare); err == nil || !strings.Contains(err.Error(), "claude-playbook link") {
		t.Fatalf("LINK without a manifest must name the way out: %v", err)
	}
	if _, err := stmt(t, "CREATE PLAYBOOK dev LINK "+bare+" SANDBOX"); err == nil || !strings.Contains(err.Error(), "SANDBOX does not apply to LINK") {
		t.Fatalf("LINK with SANDBOX: %v", err)
	}
	if err := manifest.Write(bare, &manifest.Manifest{Name: "dev"}); err != nil {
		t.Fatal(err)
	}
	mustStmt(t, "CREATE PLAYBOOK dev LINK "+bare+" NO ALIAS")
	if info, err := os.Lstat(filepath.Join(root, "dev")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("not linked: %v", err)
	}
}

func TestStatementRenameAndAlias(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CLAUDE_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	writePlaybook(t, root, "old", &manifest.Manifest{})

	mustStmt(t, "ALTER PLAYBOOK old RENAME TO new ALIAS nw")
	m, err := manifest.Read(filepath.Join(root, "new"))
	if err != nil || m == nil || m.Alias != "nw" {
		t.Fatalf("after RENAME TO … ALIAS: %#v %v", m, err)
	}
	if renameAlias != "" || renameNoAlias {
		t.Fatal("rename flags leaked")
	}
	mustStmt(t, "ALTER PLAYBOOK new ALIAS n2")
	if m, _ := manifest.Read(filepath.Join(root, "new")); m.Alias != "n2" {
		t.Fatalf("ALIAS: %q", m.Alias)
	}
	mustStmt(t, "ALTER PLAYBOOK new NO ALIAS")
	if m, _ := manifest.Read(filepath.Join(root, "new")); m.Alias != "" {
		t.Fatalf("NO ALIAS: %q", m.Alias)
	}
	if aliasRemove {
		t.Fatal("NO ALIAS leaked into the alias command's flag")
	}

	// A rename and an environment change are two statements.
	if _, err := stmt(t, "ALTER PLAYBOOK new SET VAR A=1 RENAME TO newer"); err == nil || !strings.Contains(err.Error(), "use two statements") {
		t.Fatalf("mixed statement: %v", err)
	}
	if e := readEnv(t, filepath.Join(root, "new")); !e.Empty() {
		t.Fatalf("the refused statement wrote: %#v", e)
	}
	if _, err := os.Stat(filepath.Join(root, "newer")); !os.IsNotExist(err) {
		t.Fatal("the refused statement renamed")
	}
}

// A playbook has one launcher, its alias or its name. NO ALIAS removes the
// name launcher too; ALIAS <its name> retires the alias it replaces.
func TestStatementLauncherIsOneOrNone(t *testing.T) {
	sandboxDefaultRoot(t)
	t.Setenv("CLAUDE_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	has := func(cmd string) bool {
		_, exists, _ := launcher.Lookup(config.LauncherDir, cmd)
		return exists
	}
	mustStmt(t, "CREATE PLAYBOOK plain")
	if !has("plain") {
		t.Fatal("the name launcher was not written")
	}
	mustStmt(t, "ALTER PLAYBOOK plain NO ALIAS")
	if has("plain") {
		t.Fatal("NO ALIAS kept the name launcher")
	}

	mustStmt(t, "ALTER PLAYBOOK plain ALIAS pl")
	mustStmt(t, "ALTER PLAYBOOK plain ALIAS plain")
	if has("pl") || !has("plain") {
		t.Fatalf("ALIAS <name> over alias pl: pl=%v plain=%v", has("pl"), has("plain"))
	}
	if pb, _ := playbook.Require(config.ResolvePlaybooksDir(), "plain"); pb.Alias() != "" {
		t.Fatalf("the replaced alias is still recorded: %q", pb.Alias())
	}
}

// DROP IF EXISTS is a no-op only when the playbook is not there; a registry
// that cannot be read is an error.
func TestStatementDropIfExistsKeepsDiscoveryErrors(t *testing.T) {
	root := sandboxDefaultRoot(t)
	writePlaybook(t, root, "target", &manifest.Manifest{})
	if err := os.MkdirAll(filepath.Join(root, "broken"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "broken", manifest.FileName), []byte("not = [toml"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := stmt(t, "DROP PLAYBOOK IF EXISTS target --yes"); err == nil {
		t.Fatal("a discovery error became nothing to drop")
	}
	if _, err := os.Stat(filepath.Join(root, "target")); err != nil {
		t.Fatalf("target: %v", err)
	}
}
