package cmd

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// sandboxOf is a playbook's [sandbox] table; nil for none (a playbook
// created empty has no manifest at all).
func sandboxOf(t *testing.T, name string) *manifest.Sandbox {
	t.Helper()
	m, err := manifest.Read(filepath.Join(config.ResolvePlaybooksDir(), name))
	if err != nil {
		t.Fatal(err)
	}
	if m == nil {
		return nil
	}
	return m.Sandbox
}

// The sandbox.<key> properties set only the keys they name; always is one
// of them and changes nothing else: sandbox.always = true needs the login
// isolated, already or in the same statement, and false leaves it isolated.
func TestSetSandboxForms(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	mustStmt(t, "CREATE PLAYBOOK p SET launcher = ''")

	mustStmt(t, "ALTER PLAYBOOK p SET sandbox.backend = sbx, sandbox.host = me@buildbox, sandbox.mounts = [~/libs:ro,~/data]")
	sb := sandboxOf(t, "p")
	if sb == nil || sb.Always || sb.Backend != "sbx" || sb.Host != "me@buildbox" || !reflect.DeepEqual(sb.Mounts, []string{"~/libs:ro", "~/data"}) {
		t.Fatalf("SET sandbox.<key>: %#v", sb)
	}
	if isolateAuthOf(t, "p") {
		t.Fatal("SET sandbox.<key> isolated the login")
	}

	// always = true on a shared login is refused, and writes nothing.
	if err := stmtErr(t, "ALTER PLAYBOOK p SET sandbox.always = true"); err == nil || !strings.Contains(err.Error(), "SET sandbox.always = true, login = 'isolated'") {
		t.Fatalf("sandbox.always on a shared login: %v", err)
	}
	if sb := sandboxOf(t, "p"); sb.Always || isolateAuthOf(t, "p") {
		t.Fatalf("the refused statement wrote: %#v", sb)
	}
	out := mustStmt(t, "ALTER PLAYBOOK p SET sandbox.always = true, login = isolated")
	if sb := sandboxOf(t, "p"); !sb.Always || sb.Backend != "sbx" || !isolateAuthOf(t, "p") {
		t.Fatalf("SET sandbox.always = true, login = 'isolated': %#v isolated=%v", sb, isolateAuthOf(t, "p"))
	}
	if !strings.Contains(out, "login     isolated") {
		t.Errorf("the login line is missing:\n%s", out)
	}

	if out := mustStmt(t, "EXPLAIN PLAYBOOK p"); !strings.Contains(out, "every launch runs in a sandbox, with an isolated login") || !strings.Contains(out, "SET sandbox.always = false keeps the login isolated") || strings.Contains(out, "Login:") {
		t.Errorf("EXPLAIN of a sandboxed playbook: the Sandbox line says the login, and no Login line repeats it:\n%s", out)
	}

	mustStmt(t, "ALTER PLAYBOOK p SET sandbox.always = false")
	if sb := sandboxOf(t, "p"); sb.Always || sb.Backend != "sbx" || !isolateAuthOf(t, "p") {
		t.Fatalf("sandbox.always = false: %#v isolated=%v", sb, isolateAuthOf(t, "p"))
	}
	if out := mustStmt(t, "EXPLAIN PLAYBOOK p"); !strings.Contains(out, "Login: isolated") {
		t.Errorf("EXPLAIN after sandbox.always = false:\n%s", out)
	}

	mustStmt(t, "ALTER PLAYBOOK p DELETE sandbox.host, sandbox.mounts, sandbox.backend")
	if sb := sandboxOf(t, "p"); sb != nil {
		t.Fatalf("DELETE of every key left %#v", sb)
	}
	if out := mustStmt(t, "ALTER PLAYBOOK p DELETE sandbox"); !strings.Contains(out, "unchanged") {
		t.Errorf("DELETE sandbox with nothing set: %s", out)
	}
}

// A value the [sandbox] table refuses is refused before anything is written.
func TestSetSandboxRefusesBadValues(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	mustStmt(t, "CREATE PLAYBOOK p SET launcher = ''")
	mustStmt(t, "ALTER PLAYBOOK p SET sandbox.host = me@buildbox")
	before, err := os.ReadFile(filepath.Join(config.ResolvePlaybooksDir(), "p", manifest.FileName))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		"ALTER PLAYBOOK p SET sandbox.secrets = bogus",
		"ALTER PLAYBOOK p SET sandbox.backend = nope",
	} {
		if err := stmtErr(t, line); err == nil {
			t.Errorf("%s: accepted", line)
		}
	}
	after, _ := os.ReadFile(filepath.Join(config.ResolvePlaybooksDir(), "p", manifest.FileName))
	if string(after) != string(before) {
		t.Fatalf("a refused SET sandbox.<key> wrote the manifest:\n%s", after)
	}
}

