package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadRejectsEscapingSourcePaths(t *testing.T) {
	for _, field := range []string{"subdir"} {
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
	err := Write(t.TempDir(), &Manifest{Source: &Source{Subdir: "/tmp/outside"}})
	if err == nil {
		t.Fatal("expected Write to validate source.subdir")
	}
}

func TestReadRejectsEscapingPreservePath(t *testing.T) {
	dir := t.TempDir()
	content := "version = \"0.1.0\"\n[update]\npreserve = [\"../outside\"]\n"
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(dir); err == nil {
		t.Fatal("expected update.preserve traversal to be rejected")
	}
}

func TestPreserveRoundTrips(t *testing.T) {
	dir := t.TempDir()
	if err := Write(dir, &Manifest{Update: &Update{Preserve: []string{"settings.json", "local/notes.md"}}}); err != nil {
		t.Fatal(err)
	}
	m, err := Read(dir)
	if err != nil || m == nil || m.Update == nil {
		t.Fatalf("m=%#v err=%v", m, err)
	}
	if len(m.Update.Preserve) != 2 || m.Update.Preserve[0] != "settings.json" || m.Update.Preserve[1] != "local/notes.md" {
		t.Fatalf("preserve=%#v", m.Update.Preserve)
	}
}
