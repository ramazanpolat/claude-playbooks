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

	if err := runPlaybookUpdate("pb", false); err != nil {
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
	pbs, err := playbook.Discover(config.PlaybooksDir)
	if err != nil || len(pbs) != 1 || pbs[0].Name != "pb" {
		t.Fatalf("backup was discovered as a playbook: pbs=%v err=%v", pbs, err)
	}
}

func TestNativeUpdateRestoresInstallName(t *testing.T) {
	resetCommandTestState(t)
	root := t.TempDir()
	config.PlaybooksDir = filepath.Join(root, "playbooks")
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
	// The source ships its own name; the install carries a stale one.
	if err := manifest.Write(source, &manifest.Manifest{Name: "upstream"}); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Write(installed, &manifest.Manifest{Name: "kommander", Source: &manifest.Source{Repository: source}}); err != nil {
		t.Fatal(err)
	}

	if err := runPlaybookUpdate("pb", false); err != nil {
		t.Fatal(err)
	}

	m, err := manifest.Read(installed)
	if err != nil || m == nil {
		t.Fatalf("updated manifest: m=%#v err=%v", m, err)
	}
	if m.Name != "pb" {
		t.Fatalf("manifest name = %q, want \"pb\"", m.Name)
	}
	if m.Source == nil || m.Source.Repository != source {
		t.Fatalf("source metadata lost: %#v", m.Source)
	}
}

