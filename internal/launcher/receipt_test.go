package launcher

import (
	"os"
	"path/filepath"
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

	path, err := Write(dir, "recorded", "", "")
	if err != nil {
		t.Fatal(err)
	}
	got := Recorded()
	if len(got) != 1 || got[0] != path {
		t.Fatalf("after Write, receipt = %v, want [%s]", got, path)
	}

	// Re-writing the identical link must not duplicate the entry.
	if _, err := Write(dir, "recorded", "", ""); err != nil {
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
	if err := record("/x/y", "", ""); err != nil {
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

// Attribution round-trips, is replaced on re-record, and is absent for a
// path-only (pre-v3.10.1 or unattributed) line, which Recorded still lists.
func TestReceiptAttribution(t *testing.T) {
	t.Setenv("CLAUDE_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	if err := record("/l/a", "/root", "pb"); err != nil {
		t.Fatal(err)
	}
	if err := record("/l/b", "", ""); err != nil {
		t.Fatal(err)
	}
	if r, p, ok := Attribution("/l/a"); !ok || r != "/root" || p != "pb" {
		t.Fatalf("attribution of /l/a = %q %q %v", r, p, ok)
	}
	if _, _, ok := Attribution("/l/b"); ok {
		t.Fatal("unattributed line claimed an owner")
	}
	if _, _, ok := Attribution("/l/none"); ok {
		t.Fatal("unknown path claimed an owner")
	}
	if got := Recorded(); len(got) != 2 || got[0] != "/l/a" || got[1] != "/l/b" {
		t.Fatalf("Recorded = %v", got)
	}
	// re-record replaces the attribution in place, once
	if err := record("/l/a", "/root2", "pb2"); err != nil {
		t.Fatal(err)
	}
	if r, p, _ := Attribution("/l/a"); r != "/root2" || p != "pb2" {
		t.Fatalf("attribution not replaced: %q %q", r, p)
	}
	if got := Recorded(); len(got) != 2 {
		t.Fatalf("duplicate line after re-record: %v", got)
	}
	// a hand-edited old-format file is tolerated
	if err := os.WriteFile(ReceiptPath(), []byte("/old/one\n/l/a\t/root\tpb\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Recorded(); len(got) != 2 || got[0] != "/old/one" {
		t.Fatalf("old format: %v", got)
	}
	if _, _, ok := Attribution("/old/one"); ok {
		t.Fatal("old-format line claimed an owner")
	}
	if err := unrecord("/l/a"); err != nil {
		t.Fatal(err)
	}
	if got := Recorded(); len(got) != 1 {
		t.Fatalf("unrecord by path: %v", got)
	}
}

// Field and line separators never enter the receipt: a name with a tab is
// not a valid launcher name, a root with one leaves the line unattributed,
// a path with one is refused outright.
func TestReceiptRefusesSeparators(t *testing.T) {
	t.Setenv("CLAUDE_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	if err := ValidateName("a\tb"); err == nil {
		t.Fatal("tab accepted in a command name")
	}
	if err := record("/l/tab\tpath", "/root", "pb"); err == nil {
		t.Fatal("path with a tab recorded")
	}
	if got := Recorded(); len(got) != 0 {
		t.Fatalf("something recorded: %v", got)
	}
	if err := record("/l/ok", "/ro\tot", "pb"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := Attribution("/l/ok"); ok {
		t.Fatal("root with a tab attributed")
	}
	if got := Recorded(); len(got) != 1 || got[0] != "/l/ok" {
		t.Fatalf("Recorded = %v", got)
	}
}

// Launcher paths are matched in a normalized form (absolute, directory
// resolved), and attributed fields read back verbatim, trailing space
// included.
func TestReceiptNormalizesPathsAndKeepsFieldsVerbatim(t *testing.T) {
	t.Setenv("CLAUDE_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	real := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "bin-link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := record(filepath.Join(link, "cmd"), "/root", "pb "); err != nil {
		t.Fatal(err)
	}
	if r, p, ok := Attribution(filepath.Join(real, "cmd")); !ok || r != "/root" || p != "pb " {
		t.Fatalf("attribution through the resolved dir = %q %q %v", r, p, ok)
	}
	wd, _ := os.Getwd()
	if err := os.Chdir(real); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if _, _, ok := Attribution("cmd"); !ok {
		t.Fatal("relative launcher path not matched")
	}
	if err := unrecord("./cmd"); err != nil {
		t.Fatal(err)
	}
	if got := Recorded(); len(got) != 0 {
		t.Fatalf("unrecord by relative path missed: %v", got)
	}
}
