package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/account"
	"github.com/ramazanpolat/claude-playbooks/internal/envset"
)

// cpb ask --account: the question signs in on a Claude token account, its
// token in the child's environment alone (set by the secret helper for a
// reference), with a fresh config directory; nothing else signs in.
func TestAskAccount(t *testing.T) {
	askFixture(t)
	helper, _ := fakeHelper(t)
	t.Setenv(envset.SecretHelperEnv, helper)
	mustStmt(t, "CREATE ACCOUNT gm SET provider = anthropic, claude_token FROM keychain:ok/gm-token")
	putAccount(t, account.Account{Name: "plain", Provider: account.Anthropic, Auth: account.Auth{Kind: account.ClaudeToken, Value: "CANARY-plain-token"}})
	rec := fakeAskClaude(t, askListPlan)
	t.Setenv("ANTHROPIC_API_KEY", "other-key")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "other-token")

	if _, err := askRun(t, map[string]string{"account": "gm"}, "list"); err != nil {
		t.Fatal(err)
	}
	env := childEnv(t, rec)
	if env["CLAUDE_CODE_OAUTH_TOKEN"] != "resolved:keychain:ok/gm-token" {
		t.Errorf("the account's token, set by the helper: %q", env["CLAUDE_CODE_OAUTH_TOKEN"])
	}
	if _, ok := env["ANTHROPIC_API_KEY"]; ok {
		t.Error("an exported key went along with the account's token")
	}
	cfg := env["CLAUDE_CONFIG_DIR"]
	if cfg == "" || underHome(cfg) || !strings.Contains(cfg, "cpb-ask-") {
		t.Errorf("the config directory: %q, want a fresh one outside home", cfg)
	} else if _, err := os.Stat(cfg); !os.IsNotExist(err) {
		t.Errorf("the config directory is left behind: %s", cfg)
	}
	if argv := readRec(t, rec, "argv"); strings.Contains(argv, "keychain:ok/gm-token") || strings.Contains(argv, "resolved:") {
		t.Errorf("the reference or the token reached claude's argv:\n%s", argv)
	}

	// A token stored AS PLAINTEXT goes in as it is, the one credential.
	if _, err := askRun(t, map[string]string{"account": "plain"}, "list"); err != nil {
		t.Fatal(err)
	}
	env = childEnv(t, rec)
	if env["CLAUDE_CODE_OAUTH_TOKEN"] != "CANARY-plain-token" || !strings.Contains(env["CLAUDE_CONFIG_DIR"], "cpb-ask-") {
		t.Errorf("a plaintext token: %q, config dir %q", env["CLAUDE_CODE_OAUTH_TOKEN"], env["CLAUDE_CONFIG_DIR"])
	}
	for _, f := range []string{"argv", "stdin"} {
		if strings.Contains(readRec(t, rec, f), "CANARY") {
			t.Errorf("the token reached claude's %s", f)
		}
	}

	// machine is the way a question signs in without --account.
	if _, err := askRun(t, map[string]string{"account": "machine"}, "list"); err != nil {
		t.Fatal(err)
	}
	if _, ok := childEnv(t, rec)["CLAUDE_CONFIG_DIR"]; ok {
		t.Error("--account machine got a config directory")
	}
}

