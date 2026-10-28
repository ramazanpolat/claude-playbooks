package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/envset"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// fakeHelper installs a secret helper that follows cpb's interface:
// --check succeeds for keychain:ok… references and exits 4 otherwise; in
// exec mode it sets each K to "resolved:<ref>" and execs the command. It
// logs its arguments, which carry references and never values. Returns
// the helper's absolute path and its log.
func fakeHelper(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "helper.log")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$HELPER_LOG"
if [ "$1" = "--check" ]; then
  case "$2" in
    *=keychain:ok*) echo present; exit 0 ;;
    *) echo MISSING >&2; exit 4 ;;
  esac
fi
while [ $# -gt 0 ] && [ "$1" != "--" ]; do
  k=${1%%=*}; r=${1#*=}
  export "$k=resolved:$r"
  shift
done
shift
exec "$@"
`
	path := filepath.Join(dir, "my-helper")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HELPER_LOG", log)
	t.Setenv(envset.SecretHelperEnv, "")
	return path, log
}

func readLog(t *testing.T, path string) string {
	t.Helper()
	data, _ := os.ReadFile(path)
	return string(data)
}

func TestSetFromNeedsAHelper(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	t.Setenv(envset.SecretHelperEnv, "")
	mustStmt(t, "CREATE ENV e")
	if _, err := stmt(t, "ALTER ENV e SET TOKEN FROM keychain:ok/x"); err == nil || !strings.Contains(err.Error(), "no secret helper configured") {
		t.Fatalf("SET FROM without a helper: %v", err)
	}
	if p := readProfile(t, "e"); len(p.Refs) != 0 {
		t.Fatalf("a refused SET FROM wrote: %#v", p.Refs)
	}
}

func TestSetFromChecksTheReference(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	helper, log := fakeHelper(t)
	root := seedFlatPlaybook(t, "router")
	mustStmt(t, "ALTER DEFAULTS SET secret_helper = "+helper)
	mustStmt(t, "CREATE ENV e SET TOKEN=literal AS PLAINTEXT")

	out := mustStmt(t, "ALTER ENV e SET TOKEN FROM keychain:ok/router")
	p := readProfile(t, "e")
	if p.Refs["TOKEN"] != "keychain:ok/router" || p.Set["TOKEN"] != "" {
		t.Fatalf("a reference replaces the literal: %#v", p)
	}
	if !strings.Contains(out, "ref       TOKEN <from keychain:ok/router>") {
		t.Fatalf("report:\n%s", out)
	}
	if !strings.Contains(readLog(t, log), "--check TOKEN=keychain:ok/router") {
		t.Fatalf("helper not asked:\n%s", readLog(t, log))
	}

	if _, err := stmt(t, "ALTER PLAYBOOK router SET VAR API_TOKEN FROM keychain:gone"); err == nil || !strings.Contains(err.Error(), "could not resolve") {
		t.Fatalf("an unresolvable reference was accepted: %v", err)
	}
	if e := readEnv(t, root); !e.Empty() {
		t.Fatalf("a refused SET FROM wrote the manifest: %#v", e)
	}

	// UNSET forgets a reference; BLOCK replaces it.
	mustStmt(t, "ALTER ENV e UNSET TOKEN")
	if p := readProfile(t, "e"); len(p.Refs) != 0 {
		t.Fatalf("UNSET kept the reference: %#v", p)
	}
}

// An account holding a Claude token by reference (v4.2.0): the launch
// execs through the helper, which sets the token, and a token exported in
// the shell never stands in for it. On the isolated path too. --account
// picks another account the playbook lists.
func TestOAuthTokenByReference(t *testing.T) {
	root := sandboxRoot(t, "pbs")
	writePlaybook(t, root, "work", &manifest.Manifest{})
	writePlaybook(t, root, "iso", &manifest.Manifest{IsolatedLogin: true})
	helper, helperLog := fakeHelper(t)
	claudeLog := stubClaude(t)
	mustStmt(t, "ALTER DEFAULTS SET secret_helper = "+helper)
	mustStmt(t, "CREATE ACCOUNT acct-work SET provider=anthropic, claude_token FROM keychain:ok/work")
	mustStmt(t, "CREATE ACCOUNT acct-iso SET provider=anthropic, claude_token FROM keychain:ok/iso")
	mustStmt(t, "ALTER PLAYBOOK work USE ACCOUNT acct-work, acct-iso")
	mustStmt(t, "ALTER PLAYBOOK iso USE ACCOUNT acct-iso")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "stale-from-the-shell")

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"work", "--version"}, "keychain:ok/work"},
		{[]string{"iso", "--version"}, "keychain:ok/iso"},
		{[]string{"--account", "acct-iso", "work", "--version"}, "keychain:ok/iso"},
	} {
		os.Remove(claudeLog)
		os.Remove(helperLog)
		if err := runRun(nil, tc.args); err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		got := readLog(t, claudeLog)
		if !strings.Contains(got, "CLAUDE_CODE_OAUTH_TOKEN=resolved:"+tc.want) {
			t.Fatalf("%v: claude did not get the token from the helper:\n%s", tc.args, got)
		}
		if strings.Contains(got, "stale-from-the-shell") {
			t.Fatalf("%v: the shell's token reached claude:\n%s", tc.args, got)
		}
		if h := readLog(t, helperLog); !strings.Contains(h, "CLAUDE_CODE_OAUTH_TOKEN="+tc.want+" -- ") {
			t.Fatalf("%v: helper argv:\n%s", tc.args, h)
		}
	}
	// An account the playbook does not list is refused, before claude runs.
	os.Remove(claudeLog)
	if err := runRun(nil, []string{"--account", "acct-work", "iso", "--version"}); err == nil || !strings.Contains(err.Error(), "runs only on the accounts it lists") {
		t.Fatalf("an unlisted account: %v", err)
	}
	if _, err := os.Stat(claudeLog); err == nil {
		t.Fatal("claude ran on an unlisted account")
	}
	// The file holds the reference, never a value.
	if a := readAccount(t, root, "acct-work"); a.Auth.Ref != "keychain:ok/work" || a.Auth.Value != "" {
		t.Fatalf("account: %+v", a.Auth)
	}
}

func TestLaunchExecsThroughTheHelper(t *testing.T) {
	root := sandboxRoot(t, "pbs")
	writePlaybook(t, root, "router", &manifest.Manifest{IsolatedLogin: true})
	helper, helperLog := fakeHelper(t)
	claudeLog := stubClaude(t)
	mustStmt(t, "ALTER DEFAULTS SET secret_helper = "+helper)
	mustStmt(t, "CREATE ENV r SET BASE=http://buildbox/v1")
	mustStmt(t, "CREATE ACCOUNT router SET provider=gateway, base_url=http://localhost:4000/v1, auth_token FROM keychain:ok/router")
	mustStmt(t, "ALTER PLAYBOOK router USE ENV r USE ACCOUNT router")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "stale-from-the-shell")

	if err := runRun(nil, []string{"router", "--version"}); err != nil {
		t.Fatal(err)
	}
	got := readLog(t, claudeLog)
	if !strings.Contains(got, "ARGS --version") || !strings.Contains(got, "ANTHROPIC_AUTH_TOKEN=resolved:keychain:ok/router") || !strings.Contains(got, "BASE=http://buildbox/v1") || !strings.Contains(got, "ANTHROPIC_BASE_URL=http://localhost:4000/v1") {
		t.Fatalf("claude did not get the resolved value:\n%s", got)
	}
	if strings.Contains(got, "stale-from-the-shell") {
		t.Fatalf("the shell's value reached claude:\n%s", got)
	}
	if h := readLog(t, helperLog); !strings.Contains(h, "ANTHROPIC_AUTH_TOKEN=keychain:ok/router -- ") || !strings.Contains(h, " --version") {
		t.Fatalf("helper argv:\n%s", h)
	}

	// Without a helper the launch is refused, and claude never runs.
	mustStmt(t, "ALTER DEFAULTS RESET secret_helper")
	os.Remove(claudeLog)
	err := runRun(nil, []string{"router", "--version"})
	if err == nil || !strings.Contains(err.Error(), "no secret helper configured") || !strings.Contains(err.Error(), "ANTHROPIC_AUTH_TOKEN") {
		t.Fatalf("launch without a helper: %v", err)
	}
	if _, serr := os.Stat(claudeLog); serr == nil {
		t.Fatal("claude ran without its secret")
	}

	// CPB_SECRET_HELPER alone is enough.
	t.Setenv(envset.SecretHelperEnv, helper)
	if err := runRun(nil, []string{"router", "--version"}); err != nil {
		t.Fatalf("launch with CPB_SECRET_HELPER: %v", err)
	}
}

func TestSandboxedLaunchRefusesReferences(t *testing.T) {
	root := sandboxRoot(t, "pbs")
	writePlaybook(t, root, "boxed", &manifest.Manifest{IsolatedLogin: true, Sandbox: &manifest.Sandbox{Always: true}})
	helper, _ := fakeHelper(t)
	stubSbx(t)
	mustStmt(t, "ALTER DEFAULTS SET secret_helper = "+helper)
	mustStmt(t, "ALTER PLAYBOOK boxed SET VAR API_TOKEN FROM keychain:ok/x")
	err := runRun(nil, []string{"--workdir", t.TempDir(), "boxed", "--version"})
	if err == nil || !strings.Contains(err.Error(), "cannot resolve them yet") {
		t.Fatalf("sandboxed launch with references: %v", err)
	}
}

func TestShowAndExplainReferences(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	helper, _ := fakeHelper(t)
	seedFlatPlaybook(t, "router")
	mustStmt(t, "ALTER DEFAULTS SET secret_helper = "+helper)
	mustStmt(t, "ALTER PLAYBOOK router SET VAR API_TOKEN FROM keychain:ok/x")

	var pb struct {
		Vars []map[string]any `json:"vars"`
	}
	if err := json.Unmarshal([]byte(mustStmt(t, "SHOW PLAYBOOK router --json")), &pb); err != nil ||
		len(pb.Vars) != 1 || pb.Vars[0]["ref"] != "keychain:ok/x" || pb.Vars[0]["value"] != nil {
		t.Fatalf("SHOW PLAYBOOK --json: %+v %v", pb, err)
	}
	var ex struct {
		SecretHelper *struct {
			Command string `json:"command"`
			From    string `json:"from"`
		} `json:"secret_helper"`
	}
	if err := json.Unmarshal([]byte(mustStmt(t, "EXPLAIN PLAYBOOK router --json")), &ex); err != nil ||
		ex.SecretHelper == nil || ex.SecretHelper.Command != helper || ex.SecretHelper.From != "setting" {
		t.Fatalf("EXPLAIN --json helper: %+v %v", ex.SecretHelper, err)
	}
	human := mustStmt(t, "EXPLAIN PLAYBOOK router")
	if !strings.Contains(human, "<from keychain:ok/x>") || !strings.Contains(human, "Secret helper: "+helper+" (from setting)") {
		t.Fatalf("EXPLAIN:\n%s", human)
	}
	t.Setenv(envset.SecretHelperEnv, "other-helper")
	if out := mustStmt(t, "SHOW DEFAULTS"); !strings.Contains(out, "other-helper (from CPB_SECRET_HELPER)") {
		t.Fatalf("SHOW DEFAULTS with the override:\n%s", out)
	}
}

// SET VAR of one key keeps the references for other keys, and a literal
// replaces a reference for the same key, so the manifest stays valid.
func TestSetVarAndReferences(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	helper, _ := fakeHelper(t)
	root := seedFlatPlaybook(t, "router")
	mustStmt(t, "ALTER DEFAULTS SET secret_helper = "+helper)
	mustStmt(t, "ALTER PLAYBOOK router SET VAR A_TOKEN FROM keychain:ok/a SET VAR B_TOKEN FROM keychain:ok/b")
	mustStmt(t, "ALTER PLAYBOOK router SET VAR OTHER=1")
	if e := readEnv(t, root); e.Refs["A_TOKEN"] != "keychain:ok/a" || e.Refs["B_TOKEN"] != "keychain:ok/b" {
		t.Fatalf("SET VAR dropped references: %#v", e)
	}
	mustStmt(t, "ALTER PLAYBOOK router SET VAR A_TOKEN=literal AS PLAINTEXT")
	if e := readEnv(t, root); e.Set["A_TOKEN"] != "literal" || e.Refs["A_TOKEN"] != "" || e.Refs["B_TOKEN"] == "" {
		t.Fatalf("set over a reference: %#v", e)
	}
}

// The check helper never sees a value the shell exports under the key it
// is asked about; and CREATE ENV IF NOT EXISTS on an existing set asks it
// nothing, since nothing is written.
func TestCheckHelperEnvAndIfNotExists(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	dir := t.TempDir()
	log := filepath.Join(dir, "helper.log")
	helper := filepath.Join(dir, "env-helper")
	script := "#!/bin/sh\nprintf 'TOKEN=%s\\n' \"${TOKEN-unset}\" >> \"$HELPER_LOG\"\nexit 0\n"
	if err := os.WriteFile(helper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HELPER_LOG", log)
	t.Setenv(envset.SecretHelperEnv, "")
	t.Setenv("TOKEN", "stale-shell-value")
	mustStmt(t, "ALTER DEFAULTS SET secret_helper = "+helper)

	mustStmt(t, "CREATE ENV e SET TOKEN FROM keychain:x")
	if got := readLog(t, log); got != "TOKEN=unset\n" {
		t.Fatalf("the check helper saw the shell's value: %q", got)
	}
	mustStmt(t, "CREATE ENV IF NOT EXISTS e SET TOKEN FROM keychain:y")
	if got := readLog(t, log); got != "TOKEN=unset\n" {
		t.Fatalf("IF NOT EXISTS on an existing set asked the helper: %q", got)
	}
}
