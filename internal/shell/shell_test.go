package shell

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteAndReadAliasWithSpacesInPath(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), ".zshrc")
	playbookDir := filepath.Join(t.TempDir(), "playbooks with spaces", "pb")

	if err := Write(configFile, "spacepath", playbookDir); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatal(err)
	}
	binName := "claude-playbook"
	if len(os.Args) > 0 {
		baseBin := filepath.Base(os.Args[0])
		if baseBin != "." && baseBin != "/" {
			binName = baseBin
		}
	}
	expected := "# claude-playbook: pb\nalias spacepath='CLAUDE_CONFIG_DIR=\"" + playbookDir + "\" " + binName + " run pb'\n"
	if got := string(data); got != expected {
		t.Fatalf("alias line = %q, want %q", got, expected)
	}

	entries, err := ReadAll(configFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %#v", entries)
	}
	if entries[0].Path != playbookDir {
		t.Fatalf("path = %q, want %q", entries[0].Path, playbookDir)
	}
}

func TestReadAllParsesLegacyUnquotedAlias(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), ".zshrc")
	if err := os.WriteFile(configFile, []byte("alias old='CLAUDE_CONFIG_DIR=/tmp/pb claude --model test'\n"), 0644); err != nil {
		t.Fatal(err)
	}

	entries, err := ReadAll(configFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Path != "/tmp/pb" {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestRewritePathPrefixPreservesQuotedAliasPath(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), ".zshrc")
	oldRoot := filepath.Join(t.TempDir(), "old root")
	oldConfig := filepath.Join(oldRoot, "config dir")
	newRoot := filepath.Join(t.TempDir(), "new root")
	newConfig := filepath.Join(newRoot, "config dir")
	if err := Write(configFile, "pb", oldConfig); err != nil {
		t.Fatal(err)
	}

	changed, err := RewritePathPrefix(configFile, oldRoot, newRoot)
	if err != nil {
		t.Fatal(err)
	}
	if changed != 1 {
		t.Fatalf("changed = %d, want 1", changed)
	}

	entries, err := ReadAll(configFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Path != newConfig {
		t.Fatalf("entries = %#v, want path %s", entries, newConfig)
	}
}

func TestWriteRejectsInvalidAliasName(t *testing.T) {
	err := Write(filepath.Join(t.TempDir(), ".zshrc"), "bad-name!", "/tmp/pb")
	if err == nil {
		t.Fatal("expected invalid alias name error")
	}
}