// The accounts cpb ask cannot sign in on are refused before claude runs,
// naming why and never a value.
func TestAskAccountRefused(t *testing.T) {
	askFixture(t)
	putAccount(t, account.Account{Name: "console", Provider: account.Anthropic, Auth: account.Auth{Kind: account.APIKey, Value: "sk-ant-CANARY"}})
	putAccount(t, account.Account{Name: "glm", Provider: account.Gateway, BaseURL: "http://127.0.0.1:9/v1", Auth: account.Auth{Kind: account.None}})
	putAccount(t, account.Account{Name: "headed", Provider: account.Anthropic, Auth: account.Auth{Kind: account.ClaudeToken, Value: "CANARY-2"}, Headers: map[string]string{"X-Org": "keychain:ok/org"}})
	putAccount(t, account.Account{Name: "only", Provider: account.Anthropic, Access: account.AccessListed, Auth: account.Auth{Kind: account.ClaudeToken, Value: "CANARY-3"}})
	putAccount(t, account.Account{Name: "byref", Provider: account.Anthropic, Auth: account.Auth{Kind: account.ClaudeToken, Ref: "keychain:ok/byref"}})
	t.Setenv(envset.SecretHelperEnv, "")
	rec := fakeAskClaude(t, askListPlan)
	for name, want := range map[string]string{
		"console": "signs in with its api_key",
		"glm":     "away from Anthropic",
		"headed":  "request headers of its own (X-Org)",
		"only":    "access = 'listed'",
		"byref":   "is a secret reference, and no secret helper configured",
		"nope":    "nope",
	} {
		_, err := askRun(t, map[string]string{"account": name}, "list")
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "nothing was sent") && name != "only" || strings.Contains(err.Error(), "CANARY") {
			t.Errorf("%s: %v, want %q", name, err, want)
		}
	}
	if _, err := os.Stat(filepath.Join(rec, "argv")); err == nil {
		t.Fatal("claude ran for a refused account")
	}
	var err error
	captureStderr(t, func() { _, err = askRun(t, map[string]string{"account": "Bad Name"}, "list") })
	if exitOf(err) != 2 {
		t.Errorf("an invalid account name: %v, want exit 2", err)
	}
}

