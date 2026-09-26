package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envprofile"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

// SELECT (docs/reference/cli-grammar.md, "SELECT"). cpb answers one form
// itself, SELECT <col>[, <col>…] FROM <table>, a strict subset of
// ClickHouse SQL, so a query means the same on both paths. Anything else is
// handed to ClickHouse's clickhouse-local, when it is installed: the
// table's rows go to its stdin as the same objects SHOW … --json prints
// (secrets redacted, references as references), and nothing else.

// selectTable is one queryable table: its columns in order, the typed
// structure clickhouse-local reads its rows with, and the rows: the objects
// SHOW … --json prints, one per row.
type selectTable struct {
	columns   []string
	structure string
	rows      func() ([]any, error)
}

// versionPattern is the leading numeric part of a version ("v3.12.3-rc1":
// "3.12.3"); version_tuple is its numbers. Both paths compute it the same
// way: in Go for the built-in form, in the query for ClickHouse, so the
// rows handed over stay exactly SHOW's.
const versionPattern = `^v?([0-9]+([.][0-9]+)*)`

var versionRe = regexp.MustCompile(versionPattern)

// versionTupleSQL is version_tuple as a ClickHouse expression.
const versionTupleSQL = "if(extract(ifNull(version, ''), '" + versionPattern + "') = '', CAST([] AS Array(UInt32)), " +
	"arrayMap(x -> toUInt32(x), splitByChar('.', extract(ifNull(version, ''), '" + versionPattern + "'))))"

var selectTables = map[string]selectTable{
	"PLAYBOOKS": {
		columns: []string{"name", "version", "version_tuple", "path", "source", "linked", "launcher", "envs", "vars", "sandbox",
			"marketplaces", "plugins", "agent", "mcp_servers", "tools", "skills", "statusline", "model"},
		structure: "name String, version Nullable(String), path String, source JSON, linked Nullable(String), " +
			"launcher Nullable(String), envs Array(String), vars Array(JSON), sandbox Bool, marketplaces Array(JSON), plugins Array(JSON), " +
			"agent Nullable(String), mcp_servers Array(JSON), tools JSON, skills Array(JSON), statusline Nullable(String), model Nullable(String)",
		rows: playbookRows,
	},
	"ENVS": {
		columns:   []string{"name", "description", "vars", "used_by", "default"},
		structure: "name String, description String, vars Array(JSON), used_by Array(String), `default` Bool",
		rows:      envRows,
	},
	"VARS": {
		columns:   []string{"playbook", "key", "value", "ref", "redacted", "plaintext", "blocked", "layer", "effective"},
		structure: "playbook String, key String, value Nullable(String), ref Nullable(String), redacted Bool, plaintext Bool, blocked Bool, layer JSON, effective Bool",
		rows:      varRows,
	},
	"DEFAULTS": {
		columns:   []string{"envs", "secret_helper"},
		structure: "envs Array(String), secret_helper JSON",
		rows:      defaultsRows,
	},
}

// varRowJSON is one VARS row: a variable of one layer of one playbook.
type varRowJSON struct {
	Playbook string `json:"playbook"`
	varJSON
	Effective bool `json:"effective"`
}

func toRow(v any) (map[string]any, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	return m, json.Unmarshal(data, &m)
}

// versionTuple turns "v3.12.3" into [3 12 3], so versions sort and compare
// as numbers ("v3.9.0" < "v3.12.3").
func versionTuple(v string) []uint64 {
	out := []uint64{}
	m := versionRe.FindStringSubmatch(v)
	if m == nil {
		return out
	}
	for _, part := range strings.Split(m[1], ".") {
		n, err := strconv.ParseUint(part, 10, 32)
		if err != nil {
			break
		}
		out = append(out, n)
	}
	return out
}

func playbookRows() ([]any, error) {
	pbs, err := playbook.Discover(config.ResolvePlaybooksDir())
	if err != nil {
		return nil, err
	}
	rows := []any{}
	for _, pb := range pbs {
		rows = append(rows, describePlaybook(pb))
	}
	return rows, nil
}

