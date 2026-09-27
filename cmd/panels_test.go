package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func readManifest(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cpb, _ := panelOwner(data); !cpb {
		t.Fatalf("%s does not carry cpb's marker with a matching hash:\n%s", path, data)
	}
	m := map[string]any{}
	if _, err := toml.Decode(string(data), &m); err != nil {
		t.Fatalf("%s is not TOML: %v\n%s", path, err, data)
	}
	return m
}

// ADD PANEL writes one SPC/1 manifest per panel type, with contract = 1 and
// only the fields given; a repeat is unchanged; DROP PANEL removes it (and
// the namespace directory once empty); the layout file is never written.
func TestPanelsAddDrop(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	root := seedFlatPlaybook(t, "k")
	must := func(line string) string {
		t.Helper()
		out, err := quotedStmt(t, line)
		if err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		return out
	}
	must("ALTER PLAYBOOK k ADD PANEL local.clock EXEC 'date +%H:%M' ROW 1 PRIORITY 60 ALIGN RIGHT TIMEOUT 100 ADD PANEL local.model TEMPLATE '{model.display_name}' WHEN 'model' ADD PANEL kom.lease RECORDS '${CLAUDE_CONFIG_DIR}/data/lease.ndjson' STALE 30000 ADD PANEL kom.beat OBSERVE 'sh ${PANEL_DIR}/beat.sh' EVERY 10000 MAX RUN 30000")
	dir := filepath.Join(root, panelsDirName)
	clock := readManifest(t, filepath.Join(dir, "local", "clock.toml"))
	for k, want := range map[string]any{"contract": int64(1), "id": "clock", "type": "exec", "command": "date +%H:%M", "row": int64(1), "priority": int64(60), "align": "right", "timeout_ms": int64(100)} {
		if clock[k] != want {
			t.Errorf("clock %s = %v, want %v", k, clock[k], want)
		}
	}
	if len(clock) != 8 {
		t.Errorf("clock has fields not given: %v", clock)
	}
	if m := readManifest(t, filepath.Join(dir, "local", "model.toml")); m["type"] != "template" || m["text"] != "{model.display_name}" || m["when"] != "model" {
		t.Errorf("template: %v", m)
	}
	if m := readManifest(t, filepath.Join(dir, "kom", "lease.toml")); m["type"] != "records" || m["path"] != "${CLAUDE_CONFIG_DIR}/data/lease.ndjson" || m["stale_ms"] != int64(30000) {
		t.Errorf("records: %v", m)
	}
	if m := readManifest(t, filepath.Join(dir, "kom", "beat.toml")); m["type"] != "observe" || m["command"] != "sh ${PANEL_DIR}/beat.sh" || m["every_ms"] != int64(10000) || m["max_run_ms"] != int64(30000) {
		t.Errorf("observe: %v", m)
	}
	if out := must("ALTER PLAYBOOK k ADD PANEL local.clock EXEC 'date +%H:%M' ROW 1 PRIORITY 60 ALIGN RIGHT TIMEOUT 100"); !strings.Contains(out, "unchanged") {
		t.Fatalf("a repeat:\n%s", out)
	}
	must("ALTER PLAYBOOK k DROP PANEL kom.lease DROP PANEL kom.beat")
	if _, err := os.Stat(filepath.Join(dir, "kom")); !os.IsNotExist(err) {
		t.Fatalf("the empty namespace directory stayed: %v", err)
	}
	if out := must("ALTER PLAYBOOK k DROP PANEL kom.beat"); !strings.Contains(out, "unchanged") {
		t.Fatalf("dropping an absent panel:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(root, "statusline.toml")); !os.IsNotExist(err) {
		t.Fatal("cpb wrote the pilot's layout file")
	}
}

func TestPanelRefusals(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	seedFlatPlaybook(t, "k")
	for line, want := range map[string]string{
		`ALTER PLAYBOOK k ADD PANEL local.x EXEC 'sh "${PANEL_DIR}/x.sh"'`:               "${PANEL_DIR} is inside quotes",
		`ALTER PLAYBOOK k ADD PANEL local.x EXEC "sh '${HOME}/x.sh'"`:                    "${HOME} is inside quotes",
		"ALTER PLAYBOOK k ADD PANEL project.x TEMPLATE 'a'":                              "the project namespace",
		"ALTER PLAYBOOK k ADD PANEL Local.x TEMPLATE 'a'":                                "<namespace>.<id>",
		"ALTER PLAYBOOK k ADD PANEL clock TEMPLATE 'a'":                                  "<namespace>.<id>",
		"ALTER PLAYBOOK k ADD PANEL local.x OBSERVE 'sh b.sh'":                           "OBSERVE needs EVERY",
		"ALTER PLAYBOOK k ADD PANEL local.x OBSERVE 'sh b.sh' EVERY 500":                 "at least 1000",
		"ALTER PLAYBOOK k ADD PANEL local.x TEMPLATE 'a' TTL 5":                          "TTL does not apply to a template panel",
		"ALTER PLAYBOOK k ADD PANEL local.x TEMPLATE 'a' ROW 1 ROW 2":                    "ROW appears twice",
		"ALTER PLAYBOOK k ADD PANEL local.x EXEC 'a' PRIORITY 101":                       "0 to 100",
		"ALTER PLAYBOOK k ADD PANEL local.x TEMPLATE 'a' ADD PANEL local.x TEMPLATE 'b'": "panel local.x appears twice",
		"ALTER PLAYBOOK k ADD PANEL local.x":                                             "ADD PANEL needs EXEC",
	} {
		if _, err := quotedStmt(t, line); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", line, err, want)
		}
	}
	// ${…} unquoted is right, and a plain quoted word beside it is fine.
	if _, err := quotedStmt(t, `ALTER PLAYBOOK k ADD PANEL local.ok EXEC 'sh ${PANEL_DIR}/x.sh "--flag"'`); err != nil {
		t.Fatalf("an unquoted variable was refused: %v", err)
	}
}

