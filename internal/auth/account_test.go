package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envset"
)

// accountFixture is a playbooks root with two env sets: acct-work holds the
// token by reference (an account), router holds none.
func accountFixture(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv(oauthTokenFileEnv, filepath.Join(t.TempDir(), "no-token"))
	os.Unsetenv(OAuthTokenEnv)
	root := t.TempDir()
	t.Setenv("CPB_PLAYBOOKS_DIR", root)
	config.PlaybooksDir = ""
	for _, s := range []*envset.Set{
		{Name: "acct-work", Refs: map[string]string{OAuthTokenEnv: "keychain:claude-token-work"}},
		{Name: "router", Set: map[string]string{"ANTHROPIC_MODEL": "glm"}},
	} {
		if err := envset.Write(envset.Dir(root), s); err != nil {
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
	ref, value := "reference", "value"
	work := "acct-work"
	for _, tc := range []struct {
		name, manifest string
		want           Account
	}{
		{"an account by reference", "[env]\nsets = [\"router\", \"acct-work\"]\n",
			Account{AuthMode: ModePlaybookToken, TokenSource: "env-set:acct-work", TokenKind: &ref, TokenAccount: &work}},
		{"isolated, on an account", "isolated_login = true\n\n[env]\nsets = [\"acct-work\"]\n",
			Account{AuthMode: ModePlaybookToken, TokenSource: "env-set:acct-work", TokenKind: &ref, TokenAccount: &work}},
		{"the playbook's own, by value", "[env.set]\nCLAUDE_CODE_OAUTH_TOKEN = \"sk-ant-oat01-OWN\"\n",
			Account{AuthMode: ModePlaybookToken, TokenSource: "playbook", TokenKind: &value}},
		{"blocked over an account", "[env]\nsets = [\"acct-work\"]\nblock = [\"CLAUDE_CODE_OAUTH_TOKEN\"]\n",
			Account{AuthMode: ModeSharedLogin, TokenSource: "none", TokenBlocked: true}},
		{"isolated, blocked", "isolated_login = true\n\n[env]\nblock = [\"CLAUDE_CODE_OAUTH_TOKEN\"]\n",
			Account{AuthMode: ModeIsolatedLogin, TokenSource: "none", TokenBlocked: true}},
		{"shared, no token", "", Account{AuthMode: ModeSharedLogin, TokenSource: "none"}},
		{"a missing env set", "[env]\nsets = [\"ghost\"]\n", Account{AuthMode: ModeError, TokenSource: "unknown"}},
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

// An account attached through DEFAULTS is the machine's layer: an isolated
// playbook never takes its token, so it reports its own login; a shared one
// reports the account.
func TestAccountOfDefaultsAccount(t *testing.T) {
	root := accountFixture(t)
	if err := envset.WriteDefaults(envset.Dir(root), []string{"acct-work"}); err != nil {
		t.Fatal(err)
	}
	if a := AccountOf(accountPlaybook(t, root, "iso", "isolated_login = true\n")); a.AuthMode != ModeIsolatedLogin || a.TokenSource != "none" || a.TokenKind != nil {
		t.Fatalf("isolated: %+v", a)
	}
	if a := AccountOf(accountPlaybook(t, root, "shared", "")); a.AuthMode != ModePlaybookToken || a.TokenSource != "env-set:acct-work" {
		t.Fatalf("shared: %+v", a)
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
	t.Setenv(OAuthTokenEnv, "sk-ant-oat01-SHELL")
	if a := AccountOf(shared); a.TokenSource != "shell" {
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
