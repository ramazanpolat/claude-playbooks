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
		"-q", "SELECT name FROM (SELECT * EXCEPT (pilot_profile), " + versionTupleSQL + " AS version_tuple, pilot_profile FROM table) WHERE version_tuple > [3, 10] ORDER BY name"}, "\n") + "\n"
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
		froms, second, _ := scanSQL(c.q)
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

// On a terminal, with no FORMAT in the query, cpb asks clickhouse-local for
// JSONCompact and renders it; a pipe, or a FORMAT of the query's own, gets
// clickhouse-local's output untouched.
func TestSelectOutputChoice(t *testing.T) {
	selectFixture(t)
	old := selectTTY
	t.Cleanup(func() { selectTTY = old })
	args := func(tty bool, q string) string {
		selectTTY = func() bool { return tty }
		p, err := planSelect(q)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(p.clickhouseArgs(), " ")
	}
	if a := args(false, "SELECT count() FROM PLAYBOOKS"); strings.Contains(a, "--output-format") {
		t.Errorf("pipe: %s", a)
	}
	if a := args(true, "SELECT count() FROM PLAYBOOKS"); !strings.Contains(a, "--output-format JSONCompact --output_format_json_escape_forward_slashes=0") {
		t.Errorf("terminal: %s", a)
	}
	if a := args(true, "SELECT count() FROM PLAYBOOKS format TSV"); strings.Contains(a, "--output-format") {
		t.Errorf("the query's FORMAT lost: %s", a)
	}
	for _, q := range []string{
		"SELECT 'FORMAT' AS f FROM PLAYBOOKS -- FORMAT TSV",    // a string and a comment
		"SELECT launcher AS format FROM PLAYBOOKS",             // an alias
		"SELECT format('{}', name) FROM PLAYBOOKS",             // a function
		"SELECT name AS format FROM PLAYBOOKS ORDER BY format", // a column, last
		"SELECT name FROM PLAYBOOKS WHERE name = 'format'",
	} {
		if a := args(true, q); !strings.Contains(a, "JSONCompact") {
			t.Errorf("not the query's FORMAT clause: %s\n%s", q, a)
		}
	}
	for _, q := range []string{
		"SELECT name FROM PLAYBOOKS FORMAT Vertical",
		"SELECT name FROM PLAYBOOKS ORDER BY name FORMAT JSONEachRow SETTINGS max_threads = 1",
	} {
		if a := args(true, q); strings.Contains(a, "--output-format") {
			t.Errorf("the query's FORMAT lost: %s\n%s", q, a)
		}
	}
}