func envRows() ([]any, error) {
	playbooksDir := config.ResolvePlaybooksDir()
	dir := envprofile.Dir(playbooksDir)
	profiles, err := envprofile.List(dir)
	if err != nil {
		return nil, err
	}
	users, err := profileUsers(playbooksDir)
	if err != nil {
		return nil, err
	}
	defaults, _ := envprofile.Defaults(dir)
	rows := []any{}
	for _, p := range profiles {
		rows = append(rows, envJSON{Name: p.Name, Description: p.Description,
			Vars: layerVars(p.Set, p.Refs, p.Unset), UsedBy: nonNil(users[p.Name]),
			Default: isRegistryDefault(dir, defaults, p.Name)})
	}
	return rows, nil
}

// varRows is one row per variable, per layer, per playbook: what EXPLAIN
// shows for every layer, with effective marking the entry a launch uses.
func varRows() ([]any, error) {
	playbooksDir := config.ResolvePlaybooksDir()
	dir := envprofile.Dir(playbooksDir)
	pbs, err := playbook.Discover(playbooksDir)
	if err != nil {
		return nil, err
	}
	rows := []any{}
	for _, pb := range pbs {
		// The launch reads the governing manifest; so does this table.
		governing, _ := governingManifest(pb)
		var env *manifest.Env
		if governing != nil {
			env = governing.Env
		}
		origins, err := envprofile.ExplainAll(dir, env)
		if err != nil {
			continue // EXPLAIN reports a broken layer; the table leaves it out
		}
		for _, o := range origins {
			v := varJSON{Key: o.Key, Blocked: o.Blocked}
			switch {
			case o.Ref != "":
				v = varJSON{Key: o.Key, Ref: strPtr(o.Ref)}
			case !o.Blocked:
				v = literalVar(o.Key, o.Value)
			}
			v.Layer = &layerJSON{Kind: o.Kind, Name: o.Set}
			rows = append(rows, varRowJSON{Playbook: pb.Name, varJSON: v, Effective: o.Effective})
		}
	}
	return rows, nil
}

func defaultsRows() ([]any, error) {
	dir := envprofile.Dir(config.ResolvePlaybooksDir())
	names, err := envprofile.Defaults(dir)
	if err != nil {
		return nil, err
	}
	helper, err := helperInEffect(dir)
	if err != nil {
		return nil, err
	}
	return []any{defaultsJSON{Envs: nonNil(names), SecretHelper: helper}}, nil
}

var builtinSelect = regexp.MustCompile(`(?is)^\s*SELECT\s+([A-Za-z_][A-Za-z0-9_]*(?:\s*,\s*[A-Za-z_][A-Za-z0-9_]*)*)\s+FROM\s+([A-Za-z_]+)\s*;?\s*$`)

// selectPlan is how a query runs: built in, or through clickhouse-local.
type selectPlan struct {
	table   string
	columns []string // built in
	query   string   // for ClickHouse, with FROM <table> rewritten to read stdin
}

// sqlFrom is one FROM <table> the scan found: the span to rewrite.
type sqlFrom struct {
	start, end int
	table      string
}

