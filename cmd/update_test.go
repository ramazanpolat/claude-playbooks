package cmd

import (
	"io"
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

	if err := updateOnePlaybook("pb", false); err != nil {
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
	if err := manifest.Write(installed, &manifest.Manifest{Name: "toolkit", Source: &manifest.Source{Repository: source}}); err != nil {
		t.Fatal(err)
	}

	if err := updateOnePlaybook("pb", false); err != nil {
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

	err := updateOnePlaybook("linked", false)
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
	if err := updateOnePlaybook("pb", false); err == nil {
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

	if err := updateOnePlaybook("pb", false); err != nil {
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

	if err := updateOnePlaybook("pb", false); err != nil {
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

// migrateFixture is a playbook pb at 1.0.0 whose source, at 2.0.0, ships
// migrations/apply.sh; declared puts it in the source's [update] migrate.
// The script writes its arguments to the returned receipt.
func migrateFixture(t *testing.T, declared bool) (installed, receipt string) {
	t.Helper()
	resetCommandTestState(t)
	root := t.TempDir()
	config.PlaybooksDir = filepath.Join(root, "playbooks")
	source := filepath.Join(root, "source")
	installed = filepath.Join(config.PlaybooksDir, "pb")
	for _, d := range []string{filepath.Join(source, "migrations"), installed} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	receipt = filepath.Join(root, "migrated.txt")
	apply := "#!/bin/sh\nprintf '%s %s %s\\n' \"$1\" \"$2\" \"$3\" > " + receipt + "\n"
	if err := os.WriteFile(filepath.Join(source, "migrations", "apply.sh"), []byte(apply), 0755); err != nil {
		t.Fatal(err)
	}
	src := &manifest.Manifest{Version: "2.0.0"}
	if declared {
		src.Update = &manifest.Update{Migrate: "migrations/apply.sh"}
	}
	if err := manifest.Write(source, src); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Write(installed, &manifest.Manifest{Version: "1.0.0", Source: &manifest.Source{Repository: source}}); err != nil {
		t.Fatal(err)
	}
	return installed, receipt
}

func installedVersion(t *testing.T, dir string) string {
	t.Helper()
	m, err := manifest.Read(dir)
	if err != nil || m == nil {
		t.Fatalf("manifest: %#v %v", m, err)
	}
	return m.Version
}

// A declared migrate step runs after the new files are in place, as
// <script> <from> <to> <install dir>.
func TestUpdateRunsTheDeclaredMigrateStep(t *testing.T) {
	installed, receipt := migrateFixture(t, true)
	if err := runPlaybookUpdate(io.Discard, "pb", updateOpts{yes: true}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(receipt)
	if err != nil {
		t.Fatalf("the migrate step did not run: %v", err)
	}
	if want := "1.0.0 2.0.0 " + installed + "\n"; string(got) != want {
		t.Fatalf("migrate args=%q want %q", got, want)
	}
	if v := installedVersion(t, installed); v != "2.0.0" {
		t.Fatalf("version not advanced: %s", v)
	}
}

// Only a declared step runs: a source that ships migrations/apply.sh and
// does not declare it updates without running it.
func TestUpdateDoesNotRunAnUndeclaredScript(t *testing.T) {
	installed, receipt := migrateFixture(t, false)
	if err := runPlaybookUpdate(io.Discard, "pb", updateOpts{yes: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(receipt); !os.IsNotExist(err) {
		t.Fatalf("an undeclared script ran: %v", err)
	}
	if v := installedVersion(t, installed); v != "2.0.0" {
		t.Fatalf("version not advanced: %s", v)
	}
}

// The step comes from the source being updated to, never from the
// installed copy: an install that declares one, updating from a source that
// does not, runs nothing.
func TestUpdateIgnoresTheInstalledCopysMigrateStep(t *testing.T) {
	installed, receipt := migrateFixture(t, false)
	m, err := manifest.Read(installed)
	if err != nil {
		t.Fatal(err)
	}
	m.Update = &manifest.Update{Migrate: "migrations/apply.sh"}
	if err := manifest.Write(installed, m); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(installed, "migrations"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installed, "migrations", "apply.sh"), []byte("#!/bin/sh\ntouch "+receipt+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runPlaybookUpdate(io.Discard, "pb", updateOpts{yes: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(receipt); !os.IsNotExist(err) {
		t.Fatal("the installed copy's migrate step ran")
	}
}

// Without a terminal and without --yes, a declared step refuses the whole
// update before anything changes.
func TestUpdateMigrateStepNeedsConsent(t *testing.T) {
	installed, receipt := migrateFixture(t, true)
	updateAsks = func() bool { return false }
	t.Cleanup(func() { updateAsks = func() bool { return isTerminal(os.Stdin) && isTerminal(os.Stdout) } })
	err := runPlaybookUpdate(io.Discard, "pb", updateOpts{})
	if err == nil || !strings.Contains(err.Error(), "pass --yes") || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(receipt); !os.IsNotExist(err) {
		t.Fatal("the step ran without consent")
	}
	if v := installedVersion(t, installed); v != "1.0.0" {
		t.Fatalf("the update went ahead without consent: %s", v)
	}
}

// A declared step needs consent even when it will not run (no version on
// one side): the update itself still runs ahead of it.
func TestUpdateMigrateStepNeedsConsentEvenWhenSkipped(t *testing.T) {
	installed, receipt := migrateFixture(t, true)
	// By hand: manifest.Write supplies a default version.
	noVersion := "name = \"pb\"\n\n[source]\nrepository = " + quoteTOML(readSource(t, installed).Repository) + "\n"
	if err := os.WriteFile(filepath.Join(installed, manifest.FileName), []byte(noVersion), 0o644); err != nil {
		t.Fatal(err)
	}
	updateAsks = func() bool { return false }
	t.Cleanup(func() { updateAsks = func() bool { return isTerminal(os.Stdin) && isTerminal(os.Stdout) } })
	if err := runPlaybookUpdate(io.Discard, "pb", updateOpts{}); err == nil || !strings.Contains(err.Error(), "pass --yes") {
		t.Fatalf("err = %v", err)
	}
	if err := runPlaybookUpdate(io.Discard, "pb", updateOpts{yes: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(receipt); !os.IsNotExist(err) {
		t.Fatal("a step with no version on one side ran")
	}
}

// readSource is the [source] a fixture's installed manifest records.
func readSource(t *testing.T, dir string) *manifest.Source {
	t.Helper()
	m, err := manifest.Read(dir)
	if err != nil || m == nil || m.Source == nil {
		t.Fatalf("manifest: %#v %v", m, err)
	}
	return m.Source
}

// On a terminal the step is asked about: no cancels the update, yes runs
// it.
func TestUpdateMigrateStepAsksOnATerminal(t *testing.T) {
	installed, receipt := migrateFixture(t, true)
	updateAsks = func() bool { return true }
	t.Cleanup(func() { updateAsks = func() bool { return isTerminal(os.Stdin) && isTerminal(os.Stdout) } })
	var out strings.Builder
	feedStdin(t, "n\n")
	if err := runPlaybookUpdate(&out, "pb", updateOpts{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "declares a migrate step: migrations/apply.sh (sha256 ") || !strings.Contains(out.String(), "Cancelled; nothing was changed.") {
		t.Fatalf("declined:\n%s", out.String())
	}
	if v := installedVersion(t, installed); v != "1.0.0" {
		t.Fatalf("a declined update changed the playbook: %s", v)
	}
	feedStdin(t, "y\n")
	if err := runPlaybookUpdate(io.Discard, "pb", updateOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(receipt); err != nil {
		t.Fatalf("a confirmed step did not run: %v", err)
	}
}

// --dry-run shows the step with its sha256, and changes nothing.
func TestUpdateDryRunShowsTheMigrateStep(t *testing.T) {
	installed, receipt := migrateFixture(t, true)
	var out strings.Builder
	if err := runPlaybookUpdate(&out, "pb", updateOpts{dryRun: true}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"installed: 1.0.0", "available: 2.0.0", "migrate:   migrations/apply.sh (sha256 ", "run as migrations/apply.sh 1.0.0 2.0.0 <install dir>", "Nothing was changed (--dry-run)."} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("--dry-run lacks %q:\n%s", want, out.String())
		}
	}
	if _, err := os.Stat(receipt); !os.IsNotExist(err) {
		t.Fatal("--dry-run ran the step")
	}
	if v := installedVersion(t, installed); v != "1.0.0" {
		t.Fatalf("--dry-run changed the playbook: %s", v)
	}
}

// A declared step must resolve inside the source: a symlink out of it is
// refused before anything changes.
func TestUpdateRefusesAMigrateStepOutsideThePlaybook(t *testing.T) {
	installed, receipt := migrateFixture(t, true)
	m, err := manifest.Read(installed)
	if err != nil {
		t.Fatal(err)
	}
	source := m.Source.Repository
	outside := filepath.Join(t.TempDir(), "evil.sh")
	if err := os.WriteFile(outside, []byte("#!/bin/sh\ntouch "+receipt+"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(source, "migrations", "apply.sh")
	if err := os.Remove(script); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, script); err != nil {
		t.Fatal(err)
	}
	err = runPlaybookUpdate(io.Discard, "pb", updateOpts{yes: true})
	if err == nil || !strings.Contains(err.Error(), "resolves outside") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(receipt); !os.IsNotExist(err) {
		t.Fatal("a step outside the playbook ran")
	}
	if v := installedVersion(t, installed); v != "1.0.0" {
		t.Fatalf("the update went ahead: %s", v)
	}
}

func TestNativeUpdateDryRunDoesNotInstall(t *testing.T) {
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

	if err := updateOnePlaybook("pb", true); err != nil {
		t.Fatal(err)
	}

	if got, err := os.ReadFile(filepath.Join(installed, "CLAUDE.md")); err != nil || string(got) != "old\n" {
		t.Fatalf("--dry-run installed the update: %q err=%v", got, err)
	}
	backups, err := filepath.Glob(filepath.Join(config.PlaybooksDir, ".pb.bak.*"))
	if err != nil || len(backups) != 0 {
		t.Fatalf("--dry-run made a backup: %v err=%v", backups, err)
	}
}

// updateOnePlaybook is the single-playbook update as these tests exercise it:
// output discarded, run as a script runs it (no terminal, --yes). An
// already-current playbook is re-applied rather than skipped, which is how a
// drifted install is repaired.
func updateOnePlaybook(name string, dryRun bool) error {
	return runPlaybookUpdate(io.Discard, name, updateOpts{dryRun: dryRun, yes: true})
}

// update takes one name; the self-update moved to its own command.
func TestUpdateNeedsANameAndPointsAtSelfUpdate(t *testing.T) {
	if err := updateCmd.Args(updateCmd, nil); err == nil || !strings.Contains(err.Error(), "cpb self-update") {
		t.Fatalf("update with no name: %v", err)
	}
	if c, _, err := rootCmd.Find([]string{"self-update"}); err != nil || c != selfUpdateCmd {
		t.Fatalf("self-update is not a command: %v", err)
	}
}

// The played-playbook flags are refused for a playbook that updates from its
// [source].
func TestUpdateRefusesPlayFlagsForASourcePlaybook(t *testing.T) {
	migrateFixture(t, false)
	if err := updateCmd.Flags().Set("trust-secret", "keychain:x"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		updateTrustSecret = nil
		updateCmd.Flags().Lookup("trust-secret").Changed = false
	})
	if err := runUpdate(updateCmd, []string{"pb"}); err == nil || !strings.Contains(err.Error(), "--trust-secret applies to a playbook kept by cpb play") {
		t.Fatalf("err = %v", err)
	}
}
