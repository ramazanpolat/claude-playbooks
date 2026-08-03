package shell

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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
	expected := "# claude-playbook: pb\n" + Format("spacepath", playbookDir) + "\n"
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

	changed, skipped, err := RewritePathPrefix(configFile, oldRoot, newRoot)
	if err != nil {
		t.Fatal(err)
	}
	if changed != 1 || len(skipped) != 0 {
		t.Fatalf("changed = %d, skipped = %v, want 1 and none", changed, skipped)
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

func TestAliasQuotesPlaybookNameAsOneArgument(t *testing.T) {
	root := t.TempDir()
	configFile := filepath.Join(root, "shellrc")
	playbookDir := filepath.Join(root, "victim; touch PWNED")
	if err := Write(configFile, "safe", playbookDir); err != nil {
		t.Fatal(err)
	}

	binName := filepath.Base(os.Args[0])
	fakeBin := filepath.Join(root, binName)
	if err := os.WriteFile(fakeBin, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$RECORD\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "invoke.sh")
	content := "source " + shellQuote(configFile) + "\nsafe\n"
	if err := os.WriteFile(script, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(root, "args")
	c := exec.Command("bash", "-O", "expand_aliases", script)
	c.Dir = root
	c.Env = append(os.Environ(), "PATH="+root+":"+os.Getenv("PATH"), "RECORD="+record)
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("alias execution failed: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(root, "PWNED")); !os.IsNotExist(err) {
		t.Fatalf("playbook name executed shell syntax, err=%v", err)
	}
	args, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(args), "run\nvictim; touch PWNED\n"; got != want {
		t.Fatalf("arguments = %q, want %q", got, want)
	}
}

func TestConcurrentWritesRetainEveryAlias(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "nested", "config.fish")
	const count = 24
	var wg sync.WaitGroup
	errCh := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errCh <- Write(configFile, fmt.Sprintf("pb%d", i), filepath.Join(t.TempDir(), fmt.Sprintf("pb%d", i)))
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	entries, err := ReadAll(configFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != count {
		var names []string
		for _, entry := range entries {
			names = append(names, entry.AliasName)
		}
		t.Fatalf("got %d aliases, want %d: %s", len(entries), count, strings.Join(names, ", "))
	}
}

func TestWritePreservesExistingMode(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "shellrc")
	if err := os.WriteFile(configFile, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Write(configFile, "pb", "/tmp/pb"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(configFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("mode = %o, want 600", got)
	}
}

func TestWritePreservesShellConfigSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "dotfiles", "zshrc")
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("# existing\n"), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, ".zshrc")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := Write(link, "pb", "/tmp/pb"); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("shell config symlink was replaced: info=%v err=%v", info, err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "alias pb=") {
		t.Fatalf("target was not updated: %s", data)
	}
}