// A playbook on a Claude token account: its question signs in with that
// token, as a launch would. --account with a playbook named must be one it
// may run on.
func TestAskAddressedAccount(t *testing.T) {
	askFixture(t)
	putAccount(t, account.Account{Name: "gm", Provider: account.Anthropic, Auth: account.Auth{Kind: account.ClaudeToken, Value: "token-gm"}})
	putAccount(t, account.Account{Name: "mt", Provider: account.Anthropic, Auth: account.Auth{Kind: account.ClaudeToken, Value: "token-mt"}})
	mustStmt(t, "CREATE PLAYBOOK tok SET launcher = '' USE ACCOUNT gm")
	mustStmt(t, "CREATE PLAYBOOK open SET launcher = ''")
	rec := fakeAskClaude(t, askListPlan)
	if _, err := askRun(t, nil, "tok", "list"); err != nil {
		t.Fatal(err)
	}
	if env := childEnv(t, rec); env["CLAUDE_CODE_OAUTH_TOKEN"] != "token-gm" || !strings.Contains(env["CLAUDE_CONFIG_DIR"], "cpb-ask-") {
		t.Errorf("tok's account: %q, %q", env["CLAUDE_CODE_OAUTH_TOKEN"], env["CLAUDE_CONFIG_DIR"])
	}
	if _, err := askRun(t, map[string]string{"account": "mt"}, "open", "list"); err != nil {
		t.Fatal(err)
	}
	if env := childEnv(t, rec); env["CLAUDE_CODE_OAUTH_TOKEN"] != "token-mt" || !strings.Contains(readRec(t, rec, "stdin"), `"addressed":"open"`) {
		t.Errorf("--account mt on open: %q", env["CLAUDE_CODE_OAUTH_TOKEN"])
	}
	os.Remove(filepath.Join(rec, "argv"))
	if _, err := askRun(t, map[string]string{"account": "mt"}, "tok", "list"); err == nil || !strings.Contains(err.Error(), "runs only on the accounts it lists") {
		t.Errorf("an account tok does not list: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rec, "argv")); err == nil {
		t.Fatal("claude ran on an account the playbook does not list")
	}
}

// A failure, a hit limit among them, ends with the accounts the question
// could sign in on instead: Claude token accounts open to any playbook, by
// name, never the one that failed.
func TestAskFailureNamesOtherAccounts(t *testing.T) {
	askFixture(t)
	fakeAskClaude(t, `{"is_error":true,"result":"You've hit your weekly limit · resets Nov 5","subtype":"success"}`)
	_, err := askRun(t, nil, "list")
	if err == nil || !strings.Contains(err.Error(), "weekly limit") || strings.Contains(err.Error(), "--account") {
		t.Fatalf("no accounts: %v", err)
	}
	putAccount(t, account.Account{Name: "gm", Provider: account.Anthropic, Auth: account.Auth{Kind: account.ClaudeToken, Value: "CANARY-gm"}})
	putAccount(t, account.Account{Name: "mt", Provider: account.Anthropic, Auth: account.Auth{Kind: account.ClaudeToken, Value: "CANARY-mt"}})
	putAccount(t, account.Account{Name: "only", Provider: account.Anthropic, Access: account.AccessListed, Auth: account.Auth{Kind: account.ClaudeToken, Value: "x"}})
	putAccount(t, account.Account{Name: "glm", Provider: account.Gateway, BaseURL: "http://127.0.0.1:9/v1", Auth: account.Auth{Kind: account.None}})
	_, err = askRun(t, nil, "list")
	if err == nil || !strings.Contains(err.Error(), `or ask on another account: cpb ask --account gm "<request>" (yours: gm, mt)`) || strings.Contains(err.Error(), "CANARY") {
		t.Fatalf("the default login failed: %v", err)
	}
	_, err = askRun(t, map[string]string{"account": "gm"}, "list")
	if err == nil || !strings.Contains(err.Error(), "signed in on account gm") || !strings.Contains(err.Error(), "(yours: mt)") {
		t.Fatalf("on account gm: %v", err)
	}
}

// --account <a claude_login account>: the login is decided on that
// account, as `cpb run --account` decides it, never on the playbook's own
// choice: no machine token, and no exported key or token beside the login.
func TestAskAccountClaudeLogin(t *testing.T) {
	root := askFixture(t)
	putAccount(t, account.Account{Name: "lg", Provider: account.Anthropic, Auth: account.Auth{Kind: account.ClaudeLogin}})
	putAccount(t, account.Account{Name: "gm", Provider: account.Anthropic, Auth: account.Auth{Kind: account.ClaudeToken, Value: "CANARY-gm"}})
	mustStmt(t, "CREATE PLAYBOOK open SET launcher = ''")
	mustStmt(t, "CREATE PLAYBOOK two SET launcher = '', login = 'isolated' USE ACCOUNT gm, lg")
	two := filepath.Join(root, "two")
	writeFile(t, filepath.Join(two, ".credentials.json"), `{"claudeAiOauth":{"accessToken":"a","refreshToken":"r","expiresAt":9999999999999}}`)
	tokenFile := filepath.Join(t.TempDir(), "oauth-token")
	writeFile(t, tokenFile, "CANARY-machine-token\n")
	t.Setenv("CPB_OAUTH_TOKEN_FILE", tokenFile)
	t.Setenv("ANTHROPIC_API_KEY", "CANARY-key")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "CANARY-exported")
	rec := fakeAskClaude(t, askListPlan)

	// open shares the machine's login; on lg the machine token is blocked.
	if _, err := askRun(t, map[string]string{"account": "lg"}, "open", "list"); err != nil {
		t.Fatal(err)
	}
	if env := readRec(t, rec, "env"); strings.Contains(env, "CANARY") || strings.Contains(env, "CLAUDE_CONFIG_DIR=") {
		t.Errorf("open on lg: another credential or a config dir went along:\n%s", env)
	}
	// two lists gm first; on lg it signs in with its own login.
	if _, err := askRun(t, map[string]string{"account": "lg"}, "two", "list"); err != nil {
		t.Fatal(err)
	}
	env := childEnv(t, rec)
	if env["CLAUDE_CONFIG_DIR"] != two || strings.Contains(readRec(t, rec, "env"), "CANARY") {
		t.Errorf("two on lg: config dir %q\n%s", env["CLAUDE_CONFIG_DIR"], readRec(t, rec, "env"))
	}
	// No playbook: Claude Code's own login, alone.
	if _, err := askRun(t, map[string]string{"account": "lg"}, "list"); err != nil {
		t.Fatal(err)
	}
	if env := readRec(t, rec, "env"); strings.Contains(env, "CANARY") || strings.Contains(env, "CLAUDE_CONFIG_DIR=") {
		t.Errorf("lg, no playbook:\n%s", env)
	}
}

