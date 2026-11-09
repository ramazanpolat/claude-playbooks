package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/account"
	"github.com/ramazanpolat/claude-playbooks/internal/auth"
	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envset"
)

// askGatewayFixture is askFixture with gateway accounts: gw (a token and a
// header by reference, its own models), gwkey (a key stored AS PLAINTEXT),
// fwd (a gateway trusted with the Claude login) and bed (Bedrock). The
// shell carries an Anthropic key and token that must never go along.
func askGatewayFixture(t *testing.T) {
	t.Helper()
	askFixture(t)
	helper, _ := fakeHelper(t)
	t.Setenv(envset.SecretHelperEnv, helper)
	putAccount(t, account.Account{Name: "gw", Provider: account.Gateway, BaseURL: "https://gw.example.net/v1/CANARY-path",
		DefaultModel: "glm-5.3", SonnetModel: "glm-5.3", Auth: account.Auth{Kind: account.AuthToken, Ref: "keychain:ok/gw-key"},
		Headers: map[string]string{"X-Org": "keychain:ok/org"}})
	putAccount(t, account.Account{Name: "gwkey", Provider: account.Gateway, BaseURL: "http://127.0.0.1:9/v1", Auth: account.Auth{Kind: account.APIKey, Value: "plain-gw-key"}})
	putAccount(t, account.Account{Name: "fwd", Provider: account.Gateway, BaseURL: "https://fwd.example.net/v1", ClaudeLoginTrusted: true, Auth: account.Auth{Kind: account.ClaudeToken, Value: "CANARY-claude-token"}})
	putAccount(t, account.Account{Name: "bed", Provider: account.Bedrock, Auth: account.Auth{Kind: account.Cloud}})
	putAccount(t, account.Account{Name: "gwnone", Provider: account.Gateway, BaseURL: "http://127.0.0.1:9/v1", Auth: account.Auth{Kind: account.None}})
	putAccount(t, account.Account{Name: "gwhelper", Provider: account.Gateway, BaseURL: "https://gw.example.net/v1", Auth: account.Auth{Kind: account.Helper, Helper: "/usr/local/bin/print-gw-key"}})
	t.Setenv("ANTHROPIC_API_KEY", "CANARY-shell-key")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "CANARY-shell-token")
	tokenFile := filepath.Join(t.TempDir(), "oauth-token")
	writeFile(t, tokenFile, "CANARY-machine-token\n")
	t.Setenv("CPB_OAUTH_TOKEN_FILE", tokenFile)
}

