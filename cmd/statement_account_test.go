package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/account"
	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envset"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

// putAccount stores an account directly, without the secret helper's
// check, for tests that launch on it.
func putAccount(t *testing.T, a account.Account) {
	t.Helper()
	if err := account.Write(account.Dir(config.ResolvePlaybooksDir()), &a); err != nil {
		t.Fatal(err)
	}
}

func readAccount(t *testing.T, root, name string) *account.Account {
	t.Helper()
	a, err := account.Read(account.Dir(root), name)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestAccountLifecycle(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CPB_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	helper, log := fakeHelper(t)
	t.Setenv(envset.SecretHelperEnv, helper)

	out := mustStmt(t, "CREATE ACCOUNT glm SET provider=gateway, base_url=http://localhost:4000/v1, auth_token FROM keychain:ok-glm, model=glm-5.3")
	if !strings.Contains(out, "Created ACCOUNT glm") || !strings.Contains(out, "auth_token  <from keychain:ok-glm>") {
		t.Fatalf("create report:\n%s", out)
	}
	a := readAccount(t, root, "glm")
	if a == nil || a.Provider != account.Gateway || a.Auth.Kind != account.AuthToken || a.Auth.Ref != "keychain:ok-glm" || a.DefaultModel != "glm-5.3" {
		t.Fatalf("stored: %+v", a)
	}
	// The helper was asked about the reference, under the variable it
	// becomes at launch.
	if got := readLog(t, log); !strings.Contains(got, "--check ANTHROPIC_AUTH_TOKEN=keychain:ok-glm") {
		t.Fatalf("helper log: %q", got)
	}
	if err := stmtErr(t, "CREATE ACCOUNT glm SET provider=anthropic, auth=claude_login"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("a second CREATE: %v", err)
	}
	if out := mustStmt(t, "CREATE ACCOUNT IF NOT EXISTS glm SET provider=anthropic, auth=claude_login"); !strings.Contains(out, "unchanged") {
		t.Fatalf("IF NOT EXISTS:\n%s", out)
	}
	// A reference the helper cannot resolve writes nothing.
	if err := stmtErr(t, "ALTER ACCOUNT glm SET auth_token FROM keychain:missing"); err == nil {
		t.Fatal("an unresolvable reference was stored")
	}
	if readAccount(t, root, "glm").Auth.Ref != "keychain:ok-glm" {
		t.Fatal("a refused ALTER changed the account")
	}
	// Moving the account needs its credential in the same statement.
	if err := stmtErr(t, "ALTER ACCOUNT glm SET base_url=https://router.example/v1"); err == nil || !strings.Contains(err.Error(), "state the credential") {
		t.Fatalf("move without the credential: %v", err)
	}
	mustStmt(t, "ALTER ACCOUNT glm SET base_url=https://router.example/v1, auth_token FROM keychain:ok-glm")
	if readAccount(t, root, "glm").BaseURL != "https://router.example/v1" {
		t.Fatal("the move did not land")
	}
	// The record is validated whole: a gateway never takes the Claude login
	// unless trusted.
	if err := stmtErr(t, "ALTER ACCOUNT glm SET auth=claude_login"); err == nil || !strings.Contains(err.Error(), "claude_login_trusted") {
		t.Fatalf("an untrusted gateway took the Claude login: %v", err)
	}
	if out := mustStmt(t, "ALTER ACCOUNT glm SET model=glm-5.3"); !strings.Contains(out, "unchanged") {
		t.Fatalf("same value:\n%s", out)
	}
	mustStmt(t, "ALTER ACCOUNT glm RESET model")
	if readAccount(t, root, "glm").DefaultModel != "" {
		t.Fatal("RESET model kept it")
	}

	// A playbook lists it; DROP is refused while it does.
	mustStmt(t, "CREATE PLAYBOOK r SET launcher = '' USE ACCOUNT glm, machine")
	if got := strings.Join(readEnv(t, filepath.Join(root, "r")).Accounts, ","); got != "glm,machine" {
		t.Fatalf("list: %q", got)
	}
	if err := stmtErr(t, "ALTER PLAYBOOK r USE ACCOUNT ghost"); err == nil || !strings.Contains(err.Error(), `account "ghost" does not exist`) {
		t.Fatalf("an unknown account: %v", err)
	}
	if err := stmtErr(t, "DROP ACCOUNT glm"); err == nil || !strings.Contains(err.Error(), "used by r") {
		t.Fatalf("drop in use: %v", err)
	}
	show := mustStmt(t, "SHOW PLAYBOOK r --json")
	if !strings.Contains(show, `"accounts": [`) || !strings.Contains(show, `"glm"`) {
		t.Fatalf("SHOW PLAYBOOK:\n%s", show)
	}
	if show := mustStmt(t, "SHOW CREATE PLAYBOOK r"); !strings.Contains(show, "USE ACCOUNT glm, machine;") {
		t.Fatalf("SHOW CREATE PLAYBOOK:\n%s", show)
	}
	if out := mustStmt(t, "ALTER PLAYBOOK r USE ACCOUNT glm, machine"); !strings.Contains(out, "unchanged") {
		t.Fatalf("the same list:\n%s", out)
	}
	mustStmt(t, "ALTER PLAYBOOK r USE ACCOUNT ''")
	if e := readEnv(t, filepath.Join(root, "r")); e != nil && len(e.Accounts) > 0 {
		t.Fatalf("emptied list: %v", e.Accounts)
	}

	// DEFAULTS names one account, never a listed one.
	mustStmt(t, "CREATE ACCOUNT mine SET provider=anthropic, auth=claude_login")
	mustStmt(t, "ALTER DEFAULTS USE ACCOUNT mine")
	if def, _ := account.Default(account.Dir(root)); def != "mine" {
		t.Fatalf("DEFAULTS account: %q", def)
	}
	if show := mustStmt(t, "SHOW DEFAULTS --json"); !strings.Contains(show, `"account": "mine"`) {
		t.Fatalf("SHOW DEFAULTS:\n%s", show)
	}
	if err := stmtErr(t, "ALTER ACCOUNT mine SET access=listed"); err == nil || !strings.Contains(err.Error(), "DEFAULTS account") {
		t.Fatalf("listed default: %v", err)
	}
	mustStmt(t, "ALTER ACCOUNT glm SET access=listed")
	if err := stmtErr(t, "ALTER DEFAULTS USE ACCOUNT glm"); err == nil || !strings.Contains(err.Error(), "access = 'listed'") {
		t.Fatalf("listed account as DEFAULTS: %v", err)
	}
	if err := stmtErr(t, "ALTER DEFAULTS USE ACCOUNT glm, mine"); err == nil || !strings.Contains(err.Error(), "one account") {
		t.Fatalf("two DEFAULTS accounts: %v", err)
	}
	if err := stmtErr(t, "DROP ACCOUNT mine"); err == nil || !strings.Contains(err.Error(), "DEFAULTS account") {
		t.Fatalf("drop the DEFAULTS account: %v", err)
	}
	mustStmt(t, "ALTER DEFAULTS USE ACCOUNT ''")
	mustStmt(t, "DROP ACCOUNT mine")
	if readAccount(t, root, "mine") != nil {
		t.Fatal("DROP left the account")
	}
	if out := mustStmt(t, "DROP ACCOUNT IF EXISTS mine"); !strings.Contains(out, "nothing to drop") {
		t.Fatalf("IF EXISTS:\n%s", out)
	}
}

func TestAccountShow(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CPB_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	helper, _ := fakeHelper(t)
	t.Setenv(envset.SecretHelperEnv, helper)
	mustStmt(t, "CREATE ACCOUNT glm SET provider=gateway, base_url=http://localhost:4000/v1, auth_token FROM keychain:ok-glm, identity=me@example.com")
	mustStmt(t, "CREATE ACCOUNT console SET provider=anthropic, api_key=sk-ant-literal-123 AS PLAINTEXT")

	for _, line := range []string{"SHOW ACCOUNTS", "SHOW ACCOUNTS --json", "SHOW ACCOUNT console", "SHOW ACCOUNT console --json"} {
		out := mustStmt(t, line)
		if strings.Contains(out, "sk-ant-literal-123") {
			t.Fatalf("%s printed a credential:\n%s", line, out)
		}
	}
	if out := mustStmt(t, "SHOW ACCOUNTS"); !strings.Contains(out, "auth_token from keychain:ok-glm") || !strings.Contains(out, "api_key (plaintext)") {
		t.Fatalf("SHOW ACCOUNTS:\n%s", out)
	}

	// SHOW CREATE states an account whole, and it parses back.
	text := mustStmt(t, "SHOW CREATE ACCOUNT glm")
	if !strings.Contains(text, "CREATE OR REPLACE ACCOUNT glm") || !strings.Contains(text, "auth_token FROM 'keychain:ok-glm'") {
		t.Fatalf("SHOW CREATE ACCOUNT:\n%s", text)
	}
	// A literal never prints: refused, or left out with --skip-secrets.
	if out, err := stmt(t, "SHOW CREATE ACCOUNT console"); err == nil || strings.Contains(out, "sk-ant-literal-123") || strings.Contains(err.Error(), "sk-ant-literal-123") {
		t.Fatalf("SHOW CREATE of a literal: %v\n%s", err, out)
	}
	if out := mustStmt(t, "SHOW CREATE ACCOUNT console --skip-secrets"); !strings.Contains(out, "-- ACCOUNT console left out") || strings.Contains(out, "sk-ant") {
		t.Fatalf("--skip-secrets:\n%s", out)
	}

	// The dump replays onto an empty root: accounts before the playbooks
	// that name them.
	mustStmt(t, "CREATE PLAYBOOK p SET launcher = '' USE ACCOUNT glm")
	dump := mustStmt(t, "SHOW CREATE ALL --skip-secrets")
	if strings.Index(dump, "ACCOUNT glm") > strings.Index(dump, "PLAYBOOK p") {
		t.Fatalf("an account after the playbook naming it:\n%s", dump)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if out, err := apply(t, writePlaybookFile(t, dump)); err != nil {
		t.Fatalf("replay: %v\n%s", err, out)
	}
	if a := readAccount(t, root, "glm"); a == nil || a.Auth.Ref != "keychain:ok-glm" || a.Identity != "me@example.com" {
		t.Fatalf("replayed account: %+v", a)
	}
	if got := readEnv(t, filepath.Join(root, "p")); got == nil || strings.Join(got.Accounts, ",") != "glm" {
		t.Fatalf("replayed list: %+v", got)
	}
}

func TestAccountDryRun(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CPB_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	file := writePlaybookFile(t, `CREATE OR REPLACE ACCOUNT mine SET provider = 'anthropic', auth = 'claude_login';
CREATE OR ALTER PLAYBOOK p SET launcher = '' USE ACCOUNT mine;
ALTER DEFAULTS USE ACCOUNT mine;
`)
	out, err := apply(t, file, "--dry-run")
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	if readAccount(t, root, "mine") != nil {
		t.Fatal("a dry run wrote the account")
	}
	if out, err := apply(t, file); err != nil || !strings.Contains(out, "2 created, 1 changed") {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	// Applied again it converges: nothing changes.
	if out, err := apply(t, file); err != nil || !strings.Contains(out, "0 created, 0 changed, 3 unchanged") {
		t.Fatalf("apply again: %v\n%s", err, out)
	}
}

func TestAccountRecipeRefused(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "r.cpb"), []byte("ALTER PLAYBOOK USE ACCOUNT mine;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := stmtErr(t, "APPLY "+filepath.Join(dir, "r.cpb")+" TO "+dir); err == nil {
		t.Fatal("a recipe's USE ACCOUNT applied to a directory")
	}
}

// A source never picks the accounts a playbook runs on: they are local.
func TestInstallIgnoresAccounts(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	home, _ := os.UserHomeDir()
	src := filepath.Join(home, "src")
	writeSource(t, src)
	if err := os.WriteFile(filepath.Join(src, manifest.FileName), []byte("name = \"src\"\n\n[env]\naccounts = [\"claude-work\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var err error
	stderr := captureStderr(t, func() { _, err = quotedStmt(t, "CREATE PLAYBOOK x SET launcher = '' FROM '"+src+"'") })
	if err != nil {
		t.Fatal(err)
	}
	pb, err := playbook.Require(config.ResolvePlaybooksDir(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if pb.Manifest != nil && pb.Manifest.Env != nil && len(pb.Manifest.Env.Accounts) > 0 {
		t.Fatalf("the source's accounts were installed: %v", pb.Manifest.Env.Accounts)
	}
	// Accounts live in [env], which an install never takes from a source.
	if !strings.Contains(stderr, "ignoring the [env] block shipped in the source's") {
		t.Fatalf("no note:\n%s", stderr)
	}
}
