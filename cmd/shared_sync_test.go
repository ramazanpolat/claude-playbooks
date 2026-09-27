package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/auth"
	"github.com/ramazanpolat/claude-playbooks/internal/config"
)

// The known issue's path 2, end to end: one launch isolated by
// CLAUDE_PLAYBOOKS_ISOLATE_AUTH, a login of another account made there, then
// a plain launch. The machine store stays byte-identical, and the login is
// set aside, not copied (v3.23.1).
func TestOneIsolatedLaunchNeverSwapsTheMachineLogin(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	store := seedMachineLogin(t)
	stubClaude(t)
	home, _ := os.UserHomeDir()
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"hasCompletedOnboarding":true,"oauthAccount":{"accountUuid":"pilot-acct"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var notices []string
	old := auth.Notice
	auth.Notice = func(m string) { notices = append(notices, m) }
	t.Cleanup(func() { auth.Notice = old })

	mustStmt(t, "CREATE PLAYBOOK z NO ALIAS")
	pb := filepath.Join(config.ResolvePlaybooksDir(), "z")
	t.Setenv(auth.IsolateAuthEnv, "true")
	var err error
	captureStderr(t, func() { err = runRun(nil, []string{"z", "--version"}) })
	if err != nil {
		t.Fatal(err)
	}
	// What a /login as another account leaves (Claude Code writes by rename).
	if err := os.WriteFile(filepath.Join(pb, ".credentials.json"), []byte(sourceLogin), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pb, ".claude.json"), []byte(`{"oauthAccount":{"accountUuid":"other-acct"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(auth.IsolateAuthEnv, "")
	captureStderr(t, func() { err = runRun(nil, []string{"z", "--version"}) })
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(store); string(data) != machineLogin {
		t.Fatalf("the machine store changed: %s", data)
	}
	if aside, _ := filepath.Glob(filepath.Join(pb, ".credentials.json.cpb-own-*")); len(aside) != 1 {
		t.Fatalf("the other account's login was not set aside: %v", aside)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "another account's login") {
		t.Fatalf("notices: %q", notices)
	}
}