// --account machine is what `cpb run --account machine` is: allowed for a
// playbook that lists no account, whatever DEFAULTS names, and signing in
// as the machine account; refused for one that lists others.
func TestAskAccountMachine(t *testing.T) {
	askFixture(t)
	putAccount(t, account.Account{Name: "gm", Provider: account.Anthropic, Auth: account.Auth{Kind: account.ClaudeToken, Value: "CANARY-gm"}})
	mustStmt(t, "ALTER DEFAULTS USE ACCOUNT gm")
	mustStmt(t, "CREATE PLAYBOOK open SET launcher = ''")
	mustStmt(t, "CREATE PLAYBOOK listed SET launcher = '' USE ACCOUNT gm")
	t.Setenv("CPB_OAUTH_TOKEN_FILE", filepath.Join(t.TempDir(), "none"))
	rec := fakeAskClaude(t, askListPlan)
	// Without the flag, open runs on DEFAULTS' gm.
	if _, err := askRun(t, nil, "open", "list"); err != nil {
		t.Fatal(err)
	}
	if childEnv(t, rec)["CLAUDE_CODE_OAUTH_TOKEN"] != "CANARY-gm" {
		t.Error("open without --account: not on DEFAULTS' account")
	}
	if _, err := askRun(t, map[string]string{"account": "machine"}, "open", "list"); err != nil {
		t.Fatalf("open on machine: %v", err)
	}
	if env := readRec(t, rec, "env"); strings.Contains(env, "CANARY") || strings.Contains(env, "CLAUDE_CONFIG_DIR=") {
		t.Errorf("open on machine: DEFAULTS' account went along:\n%s", env)
	}
	if _, err := askRun(t, map[string]string{"account": "machine"}, "listed", "list"); err == nil || !strings.Contains(err.Error(), "runs only on the accounts it lists") {
		t.Errorf("listed on machine: %v", err)
	}
}

// --account given empty (an unset variable) is a usage error, never the
// default login.
func TestAskAccountEmpty(t *testing.T) {
	askFixture(t)
	rec := fakeAskClaude(t, askListPlan)
	var err error
	captureStderr(t, func() { _, err = askRun(t, map[string]string{"account": ""}, "list") })
	if exitOf(err) != 2 {
		t.Fatalf("--account '': %v, want exit 2", err)
	}
	if _, err := os.Stat(filepath.Join(rec, "argv")); err == nil {
		t.Fatal("claude ran on --account ''")
	}
	// The flag left unset afterwards: the default login again.
	if _, err := askRun(t, nil, "list"); err != nil {
		t.Fatalf("after: %v", err)
	}
}

// cpb ask shows the secret helper's notice that a token was read from the
// file store, as a launch does; the rest of the child's stderr stays out.
func TestAskShowsTheHelperNotice(t *testing.T) {
	askFixture(t)
	dir := t.TempDir()
	helper := filepath.Join(dir, "notice-helper")
	script := `#!/bin/sh
[ "$1" = --check ] && exit 0
echo "cpb:gm-token is read from the file /home/user/.local/share/cpb/secrets/gm-token: the keyring does not hold it" >&2
echo "other helper chatter" >&2
while [ $# -gt 0 ] && [ "$1" != "--" ]; do k=${1%%=*}; export "$k=resolved"; shift; done
shift
exec "$@"
`
	if err := os.WriteFile(helper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envset.SecretHelperEnv, helper)
	mustStmt(t, "CREATE ACCOUNT gm SET provider = anthropic, claude_token FROM keychain:ok/gm-token")
	fakeAskClaude(t, askListPlan)
	var err error
	out := captureStderr(t, func() { _, err = askRun(t, map[string]string{"account": "gm"}, "list") })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "cpb:gm-token is read from the file /home/user/.local/share/cpb/secrets/gm-token") || strings.Contains(out, "other helper chatter") {
		t.Fatalf("stderr:\n%s", out)
	}
}
