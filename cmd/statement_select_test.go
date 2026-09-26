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
		"--output-format", "PrettyCompact", "-q", "SELECT name FROM table WHERE version_tuple > [3, 10] ORDER BY name"}, "\n") + "\n"
	if string(args) != want {
		t.Fatalf("args:\n%s\nwant:\n%s", args, want)
	}
	stdin, _ := os.ReadFile(filepath.Join(dir, "stdin"))
	rows, _ := playbookRows()
	var exp bytes.Buffer
	for _, r := range rows {
		line, _ := json.Marshal(r)
		exp.Write(line)
		exp.WriteByte('\n')
	}
	if !bytes.Equal(stdin, exp.Bytes()) {
		t.Fatalf("stdin differs from the --json rows:\n%s\nwant:\n%s", stdin, exp.Bytes())
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
