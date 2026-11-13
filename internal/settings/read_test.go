package settings

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// within fails the test when f has not returned in two seconds: a read
// that waits on a FIFO never returns.
func within(t *testing.T, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { f(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("blocked")
	}
}

// A FIFO, a folder or a device where a settings file belongs is refused
// at once, never waited on; a regular file and a link to one are read.
func TestReadFileRefusesWhatIsNotARegularFile(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo.json")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(dir, "folder.json")
	if err := os.Mkdir(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{fifo, folder, os.DevNull} {
		within(t, func() {
			if _, err := ReadFile(p); !errors.Is(err, ErrNotRegular) {
				t.Errorf("%s: %v", p, err)
			}
		})
	}
	file := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(file, []byte(`{"a":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{file, link} {
		if b, err := ReadFile(p); err != nil || string(b) != `{"a":1}` {
			t.Errorf("%s: %q %v", p, b, err)
		}
	}
	if _, err := ReadFile(filepath.Join(dir, "none.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a missing file: %v", err)
	}
	// Load refuses a FIFO settings.json too, at once.
	pb := filepath.Join(dir, "pb")
	if err := os.Mkdir(pb, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(pb, FileName), 0o600); err != nil {
		t.Fatal(err)
	}
	within(t, func() {
		if _, err := Load(pb); !errors.Is(err, ErrNotRegular) {
			t.Errorf("Load: %v", err)
		}
	})
}
