package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// A helper account passes its apiKeyHelper as a launch-only setting, and
// never writes it into the playbook's settings.json.
func TestAccountHelperIsALaunchSetting(t *testing.T) {
	root := sandboxRoot(t, "pbs")
	writePlaybook(t, root, "h", &manifest.Manifest{IsolatedLogin: true})
	claudeLog := stubClaude(t)
	if _, err := quotedStmt(t, "CREATE ACCOUNT console SET provider = 'anthropic', helper = 'my-key-helper --vault work'"); err != nil {
		t.Fatal(err)
	}
	mustStmt(t, "ALTER PLAYBOOK h USE ACCOUNT console")
	if err := runRun(nil, []string{"h", "--version"}); err != nil {
		t.Fatal(err)
	}
	got := readLog(t, claudeLog)
	if !strings.Contains(got, `ARGS --settings {"apiKeyHelper":"my-key-helper --vault work"} --version`) {
		t.Fatalf("claude's arguments:\n%s", got)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "h", "settings.json")); strings.Contains(string(data), "apiKeyHelper") {
		t.Fatalf("the helper was written into settings.json: %s", data)
	}
}

// Both sides are allow-lists: a listed account runs only for a playbook
// that lists it, and a launch flag never sets an account's variable.
func TestAccountLaunchRefusals(t *testing.T) {
	root := sandboxRoot(t, "pbs")
	writePlaybook(t, root, "open", &manifest.Manifest{IsolatedLogin: true})
	claudeLog := stubClaude(t)
	mustStmt(t, "CREATE ACCOUNT work SET provider=gateway, base_url=http://localhost:4000/v1, auth=none, access=listed")
	if err := runRun(nil, []string{"--account", "work", "open", "--version"}); err == nil || !strings.Contains(err.Error(), "access = 'listed'") {
		t.Fatalf("a listed account for a playbook that does not list it: %v", err)
	}
	if err := runRun(nil, []string{"--account", "ghost", "open", "--version"}); err == nil || !strings.Contains(err.Error(), `account "ghost" does not exist`) {
		t.Fatalf("an unknown account: %v", err)
	}
	if err := runRun(nil, []string{"--env", "ANTHROPIC_BASE_URL=http://x/v1", "open", "--version"}); err == nil || !strings.Contains(err.Error(), "ANTHROPIC_BASE_URL, set by a launch flag") {
		t.Fatalf("a launch flag's routing: %v", err)
	}
	if _, err := os.Stat(claudeLog); err == nil {
		t.Fatal("claude ran on a refused launch")
	}
	// The working folder's local settings are refused too.
	work := t.TempDir()
	if err := os.MkdirAll(filepath.Join(work, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, ".claude", "settings.local.json"), []byte(`{"apiKeyHelper":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(work)
	if err := runRun(nil, []string{"open", "--version"}); err == nil || !strings.Contains(err.Error(), "sets apiKeyHelper") {
		t.Fatalf("a repo's apiKeyHelper: %v", err)
	}
	// Blocking an account's variable is fine: the launch removes it anyway.
	if err := os.Remove(filepath.Join(work, ".claude", "settings.local.json")); err != nil {
		t.Fatal(err)
	}
	if err := runRun(nil, []string{"--block", "ANTHROPIC_API_KEY", "open", "--version"}); err != nil {
		t.Fatalf("a blocked account variable: %v", err)
	}
}
