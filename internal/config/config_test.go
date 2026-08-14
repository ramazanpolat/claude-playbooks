package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveLauncherDirHasNoSideEffects(t *testing.T) {
	// Resolution must only compute paths; creating the fallback directory
	// belongs to the write path, or read-only callers (list, --dry-run)
	// would mutate the filesystem.
	target := filepath.Join(t.TempDir(), "does", "not", "exist")
	t.Setenv("CLAUDE_LAUNCHER_DIR", target)
	got, err := ResolveLauncherDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != target {
		t.Errorf("got %s, want %s", got, target)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("resolution created the directory: %v", err)
	}
}