// scanSQL reads a query as ClickHouse's lexer does, skipping string
// literals, quoted identifiers and comments, and returns every FROM that
// names one of the tables, and whether code follows a semicolon (a second
// statement).
func scanSQL(q string) (froms []sqlFrom, second bool) {
	isWord := func(c byte) bool {
		return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
	}
	// skip returns the index after the non-code (quote or comment) at i, or
	// i when there is none.
	skip := func(i int) int {
		switch c := q[i]; {
		case c == '\'' || c == '"' || c == '`':
			for j := i + 1; j < len(q); j++ {
				switch q[j] {
				case '\\':
					j++
				case c:
					if j+1 < len(q) && q[j+1] == c {
						j++
						continue
					}
					return j + 1
				}
			}
			return len(q)
		case c == '#' || c == '-' && strings.HasPrefix(q[i:], "--"):
			if n := strings.IndexByte(q[i:], '\n'); n >= 0 {
				return i + n + 1
			}
			return len(q)
		case c == '/' && strings.HasPrefix(q[i:], "/*"):
			if n := strings.Index(q[i+2:], "*/"); n >= 0 {
				return i + 2 + n + 2
			}
			return len(q)
		}
		return i
	}
	semicolon := false
	for i := 0; i < len(q); {
		if j := skip(i); j != i {
			i = j
			continue
		}
		c := q[i]
		switch {
		case c == ';':
			semicolon = true
			i++
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			i++
		case isWord(c):
			j := i
			for j < len(q) && isWord(q[j]) {
				j++
			}
			if semicolon {
				second = true
			}
			if strings.EqualFold(q[i:j], "FROM") && (i == 0 || !isWord(q[i-1])) {
				k := j
				for k < len(q) && (q[k] == ' ' || q[k] == '\t' || q[k] == '\r' || q[k] == '\n') {
					k++
				}
				e := k
				for e < len(q) && isWord(q[e]) {
					e++
				}
				if name := strings.ToUpper(q[k:e]); e > k {
					if _, ok := selectTables[name]; ok {
						froms = append(froms, sqlFrom{start: i, end: e, table: name})
					}
				}
			}
			i = j
		default:
			if semicolon {
				second = true
			}
			i++
		}
	}
	return froms, second
}

func planSelect(q string) (*selectPlan, error) {
	if m := builtinSelect.FindStringSubmatch(q); m != nil {
		table := strings.ToUpper(m[2])
		t, ok := selectTables[table]
		if !ok {
			return nil, fmt.Errorf("unknown table %q (tables: PLAYBOOKS, ENVS, VARS, DEFAULTS)", m[2])
		}
		var cols []string
		for _, c := range strings.Split(m[1], ",") {
			c = strings.TrimSpace(c)
			// Exact spelling: ClickHouse identifiers are case-sensitive, and
			// the built-in form means what ClickHouse would.
			known := false
			for _, k := range t.columns {
				if k == c {
					known = true
				}
			}
			if !known {
				return nil, fmt.Errorf("unknown column '%s' (columns: %s). If you typed * unquoted, the shell expanded it: quote the statement", c, strings.Join(t.columns, " "))
			}
			cols = append(cols, c)
		}
		return &selectPlan{table: table, columns: cols}, nil
	}
	froms, second := scanSQL(q)
	if second {
		return nil, errors.New("one statement at a time: remove what follows the semicolon")
	}
	if len(froms) == 0 {
		return nil, errors.New("a query reads FROM one of the tables: PLAYBOOKS, ENVS, VARS, DEFAULTS")
	}
	if len(froms) > 1 {
		return nil, errors.New("a query reads one table; join them in ClickHouse yourself: cpb SHOW … --json | clickhouse local …")
	}
	f := froms[0]
	source := "FROM table"
	if f.table == "PLAYBOOKS" {
		source = "FROM (SELECT *, " + versionTupleSQL + " AS version_tuple FROM table)"
	}
	return &selectPlan{table: f.table, query: q[:f.start] + source + q[f.end:]}, nil
}

// clickhouseBinary finds clickhouse-local: CPB_CLICKHOUSE, else
// `clickhouse`, else `ch` on PATH.
func clickhouseBinary() (string, error) {
	if p := os.Getenv("CPB_CLICKHOUSE"); p != "" {
		return p, nil
	}
	for _, name := range []string{"clickhouse", "ch"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", errors.New("this query needs ClickHouse (clickhouse local); install it, or pick columns only: SELECT <col>, … FROM <table>")
}

func (p *selectPlan) clickhouseArgs() []string {
	// No --output-format: it would override the query's own FORMAT, and
	// clickhouse-local already prints a table to a terminal and TSV to a pipe.
	return []string{"local", "--input-format", "JSONEachRow", "--structure", selectTables[p.table].structure, "-q", p.query}
}

// runSelect runs a SELECT, or with explain says how it would run.
func runSelect(q string, explain, asJSON bool) error {
	p, err := planSelect(q)
	if err != nil {
		return err
	}
	if explain {
		if p.query == "" {
			fmt.Printf("engine: built in (SELECT <columns> FROM %s)\n", p.table)
			return nil
		}
		bin, err := clickhouseBinary()
		if err != nil {
			return err
		}
		fmt.Printf("engine: clickhouse local\ncommand: cpb SHOW … --json rows of %s | %s\n", p.table, shellCommand(append([]string{bin}, p.clickhouseArgs()...)))
		return nil
	}
	rows, err := selectTables[p.table].rows()
	if err != nil {
		return err
	}
	if p.query == "" {
		return printSelect(p.table, p.columns, rows, asJSON)
	}
	bin, err := clickhouseBinary()
	if err != nil {
		return err
	}
	var in bytes.Buffer
	for _, r := range rows { // each object as SHOW … --json prints it, one per line
		line, err := json.Marshal(r)
		if err != nil {
			return err
		}
		in.Write(line)
		in.WriteByte('\n')
	}
	c := exec.Command(bin, p.clickhouseArgs()...)
	c.Stdin, c.Stdout, c.Stderr = &in, os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("clickhouse local: %w", err)
	}
	return nil
}

