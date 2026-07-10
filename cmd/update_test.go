package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

func TestNativeUpdatePreservesRuntimeState(t *testing.T) {
	resetCommandTestState(t)
	root := t.TempDir()
	config.PlaybooksDir = filepath.Join(root, "playbooks")
	config.ShellConfig = filepath.Join(root, "shellrc")
	source := filepath.Join(root, "source")
	installed := filepath.Join(config.PlaybooksDir, "pb")
	if err := os.MkdirAll(source, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(installed, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "CLAUDE.md"), []byte("new\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, ".claude.json"), []byte("{\"source\":true}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installed, "CLAUDE.md"), []byte("old\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installed, ".claude.json"), []byte("{\"state\":true}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	globalCreds := filepath.Join(root, "credentials.json")
	if err := os.WriteFile(globalCreds, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(globalCreds, filepath.Join(installed, ".credentials.json")); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Write(installed, &manifest.Manifest{Source: &manifest.Source{Repository: source}}); err != nil {
		t.Fatal(err)
	}

	if err := runPlaybookUpdate("pb", nil); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(installed, "CLAUDE.md")); err != nil || string(got) != "new\n" {
		t.Fatalf("updated content=%q err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(installed, ".claude.json")); err != nil {
		t.Fatalf("runtime state was lost: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(installed, ".claude.json")); err != nil || string(got) != "{\"state\":true}\n" {
		t.Fatalf("runtime state=%q err=%v", got, err)
	}
	if got, err := os.Readlink(filepath.Join(installed, ".credentials.json")); err != nil || got != globalCreds {
		t.Fatalf("credential link=%q err=%v", got, err)
	}
	backups, err := filepath.Glob(filepath.Join(config.PlaybooksDir, ".pb.bak.*"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("backups=%v err=%v", backups, err)
	}
	pbs, err := playbook.Discover(config.PlaybooksDir, config.ShellConfig)
	if err != nil || len(pbs) != 1 || pbs[0].Name != "pb" {
		t.Fatalf("backup was discovered as a playbook: pbs=%v err=%v", pbs, err)
	}
}

func TestNativeUpdateRefusesLinkedPlaybook(t *testing.T) {
	resetCommandTestState(t)
	root := t.TempDir()
	config.PlaybooksDir = filepath.Join(root, "playbooks")
	config.ShellConfig = filepath.Join(root, "shellrc")
	source := filepath.Join(root, "source")
	external := filepath.Join(root, "external")
	if err := os.MkdirAll(source, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(external, 0755); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Write(external, &manifest.Manifest{Source: &manifest.Source{Repository: source}}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(config.PlaybooksDir, 0755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(config.PlaybooksDir, "linked")
	if err := os.Symlink(external, link); err != nil {
		t.Fatal(err)
	}

	err := runPlaybookUpdate("linked", nil)
	if err == nil || !strings.Contains(err.Error(), "native update is disabled") {
		t.Fatalf("expected linked update rejection, got %v", err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link was replaced: info=%v err=%v", info, err)
	}
}

func TestUpdateRejectsEscapingScriptPath(t *testing.T) {
	resetCommandTestState(t)
	root := t.TempDir()
	config.PlaybooksDir = filepath.Join(root, "playbooks")
	config.ShellConfig = filepath.Join(root, "shellrc")
	installed := filepath.Join(config.PlaybooksDir, "pb")
	if err := os.MkdirAll(installed, 0755); err != nil {
		t.Fatal(err)
	}
	data := "[source]\nupdate_script = \"../outside.sh\"\n"
	if err := os.WriteFile(filepath.Join(installed, manifest.FileName), []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
	if err := runPlaybookUpdate("pb", nil); err == nil {
		t.Fatal("expected escaping update script to be rejected")
	}
}

func TestPreserveExitCode(t *testing.T) {
	err := exec.Command("sh", "-c", "exit 42").Run()
	preserved := preserveExitCode(err)
	if code, ok := exitCode(preserved); !ok || code != 42 {
		t.Fatalf("exit code=%d ok=%v err=%v", code, ok, preserved)
	}
}
