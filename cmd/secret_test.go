package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/envprofile"
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
	t.Setenv(envprofile.SecretHelperEnv, "")
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
	t.Setenv(envprofile.SecretHelperEnv, "")
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
	mustStmt(t, "ALTER DEFAULTS SET SECRET HELPER "+helper)
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

// Keys whose value cpb itself reads never take a reference, at any layer.
func TestOAuthTokenReferenceRefusedAtEveryLayer(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	helper, _ := fakeHelper(t)
	root := seedFlatPlaybook(t, "router")
	mustStmt(t, "ALTER DEFAULTS SET SECRET HELPER "+helper)
	mustStmt(t, "CREATE ENV e")
	for _, line := range []string{
		"ALTER ENV e SET CLAUDE_CODE_OAUTH_TOKEN FROM keychain:ok/x",
		"ALTER PLAYBOOK router SET VAR CLAUDE_CODE_OAUTH_TOKEN FROM keychain:ok/x",
	} {
		if _, err := stmt(t, line); err == nil || !strings.Contains(err.Error(), "cannot be a secret reference") {
			t.Errorf("%s: %v", line, err)
		}
	}
	// A hand-written reference is refused when the file is read or written,
	// so no launch runs with it.
	if err := envprofile.Write(envprofile.Dir(filepath.Dir(root)), &envprofile.Profile{Name: "x",
		Refs: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "keychain:ok/x"}}); err == nil {
		t.Error("an env set file took a reference for the OAuth token")
	}
	if err := manifest.Write(root, &manifest.Manifest{Name: "router",
		Env: &manifest.Env{Refs: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "keychain:ok/x"}}}); err == nil {
		t.Error("a manifest took a reference for the OAuth token")
	}
}

func TestLaunchExecsThroughTheHelper(t *testing.T) {
	root := sandboxRoot(t, "pbs")
	writePlaybook(t, root, "router", &manifest.Manifest{IsolateAuth: true})
	helper, helperLog := fakeHelper(t)
	claudeLog := stubClaude(t)
	mustStmt(t, "ALTER DEFAULTS SET SECRET HELPER "+helper)
	mustStmt(t, "CREATE ENV r SET BASE=http://tr0/v1")
	mustStmt(t, "ALTER ENV r SET ANTHROPIC_AUTH_TOKEN FROM keychain:ok/router")
	mustStmt(t, "ALTER PLAYBOOK router USE ENV r")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "stale-from-the-shell")

	if err := runRun(nil, []string{"router", "--version"}); err != nil {
		t.Fatal(err)
	}
	got := readLog(t, claudeLog)
	if !strings.Contains(got, "ARGS --version") || !strings.Contains(got, "ANTHROPIC_AUTH_TOKEN=resolved:keychain:ok/router") || !strings.Contains(got, "BASE=http://tr0/v1") {
		t.Fatalf("claude did not get the resolved value:\n%s", got)
	}
	if strings.Contains(got, "stale-from-the-shell") {
		t.Fatalf("the shell's value reached claude:\n%s", got)
	}
	if h := readLog(t, helperLog); !strings.Contains(h, "ANTHROPIC_AUTH_TOKEN=keychain:ok/router -- ") || !strings.Contains(h, " --version") {
		t.Fatalf("helper argv:\n%s", h)
	}

	// Without a helper the launch is refused, and claude never runs.
	mustStmt(t, "ALTER DEFAULTS UNSET SECRET HELPER")
	os.Remove(claudeLog)
	err := runRun(nil, []string{"router", "--version"})
	if err == nil || !strings.Contains(err.Error(), "no secret helper configured") || !strings.Contains(err.Error(), "ANTHROPIC_AUTH_TOKEN") {
		t.Fatalf("launch without a helper: %v", err)
	}
	if _, serr := os.Stat(claudeLog); serr == nil {
		t.Fatal("claude ran without its secret")
	}

	// CPB_SECRET_HELPER alone is enough.
	t.Setenv(envprofile.SecretHelperEnv, helper)
	if err := runRun(nil, []string{"router", "--version"}); err != nil {
		t.Fatalf("launch with CPB_SECRET_HELPER: %v", err)
	}
}

func TestSandboxedLaunchRefusesReferences(t *testing.T) {
	root := sandboxRoot(t, "pbs")
	writePlaybook(t, root, "boxed", &manifest.Manifest{IsolateAuth: true, Sandbox: &manifest.Sandbox{Always: true}})
	helper, _ := fakeHelper(t)
	stubSbx(t)
	mustStmt(t, "ALTER DEFAULTS SET SECRET HELPER "+helper)
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
	mustStmt(t, "ALTER DEFAULTS SET SECRET HELPER "+helper)
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
	t.Setenv(envprofile.SecretHelperEnv, "other-helper")
	if out := mustStmt(t, "SHOW DEFAULTS"); !strings.Contains(out, "other-helper (from CPB_SECRET_HELPER)") {
		t.Fatalf("SHOW DEFAULTS with the override:\n%s", out)
	}
}

// The hidden env command keeps its own code path: it preserves references
// for other keys, and a literal it sets replaces a reference for the same
// key, so the manifest stays valid.
func TestHiddenEnvCommandAndReferences(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	helper, _ := fakeHelper(t)
	root := seedFlatPlaybook(t, "router")
	mustStmt(t, "ALTER DEFAULTS SET SECRET HELPER "+helper)
	mustStmt(t, "ALTER PLAYBOOK router SET VAR A_TOKEN FROM keychain:ok/a SET VAR B_TOKEN FROM keychain:ok/b")
	if err := runEnv(nil, []string{"router", "set", "OTHER=1"}); err != nil {
		t.Fatal(err)
	}
	if e := readEnv(t, root); e.Refs["A_TOKEN"] != "keychain:ok/a" || e.Refs["B_TOKEN"] != "keychain:ok/b" {
		t.Fatalf("the hidden env command dropped references: %#v", e)
	}
	if err := runEnv(nil, []string{"router", "set", "A_TOKEN=literal"}); err != nil {
		t.Fatal(err)
	}
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
	t.Setenv(envprofile.SecretHelperEnv, "")
	t.Setenv("TOKEN", "stale-shell-value")
	mustStmt(t, "ALTER DEFAULTS SET SECRET HELPER "+helper)

	mustStmt(t, "CREATE ENV e SET TOKEN FROM keychain:x")
	if got := readLog(t, log); got != "TOKEN=unset\n" {
		t.Fatalf("the check helper saw the shell's value: %q", got)
	}
	mustStmt(t, "CREATE ENV IF NOT EXISTS e SET TOKEN FROM keychain:y")
	if got := readLog(t, log); got != "TOKEN=unset\n" {
		t.Fatalf("IF NOT EXISTS on an existing set asked the helper: %q", got)
	}
}
