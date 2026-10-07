package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
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
	// In a pipe: TSV with a header row (tests run off a terminal).
	out := mustStmt(t, "SELECT name, version FROM PLAYBOOKS")
	if !strings.HasPrefix(out, "name\tversion\n") || !strings.Contains(out, "alpha\tv3.12.3\n") {
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
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + dir + "/args\"\ncat > \"" + dir + "/stdin\"\n" +
		"echo '{\"meta\":[{\"name\":\"name\"}],\"data\":[[\"ok\"]],\"rows\":1}'\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CPB_CLICKHOUSE", stub)
	var err error
	out := captureStdout(t, func() {
		err = runStatement([]string{"SELECT name FROM PLAYBOOKS WHERE version_tuple > [3, 10] ORDER BY name"})
	})
	if err != nil || out != "name\nok\n" {
		t.Fatalf("handoff: %v\n%q", err, out)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	want := strings.Join([]string{"local", "--input-format", "JSONEachRow", "--structure", selectTables["PLAYBOOKS"].structure,
		"--date_time_input_format", "best_effort", "--output-format", "JSONCompact", "--output_format_json_escape_forward_slashes=0", "--output_format_json_quote_64bit_integers=0",
		"-q", "SELECT name FROM (SELECT * EXCEPT (play, apply), " + versionTupleSQL + " AS version_tuple, play, apply FROM table) WHERE version_tuple > [3, 10] ORDER BY name"}, "\n") + "\n"
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

// With no FORMAT in the query, cpb asks clickhouse-local for JSONCompact and
// prints it itself, on a terminal and in a pipe; a FORMAT of the query's own
// gets clickhouse-local's output untouched.
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
	for _, tty := range []bool{false, true} {
		if a := args(tty, "SELECT count() FROM PLAYBOOKS"); !strings.Contains(a, "--output-format JSONCompact --output_format_json_escape_forward_slashes=0 --output_format_json_quote_64bit_integers=0") {
			t.Errorf("terminal %v: %s", tty, a)
		}
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
// TSV with a header row in a pipe.
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
	if out := mustStmt(t, q); strings.Contains(out, "Row 1") || !strings.HasPrefix(out, "name\tversion\tpath\tlinked\tlauncher\tenvs\tsandbox\n") {
		t.Fatalf("pipe:\n%s", out)
	}
}

// Both engines print a result the same way: the same rows through the
// built-in form and through clickhouse-local give the same bytes in a pipe
// and with --json. A FORMAT of the query's own cannot be combined with
// --json, and a TSV cell escapes a tab, a line break and a backslash.
func TestSelectSameShapeOnBothEngines(t *testing.T) {
	selectFixture(t)
	old := selectTTY
	t.Cleanup(func() { selectTTY = old })
	selectTTY = func() bool { return false }
	dir := t.TempDir()
	stub := filepath.Join(dir, "clickhouse")
	write := func(doc string) {
		script := "#!/bin/sh\ncat >/dev/null\ncat <<'EOF'\n" + doc + "\nEOF\n"
		if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CPB_CLICKHOUSE", stub)
	write(`{"meta":[{"name":"name","type":"String"},{"name":"version","type":"Nullable(String)"}],"data":[["alpha","v3.12.3"],["beta","v3.9.0"]],"rows":2}`)
	for _, flags := range [][]string{nil, {"--json"}} {
		builtIn := captureStdout(t, func() {
			if err := runStatement(append([]string{"SELECT name, version FROM PLAYBOOKS"}, flags...)); err != nil {
				t.Fatal(err)
			}
		})
		viaClickHouse := captureStdout(t, func() {
			if err := runStatement(append([]string{"SELECT name, version FROM PLAYBOOKS WHERE 1"}, flags...)); err != nil {
				t.Fatal(err)
			}
		})
		if builtIn != viaClickHouse {
			t.Errorf("%v: the engines differ:\nbuilt in:\n%s\nclickhouse:\n%s", flags, builtIn, viaClickHouse)
		}
	}
	write(`{"meta":[{"name":"n","type":"UInt64"},{"name":"s\tt","type":"String"}],"data":[[18446744073709551615,"a\tb\\c\nd"]],"rows":1}`)
	out := captureStdout(t, func() {
		if err := runStatement([]string{"SELECT count() AS n, 'x' AS s FROM PLAYBOOKS"}); err != nil {
			t.Fatal(err)
		}
	})
	if out != "n\ts\\tt\n18446744073709551615\ta\\tb\\\\c\\nd\n" {
		t.Errorf("TSV escaping: %q", out)
	}
	js := captureStdout(t, func() {
		if err := runStatement([]string{"SELECT count() AS n, 'x' AS s FROM PLAYBOOKS", "--json"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(js, `"n": 18446744073709551615`) {
		t.Errorf("--json through clickhouse: a UInt64 must stay exact: %s", js)
	}
	if _, err := stmt(t, "SELECT name FROM PLAYBOOKS FORMAT TSV --json"); err == nil || !strings.Contains(err.Error(), "--json and a FORMAT in the query cannot be combined") {
		t.Errorf("--json with a FORMAT: %v", err)
	}
}

func TestDescribe(t *testing.T) {
	selectFixture(t)
	old := selectTTY
	t.Cleanup(func() { selectTTY = old })
	selectTTY = func() bool { return true }
	out := mustStmt(t, "DESCRIBE playbooks")
	for _, want := range []string{"NAME", "TYPE", "COMMENT", "version_tuple", "Array(UInt32)", "source", "JSON", "model", "to compare and sort versions"} {
		if !strings.Contains(out, want) {
			t.Fatalf("DESCRIBE playbooks on a terminal lacks %q:\n%s", want, out)
		}
	}
	selectTTY = func() bool { return false }
	tsv := mustStmt(t, "DESC TABLE defaults")
	if want := "name\ttype\tcomment\nenvs\tArray(String)\tThe env sets every launch applies first, in order.\n"; !strings.HasPrefix(tsv, want) || strings.Count(tsv, "\n") != 3 {
		t.Fatalf("DESC TABLE defaults in a pipe: TSV with a header row:\n%q", tsv)
	}
	var cols []columnJSON
	var err error
	js := captureStdout(t, func() { err = runStatement([]string{"DESC TABLE envs --json"}) })
	if err != nil || json.Unmarshal([]byte(js), &cols) != nil || len(cols) != 5 ||
		cols[4] != (columnJSON{Name: "default", Type: "Bool", Comment: "True when the env set is in DEFAULTS."}) {
		t.Fatalf("DESC TABLE envs --json: %v %v\n%s", err, cols, js)
	}
	if keys := jsonKeys(t, js); len(keys) != 5 || strings.Join(keys[0], " ") != "name type comment" {
		t.Fatalf("--json keys: %v", keys)
	}
	if _, err := stmt(t, "DESCRIBE nope"); err == nil || !strings.Contains(err.Error(), `unknown table "nope"`) {
		t.Fatalf("unknown table: %v", err)
	}
	for _, line := range []string{"DESCRIBE", "DESCRIBE playbooks envs"} {
		if _, err := stmt(t, line); err == nil || !strings.Contains(err.Error(), "needs one table") {
			t.Fatalf("%s: %v", line, err)
		}
	}
}

// TestDescribeEveryColumnHasAComment: DESCRIBE's comment says what each
// column means, so none may be empty, and a comment names no column the
// table lacks.
func TestDescribeEveryColumnHasAComment(t *testing.T) {
	for name, tbl := range selectTables {
		cols, err := describeTable(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range cols {
			if c.Type == "" || strings.TrimSpace(c.Comment) == "" {
				t.Errorf("%s.%s: type %q, comment %q", name, c.Name, c.Type, c.Comment)
			}
		}
		if len(tbl.comments) != len(tbl.columns) {
			t.Errorf("%s: %d comments for %d columns", name, len(tbl.comments), len(tbl.columns))
		}
	}
}

// TestDescribeMatchesSpec holds SPEC.md's DESCRIBE column tables to the
// code, line for line: every table, every column in order, its type and its
// comment.
func TestDescribeMatchesSpec(t *testing.T) {
	data, err := os.ReadFile("../SPEC.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	start := strings.Index(text, "\n### DESCRIBE\n")
	if start < 0 {
		t.Fatal("SPEC.md has no DESCRIBE section")
	}
	section := text[start+1:]
	for _, next := range []string{"\n## ", "\n### "} {
		if end := strings.Index(section, next); end >= 0 {
			section = section[:end]
		}
	}
	spec := map[string][]columnJSON{}
	table := ""
	row := regexp.MustCompile("^\\| `([^`]+)` \\| `([^`]+)` \\| (.+) \\|$")
	for _, line := range strings.Split(section, "\n") {
		if strings.HasPrefix(line, "**`") && strings.HasSuffix(line, "`**") {
			table = strings.TrimSuffix(strings.TrimPrefix(line, "**`"), "`**")
			continue
		}
		if !strings.HasPrefix(line, "|") || line == "| Column | Type | Comment |" || line == "|---|---|---|" {
			continue
		}
		m := row.FindStringSubmatch(line)
		if m == nil || table == "" {
			t.Errorf("SPEC.md's DESCRIBE has a row this test cannot read: %q", line)
			continue
		}
		spec[table] = append(spec[table], columnJSON{Name: m[1], Type: m[2], Comment: m[3]})
	}
	if len(spec) != len(selectTables) {
		t.Fatalf("SPEC.md's DESCRIBE lists %d tables, the code has %d", len(spec), len(selectTables))
	}
	for name := range selectTables {
		cols, _ := describeTable(name)
		got := spec[name]
		if len(got) != len(cols) {
			t.Errorf("%s: SPEC.md lists %d columns, the code %d", name, len(got), len(cols))
			continue
		}
		for i := range cols {
			if got[i] != cols[i] {
				t.Errorf("%s column %d:\n SPEC.md %+v\n code    %+v", name, i+1, got[i], cols[i])
			}
		}
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