func TestNativeUpdateRefusesLinkedPlaybook(t *testing.T) {
	resetCommandTestState(t)
	root := t.TempDir()
	config.PlaybooksDir = filepath.Join(root, "playbooks")
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

	err := runPlaybookUpdate("linked", false)
	if err == nil || !strings.Contains(err.Error(), "native update is disabled") {
		t.Fatalf("expected linked update rejection, got %v", err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link was replaced: info=%v err=%v", info, err)
	}
}

func TestPreserveExitCode(t *testing.T) {
	err := exec.Command("sh", "-c", "exit 42").Run()
	preserved := preserveExitCode(err)
	if code, ok := exitCode(preserved); !ok || code != 42 {
		t.Fatalf("exit code=%d ok=%v err=%v", code, ok, preserved)
	}
}

func TestUpdateRejectsEscapingPreservePath(t *testing.T) {
	resetCommandTestState(t)
	root := t.TempDir()
	config.PlaybooksDir = filepath.Join(root, "playbooks")
	installed := filepath.Join(config.PlaybooksDir, "pb")
	if err := os.MkdirAll(installed, 0755); err != nil {
		t.Fatal(err)
	}
	data := "[source]\nrepository = \"" + filepath.Join(root, "source") + "\"\n\n[update]\npreserve = [\"../outside.conf\"]\n"
	if err := os.WriteFile(filepath.Join(installed, manifest.FileName), []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
	if err := runPlaybookUpdate("pb", false); err == nil {
		t.Fatal("expected escaping preserve path to be rejected")
	}
}

// The install's settings.json is tracked by many playbooks so that a generic
// installer lands a wired-up install, but the live file carries the pilot's
// own API routing, model pins and permissions. An update must never replace it
// with the stock copy.
func TestNativeUpdatePreservesSettings(t *testing.T) {
	resetCommandTestState(t)
	root := t.TempDir()
	config.PlaybooksDir = filepath.Join(root, "playbooks")
	source := filepath.Join(root, "source")
	installed := filepath.Join(config.PlaybooksDir, "pb")
	for _, d := range []string{filepath.Join(source, "hooks"), filepath.Join(installed, "hooks")} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(source, "settings.json"), "{\"stock\":true}\n")
	write(filepath.Join(source, "settings.json.template"), "{\"stock\":true}\n")
	write(filepath.Join(source, "hooks", "start.sh"), "new\n")
	write(filepath.Join(installed, "settings.json"), "{\"mine\":true}\n")
	write(filepath.Join(installed, "settings.local.json"), "{\"local\":true}\n")
	write(filepath.Join(installed, "hooks", "start.sh"), "old\n")
	if err := manifest.Write(installed, &manifest.Manifest{Source: &manifest.Source{Repository: source}}); err != nil {
		t.Fatal(err)
	}

	if err := runPlaybookUpdate("pb", false); err != nil {
		t.Fatal(err)
	}

	if got, err := os.ReadFile(filepath.Join(installed, "settings.json")); err != nil || string(got) != "{\"mine\":true}\n" {
		t.Fatalf("settings.json was clobbered: %q err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(installed, "settings.local.json")); err != nil || string(got) != "{\"local\":true}\n" {
		t.Fatalf("settings.local.json=%q err=%v", got, err)
	}
	// Everything else still updates, and new stock settings arrive alongside.
	if got, err := os.ReadFile(filepath.Join(installed, "hooks", "start.sh")); err != nil || string(got) != "new\n" {
		t.Fatalf("hooks were not updated: %q err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(installed, "settings.json.template")); err != nil {
		t.Fatalf("stock template did not land: %v", err)
	}
}

// Runtime state the source knows nothing about must not be copied, moved, or
// backed up: an install is a live config dir written continuously by whatever
// session is running in it.
func TestNativeUpdateLeavesRuntimeStateInPlace(t *testing.T) {
	resetCommandTestState(t)
	root := t.TempDir()
	config.PlaybooksDir = filepath.Join(root, "playbooks")
	source := filepath.Join(root, "source")
	installed := filepath.Join(config.PlaybooksDir, "pb")
	data := filepath.Join(installed, "data", "tasks")
	for _, d := range []string{source, data} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "CLAUDE.md"), []byte("new\n"), 0644); err != nil {
		t.Fatal(err)
	}
	live := filepath.Join(data, "log.md")
	if err := os.WriteFile(live, []byte("session\n"), 0644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(live)
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.Write(installed, &manifest.Manifest{Source: &manifest.Source{Repository: source}}); err != nil {
		t.Fatal(err)
	}

	if err := runPlaybookUpdate("pb", false); err != nil {
		t.Fatal(err)
	}

	after, err := os.Stat(live)
	if err != nil {
		t.Fatalf("runtime data was lost: %v", err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("runtime data was copied through the update instead of left in place")
	}
	backups, err := filepath.Glob(filepath.Join(config.PlaybooksDir, ".pb.bak.*"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("backups=%v err=%v", backups, err)
	}
	if _, err := os.Stat(filepath.Join(backups[0], "data")); !os.IsNotExist(err) {
		t.Fatalf("runtime data was dragged into the backup: %v", err)
	}
}

func TestNativeUpdateRunsMigrations(t *testing.T) {
	resetCommandTestState(t)
	root := t.TempDir()
	config.PlaybooksDir = filepath.Join(root, "playbooks")
	source := filepath.Join(root, "source")
	installed := filepath.Join(config.PlaybooksDir, "pb")
	if err := os.MkdirAll(filepath.Join(source, "migrations"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(installed, 0755); err != nil {
		t.Fatal(err)
	}
	receipt := filepath.Join(root, "migrated.txt")
	apply := "#!/bin/sh\nprintf '%s %s %s\\n' \"$1\" \"$2\" \"$3\" > " + receipt + "\n"
	if err := os.WriteFile(filepath.Join(source, "migrations", "apply.sh"), []byte(apply), 0755); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Write(source, &manifest.Manifest{Version: "2.0.0"}); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Write(installed, &manifest.Manifest{Version: "1.0.0", Source: &manifest.Source{Repository: source}}); err != nil {
		t.Fatal(err)
	}

	if err := runPlaybookUpdate("pb", false); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(receipt)
	if err != nil {
		t.Fatalf("migrations did not run: %v", err)
	}
	want := "1.0.0 2.0.0 " + installed + "\n"
	if string(got) != want {
		t.Fatalf("migration args=%q want %q", got, want)
	}
	m, err := manifest.Read(installed)
	if err != nil || m == nil || m.Version != "2.0.0" {
		t.Fatalf("version not advanced: m=%#v err=%v", m, err)
	}
}

func TestNativeUpdateCheckDoesNotInstall(t *testing.T) {
	resetCommandTestState(t)
	root := t.TempDir()
	config.PlaybooksDir = filepath.Join(root, "playbooks")
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
	if err := os.WriteFile(filepath.Join(installed, "CLAUDE.md"), []byte("old\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Write(source, &manifest.Manifest{Version: "2.0.0"}); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Write(installed, &manifest.Manifest{Version: "1.0.0", Source: &manifest.Source{Repository: source}}); err != nil {
		t.Fatal(err)
	}

	if err := runPlaybookUpdate("pb", true); err != nil {
		t.Fatal(err)
	}

	if got, err := os.ReadFile(filepath.Join(installed, "CLAUDE.md")); err != nil || string(got) != "old\n" {
		t.Fatalf("--check installed the update: %q err=%v", got, err)
	}
	backups, err := filepath.Glob(filepath.Join(config.PlaybooksDir, ".pb.bak.*"))
	if err != nil || len(backups) != 0 {
		t.Fatalf("--check made a backup: %v err=%v", backups, err)
	}
}