// SHOW PLAYBOOK has the table as one object and on its Sandbox line; SHOW
// CREATE writes it as SET sandbox.always = …, sandbox.<key> = …, which
// APPLY puts back as it was.
func TestSandboxShowAndRoundTrip(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	mustStmt(t, "CREATE PLAYBOOK p SET launcher = ''")
	mustStmt(t, "ALTER PLAYBOOK p SET sandbox.always = true, login = isolated, sandbox.backend = sbx, sandbox.allow_net = [api.example.com:443], sandbox.share_skills = true")

	sb, _ := showPlaybook(t, "p")["sandbox"].(map[string]any)
	if sb["always"] != true || sb["backend"] != "sbx" || sb["host"] != nil || sb["share_skills"] != true ||
		!reflect.DeepEqual(sb["allow_net"], []any{"api.example.com:443"}) || !reflect.DeepEqual(sb["mounts"], []any{}) {
		t.Fatalf("SHOW --json sandbox: %#v", sb)
	}
	if out := mustStmt(t, "SHOW PLAYBOOK p"); !strings.Contains(out, "yes (backend=sbx, allow_net=api.example.com:443, share_skills=true)") {
		t.Errorf("SHOW PLAYBOOK Sandbox line:\n%s", out)
	}
	if v := showPlaybook(t, "p")["login"]; v != "isolated" {
		t.Errorf("login = %v", v)
	}

	text := mustStmt(t, "SHOW CREATE PLAYBOOK p")
	// The properties travel on the CREATE, which converges on a playbook
	// that exists.
	if want := "CREATE OR ALTER PLAYBOOK p\n  SET launcher = '', login = 'isolated', memory = 'isolated', sandbox.always = true, sandbox.backend = 'sbx', sandbox.allow_net = ['api.example.com:443'], sandbox.share_skills = true;"; !strings.Contains(text, want) {
		t.Errorf("SHOW CREATE lacks %q:\n%s", want, text)
	}

	want := sandboxOf(t, "p")
	file := filepath.Join(t.TempDir(), "p.cpb")
	if err := os.WriteFile(file, []byte(text+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Applied where p already exists, the file changes nothing.
	if out := mustStmt(t, "APPLY "+file); !strings.Contains(out, "0 created, 0 changed") {
		t.Errorf("APPLY of SHOW CREATE over the same playbook changed it:\n%s", out)
	}
	// Applied to a fresh machine, it builds the same table.
	aliasTestHome(t)
	mustStmt(t, "APPLY "+file)
	if got := sandboxOf(t, "p"); !reflect.DeepEqual(got, want) || !isolateAuthOf(t, "p") {
		t.Fatalf("round trip: %#v, want %#v", got, want)
	}
}

// A dry run says what SET sandbox.<key> would do and writes nothing; a linked
// playbook's [sandbox] is the target's (a plain directory's refusal is in
// TestPlainDirectoryRefusals).
func TestSetSandboxDryRunAndRefusals(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	mustStmt(t, "CREATE PLAYBOOK p SET launcher = ''")
	file := filepath.Join(t.TempDir(), "s.cpb")
	if err := os.WriteFile(file, []byte("ALTER PLAYBOOK p SET sandbox.always = true, login = 'isolated', sandbox.host = 'me@buildbox';\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := mustStmt(t, "APPLY "+file+" --dry-run"); !strings.Contains(out, "1 changed") {
		t.Errorf("dry run:\n%s", out)
	}
	if sb := sandboxOf(t, "p"); sb != nil || isolateAuthOf(t, "p") {
		t.Fatalf("the dry run wrote %#v", sb)
	}

	target := t.TempDir()
	if err := manifest.Write(target, &manifest.Manifest{Name: "ext"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(config.ResolvePlaybooksDir(), "ext")); err != nil {
		t.Fatal(err)
	}
	if err := stmtErr(t, "ALTER PLAYBOOK ext SET sandbox.host = me@buildbox"); err == nil || !strings.Contains(err.Error(), "linked") {
		t.Errorf("linked: %v", err)
	}
}
