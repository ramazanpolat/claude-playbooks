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
