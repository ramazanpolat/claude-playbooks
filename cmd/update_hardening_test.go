package cmd

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

func mkdirs(t *testing.T, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func writeFile(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A preserved nested path whose directory the incoming source turned into an
// external symlink must not be restored THROUGH that symlink.
func TestOverlayRefusesPreserveThroughExternalSymlink(t *testing.T) {
	root := t.TempDir()
	live := filepath.Join(root, "live")
	work := filepath.Join(root, "work")
	external := t.TempDir()
	mkdirs(t, live, work)
	writeFile(t, filepath.Join(live, "config", "private.txt"), "install-local")
	writeFile(t, filepath.Join(external, "private.txt"), "unrelated-external-file")
	if err := os.Symlink(external, filepath.Join(work, "config")); err != nil {
		t.Fatal(err)
	}

	_, err := overlaySource(work, live, []string{"config/private.txt"})
	if err == nil {
		t.Fatal("overlay restored a preserved path through an external symlink")
	}
	if got, _ := os.ReadFile(filepath.Join(external, "private.txt")); string(got) != "unrelated-external-file" {
		t.Fatalf("external file was overwritten: %q", got)
	}
	// Rolled back: the live tree is as it was.
	if got, _ := os.ReadFile(filepath.Join(live, "config", "private.txt")); string(got) != "install-local" {
		t.Fatalf("live file after rollback: %q", got)
	}
	if info, err := os.Lstat(filepath.Join(live, "config")); err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("live config dir not restored as a directory: %v %v", info, err)
	}
}

// A preserved file the install did not have must not be adopted from the
// source: its previous absence is restored.
func TestOverlayDoesNotAdoptSourceOnlyPreservedFiles(t *testing.T) {
	root := t.TempDir()
	live := filepath.Join(root, "live")
	work := filepath.Join(root, "work")
	mkdirs(t, live, work)
	writeFile(t, filepath.Join(work, "CLAUDE.md"), "new")
	writeFile(t, filepath.Join(work, ".credentials.json"), `{"claudeAiOauth":{"accessToken":"UPSTREAM"}}`)
	writeFile(t, filepath.Join(work, "settings.json"), `{"env":{"X":"upstream"}}`)

	if _, err := overlaySource(work, live, defaultPreserved); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{".credentials.json", "settings.json"} {
		if _, err := os.Lstat(filepath.Join(live, f)); err == nil {
			t.Fatalf("source-only %s was adopted into the install", f)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(live, "CLAUDE.md")); string(got) != "new" {
		t.Fatalf("update content missing: %q", got)
	}
}

// The live root's own mode is the pilot's; the overlay must not apply the
// source root's mode to it.
func TestOverlayKeepsLiveRootMode(t *testing.T) {
	root := t.TempDir()
	live := filepath.Join(root, "live")
	work := filepath.Join(root, "work")
	mkdirs(t, live, work)
	if err := os.Chmod(live, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(work, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(work, "CLAUDE.md"), "x")
	if _, err := overlaySource(work, live, nil); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(live); info.Mode().Perm() != 0o700 {
		t.Fatalf("live root mode changed to %v", info.Mode().Perm())
	}
}

// A failed overlay must remove what it introduced, not only restore what it
// moved. A Unix socket in the source is a deterministic way to make the copy
// fail partway; disk-full or I/O errors reach the same branch.
func TestOverlayRollbackRemovesIntroducedEntries(t *testing.T) {
	root := t.TempDir()
	live := filepath.Join(root, "live")
	work := filepath.Join(root, "work")
	mkdirs(t, live, work)
	writeFile(t, filepath.Join(live, "existing"), "old")
	writeFile(t, filepath.Join(work, "a-new-hook"), "new")
	writeFile(t, filepath.Join(work, "existing"), "updated")
	sock := filepath.Join(work, "zz-socket")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("cannot create a unix socket here: %v", err)
	}
	defer l.Close()

	if _, err := overlaySource(work, live, nil); err == nil {
		t.Fatal("overlay succeeded despite an uncopyable source entry")
	}
	if got, _ := os.ReadFile(filepath.Join(live, "existing")); string(got) != "old" {
		t.Fatalf("moved entry not restored: %q", got)
	}
	if _, err := os.Lstat(filepath.Join(live, "a-new-hook")); err == nil {
		t.Fatal("introduced entry survived the rollback")
	}
	if _, err := os.Lstat(filepath.Join(live, "zz-socket")); err == nil {
		t.Fatal("partially copied entry survived the rollback")
	}
	backups, _ := filepath.Glob(filepath.Join(root, ".live.bak.*"))
	if len(backups) != 0 {
		t.Fatalf("backup left behind after a complete rollback: %v", backups)
	}
}

// `--delete` is the wrapper's only in the leading positions; anywhere else it
// is claude's argument and must never remove the directory.
func TestStartDeleteIsNotTakenFromClaudeArgs(t *testing.T) {
	resetCommandTestState(t)
	home := os.Getenv("HOME")
	stub := filepath.Join(home, "stub-bin")
	argsFile := filepath.Join(home, "claude-args")
	if err := os.WriteFile(filepath.Join(stub, "claude"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$CPB_TEST_ARGS\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CPB_TEST_ARGS", argsFile)

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"after --", []string{"--", "--delete"}},
		{"as a -p value", []string{"-p", "--delete"}},
		{"after a claude flag", []string{"--version", "--delete"}},
	} {
		cfg := filepath.Join(home, "cfg-"+strings.ReplaceAll(tc.name, " ", "-"))
		writeFile(t, filepath.Join(cfg, "keep.txt"), "keep")
		if err := runStart(nil, append([]string{cfg}, tc.args...)); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if _, err := os.Stat(filepath.Join(cfg, "keep.txt")); err != nil {
			t.Fatalf("%s: the config directory was deleted", tc.name)
		}
		got, _ := os.ReadFile(argsFile)
		if !strings.Contains(string(got), "--delete") {
			t.Fatalf("%s: --delete was not forwarded to claude: %q", tc.name, got)
		}
	}

	// The documented spellings still delete: before the path and right after it.
	for _, args := range [][]string{{"--delete", "PATH"}, {"PATH", "--delete"}} {
		cfg := filepath.Join(home, "cfg-del-"+strings.Join(args, "_"))
		writeFile(t, filepath.Join(cfg, "keep.txt"), "keep")
		for i := range args {
			if args[i] == "PATH" {
				args[i] = cfg
			}
		}
		if err := runStart(nil, args); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(cfg); err == nil {
			t.Fatalf("%v: directory survived an intentional --delete", args)
		}
	}
}

// A fresh install takes the SOURCE root's mode even though staging goes
// through a 0700 MkdirTemp directory: only the live-update overlay keeps an
// existing root's mode.
func TestLocalInstallTakesSourceRootMode(t *testing.T) {
	resetCommandTestState(t)
	config.PlaybooksDir = filepath.Join(t.TempDir(), "playbooks")
	source := filepath.Join(t.TempDir(), "src")
	writeFile(t, filepath.Join(source, "CLAUDE.md"), "x")
	if err := os.Chmod(source, 0o755); err != nil {
		t.Fatal(err)
	}
	installNoAlias = true
	if err := runInstall(nil, []string{source}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(config.PlaybooksDir, "src"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("installed root mode = %v, want the source's 0755 (staging's 0700 leaked)", info.Mode().Perm())
	}
}

// The ordinary shared-credentials layout: the preserved entry IS a symlink
// pointing outside the install. Containment must judge the ancestors, not the
// link's target, or every such install would refuse to update whenever the
// source also ships that file.
func TestOverlayPreservesExternalSymlinkEntry(t *testing.T) {
	root := t.TempDir()
	live := filepath.Join(root, "live")
	work := filepath.Join(root, "work")
	mkdirs(t, live, work)
	global := filepath.Join(t.TempDir(), "global-credentials.json")
	writeFile(t, global, `{"claudeAiOauth":{"accessToken":"GLOBAL"}}`)
	if err := os.Symlink(global, filepath.Join(live, ".credentials.json")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(work, ".credentials.json"), `{"claudeAiOauth":{"accessToken":"UPSTREAM"}}`)
	writeFile(t, filepath.Join(work, "CLAUDE.md"), "new")

	if _, err := overlaySource(work, live, defaultPreserved); err != nil {
		t.Fatalf("update refused for a symlinked credentials entry: %v", err)
	}
	got, err := os.Readlink(filepath.Join(live, ".credentials.json"))
	if err != nil || got != global {
		t.Fatalf("credentials link after update = %q, %v; want %q", got, err, global)
	}
	if body, _ := os.ReadFile(global); string(body) != `{"claudeAiOauth":{"accessToken":"GLOBAL"}}` {
		t.Fatalf("global store touched: %s", body)
	}
}

// A symlinked ancestor that points INSIDE the install, at a sibling entry the
// overlay never touched (config -> data), is as bad as one pointing outside:
// data/ was never backed up, so an overwrite there is unrecoverable.
func TestOverlayRefusesPreserveIntoUntouchedSibling(t *testing.T) {
	root := t.TempDir()
	live := filepath.Join(root, "live")
	work := filepath.Join(root, "work")
	mkdirs(t, live, work)
	writeFile(t, filepath.Join(live, "config", "private.txt"), "install-local")
	writeFile(t, filepath.Join(live, "data", "private.txt"), "runtime-data")
	if err := os.Symlink("data", filepath.Join(work, "config")); err != nil {
		t.Fatal(err)
	}

	if _, err := overlaySource(work, live, []string{"config/private.txt"}); err == nil {
		t.Fatal("overlay restored a preserved path into an untouched sibling entry")
	}
	if got, _ := os.ReadFile(filepath.Join(live, "data", "private.txt")); string(got) != "runtime-data" {
		t.Fatalf("untouched runtime data was overwritten: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(live, "config", "private.txt")); string(got) != "install-local" {
		t.Fatalf("live file after rollback: %q", got)
	}
}

// On a case-insensitive filesystem the source's SETTINGS.JSON and the preserved
// settings.json are one file; preservation must match by identity, not name.
func TestOverlayPreservesCaseVariantEntries(t *testing.T) {
	root := t.TempDir()
	probe := filepath.Join(root, "Probe")
	writeFile(t, probe, "x")
	if _, err := os.Stat(filepath.Join(root, "probe")); err != nil {
		t.Skip("case-sensitive filesystem; the finding does not apply here")
	}
	live := filepath.Join(root, "live")
	work := filepath.Join(root, "work")
	mkdirs(t, live, work)
	writeFile(t, filepath.Join(live, "settings.json"), `{"local":true}`)
	writeFile(t, filepath.Join(work, "SETTINGS.JSON"), `{"upstream":true}`)
	writeFile(t, filepath.Join(work, ".CREDENTIALS.JSON"), `{"claudeAiOauth":{"accessToken":"UPSTREAM"}}`)

	if _, err := overlaySource(work, live, defaultPreserved); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(live, "settings.json")); string(got) != `{"local":true}` {
		t.Fatalf("local settings.json replaced by the upstream case variant: %s", got)
	}
	if _, err := os.Lstat(filepath.Join(live, ".credentials.json")); err == nil {
		t.Fatal("upstream .CREDENTIALS.JSON was adopted as the install's credentials")
	}
}

// "--" is never a path: `start -- --delete` must not resume wrapper parsing
// after it and delete a directory named "--".
func TestStartRejectsDelimiterAsPath(t *testing.T) {
	resetCommandTestState(t)
	wd, _ := os.Getwd()
	scratch := t.TempDir()
	if err := os.Chdir(scratch); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(wd) })
	writeFile(t, filepath.Join(scratch, "--", "keep.txt"), "keep")

	if err := runStart(nil, []string{"--", "--delete"}); err == nil {
		t.Fatal("start accepted -- as the path")
	}
	if _, err := os.Stat(filepath.Join(scratch, "--", "keep.txt")); err != nil {
		t.Fatal("a directory named -- was deleted")
	}
}

// A preserve list naming both an entry and a descendant of it, with the source
// introducing that entry into an install that lacked it: the first restore
// removes the introduced tree, and the descendant must not fail the update by
// finding its ancestor gone. Both orderings are covered by the collapse.
func TestOverlayPreserveAncestorAndDescendant(t *testing.T) {
	for _, list := range [][]string{
		{"config", "config/private.json"},
		{"config/private.json", "config"},
	} {
		root := t.TempDir()
		live := filepath.Join(root, "live")
		work := filepath.Join(root, "work")
		mkdirs(t, live, work)
		writeFile(t, filepath.Join(work, "config", "private.json"), `{"upstream":true}`)
		writeFile(t, filepath.Join(work, "CLAUDE.md"), "new")

		got, err := preservePaths(live, &manifest.Manifest{Update: &manifest.Update{Preserve: list}})
		if err != nil {
			t.Fatal(err)
		}
		for _, rel := range got {
			if rel == "config/private.json" {
				t.Fatalf("descendant not collapsed into its preserved ancestor: %v", got)
			}
		}
		if _, err := overlaySource(work, live, got); err != nil {
			t.Fatalf("%v: %v", list, err)
		}
		if _, err := os.Lstat(filepath.Join(live, "config")); err == nil {
			t.Fatalf("%v: introduced config/ was adopted", list)
		}
		if body, _ := os.ReadFile(filepath.Join(live, "CLAUDE.md")); string(body) != "new" {
			t.Fatalf("%v: update content missing", list)
		}
	}
	// And the raw case without the collapse: an already-absent ancestor is
	// treated as restored, not as an escape.
	root := t.TempDir()
	live := filepath.Join(root, "live")
	work := filepath.Join(root, "work")
	mkdirs(t, live, work)
	writeFile(t, filepath.Join(work, "config", "private.json"), `{"upstream":true}`)
	if _, err := overlaySource(work, live, []string{"config", "config/private.json"}); err != nil {
		t.Fatalf("descendant after its removed ancestor failed the update: %v", err)
	}
}

// The install had a FILE named config; the source introduces a config/
// directory and config/private.json is preserved. The backup path for the
// preserved file fails with ENOTDIR, which must read as "absent", not as an
// error that fails the update.
func TestOverlayPreserveThroughFormerFileIsAbsence(t *testing.T) {
	root := t.TempDir()
	live := filepath.Join(root, "live")
	work := filepath.Join(root, "work")
	mkdirs(t, live, work)
	writeFile(t, filepath.Join(live, "config"), "i was a file")
	writeFile(t, filepath.Join(work, "config", "private.json"), `{"upstream":true}`)
	writeFile(t, filepath.Join(work, "config", "public.json"), `{"ok":true}`)

	if _, err := overlaySource(work, live, []string{"config/private.json"}); err != nil {
		t.Fatalf("ENOTDIR on the backup path failed the update: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(live, "config", "private.json")); err == nil {
		t.Fatal("preserved path's previous absence was not restored")
	}
	if _, err := os.Stat(filepath.Join(live, "config", "public.json")); err != nil {
		t.Fatalf("unpreserved sibling from the source missing: %v", err)
	}
}

// The mirror of the backup-side ENOTDIR: the SOURCE introduces a regular file
// named config while config/private.json is preserved and locally absent. The
// absence restore hits ENOTDIR at the destination and must read as done.
func TestOverlayPreserveUnderIntroducedFileIsAbsence(t *testing.T) {
	root := t.TempDir()
	live := filepath.Join(root, "live")
	work := filepath.Join(root, "work")
	mkdirs(t, live, work)
	writeFile(t, filepath.Join(work, "config"), "now a file")

	if _, err := overlaySource(work, live, []string{"config/private.json"}); err != nil {
		t.Fatalf("ENOTDIR at the destination failed the update: %v", err)
	}
	if body, _ := os.ReadFile(filepath.Join(live, "config")); string(body) != "now a file" {
		t.Fatalf("introduced file missing or changed: %q", body)
	}
}