// A manifest cpb did not write is the pilot's: ADD over it and DROP of it
// are refused, and it is listed as not cpb's.
func TestPanelsLeaveHandWrittenAlone(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	root := seedFlatPlaybook(t, "k")
	mine := filepath.Join(root, panelsDirName, "local", "mine.toml")
	if err := os.MkdirAll(filepath.Dir(mine), 0o755); err != nil {
		t.Fatal(err)
	}
	hand := "contract = 1\nid = \"mine\"\ntype = \"template\"\ntext = \"hand\"\n"
	if err := os.WriteFile(mine, []byte(hand), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"ALTER PLAYBOOK k ADD PANEL local.mine TEMPLATE 'cpb'", "ALTER PLAYBOOK k DROP PANEL local.mine"} {
		if _, err := quotedStmt(t, line); err == nil || !strings.Contains(err.Error(), "not written by cpb") {
			t.Fatalf("%s: %v", line, err)
		}
	}
	if data, _ := os.ReadFile(mine); string(data) != hand {
		t.Fatal("the pilot's manifest changed")
	}
	var v struct {
		Panels []panelJSON `json:"panels"`
	}
	if err := json.Unmarshal([]byte(mustStmt(t, "SHOW PLAYBOOK k --json")), &v); err != nil || len(v.Panels) != 1 || v.Panels[0].Panel != "local.mine" || v.Panels[0].Cpb {
		t.Fatalf("SHOW --json panels: %v %+v", err, v.Panels)
	}
	if created := mustStmt(t, "SHOW CREATE PLAYBOOK k"); strings.Contains(created, "local.mine") {
		t.Fatalf("SHOW CREATE wrote the pilot's panel:\n%s", created)
	}
}

