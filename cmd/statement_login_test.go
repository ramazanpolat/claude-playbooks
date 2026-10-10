package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

func isolateAuthOf(t *testing.T, name string) bool {
	t.Helper()
	m, err := manifest.Read(filepath.Join(config.ResolvePlaybooksDir(), name))
	if err != nil {
		t.Fatal(err)
	}
	return m != nil && m.IsolatedLogin
}

// The login property: isolated_login without a sandbox. CREATE … SET
// login = 'isolated' and ALTER … SET login = 'isolated' record it and
// drop the link to the shared login at once; 'shared' is refused on a
// sandboxed playbook and while the playbook holds a login of its own. SHOW, EXPLAIN, SELECT and SHOW CREATE (round
// trip) read it.
func TestIsolatedLogin(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	home, _ := os.UserHomeDir()
	shared := filepath.Join(home, ".claude", ".credentials.json")
	if err := os.MkdirAll(filepath.Dir(shared), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shared, []byte(`{"claudeAiOauth":{"accessToken":"shared"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	mustStmt(t, "CREATE PLAYBOOK a SET launcher = '' SET login=isolated")
	if !isolateAuthOf(t, "a") {
		t.Fatal("CREATE … SET login = 'isolated' did not record isolated_login")
	}
	var v struct {
		Sandbox struct {
			Always bool `json:"always"`
		} `json:"sandbox"`
		Login string `json:"login"`
	}
	if err := json.Unmarshal([]byte(mustStmt(t, "SHOW PLAYBOOK a --json")), &v); err != nil || v.Login != "isolated" || v.Sandbox.Always {
		t.Fatalf("SHOW --json: %v %+v", err, v)
	}

	// A shared playbook: SET detaches it now, a repeat changes nothing.
	root := seedFlatPlaybook(t, "k")
	link := filepath.Join(root, ".credentials.json")
	if err := os.Symlink(shared, link); err != nil {
		t.Fatal(err)
	}
	if out := mustStmt(t, "ALTER PLAYBOOK k SET login=isolated"); !strings.Contains(out, "isolated") {
		t.Fatalf("MODIFY report:\n%s", out)
	}
	if !isolateAuthOf(t, "k") {
		t.Fatal("SET login = 'isolated' did not record isolated_login")
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatalf("the link to the shared login is still there: %v", err)
	}
	if data, _ := os.ReadFile(shared); !strings.Contains(string(data), `"shared"`) {
		t.Fatal("the shared store changed")
	}
	if out := mustStmt(t, "ALTER PLAYBOOK k SET login=isolated"); !strings.Contains(out, "unchanged") {
		t.Fatalf("a repeat:\n%s", out)
	}
	if out := mustStmt(t, "EXPLAIN PLAYBOOK k"); strings.Count(out, "Login:") != 1 || !strings.Contains(out, "Login: isolated, shares nothing with ~/.claude: no link to its login and no machine token; /login once in it") {
		t.Fatalf("EXPLAIN: one Login line:\n%s", out)
	}
	if out := mustStmt(t, "SHOW PLAYBOOK k"); !strings.Contains(out, "isolated (shares nothing with ~/.claude)") {
		t.Fatalf("SHOW:\n%s", out)
	}
	var rows []map[string]any
	js := mustStmt(t, "SELECT name, login FROM PLAYBOOKS --json")
	if json.Unmarshal([]byte(js), &rows) != nil || len(rows) != 2 || rows[1]["name"] != "k" || rows[1]["login"] != "isolated" {
		t.Fatalf("SELECT: %s", js)
	}
	created := mustStmt(t, "SHOW CREATE PLAYBOOK k")
	if !strings.Contains(created, "CREATE OR ALTER PLAYBOOK k\n  SET launcher = '', login = 'isolated', memory = 'shared', sandbox.always = false;") {
		t.Fatalf("SHOW CREATE:\n%s", created)
	}
	if out, err := apply(t, writePlaybookFile(t, created)); err != nil || !strings.Contains(out, " 0 created, 0 changed,") {
		t.Fatalf("SHOW CREATE did not re-apply unchanged: %v\n%s", err, out)
	}

	// A login of its own blocks UNSET: a shared launch would copy it over
	// the machine's login.
	own := `{"claudeAiOauth":{"accessToken":"own"}}`
	if err := os.WriteFile(link, []byte(own), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := quotedStmt(t, "ALTER PLAYBOOK k SET login = 'shared'"); err == nil || !strings.Contains(err.Error(), "has a login of its own") || strings.Contains(err.Error(), "own\"") {
		t.Fatalf("login = 'shared' with an own login: %v", err)
	}
	if !isolateAuthOf(t, "k") {
		t.Fatal("a refused UNSET changed the manifest")
	}
	if data, _ := os.ReadFile(link); string(data) != own {
		t.Fatal("a refused UNSET touched the playbook's login")
	}
	// Logged out (a store without a grant): UNSET goes through, and the
	// link comes back at the next launch, not now.
	if err := os.WriteFile(link, []byte(`{"mcpOAuth":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	mustStmt(t, "ALTER PLAYBOOK k SET login=shared")
	if isolateAuthOf(t, "k") {
		t.Fatal("login = 'shared' left isolated_login")
	}
	if created := mustStmt(t, "SHOW CREATE PLAYBOOK k"); !strings.Contains(created, "login = 'shared', memory = 'shared'") {
		t.Fatalf("SHOW CREATE after login = 'shared':\n%s", created)
	}

	// A sandboxed playbook is isolated: sharing its login is refused and
	// SHOW CREATE does not repeat it.
	mustStmt(t, "CREATE PLAYBOOK s SET launcher = '', sandbox.always = true, login = isolated")
	if _, err := quotedStmt(t, "ALTER PLAYBOOK s DELETE login"); err == nil || !strings.Contains(err.Error(), "always runs in a sandbox") {
		t.Fatalf("DELETE login on a sandboxed playbook: %v", err)
	}
	if created := mustStmt(t, "SHOW CREATE PLAYBOOK s"); !strings.Contains(created, "login = 'isolated', memory = 'isolated', sandbox.always = true") {
		t.Fatalf("SHOW CREATE of a sandboxed playbook:\n%s", created)
	}

	for line, want := range map[string]string{
		"CREATE PLAYBOOK c LINK /x SET login = 'isolated'":              "does not apply to LINK",
		"ALTER PLAYBOOK k SET login = 'isolated' DELETE login":          "login is named twice in one statement",
		"ALTER PLAYBOOK k SET ISOLATED LOGIN":                           "ISOLATED LOGIN is a property now: SET login = 'isolated'",
		"CREATE PLAYBOOK c SET login = 'isolated' SET login = 'shared'": "login is named twice in one statement",
	} {
		if _, err := quotedStmt(t, line); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", line, err, want)
		}
	}
}

// A dry run judges the login setting from the state its earlier statements
// leave, and writes nothing.
func TestIsolatedLoginDryRun(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	f := writePlaybookFile(t, "CREATE PLAYBOOK d SET launcher = '', sandbox.always = true, login = 'isolated';\nALTER PLAYBOOK d SET login = 'shared';\n")
	if _, err := apply(t, f, "--dry-run"); err == nil || !strings.Contains(err.Error(), "always runs in a sandbox") {
		t.Fatalf("dry run, UNSET on a sandboxed playbook: %v", err)
	}
	f = writePlaybookFile(t, "CREATE PLAYBOOK e SET launcher = '';\nALTER PLAYBOOK e SET login = 'isolated';\nALTER PLAYBOOK e SET memory = 'isolated';\n")
	out, err := apply(t, f, "--dry-run")
	if err != nil || !strings.Contains(out, "1 created, 1 changed, 1 unchanged") {
		t.Fatalf("dry run:\n%v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(config.ResolvePlaybooksDir(), "e")); !os.IsNotExist(err) {
		t.Fatalf("the dry run created e: %v", err)
	}
}

// A sandboxed playbook reads as isolated even without isolated_login; and a
// dry run keeps a renamed playbook's login setting, refusing UNSET as the
// real run does (agy review, v3.23.0).
func TestIsolatedLoginSandboxAndRename(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	root := seedFlatPlaybook(t, "sb")
	if err := os.WriteFile(filepath.Join(root, ".playbook"), []byte("name = \"sb\"\n[sandbox]\nalways = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var v struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal([]byte(mustStmt(t, "SHOW PLAYBOOK sb --json")), &v); err != nil || v.Login != "isolated" {
		t.Fatalf("a sandboxed playbook: %v %+v", err, v)
	}
	f := writePlaybookFile(t, "ALTER PLAYBOOK sb RENAME TO sb2;\nALTER PLAYBOOK sb2 SET login = 'shared';\n")
	if _, err := apply(t, f, "--dry-run"); err == nil || !strings.Contains(err.Error(), "always runs in a sandbox") {
		t.Fatalf("dry run after a rename: %v", err)
	}
	mustStmt(t, "CREATE PLAYBOOK own SET launcher = '' SET login=isolated")
	own := filepath.Join(config.ResolvePlaybooksDir(), "own", ".credentials.json")
	if err := os.WriteFile(own, []byte(`{"claudeAiOauth":{"accessToken":"own"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f = writePlaybookFile(t, "ALTER PLAYBOOK own RENAME TO own2;\nALTER PLAYBOOK own2 SET login = 'shared';\n")
	if _, err := apply(t, f, "--dry-run"); err == nil || !strings.Contains(err.Error(), "has a login of its own") {
		t.Fatalf("dry run after a rename, own login: %v", err)
	}
}
