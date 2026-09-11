package launcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// All receipt tests (and every launcher test that calls Write/Remove) run
// against an isolated receipt via CLAUDE_LAUNCHER_RECEIPT, so nothing
// touches the real ~/.local/state.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "launcher-receipt-*")
	if err != nil {
		panic(err)
	}
	os.Setenv("CLAUDE_LAUNCHER_RECEIPT", filepath.Join(dir, "launchers"))
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func TestWriteRecordsAndRemoveUnrecords(t *testing.T) {
	t.Setenv("CLAUDE_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	dir := t.TempDir()

	path, err := Write(dir, "recorded")
	if err != nil {
		t.Fatal(err)
	}
	got := Recorded()
	if len(got) != 1 || got[0] != path {
		t.Fatalf("after Write, receipt = %v, want [%s]", got, path)
	}

	// Re-writing the identical link must not duplicate the entry.
	if _, err := Write(dir, "recorded"); err != nil {
		t.Fatal(err)
	}
	if got := Recorded(); len(got) != 1 {
		t.Fatalf("duplicate entry after refresh: %v", got)
	}

	ok, err := Remove(dir, "recorded")
	if err != nil || !ok {
		t.Fatalf("Remove: ok=%v err=%v", ok, err)
	}
	if got := Recorded(); len(got) != 0 {
		t.Fatalf("after Remove, receipt = %v, want empty", got)
	}
	// An emptied receipt file is deleted outright.
	if _, err := os.Stat(ReceiptPath()); !os.IsNotExist(err) {
		t.Fatalf("empty receipt file left behind: %v", err)
	}
}

func TestRecordedSkipsBlanksAndDuplicates(t *testing.T) {
	rp := filepath.Join(t.TempDir(), "launchers")
	t.Setenv("CLAUDE_LAUNCHER_RECEIPT", rp)
	if err := os.WriteFile(rp, []byte("/a/b\n\n/a/b\n  /c/d\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := Recorded()
	if len(got) != 2 || got[0] != "/a/b" || got[1] != "/c/d" {
		t.Fatalf("Recorded() = %v", got)
	}
}

func TestRemoveReceiptCleansStateDir(t *testing.T) {
	dir := t.TempDir()
	rp := filepath.Join(dir, "state", "launchers")
	t.Setenv("CLAUDE_LAUNCHER_RECEIPT", rp)
	if err := record("/x/y"); err != nil {
		t.Fatal(err)
	}
	RemoveReceipt()
	if _, err := os.Stat(rp); !os.IsNotExist(err) {
		t.Fatal("receipt file survives RemoveReceipt")
	}
	if _, err := os.Stat(filepath.Dir(rp)); !os.IsNotExist(err) {
		t.Fatal("empty state dir survives RemoveReceipt")
	}
}

// Line separators never enter the receipt: a name with a tab is not a
// valid launcher name, a path with one is refused outright, even when the
// tab arrives through the working directory.
func TestReceiptRefusesSeparators(t *testing.T) {
	t.Setenv("CLAUDE_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	if err := ValidateName("a\tb"); err == nil {
		t.Fatal("tab accepted in a command name")
	}
	if err := record("/l/tab\tpath"); err == nil {
		t.Fatal("path with a tab recorded")
	}
	if got := Recorded(); len(got) != 0 {
		t.Fatalf("something recorded: %v", got)
	}
	tabbed := filepath.Join(t.TempDir(), "tab\tdir")
	if err := os.MkdirAll(tabbed, 0o755); err == nil {
		wd, _ := os.Getwd()
		if err := os.Chdir(tabbed); err == nil {
			t.Cleanup(func() { _ = os.Chdir(wd) })
			if err := record("rel"); err == nil {
				t.Fatal("relative path under a tabbed working directory recorded")
			}
			_ = os.Chdir(wd)
		}
	}
}

// Launcher paths are matched in a normalized form (absolute, directory
// resolved), persisted absolute, and a legacy attributed line (v3.10.1) is
// still read by its path.
func TestReceiptNormalizesPathsAndReadsLegacyLines(t *testing.T) {
	t.Setenv("CLAUDE_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	real := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "bin-link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := record(filepath.Join(link, "cmd")); err != nil {
		t.Fatal(err)
	}
	if err := record(filepath.Join(real, "cmd")); err != nil {
		t.Fatal(err)
	}
	if got := Recorded(); len(got) != 1 {
		t.Fatalf("the same launcher through two spellings recorded twice: %v", got)
	}
	wd, _ := os.Getwd()
	if err := os.Chdir(real); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if err := unrecord("./cmd"); err != nil {
		t.Fatal(err)
	}
	if got := Recorded(); len(got) != 0 {
		t.Fatalf("unrecord by relative path missed: %v", got)
	}
	if err := record("./rel"); err != nil {
		t.Fatal(err)
	}
	if got := Recorded(); len(got) != 1 || !filepath.IsAbs(got[0]) || filepath.Base(got[0]) != "rel" {
		t.Fatalf("relative path persisted as %v", got)
	}
	// a v3.10.1 line with attribution fields is read by its path and rewritten path-only
	if err := os.WriteFile(ReceiptPath(), []byte("/old/one\t/root\tpb\n/old/two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Recorded(); len(got) != 2 || got[0] != "/old/one" || got[1] != "/old/two" {
		t.Fatalf("legacy lines: %v", got)
	}
	if err := record("/old/one"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(ReceiptPath())
	if strings.Contains(string(data), "\t") {
		t.Fatalf("legacy attribution survived a re-record:\n%s", data)
	}
}

// A launcher directory spelled with different case on a case-insensitive
// filesystem is the same directory: record and unrecord must agree.
func TestReceiptMatchesCaseVariantDirectory(t *testing.T) {
	t.Setenv("CLAUDE_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	base := t.TempDir()
	lower := filepath.Join(base, "bin")
	upper := filepath.Join(base, "BIN")
	if err := os.MkdirAll(lower, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(upper); err != nil {
		t.Skip("case-sensitive filesystem: nothing to reconcile")
	}
	link := filepath.Join(lower, "cmd")
	if err := os.Symlink("/nonexistent/target", link); err != nil {
		t.Fatal(err)
	}
	if err := record(link); err != nil {
		t.Fatal(err)
	}
	if err := record(filepath.Join(upper, "cmd")); err != nil {
		t.Fatal(err)
	}
	if got := Recorded(); len(got) != 1 {
		t.Fatalf("re-record through a case variant duplicated the line: %v", got)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := unrecord(filepath.Join(upper, "cmd")); err != nil {
		t.Fatal(err)
	}
	if got := Recorded(); len(got) != 0 {
		t.Fatalf("unrecord through a case variant after removal missed: %v", got)
	}
}

// Whitespace never enters a command name, and a receipt line keeps a path
// exactly (leading indentation aside).
func TestNamesRejectWhitespaceAndLinesKeepPaths(t *testing.T) {
	for _, bad := range []string{"demo ", " demo", "de mo"} {
		if err := ValidateName(bad); err == nil {
			t.Fatalf("whitespace accepted in command name %q", bad)
		}
	}
	t.Setenv("CLAUDE_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	if err := os.WriteFile(ReceiptPath(), []byte("  /a/b \n/c/d\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Recorded(); len(got) != 2 || got[0] != "/a/b " || got[1] != "/c/d" {
		t.Fatalf("Recorded = %q", got)
	}
}

// Removing one spelling of a case-folded launcher clears every receipt
// line for that entry, and the CLI's reserved symlink is never a
// candidate under any spelling.
func TestRemoveClearsCaseVariantLinesAndSparesReserved(t *testing.T) {
	t.Setenv("CLAUDE_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	dir := t.TempDir()
	if _, err := Write(dir, "Foo"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "foo")); err != nil {
		t.Skip("case-sensitive filesystem: nothing folds")
	}
	if _, err := Write(dir, "foo"); err != nil {
		t.Fatal(err)
	}
	if got := Recorded(); len(got) != 1 {
		t.Fatalf("one entry recorded twice: %v", got)
	}
	if ok, err := Remove(dir, "Foo"); err != nil || !ok {
		t.Fatalf("remove Foo: %v %v", ok, err)
	}
	if got := Recorded(); len(got) != 0 {
		t.Fatalf("receipt kept a line for the removed entry: %v", got)
	}
	// reserved under another spelling
	if _, err := Write(dir, "helper"); err != nil {
		t.Fatal(err)
	}
	bin, _ := BinPath()
	if err := os.Symlink(bin, filepath.Join(dir, "cpb")); err != nil {
		t.Fatal(err)
	}
	if !IsReservedEntry(dir, "CPB") {
		t.Fatal("CPB not recognised as the reserved cpb entry")
	}
	if ok, _ := Remove(dir, "CPB"); ok {
		t.Fatal("removed the reserved symlink through a case variant")
	}
	if _, err := os.Lstat(filepath.Join(dir, "cpb")); err != nil {
		t.Fatal("reserved symlink gone")
	}
}
