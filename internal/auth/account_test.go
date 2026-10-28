package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/account"
	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envset"
)

// accountFixture is a playbooks root with three accounts: work (a Claude
// token by reference), team (the playbook's own Claude login) and glm (a
// router key, no Claude login), and an env set, acct-work, that still holds
// a token as v4.1 did (refused at launch since v4.2.0).
func accountFixture(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv(oauthTokenFileEnv, filepath.Join(t.TempDir(), "no-token"))
	os.Unsetenv(OAuthTokenEnv)
	root := t.TempDir()
	t.Setenv("CPB_PLAYBOOKS_DIR", root)
	config.PlaybooksDir = ""
	if err := envset.Write(envset.Dir(root), &envset.Set{Name: "acct-work", Refs: map[string]string{OAuthTokenEnv: "keychain:claude-token-work"}}); err != nil {
		t.Fatal(err)
	}
	for _, a := range []*account.Account{
		{Name: "work", Provider: account.Anthropic, Auth: account.Auth{Kind: account.ClaudeToken, Ref: "keychain:claude-token-work"}},
		{Name: "team", Provider: account.Anthropic, Auth: account.Auth{Kind: account.ClaudeLogin}},
		{Name: "glm", Provider: account.Gateway, BaseURL: "https://router.example/v1", Auth: account.Auth{Kind: account.AuthToken, Ref: "keychain:router"}},
	} {
		if err := account.Write(account.Dir(root), a); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func accountPlaybook(t *testing.T, root, name, manifest string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, dir, manifest)
	return dir
}

func TestAccountOf(t *testing.T) {
	root := accountFixture(t)
	ref := "reference"
	for _, tc := range []struct {
		name, manifest string
		want           Account
	}{
		{"a claude_token account", "[env]\naccounts = [\"work\"]\n",
			Account{AuthMode: ModePlaybookToken, TokenSource: "account", TokenKind: &ref}},
		{"isolated, on a claude_token account", "isolated_login = true\n\n[env]\naccounts = [\"work\"]\n",
			Account{AuthMode: ModePlaybookToken, TokenSource: "account", TokenKind: &ref}},
		{"shared, on a claude_login account", "[env]\naccounts = [\"team\"]\n",
			Account{AuthMode: ModeSharedLogin, TokenSource: "none", TokenBlocked: true}},
		{"isolated, on a claude_login account", "isolated_login = true\n\n[env]\naccounts = [\"team\"]\n",
			Account{AuthMode: ModeIsolatedLogin, TokenSource: "none"}},
		{"isolated, on a router account", "isolated_login = true\n\n[env]\naccounts = [\"glm\"]\n",
			Account{AuthMode: ModeAccount, TokenSource: "none"}},
		{"shared, on a router account: refused", "[env]\naccounts = [\"glm\"]\n",
			Account{AuthMode: ModeError, TokenSource: "unknown"}},
		{"a v4.1 token in an env set: refused", "[env]\nsets = [\"acct-work\"]\n",
			Account{AuthMode: ModeError, TokenSource: "unknown"}},
		{"a v4.1 token of the playbook's own: refused", "[env.set]\nCLAUDE_CODE_OAUTH_TOKEN = \"sk-ant-oat01-OWN\"\n",
			Account{AuthMode: ModeError, TokenSource: "unknown"}},
		{"isolated, blocked", "isolated_login = true\n\n[env]\nblock = [\"CLAUDE_CODE_OAUTH_TOKEN\"]\n",
			Account{AuthMode: ModeIsolatedLogin, TokenSource: "none", TokenBlocked: true}},
		{"shared, blocked", "[env]\nblock = [\"CLAUDE_CODE_OAUTH_TOKEN\"]\n",
			Account{AuthMode: ModeSharedLogin, TokenSource: "none", TokenBlocked: true}},
		{"shared, no token", "", Account{AuthMode: ModeSharedLogin, TokenSource: "none"}},
		{"a missing env set", "[env]\nsets = [\"ghost\"]\n", Account{AuthMode: ModeError, TokenSource: "unknown"}},
		{"a missing account", "[env]\naccounts = [\"ghost\"]\n", Account{AuthMode: ModeError, TokenSource: "unknown"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := AccountOf(accountPlaybook(t, root, strings.ReplaceAll(tc.name, " ", "-"), tc.manifest))
			gj, _ := json.Marshal(got)
			wj, _ := json.Marshal(tc.want)
			if string(gj) != string(wj) {
				t.Fatalf("got  %s\nwant %s", gj, wj)
			}
		})
	}
}

// A playbook that names no account runs on the DEFAULTS account: isolated
// or shared, it reports that account's credential.
func TestAccountOfDefaultsAccount(t *testing.T) {
	root := accountFixture(t)
	if err := account.WriteDefault(account.Dir(root), "work"); err != nil {
		t.Fatal(err)
	}
	for name, m := range map[string]string{"iso": "isolated_login = true\n", "shared": ""} {
		if a := AccountOf(accountPlaybook(t, root, name, m)); a.AuthMode != ModePlaybookToken || a.TokenSource != "account" {
			t.Fatalf("%s: %+v", name, a)
		}
	}
}

// The machine's token: the file, or one exported in the shell, which wins.
// An isolated playbook takes neither.
func TestAccountOfMachineToken(t *testing.T) {
	root := accountFixture(t)
	tokenPath := filepath.Join(t.TempDir(), "oauth-token")
	if err := os.WriteFile(tokenPath, []byte("sk-ant-oat01-FROMFILE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(oauthTokenFileEnv, tokenPath)
	shared := accountPlaybook(t, root, "shared", "")
	isolated := accountPlaybook(t, root, "iso", "isolated_login = true\n")

	if a := AccountOf(shared); a.AuthMode != ModeToken || a.TokenSource != "file" || a.TokenKind == nil || *a.TokenKind != "value" {
		t.Fatalf("file: %+v", a)
	}
	// A token exported in the shell never reaches a launch: still the file.
	t.Setenv(OAuthTokenEnv, "sk-ant-oat01-SHELL")
	if a := AccountOf(shared); a.TokenSource != "file" {
		t.Fatalf("shell: %+v", a)
	}
	if a := AccountOf(isolated); a.AuthMode != ModeIsolatedLogin || a.TokenSource != "none" || a.TokenKind != nil {
		t.Fatalf("isolated: %+v", a)
	}
	// Never a credential character.
	for _, dir := range []string{shared, isolated} {
		out, _ := json.Marshal(AccountOf(dir))
		if strings.Contains(string(out), "sk-ant") || strings.Contains(string(out), "keychain:") {
			t.Fatalf("account report carries a credential or a reference: %s", out)
		}
	}
}
