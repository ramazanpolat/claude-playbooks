package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

func selectFixture(t *testing.T) string {
	t.Helper()
	root := sandboxDefaultRoot(t)
	writePlaybook(t, root, "alpha", &manifest.Manifest{Version: "v3.12.3"})
	writePlaybook(t, root, "beta", &manifest.Manifest{Version: "v3.9.0",
		Env: &manifest.Env{Set: map[string]string{"X_TOKEN": "sk-live-0123456789abcdef", "MODEL": "glm"}}})
	return root
}

func TestSelectBuiltIn(t *testing.T) {
	selectFixture(t)
	out := mustStmt(t, "SELECT name, version FROM PLAYBOOKS")
	if !strings.Contains(out, "alpha") || !strings.Contains(out, "v3.12.3") || !strings.Contains(out, "NAME") {
		t.Fatalf("built in:\n%s", out)
	}
	var got []map[string]any
	var err error
	js := captureStdout(t, func() { err = runStatement([]string{"SELECT name, version_tuple FROM PLAYBOOKS", "--json"}) })
	if err != nil || json.Unmarshal([]byte(js), &got) != nil || len(got) != 2 {
		t.Fatalf("--json: %v\n%s", err, js)
	}
	if !reflect.DeepEqual(got[1]["version_tuple"], []any{float64(3), float64(9), float64(0)}) {
		t.Fatalf("version_tuple: %v", got[1])
	}
	if _, err := stmt(t, "SELECT nmae FROM PLAYBOOKS"); err == nil || !strings.Contains(err.Error(), "unknown column 'nmae'") || !strings.Contains(err.Error(), "quote the statement") {
		t.Fatalf("unknown column: %v", err)
	}
}

func TestSelectNeedsClickHouse(t *testing.T) {
	selectFixture(t)
	t.Setenv("CPB_CLICKHOUSE", "")
	t.Setenv("PATH", t.TempDir())
	if _, err := stmt(t, "SELECT count() FROM PLAYBOOKS"); err == nil || !strings.Contains(err.Error(), "needs ClickHouse (clickhouse local)") {
		t.Fatalf("no ClickHouse: %v", err)
	}
}

// Anything beyond the built-in form goes to clickhouse-local: exactly the
// --json rows on stdin (nothing secret), the query with FROM rewritten.
func TestSelectHandsOffToClickHouse(t *testing.T) {
	selectFixture(t)
	dir := t.TempDir()
	stub := filepath.Join(dir, "clickhouse")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + dir + "/args\"\ncat > \"" + dir + "/stdin\"\necho ok\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CPB_CLICKHOUSE", stub)
	var err error
	out := captureStdout(t, func() {
		err = runStatement([]string{"SELECT name FROM PLAYBOOKS WHERE version_tuple > [3, 10] ORDER BY name"})
	})
	if err != nil || !strings.Contains(out, "ok") {
		t.Fatalf("handoff: %v\n%s", err, out)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	want := strings.Join([]string{"local", "--input-format", "JSONEachRow", "--structure", selectTables["PLAYBOOKS"].structure,
		"-q", "SELECT name FROM (SELECT *, " + versionTupleSQL + " AS version_tuple FROM table) WHERE version_tuple > [3, 10] ORDER BY name"}, "\n") + "\n"
	if string(args) != want {
		t.Fatalf("args:\n%s\nwant:\n%s", args, want)
	}
	// Byte for byte, SHOW PLAYBOOKS --json's objects, one per line.
	stdin, _ := os.ReadFile(filepath.Join(dir, "stdin"))
	var shown []json.RawMessage
	if err := json.Unmarshal([]byte(mustStmt(t, "SHOW PLAYBOOKS --json")), &shown); err != nil || len(shown) != 2 {
		t.Fatalf("SHOW PLAYBOOKS --json: %v", err)
	}
	var exp bytes.Buffer
	for _, r := range shown {
		if err := json.Compact(&exp, r); err != nil {
			t.Fatal(err)
		}
		exp.WriteByte('\n')
	}
	if !bytes.Equal(stdin, exp.Bytes()) {
		t.Fatalf("stdin differs from SHOW PLAYBOOKS --json:\n%s\nwant:\n%s", stdin, exp.Bytes())
	}
	if bytes.Contains(stdin, []byte("sk-live")) {
		t.Fatal("a secret value reached ClickHouse")
	}
	if out := mustStmt(t, "EXPLAIN SELECT count() FROM VARS"); !strings.Contains(out, "engine: clickhouse local") || !strings.Contains(out, "FROM table") {
		t.Fatalf("EXPLAIN SELECT:\n%s", out)
	}
	if out := mustStmt(t, "EXPLAIN SELECT key FROM VARS"); !strings.Contains(out, "engine: built in") {
		t.Fatalf("EXPLAIN SELECT built in:\n%s", out)
	}
}

