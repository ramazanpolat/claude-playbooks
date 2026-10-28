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

// A --settings passed through to claude is held to the settings files' rule:
// inline or a file, a reserved variable or an apiKeyHelper refuses.
func TestReservedInSettingsArgs(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "routed.json"), []byte(`{"env":{"ANTHROPIC_BASE_URL":"https://gateway.example"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "plain.json"), []byte(`{"env":{"FOO":"1"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		want string // "" for accepted
	}{
		{[]string{"-p", "hi"}, ""},
		{[]string{"--settings", `{"env":{"FOO":"1"}}`}, ""},
		{[]string{"--settings", "plain.json"}, ""},
		{[]string{"--settings", `{"env":{"ANTHROPIC_BASE_URL":"https://gateway.example"}}`}, "ANTHROPIC_BASE_URL"},
		{[]string{"--settings={\"apiKeyHelper\":\"echo k\"}"}, "apiKeyHelper"},
		{[]string{"-p", "x", "--settings", "routed.json"}, "ANTHROPIC_BASE_URL"},
		{[]string{"--settings", "missing.json"}, "cannot be read"},
		{[]string{"--settings", "{not json"}, "does not parse"},
	} {
		err := reservedInSettingsArgs(tc.args, work)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%q refused: %v", tc.args, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%q: want a refusal naming %q, got %v", tc.args, tc.want, err)
		}
	}
}

// Inside a sandbox the account's variables are set again after the login
// shell, and every other one is removed; a credential is never on the
// command line.
func TestAccountEnvCommand(t *testing.T) {
	cmd := accountEnvCommand([]string{
		"ANTHROPIC_BASE_URL=https://router.example/v1",
		"ANTHROPIC_AUTH_TOKEN=sk-secret",
		"ANTHROPIC_DEFAULT_MODEL=glm",
		"PATH=/bin",
	})
	for _, want := range []string{"env -u ", "-u CLAUDE_CODE_USE_BEDROCK", "'ANTHROPIC_BASE_URL=https://router.example/v1'", "'ANTHROPIC_DEFAULT_MODEL=glm'"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("lacks %q: %s", want, cmd)
		}
	}
	for _, not := range []string{"sk-secret", "ANTHROPIC_AUTH_TOKEN", "-u ANTHROPIC_BASE_URL", "PATH", "CLAUDE_CODE_OAUTH_TOKEN"} {
		if strings.Contains(cmd, not) {
			t.Errorf("carries %q: %s", not, cmd)
		}
	}
	if i, j := strings.LastIndex(cmd, "-u "), strings.Index(cmd, "'ANTHROPIC_"); i > j {
		t.Errorf("an assignment before an option: %s", cmd)
	}
}
