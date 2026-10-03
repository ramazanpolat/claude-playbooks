package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envset"
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

// --- list ---

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

func TestShowPlaybookManifestFields(t *testing.T) {
	config.PlaybooksDir = sandboxRoot(t, "playbooks")
	writePlaybook(t, config.PlaybooksDir, "rich", &manifest.Manifest{
		Version:     "1.2.3",
		Launcher:    "ri",
		Description: "A rich playbook",
		Homepage:    "https://example.com",
		Author:      "Tester",
	})
	writePlaybook(t, config.PlaybooksDir, "plain", nil)

	out := mustStmt(t, "SHOW PLAYBOOK rich")
	for _, want := range []string{
		"Version:      1.2.3",
		"Description:  A rich playbook",
		"Homepage:     https://example.com",
		"Author:       Tester",
		"Launcher:     ri",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("SHOW PLAYBOOK missing %q, got:\n%s", want, out)
		}
	}
	if out := mustStmt(t, "SHOW PLAYBOOK plain"); strings.Contains(out, "Description") || strings.Contains(out, "Homepage") || strings.Contains(out, "Author") {
		t.Errorf("a playbook with no such fields shows them:\n%s", out)
	}

	var v map[string]any
	if err := json.Unmarshal([]byte(mustStmt(t, "SHOW PLAYBOOK rich --json")), &v); err != nil {
		t.Fatal(err)
	}
	if v["description"] != "A rich playbook" || v["homepage"] != "https://example.com" || v["author"] != "Tester" {
		t.Errorf("--json fields = %v, %v, %v", v["description"], v["homepage"], v["author"])
	}
	used, _ := v["last_used"].(string)
	if ts, err := time.Parse(time.RFC3339, used); err != nil || time.Since(ts) > time.Hour {
		t.Errorf("last_used = %q (%v)", used, err)
	}
	if err := json.Unmarshal([]byte(mustStmt(t, "SHOW PLAYBOOK plain --json")), &v); err != nil {
		t.Fatal(err)
	}
	if v["description"] != nil || v["homepage"] != nil || v["author"] != nil || v["migrate"] != nil {
		t.Errorf("plain --json fields = %v, %v, %v, %v; want null", v["description"], v["homepage"], v["author"], v["migrate"])
	}

	// The declared migrate step, which cpb update runs.
	writePlaybook(t, config.PlaybooksDir, "migrating", &manifest.Manifest{Update: &manifest.Update{Migrate: "migrations/apply.sh"}})
	if out := mustStmt(t, "SHOW PLAYBOOK migrating"); !strings.Contains(out, "Migrate:") || !strings.Contains(out, "migrations/apply.sh (run by cpb update)") {
		t.Errorf("SHOW PLAYBOOK lacks the migrate step:\n%s", out)
	}
	if err := json.Unmarshal([]byte(mustStmt(t, "SHOW PLAYBOOK migrating --json")), &v); err != nil || v["migrate"] != "migrations/apply.sh" {
		t.Errorf("--json migrate = %v (%v)", v["migrate"], err)
	}
}

// --- delete ---

