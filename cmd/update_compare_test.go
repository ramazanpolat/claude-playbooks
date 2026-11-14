package cmd

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// writeRel writes body at rel below dir, making its folders.
func writeRel(t *testing.T, dir, rel, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// updateNow updates pb, failing the test on an error.
func updateNow(t *testing.T) {
	t.Helper()
	if err := runPlaybookUpdate(io.Discard, "pb", updateOpts{yes: true}); err != nil {
		t.Fatal(err)
	}
}

// caseInsensitive reports whether dir's filesystem takes names that differ
// only in case for one entry.
func caseInsensitive(t *testing.T, dir string) bool {
	t.Helper()
	probe := filepath.Join(dir, "case-probe")
	if err := os.WriteFile(probe, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(probe)
	_, err := os.Lstat(filepath.Join(dir, "CASE-PROBE"))
	return err == nil
}

// A hard link to a preserved file is a file of its own, which the update
// replaces: the dry run lists it. Under a case variant of the preserved
// name on a filesystem that heeds case too.
func TestUpdateDryRunComparesAHardLinkToAPreservedFile(t *testing.T) {
	installed, _ := migrateFixture(t, false)
	source := filepath.Join(filepath.Dir(config.PlaybooksDir), "source")
	writeRel(t, source, "docs/a.md", "from the source\n")
	writeRel(t, installed, "settings.json", `{"mine":true}`)
	updateNow(t)
	if out := dryRunUpdate(t); !strings.Contains(out, "replaces:  nothing") {
		t.Fatalf("after the update:\n%s", out)
	}
	a := filepath.Join(installed, "docs", "a.md")
	if err := os.Remove(a); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(installed, "settings.json"), a); err != nil {
		t.Fatal(err)
	}
	if out := dryRunUpdate(t); !strings.Contains(out, "replaces:  docs/ (changed)\n") {
		t.Fatalf("docs/a.md a hard link to settings.json:\n%s", out)
	}
	updateNow(t)
	if b, _ := os.ReadFile(a); string(b) != "from the source\n" {
		t.Fatalf("the update did not replace docs/a.md: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(installed, "settings.json")); string(b) != `{"mine":true}` {
		t.Fatalf("the update changed settings.json: %s", b)
	}
	if caseInsensitive(t, installed) {
		return // SETTINGS.JSON is settings.json here: the other test's case
	}
	writeRel(t, source, "SETTINGS.JSON", `{"from":"source"}`)
	if err := os.Link(filepath.Join(installed, "settings.json"), filepath.Join(installed, "SETTINGS.JSON")); err != nil {
		t.Fatal(err)
	}
	if out := dryRunUpdate(t); !strings.Contains(out, "SETTINGS.JSON (changed)") {
		t.Fatalf("a hard link under a case variant, where case counts:\n%s", out)
	}
	updateNow(t)
	if b, _ := os.ReadFile(filepath.Join(installed, "SETTINGS.JSON")); string(b) != `{"from":"source"}` {
		t.Fatalf("the update did not replace SETTINGS.JSON: %s", b)
	}
}

// A preserved path in a folder the source ships is left out of that
// folder's comparison; anything else of the install's own beside it is
// listed, since the update drops it.
func TestUpdateDryRunPreservedPathInAShippedFolder(t *testing.T) {
	installed, _ := migrateFixture(t, false)
	source := filepath.Join(filepath.Dir(config.PlaybooksDir), "source")
	if err := manifest.Write(source, &manifest.Manifest{Version: "2.0.0", Update: &manifest.Update{Preserve: []string{"config/local.json"}}}); err != nil {
		t.Fatal(err)
	}
	writeRel(t, source, "config/app.json", `{"app":1}`)
	writeRel(t, source, "config/local.json", `{"local":"source"}`)
	updateNow(t)
	writeRel(t, installed, "config/local.json", `{"local":"mine"}`)
	if out := dryRunUpdate(t); !strings.Contains(out, "replaces:  nothing") || !strings.Contains(out, "  up to date\n") {
		t.Fatalf("an edited preserved file in a shipped folder:\n%s", out)
	}
	updateNow(t)
	if b, _ := os.ReadFile(filepath.Join(installed, "config", "local.json")); string(b) != `{"local":"mine"}` {
		t.Fatalf("the update did not keep config/local.json: %s", b)
	}
	writeRel(t, installed, "config/extra.json", `{}`)
	if out := dryRunUpdate(t); !strings.Contains(out, "replaces:  config/ (changed)\n") {
		t.Fatalf("an unpreserved file beside it:\n%s", out)
	}
}

// A dry run leaves nothing in the temp folder: the staging folder and the
// scratch copy are both removed.
func TestUpdateDryRunLeavesNothingInTheTempFolder(t *testing.T) {
	migrateFixture(t, false)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	source := filepath.Join(filepath.Dir(config.PlaybooksDir), "source")
	writeRel(t, source, "docs/a.md", "a\n")
	updateNow(t)
	for i := 0; i < 2; i++ {
		dryRunUpdate(t)
		ents, err := os.ReadDir(tmp)
		if err != nil {
			t.Fatal(err)
		}
		if len(ents) != 0 {
			t.Fatalf("left in the temp folder: %v", ents)
		}
	}
}

// A link out of the source is copied as a link, so it is compared by its
// target: retargeted, it is listed.
func TestUpdateDryRunComparesALinkOutOfTheSourceByItsTarget(t *testing.T) {
	installed, _ := migrateFixture(t, false)
	source := filepath.Join(filepath.Dir(config.PlaybooksDir), "source")
	outside := t.TempDir()
	writeRel(t, outside, "a.md", "same\n")
	writeRel(t, outside, "b.md", "same\n")
	link := filepath.Join(source, "ext.md")
	if err := os.Symlink(filepath.Join(outside, "a.md"), link); err != nil {
		t.Fatal(err)
	}
	updateNow(t)
	if got, err := os.Readlink(filepath.Join(installed, "ext.md")); err != nil || got != filepath.Join(outside, "a.md") {
		t.Fatalf("the update did not copy the link: %q %v", got, err)
	}
	if out := dryRunUpdate(t); !strings.Contains(out, "replaces:  nothing") {
		t.Fatalf("after the update:\n%s", out)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "b.md"), link); err != nil {
		t.Fatal(err)
	}
	if out := dryRunUpdate(t); !strings.Contains(out, "replaces:  ext.md (changed)\n") {
		t.Fatalf("a retargeted link to the same bytes:\n%s", out)
	}
}

// openNoFollow keeps only what it was asked for, a file or a folder,
// reached without a link at its last step, and never waits.
func TestOpenNoFollow(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	writeRel(t, dir, "file", "x")
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, tc := range []struct {
			path string
			dir  bool
			ok   bool
		}{
			{file, false, true}, {dir, true, true},
			{fifo, false, false}, {fifo, true, false},
			{dir, false, false}, {file, true, false},
			{link, false, false}, {link, true, false},
		} {
			f, err := openNoFollow(tc.path, tc.dir)
			if f != nil {
				f.Close()
			}
			if tc.ok && err != nil || !tc.ok && !errors.Is(err, errNotComparable) {
				t.Errorf("%s (folder %v): %v", filepath.Base(tc.path), tc.dir, err)
			}
		}
		if _, err := readRegular(fifo, -1); !errors.Is(err, errNotComparable) {
			t.Errorf("readRegular on a FIFO: %v", err)
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("waited on a FIFO")
	}
}

// Two FIFOs are never the same: neither is opened, so nothing says they
// would be.
func TestSameTreeFIFOs(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	for _, d := range []string{a, b} {
		if err := syscall.Mkfifo(filepath.Join(d, "p"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c := newUpdateCompare(b, nil, nil, false)
	if same, err := c.sameTree(filepath.Join(a, "p"), filepath.Join(b, "p"), "p"); same || err != nil {
		t.Fatalf("two FIFOs: %v %v", same, err)
	}
}

// gitHead refuses a ref name out of its pattern, and a loose ref that is a
// link or a FIFO is "cannot tell", never a reason to read packed-refs.
func TestGitHeadRefusals(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef01234567"
	gitDir := func(files map[string]string) string {
		t.Helper()
		dir := filepath.Join(t.TempDir(), ".git")
		for rel, body := range files {
			writeRel(t, dir, rel, body)
		}
		return dir
	}
	if got := gitHead(gitDir(map[string]string{"HEAD": "ref: refs/heads/mäin\n", "refs/heads/mäin": id + "\n"})); got != "" {
		t.Errorf("a ref name out of the pattern: %q", got)
	}
	packed := map[string]string{"HEAD": "ref: refs/heads/main\n", "packed-refs": id + " refs/heads/main\n", "elsewhere": id + "\n"}
	dir := gitDir(packed)
	if got := gitHead(dir); got != id {
		t.Fatalf("packed-refs alone: %q", got)
	}
	ref := filepath.Join(dir, "refs", "heads", "main")
	if err := os.MkdirAll(filepath.Dir(ref), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "elsewhere"), ref); err != nil {
		t.Fatal(err)
	}
	if got := gitHead(dir); got != "" {
		t.Errorf("a loose ref that is a link: %q", got)
	}
	dir = gitDir(packed)
	ref = filepath.Join(dir, "refs", "heads", "main")
	if err := os.MkdirAll(filepath.Dir(ref), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(ref, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := gitHead(dir); got != "" {
		t.Errorf("a loose ref that is a FIFO: %q", got)
	}
}

// On a filesystem that ignores case, an install whose preserved
// settings.json is spelled Settings.json, as the source ships it (an
// install made from such a source, before its first update), keeps its
// own: the dry run does not list it, and the update keeps its bytes.
func TestUpdateDryRunPreservedFileSpelledAsTheSourceShipsIt(t *testing.T) {
	installed, _ := migrateFixture(t, false)
	if !caseInsensitive(t, installed) {
		t.Skip("a filesystem that heeds case: Settings.json is another file")
	}
	source := filepath.Join(filepath.Dir(config.PlaybooksDir), "source")
	writeRel(t, source, "Settings.json", `{"from":"source"}`)
	writeRel(t, installed, "Settings.json", `{"mine":true}`)
	if out := dryRunUpdate(t); strings.Contains(out, "Settings.json") {
		t.Fatalf("the install's own Settings.json listed:\n%s", out)
	}
	updateNow(t)
	if b, _ := os.ReadFile(filepath.Join(installed, "settings.json")); string(b) != `{"mine":true}` {
		t.Fatalf("the update did not keep the install's own: %s", b)
	}
	if out := dryRunUpdate(t); !strings.Contains(out, "replaces:  nothing") {
		t.Fatalf("after the update:\n%s", out)
	}
}
