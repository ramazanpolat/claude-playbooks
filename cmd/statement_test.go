package cmd

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envprofile"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// stmt runs one statement, written as it would be typed after "cpb".
func stmt(t *testing.T, line string) (string, error) {
	t.Helper()
	var err error
	out := captureStdout(t, func() { err = runStatement(strings.Fields(line)) })
	return out, err
}

func mustStmt(t *testing.T, line string) string {
	t.Helper()
	out, err := stmt(t, line)
	if err != nil {
		t.Fatalf("%s: %v", line, err)
	}
	return out
}

func readProfile(t *testing.T, name string) *envprofile.Profile {
	t.Helper()
	p, err := envprofile.Read(envprofile.Dir(config.ResolvePlaybooksDir()), name)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestStatementEnvLifecycle(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)

	out := mustStmt(t, "CREATE ENV glm SET ANTHROPIC_BASE_URL=http://tr0:20128/v1 MODEL=glm-5.3")
	if !strings.Contains(out, "Created ENV glm") || !strings.Contains(out, "set       MODEL") {
		t.Fatalf("create report:\n%s", out)
	}
	if strings.Contains(out, "http://tr0") {
		t.Fatalf("a value reached the report:\n%s", out)
	}
	p := readProfile(t, "glm")
	if p.Set["MODEL"] != "glm-5.3" || p.Set["ANTHROPIC_BASE_URL"] != "http://tr0:20128/v1" {
		t.Fatalf("profile after create: %#v", p)
	}

	if _, err := stmt(t, "CREATE ENV glm SET X=1"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("create over an existing set: %v", err)
	}
	if out := mustStmt(t, "create env if not exists glm set X=1"); !strings.Contains(out, "unchanged") {
		t.Fatalf("IF NOT EXISTS: %s", out)
	}
	if _, ok := readProfile(t, "glm").Set["X"]; ok {
		t.Fatal("IF NOT EXISTS changed an existing set")
	}

	mustStmt(t, "ALTER ENV glm BLOCK MODEL DESCRIBE router")
	p = readProfile(t, "glm")
	if _, ok := p.Set["MODEL"]; ok || !reflect.DeepEqual(p.Unset, []string{"MODEL"}) || p.Description != "router" {
		t.Fatalf("after BLOCK and DESCRIBE: %#v", p)
	}
	mustStmt(t, "ALTER ENV glm UNSET MODEL")
	if p = readProfile(t, "glm"); len(p.Unset) != 0 {
		t.Fatalf("after UNSET: %#v", p)
	}
	mustStmt(t, "ALTER ENV glm SET MODEL=glm-5.3-flash")
	if p = readProfile(t, "glm"); p.Set["MODEL"] != "glm-5.3-flash" {
		t.Fatalf("after SET: %#v", p)
	}

	out = mustStmt(t, "CREATE OR REPLACE ENV glm SET ONLY=1")
	p = readProfile(t, "glm")
	if !strings.Contains(out, "Replaced ENV glm") || !reflect.DeepEqual(p.Set, map[string]string{"ONLY": "1"}) || p.Description != "" {
		t.Fatalf("OR REPLACE must replace the whole set:\n%s\n%#v", out, p)
	}

	if _, err := stmt(t, "ALTER ENV ghost SET A=1"); err == nil || !strings.Contains(err.Error(), "CREATE ENV ghost") {
		t.Fatalf("ALTER of a missing set: %v", err)
	}
	mustStmt(t, "DROP ENV glm")
	if readProfile(t, "glm") != nil {
		t.Fatal("DROP ENV left the set")
	}
	if _, err := stmt(t, "DROP ENV glm"); err == nil {
		t.Fatal("DROP of a missing set succeeded")
	}
	if out := mustStmt(t, "DROP ENV IF EXISTS glm"); !strings.Contains(out, "nothing to drop") {
		t.Fatalf("IF EXISTS: %s", out)
	}
}

// A credential literal takes AS PLAINTEXT, and the report marks it; without
// it the parser refuses and nothing is written.
func TestStatementPlaintextCredential(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	if _, err := stmt(t, "CREATE ENV r SET ANTHROPIC_AUTH_TOKEN=abc123"); err == nil {
		t.Fatal("a credential literal was accepted")
	}
	if readProfile(t, "r") != nil {
		t.Fatal("a refused statement wrote the set")
	}
	out := mustStmt(t, "CREATE ENV r SET ANTHROPIC_AUTH_TOKEN=abc123 AS PLAINTEXT")
	if !strings.Contains(out, "ANTHROPIC_AUTH_TOKEN (plaintext)") || strings.Contains(out, "abc123") {
		t.Fatalf("report:\n%s", out)
	}
	if readProfile(t, "r").Set["ANTHROPIC_AUTH_TOKEN"] != "abc123" {
		t.Fatal("AS PLAINTEXT did not store the literal")
	}
}