func TestSelectVarsAndQuotedStatement(t *testing.T) {
	selectFixture(t)
	var got []map[string]any
	var err error
	js := captureStdout(t, func() { err = runStatement([]string{"SELECT playbook, key, redacted, effective FROM VARS;", "--json"}) })
	if err != nil || json.Unmarshal([]byte(js), &got) != nil || len(got) != 2 {
		t.Fatalf("VARS: %v\n%s", err, js)
	}
	for _, r := range got {
		if r["key"] == "X_TOKEN" && r["redacted"] != true {
			t.Fatalf("a credential is not redacted: %v", r)
		}
	}
	// Any statement may be given as one quoted argument.
	var out string
	out = captureStdout(t, func() { err = runStatement([]string{"SHOW PLAYBOOK alpha --json"}) })
	if err != nil || !strings.Contains(out, `"name": "alpha"`) {
		t.Fatalf("quoted SHOW: %v\n%s", err, out)
	}
}

// The FROM that names a table is found as ClickHouse reads the query: not
// inside a string, a quoted identifier or a comment.
func TestScanSQL(t *testing.T) {
	cases := []struct {
		q      string
		tables string
		second bool
	}{
		{"SELECT 'FROM VARS' AS m, name FROM PLAYBOOKS", "PLAYBOOKS", false},
		{"SELECT name FROM PLAYBOOKS -- FROM VARS", "PLAYBOOKS", false},
		{"SELECT name /* FROM VARS */ FROM envs", "ENVS", false},
		{"SELECT 'it''s; FROM VARS', `FROM VARS` FROM vars", "VARS", false},
		{"SELECT 'a\\' FROM VARS' FROM DEFAULTS", "DEFAULTS", false},
		{"SELECT count() FROM PLAYBOOKS; SELECT 1", "PLAYBOOKS", true},
		{"SELECT count() FROM PLAYBOOKS; -- done", "PLAYBOOKS", false},
		{"SELECT a FROM PLAYBOOKS p JOIN (SELECT b FROM VARS) v ON 1", "PLAYBOOKS VARS", false},
		{"SELECT from_x FROM numbers(3)", "", false},
	}
	for _, c := range cases {
		froms, second := scanSQL(c.q)
		var got []string
		for _, f := range froms {
			got = append(got, f.table)
		}
		if strings.Join(got, " ") != c.tables || second != c.second {
			t.Errorf("%s: tables %v second %v, want %q %v", c.q, got, second, c.tables, c.second)
		}
	}
	p, err := planSelect("SELECT 'FROM VARS' AS m FROM VARS")
	if err != nil || p.query != "SELECT 'FROM VARS' AS m FROM table" {
		t.Fatalf("rewrite: %+v %v", p, err)
	}
	if _, err := planSelect("SELECT count() FROM PLAYBOOKS; SELECT 1"); err == nil || !strings.Contains(err.Error(), "one statement") {
		t.Fatalf("a second statement: %v", err)
	}
}

func TestSelectQuotedJSONAndExactColumns(t *testing.T) {
	selectFixture(t)
	t.Setenv("CPB_CLICKHOUSE", "")
	t.Setenv("PATH", t.TempDir())
	var got []map[string]any
	var err error
	js := captureStdout(t, func() { err = runStatement([]string{"SELECT name FROM PLAYBOOKS --json"}) })
	if err != nil || json.Unmarshal([]byte(js), &got) != nil || len(got) != 2 {
		t.Fatalf("quoted --json: %v\n%s", err, js)
	}
	// ClickHouse identifiers are case-sensitive, so the built-in form is too.
	if _, err := stmt(t, "SELECT NAME FROM PLAYBOOKS"); err == nil || !strings.Contains(err.Error(), "unknown column 'NAME'") {
		t.Fatalf("NAME: %v", err)
	}
	if !reflect.DeepEqual(versionTuple("v3.12.3-rc1"), []uint64{3, 12, 3}) || len(versionTuple("")) != 0 || len(versionTuple("dev")) != 0 {
		t.Fatalf("versionTuple: %v", versionTuple("v3.12.3-rc1"))
	}
}