// What cpb prints from clickhouse-local's JSONCompact: a table for a narrow
// result, one block per row for a wide one; objects and arrays of objects
// as JSON with '/' unescaped, NULL as '-'.
func TestSelectRendersForATerminal(t *testing.T) {
	selectFixture(t)
	old := selectTTY
	t.Cleanup(func() { selectTTY = old })
	selectTTY = func() bool { return true }
	dir := t.TempDir()
	stub := filepath.Join(dir, "clickhouse")
	write := func(doc string) {
		script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + dir + "/args\"\ncat >/dev/null\ncat <<'EOF'\n" + doc + "\nEOF\n"
		if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CPB_CLICKHOUSE", stub)
	write(`{"meta":[{"name":"name","type":"String"},{"name":"vars","type":"Array(JSON)"},{"name":"version","type":"Nullable(String)"}],` +
		`"data":[["alpha",[{"key":"K","ref":"https:\/\/x.example\/y"}],null]],"rows":1}`)
	var err error
	out := captureStdout(t, func() { err = runStatement([]string{"SELECT name, vars, version FROM PLAYBOOKS WHERE 1"}) })
	if err != nil || !strings.Contains(out, "NAME") || !strings.Contains(out, `[{"key":"K","ref":"https://x.example/y"}]`) ||
		strings.Contains(out, `\/`) || strings.Contains(out, `\N`) || !strings.Contains(out, " -") {
		t.Fatalf("narrow:\n%s %v", out, err)
	}
	if a, _ := os.ReadFile(filepath.Join(dir, "args")); !strings.Contains(string(a), "--output-format\nJSONCompact\n--output_format_json_escape_forward_slashes=0\n") {
		t.Fatalf("args:\n%s", a)
	}
	// The boundary: six columns are a table, seven go vertical.
	write(`{"meta":[{"name":"a"},{"name":"b"},{"name":"c"},{"name":"d"},{"name":"e"},{"name":"f"}],"data":[["1","2","3","4","5",{}]],"rows":1}`)
	out = captureStdout(t, func() { err = runStatement([]string{"SELECT * FROM PLAYBOOKS WHERE 1"}) })
	if err != nil || strings.Contains(out, "Row 1") || !strings.Contains(out, "F") || strings.Contains(out, "{}") {
		t.Fatalf("six columns:\n%s %v", out, err)
	}
	write(`{"meta":[{"name":"a"},{"name":"b"},{"name":"c"},{"name":"d"},{"name":"e"},{"name":"f"},{"name":"g"}],"data":[["1","2","3","4","5","6",null]],"rows":1}`)
	out = captureStdout(t, func() { err = runStatement([]string{"SELECT * FROM PLAYBOOKS WHERE 1"}) })
	if err != nil || !strings.Contains(out, "Row 1") || !strings.Contains(out, "G:  -") {
		t.Fatalf("seven columns:\n%s %v", out, err)
	}
}

// The built-in form: a wide selection is one block per row on a terminal,
// a table in a pipe, with the same headers.
func TestSelectBuiltInOnATerminal(t *testing.T) {
	selectFixture(t)
	old := selectTTY
	t.Cleanup(func() { selectTTY = old })
	q := "SELECT name, version, path, linked, launcher, envs, sandbox FROM PLAYBOOKS"
	selectTTY = func() bool { return true }
	if out := mustStmt(t, q); !strings.Contains(out, "Row 1") || !strings.Contains(out, "SANDBOX:") {
		t.Fatalf("terminal:\n%s", out)
	}
	selectTTY = func() bool { return false }
	if out := mustStmt(t, q); strings.Contains(out, "Row 1") || !strings.Contains(out, "SANDBOX") {
		t.Fatalf("pipe:\n%s", out)
	}
}

func TestDescribe(t *testing.T) {
	selectFixture(t)
	out := mustStmt(t, "DESCRIBE playbooks")
	for _, want := range []string{"NAME", "TYPE", "version_tuple", "Array(UInt32)", "source", "JSON", "model"} {
		if !strings.Contains(out, want) {
			t.Fatalf("DESCRIBE playbooks lacks %q:\n%s", want, out)
		}
	}
	var cols []columnJSON
	var err error
	js := captureStdout(t, func() { err = runStatement([]string{"DESC TABLE envs --json"}) })
	if err != nil || json.Unmarshal([]byte(js), &cols) != nil || len(cols) != 5 || cols[4] != (columnJSON{Name: "default", Type: "Bool"}) {
		t.Fatalf("DESC TABLE envs --json: %v %v\n%s", err, cols, js)
	}
	if _, err := stmt(t, "DESCRIBE nope"); err == nil || !strings.Contains(err.Error(), `unknown table "nope"`) {
		t.Fatalf("unknown table: %v", err)
	}
	if _, err := stmt(t, "DESCRIBE"); err == nil || !strings.Contains(err.Error(), "needs one table") {
		t.Fatalf("no table: %v", err)
	}
}

// jsonKeys returns each object's keys in the order the text has them.
func jsonKeys(t *testing.T, js string) [][]string {
	t.Helper()
	var rows []json.RawMessage
	if err := json.Unmarshal([]byte(js), &rows); err != nil {
		t.Fatalf("not a JSON array: %v\n%s", err, js)
	}
	var out [][]string
	for _, r := range rows {
		dec := json.NewDecoder(bytes.NewReader(r))
		if _, err := dec.Token(); err != nil { // {
			t.Fatal(err)
		}
		var keys []string
		for dec.More() {
			k, err := dec.Token()
			if err != nil {
				t.Fatal(err)
			}
			keys = append(keys, k.(string))
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				t.Fatal(err)
			}
		}
		out = append(out, keys)
	}
	return out
}

// --json keys follow the query's column order on every table, as
// clickhouse local's do; a map would sort them (#133).
func TestSelectJSONKeyOrder(t *testing.T) {
	selectFixture(t)
	for table, st := range selectTables {
		cols := make([]string, len(st.columns))
		for i, c := range st.columns {
			cols[len(cols)-1-i] = c
		}
		q := "SELECT " + strings.Join(cols, ", ") + " FROM " + table
		var err error
		js := captureStdout(t, func() { err = runStatement([]string{q, "--json"}) })
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		for i, keys := range jsonKeys(t, js) {
			if !reflect.DeepEqual(keys, cols) {
				t.Errorf("%s row %d keys %v, want %v", table, i, keys, cols)
			}
		}
	}
	var err error
	js := captureStdout(t, func() { err = runStatement([]string{"SELECT version, name FROM PLAYBOOKS", "--json"}) })
	if keys := jsonKeys(t, js); err != nil || len(keys) != 2 || !reflect.DeepEqual(keys[0], []string{"version", "name"}) {
		t.Fatalf("SELECT version, name: %v %v\n%s", err, keys, js)
	}
}

// A column named twice is one key, at its first position; text is not
// HTML-escaped, as printJSON's is not.
func TestOrderedRowJSON(t *testing.T) {
	r := orderedRow{cols: []string{"b", "a", "b"}, vals: map[string]any{"a": "x<y&z", "b": []any{1.0, nil}}}
	got, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, got); err != nil {
		t.Fatal(err)
	}
	if want := `{"b":[1,null],"a":"x\u003cy\u0026z"}`; buf.String() != want {
		t.Fatalf("json.Marshal: %s, want %s", buf.String(), want)
	}
	out := captureStdout(t, func() { err = printJSON([]orderedRow{r}) })
	if err != nil || !strings.Contains(out, `"a": "x<y&z"`) || strings.Index(out, `"b"`) > strings.Index(out, `"a"`) {
		t.Fatalf("printJSON:\n%s", out)
	}
}