func TestStatementDropEnvGuards(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	seedFlatPlaybook(t, "router")
	mustStmt(t, "CREATE ENV used SET A=1")
	mustStmt(t, "CREATE ENV base SET B=1")
	mustStmt(t, "ALTER PLAYBOOK router USE ENV used")
	mustStmt(t, "ALTER DEFAULTS USE ENV base")

	if _, err := stmt(t, "DROP ENV used"); err == nil || !strings.Contains(err.Error(), "used by router") {
		t.Fatalf("DROP of an attached set: %v", err)
	}
	if _, err := stmt(t, "DROP ENV base"); err == nil || !strings.Contains(err.Error(), "ALTER DEFAULTS DROP ENV base") {
		t.Fatalf("DROP of a default: %v", err)
	}
	mustStmt(t, "ALTER PLAYBOOK router DROP ENV used")
	mustStmt(t, "ALTER DEFAULTS DROP ENV base")
	mustStmt(t, "DROP ENV used")
	mustStmt(t, "DROP ENV base")
}

func TestStatementPlaybookEnvList(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	root := seedFlatPlaybook(t, "router")
	for _, n := range []string{"a", "b", "c", "d"} {
		mustStmt(t, "CREATE ENV "+n)
	}
	profiles := func() []string {
		e := readEnv(t, root)
		if e == nil {
			return nil
		}
		return e.Profiles
	}

	mustStmt(t, "ALTER PLAYBOOK router USE ENV a b")
	if got := profiles(); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("USE ENV a b: %q", got)
	}
	mustStmt(t, "ALTER PLAYBOOK router ADD ENV c FIRST")
	if got := profiles(); !reflect.DeepEqual(got, []string{"c", "a", "b"}) {
		t.Fatalf("ADD c FIRST: %q", got)
	}
	mustStmt(t, "ALTER PLAYBOOK router ADD ENV a AFTER b")
	if got := profiles(); !reflect.DeepEqual(got, []string{"c", "b", "a"}) {
		t.Fatalf("ADD a AFTER b (moves it): %q", got)
	}
	mustStmt(t, "ALTER PLAYBOOK router ADD ENV d BEFORE b")
	if got := profiles(); !reflect.DeepEqual(got, []string{"c", "d", "b", "a"}) {
		t.Fatalf("ADD d BEFORE b: %q", got)
	}
	mustStmt(t, "ALTER PLAYBOOK router ADD ENV d")
	if got := profiles(); !reflect.DeepEqual(got, []string{"c", "b", "a", "d"}) {
		t.Fatalf("ADD d (LAST): %q", got)
	}

	before := profiles()
	for _, bad := range []string{
		"ALTER PLAYBOOK router ADD ENV a AFTER zzz",
		"ALTER PLAYBOOK router USE ENV a ghost",
		"ALTER PLAYBOOK router ADD ENV ghost",
		"ALTER PLAYBOOK router SET VAR OK=1 USE ENV ghost",
	} {
		if _, err := stmt(t, bad); err == nil {
			t.Fatalf("%s: accepted", bad)
		}
		if got := profiles(); !reflect.DeepEqual(got, before) {
			t.Fatalf("%s: wrote %q", bad, got)
		}
	}
	if _, ok := readEnv(t, root).Set["OK"]; ok {
		t.Fatal("a refused statement applied its other clause")
	}

	out := mustStmt(t, "ALTER PLAYBOOK router DROP ENV c zzz")
	if !strings.Contains(out, "zzz was not attached") || !strings.Contains(out, "env sets  b, a, d") {
		t.Fatalf("DROP report:\n%s", out)
	}
	mustStmt(t, "ALTER PLAYBOOK router DROP ENV b a d")
	if e := readEnv(t, root); !e.Empty() {
		t.Fatalf("an emptied block must leave no [env]: %#v", e)
	}
}