func printSelect(table string, cols []string, objs []any, asJSON bool) error {
	rows := make([]map[string]any, 0, len(objs))
	for _, o := range objs {
		row, err := toRow(o)
		if err != nil {
			return err
		}
		if table == "PLAYBOOKS" {
			version, _ := row["version"].(string)
			row["version_tuple"] = versionTuple(version)
		}
		rows = append(rows, row)
	}
	if asJSON {
		out := make([]map[string]any, 0, len(rows))
		for _, r := range rows {
			o := map[string]any{}
			for _, c := range cols {
				o[c] = r[c]
			}
			out = append(out, o)
		}
		return printJSON(out)
	}
	header := make([]string, len(cols))
	for i, c := range cols {
		header[i] = strings.ToUpper(c)
	}
	t := newTable(header...)
	for _, r := range rows {
		cells := make([]string, len(cols))
		for i, c := range cols {
			cells[i] = cellText(r[c])
		}
		t.add(cells...)
	}
	t.render(os.Stdout)
	return nil
}

func cellText(v any) string {
	switch x := v.(type) {
	case nil:
		return "-"
	case string:
		return x
	case []any:
		parts := make([]string, 0, len(x))
		for _, e := range x {
			parts = append(parts, cellText(e))
		}
		if len(parts) == 0 {
			return "-"
		}
		return strings.Join(parts, ", ")
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+"="+cellText(x[k]))
		}
		return strings.Join(parts, " ")
	default:
		return fmt.Sprint(x)
	}
}

// shellCommand quotes a command for display.
func shellCommand(argv []string) string {
	out := make([]string, len(argv))
	for i, a := range argv {
		if a != "" && !strings.ContainsAny(a, " \t\n'\"`$\\|&;<>()*?[]{}!#~") {
			out[i] = a
			continue
		}
		out[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(out, " ")
}

// selectArgs recognises SELECT and EXPLAIN SELECT, word by word or as one
// quoted argument, with an optional trailing --json.
func selectArgs(args []string) (query string, explain, asJSON bool, ok bool) {
	if len(args) > 0 && args[len(args)-1] == "--json" {
		asJSON = true
		args = args[:len(args)-1]
	}
	if len(args) == 0 {
		return "", false, false, false
	}
	text := strings.Join(args, " ")
	if len(args) == 1 {
		// The quoted form carries its flags inside, as a quoted SHOW does.
		text = strings.TrimSpace(args[0])
		if f := strings.Fields(text); len(f) > 1 && f[len(f)-1] == "--json" {
			asJSON = true
			text = strings.TrimSpace(strings.TrimSuffix(text, "--json"))
		}
		text = strings.TrimSpace(strings.TrimSuffix(text, ";"))
	}
	fields := strings.Fields(text)
	if len(fields) >= 2 && strings.EqualFold(fields[0], "EXPLAIN") && strings.EqualFold(fields[1], "SELECT") {
		return strings.TrimSpace(text[strings.Index(strings.ToUpper(text), "SELECT"):]), true, asJSON, true
	}
	if len(fields) >= 1 && strings.EqualFold(fields[0], "SELECT") {
		return text, false, asJSON, true
	}
	return "", false, false, false
}
