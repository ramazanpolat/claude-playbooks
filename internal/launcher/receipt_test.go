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