func TestStatementPlaybookVars(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	root := seedFlatPlaybook(t, "router")

	mustStmt(t, "ALTER PLAYBOOK router SET VAR MAX_THINKING_TOKENS=8000 MODEL=x BLOCK VAR HTTP_PROXY")
	e := readEnv(t, root)
	if e.Set["MAX_THINKING_TOKENS"] != "8000" || e.Set["MODEL"] != "x" || !reflect.DeepEqual(e.Unset, []string{"HTTP_PROXY"}) {
		t.Fatalf("after SET and BLOCK: %#v", e)
	}
	mustStmt(t, "ALTER PLAYBOOK router BLOCK VAR MODEL UNSET VAR HTTP_PROXY")
	e = readEnv(t, root)
	if _, ok := e.Set["MODEL"]; ok || !reflect.DeepEqual(e.Unset, []string{"MODEL"}) {
		t.Fatalf("BLOCK moves a key from set to unset; UNSET forgets: %#v", e)
	}
	out := mustStmt(t, "ALTER PLAYBOOK router BLOCK VAR CLAUDE_CODE_OAUTH_TOKEN")
	if !strings.Contains(out, "authenticates from stored credentials") {
		t.Fatalf("OAuth note missing:\n%s", out)
	}
	if _, err := stmt(t, "ALTER PLAYBOOK ghost SET VAR A=1"); err == nil {
		t.Fatal("ALTER of a missing playbook succeeded")
	}
}

