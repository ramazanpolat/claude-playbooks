package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadRejectsEscapingSourcePaths(t *testing.T) {
	for _, field := range []string{"subdir", "update_script"} {
		t.Run(field, func(t *testing.T) {
			dir := t.TempDir()
			content := "version = \"0.1.0\"\n[source]\n" + field + " = \"../outside\"\n"
			if err := os.WriteFile(filepath.Join(dir, FileName), []byte(content), 0644); err != nil {
				t.Fatal(err)
			}
			if _, err := Read(dir); err == nil {
				t.Fatalf("expected %s traversal to be rejected", field)
			}
		})
	}
}

func TestReadAllowsDotDotPrefixInOrdinaryName(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("subdir = \"..config\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(dir); err != nil {
		t.Fatalf("safe path was rejected: %v", err)
	}
}

func TestResolveSubdirRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()
	if err := os.Symlink(external, filepath.Join(root, "config")); err != nil {
		t.Fatal(err)
	}
	_, err := ResolveSubdir(root, "subdir", "config")
	if err == nil || !strings.Contains(err.Error(), "resolves outside") {
		t.Fatalf("expected symlink escape error, got %v", err)
	}
}

func TestWriteValidatesSourcePaths(t *testing.T) {
	err := Write(t.TempDir(), &Manifest{Source: &Source{UpdateScript: "/tmp/outside"}})
	if err == nil {
		t.Fatal("expected Write to validate source.update_script")
	}
}
