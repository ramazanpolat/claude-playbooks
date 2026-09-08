package cmd

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envprofile"
	"github.com/ramazanpolat/claude-playbooks/internal/launcher"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// feedStdin points os.Stdin at a pipe preloaded with in, so interactive
// confirm() prompts can be answered. confirm() builds its reader from
// os.Stdin at call time, so the swap takes effect.
func feedStdin(t *testing.T, in string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	// The write runs in its own goroutine so an input larger than the pipe
	// buffer cannot deadlock before the command under test drains it.
	go func() {
		_, _ = io.WriteString(w, in)
		w.Close()
	}()
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = old
		r.Close()
	})
}

// sandboxRoot resets command state and repoints HOME and the playbooks root
// at a fresh sandbox, so every test gets the same isolation contract.
// Returns the sandboxed playbooks root.
func sandboxRoot(t *testing.T, name string) string {
	t.Helper()
	resetCommandTestState(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, name)
	config.PlaybooksDir = root
	return root
}

// sandboxDefaultRoot sandboxes the DEFAULT playbooks root, so launcher
// mutations (gated on the default root by launcherOpsAllowed) run inside
// the test. Returns the sandboxed playbooks root.
func sandboxDefaultRoot(t *testing.T) string {
	t.Helper()
	return sandboxRoot(t, ".claude-playbooks")
}

// writePlaybook creates a playbook directory with a CLAUDE.md and, when m is
// non-nil, a .playbook manifest.
func writePlaybook(t *testing.T, root, name string, m *manifest.Manifest) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("# "+name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if m != nil {
		if m.Name == "" {
			m.Name = name
		}
		if err := manifest.Write(dir, m); err != nil {
			t.Fatal(err)
		}
	}
}

// listRow returns the data row for a playbook from `list` output.
func listRow(t *testing.T, out, name string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, name+"  ") {
			return line
		}
	}
	t.Fatalf("no row for %q in list output:\n%s", name, out)
	return ""
}

// --- list ---

func TestListEmptyRootPrintsHint(t *testing.T) {
	config.PlaybooksDir = sandboxRoot(t, "playbooks")

	out := captureStdout(t, func() {
		if err := runList(nil, nil); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "No playbooks found") {
		t.Fatalf("empty root should print the getting-started hint, got:\n%s", out)
	}
}

func TestListPrefixFiltersByName(t *testing.T) {
	config.PlaybooksDir = sandboxRoot(t, "playbooks")
	for _, name := range []string{"alpha", "alphabet", "beta"} {
		writePlaybook(t, config.PlaybooksDir, name, nil)
	}

	out := captureStdout(t, func() {
		if err := runList(nil, []string{"alph"}); err != nil {
			t.Fatal(err)
		}
	})
	listRow(t, out, "alpha")
	listRow(t, out, "alphabet")
	if strings.Contains(out, "beta") {
		t.Fatalf("prefix filter did not exclude non-matching playbook:\n%s", out)
	}
	if !strings.Contains(out, "NAME") || !strings.Contains(out, "COMMAND") {
		t.Fatalf("header row missing from list output:\n%s", out)
	}
}

func TestListCommandColumnShowsOnlyExistingLaunchers(t *testing.T) {
	root := sandboxDefaultRoot(t)
	// withcmd advertises alias "wc"; only its launcher exists. nocmd has no
	// launcher at all. The reserved cpb launcher must never surface as a
	// playbook command — written by hand here because launcher.Write
	// (rightly) refuses reserved names.
	writePlaybook(t, root, "withcmd", &manifest.Manifest{Alias: "wc"})
	writePlaybook(t, root, "nocmd", nil)
	if _, err := launcher.Write(config.LauncherDir, "wc"); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(exe, filepath.Join(config.LauncherDir, "cpb")); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if err := runList(nil, nil); err != nil {
			t.Fatal(err)
		}
	})
	if row := listRow(t, out, "withcmd"); !strings.Contains(row, " wc ") {
		t.Fatalf("withcmd row should show its launcher command wc, got %q", row)
	}
	if row := listRow(t, out, "nocmd"); !strings.Contains(row, " - ") {
		t.Fatalf("nocmd row should show '-' (launcher missing), got %q", row)
	}
	if strings.Contains(out, " cpb ") {
		t.Fatalf("reserved name cpb advertised as a playbook command:\n%s", out)
	}
}

