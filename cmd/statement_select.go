package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/term"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envset"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

// SELECT (SPEC.md, "SELECT"). cpb answers one form
// itself, SELECT <col>[, <col>…] FROM <table>, a strict subset of
// ClickHouse SQL, so a query means the same on both paths. Anything else is
// handed to ClickHouse's clickhouse-local, when it is installed: the
// table's rows go to its stdin as the same objects SHOW … --json prints
// (secrets redacted, references as references), and nothing else.

// selectTable is one queryable table: its columns in order, the typed
// structure clickhouse-local reads its rows with, what each column means
// (DESCRIBE's comment; SPEC.md's DESCRIBE has the same lines), and the rows:
// the objects SHOW … --json prints, one per row.
type selectTable struct {
	columns   []string
	structure string
	comments  map[string]string
	rows      func() ([]any, []playbook.Unreadable, error)
}

// versionPattern is the leading numeric part of a version ("v3.12.3-rc1":
// "3.12.3"); version_tuple is its numbers. Both paths compute it the same
// way: in Go for the built-in form, in the query for ClickHouse, so the
// rows handed over stay exactly SHOW's.
const versionPattern = `^v?([0-9]+([.][0-9]+)*)`

var versionRe = regexp.MustCompile(versionPattern)

// playbooksLateColumns are the PLAYBOOKS columns clickhouse-local's
// SELECT * lists after the computed version_tuple, where they were appended.
var playbooksLateColumns = []string{"play", "apply"}

// versionTupleSQL is version_tuple as a ClickHouse expression.
const versionTupleSQL = "if(extract(ifNull(version, ''), '" + versionPattern + "') = '', CAST([] AS Array(UInt32)), " +
	"arrayMap(x -> toUInt32(x), splitByChar('.', extract(ifNull(version, ''), '" + versionPattern + "'))))"