func TestStatementRefusedOnLinkedPlaybook(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	target := t.TempDir()
	if err := manifest.Write(target, &manifest.Manifest{Name: "ext"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(config.ResolvePlaybooksDir(), "ext")); err != nil {
		t.Fatal(err)
	}
	if _, err := stmt(t, "ALTER PLAYBOOK ext SET VAR A=1"); err == nil || !strings.Contains(err.Error(), "linked") {
		t.Fatalf("linked mutation not refused: %v", err)
	}
	if e := readEnv(t, target); !e.Empty() {
		t.Fatalf("shared manifest was mutated: %#v", e)
	}
}

func TestStatementDefaults(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	dir := envprofile.Dir(config.ResolvePlaybooksDir())
	for _, n := range []string{"a", "b", "c"} {
		mustStmt(t, "CREATE ENV "+n)
	}
	defaults := func() string {
		d, err := envprofile.Defaults(dir)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(d, ",")
	}

	mustStmt(t, "ALTER DEFAULTS USE ENV a b")
	if got := defaults(); got != "a,b" {
		t.Fatalf("USE ENV a b: %q", got)
	}
	mustStmt(t, "ALTER DEFAULTS ADD ENV c BEFORE b")
	if got := defaults(); got != "a,c,b" {
		t.Fatalf("ADD c BEFORE b: %q", got)
	}
	mustStmt(t, "ALTER DEFAULTS DROP ENV a")
	if got := defaults(); got != "c,b" {
		t.Fatalf("DROP a: %q", got)
	}
	mustStmt(t, "ALTER DEFAULTS DROP ENV c b")
	if _, err := os.Stat(filepath.Join(dir, envprofile.DefaultMarker)); !os.IsNotExist(err) {
		t.Fatalf("an empty DEFAULTS must remove the marker: %v", err)
	}

	// A broken marker refuses an edit, but USE ENV replaces it outright.
	if err := os.WriteFile(filepath.Join(dir, envprofile.DefaultMarker), []byte("not a name!\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := stmt(t, "ALTER DEFAULTS ADD ENV a"); err == nil || !strings.Contains(err.Error(), "USE ENV") {
		t.Fatalf("ADD over a broken marker: %v", err)
	}
	mustStmt(t, "ALTER DEFAULTS USE ENV a")
	if got := defaults(); got != "a" {
		t.Fatalf("USE over a broken marker: %q", got)
	}

	// Removing works while a profile is broken: that is when it is needed.
	mustStmt(t, "ALTER DEFAULTS USE ENV a b")
	if err := os.WriteFile(filepath.Join(dir, "a.toml"), []byte("= [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustStmt(t, "ALTER DEFAULTS DROP ENV a")
	if got := defaults(); got != "b" {
		t.Fatalf("DROP with a broken profile: %q", got)
	}
}

// Statements whose phase has not landed are refused before any write.
func TestStatementNotYet(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	root := seedFlatPlaybook(t, "router")
	mustStmt(t, "CREATE ENV e")
	for _, line := range []string{
		"SHOW CREATE ALL",
		"CREATE PLAYBOOK fresh",
		"DROP PLAYBOOK router",
		"ALTER PLAYBOOK router SET VAR A=1 ALIAS r2",
		"ALTER PLAYBOOK router SET VAR A=1 RENAME TO r2",
		"ALTER PLAYBOOK router SET VAR TOKEN FROM keychain:x",
		"ALTER ENV e SET TOKEN FROM keychain:x",
		"ALTER DEFAULTS SET SECRET HELPER h",
		"APPLY setup.cpb",
	} {
		if _, err := stmt(t, line); err == nil || !strings.Contains(err.Error(), "not implemented yet") {
			t.Errorf("%s: %v", line, err)
		}
	}
	if e := readEnv(t, root); !e.Empty() {
		t.Fatalf("a refused statement wrote the manifest: %#v", e)
	}
}

func TestStatementArgs(t *testing.T) {
	resetCommandTestState(t)
	cases := []struct {
		args     []string
		want     []string
		ok       bool
		playbook string
	}{
		{words("alter playbook k USE ENV a"), words("alter playbook k USE ENV a"), true, ""},
		{words("--playbooks-dir /x ALTER ENV e SET A=1"), words("ALTER ENV e SET A=1"), true, "/x"},
		{words("--playbooks-dir=/y --launcher-dir /l SHOW ENVS"), words("SHOW ENVS"), true, "/y"},
		{words("ALTER PLAYBOOK k SET VAR OPTS=-v"), words("ALTER PLAYBOOK k SET VAR OPTS=-v"), true, ""},
		{words("create playbook x"), words("create playbook x"), true, ""},
		{words("create x --alias y"), nil, false, ""}, // the hidden pre-grammar create
		{words("--playbooks-dir /z list"), nil, false, ""},
		{words("env k set A=1"), nil, false, ""},
		{words("--playbooks-dir"), nil, false, ""},
		{nil, nil, false, ""},
	}
	for _, tc := range cases {
		config.PlaybooksDir = ""
		got, ok := statementArgs(tc.args)
		if ok != tc.ok || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("statementArgs(%q) = %q, %v; want %q, %v", tc.args, got, ok, tc.want, tc.ok)
		}
		if config.PlaybooksDir != tc.playbook {
			t.Errorf("statementArgs(%q): playbooks dir %q, want %q (flags apply only to a statement)", tc.args, config.PlaybooksDir, tc.playbook)
		}
	}
}

func words(s string) []string { return strings.Fields(s) }

// In a subdir layout the launch reads the nested manifest; a set it names
// must not be dropped, by the statement or by the hidden env-profile.
func TestDropEnvSeesTheGoverningManifest(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	root := seedFlatPlaybook(t, "nested")
	if err := os.WriteFile(filepath.Join(root, ".playbook"), []byte("version = \"0.1.0\"\nname = \"nested\"\nsubdir = \"config\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Join(root, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustStmt(t, "CREATE ENV inner")
	if err := manifest.Write(configDir, &manifest.Manifest{Name: "nested", Env: &manifest.Env{Profiles: []string{"inner"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := stmt(t, "DROP ENV inner"); err == nil || !strings.Contains(err.Error(), "used by nested") {
		t.Fatalf("DROP ENV of a set the governing manifest uses: %v", err)
	}
	if err := runEnvProfile(nil, []string{"inner", "delete"}); err == nil || !strings.Contains(err.Error(), "nested") {
		t.Fatalf("hidden env-profile delete of a set the governing manifest uses: %v", err)
	}
	if readProfile(t, "inner") == nil {
		t.Fatal("the set was deleted")
	}
}

// The hidden env-profile keeps its singleton wording byte for byte.
func TestHiddenDeleteOfTheSingleDefaultKeepsItsWording(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	mustStmt(t, "CREATE ENV base")
	mustStmt(t, "ALTER DEFAULTS USE ENV base")
	err := runEnvProfile(nil, []string{"base", "delete"})
	want := `env profile "base" is the registry default; clear it first with 'claude-playbook env-profile base undefault'`
	if err == nil || err.Error() != want {
		t.Fatalf("got %v\nwant %s", err, want)
	}
	mustStmt(t, "CREATE ENV other")
	err = runEnvProfile(nil, []string{"other", "undefault"})
	want = `the registry default is "base", not "other"`
	if err == nil || err.Error() != want {
		t.Fatalf("undefault of a non-default: got %v\nwant %s", err, want)
	}
}