// Without --allow-other-provider, an account away from Anthropic is
// refused before claude runs, and the refusal names the flag and what it
// would send, or says why the flag would not help. A Claude credential
// stays on Anthropic with the flag too.
func TestAskOtherProviderRefused(t *testing.T) {
	askGatewayFixture(t)
	rec := fakeAskClaude(t, askListPlan)
	for _, c := range []struct {
		name  string
		allow bool
		want  []string
	}{
		{"gw", false, []string{"away from Anthropic (provider gateway)", "nothing was sent", "To ask there anyway, add --allow-other-provider",
			"account gw's gateway at https://gw.example.net receives", "the playbooks root", "session counts", "the platform and OS version",
			"neither reads nor writes your ask history and does not send .ask/CLAUDE.md", "a follow-up that leans on an earlier answer does not work"}},
		{"fwd", false, []string{"away from Anthropic", "does not lift this"}},
		{"fwd", true, []string{"would send your Claude credential to a gateway", "--allow-other-provider or not", "nothing was sent"}},
		{"bed", false, []string{"away from Anthropic (provider bedrock)", "asks a gateway account only"}},
		{"bed", true, []string{"is a bedrock account", "asks a gateway account only", "nothing was sent"}},
	} {
		flags := map[string]string{"account": c.name}
		if c.allow {
			flags[askAllowOtherFlag] = "true"
		}
		_, err := askRun(t, flags, "list")
		for _, w := range c.want {
			if err == nil || !strings.Contains(err.Error(), w) {
				t.Errorf("%s (allow %v): %v, want %q", c.name, c.allow, err, w)
			}
		}
		if err != nil && (strings.Contains(err.Error(), "CANARY") || strings.Contains(err.Error(), "keychain:")) {
			t.Errorf("%s: the refusal names a value or a reference: %v", c.name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(rec, "argv")); err == nil {
		t.Fatal("claude ran for a refused account")
	}
}

// With --allow-other-provider, the question on a gateway account goes to
// its address, signed in with its own credential (by reference through the
// secret helper, its headers through cpb's header step), on its own
// models, from a new, empty config directory, and with no Anthropic key or
// Claude token beside it.
func TestAskOtherProvider(t *testing.T) {
	askGatewayFixture(t)
	rec := fakeAskClaude(t, askListPlan)
	var err error
	stderr := captureStderr(t, func() {
		_, err = askRun(t, map[string]string{"account": "gw", askAllowOtherFlag: "true"}, "list")
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "--allow-other-provider: asking account gw's gateway at https://gw.example.net, not Anthropic, without your ask history or .ask/CLAUDE.md") {
		t.Errorf("no notice of where the question went:\n%s", stderr)
	}
	env := childEnv(t, rec)
	for k, want := range map[string]string{
		"ANTHROPIC_BASE_URL":             "https://gw.example.net/v1/CANARY-path",
		"ANTHROPIC_AUTH_TOKEN":           "resolved:keychain:ok/gw-key",
		"ANTHROPIC_CUSTOM_HEADERS":       "X-Org: resolved:keychain:ok/org",
		"ANTHROPIC_DEFAULT_MODEL":        "glm-5.3",
		"ANTHROPIC_DEFAULT_SONNET_MODEL": "glm-5.3",
	} {
		if env[k] != want {
			t.Errorf("%s = %q, want %q", k, env[k], want)
		}
	}
	for _, k := range []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"} {
		if _, ok := env[k]; ok {
			t.Errorf("%s went along to the gateway", k)
		}
	}
	raw := readRec(t, rec, "env")
	for _, c := range []string{"CANARY-shell", "CANARY-machine", "CANARY-claude", "CANARY-account", "CANARY-env-set"} {
		if strings.Contains(raw, c) {
			t.Errorf("%s reached the child's environment", c)
		}
	}
	cfg := env["CLAUDE_CONFIG_DIR"]
	if cfg == "" || underHome(cfg) || !strings.Contains(cfg, "cpb-ask-") {
		t.Errorf("the config directory: %q, want a fresh one outside home", cfg)
	} else if _, err := os.Stat(cfg); !os.IsNotExist(err) {
		t.Errorf("the config directory is left behind: %s", cfg)
	}
	if !strings.HasPrefix(env["ANTHROPIC_CONFIG_DIR"], cfg+string(filepath.Separator)) {
		t.Errorf("ANTHROPIC_CONFIG_DIR = %q, want one inside %s", env["ANTHROPIC_CONFIG_DIR"], cfg)
	}
	argv := readRec(t, rec, "argv")
	if !strings.Contains(argv, "--model\x00glm-5.3\x00") {
		t.Errorf("the account's model was not asked for:\n%q", argv)
	}
	for _, f := range []string{"argv", "stdin"} {
		if s := readRec(t, rec, f); strings.Contains(s, "keychain:ok") || strings.Contains(s, "resolved:") {
			t.Errorf("a reference or a value reached claude's %s", f)
		}
	}

	// --model wins over the account's model; a key stored AS PLAINTEXT
	// goes in as the account renders it, with no helper needed.
	t.Setenv(envset.SecretHelperEnv, "")
	captureStderr(t, func() {
		_, err = askRun(t, map[string]string{"account": "gwkey", askAllowOtherFlag: "true", "model": "big-model"}, "list")
	})
	if err != nil {
		t.Fatal(err)
	}
	env = childEnv(t, rec)
	if env["ANTHROPIC_API_KEY"] != "plain-gw-key" || env["ANTHROPIC_BASE_URL"] != "http://127.0.0.1:9/v1" {
		t.Errorf("gwkey: key %q, base %q", env["ANTHROPIC_API_KEY"], env["ANTHROPIC_BASE_URL"])
	}
	if _, ok := env["ANTHROPIC_AUTH_TOKEN"]; ok {
		t.Error("gwkey got an auth token")
	}
	if !strings.Contains(readRec(t, rec, "argv"), "--model\x00big-model\x00") {
		t.Error("--model did not win over the account's model")
	}

	// A reference needs the helper: with none, refused before claude runs.
	os.Remove(filepath.Join(rec, "argv"))
	captureStderr(t, func() {
		_, err = askRun(t, map[string]string{"account": "gw", askAllowOtherFlag: "true"}, "list")
	})
	if err == nil || !strings.Contains(err.Error(), "secret references") || !strings.Contains(err.Error(), "nothing was sent") {
		t.Errorf("gw with no helper: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rec, "argv")); err == nil {
		t.Fatal("claude ran with no helper for a reference")
	}
}

// A keyless gateway account (auth = 'none') gets cpb's fixed, non-secret
// placeholder, as a launch does; an apiKeyHelper account passes its helper
// as a call-only setting, never as a value.
func TestAskOtherProviderKeylessAndHelper(t *testing.T) {
	askGatewayFixture(t)
	rec := fakeAskClaude(t, askListPlan)
	var err error
	captureStderr(t, func() {
		_, err = askRun(t, map[string]string{"account": "gwnone", askAllowOtherFlag: "true"}, "list")
	})
	if err != nil {
		t.Fatal(err)
	}
	env := childEnv(t, rec)
	if env["ANTHROPIC_AUTH_TOKEN"] != auth.NoCredentialPlaceholder || env["ANTHROPIC_BASE_URL"] != "http://127.0.0.1:9/v1" {
		t.Errorf("gwnone: token %q, base %q", env["ANTHROPIC_AUTH_TOKEN"], env["ANTHROPIC_BASE_URL"])
	}
	if _, ok := env["ANTHROPIC_API_KEY"]; ok {
		t.Error("gwnone got an API key")
	}

	captureStderr(t, func() {
		_, err = askRun(t, map[string]string{"account": "gwhelper", askAllowOtherFlag: "true"}, "list")
	})
	if err != nil {
		t.Fatal(err)
	}
	if argv := readRec(t, rec, "argv"); !strings.Contains(argv, "--settings\x00{\"apiKeyHelper\":\"/usr/local/bin/print-gw-key\"}\x00") {
		t.Errorf("gwhelper: no apiKeyHelper setting:\n%q", argv)
	}
	env = childEnv(t, rec)
	for _, k := range []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"} {
		if _, ok := env[k]; ok {
			t.Errorf("gwhelper: %s set beside the helper", k)
		}
	}
	if env["ANTHROPIC_BASE_URL"] != "https://gw.example.net/v1" {
		t.Errorf("gwhelper: base %q", env["ANTHROPIC_BASE_URL"])
	}
}

// An ask on another provider neither reads nor writes the ask store: no
// standing context and no history in the prompt, a tool that reads none,
// and history.md byte-identical afterwards, with or without --no-history.
func TestAskOtherProviderNoStore(t *testing.T) {
	askGatewayFixture(t)
	root := config.ResolvePlaybooksDir()
	rec := fakeAskClaude(t, askListPlan)
	id, err := appendAskRecord(root, "", "an earlier CANARY-history question", &askPlan{Answer: "CANARY-history answer"})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(askStoreDir(root), askStandingName), "CANARY-standing context\n")
	path := filepath.Join(askStoreDir(root), askHistoryName)
	before, _ := os.ReadFile(path)
	out := captureStdout(t, func() {
		captureStderr(t, func() {
			_, err = askRun(t, map[string]string{"account": "gw", askAllowOtherFlag: "true", "json": "true"}, "what did I ask before?")
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	stdin := readRec(t, rec, "stdin")
	for _, c := range []string{"CANARY-history", "CANARY-standing", id, "History of your earlier answers", "standing context"} {
		if strings.Contains(stdin, c) {
			t.Errorf("%q reached the other provider's prompt", c)
		}
	}
	if argv := readRec(t, rec, "argv"); !strings.Contains(argv, `"ask-tool","--no-history"`) || strings.Contains(argv, "CANARY") {
		t.Errorf("the tool was started to read the history:\n%q", argv)
	}
	if strings.Contains(out, `"record"`) {
		t.Errorf("the ask was recorded:\n%s", out)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatal("history.md changed")
	}

	// An ask on Anthropic still reads both.
	if _, err := askRun(t, nil, "list"); err != nil {
		t.Fatal(err)
	}
	if stdin := readRec(t, rec, "stdin"); !strings.Contains(stdin, "CANARY-standing") || !strings.Contains(stdin, "CANARY-history") {
		t.Error("an ask on Anthropic lost the store")
	}
}

// --allow-other-provider lifts a playbook's refusal only when its account
// alone routes it: a settings file that routes it too, or a layer that does
// not resolve, keeps it refused, with --account as without.
func TestAskOtherProviderRouteBeyondAccount(t *testing.T) {
	askGatewayFixture(t)
	root := config.ResolvePlaybooksDir()
	rec := fakeAskClaude(t, askListPlan)
	mustStmt(t, "CREATE PLAYBOOK gwset SET launcher = '' USE ACCOUNT gw")
	writeFile(t, filepath.Join(root, "gwset", "settings.json"), `{"env":{"ANTHROPIC_BASE_URL":"https://elsewhere.example.net"}}`)
	mustStmt(t, "CREATE ENV ghost SET VAR GHOST_SETTING=1")
	mustStmt(t, "CREATE PLAYBOOK gwghost SET launcher = '' USE ENV ghost USE ACCOUNT gw")
	if err := os.Remove(filepath.Join(root, ".env-sets", "ghost.toml")); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		pb, account string
	}{{"gwset", ""}, {"gwset", "gw"}, {"gwghost", ""}} {
		flags := map[string]string{askAllowOtherFlag: "true"}
		if c.account != "" {
			flags["account"] = c.account
		}
		var err error
		captureStderr(t, func() {
			_, err = askRun(t, flags, c.pb, "how many of your sessions are live?")
		})
		if err == nil || !strings.Contains(err.Error(), "nothing was sent") || strings.Contains(err.Error(), "To ask there anyway") {
			t.Errorf("%s (account %q) with the flag: %v", c.pb, c.account, err)
		}
	}
	if _, err := os.Stat(filepath.Join(rec, "argv")); err == nil {
		t.Fatal("claude ran for a playbook routed by more than its account")
	}
}

// A failure on a gateway account says where the question went.
func TestAskOtherProviderFailure(t *testing.T) {
	askGatewayFixture(t)
	fakeAskClaude(t, `{"is_error":true,"result":"upstream refused the model","subtype":"success"}`)
	var err error
	captureStderr(t, func() {
		_, err = askRun(t, map[string]string{"account": "gwkey", askAllowOtherFlag: "true"}, "list")
	})
	if err == nil || !strings.Contains(err.Error(), "upstream refused the model") || !strings.Contains(err.Error(), "cpb ask asked account gwkey's gateway at http://127.0.0.1:9, with the account's own credential") {
		t.Fatalf("a failure on gwkey: %v", err)
	}
}

// A playbook that runs on a gateway account: refused, naming its account
// and the flag; with the flag, it asks there, as a launch of it would.
// The flag changes nothing for an Anthropic account.
func TestAskOtherProviderPlaybook(t *testing.T) {
	askGatewayFixture(t)
	rec := fakeAskClaude(t, askListPlan)
	mustStmt(t, "CREATE PLAYBOOK gwpb SET launcher = '' USE ACCOUNT gw")
	_, err := askRun(t, nil, "gwpb", "how many of your sessions are live?")
	for _, w := range []string{"gwpb sends its requests away from Anthropic", "It runs on account gw (provider gateway)", "add --allow-other-provider", "nothing was sent"} {
		if err == nil || !strings.Contains(err.Error(), w) {
			t.Errorf("gwpb without the flag: %v, want %q", err, w)
		}
	}
	if _, err := os.Stat(filepath.Join(rec, "argv")); err == nil {
		t.Fatal("claude ran for a refused playbook")
	}
	captureStderr(t, func() {
		_, err = askRun(t, map[string]string{askAllowOtherFlag: "true"}, "gwpb", "how many of your sessions are live?")
	})
	if err != nil {
		t.Fatal(err)
	}
	env := childEnv(t, rec)
	if env["ANTHROPIC_BASE_URL"] != "https://gw.example.net/v1/CANARY-path" || env["ANTHROPIC_AUTH_TOKEN"] != "resolved:keychain:ok/gw-key" || !strings.Contains(readRec(t, rec, "stdin"), `"addressed":"gwpb"`) {
		t.Errorf("gwpb with the flag: base %q, token %q", env["ANTHROPIC_BASE_URL"], env["ANTHROPIC_AUTH_TOKEN"])
	}

	// An env-routed playbook is not an account's route: no flag lifts it.
	_, err = askRun(t, map[string]string{askAllowOtherFlag: "true"}, "work", "how many of your sessions are live?")
	if err == nil || !strings.Contains(err.Error(), "sends its requests away from Anthropic") || strings.Contains(err.Error(), "add --allow-other-provider") {
		t.Errorf("work with the flag: %v", err)
	}

	// An Anthropic account with the flag signs in as without it.
	putAccount(t, account.Account{Name: "gm", Provider: account.Anthropic, Auth: account.Auth{Kind: account.ClaudeToken, Value: "token-gm"}})
	if _, err := askRun(t, map[string]string{"account": "gm", askAllowOtherFlag: "true"}, "list"); err != nil {
		t.Fatal(err)
	}
	if env := childEnv(t, rec); env["CLAUDE_CODE_OAUTH_TOKEN"] != "token-gm" || env["ANTHROPIC_BASE_URL"] != "" {
		t.Errorf("gm with the flag: token %q, base %q", env["CLAUDE_CODE_OAUTH_TOKEN"], env["ANTHROPIC_BASE_URL"])
	}
}