func TestFormatAge(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		at   time.Time
		want string
	}{
		{"zero", time.Time{}, "never"},
		{"seconds", now.Add(-10 * time.Second), "just now"},
		{"minutes", now.Add(-5 * time.Minute), "5 minutes ago"},
		{"hours", now.Add(-3 * time.Hour), "3 hours ago"},
		{"yesterday", now.Add(-30 * time.Hour), "yesterday"},
		{"days", now.Add(-5 * 24 * time.Hour), "5 days ago"},
	}
	for _, tc := range cases {
		if got := formatAge(tc.at); got != tc.want {
			t.Errorf("%s: formatAge = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// --- info ---

func TestInfoRendersManifestFields(t *testing.T) {
	config.PlaybooksDir = sandboxRoot(t, "playbooks")
	writePlaybook(t, config.PlaybooksDir, "rich", &manifest.Manifest{
		Version:     "1.2.3",
		Alias:       "ri",
		Description: "A rich playbook",
		Homepage:    "https://example.com",
		Author:      "Tester",
	})
	rich := filepath.Join(config.PlaybooksDir, "rich")
	if err := os.MkdirAll(filepath.Join(rich, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rich, "sub", "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if err := runInfo(nil, []string{"rich"}); err != nil {
			t.Fatal(err)
		}
	})
	for _, want := range []string{
		"Name:        rich",
		"Version:     1.2.3",
		"Path:        " + rich,
		"Type:        directory",
		"Alias:       ri",
		"Size:        3 files, 1 directories", // CLAUDE.md, .playbook, sub/f.txt; dir: sub
		"Last used:   just now",
		"Description: A rich playbook",
		"Homepage:    https://example.com",
		"Author:      Tester",
		"Update from: (no [source] metadata; cannot update)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("info output missing %q, got:\n%s", want, out)
		}
	}
}

func TestInfoShowsSourceAndMigrationRunner(t *testing.T) {
	config.PlaybooksDir = sandboxRoot(t, "playbooks")
	writePlaybook(t, config.PlaybooksDir, "upd", &manifest.Manifest{
		Source: &manifest.Source{Repository: "https://example.com/playbooks"},
	})
	migrations := filepath.Join(config.PlaybooksDir, "upd", "migrations")
	if err := os.MkdirAll(migrations, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(migrations, "apply.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if err := runInfo(nil, []string{"upd"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "Update from: https://example.com/playbooks") {
		t.Fatalf("update source not reported, got:\n%s", out)
	}
	if !strings.Contains(out, "Migrations:  migrations/apply.sh") {
		t.Fatalf("migration runner not reported, got:\n%s", out)
	}
}

func TestInfoShowsSymlinkTypeForLinkedPlaybook(t *testing.T) {
	sandbox := sandboxRoot(t, "playbooks")
	target := filepath.Join(sandbox, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "CLAUDE.md"), []byte("# external\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(config.PlaybooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(config.PlaybooksDir, "linked")); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if err := runInfo(nil, []string{"linked"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "Type:        symlink → "+target) {
		t.Fatalf("linked playbook should show its symlink target, got:\n%s", out)
	}
}

func TestInfoUnknownPlaybookErrors(t *testing.T) {
	config.PlaybooksDir = sandboxRoot(t, "playbooks")

	err := runInfo(nil, []string{"ghost"})
	if err == nil {
		t.Fatal("expected an error for an unknown playbook")
	}
	if !strings.Contains(err.Error(), "unknown playbook") {
		t.Fatalf("error = %v", err)
	}
}

// --- delete ---

func TestDeleteYesRemovesPlaybookDirectory(t *testing.T) {
	sandboxDefaultRoot(t)
	writePlaybook(t, config.PlaybooksDir, "victim", nil)
	deleteYes = true

	out := captureStdout(t, func() {
		if err := runDelete(nil, []string{"victim"}); err != nil {
			t.Fatal(err)
		}
	})
	if _, err := os.Stat(filepath.Join(config.PlaybooksDir, "victim")); !os.IsNotExist(err) {
		t.Fatalf("playbook directory still present, err=%v", err)
	}
	if !strings.Contains(out, `Deleted playbook "victim".`) {
		t.Fatalf("deletion not confirmed in output, got:\n%s", out)
	}
}

func TestDeleteUnknownNameErrors(t *testing.T) {
	sandboxDefaultRoot(t)
	deleteYes = true

	err := runDelete(nil, []string{"ghost"})
	if err == nil {
		t.Fatal("expected an error for an unknown playbook")
	}
	if !strings.Contains(err.Error(), "not found under") {
		t.Fatalf("error = %v", err)
	}
}

func TestDeleteDeclinedKeepsPlaybook(t *testing.T) {
	sandboxDefaultRoot(t)
	writePlaybook(t, config.PlaybooksDir, "victim", nil)
	deleteYes = false
	feedStdin(t, "n\n")

	out := captureStdout(t, func() {
		if err := runDelete(nil, []string{"victim"}); err != nil {
			t.Fatal(err)
		}
	})
	if _, err := os.Stat(filepath.Join(config.PlaybooksDir, "victim")); err != nil {
		t.Fatalf("declined delete removed the playbook: %v", err)
	}
	if !strings.Contains(out, "Cancelled.") {
		t.Fatalf("declined delete should print Cancelled., got:\n%s", out)
	}
}

// A linked playbook is only a symlink in the registry: deleting it removes
// the link, never the external source directory (README's link contract).
func TestDeleteLinkedPlaybookRemovesSymlinkOnly(t *testing.T) {
	sandbox := sandboxRoot(t, "playbooks")
	target := filepath.Join(sandbox, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "CLAUDE.md"), []byte("# source\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(config.PlaybooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(config.PlaybooksDir, "linked")); err != nil {
		t.Fatal(err)
	}
	deleteYes = true

	if err := runDelete(nil, []string{"linked"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(config.PlaybooksDir, "linked")); !os.IsNotExist(err) {
		t.Fatalf("registry symlink still present, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "CLAUDE.md")); err != nil {
		t.Fatalf("linked source directory was removed: %v", err)
	}
}

// An unclaimed launcher is RETAINED with a manual-removal hint: a stateless
// symlink may be serving another registry root, so removal stays the user's
// call. Noisy, never silently wrong.
func TestDeleteRetainsUnclaimedLauncherWithHint(t *testing.T) {
	sandboxDefaultRoot(t)
	writePlaybook(t, config.PlaybooksDir, "victim", nil)
	if _, err := launcher.Write(config.LauncherDir, "victim"); err != nil {
		t.Fatal(err)
	}
	deleteYes = true

	out := captureStdout(t, func() {
		if err := runDelete(nil, []string{"victim"}); err != nil {
			t.Fatal(err)
		}
	})
	if _, exists, foreign := launcher.Lookup(config.LauncherDir, "victim"); !exists || foreign {
		t.Fatalf("unclaimed launcher should be retained, exists=%v foreign=%v", exists, foreign)
	}
	if !strings.Contains(out, "remove it manually") {
		t.Fatalf("retained launcher should print a manual-removal hint, got:\n%s", out)
	}
}

// A launcher whose name still addresses another playbook is kept silently
// (no rm hint): deleting victim must not take a live command with it.
func TestDeleteKeepsLauncherStillAddressingAnotherPlaybook(t *testing.T) {
	root := sandboxDefaultRoot(t)
	writePlaybook(t, root, "victim", nil)
	// "other" claims the command name "victim" via its manifest alias —
	// the registry, not the symlink, owns command-name ownership.
	writePlaybook(t, root, "other", &manifest.Manifest{Alias: "victim"})
	if _, err := launcher.Write(config.LauncherDir, "victim"); err != nil {
		t.Fatal(err)
	}
	deleteYes = true

	out := captureStdout(t, func() {
		if err := runDelete(nil, []string{"victim"}); err != nil {
			t.Fatal(err)
		}
	})
	if _, exists, foreign := launcher.Lookup(config.LauncherDir, "victim"); !exists || foreign {
		t.Fatalf("claimed launcher should be retained, exists=%v foreign=%v", exists, foreign)
	}
	if !strings.Contains(out, `still addresses playbook "other"`) {
		t.Fatalf("kept launcher should name the claiming playbook, got:\n%s", out)
	}
}

// A directory that exists at the expected path but is not discoverable
// (dot-named, so discovery skips it) goes through the orphan path: removed
// with an explicit message rather than reported as unknown.
func TestDeleteOrphanRemovesNonDiscoverableDirectory(t *testing.T) {
	sandboxRoot(t, "playbooks")
	orphan := filepath.Join(config.PlaybooksDir, ".hidden")
	if err := os.MkdirAll(orphan, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphan, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	deleteYes = true

	out := captureStdout(t, func() {
		if err := runDelete(nil, []string{".hidden"}); err != nil {
			t.Fatal(err)
		}
	})
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphan directory still present, err=%v", err)
	}
	if !strings.Contains(out, `Deleted ".hidden".`) {
		t.Fatalf("orphan deletion not confirmed in output, got:\n%s", out)
	}
}

// The env profile store is dot-named, so discovery skips it and a delete by
// name would fall through to the orphan path; it must be refused by name and
// by file identity (a case variant on a case-insensitive filesystem).
func TestDeleteRefusesEnvProfileStore(t *testing.T) {
	sandboxRoot(t, "playbooks")
	store := envprofile.Dir(config.PlaybooksDir)
	if err := envprofile.Write(store, &envprofile.Profile{Name: "glm", Set: map[string]string{"A": "1"}}); err != nil {
		t.Fatal(err)
	}
	deleteYes = true
	for _, name := range []string{envprofile.DirName, strings.ToUpper(envprofile.DirName)} {
		if name != envprofile.DirName {
			if _, err := os.Stat(filepath.Join(config.PlaybooksDir, name)); err != nil {
				continue // case-sensitive filesystem: the variant is simply not found
			}
		}
		err := runDelete(nil, []string{name})
		if err == nil || !strings.Contains(err.Error(), "env profile store") {
			t.Fatalf("delete %q: %v", name, err)
		}
	}
	if p, err := envprofile.Read(store, "glm"); err != nil || p == nil {
		t.Fatalf("profile store damaged: %v %v", p, err)
	}
}

// A leftover symlink pointing AT the store is deletable (only the link goes);
// a leftover directory the store is symlinked INTO is refused, because
// RemoveAll would descend into the store's physical location.
func TestDeleteStoreSymlinkShapes(t *testing.T) {
	sandboxRoot(t, "playbooks")
	root := config.PlaybooksDir
	store := envprofile.Dir(root)
	if err := envprofile.Write(store, &envprofile.Profile{Name: "glm", Set: map[string]string{"A": "1"}}); err != nil {
		t.Fatal(err)
	}
	deleteYes = true

	// 1. link -> store: the link is removed, the store survives.
	link := filepath.Join(root, ".oldlink")
	if err := os.Symlink(store, link); err != nil {
		t.Fatal(err)
	}
	if err := runDelete(nil, []string{".oldlink"}); err != nil {
		t.Fatalf("deleting a symlink to the store: %v", err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatal("link not removed")
	}
	if p, err := envprofile.Read(store, "glm"); err != nil || p == nil {
		t.Fatalf("store damaged by deleting a link to it: %v %v", p, err)
	}

	// 2. store -> inside a leftover directory: deleting the leftover is refused.
	if err := os.RemoveAll(store); err != nil {
		t.Fatal(err)
	}
	leftover := filepath.Join(root, ".leftover")
	inner := filepath.Join(leftover, "profiles")
	if err := envprofile.Write(inner, &envprofile.Profile{Name: "glm", Set: map[string]string{"A": "1"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(inner, store); err != nil {
		t.Fatal(err)
	}
	err := runDelete(nil, []string{".leftover"})
	if err == nil || !strings.Contains(err.Error(), "contains the registry's env profile store") {
		t.Fatalf("deleting the directory the store lives in: %v", err)
	}
	if p, err := envprofile.Read(store, "glm"); err != nil || p == nil {
		t.Fatalf("store damaged: %v %v", p, err)
	}
	// The message names the canonical store, whatever spelling was typed.
	if err := runDelete(nil, []string{envprofile.DirName}); err == nil || !strings.Contains(err.Error(), `".env-profiles" is the registry's env profile store`) {
		t.Fatalf("message: %v", err)
	}
}

// Identity, not spelling: the store's registry entry can be a symlink, the
// typed name a case variant, the playbooks root relative. None of these may
// reach the store.
func TestDeleteStoreGuardByIdentity(t *testing.T) {
	sandboxRoot(t, "playbooks")
	root := config.PlaybooksDir
	store := envprofile.Dir(root)
	leftover := filepath.Join(root, ".leftover")
	inner := filepath.Join(leftover, "profiles")
	if err := envprofile.Write(inner, &envprofile.Profile{Name: "glm", Set: map[string]string{"A": "1"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(inner, store); err != nil {
		t.Fatal(err)
	}
	deleteYes = true
	caseInsensitive := false
	if _, err := os.Stat(filepath.Join(root, ".LEFTOVER")); err == nil {
		caseInsensitive = true
	}

	// The store's registry SYMLINK addressed by a case variant: refused, link intact.
	if caseInsensitive {
		if err := runDelete(nil, []string{".ENV-PROFILES"}); err == nil || !strings.Contains(err.Error(), "env profile store") {
			t.Fatalf("case variant of the store link: %v", err)
		}
		if _, err := os.Lstat(store); err != nil {
			t.Fatal("store link removed")
		}
		// The directory the store resolves into, addressed by a case variant.
		if err := runDelete(nil, []string{".LEFTOVER"}); err == nil || !strings.Contains(err.Error(), "contains the registry's env profile store") {
			t.Fatalf("case variant of the containing directory: %v", err)
		}
	}

	// A relative playbooks root: the containment check must not compare a
	// relative path against the store's absolute target.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(filepath.Dir(root)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	rel := filepath.Base(root)
	if err := refuseRegistryOwned(rel, ".leftover", filepath.Join(rel, ".leftover")); err == nil || !strings.Contains(err.Error(), "contains the registry's env profile store") {
		t.Fatalf("relative playbooks root: %v", err)
	}
	if p, err := envprofile.Read(store, "glm"); err != nil || p == nil {
		t.Fatalf("store damaged: %v %v", p, err)
	}
}

// The store is protected along its whole symlink chain: an intermediate link
// and the directory holding it are refused; an unrelated link is not.
func TestDeleteStoreGuardCoversTheResolutionChain(t *testing.T) {
	sandboxRoot(t, "playbooks")
	root := config.PlaybooksDir
	store := envprofile.Dir(root)
	holder := filepath.Join(root, ".holder")
	final := filepath.Join(t.TempDir(), "profiles")
	if err := envprofile.Write(final, &envprofile.Profile{Name: "glm", Set: map[string]string{"A": "1"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(holder, 0o755); err != nil {
		t.Fatal(err)
	}
	bridge := filepath.Join(holder, "bridge")
	if err := os.Symlink(final, bridge); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(".holder", "bridge"), store); err != nil { // relative target
		t.Fatal(err)
	}
	deleteYes = true
	if err := runDelete(nil, []string{".holder"}); err == nil || !strings.Contains(err.Error(), "or a link it resolves through") {
		t.Fatalf("directory holding an intermediate link: %v", err)
	}
	if err := refuseRegistryOwned(root, "bridge", bridge); err == nil || !strings.Contains(err.Error(), "resolves through") {
		t.Fatalf("intermediate link itself: %v", err)
	}
	unrelated := filepath.Join(root, ".unrelated")
	if err := os.Symlink(final, unrelated); err != nil {
		t.Fatal(err)
	}
	if err := runDelete(nil, []string{".unrelated"}); err != nil {
		t.Fatalf("unrelated link to the same target must be deletable: %v", err)
	}
	if p, err := envprofile.Read(store, "glm"); err != nil || p == nil {
		t.Fatalf("store damaged: %v %v", p, err)
	}
}

// A symlink in a PARENT component of the store's target is part of the
// resolution too, and a resolution that cannot be established refuses.
func TestDeleteStoreGuardResolvesParentComponents(t *testing.T) {
	sandboxRoot(t, "playbooks")
	root := config.PlaybooksDir
	store := envprofile.Dir(root)
	leftover := filepath.Join(root, ".leftover")
	if err := envprofile.Write(filepath.Join(leftover, "sub", "profiles"), &envprofile.Profile{Name: "glm", Set: map[string]string{"A": "1"}}); err != nil {
		t.Fatal(err)
	}
	bridge := filepath.Join(root, ".bridge")
	if err := os.Symlink(filepath.Join(".leftover", "sub"), bridge); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(".bridge", "profiles"), store); err != nil {
		t.Fatal(err)
	}
	deleteYes = true
	if err := runDelete(nil, []string{".bridge"}); err == nil || !strings.Contains(err.Error(), "resolves through") {
		t.Fatalf("parent-component link: %v", err)
	}
	if err := runDelete(nil, []string{".leftover"}); err == nil || !strings.Contains(err.Error(), "contains the registry's env profile store") {
		t.Fatalf("directory holding the parent-component target: %v", err)
	}
	if p, err := envprofile.Read(store, "glm"); err != nil || p == nil {
		t.Fatalf("store damaged: %v %v", p, err)
	}

	// A looped store cannot be verified: the delete of anything else is refused.
	if err := os.Remove(store); err != nil {
		t.Fatal(err)
	}
	loop := filepath.Join(root, ".loop")
	if err := os.Symlink(store, loop); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(loop, store); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(root, ".other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runDelete(nil, []string{".other"}); err == nil || !strings.Contains(err.Error(), "cannot verify") {
		t.Fatalf("looped store: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("directory removed despite an unverifiable store")
	}
}