// FROM STATUSLINE turns the current status line into an exec panel; it is
// refused with no status line and when the status line is the host itself.
func TestPanelFromStatusline(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	root := seedFlatPlaybook(t, "k")
	if _, err := quotedStmt(t, "ALTER PLAYBOOK k ADD PANEL local.bar FROM STATUSLINE"); err == nil || !strings.Contains(err.Error(), "no status line command") {
		t.Fatalf("no status line: %v", err)
	}
	write := func(cmd string) {
		data := `{"statusLine":{"type":"command","command":` + strconvQuote(cmd) + `}}`
		if err := os.WriteFile(filepath.Join(root, "settings.json"), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("statusmux render")
	if _, err := quotedStmt(t, "ALTER PLAYBOOK k ADD PANEL local.bar FROM STATUSLINE"); err == nil || !strings.Contains(err.Error(), "the host itself") {
		t.Fatalf("host: %v", err)
	}
	write("bash ~/bar.sh")
	if _, err := quotedStmt(t, "ALTER PLAYBOOK k ADD PANEL local.bar FROM STATUSLINE ROW 2"); err != nil {
		t.Fatal(err)
	}
	if m := readManifest(t, filepath.Join(root, panelsDirName, "local", "bar.toml")); m["type"] != "exec" || m["command"] != "bash ~/bar.sh" || m["row"] != int64(2) {
		t.Fatalf("FROM STATUSLINE: %v", m)
	}
	// SHOW CREATE writes the command itself, and it re-applies unchanged.
	created := mustStmt(t, "SHOW CREATE PLAYBOOK k")
	if !strings.Contains(created, "ADD PANEL local.bar EXEC 'bash ~/bar.sh' ROW 2") {
		t.Fatalf("SHOW CREATE:\n%s", created)
	}
	if out, err := apply(t, writePlaybookFile(t, created)); err != nil || !strings.Contains(out, " 0 created, 0 changed,") {
		t.Fatalf("SHOW CREATE did not re-apply unchanged: %v\n%s", err, out)
	}
	if out := mustStmt(t, "EXPLAIN PLAYBOOK k"); !strings.Contains(out, "Panels: 1 (local.bar); the status line is not a host, so they do not render") {
		t.Fatalf("EXPLAIN:\n%s", out)
	}
}

func strconvQuote(s string) string { b, _ := json.Marshal(s); return string(b) }

// An enabled plugin's panels are listed read-only (source "plugin
// <key>"); a disabled plugin's are not; SELECT FROM PANELS lists both
// kinds with their playbook.
func TestPanelsListPluginPanels(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	root := seedFlatPlaybook(t, "k")
	plug := t.TempDir()
	if err := os.MkdirAll(filepath.Join(plug, "statusline"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plug, "statusline", "lease.toml"), []byte("contract = 1\nid = \"lease\"\ntype = \"records\"\npath = \"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	inst := `{"version":2,"plugins":{"Kommander@kmkt":[{"scope":"user","installPath":` + strconvQuote(plug) + `}],"off@kmkt":[{"scope":"user","installPath":` + strconvQuote(plug) + `}]}}`
	if err := os.WriteFile(filepath.Join(root, "plugins", "installed_plugins.json"), []byte(inst), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "settings.json"), []byte(`{"enabledPlugins":{"Kommander@kmkt":true,"off@kmkt":false}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	mustStmt(t, "ALTER PLAYBOOK k ADD PANEL local.clock TEMPLATE x")
	var v struct {
		Panels []panelJSON `json:"panels"`
	}
	if err := json.Unmarshal([]byte(mustStmt(t, "SHOW PLAYBOOK k --json")), &v); err != nil || len(v.Panels) != 2 {
		t.Fatalf("panels: %v %+v", err, v.Panels)
	}
	if p := v.Panels[1]; p.Panel != "kommander.lease" || p.Source != "plugin Kommander@kmkt" || p.Cpb || p.Type != "records" {
		t.Fatalf("plugin panel: %+v", p)
	}
	var rows []map[string]any
	js := mustStmt(t, "SELECT playbook, panel, source, cpb FROM PANELS --json")
	if json.Unmarshal([]byte(js), &rows) != nil || len(rows) != 2 || rows[0]["playbook"] != "k" || rows[0]["cpb"] != true {
		t.Fatalf("SELECT FROM PANELS: %s", js)
	}
	// The plugin's panel is not cpb's to write back.
	if created := mustStmt(t, "SHOW CREATE PLAYBOOK k"); strings.Contains(created, "lease") {
		t.Fatalf("SHOW CREATE wrote a plugin's panel:\n%s", created)
	}
}

// A dry run writes no manifest and judges a later DROP against the ADD an
// earlier statement would make; a plain directory takes panels too.
func TestPanelsDryRunAndDir(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	root := seedFlatPlaybook(t, "k")
	f := writePlaybookFile(t, "ALTER PLAYBOOK k ADD PANEL local.a TEMPLATE x;\nALTER PLAYBOOK k ADD PANEL local.a TEMPLATE x;\nALTER PLAYBOOK k DROP PANEL local.a;\n")
	out, err := apply(t, f, "--dry-run")
	if err != nil || !strings.Contains(out, "2 changed, 1 unchanged") {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(root, panelsDirName)); !os.IsNotExist(err) {
		t.Fatalf("the dry run wrote a manifest: %v", err)
	}
	// A playbook created earlier in the same dry run has no directory yet.
	f = writePlaybookFile(t, "CREATE PLAYBOOK fresh NO ALIAS;\nALTER PLAYBOOK fresh ADD PANEL local.a TEMPLATE x;\nALTER PLAYBOOK fresh ADD PANEL local.a TEMPLATE x;\n")
	if out, err := apply(t, f, "--dry-run"); err != nil || !strings.Contains(out, "1 created, 1 changed, 1 unchanged") {
		t.Fatalf("dry run on a new playbook: %v\n%s", err, out)
	}
	cfg := t.TempDir()
	recipe := writePlaybookFile(t, "ALTER PLAYBOOK ADD PANEL local.clock EXEC 'date';\n")
	captureStderr(t, func() { _, err = apply(t, recipe, "TO", cfg, "--yes") })
	if err != nil {
		t.Fatal(err)
	}
	readManifest(t, filepath.Join(cfg, panelsDirName, "local", "clock.toml"))
}

// A manifest cpb wrote and the pilot then edited (marker kept) is the
// pilot's: ADD over it and DROP of it are refused, naming the file, and it
// is byte-identical after; it is listed as not cpb's and SHOW CREATE leaves
// it out.
func TestPanelsEditedManifestIsThePilots(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	root := seedFlatPlaybook(t, "k")
	mustStmt(t, "ALTER PLAYBOOK k ADD PANEL local.clock TEMPLATE x")
	path := filepath.Join(root, panelsDirName, "local", "clock.toml")
	data, _ := os.ReadFile(path)
	edited := strings.Replace(string(data), `text = "x"`, `text = "mine"`, 1)
	if edited == string(data) {
		t.Fatalf("the edit did not apply:\n%s", data)
	}
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"ALTER PLAYBOOK k ADD PANEL local.clock TEMPLATE y", "ALTER PLAYBOOK k DROP PANEL local.clock"} {
		if _, err := quotedStmt(t, line); err == nil || !strings.Contains(err.Error(), "was edited after cpb wrote it") || !strings.Contains(err.Error(), "statusline.d/local/clock.toml") {
			t.Fatalf("%s: %v", line, err)
		}
	}
	if got, _ := os.ReadFile(path); string(got) != edited {
		t.Fatal("the edited manifest changed")
	}
	if created := mustStmt(t, "SHOW CREATE PLAYBOOK k"); strings.Contains(created, "local.clock") {
		t.Fatalf("SHOW CREATE wrote an edited panel:\n%s", created)
	}
}

// A credential-looking literal in a command or template is refused unless
// AS PLAINTEXT; SHOW CREATE withholds such a panel as a comment.
func TestPanelCredentials(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	seedFlatPlaybook(t, "k")
	for _, line := range []string{
		`ALTER PLAYBOOK k ADD PANEL local.x EXEC 'curl -H "Authorization: Bearer sk-live-0000000000000000" https://x'`,
		`ALTER PLAYBOOK k ADD PANEL local.x EXEC 'API_TOKEN=abcdef0123456789 sh x.sh'`,
		`ALTER PLAYBOOK k ADD PANEL local.x EXEC 'tool --api-key abcdef0123456789'`,
		`ALTER PLAYBOOK k ADD PANEL local.x OBSERVE 'curl https://u:hunter2@x.example/beat' EVERY 10000`,
		`ALTER PLAYBOOK k ADD PANEL local.x TEMPLATE 'password=abcdef0123456789'`,
	} {
		if _, err := quotedStmt(t, line); err == nil || !strings.Contains(err.Error(), "looks like a credential") {
			t.Errorf("%s: %v", line, err)
		}
	}
	for _, line := range []string{
		"ALTER PLAYBOOK k ADD PANEL local.ok EXEC 'date +%H:%M'",
		"ALTER PLAYBOOK k ADD PANEL local.ok2 EXEC 'curl -H \"Accept: text/plain\" https://x.example'",
		"ALTER PLAYBOOK k ADD PANEL local.ok3 EXEC 'MAX_THINKING_TOKENS=8000 sh x.sh'",
	} {
		if _, err := quotedStmt(t, line); err != nil {
			t.Errorf("a plain command was refused: %s: %v", line, err)
		}
	}
	if _, err := quotedStmt(t, `ALTER PLAYBOOK k ADD PANEL local.x EXEC 'curl -H "Authorization: Bearer sk-live-0000000000000000" https://x' AS PLAINTEXT`); err != nil {
		t.Fatalf("AS PLAINTEXT: %v", err)
	}
	created := mustStmt(t, "SHOW CREATE PLAYBOOK k")
	if strings.Contains(created, "sk-live") || !strings.Contains(created, "-- withheld: panel local.x") {
		t.Fatalf("SHOW CREATE:\n%s", created)
	}
	if !strings.Contains(created, "ADD PANEL local.ok EXEC 'date +%H:%M'") {
		t.Fatalf("SHOW CREATE lost a plain panel:\n%s", created)
	}
}
