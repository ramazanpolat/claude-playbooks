package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/account"
	"github.com/ramazanpolat/claude-playbooks/internal/config"
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
	if err := runRun(nil, []string{"--env", "ANTHROPIC_BASE_URL=http://x/v1", "open", "--version"}); err == nil || !strings.Contains(err.Error(), "the launch's flags (--env, --env-set, --env-file) states an address with no key") {
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
		{[]string{"--settings", "missing.json"}, "cannot be checked"},
		{[]string{"--settings", "sk-ant-api03-SECRET"}, "cannot be checked"},
		{[]string{"--settings", "{not json"}, "does not parse"},
		// Keys match exactly, as Claude Code reads them.
		{[]string{"--settings", `{"env":{"ANTHROPIC_BASE_URL":"https://gateway.example"},"ENV":null}`}, "ANTHROPIC_BASE_URL"},
		{[]string{"--settings", `{"apiKeyHelper":"echo k","APIKEYHELPER":null}`}, "apiKeyHelper"},
		// Past "--" too: it may be another option's value, as here.
		{[]string{"--append-system-prompt", "--", "--settings", "routed.json"}, "ANTHROPIC_BASE_URL"},
	} {
		err := reservedInSettingsArgs(tc.args, work, false)
		if err != nil && strings.Contains(err.Error(), "SECRET") {
			t.Errorf("%q: the error echoes the value: %v", tc.args, err)
		}
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%q refused: %v", tc.args, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%q: want a refusal naming %q, got %v", tc.args, tc.want, err)
		}
	}
}

// A sandboxed launch takes --settings inline only: a file is read inside.
func TestReservedInSettingsArgsSandboxed(t *testing.T) {
	if err := reservedInSettingsArgs([]string{"--settings", `{"env":{"FOO":"1"}}`}, "", true); err != nil {
		t.Fatalf("inline: %v", err)
	}
	if err := reservedInSettingsArgs([]string{"--settings", "/tmp/s.json"}, "", true); err == nil || !strings.Contains(err.Error(), "inline") {
		t.Fatalf("a file: %v", err)
	}
}

// Inside a sandbox, claude starts from a plain shell: the login shell is
// read for PATH only, and every reserved variable the launch does not pass
// is removed; a value is never on the command line.
func TestClaudeCommand(t *testing.T) {
	cmd := claudeCommand("/w", []string{
		"ANTHROPIC_BASE_URL=https://router.example/v1",
		"ANTHROPIC_AUTH_TOKEN=sk-secret",
		"PATH=/bin",
	})
	for _, want := range []string{"cd '/w' && p=\"$(bash -lc ", "</dev/null", "&& exec env -u ", "-u ANTHROPIC_API_KEY", "-u CLAUDE_CODE_OAUTH_TOKEN", "-u CLAUDE_CODE_USE_BEDROCK", ` PATH="$p" claude`} {
		if !strings.Contains(cmd, want) {
			t.Errorf("lacks %q: %s", want, cmd)
		}
	}
	for _, not := range []string{"sk-secret", "router.example", "-u ANTHROPIC_BASE_URL", "-u ANTHROPIC_AUTH_TOKEN", "CPB_ACCOUNT"} {
		if strings.Contains(cmd, not) {
			t.Errorf("carries %q: %s", not, cmd)
		}
	}
}

// ANTHROPIC_CONFIG_DIR's directory is made afresh at every launch: what a
// session left there, or a link in its place, is gone.
func TestPrepareRecreatesAnthropicConfigDir(t *testing.T) {
	cfg := t.TempDir()
	dir := filepath.Join(cfg, anthropicConfigDirName)
	a := &accountLaunch{configDir: cfg}
	if err := a.prepare(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "profile.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.prepare(); err != nil {
		t.Fatal(err)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("not empty: %v", ents)
	}
	elsewhere := t.TempDir()
	if err := os.WriteFile(filepath.Join(elsewhere, "keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(dir)
	if err := os.Symlink(elsewhere, dir); err != nil {
		t.Fatal(err)
	}
	if err := a.prepare(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("not a directory of its own: %v %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(elsewhere, "keep")); err != nil {
		t.Fatal("the link's target was touched")
	}
}

// A helper account takes claude's one --settings: a passed one is refused.
func TestHelperWithSettings(t *testing.T) {
	sel := account.Selection{Name: "h"}
	r := account.Rendered{KeyHelper: "print-key"}
	if err := helperWithSettings(sel, r, []string{"-p", "x"}); err != nil {
		t.Fatal(err)
	}
	if err := helperWithSettings(sel, r, []string{"--settings", "{}"}); err == nil {
		t.Fatal("a passed --settings with a helper account")
	}
	if err := helperWithSettings(sel, account.Rendered{}, []string{"--settings", "{}"}); err != nil {
		t.Fatal(err)
	}
}

// A mount that covers the registry's account store is refused whether or
// not the store exists yet: one made later (a sandbox's account record, an
// account) would land inside the mount.
func TestMountCoveringAMissingAccountStore(t *testing.T) {
	root := filepath.Join(t.TempDir(), "pbs")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CPB_PLAYBOOKS_DIR", root)
	config.PlaybooksDir = ""
	t.Setenv("HOME", t.TempDir())
	if _, err := os.Stat(account.Dir(root)); !os.IsNotExist(err) {
		t.Fatalf("the account store exists: %v", err)
	}
	if what := machineLoginInside(filepath.Dir(root)); !strings.Contains(what, "env sets") && !strings.Contains(what, "accounts") {
		t.Fatalf("a mount over the playbooks root passed: %q", what)
	}
	if what := machineLoginInside(t.TempDir()); what != "" {
		t.Fatalf("an unrelated mount refused: %q", what)
	}
}
