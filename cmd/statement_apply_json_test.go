package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
)

// applyJSON runs APPLY … --json and returns the report, its raw text and
// the exit code the command would end with.
func applyJSON(t *testing.T, args ...string) (map[string]any, string, int) {
	t.Helper()
	var err error
	out := captureStdout(t, func() { err = runStatement(append([]string{"APPLY"}, args...)) })
	code := 0
	if err != nil {
		c, ok := exitCode(err)
		if !ok {
			t.Fatalf("APPLY --json returned a plain error: %v\n%s", err, out)
		}
		code = c
	}
	var rep map[string]any
	if jerr := json.Unmarshal([]byte(out), &rep); jerr != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", jerr, out)
	}
	return rep, out, code
}

func stmts(rep map[string]any) []map[string]any {
	var out []map[string]any
	for _, s := range rep["statements"].([]any) {
		out = append(out, s.(map[string]any))
	}
	return out
}

func treeOf(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil {
			b.WriteString(p + "\n")
		}
		return nil
	})
	return b.String()
}

// The plan: schema and version, resolved files, a target on every
// statement (the implicit CREATE of a missing TO target included), stable
// warning codes, the summary; and a dry run creates nothing, even when
// the store does not exist yet.
func TestApplyJSONPlan(t *testing.T) {
	resetCommandTestState(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	store := filepath.Join(home, "store")
	t.Setenv("CPB_PLAYBOOKS_DIR", store)
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	writeCpb(t, dir, "base.cpb", "CREATE OR REPLACE ENV router SET BASE=http://router.invalid;\n")
	main := writeCpb(t, dir, "main.cpb", "INCLUDE 'base.cpb';\nUSE PLAYBOOK other;\nALTER PLAYBOOK USE ENV router;\nCREATE PLAYBOOK IF NOT EXISTS named SET launcher = '';\n")
	before := treeOf(t, home)

	rep, _, code := applyJSON(t, main, "TO", "fresh", "--dry-run", "--json")
	if code != 0 || rep["ok"] != true || rep["error"] != nil {
		t.Fatalf("plan: code %d, %v", code, rep)
	}
	if rep["schema"] != float64(1) || rep["cpb_version"] != Version {
		t.Fatalf("schema/version: %v %v", rep["schema"], rep["cpb_version"])
	}
	if files := rep["files"].([]any); len(files) != 2 || files[0] != main || files[1] != filepath.Join(dir, "base.cpb") {
		t.Fatalf("files: %v", files)
	}
	if tg := rep["target"].(map[string]any); tg["kind"] != "playbook" || tg["name"] != "fresh" {
		t.Fatalf("target: %v", tg)
	}
	ss := stmts(rep)
	if len(ss) != 4 {
		t.Fatalf("statements: %d\n%v", len(ss), ss)
	}
	for _, s := range ss {
		if s["target"] == nil {
			t.Fatalf("a statement without a target: %v", s)
		}
	}
	if ss[0]["file"] != filepath.Join(dir, "base.cpb") || ss[0]["line"] != float64(1) || ss[0]["verdict"] != "created" {
		t.Fatalf("included statement: %v", ss[0])
	}
	if ss[1]["implicit"] != true || ss[1]["verdict"] != "created" || ss[1]["statement"] != "CREATE PLAYBOOK fresh" {
		t.Fatalf("implicit create: %v", ss[1])
	}
	if ss[2]["recipe"] != true || ss[2]["target"].(map[string]any)["name"] != "fresh" || ss[2]["line"] != float64(3) {
		t.Fatalf("recipe: %v", ss[2])
	}
	if ss[3]["recipe"] != false || ss[3]["target"].(map[string]any)["name"] != "named" {
		t.Fatalf("an explicit name stays literal: %v", ss[3])
	}
	ws := rep["warnings"].([]any)
	if len(ws) != 1 {
		t.Fatalf("warnings: %v", ws)
	}
	if w := ws[0].(map[string]any); w["code"] != "use_playbook_overridden" || w["file"] != main || w["line"] != float64(2) {
		t.Fatalf("warning: %v", w)
	}
	sum := rep["summary"].(map[string]any)
	if sum["created"] != float64(3) || sum["changed"] != float64(1) || sum["warnings"] != float64(1) || sum["refused"] != float64(0) {
		t.Fatalf("summary: %v", sum)
	}
	if after := treeOf(t, home); after != before {
		t.Fatalf("the dry run wrote:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if _, err := os.Stat(store); !os.IsNotExist(err) {
		t.Fatal("the dry run created the store")
	}
}

// Actions: absolute argv and paths, network flags, only non-secret env,
// references as references, and no secret value anywhere in the output.
func TestApplyJSONActionsAndSecrets(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	fakeClaude(t)
	fakeMCP(t)
	helper, _ := fakeHelper(t)
	root := seedFlatPlaybook(t, "k")
	mustStmt(t, "ALTER DEFAULTS SET SECRET HELPER "+helper)
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	mkt := filepath.Join(dir, "mkt")
	if err := os.MkdirAll(filepath.Join(mkt, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mkt, ".claude-plugin", "marketplace.json"), []byte(`{"name":"mkt"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	makeSkill(t, filepath.Join(dir, "notes"))
	secret := "sk-live-0123456789abcdef"
	f := writeCpb(t, dir, "agent.cpb", "ALTER PLAYBOOK k\n"+
		"  ADD MARKETPLACE mkt FROM './mkt'\n"+
		"  ADD PLUGIN hello@mkt\n"+
		"  ADD MCP SERVER sentry URL 'https://mcp.example/mcp' HEADER 'Authorization' FROM 'keychain:ok/sentry'\n"+
		"  ADD SKILL notes FROM './notes'\n"+
		"  SET VAR X_TOKEN="+secret+" AS PLAINTEXT;\n")

	rep, out, code := applyJSON(t, f, "--dry-run", "--json")
	if code != 0 {
		t.Fatalf("code %d\n%s", code, out)
	}
	if strings.Contains(out, secret) || strings.Contains(out, "resolved") {
		t.Fatalf("a secret value reached the JSON:\n%s", out)
	}
	acts := stmts(rep)[0]["actions"].([]any)
	find := func(pred func(map[string]any) bool) map[string]any {
		for _, a := range acts {
			if m := a.(map[string]any); pred(m) {
				return m
			}
		}
		t.Fatalf("no such action in %v", acts)
		return nil
	}
	argv := func(m map[string]any) string {
		var parts []string
		for _, a := range m["argv"].([]any) {
			parts = append(parts, a.(string))
		}
		return strings.Join(parts, " ")
	}
	add := find(func(m map[string]any) bool {
		return m["type"] == "command" && strings.Contains(argv(m), "marketplace add")
	})
	if argv(add) != "claude plugin marketplace add "+mkt+" --scope user" || add["network"] != false {
		t.Fatalf("marketplace add: %v", add)
	}
	inst := find(func(m map[string]any) bool {
		return m["type"] == "command" && strings.Contains(argv(m), "plugin install")
	})
	if inst["network"] != true {
		t.Fatalf("install: %v", inst)
	}
	for _, a := range acts {
		m := a.(map[string]any)
		if m["type"] != "command" {
			continue
		}
		env := m["env"].(map[string]any)
		if len(env) != 1 || env["CLAUDE_CONFIG_DIR"] != root {
			t.Fatalf("env beyond CLAUDE_CONFIG_DIR: %v", m)
		}
	}
	mcp := find(func(m map[string]any) bool {
		return m["type"] == "command" && strings.Contains(argv(m), "mcp add-json sentry")
	})
	refs := mcp["refs"].(map[string]any)
	if len(refs) != 1 {
		t.Fatalf("mcp refs: %v", mcp)
	}
	for v, ref := range refs {
		if ref != "keychain:ok/sentry" || !strings.Contains(argv(mcp), "${"+v+"}") {
			t.Fatalf("mcp ref %s=%v: %v", v, ref, mcp)
		}
	}
	sk := find(func(m map[string]any) bool { return m["type"] == "skill" })
	if sk["op"] != "link" || sk["path"] != filepath.Join(root, "skills", "notes") || sk["source"] != filepath.Join(dir, "notes") || sk["network"] != false {
		t.Fatalf("skill: %v", sk)
	}
}

// DROP PLAYBOOK lists what it deletes, with its size on disk.
func TestApplyJSONDelete(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	root := seedFlatPlaybook(t, "gone")
	if err := os.WriteFile(filepath.Join(root, "data.txt"), []byte(strings.Repeat("x", 1000)), 0o644); err != nil {
		t.Fatal(err)
	}
	f := writePlaybookFile(t, "DROP PLAYBOOK gone;\n")
	rep, out, code := applyJSON(t, f, "--dry-run", "--json")
	if code != 0 {
		t.Fatalf("code %d\n%s", code, out)
	}
	s := stmts(rep)[0]
	d := s["actions"].([]any)[0].(map[string]any)
	if s["verdict"] != "dropped" || d["type"] != "delete" || d["what"] != "playbook" || d["path"] != root || d["bytes"].(float64) < 1000 {
		t.Fatalf("delete: %v", s)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatal("the dry run deleted")
	}
}

// Exit 1 when the files are refused: a refused statement ends the plan with
// its reason; a refusal before any statement is the report's error, with
// its file and line. Exit 2 for a usage error. stdout is JSON every time.
func TestApplyJSONRefusals(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	helper, _ := fakeHelper(t)
	mustStmt(t, "ALTER DEFAULTS SET SECRET HELPER "+helper)

	f := writePlaybookFile(t, "CREATE OR REPLACE ENV e SET A=1;\nALTER PLAYBOOK ghost SET VAR B=1;\nCREATE OR REPLACE ENV later;\n")
	rep, _, code := applyJSON(t, f, "--dry-run", "--json")
	ss := stmts(rep)
	if code != 1 || rep["ok"] != false || rep["error"] != nil || len(ss) != 2 {
		t.Fatalf("refused statement: code %d, %v", code, rep)
	}
	if last := ss[1]; last["verdict"] != "refused" || !strings.Contains(last["reason"].(string), "ghost") || last["line"] != float64(2) {
		t.Fatalf("refused: %v", last)
	}

	g := writePlaybookFile(t, "CREATE OR REPLACE ENV e;\nALTER ENV e SET K FROM 'keychain:gone/k';\n")
	rep, _, code = applyJSON(t, g, "--dry-run", "--json")
	e, _ := rep["error"].(map[string]any)
	gr, _ := filepath.EvalSymlinks(g) // the report names files resolved
	if code != 1 || len(stmts(rep)) != 0 || e == nil || e["file"] != gr || e["line"] != float64(2) {
		t.Fatalf("preflight: code %d, %v", code, rep)
	}

	rep, _, code = applyJSON(t, g, "--json")
	if code != 2 || rep["ok"] != false || !strings.Contains(rep["error"].(map[string]any)["message"].(string), "--json needs --dry-run") {
		t.Fatalf("usage: code %d, %v", code, rep)
	}
	rep, _, code = applyJSON(t, filepath.Join(t.TempDir(), "missing.cpb"), "--dry-run", "--json")
	if code != 2 || rep["error"] == nil {
		t.Fatalf("missing file: code %d, %v", code, rep)
	}
}

// CREATE PLAYBOOK … FROM plans the fetch it would run, and runs none.
func TestApplyJSONFetch(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	f := writePlaybookFile(t, "CREATE PLAYBOOK IF NOT EXISTS gitpb FROM https://git.example/x.git BRANCH v1 SET launcher = '';\n")
	rep, out, code := applyJSON(t, f, "--dry-run", "--json")
	if code != 0 {
		t.Fatalf("code %d\n%s", code, out)
	}
	a := stmts(rep)[0]["actions"].([]any)[0].(map[string]any)
	if a["type"] != "fetch" || a["source"] != "https://git.example/x.git" || a["branch"] != "v1" || a["network"] != true ||
		a["to"] != filepath.Join(config.ResolvePlaybooksDir(), "gitpb") {
		t.Fatalf("fetch: %v", a)
	}
}

// A command line that does not parse still answers in JSON, exit 2; a
// local FROM source is local by its form, and absolute.
func TestApplyJSONParseErrorAndLocalFetch(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	rep, _, code := applyJSON(t, "--dry-run", "--json")
	if code != 2 || rep["ok"] != false || rep["error"] == nil || rep["schema"] != float64(1) {
		t.Fatalf("parse error: code %d, %v", code, rep)
	}
	missing := filepath.Join(t.TempDir(), "not-yet")
	f := writePlaybookFile(t, "CREATE PLAYBOOK IF NOT EXISTS localpb FROM "+missing+" SET launcher = '';\n")
	rep, out, code := applyJSON(t, f, "--dry-run", "--json")
	if code != 0 {
		t.Fatalf("code %d\n%s", code, out)
	}
	a := stmts(rep)[0]["actions"].([]any)[0].(map[string]any)
	if a["type"] != "fetch" || a["source"] != missing || a["network"] != false {
		t.Fatalf("local fetch: %v", a)
	}
}

// A syntax error in a file named by a relative path: the report's
// error.file is the resolved path, as everywhere else in the report.
func TestApplyJSONParseErrorFileResolved(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	writeCpb(t, dir, "bad.cpb", "CREATE OR REPLACE ENV e;\nALTER PLAYBOOK k SET SET;\n")
	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	rep, _, code := applyJSON(t, "bad.cpb", "--dry-run", "--json")
	e, _ := rep["error"].(map[string]any)
	if code != 1 || e == nil || e["file"] != filepath.Join(dir, "bad.cpb") || e["line"] != float64(2) {
		t.Fatalf("parse error: code %d, %v", code, rep)
	}
}
