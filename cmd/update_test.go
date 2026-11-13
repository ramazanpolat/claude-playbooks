package cmd

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

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
	// The backup held only the old manifest, which went live again, so
	// none is kept; there is at most one either way.
	backups, err := filepath.Glob(filepath.Join(config.PlaybooksDir, ".pb.bak.*"))
	if err != nil || len(backups) > 1 {
		t.Fatalf("backups=%v err=%v", backups, err)
	}
	for _, b := range backups {
		if _, err := os.Stat(filepath.Join(b, "data")); !os.IsNotExist(err) {
			t.Fatalf("runtime data was dragged into the backup: %v", err)
		}
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

// The consent names the migrate step by its declared path: "run it" read as
// running the playbook.
func TestUpdateMigrateConsentWording(t *testing.T) {
	run := migration{rel: "playbook/bin/migrate", sha256: strings.Repeat("ab", 32), from: "1.0.0", to: "1.1.0"}
	if got := run.consentPrompt("pb"); got != "Update pb and run its migrate step (playbook/bin/migrate)? [y/N] " {
		t.Fatalf("prompt: %q", got)
	}
	if err := run.consentRefusal("pb"); !strings.Contains(err.Error(), "pb declares a migrate step (playbook/bin/migrate (sha256 ") || !strings.Contains(err.Error(), "pass --yes to update and run the step, or --dry-run") {
		t.Fatalf("refusal: %v", err)
	}
	skip := run
	skip.skip = true
	if got := skip.consentPrompt("pb"); got != "Update pb without running its migrate step (playbook/bin/migrate)? [y/N] " {
		t.Fatalf("prompt, step not run: %q", got)
	}
	if err := skip.consentRefusal("pb"); !strings.Contains(err.Error(), "pass --yes to update without running it") {
		t.Fatalf("refusal, step not run: %v", err)
	}
}

// --dry-run compares the files, not only the versions: the same version
// with other files is not "up to date", and the line names what the update
// would replace. A preserved file is never listed: the update puts it
// back. After the update, a dry run finds nothing to replace.
func TestUpdateDryRunComparesTheFiles(t *testing.T) {
	installed, _ := migrateFixture(t, false)
	source := filepath.Join(filepath.Dir(config.PlaybooksDir), "source")
	write := func(dir, rel, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := manifest.Write(source, &manifest.Manifest{Version: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	write(source, "CLAUDE.md", "v1\n")
	write(source, "migrations/apply.sh", "#!/bin/sh\n")
	write(source, "settings.json", `{"from":"source"}`)
	if err := runPlaybookUpdate(io.Discard, "pb", updateOpts{yes: true}); err != nil {
		t.Fatal(err)
	}
	dry := func() string {
		t.Helper()
		var out strings.Builder
		if err := runPlaybookUpdate(&out, "pb", updateOpts{dryRun: true}); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	if out := dry(); !strings.Contains(out, "replaces:  nothing (the source's files are the installed ones)") || !strings.Contains(out, "  up to date\n") {
		t.Fatalf("the same files:\n%s", out)
	}
	// The same version, other files; the install's own settings differ too.
	write(source, "CLAUDE.md", "v1, edited\n")
	write(source, "hooks/check.sh", "#!/bin/sh\n")
	write(installed, "settings.json", `{"mine":true}`)
	out := dry()
	if !strings.Contains(out, "replaces:  CLAUDE.md (changed), hooks/ (new)\n") || strings.Contains(out, "up to date") ||
		!strings.Contains(out, "same version, other files: an update replaces what is listed above") || strings.Contains(out, "settings.json") {
		t.Fatalf("other files:\n%s", out)
	}
	if b, _ := os.ReadFile(filepath.Join(installed, "CLAUDE.md")); string(b) != "v1\n" {
		t.Fatal("--dry-run changed the playbook")
	}
	// A file the install has and the source no longer ships, in a folder
	// the source ships, goes with the folder.
	write(installed, "migrations/old.sh", "#!/bin/sh\n")
	if out := dry(); !strings.Contains(out, "migrations/ (changed)") {
		t.Fatalf("an installed extra file:\n%s", out)
	}
	if err := runPlaybookUpdate(io.Discard, "pb", updateOpts{yes: true}); err != nil {
		t.Fatal(err)
	}
	if out := dry(); !strings.Contains(out, "replaces:  nothing") {
		t.Fatalf("after the update:\n%s", out)
	}
	// A file's mode alone counts.
	if err := os.Chmod(filepath.Join(source, "hooks", "check.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if out := dry(); !strings.Contains(out, "replaces:  hooks/ (changed)\n") {
		t.Fatalf("a mode:\n%s", out)
	}
}

// dryRunUpdate is pb's --dry-run output.
func dryRunUpdate(t *testing.T) string {
	t.Helper()
	var out strings.Builder
	if err := runPlaybookUpdate(&out, "pb", updateOpts{dryRun: true}); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

// A git source keeps a link inside it as a link, and the overlay writes
// the file or folder it names: right after an update the dry run finds
// nothing to replace.
func TestUpdateDryRunComparesLinksAsTheOverlayWritesThem(t *testing.T) {
	resetCommandTestState(t)
	root := t.TempDir()
	config.PlaybooksDir = filepath.Join(root, "playbooks")
	repo := filepath.Join(root, "repo")
	installed := filepath.Join(config.PlaybooksDir, "pb")
	for _, d := range []string{filepath.Join(repo, "shared"), installed} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := manifest.Write(repo, &manifest.Manifest{Version: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	for rel, body := range map[string]string{"CLAUDE.md": "v1\n", "shared/rules.md": "rules\n"} {
		if err := os.WriteFile(filepath.Join(repo, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range map[string]string{"AGENTS.md": "CLAUDE.md", "rules": "shared"} {
		if err := os.Symlink(target, filepath.Join(repo, link)); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "initial"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := manifest.Write(installed, &manifest.Manifest{Version: "1.0.0", Source: &manifest.Source{Repository: "file://" + repo}}); err != nil {
		t.Fatal(err)
	}
	if out := dryRunUpdate(t); !strings.Contains(out, "AGENTS.md (new)") || !strings.Contains(out, "rules/ (new)") {
		t.Fatalf("before the update:\n%s", out)
	}
	if err := runPlaybookUpdate(io.Discard, "pb", updateOpts{yes: true}); err != nil {
		t.Fatal(err)
	}
	// The overlay wrote the link to a file as a file, the one to a folder
	// as a folder.
	if fi, err := os.Lstat(filepath.Join(installed, "AGENTS.md")); err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("AGENTS.md: %v %v", fi, err)
	}
	if fi, err := os.Lstat(filepath.Join(installed, "rules")); err != nil || !fi.IsDir() {
		t.Fatalf("rules: %v %v", fi, err)
	}
	if out := dryRunUpdate(t); !strings.Contains(out, "replaces:  nothing (the source's files are the installed ones)") || !strings.Contains(out, "  up to date\n") {
		t.Fatalf("after the update:\n%s", out)
	}
	// .git counts by the commit it holds: another one, with the same files.
	cmd := exec.Command("git", "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-q", "--allow-empty", "-m", "next")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
	if out := dryRunUpdate(t); !strings.Contains(out, "replaces:  .git/ (changed)\n") || !strings.Contains(out, "same version, other files") {
		t.Fatalf("another commit:\n%s", out)
	}
	if err := runPlaybookUpdate(io.Discard, "pb", updateOpts{yes: true}); err != nil {
		t.Fatal(err)
	}
	if out := dryRunUpdate(t); !strings.Contains(out, "replaces:  nothing") {
		t.Fatalf("after the second update:\n%s", out)
	}
}

// gitHead reads the commit HEAD names from the files of a .git folder.
func TestGitHead(t *testing.T) {
	const a = "0123456789abcdef0123456789abcdef01234567"
	const b = "89abcdef0123456789abcdef0123456789abcdef"
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"a loose ref", map[string]string{"HEAD": "ref: refs/heads/main\n", "refs/heads/main": a + "\n"}, a},
		{"packed-refs", map[string]string{"HEAD": "ref: refs/heads/main\n", "packed-refs": "# pack-refs with: peeled\n" + b + " refs/heads/other\n" + a + " refs/heads/main\n"}, a},
		{"detached", map[string]string{"HEAD": b + "\n"}, b},
		{"no ref", map[string]string{"HEAD": "ref: refs/heads/main\n"}, ""},
		{"a ref out of the folder", map[string]string{"HEAD": "ref: refs/../../HEAD\n", "../HEAD": a + "\n"}, ""},
		{"not a ref", map[string]string{"HEAD": "ref: HEAD\n"}, ""},
		{"not an id", map[string]string{"HEAD": "ref: refs/heads/main\n", "refs/heads/main": "main\n"}, ""},
		{"no HEAD", map[string]string{}, ""},
	} {
		dir := filepath.Join(t.TempDir(), ".git")
		for rel, body := range tc.files {
			path := filepath.Join(dir, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if got := gitHead(dir); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
	// packed-refs over its cap is not read.
	dir := filepath.Join(t.TempDir(), ".git")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("# padding\n", gitPackedRefsMax/10+1) + a + " refs/heads/main\n"
	if err := os.WriteFile(filepath.Join(dir, "packed-refs"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := gitHead(dir); got != "" {
		t.Errorf("packed-refs over its cap: %q", got)
	}
}

// A folder's mode counts, since the overlay writes each folder with the
// source's mode; the manifest's counts as the update leaves it: the
// installed manifest's mode masked by the source's.
func TestUpdateDryRunComparesFolderAndManifestModes(t *testing.T) {
	installed, _ := migrateFixture(t, false)
	source := filepath.Join(filepath.Dir(config.PlaybooksDir), "source")
	mode := func(rel string) os.FileMode {
		t.Helper()
		fi, err := os.Stat(filepath.Join(installed, rel))
		if err != nil {
			t.Fatal(err)
		}
		return fi.Mode().Perm()
	}
	chmod := func(path string, m os.FileMode) {
		t.Helper()
		if err := os.Chmod(path, m); err != nil {
			t.Fatal(err)
		}
	}
	update := func() {
		t.Helper()
		if err := runPlaybookUpdate(io.Discard, "pb", updateOpts{yes: true}); err != nil {
			t.Fatal(err)
		}
	}
	nothing := func(what string) {
		t.Helper()
		if out := dryRunUpdate(t); !strings.Contains(out, "replaces:  nothing") || !strings.Contains(out, "  up to date\n") {
			t.Fatalf("%s:\n%s", what, out)
		}
	}
	update()
	nothing("after the first update")

	// A folder with the same entries and another mode.
	chmod(filepath.Join(source, "migrations"), 0o700)
	if out := dryRunUpdate(t); !strings.Contains(out, "replaces:  migrations/ (changed)\n") || strings.Contains(out, "up to date") {
		t.Fatalf("a folder's mode:\n%s", out)
	}
	update()
	if m := mode("migrations"); m != 0o700 {
		t.Fatalf("the update left migrations/ at %o", m)
	}
	nothing("after the folder's update")

	// An installed 0644 manifest and a 0600 one in the source: the update
	// tightens the installed one.
	chmod(filepath.Join(installed, manifest.FileName), 0o644)
	chmod(filepath.Join(source, manifest.FileName), 0o600)
	if out := dryRunUpdate(t); !strings.Contains(out, "replaces:  .playbook (changed)\n") || !strings.Contains(out, "same version, other files") {
		t.Fatalf("a tighter manifest in the source:\n%s", out)
	}
	update()
	if m := mode(manifest.FileName); m != 0o600 {
		t.Fatalf("the update left the manifest at %o", m)
	}
	nothing("after the manifest's update")

	// The reverse: an installed 0600 manifest stays 0600 against a 0644 one.
	chmod(filepath.Join(source, manifest.FileName), 0o644)
	nothing("a looser manifest in the source")
	update()
	if m := mode(manifest.FileName); m != 0o600 {
		t.Fatalf("the update loosened the manifest to %o", m)
	}
}

// The dry run reads the install's manifest again once the source is
// staged, under the registry lock, as the update does: a change made
// while staging ran is what it compares with, and an install re-sourced
// meanwhile stops it as it stops the update.
func TestUpdateDryRunReadsTheManifestAfterStaging(t *testing.T) {
	installed, _ := migrateFixture(t, false)
	if err := runPlaybookUpdate(io.Discard, "pb", updateOpts{yes: true}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { updateStaged = func() {} })
	alter := func(change func(m *manifest.Manifest)) {
		updateStaged = func() {
			m, err := manifest.Read(installed)
			if err != nil {
				t.Fatal(err)
			}
			change(m)
			if err := manifest.Write(installed, m); err != nil {
				t.Fatal(err)
			}
		}
	}
	// An install-local field set while staging ran: the update keeps it,
	// so the manifest is not one it would replace.
	alter(func(m *manifest.Manifest) { m.LabelVars = []string{"TERM_PROGRAM"} })
	if out := dryRunUpdate(t); !strings.Contains(out, "replaces:  nothing") || !strings.Contains(out, "  up to date\n") {
		t.Fatalf("a field set while staging ran:\n%s", out)
	}
	// Re-sourced while staging ran.
	alter(func(m *manifest.Manifest) { m.Source.Branch = "other" })
	err := runPlaybookUpdate(io.Discard, "pb", updateOpts{dryRun: true})
	if err == nil || !strings.Contains(err.Error(), "changed while the update was staging") {
		t.Fatalf("re-sourced: %v", err)
	}
}

// gitCommit commits everything in repo.
func gitCommit(t *testing.T, repo, message string) {
	t.Helper()
	for _, args := range [][]string{{"add", "-A"}, {"-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-q", "--allow-empty", "-m", message}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// dryRunWithin is pb's --dry-run output, failing the test when the dry run
// has not ended in a few seconds: one that waits on a FIFO never does.
func dryRunWithin(t *testing.T) string {
	t.Helper()
	type result struct {
		out string
		err error
	}
	done := make(chan result, 1)
	go func() {
		var out strings.Builder
		err := runPlaybookUpdate(&out, "pb", updateOpts{dryRun: true})
		done <- result{out.String(), err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatal(r.err)
		}
		return r.out
	case <-time.After(5 * time.Second):
		t.Fatal("the dry run waited")
	}
	return ""
}

// A local source that is a git repository is compared as files, .git
// included: the update copies its .git as it is, so a changed config or an
// added hook at the same commit is listed. A link in it is compared by its
// target and never read through, so a link to a FIFO does not hold the
// dry run.
func TestUpdateDryRunComparesALocalSourcesGitAsFiles(t *testing.T) {
	installed, _ := migrateFixture(t, false)
	source := filepath.Join(filepath.Dir(config.PlaybooksDir), "source")
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = source
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	gitCommit(t, source, "initial")
	if err := runPlaybookUpdate(io.Discard, "pb", updateOpts{yes: true}); err != nil {
		t.Fatal(err)
	}
	if out := dryRunWithin(t); !strings.Contains(out, "replaces:  nothing") {
		t.Fatalf("after the update:\n%s", out)
	}
	appendTo := func(path, line string) {
		t.Helper()
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString(line)
		f.Close()
	}
	appendTo(filepath.Join(source, ".git", "config"), "[cpbtest]\n\tx = 1\n")
	if out := dryRunWithin(t); !strings.Contains(out, "replaces:  .git/ (changed)\n") || !strings.Contains(out, "same version, other files") {
		t.Fatalf("a changed .git/config at the same commit:\n%s", out)
	}
	if err := runPlaybookUpdate(io.Discard, "pb", updateOpts{yes: true}); err != nil {
		t.Fatal(err)
	}
	appendTo(filepath.Join(source, ".git", "hooks", "post-checkout"), "#!/bin/sh\n")
	if out := dryRunWithin(t); !strings.Contains(out, "replaces:  .git/ (changed)\n") {
		t.Fatalf("an added hook at the same commit:\n%s", out)
	}
	// HEAD a link to a FIFO out of the source: compared by its target.
	fifo := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	head := filepath.Join(source, ".git", "HEAD")
	if err := os.Remove(head); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fifo, head); err != nil {
		t.Fatal(err)
	}
	if out := dryRunWithin(t); !strings.Contains(out, ".git/ (changed)") {
		t.Fatalf("HEAD a link to a FIFO:\n%s", out)
	}
	if fi, err := os.Lstat(filepath.Join(installed, ".git", "HEAD")); err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("the dry run changed the install's HEAD: %v %v", fi, err)
	}
}

// A git source's .git is compared by the commit HEAD names only when every
// file on the way is a regular one reached without a link: an install
// whose HEAD is a link, or whose ref is a FIFO, is compared as files,
// without reading through the link or waiting on the FIFO.
func TestUpdateDryRunReadsAGitURLsHeadWithoutLinksOrWaiting(t *testing.T) {
	resetCommandTestState(t)
	root := t.TempDir()
	config.PlaybooksDir = filepath.Join(root, "playbooks")
	repo := filepath.Join(root, "repo")
	installed := filepath.Join(config.PlaybooksDir, "pb")
	for _, d := range []string{repo, installed} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := manifest.Write(repo, &manifest.Manifest{Version: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	gitCommit(t, repo, "initial")
	if err := manifest.Write(installed, &manifest.Manifest{Version: "1.0.0", Source: &manifest.Source{Repository: "file://" + repo}}); err != nil {
		t.Fatal(err)
	}
	if err := runPlaybookUpdate(io.Discard, "pb", updateOpts{yes: true}); err != nil {
		t.Fatal(err)
	}
	if out := dryRunWithin(t); !strings.Contains(out, "replaces:  nothing") {
		t.Fatalf("after the update:\n%s", out)
	}
	// HEAD a link to a file out of the install that names the same ref.
	head := filepath.Join(installed, ".git", "HEAD")
	b, err := os.ReadFile(head)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "HEAD")
	if err := os.WriteFile(outside, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(head); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, head); err != nil {
		t.Fatal(err)
	}
	if out := dryRunWithin(t); !strings.Contains(out, "replaces:  .git/ (changed)\n") {
		t.Fatalf("HEAD a link:\n%s", out)
	}
	if err := os.Remove(head); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(head, b, 0o644); err != nil {
		t.Fatal(err)
	}
	// refs a link to a copy out of the install: compared as files.
	refs := filepath.Join(installed, ".git", "refs")
	copied := filepath.Join(t.TempDir(), "refs")
	if err := copyDir(refs, copied); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(refs, refs+".kept"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(copied, refs); err != nil {
		t.Fatal(err)
	}
	if out := dryRunWithin(t); !strings.Contains(out, "replaces:  .git/ (changed)\n") {
		t.Fatalf("refs a link:\n%s", out)
	}
	if err := os.Remove(refs); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(refs+".kept", refs); err != nil {
		t.Fatal(err)
	}
	if out := dryRunWithin(t); !strings.Contains(out, "replaces:  nothing") {
		t.Fatalf("refs put back:\n%s", out)
	}
	// The ref HEAD names a FIFO (the loose ref, or packed-refs without one).
	ref := filepath.Join(installed, ".git", filepath.FromSlash(strings.TrimPrefix(strings.TrimSpace(string(b)), "ref: ")))
	if _, err := os.Lstat(ref); err != nil {
		ref = filepath.Join(installed, ".git", "packed-refs")
	}
	if err := os.Remove(ref); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(ref, 0o600); err != nil {
		t.Fatal(err)
	}
	if out := dryRunWithin(t); !strings.Contains(out, "replaces:  .git/ (changed)\n") {
		t.Fatalf("the ref a FIFO:\n%s", out)
	}
}

// What the update puts back after the overlay is left out on both sides:
// a skill ADD SKILL recorded, over a source that ships skills/; a preserved
// path below a folder the source does not ship; and, on a filesystem that
// ignores case, a preserved file the source ships under another case.
func TestUpdateDryRunLeavesOutWhatTheUpdatePutsBack(t *testing.T) {
	installed, _ := migrateFixture(t, false)
	source := filepath.Join(filepath.Dir(config.PlaybooksDir), "source")
	write := func(dir, rel, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := manifest.Write(source, &manifest.Manifest{Version: "2.0.0", Update: &manifest.Update{Preserve: []string{"hooks/sub/mine.txt"}}}); err != nil {
		t.Fatal(err)
	}
	write(source, "skills/shipped/SKILL.md", "---\nname: shipped\ndescription: d\n---\n")
	write(source, "hooks/check.sh", "#!/bin/sh\n")
	update := func() {
		t.Helper()
		if err := runPlaybookUpdate(io.Discard, "pb", updateOpts{yes: true}); err != nil {
			t.Fatal(err)
		}
	}
	upToDate := func(what string) {
		t.Helper()
		if out := dryRunUpdate(t); !strings.Contains(out, "replaces:  nothing") || !strings.Contains(out, "  up to date\n") {
			t.Fatalf("%s:\n%s", what, out)
		}
	}
	update()
	upToDate("after the first update")

	// A recorded skill, put back over the source's skills/.
	src, _ := filepath.EvalSymlinks(makeSkill(t, filepath.Join(t.TempDir(), "extra")))
	mustStmt(t, "ALTER PLAYBOOK pb ADD SKILL extra FROM "+src)
	update()
	if _, err := os.Lstat(filepath.Join(installed, "skills", "extra")); err != nil {
		t.Fatalf("the update did not put the skill back: %v", err)
	}
	upToDate("after an update that put a recorded skill back")
	update()
	upToDate("after the next update")

	// A preserved path below a folder the source does not ship.
	write(installed, "hooks/sub/mine.txt", "mine\n")
	update()
	if b, err := os.ReadFile(filepath.Join(installed, "hooks", "sub", "mine.txt")); err != nil || string(b) != "mine\n" {
		t.Fatalf("the update did not keep the preserved file: %q %v", b, err)
	}
	upToDate("after an update that kept a nested preserved file")
	// Anything else in that folder the update does not keep: listed.
	write(installed, "hooks/sub/other.txt", "other\n")
	if out := dryRunUpdate(t); !strings.Contains(out, "replaces:  hooks/ (changed)\n") {
		t.Fatalf("an unpreserved file beside it:\n%s", out)
	}
	if err := os.Remove(filepath.Join(installed, "hooks", "sub", "other.txt")); err != nil {
		t.Fatal(err)
	}

	// The preserved settings.json, shipped as Settings.json.
	probe := filepath.Join(installed, "case-probe")
	write(installed, "case-probe", "")
	_, err := os.Lstat(filepath.Join(installed, "CASE-PROBE"))
	os.Remove(probe)
	if err != nil {
		t.Log("a case-sensitive filesystem: Settings.json is another file here")
		return
	}
	write(installed, "settings.json", `{"mine":true}`)
	write(source, "Settings.json", `{"from":"source"}`)
	update()
	if b, _ := os.ReadFile(filepath.Join(installed, "settings.json")); string(b) != `{"mine":true}` {
		t.Fatalf("the update did not keep the install's settings: %s", b)
	}
	upToDate("a preserved file shipped under another case")
}
