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

// Bare SET SANDBOX is always = true and isolates the login, as CREATE …
// SANDBOX does; the keyed form sets only the keys it names, always never;
// always=true as a key is the same as the bare form; UNSET SANDBOX turns
// always off and leaves the login isolated.
func TestSetSandboxForms(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	mustStmt(t, "CREATE PLAYBOOK p NO ALIAS")

	mustStmt(t, "ALTER PLAYBOOK p SET SANDBOX backend=sbx host=me@buildbox mounts=~/libs:ro,~/data")
	sb := sandboxOf(t, "p")
	if sb == nil || sb.Always || sb.Backend != "sbx" || sb.Host != "me@buildbox" || !reflect.DeepEqual(sb.Mounts, []string{"~/libs:ro", "~/data"}) {
		t.Fatalf("keyed SET SANDBOX: %#v", sb)
	}
	if isolateAuthOf(t, "p") {
		t.Fatal("keyed SET SANDBOX isolated the login")
	}

	out := mustStmt(t, "ALTER PLAYBOOK p SET SANDBOX")
	if sb := sandboxOf(t, "p"); !sb.Always || sb.Backend != "sbx" || !isolateAuthOf(t, "p") {
		t.Fatalf("bare SET SANDBOX: %#v isolated=%v", sb, isolateAuthOf(t, "p"))
	}
	if !strings.Contains(out, "login     isolated") {
		t.Errorf("bare SET SANDBOX does not say it isolates the login:\n%s", out)
	}

	if out := mustStmt(t, "EXPLAIN PLAYBOOK p"); !strings.Contains(out, "every launch runs in a sandbox, with an isolated login") || !strings.Contains(out, "UNSET SANDBOX keeps the login isolated") {
		t.Errorf("EXPLAIN of a sandboxed playbook:\n%s", out)
	}

	mustStmt(t, "ALTER PLAYBOOK p UNSET SANDBOX")
	if sb := sandboxOf(t, "p"); sb.Always || sb.Backend != "sbx" || !isolateAuthOf(t, "p") {
		t.Fatalf("UNSET SANDBOX: %#v isolated=%v", sb, isolateAuthOf(t, "p"))
	}
	if out := mustStmt(t, "EXPLAIN PLAYBOOK p"); !strings.Contains(out, "Login: isolated") {
		t.Errorf("EXPLAIN after UNSET SANDBOX:\n%s", out)
	}

	mustStmt(t, "CREATE PLAYBOOK q NO ALIAS")
	mustStmt(t, "ALTER PLAYBOOK q SET SANDBOX always=true")
	if sb := sandboxOf(t, "q"); sb == nil || !sb.Always || !isolateAuthOf(t, "q") {
		t.Fatalf("SET SANDBOX always=true: %#v isolated=%v", sb, isolateAuthOf(t, "q"))
	}

	mustStmt(t, "ALTER PLAYBOOK p UNSET SANDBOX host mounts backend")
	if sb := sandboxOf(t, "p"); sb != nil {
		t.Fatalf("UNSET SANDBOX of every key left %#v", sb)
	}
	if out := mustStmt(t, "ALTER PLAYBOOK p UNSET SANDBOX"); !strings.Contains(out, "unchanged") {
		t.Errorf("UNSET SANDBOX with nothing set: %s", out)
	}
}

// A value the [sandbox] table refuses is refused before anything is written.
func TestSetSandboxRefusesBadValues(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	mustStmt(t, "CREATE PLAYBOOK p NO ALIAS")
	mustStmt(t, "ALTER PLAYBOOK p SET SANDBOX host=me@buildbox")
	before, err := os.ReadFile(filepath.Join(config.ResolvePlaybooksDir(), "p", manifest.FileName))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		"ALTER PLAYBOOK p SET SANDBOX secrets=bogus",
		"ALTER PLAYBOOK p SET SANDBOX backend=nope",
	} {
		if err := stmtErr(t, line); err == nil {
			t.Errorf("%s: accepted", line)
		}
	}
	after, _ := os.ReadFile(filepath.Join(config.ResolvePlaybooksDir(), "p", manifest.FileName))
	if string(after) != string(before) {
		t.Fatalf("a refused SET SANDBOX wrote the manifest:\n%s", after)
	}
}

// SHOW PLAYBOOK has the table as one object and on its Sandbox line; SHOW
// CREATE writes it as a bare SET SANDBOX and one SET SANDBOX <key>=<value>,
// which APPLY puts back as it was.
func TestSandboxShowAndRoundTrip(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	mustStmt(t, "CREATE PLAYBOOK p NO ALIAS")
	mustStmt(t, "ALTER PLAYBOOK p SET SANDBOX SET SANDBOX backend=sbx allow_net=api.example.com:443 share_skills=true")

	sb, _ := showPlaybook(t, "p")["sandbox"].(map[string]any)
	if sb["always"] != true || sb["backend"] != "sbx" || sb["host"] != nil || sb["share_skills"] != true ||
		!reflect.DeepEqual(sb["allow_net"], []any{"api.example.com:443"}) || !reflect.DeepEqual(sb["mounts"], []any{}) {
		t.Fatalf("SHOW --json sandbox: %#v", sb)
	}
	if out := mustStmt(t, "SHOW PLAYBOOK p"); !strings.Contains(out, "yes (backend=sbx, allow_net=api.example.com:443, share_skills=true)") {
		t.Errorf("SHOW PLAYBOOK Sandbox line:\n%s", out)
	}
	if v := showPlaybook(t, "p")["isolated_login"]; v != true {
		t.Errorf("isolated_login = %v", v)
	}

	text := mustStmt(t, "SHOW CREATE PLAYBOOK p")
	for _, want := range []string{"SET SANDBOX\n", "SET SANDBOX backend=sbx allow_net=api.example.com:443 share_skills=true"} {
		if !strings.Contains(text, want) {
			t.Errorf("SHOW CREATE lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "SET ISOLATED LOGIN") || strings.Contains(strings.SplitN(text, ";", 2)[0], "SANDBOX") {
		t.Errorf("SHOW CREATE writes the sandbox in CREATE or repeats the isolation:\n%s", text)
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

// A dry run says what SET SANDBOX would do and writes nothing; a linked
// playbook's [sandbox] is the target's (a plain directory's refusal is in
// TestPlainDirectoryRefusals).
func TestSetSandboxDryRunAndRefusals(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	mustStmt(t, "CREATE PLAYBOOK p NO ALIAS")
	file := filepath.Join(t.TempDir(), "s.cpb")
	if err := os.WriteFile(file, []byte("ALTER PLAYBOOK p SET SANDBOX SET SANDBOX host=me@buildbox;\n"), 0o644); err != nil {
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
	if err := stmtErr(t, "ALTER PLAYBOOK ext SET SANDBOX host=me@buildbox"); err == nil || !strings.Contains(err.Error(), "linked") {
		t.Errorf("linked: %v", err)
	}
}