func TestDeleteYesRemovesPlaybookDirectory(t *testing.T) {
	sandboxDefaultRoot(t)
	writePlaybook(t, config.PlaybooksDir, "victim", nil)

	out := captureStdout(t, func() {
		if err := doDelete(deleteOpts{yes: true}, []string{"victim"}); err != nil {
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

	err := doDelete(deleteOpts{yes: true}, []string{"ghost"})
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
	feedStdin(t, "n\n")

	out := captureStdout(t, func() {
		if err := doDelete(deleteOpts{}, []string{"victim"}); err != nil {
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

	if err := doDelete(deleteOpts{yes: true}, []string{"linked"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(config.PlaybooksDir, "linked")); !os.IsNotExist(err) {
		t.Fatalf("registry symlink still present, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "CLAUDE.md")); err != nil {
		t.Fatalf("linked source directory was removed: %v", err)
	}
}

// An unclaimed launcher named for the deleted playbook is REMOVED: this tool
// only writes launchers for the default root, so nothing else can be
// served by a name nobody in the registry claims.
func TestDeleteRemovesUnclaimedLauncher(t *testing.T) {
	sandboxDefaultRoot(t)
	t.Setenv("CPB_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	writePlaybook(t, config.PlaybooksDir, "victim", nil)
	if _, err := launcher.Write(config.LauncherDir, "victim"); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if err := doDelete(deleteOpts{yes: true}, []string{"victim"}); err != nil {
			t.Fatal(err)
		}
	})
	if _, exists, _ := launcher.Lookup(config.LauncherDir, "victim"); exists {
		t.Fatal("unclaimed launcher survived the delete of its playbook")
	}
	if !strings.Contains(out, `Removed launcher "victim"`) {
		t.Fatalf("removal not reported:\n%s", out)
	}
	if got := launcher.Recorded(); len(got) != 0 {
		t.Fatalf("receipt still lists the removed launcher: %v", got)
	}
}

// A launcher whose name still addresses another playbook is kept silently
// (no rm hint): deleting victim must not take a live command with it.
func TestDeleteKeepsLauncherStillAddressingAnotherPlaybook(t *testing.T) {
	root := sandboxDefaultRoot(t)
	writePlaybook(t, root, "victim", nil)
	// "other" claims the launcher name "victim" via its manifest launcher —
	// the registry, not the symlink, owns command-name ownership.
	writePlaybook(t, root, "other", &manifest.Manifest{Launcher: "victim"})
	if _, err := launcher.Write(config.LauncherDir, "victim"); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if err := doDelete(deleteOpts{yes: true}, []string{"victim"}); err != nil {
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

// A directory that exists at the expected path but is not a discoverable
// playbook (dot-named, so discovery skips it) is refused as not found, and
// left alone.
func TestDropRefusesANonPlaybookDirectory(t *testing.T) {
	sandboxRoot(t, "playbooks")
	orphan := filepath.Join(config.PlaybooksDir, ".hidden")
	if err := os.MkdirAll(orphan, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := doDelete(deleteOpts{yes: true}, []string{".hidden"}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v, want not found", err)
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Fatalf("a directory that is not a playbook was touched: %v", err)
	}
}

// The env set store is dot-named, so discovery skips it; a delete by
// name must be refused by name and by file identity (a case variant on a case-insensitive filesystem).
func TestDeleteRefusesEnvProfileStore(t *testing.T) {
	sandboxRoot(t, "playbooks")
	store := envset.Dir(config.PlaybooksDir)
	if err := envset.Write(store, &envset.Set{Name: "glm", Set: map[string]string{"A": "1"}}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{envset.DirName, strings.ToUpper(envset.DirName)} {
		if name != envset.DirName {
			if _, err := os.Stat(filepath.Join(config.PlaybooksDir, name)); err != nil {
				continue // case-sensitive filesystem: the variant is simply not found
			}
		}
		err := doDelete(deleteOpts{yes: true}, []string{name})
		if err == nil || !strings.Contains(err.Error(), "env set store") {
			t.Fatalf("delete %q: %v", name, err)
		}
	}
	if p, err := envset.Read(store, "glm"); err != nil || p == nil {
		t.Fatalf("profile store damaged: %v %v", p, err)
	}
}

// A leftover symlink pointing AT the store is deletable (only the link goes);
// a leftover directory the store is symlinked INTO is refused, because
// RemoveAll would descend into the store's physical location.
func TestDeleteStoreSymlinkShapes(t *testing.T) {
	sandboxRoot(t, "playbooks")
	root := config.PlaybooksDir
	store := envset.Dir(root)
	if err := envset.Write(store, &envset.Set{Name: "glm", Set: map[string]string{"A": "1"}}); err != nil {
		t.Fatal(err)
	}

	// 1. link -> store: a link is not the store (removing it never descends
	// into its target), so the guard lets it through.
	link := filepath.Join(root, ".oldlink")
	if err := os.Symlink(store, link); err != nil {
		t.Fatal(err)
	}
	if err := refuseRegistryOwned(root, ".oldlink", link); err != nil {
		t.Fatalf("a symlink to the store: %v", err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}

	// 2. store -> inside a leftover directory: deleting the leftover is refused.
	if err := os.RemoveAll(store); err != nil {
		t.Fatal(err)
	}
	leftover := filepath.Join(root, ".leftover")
	inner := filepath.Join(leftover, "profiles")
	if err := envset.Write(inner, &envset.Set{Name: "glm", Set: map[string]string{"A": "1"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(inner, store); err != nil {
		t.Fatal(err)
	}
	err := doDelete(deleteOpts{yes: true}, []string{".leftover"})
	if err == nil || !strings.Contains(err.Error(), "contains the registry's env set store") {
		t.Fatalf("deleting the directory the store lives in: %v", err)
	}
	if p, err := envset.Read(store, "glm"); err != nil || p == nil {
		t.Fatalf("store damaged: %v %v", p, err)
	}
	// The message names the canonical store, whatever spelling was typed.
	if err := doDelete(deleteOpts{yes: true}, []string{envset.DirName}); err == nil || !strings.Contains(err.Error(), `".env-sets" is the registry's env set store`) {
		t.Fatalf("message: %v", err)
	}
}

// Identity, not spelling: the store's registry entry can be a symlink, the
// typed name a case variant, the playbooks root relative. None of these may
// reach the store.
func TestDeleteStoreGuardByIdentity(t *testing.T) {
	sandboxRoot(t, "playbooks")
	root := config.PlaybooksDir
	store := envset.Dir(root)
	leftover := filepath.Join(root, ".leftover")
	inner := filepath.Join(leftover, "profiles")
	if err := envset.Write(inner, &envset.Set{Name: "glm", Set: map[string]string{"A": "1"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(inner, store); err != nil {
		t.Fatal(err)
	}
	caseInsensitive := false
	if _, err := os.Stat(filepath.Join(root, ".LEFTOVER")); err == nil {
		caseInsensitive = true
	}

	// The store's registry SYMLINK addressed by a case variant: refused, link intact.
	if caseInsensitive {
		if err := doDelete(deleteOpts{yes: true}, []string{".ENV-SETS"}); err == nil || !strings.Contains(err.Error(), `".env-sets" is the registry's env set store`) {
			t.Fatalf("case variant of the store link must get the store message, not the intermediate-link one: %v", err)
		}
		if _, err := os.Lstat(store); err != nil {
			t.Fatal("store link removed")
		}
		// The directory the store resolves into, addressed by a case variant.
		if err := doDelete(deleteOpts{yes: true}, []string{".LEFTOVER"}); err == nil || !strings.Contains(err.Error(), "contains the registry's env set store") {
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
	if err := refuseRegistryOwned(rel, ".leftover", filepath.Join(rel, ".leftover")); err == nil || !strings.Contains(err.Error(), "contains the registry's env set store") {
		t.Fatalf("relative playbooks root: %v", err)
	}
	if p, err := envset.Read(store, "glm"); err != nil || p == nil {
		t.Fatalf("store damaged: %v %v", p, err)
	}
}

// The store is protected along its whole symlink chain: an intermediate link
// and the directory holding it are refused; an unrelated link is not.
func TestDeleteStoreGuardCoversTheResolutionChain(t *testing.T) {
	sandboxRoot(t, "playbooks")
	root := config.PlaybooksDir
	store := envset.Dir(root)
	holder := filepath.Join(root, ".holder")
	final := filepath.Join(t.TempDir(), "profiles")
	if err := envset.Write(final, &envset.Set{Name: "glm", Set: map[string]string{"A": "1"}}); err != nil {
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
	if err := doDelete(deleteOpts{yes: true}, []string{".holder"}); err == nil || !strings.Contains(err.Error(), "or a path it resolves through") {
		t.Fatalf("directory holding an intermediate link: %v", err)
	}
	if err := refuseRegistryOwned(root, "bridge", bridge); err == nil || !strings.Contains(err.Error(), "resolves through") {
		t.Fatalf("intermediate link itself: %v", err)
	}
	unrelated := filepath.Join(root, ".unrelated")
	if err := os.Symlink(final, unrelated); err != nil {
		t.Fatal(err)
	}
	if err := refuseRegistryOwned(root, ".unrelated", unrelated); err != nil {
		t.Fatalf("an unrelated link to the same target is not the store: %v", err)
	}
}

// A symlink in a PARENT component of the store's target is part of the
// resolution too, and a resolution that cannot be established refuses.
func TestDeleteStoreGuardResolvesParentComponents(t *testing.T) {
	sandboxRoot(t, "playbooks")
	root := config.PlaybooksDir
	store := envset.Dir(root)
	leftover := filepath.Join(root, ".leftover")
	if err := envset.Write(filepath.Join(leftover, "sub", "profiles"), &envset.Set{Name: "glm", Set: map[string]string{"A": "1"}}); err != nil {
		t.Fatal(err)
	}
	bridge := filepath.Join(root, ".bridge")
	if err := os.Symlink(filepath.Join(".leftover", "sub"), bridge); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(".bridge", "profiles"), store); err != nil {
		t.Fatal(err)
	}
	if err := doDelete(deleteOpts{yes: true}, []string{".bridge"}); err == nil || !strings.Contains(err.Error(), "resolves through") {
		t.Fatalf("parent-component link: %v", err)
	}
	if err := doDelete(deleteOpts{yes: true}, []string{".leftover"}); err == nil || !strings.Contains(err.Error(), "contains the registry's env set store") {
		t.Fatalf("directory holding the parent-component target: %v", err)
	}
	if p, err := envset.Read(store, "glm"); err != nil || p == nil {
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
	if err := doDelete(deleteOpts{yes: true}, []string{".other"}); err == nil || !strings.Contains(err.Error(), "cannot verify") {
		t.Fatalf("looped store: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("directory removed despite an unverifiable store")
	}
}

// A directory entered and left again through `..` is still required by the
// kernel; a dangling store entry is protected by identity and makes every
// other delete unverifiable.
func TestDeleteStoreGuardTraversedDirsAndDanglingEntry(t *testing.T) {
	sandboxRoot(t, "playbooks")
	root := config.PlaybooksDir
	store := envset.Dir(root)
	if err := os.MkdirAll(filepath.Join(root, ".leftover"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := envset.Write(filepath.Join(root, ".profiles"), &envset.Set{Name: "glm", Set: map[string]string{"A": "1"}}); err != nil {
		t.Fatal(err)
	}
	// Built by hand: filepath.Join would clean the ".." away.
	if err := os.Symlink(".leftover"+string(filepath.Separator)+".."+string(filepath.Separator)+".profiles", store); err != nil {
		t.Fatal(err)
	}
	if err := doDelete(deleteOpts{yes: true}, []string{".leftover"}); err == nil || !strings.Contains(err.Error(), "is a directory the registry's env set store resolves through") {
		t.Fatalf("traversed directory: %v", err)
	}
	if err := doDelete(deleteOpts{yes: true}, []string{".profiles"}); err == nil || !strings.Contains(err.Error(), `".env-sets" is the registry's env set store`) {
		t.Fatalf("physical directory under another name: %v", err)
	}

	// Dangling entry: the link itself stays protected, everything else waits.
	if err := os.Remove(store); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, ".gone"), store); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".ENV-SETS")); err == nil || os.IsNotExist(err) {
		// case-insensitive: Lstat of the variant is the link itself
		if fi, err := os.Lstat(filepath.Join(root, ".ENV-SETS")); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			if err := doDelete(deleteOpts{yes: true}, []string{".ENV-SETS"}); err == nil || !strings.Contains(err.Error(), `".env-sets" is the registry's env set store`) {
				t.Fatalf("dangling store link by case variant: %v", err)
			}
		}
	}
	if err := doDelete(deleteOpts{yes: true}, []string{".leftover"}); err == nil || !strings.Contains(err.Error(), "cannot verify") {
		t.Fatalf("delete with a dangling store: %v", err)
	}
	if _, err := os.Lstat(store); err != nil {
		t.Fatal("dangling store link removed")
	}
}

// A chain the kernel refuses (ELOOP on a long acyclic chain) makes every
// delete unverifiable, even though a component walk alone would accept it.
func TestDeleteStoreGuardDefersToTheKernel(t *testing.T) {
	sandboxRoot(t, "playbooks")
	root := config.PlaybooksDir
	store := envset.Dir(root)
	final := filepath.Join(root, ".final")
	if err := envset.Write(final, &envset.Set{Name: "glm", Set: map[string]string{"A": "1"}}); err != nil {
		t.Fatal(err)
	}
	prev := final
	for i := 0; i < 64; i++ {
		link := filepath.Join(root, fmt.Sprintf(".hop%02d", i))
		if err := os.Symlink(prev, link); err != nil {
			t.Fatal(err)
		}
		prev = link
	}
	if err := os.Symlink(prev, store); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store); err == nil {
		t.Skip("this kernel resolves 65 symlink hops; nothing to defer to")
	}
	other := filepath.Join(root, ".other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := doDelete(deleteOpts{yes: true}, []string{".other"}); err == nil || !strings.Contains(err.Error(), "cannot verify") {
		t.Fatalf("chain the kernel refuses: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("directory removed despite an unverifiable store")
	}
}

// A relative playbooks root resolves from the PHYSICAL working directory:
// with `cd /x/a/link` (`link -> /x/b/sub`) and `--playbooks-dir ..`, the
// store is /x/b/.env-sets, not the /x/a/.env-sets a lexical
// filepath.Abs of the logical $PWD would name.
func TestDeleteStoreGuardRelativeRootFromPhysicalCwd(t *testing.T) {
	sandboxRoot(t, "playbooks")
	x := t.TempDir()
	b := filepath.Join(x, "b")
	sub := filepath.Join(b, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(x, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(x, "a", "link")
	if err := os.Symlink(sub, link); err != nil {
		t.Fatal(err)
	}
	// The real store lives in /x/b, symlinked into a leftover there.
	if err := envset.Write(filepath.Join(b, "foo", "profiles"), &envset.Set{Name: "glm", Set: map[string]string{"A": "1"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(b, "foo", "profiles"), envset.Dir(b)); err != nil {
		t.Fatal(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PWD", link) // the logical cwd a shell would export
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if got, _ := os.Getwd(); got != link {
		t.Skipf("Getwd does not honour $PWD here (%s); the logical/physical split cannot be exercised", got)
	}
	err = refuseRegistryOwned("..", "foo", filepath.Join("..", "foo"))
	if err == nil || !strings.Contains(err.Error(), "contains the registry's env set store") {
		t.Fatalf("relative root under a symlinked cwd: %v", err)
	}
}

// The subtree walk finds a protected identity that is not a path
// descendant of the store's resolution (a bind mount, on Linux). Without a
// mount the mechanism is exercised directly: an inner directory's identity
// is declared protected and the walk must report it; a symlink to a
// protected directory is not a hit (RemoveAll unlinks it, never descends).
func TestSubtreeHoldsFindsProtectedIdentity(t *testing.T) {
	dir := t.TempDir()
	inner := filepath.Join(dir, "a", "b", "mounted")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(inner, filepath.Join(dir, "link-to-inner")); err != nil {
		t.Fatal(err)
	}
	pi, err := os.Lstat(inner)
	if err != nil {
		t.Fatal(err)
	}
	hit, err := subtreeHolds(dir, []os.FileInfo{pi})
	if err != nil || hit != 0 {
		t.Fatalf("hit=%d err=%v, want 0 (%s)", hit, err, inner)
	}
	other := t.TempDir()
	oi, _ := os.Lstat(other)
	if err := os.Symlink(other, filepath.Join(dir, "a", "link-to-other")); err != nil {
		t.Fatal(err)
	}
	hit, err = subtreeHolds(dir, []os.FileInfo{oi})
	if err != nil || hit != -1 {
		t.Fatalf("a symlink to a protected directory is not a hit: hit=%d err=%v", hit, err)
	}
}

// Linux with CAP_SYS_ADMIN only: the real bind-mount shape. Skipped elsewhere.
func TestDeleteStoreGuardBindMount(t *testing.T) {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Skip("needs Linux and root for mount --bind")
	}
	sandboxRoot(t, "playbooks")
	root := config.PlaybooksDir
	data := t.TempDir()
	if err := envset.Write(filepath.Join(data, "profiles"), &envset.Set{Name: "glm", Set: map[string]string{"A": "1"}}); err != nil {
		t.Fatal(err)
	}
	mounted := filepath.Join(root, ".leftover", "mounted")
	if err := os.MkdirAll(mounted, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("mount", "--bind", data, mounted).CombinedOutput(); err != nil {
		t.Skipf("mount --bind: %v %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("umount", mounted).Run() })
	if err := os.Symlink(filepath.Join(data, "profiles"), envset.Dir(root)); err != nil {
		t.Fatal(err)
	}
	if err := doDelete(deleteOpts{yes: true}, []string{".leftover"}); err == nil || !strings.Contains(err.Error(), "contains the registry's env set store") {
		t.Fatalf("bind-mounted store inside the target: %v", err)
	}
}

// With the store resolving to the playbooks root itself, a profile file and
// the default marker are root entries; the orphan path must not remove
// them, while a playbook directory beside them stays deletable.
func TestDeleteStoreEntriesWhenStoreIsTheRoot(t *testing.T) {
	sandboxRoot(t, "playbooks")
	root := config.PlaybooksDir
	if err := envset.Write(root, &envset.Set{Name: "glm", Set: map[string]string{"A": "1"}}); err != nil {
		t.Fatal(err)
	}
	if err := envset.WriteDefaults(root, []string{"glm"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".", envset.Dir(root)); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"glm.toml", envset.DefaultMarker} {
		if err := doDelete(deleteOpts{yes: true}, []string{name}); err == nil || !strings.Contains(err.Error(), "is an entry of the registry's env set store") {
			t.Fatalf("delete %q: %v", name, err)
		}
	}
	if p, err := envset.Read(root, "glm"); err != nil || p == nil {
		t.Fatalf("profile damaged: %v %v", p, err)
	}
	if d, err := envset.Defaults(root); err != nil || strings.Join(d, ",") != "glm" {
		t.Fatalf("default damaged: %q %v", d, err)
	}
	seedFlatPlaybook(t, "beside")
	if err := doDelete(deleteOpts{yes: true}, []string{"beside"}); err != nil {
		t.Fatalf("a playbook beside the store entries must stay deletable: %v", err)
	}
}

// With the store resolving to the root, only the store's own entries are
// refused: a linked playbook and a stray file beside them stay deletable.
func TestDeleteBesideStoreEntriesStaysPossible(t *testing.T) {
	sandboxRoot(t, "playbooks")
	root := config.PlaybooksDir
	if err := envset.Write(root, &envset.Set{Name: "glm", Set: map[string]string{"A": "1"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".", envset.Dir(root)); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	if err := os.Symlink(external, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".stray"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := doDelete(deleteOpts{yes: true}, []string{"linked"}); err != nil {
		t.Fatalf("unlinking a linked playbook beside the store entries: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "linked")); !os.IsNotExist(err) {
		t.Fatal("link not removed")
	}
	if _, err := os.Stat(external); err != nil {
		t.Fatal("link target removed")
	}
	if err := refuseRegistryOwned(root, ".stray", filepath.Join(root, ".stray")); err != nil {
		t.Fatalf("a stray file beside the store entries is not the store: %v", err)
	}
	if err := doDelete(deleteOpts{yes: true}, []string{"glm.toml"}); err == nil {
		t.Fatal("a profile file was deletable")
	}
}

// Both the name launcher and the alias launcher of the deleted playbook go;
// a hand-made link named for it goes too (it would only fail loudly as
// stale afterwards).
func TestDeleteRemovesNameAliasAndHandMadeLaunchers(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CPB_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	writePlaybook(t, root, "mine", &manifest.Manifest{Launcher: "minecmd"})
	if _, err := launcher.Write(config.LauncherDir, "minecmd"); err != nil {
		t.Fatal(err)
	}
	// hand-made: a symlink to the binary, never recorded
	exe, err := launcher.BinPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(exe, filepath.Join(config.LauncherDir, "mine")); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if err := doDelete(deleteOpts{yes: true}, []string{"mine"}); err != nil {
			t.Fatal(err)
		}
	})
	for _, n := range []string{"minecmd", "mine"} {
		if _, exists, _ := launcher.Lookup(config.LauncherDir, n); exists {
			t.Fatalf("launcher %q survived the delete of its playbook", n)
		}
		if !strings.Contains(out, `Removed launcher "`+n+`"`) {
			t.Fatalf("removal not reported for %q:\n%s", n, out)
		}
	}
}

// The confirmation prompt states the launcher's fate, the fate is decided
// once for prompt and action, and a discovery failure keeps the launcher.
func TestLauncherFateIsConsistentAndSafe(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CPB_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))

	writePlaybook(t, root, "own", nil)
	if _, err := launcher.Write(config.LauncherDir, "own"); err != nil {
		t.Fatal(err)
	}
	feedStdin(t, "n\n")
	out := captureStdout(t, func() {
		if err := doDelete(deleteOpts{}, []string{"own"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "Launcher: own (launcher will be removed)") {
		t.Fatalf("prompt does not predict the removal:\n%s", out)
	}
	if _, exists, _ := launcher.Lookup(config.LauncherDir, "own"); !exists {
		t.Fatal("declined delete removed the launcher")
	}

	// A sibling whose manifest cannot be read is contained: the playbook
	// and its launcher go, and the sibling's own delete is its read error.
	writePlaybook(t, root, "victim", nil)
	if _, err := launcher.Write(config.LauncherDir, "victim"); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(root, "broken")
	if err := os.MkdirAll(broken, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, manifest.FileName), []byte("name = [\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	captureStdout(t, func() {
		if err := doDelete(deleteOpts{yes: true}, []string{"victim"}); err != nil {
			t.Fatalf("delete beside an unreadable sibling: %v", err)
		}
	})
	if _, exists, _ := launcher.Lookup(config.LauncherDir, "victim"); exists {
		t.Fatal("the launcher of a deleted playbook was kept over an unreadable sibling")
	}
	if err := doDelete(deleteOpts{yes: true}, []string{"broken"}); err == nil || !strings.Contains(err.Error(), "broken/.playbook") {
		t.Fatalf("deleting the unreadable playbook: %v", err)
	}
	if _, err := os.Stat(broken); err != nil {
		t.Fatalf("the unreadable playbook was removed: %v", err)
	}
	if err := os.RemoveAll(broken); err != nil {
		t.Fatal(err)
	}
}

// A retained alias launcher keeps working across a rename, and a later
// delete of the renamed playbook removes it.
func TestRenameKeepsAliasLauncherAndDeleteRemovesIt(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CPB_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	writePlaybook(t, root, "old", &manifest.Manifest{Launcher: "oa"})
	if _, err := launcher.Write(config.LauncherDir, "oa"); err != nil {
		t.Fatal(err)
	}
	if err := doRename(renameOpts{}, []string{"old", "new"}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if _, exists, _ := launcher.Lookup(config.LauncherDir, "oa"); !exists {
		t.Fatal("alias launcher lost across the rename")
	}
	out := captureStdout(t, func() {
		if err := doDelete(deleteOpts{yes: true}, []string{"new"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, `Removed launcher "oa"`) {
		t.Fatalf("renamed playbook's alias launcher kept:\n%s", out)
	}
}

// A rename whose manifest alias equals the old name (a link's default)
// leaves a working command for the new name and none for the old; the
// prompt's Alias line promises nothing about launchers.
func TestRenameAliasEqualToOldNameAndPrompt(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CPB_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	writePlaybook(t, root, "old", &manifest.Manifest{Launcher: "old"})
	if _, err := launcher.Write(config.LauncherDir, "old"); err != nil {
		t.Fatal(err)
	}
	if err := doRename(renameOpts{}, []string{"old", "new"}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if _, exists, _ := launcher.Lookup(config.LauncherDir, "old"); exists {
		t.Fatal("stale launcher for the old name kept")
	}
	if _, exists, _ := launcher.Lookup(config.LauncherDir, "new"); !exists {
		t.Fatal("no launcher for the new name")
	}
	feedStdin(t, "n\n")
	out := captureStdout(t, func() {
		if err := doDelete(deleteOpts{}, []string{"new"}); err != nil {
			t.Fatal(err)
		}
	})
	if strings.Contains(out, "its launcher will be removed") {
		t.Fatalf("Alias line still promises removal:\n%s", out)
	}
}

// On a case-insensitive filesystem two playbooks can address one directory
// entry under differently-cased names; deleting one must not take the
// other's command with it.
func TestDeleteKeepsCaseFoldedLauncherAnotherPlaybookClaims(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CPB_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	writePlaybook(t, root, "one", &manifest.Manifest{Launcher: "Foo"})
	writePlaybook(t, root, "two", &manifest.Manifest{Launcher: "foo"})
	if _, err := launcher.Write(config.LauncherDir, "Foo"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(config.LauncherDir, "foo")); err != nil {
		t.Skip("case-sensitive filesystem: the names are distinct entries")
	}
	if _, err := launcher.Write(config.LauncherDir, "foo"); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if err := doDelete(deleteOpts{yes: true}, []string{"two"}); err != nil {
			t.Fatal(err)
		}
	})
	if _, exists, _ := launcher.Lookup(config.LauncherDir, "Foo"); !exists {
		t.Fatal("deleting two removed the entry one addresses as Foo")
	}
	if !strings.Contains(out, `still addresses playbook "one"`) {
		t.Fatalf("claim by case-folded name not reported:\n%s", out)
	}
}

// A playbook named like the CLI's reserved command must not have the CLI's
// own symlink reported, or removed, as its launcher.
func TestDeleteNeverTouchesTheReservedCLILauncher(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CPB_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	exe, err := launcher.BinPath()
	if err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(config.LauncherDir, "cpb")
	if err := os.Symlink(exe, cli); err != nil {
		t.Fatal(err)
	}
	writePlaybook(t, root, "cpb", nil)
	feedStdin(t, "n\n")
	out := captureStdout(t, func() {
		if err := doDelete(deleteOpts{}, []string{"cpb"}); err != nil {
			t.Fatal(err)
		}
	})
	if strings.Contains(out, "Launcher: cpb") {
		t.Fatalf("reserved symlink presented as the playbook's launcher:\n%s", out)
	}
	out = captureStdout(t, func() {
		if err := doDelete(deleteOpts{yes: true}, []string{"cpb"}); err != nil {
			t.Fatal(err)
		}
	})
	if strings.Contains(out, `Removed launcher "cpb"`) {
		t.Fatalf("removal of the reserved symlink reported:\n%s", out)
	}
	if _, err := os.Lstat(cli); err != nil {
		t.Fatal("the CLI's own symlink was removed")
	}
}

// A playbook named like the reserved command under another spelling must
// not take the CLI's own symlink with it on a case-insensitive filesystem.
func TestDeleteSparesReservedLauncherUnderCaseVariant(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CPB_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	exe, err := launcher.BinPath()
	if err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(config.LauncherDir, "cpb")
	if err := os.Symlink(exe, cli); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(config.LauncherDir, "CPB")); err != nil {
		t.Skip("case-sensitive filesystem: CPB is a distinct entry")
	}
	writePlaybook(t, root, "CPB", nil)
	out := captureStdout(t, func() {
		if err := doDelete(deleteOpts{yes: true}, []string{"CPB"}); err != nil {
			t.Fatal(err)
		}
	})
	if strings.Contains(out, "Removed command") {
		t.Fatalf("reserved symlink reported removed:\n%s", out)
	}
	if _, err := os.Lstat(cli); err != nil {
		t.Fatal("the CLI's own symlink was removed through a case variant")
	}
}
