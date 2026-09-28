package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
)

func claudeMDOf(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(config.ResolvePlaybooksDir(), name, "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// NO PILOT PROFILE writes cpb's CLAUDE.md without the ~/.pilot-profile/
// imports; it is create-time only and refused with FROM or LINK.
func TestNoPilotProfile(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	mustStmt(t, "CREATE PLAYBOOK a NO ALIAS")
	if md := claudeMDOf(t, "a"); !strings.Contains(md, "@~/.pilot-profile/PROFILE.md") {
		t.Fatalf("the default imports the profile:\n%s", md)
	}
	mustStmt(t, "CREATE PLAYBOOK b NO ALIAS NO PILOT PROFILE")
	if md := claudeMDOf(t, "b"); strings.Contains(md, "pilot-profile") || !strings.HasPrefix(md, "# Playbook: b\n") || !strings.HasSuffix(md, "another.\n") {
		t.Fatalf("NO PILOT PROFILE:\n%s", md)
	}
	for line, want := range map[string]string{
		"CREATE PLAYBOOK c FROM /x NO PILOT PROFILE":          "the source's own",
		"CREATE PLAYBOOK c LINK /x NO PILOT PROFILE":          "the source's own",
		"ALTER PLAYBOOK a NO PILOT PROFILE":                   "applies to CREATE PLAYBOOK only",
		"CREATE PLAYBOOK c NO PILOT":                          "expected PROFILE after NO PILOT",
		"CREATE PLAYBOOK c NO ALIAS NO ALIAS":                 "appears twice",
		"CREATE PLAYBOOK c NO PILOT PROFILE NO PILOT PROFILE": "appears twice",
	} {
		if _, err := quotedStmt(t, line); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", line, err, want)
		}
	}
	// pilot stays free to name things.
	mustStmt(t, "CREATE PLAYBOOK pilot NO ALIAS NO PILOT PROFILE")

	// The pre-grammar create takes it as --no-pilot-profile.
	createNoPilotProfile = true
	t.Cleanup(func() { createNoPilotProfile = false })
	captureStdout(t, func() {
		if err := runCreate(nil, []string{"legacy"}); err != nil {
			t.Error(err)
		}
	})
	if md := claudeMDOf(t, "legacy"); strings.Contains(md, "pilot-profile") {
		t.Fatalf("--no-pilot-profile:\n%s", md)
	}
}

// The warning: given by the statement that makes a profile-importing
// playbook's ANTHROPIC_BASE_URL non-Anthropic (its own block, an env set it
// uses, DEFAULTS, or its creation under such DEFAULTS), naming the playbook
// and the host only, and not again by later statements.
func TestPilotProfileThirdPartyWarning(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	exec := func(line string) *stmtRun {
		t.Helper()
		st, err := grammar.ParseLine(line)
		if err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		r := &stmtRun{}
		captureStdout(t, func() { err = execStatement(r, st) })
		if err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		return r
	}
	quiet := func(line string) {
		t.Helper()
		if r := exec(line); r.warning != "" {
			t.Fatalf("%s: unexpected warning %q", line, r.warning)
		}
	}
	warns := func(line string, want ...string) {
		t.Helper()
		r := exec(line)
		if r.warningCode != warnPilotProfileThirdParty {
			t.Fatalf("%s: code %q, warning %q", line, r.warningCode, r.warning)
		}
		for _, w := range want {
			if !strings.Contains(r.warning, w) {
				t.Fatalf("%s: warning %q lacks %q", line, r.warning, w)
			}
		}
		if strings.Contains(r.warning, "20128") || strings.Contains(r.warning, "/v1") {
			t.Fatalf("%s: the warning carries more than the host: %q", line, r.warning)
		}
	}

	quiet("CREATE ENV glm SET ANTHROPIC_BASE_URL=http://tr0:20128/v1")
	quiet("CREATE ENV anth SET ANTHROPIC_BASE_URL=https://api.anthropic.com")
	quiet("CREATE PLAYBOOK a NO ALIAS")
	warns("ALTER PLAYBOOK a USE ENV glm", "PLAYBOOK a (tr0) imports ~/.pilot-profile/", "NO PILOT PROFILE")
	quiet("ALTER PLAYBOOK a USE ENV glm") // already so: not again
	quiet("ALTER PLAYBOOK a SET VAR X=1")

	quiet("CREATE PLAYBOOK b NO ALIAS NO PILOT PROFILE")
	quiet("ALTER PLAYBOOK b USE ENV glm") // no imports

	quiet("CREATE PLAYBOOK c NO ALIAS")
	quiet("ALTER PLAYBOOK c USE ENV anth") // Anthropic's own host
	w := exec("ALTER ENV anth SET ANTHROPIC_BASE_URL=https://glm.example/api")
	if w.warningCode != warnPilotProfileThirdParty || !strings.Contains(w.warning, "PLAYBOOK c (glm.example)") || strings.Contains(w.warning, "a (") {
		t.Fatalf("an env set turning third-party: %q", w.warning)
	}
	quiet("ALTER PLAYBOOK c SET VAR ANTHROPIC_BASE_URL=https://api.anthropic.com") // own block wins
	warns("ALTER PLAYBOOK c UNSET VAR ANTHROPIC_BASE_URL", "PLAYBOOK c (glm.example)")

	quiet("CREATE PLAYBOOK d NO ALIAS")
	quiet("CREATE PLAYBOOK e NO ALIAS")
	warns("ALTER DEFAULTS USE ENV glm", "PLAYBOOKS d (tr0), e (tr0) import")
	warns("CREATE PLAYBOOK f NO ALIAS", "PLAYBOOK f (tr0)") // created under such DEFAULTS
	quiet("CREATE PLAYBOOK g NO ALIAS NO PILOT PROFILE")
	quiet("ALTER DEFAULTS USE ENV anth")  // another third-party host: already so, not again
	quiet("ALTER DEFAULTS DROP ENV anth") // leaves the condition: no warning
	warns("ALTER DEFAULTS USE ENV glm", "d (tr0)", "e (tr0)", "f (tr0)")
}

// A dry run warns too, from the state the file's earlier statements would
// leave, with the stable code in APPLY --json; nothing is created.
func TestPilotProfileWarningDryRun(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	path := filepath.Join(t.TempDir(), "p.cpb")
	src := "CREATE PLAYBOOK h NO ALIAS;\n" +
		"ALTER PLAYBOOK h SET VAR ANTHROPIC_BASE_URL=https://router.example/v1;\n" +
		"CREATE PLAYBOOK i NO ALIAS NO PILOT PROFILE;\n" +
		"ALTER PLAYBOOK i SET VAR ANTHROPIC_BASE_URL=https://router.example/v1;\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, out, code := applyJSON(t, path, "--dry-run", "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	ss := stmts(rep)
	w, _ := ss[1]["warning"].(map[string]any)
	if w == nil || w["code"] != "pilot_profile_third_party_endpoint" || !strings.Contains(w["message"].(string), "PLAYBOOK h (router.example)") {
		t.Fatalf("statement 2's warning: %v", ss[1]["warning"])
	}
	for _, i := range []int{0, 2, 3} {
		if ss[i]["warning"] != nil {
			t.Fatalf("statement %d warned: %v", i+1, ss[i]["warning"])
		}
	}
	if _, err := os.Stat(filepath.Join(config.ResolvePlaybooksDir(), "h")); !os.IsNotExist(err) {
		t.Fatalf("the dry run created h: %v", err)
	}
}

// pilot_profile (v3.26.0): imported, not_imported (no import line, no
// CLAUDE.md), unknown (CLAUDE.md unreadable); last in the object and the
// PLAYBOOKS table.
func TestPilotProfileField(t *testing.T) {
	root := sandboxDefaultRoot(t)
	mustStmt(t, "CREATE PLAYBOOK with NO ALIAS")
	mustStmt(t, "CREATE PLAYBOOK without NO ALIAS NO PILOT PROFILE")
	mustStmt(t, "CREATE PLAYBOOK gone NO ALIAS")
	mustStmt(t, "CREATE PLAYBOOK odd NO ALIAS")
	mustStmt(t, "CREATE PLAYBOOK dangling NO ALIAS")
	dl := filepath.Join(root, "dangling", "CLAUDE.md")
	if err := os.Remove(dl); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "nowhere.md"), dl); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "gone", "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	odd := filepath.Join(root, "odd", "CLAUDE.md")
	if err := os.Remove(odd); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(odd, 0o755); err != nil {
		t.Fatal(err)
	}
	wants := map[string]string{"with": "imported", "without": "not_imported", "gone": "not_imported", "odd": "unknown", "dangling": "unknown"}
	for name, want := range wants {
		out := mustStmt(t, "SHOW PLAYBOOK "+name+" --json")
		var v map[string]any
		if err := json.Unmarshal([]byte(out), &v); err != nil || v["pilot_profile"] != want {
			t.Errorf("%s: pilot_profile = %v, want %s (%v)", name, v["pilot_profile"], want, err)
		}
		if !strings.HasSuffix(strings.TrimSpace(out), `"pilot_profile": "`+want+`"`+"\n}") {
			t.Errorf("%s: pilot_profile is not the last field:\n%s", name, out)
		}
	}
	if h := mustStmt(t, "SHOW PLAYBOOK without"); !strings.Contains(h, "Pilot profile") || !strings.Contains(h, "not imported") {
		t.Fatalf("human:\n%s", h)
	}
	cols, _ := describeTable("PLAYBOOKS")
	if last := cols[len(cols)-1]; last.Name != "pilot_profile" || last.Type != "String" {
		t.Fatalf("last column: %+v", last)
	}
	var rows []map[string]any
	var err error
	js := captureStdout(t, func() { err = runStatement([]string{"SELECT name, pilot_profile FROM PLAYBOOKS", "--json"}) })
	if err != nil || json.Unmarshal([]byte(js), &rows) != nil || len(rows) != len(wants) {
		t.Fatalf("%v\n%s", err, js)
	}
	for _, r := range rows {
		if r["pilot_profile"] != wants[r["name"].(string)] {
			t.Errorf("SELECT %v: pilot_profile %v", r["name"], r["pilot_profile"])
		}
	}
}