var selectTables = map[string]selectTable{
	"PLAYBOOKS": {
		columns: []string{"name", "version", "version_tuple", "description", "homepage", "author", "path", "last_used", "source", "migrate", "linked", "launcher", "envs", "vars", "sandbox", "login", "memory",
			"marketplaces", "plugins", "agent", "mcp_servers", "tools", "skills", "statusline", "statusline_refresh", "statusline_history", "model", "model_picker", "play", "apply"},
		structure: "name String, version Nullable(String), description Nullable(String), homepage Nullable(String), author Nullable(String), " +
			"path String, last_used Nullable(DateTime64(3, 'UTC')), source JSON, migrate Nullable(String), linked Nullable(String), " +
			"launcher Nullable(String), envs Array(String), vars Array(JSON), sandbox JSON, login String, memory String, marketplaces Array(JSON), plugins Array(JSON), " +
			"agent Nullable(String), mcp_servers Array(JSON), tools JSON, skills Array(JSON), statusline Nullable(String), statusline_refresh Nullable(UInt32), statusline_history Array(JSON), model Nullable(String), model_picker JSON, play JSON, apply JSON",
		comments: map[string]string{
			"name":               "The playbook's name.",
			"version":            "The manifest's version; null when it has none.",
			"version_tuple":      "The numbers of the version's leading numeric part ([3, 12, 3] for v3.12.3-rc1), to compare and sort versions; computed.",
			"description":        "The manifest's description; null when it has none.",
			"homepage":           "The manifest's homepage; null when it has none.",
			"author":             "The manifest's author; null when it has none.",
			"path":               "The playbook's directory under the playbooks root: its config directory.",
			"last_used":          "When the playbook's config directory last changed (UTC).",
			"source":             "Where the playbook was installed from: url, branch, subdir; null for a playbook without one.",
			"migrate":            "The declared migrate step ([update] migrate) that cpb update runs; null without one.",
			"linked":             "The target directory of a linked playbook; null otherwise.",
			"launcher":           "The command that runs the playbook, the one you type; null when no launcher is in place, as under NO LAUNCHER.",
			"envs":               "The env sets the playbook uses, in order.",
			"vars":               "The playbook's own variables, each a value, a reference, a redacted credential or a block.",
			"sandbox":            "The [sandbox] table, key for key: always, backend, host, workdir, mounts, allow_net, secrets, claude_version, share_skills.",
			"login":              "The login property: isolated when the playbook shares no login with the machine (a sandboxed playbook too), else shared.",
			"memory":             "The memory property: isolated when ~/.claude's CLAUDE.md and rules do not load into the playbook, else shared.",
			"marketplaces":       "The plugin marketplaces in the playbook's settings: name, source.",
			"plugins":            "The plugins in the playbook's settings: id, enabled.",
			"agent":              "The agent the playbook pins (SET AGENT); null when unset.",
			"mcp_servers":        "The MCP servers: name, transport, command and args or url, env and headers as variables.",
			"tools":              "The tool permission rules: allow, deny.",
			"skills":             "The skills cpb recorded: name, source, branch, subdir, mode.",
			"statusline":         "The status line command; null for none.",
			"statusline_refresh": "How often the status line refreshes, in whole seconds; null when unset.",
			"statusline_history": "The status lines SET STATUSLINE PREVIOUS can go back to, newest first: command, refresh, replaced_at.",
			"model":              "The playbook's default model (SET MODEL); null when unset.",
			"model_picker":       "The model picker: mode (only or append) and options; null when unset.",
			"play":               "The [play] record of a playbook cpb play --keep built: ref, url, sha256, played_at; null for every other.",
			"apply":              "The [apply] record of a playbook an APPLY gave name-less statements: files, to, sha256, applied_at; null for every other.",
		},
		rows: playbookRows,
	},
	"ENVS": {
		columns:   []string{"name", "description", "vars", "used_by", "default"},
		structure: "name String, description String, vars Array(JSON), used_by Array(String), `default` Bool",
		comments: map[string]string{
			"name":        "The env set's name.",
			"description": "The env set's description.",
			"vars":        "The env set's variables, each a value, a reference, a redacted credential or a block.",
			"used_by":     "The playbooks that use the env set.",
			"default":     "True when the env set is in DEFAULTS.",
		},
		rows: envRows,
	},
	"VARS": {
		columns:   []string{"playbook", "key", "value", "ref", "redacted", "plaintext", "blocked", "layer", "effective"},
		structure: "playbook String, key String, value Nullable(String), ref Nullable(String), redacted Bool, plaintext Bool, blocked Bool, layer JSON, effective Bool",
		comments: map[string]string{
			"playbook":  "The playbook a launch gives the variable to.",
			"key":       "The variable's name.",
			"value":     "The literal value; null for a reference, a redacted credential or a block.",
			"ref":       "The secret reference the value is read from at launch; null otherwise.",
			"redacted":  "True when the value is withheld from output, as for a credential-looking literal.",
			"plaintext": "True when a credential-looking value is stored as a literal rather than a reference.",
			"blocked":   "True when the layer blocks the variable (BLOCK).",
			"layer":     "Where the entry comes from: kind (defaults, env or playbook) and, for an env set, its name.",
			"effective": "True for the entry a launch uses.",
		},
		rows: varRows,
	},
	"SESSIONS": {
		columns: []string{"playbook", "pid", "session_id", "cwd", "kind", "status", "name", "claude_version",
			"started_at", "last_active", "model", "launcher", "config_dir", "resume", "tty"},
		structure: "playbook String, pid UInt32, session_id String, cwd String, kind String, status Nullable(String), name Nullable(String), " +
			"claude_version Nullable(String), started_at DateTime64(3, 'UTC'), last_active Nullable(DateTime64(3, 'UTC')), model Nullable(String), " +
			"launcher Nullable(String), config_dir String, resume String, tty Nullable(String)",
		comments: map[string]string{
			"playbook":       "The playbook's name; a plain directory's path.",
			"pid":            "The Claude Code process's id.",
			"session_id":     "The session's id.",
			"cwd":            "The session's working directory.",
			"kind":           "interactive or bg.",
			"status":         "The session's status, as Claude Code records it; null when it records none.",
			"name":           "The session's name, as Claude Code records it; null when it records none.",
			"claude_version": "The Claude Code version the session runs; null when it records none.",
			"started_at":     "When the session started (UTC).",
			"last_active":    "When the transcript last changed (UTC); null before the first message.",
			"model":          "The model of the transcript's last assistant message; null before one.",
			"launcher":       "The playbook's launcher, as SHOW PLAYBOOK reports it; null for a plain directory.",
			"config_dir":     "The config directory the session runs under.",
			"resume":         "The command that resumes this session from any folder once it ends.",
			"tty":            "The process's controlling terminal (pts/3, ttys012); null for none, as for a bg session.",
		},
		rows: sessionRows,
	},
	"DEFAULTS": {
		columns:   []string{"envs", "secret_helper"},
		structure: "envs Array(String), secret_helper JSON",
		comments: map[string]string{
			"envs":          "The env sets every launch applies first, in order.",
			"secret_helper": "The command that resolves secret references, and where it is set (setting or CPB_SECRET_HELPER); null when none is configured.",
		},
		rows: defaultsRows,
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

func playbookRows() ([]any, []playbook.Unreadable, error) {
	pbs, bad, err := playbook.Scan(config.ResolvePlaybooksDir())
	if err != nil {
		return nil, nil, err
	}
	rows := []any{}
	for _, pb := range pbs {
		rows = append(rows, describePlaybook(pb))
	}
	return rows, bad, nil
}

func envRows() ([]any, []playbook.Unreadable, error) {
	playbooksDir := config.ResolvePlaybooksDir()
	dir := envset.Dir(playbooksDir)
	profiles, err := envset.List(dir)
	if err != nil {
		return nil, nil, err
	}
	users, bad, err := profileUsers(playbooksDir)
	if err != nil {
		return nil, nil, err
	}
	defaults, _ := envset.Defaults(dir)
	rows := []any{}
	for _, p := range profiles {
		rows = append(rows, envJSON{Name: p.Name, Description: p.Description,
			Vars: layerVars(p.Set, p.Refs, p.Block), UsedBy: nonNil(users[p.Name]),
			Default: isRegistryDefault(dir, defaults, p.Name)})
	}
	return rows, bad, nil
}

// varRows is one row per variable, per layer, per playbook: what EXPLAIN
// shows for every layer, with effective marking the entry a launch uses.
func varRows() ([]any, []playbook.Unreadable, error) {
	playbooksDir := config.ResolvePlaybooksDir()
	dir := envset.Dir(playbooksDir)
	pbs, bad, err := playbook.Scan(playbooksDir)
	if err != nil {
		return nil, nil, err
	}
	rows := []any{}
	for _, pb := range pbs {
		// The launch reads the governing manifest; so does this table.
		governing, _ := governingManifest(pb)
		var env *manifest.Env
		if governing != nil {
			env = governing.Env
		}
		origins, err := envset.ExplainAll(dir, env)
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
	return rows, bad, nil
}

func defaultsRows() ([]any, []playbook.Unreadable, error) {
	dir := envset.Dir(config.ResolvePlaybooksDir())
	names, err := envset.Defaults(dir)
	if err != nil {
		return nil, nil, err
	}
	helper, err := helperInEffect(dir)
	if err != nil {
		return nil, nil, err
	}
	return []any{defaultsJSON{Envs: nonNil(names), SecretHelper: helper}}, nil, nil
}

var builtinSelect = regexp.MustCompile(`(?is)^\s*SELECT\s+([A-Za-z_][A-Za-z0-9_]*(?:\s*,\s*[A-Za-z_][A-Za-z0-9_]*)*)\s+FROM\s+([A-Za-z_]+)\s*;?\s*$`)

// selectPlan is how a query runs: built in, or through clickhouse-local.
type selectPlan struct {
	table   string
	columns []string // built in
	query   string   // for ClickHouse, with FROM <table> rewritten to read stdin
	format  bool     // the query has its own FORMAT clause
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
// sqlClauseWords are words that can follow a column named format, so a
// FORMAT before one of them is not the query's FORMAT clause.
var sqlClauseWords = map[string]bool{"FROM": true, "WHERE": true, "AS": true, "AND": true, "OR": true,
	"ORDER": true, "GROUP": true, "BY": true, "LIMIT": true, "HAVING": true, "UNION": true, "SETTINGS": true,
	"ASC": true, "DESC": true, "JOIN": true, "ON": true, "IN": true, "IS": true, "NOT": true, "LIKE": true,
	"ILIKE": true, "BETWEEN": true, "OFFSET": true, "WITH": true, "INTO": true}

func scanSQL(q string) (froms []sqlFrom, second, format bool) {
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
	prevWord := ""
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
			// The query's own FORMAT clause is FORMAT followed by a format
			// name: not an alias (AS format), a function (format(…)), or a
			// column (format, format = …, format FROM …).
			if strings.EqualFold(q[i:j], "FORMAT") && !strings.EqualFold(prevWord, "AS") {
				k := j
				for k < len(q) {
					if n := skip(k); n != k {
						k = n
						continue
					}
					if q[k] == ' ' || q[k] == '\t' || q[k] == '\r' || q[k] == '\n' {
						k++
						continue
					}
					break
				}
				e := k
				for e < len(q) && isWord(q[e]) {
					e++
				}
				if e > k && !sqlClauseWords[strings.ToUpper(q[k:e])] {
					format = true
				}
			}
			prevWord = q[i:j]
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
	return froms, second, format
}

func planSelect(q string) (*selectPlan, error) {
	if m := builtinSelect.FindStringSubmatch(q); m != nil {
		table := strings.ToUpper(m[2])
		t, ok := selectTables[table]
		if !ok {
			return nil, fmt.Errorf("unknown table %q (tables: PLAYBOOKS, ENVS, VARS, SESSIONS, DEFAULTS)", m[2])
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
	froms, second, format := scanSQL(q)
	if second {
		return nil, errors.New("one statement at a time: remove what follows the semicolon")
	}
	if len(froms) == 0 {
		return nil, errors.New("a query reads FROM one of the tables: PLAYBOOKS, ENVS, VARS, SESSIONS, DEFAULTS")
	}
	if len(froms) > 1 {
		return nil, errors.New("a query reads one table; join them in ClickHouse yourself: cpb SHOW … --json | clickhouse local …")
	}
	f := froms[0]
	source := "FROM table"
	if f.table == "PLAYBOOKS" {
		// On this path the computed version_tuple has always come after the
		// structure's columns (SELECT *); a column added since (v3.25.0)
		// goes after it, so every earlier position holds (Codex, #135).
		late := strings.Join(playbooksLateColumns, ", ")
		source = "FROM (SELECT * EXCEPT (" + late + "), " + versionTupleSQL + " AS version_tuple, " + late + " FROM table)"
	}
	return &selectPlan{table: f.table, query: q[:f.start] + source + q[f.end:], format: format}, nil
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

// selectTTY reports whether stdout is a terminal; a var, so the choice of
// output is testable (as terminalWidth is).
var selectTTY = func() bool { return term.IsTerminal(int(os.Stdout.Fd())) }

// wideColumns: a result with more columns than this is printed one block
// per row on a terminal, so nothing wraps.
const wideColumns = 6

// rendered reports whether cpb prints the result itself, which it does
// unless the query names a FORMAT of its own: then clickhouse local writes
// it, and a FORMAT in the query always wins.
func (p *selectPlan) rendered() bool { return !p.format }

// clickhouseArgs is the clickhouse-local command line. When cpb prints the
// result, it asks for JSONCompact (names, types, and the values as JSON,
// 64-bit integers as numbers) and prints it as the built-in form does.
func (p *selectPlan) clickhouseArgs() []string {
	args := []string{"local", "--input-format", "JSONEachRow", "--structure", selectTables[p.table].structure}
	// SESSIONS' times are RFC 3339, which ClickHouse's basic DateTime input
	// does not read; only a table with a DateTime column asks for more.
	if strings.Contains(selectTables[p.table].structure, "DateTime") {
		args = append(args, "--date_time_input_format", "best_effort")
	}
	if p.rendered() {
		args = append(args, "--output-format", "JSONCompact", "--output_format_json_escape_forward_slashes=0", "--output_format_json_quote_64bit_integers=0")
	}
	return append(args, "-q", p.query)
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
	rows, bad, err := selectTables[p.table].rows()
	if err != nil {
		return err
	}
	if err := selectOutput(p, rows, asJSON); err != nil {
		return err
	}
	return leftOut(bad)
}

// selectOutput runs the query over rows and prints its result.
func selectOutput(p *selectPlan, rows []any, asJSON bool) error {
	if p.query == "" {
		return printSelect(p.table, p.columns, rows, asJSON)
	}
	if asJSON && p.format {
		return fmt.Errorf("--json and a FORMAT in the query cannot be combined: drop one (the query's FORMAT is written as clickhouse local writes it)")
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
	if !p.rendered() {
		if err := c.Run(); err != nil {
			return fmt.Errorf("clickhouse local: %w", err)
		}
		return nil
	}
	var out bytes.Buffer
	c.Stdout = &out
	if err := c.Run(); err != nil {
		return fmt.Errorf("clickhouse local: %w", err)
	}
	var res struct {
		Meta []struct {
			Name string `json:"name"`
		} `json:"meta"`
		Data [][]any `json:"data"`
	}
	dec := json.NewDecoder(&out)
	dec.UseNumber()
	if err := dec.Decode(&res); err != nil {
		return fmt.Errorf("clickhouse local: unexpected output: %w", err)
	}
	cols := make([]string, len(res.Meta))
	for i, m := range res.Meta {
		cols[i] = m.Name
	}
	return emitResult(cols, res.Data, asJSON)
}

// emitResult prints a result as both engines print it: --json, an array of
// one object per row with the keys in the query's column order; on a
// terminal, rendered (renderRows); in a pipe, TSV with a header row of the
// column names, each cell as the terminal shows it, with a tab, a line break
// or a backslash in it escaped as \t, \n and \\.
func emitResult(cols []string, rows [][]any, asJSON bool) error {
	if asJSON {
		out := make([]orderedRow, 0, len(rows))
		for _, r := range rows {
			vals := make(map[string]any, len(cols))
			for i, c := range cols {
				if _, seen := vals[c]; !seen && i < len(r) {
					vals[c] = r[i]
				}
			}
			out = append(out, orderedRow{cols: cols, vals: vals})
		}
		return printJSON(out)
	}
	if selectTTY() {
		renderRows(os.Stdout, cols, rows, len(cols) > wideColumns)
		return nil
	}
	writeTSV(os.Stdout, cols, rows)
	return nil
}

var tsvEscape = strings.NewReplacer("\\", "\\\\", "\t", "\\t", "\n", "\\n", "\r", "\\r")

// writeTSV prints a header row of the column names, then one line per row,
// every field escaped alike.
func writeTSV(w io.Writer, cols []string, rows [][]any) {
	header := make([]string, len(cols))
	for i, c := range cols {
		header[i] = tsvEscape.Replace(c)
	}
	fmt.Fprintln(w, strings.Join(header, "\t"))
	for _, r := range rows {
		cells := make([]string, len(cols))
		for i := range cols {
			if i < len(r) {
				cells[i] = tsvEscape.Replace(cellText(r[i]))
			}
		}
		fmt.Fprintln(w, strings.Join(cells, "\t"))
	}
}

// renderRows prints a result for a person: a table, or, when vertical, one
// block per row (label: value), so a wide result does not wrap. Headers are
// the column names in capitals, as SHOW prints them.
func renderRows(w io.Writer, cols []string, rows [][]any, vertical bool) {
	header := make([]string, len(cols))
	width := 0
	for i, c := range cols {
		header[i] = strings.ToUpper(c)
		width = max(width, len(header[i]))
	}
	if !vertical {
		t := newTable(header...)
		for _, r := range rows {
			cells := make([]string, len(cols))
			for i := range cols {
				if i < len(r) {
					cells[i] = cellText(r[i])
				}
			}
			t.add(cells...)
		}
		t.render(w)
		return
	}
	for n, r := range rows {
		if n > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "Row %d\n", n+1)
		for i, h := range header {
			v := ""
			if i < len(r) {
				v = cellText(r[i])
			}
			fmt.Fprintf(w, "  %-*s  %s\n", width+1, h+":", v)
		}
	}
	if len(rows) == 0 {
		fmt.Fprintln(w, "(no rows)")
	}
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
	data := make([][]any, 0, len(rows))
	for _, r := range rows {
		row := make([]any, len(cols))
		for i, c := range cols {
			row[i] = r[c]
		}
		data = append(data, row)
	}
	return emitResult(cols, data, asJSON)
}

// orderedRow is one --json row with its keys in the query's column order,
// as clickhouse local writes them (a map would sort them). A column named
// twice is one key, at its first position.
type orderedRow struct {
	cols []string
	vals map[string]any
}

func (r orderedRow) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // as printJSON: the encoder does not undo it
	seen := make(map[string]bool, len(r.cols))
	buf.WriteByte('{')
	for _, c := range r.cols {
		if seen[c] {
			continue
		}
		if len(seen) > 0 {
			buf.WriteByte(',')
		}
		seen[c] = true
		if err := enc.Encode(c); err != nil {
			return nil, err
		}
		buf.WriteByte(':')
		if err := enc.Encode(r.vals[c]); err != nil {
			return nil, err
		}
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func cellText(v any) string {
	switch x := v.(type) {
	case nil:
		return "-"
	case string:
		return x
	case json.Number:
		return x.String()
	case []any:
		if len(x) == 0 {
			return "-"
		}
		for _, e := range x {
			switch e.(type) {
			case map[string]any, []any:
				return jsonText(x) // an array of objects reads as JSON
			}
		}
		parts := make([]string, 0, len(x))
		for _, e := range x {
			parts = append(parts, cellText(e))
		}
		return strings.Join(parts, ", ")
	case map[string]any:
		if len(x) == 0 {
			return "-" // ClickHouse's JSON type reads a null object as {}
		}
		return jsonText(x)
	default:
		return fmt.Sprint(x)
	}
}

// jsonText is v as compact JSON, '/' and '<' unescaped, for reading.
func jsonText(v any) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if enc.Encode(v) != nil {
		return fmt.Sprint(v)
	}
	return strings.TrimSpace(b.String())
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

// DESCRIBE [TABLE] <table> (DESC too) lists a SELECT table's columns: each
// one's name, its type (the typed structure clickhouse-local reads the rows
// with, plus the computed version_tuple) and a comment saying what it means,
// as ClickHouse's DESC has.

// describeArgs recognises DESCRIBE / DESC, word by word or as one quoted
// argument, with an optional --json. words are what follows DESCRIBE
// [TABLE], a trailing ";" dropped; runDescribe refuses anything but one
// table.
func describeArgs(args []string) (words []string, asJSON, ok bool) {
	words = append([]string(nil), args...)
	if len(args) == 1 {
		words = strings.Fields(strings.TrimSpace(args[0]))
	}
	if len(words) > 0 && words[len(words)-1] == "--json" {
		asJSON, words = true, words[:len(words)-1]
	}
	if len(words) == 0 {
		return nil, false, false
	}
	switch strings.ToUpper(words[0]) {
	case "DESCRIBE", "DESC":
	default:
		return nil, false, false
	}
	words = words[1:]
	if len(words) > 0 && strings.EqualFold(words[0], "TABLE") {
		words = words[1:]
	}
	if n := len(words); n > 0 && words[n-1] == ";" {
		words = words[:n-1]
	} else if n > 0 {
		words[n-1] = strings.TrimSuffix(words[n-1], ";")
	}
	return words, asJSON, true
}

type columnJSON struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Comment string `json:"comment"`
}

func describeTable(name string) ([]columnJSON, error) {
	t, ok := selectTables[strings.ToUpper(name)]
	if !ok {
		return nil, fmt.Errorf("unknown table %q (tables: PLAYBOOKS, ENVS, VARS, SESSIONS, DEFAULTS)", name)
	}
	types := map[string]string{"version_tuple": "Array(UInt32)"} // computed, not in the structure
	depth, start := 0, 0
	parts := []string{}
	for i, c := range t.structure {
		switch c {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, t.structure[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, t.structure[start:])
	for _, part := range parts {
		part = strings.TrimSpace(part)
		n, typ, _ := strings.Cut(part, " ")
		types[strings.Trim(n, "`")] = strings.TrimSpace(typ)
	}
	out := make([]columnJSON, 0, len(t.columns))
	for _, c := range t.columns {
		out = append(out, columnJSON{Name: c, Type: types[c], Comment: t.comments[c]})
	}
	return out, nil
}

func runDescribe(words []string, asJSON bool) error {
	if len(words) != 1 {
		return errors.New("DESCRIBE needs one table: PLAYBOOKS, ENVS, VARS, SESSIONS or DEFAULTS")
	}
	cols, err := describeTable(words[0])
	if err != nil {
		return err
	}
	data := make([][]any, 0, len(cols))
	for _, c := range cols {
		data = append(data, []any{c.Name, c.Type, c.Comment})
	}
	return emitResult([]string{"name", "type", "comment"}, data, asJSON)
}
