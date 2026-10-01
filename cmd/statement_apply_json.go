package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
)

// APPLY … --dry-run --json (docs/reference/cli-grammar.md, "APPLY --json"):
// the plan as one JSON object, for a program to read. It follows the --json
// rule: fields may be added, none changes meaning within a major version.
// schema is bumped only on a meaning change, and so only with a major cpb
// version. Verdicts, action types and warning codes are closed sets within
// a major version.
const applySchema = 1

type applyReport struct {
	Schema     int             `json:"schema"`
	CPBVersion string          `json:"cpb_version"`
	Files      []string        `json:"files"`
	Target     *targetJSON     `json:"target"`
	OK         bool            `json:"ok"`
	Error      *applyErrorJSON `json:"error"`
	Warnings   []applyWarning  `json:"warnings"`
	Statements []applyStmtJSON `json:"statements"`
	Summary    applySummary    `json:"summary"`
}

// targetJSON is what a statement writes to: a playbook, a plain config
// directory, an env set, or DEFAULTS.
type targetJSON struct {
	Kind string `json:"kind"`
	Name string `json:"name,omitempty"`
	Path string `json:"path,omitempty"`
}

type applyErrorJSON struct {
	File    *string `json:"file"`
	Line    *int    `json:"line"`
	Message string  `json:"message"`
}

// applyWarning is a warning with a stable code. shown is the file as the
// human output names it.
type applyWarning struct {
	Code    string `json:"code"`
	File    string `json:"file"`
	Line    int    `json:"line"`
	Message string `json:"message"`
	shown   string
}

// The warning codes (a closed set within a major version).
const (
	warnUsePlaybookOverridden = "use_playbook_overridden"
	warnSourceDrift           = "source_drift"
	// SET STATUSLINE left a host's (statusmux's) status line as it is.
	warnStatuslineHeldByHost = "statusline_held_by_host"
	// A playbook importing ~/.pilot-profile/ now has a non-Anthropic
	// ANTHROPIC_BASE_URL (v3.23.0).
	warnPilotProfileThirdParty = "pilot_profile_third_party_endpoint"
	// An ADD MARKETPLACE git source whose #ref looks like a commit, which
	// Claude Code cannot clone (v3.27.0).
	warnMarketplaceRefNotCloneable = "marketplace_ref_not_cloneable"
)

func (w applyWarning) String() string { return fmt.Sprintf("%s:%d: %s", w.shown, w.Line, w.Message) }

type applyStmtJSON struct {
	File      string        `json:"file"`
	Line      int           `json:"line"`
	Statement string        `json:"statement"`
	Verb      string        `json:"verb"`
	Object    string        `json:"object"`
	Target    *targetJSON   `json:"target"`
	Recipe    bool          `json:"recipe"`
	Implicit  bool          `json:"implicit"`
	Verdict   string        `json:"verdict"`
	Reason    *string       `json:"reason"`
	Warning   *applyWarning `json:"warning"`
	Actions   []planAction  `json:"actions"`
}

type applySummary struct {
	Created   int `json:"created"`
	Changed   int `json:"changed"`
	Unchanged int `json:"unchanged"`
	Dropped   int `json:"dropped"`
	Refused   int `json:"refused"`
	Warnings  int `json:"warnings"`
}

const verdictRefused = "refused"

// planAction is one thing a real APPLY would do beyond the playbook's own
// manifest and env files, which the verdict covers. Types: command (a
// `claude plugin` / `claude mcp` run), skill (a skill put in place: op link
// or copy), fetch (CREATE PLAYBOOK … FROM), backup, write, delete. Paths are absolute. env holds only
// non-secret variables; an MCP config holds ${CPB_MCP_…} placeholders only,
// and refs maps each to its reference, never a value.
type planAction struct {
	Type    string            `json:"type"`
	Argv    []string          `json:"argv,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Refs    map[string]string `json:"refs,omitempty"`
	Op      string            `json:"op,omitempty"`
	What    string            `json:"what,omitempty"`
	Name    string            `json:"name,omitempty"`
	Path    string            `json:"path,omitempty"`
	Source  string            `json:"source,omitempty"`
	Branch  string            `json:"branch,omitempty"`
	Subdir  string            `json:"subdir,omitempty"`
	To      string            `json:"to,omitempty"`
	Bytes   *int64            `json:"bytes,omitempty"`
	Network bool              `json:"network"`
}

var mcpPlaceholder = regexp.MustCompile(`\$\{(CPB_MCP_[A-Za-z0-9_]+)\}`)

// stepActions describes one planned step. refOf finds the reference a
// derived MCP variable stands for.
func stepActions(configDir string, s pluginStep, refOf func(string) string) []planAction {
	if op := s.skill; op != nil {
		path := skillPath(configDir, op.name)
		if op.rec == nil {
			return []planAction{deleteAction("skill", path)}
		}
		var out []planAction
		if op.old != nil {
			out = append(out, deleteAction("skill", path))
		}
		src := op.rec.Source
		if op.rec.Mode == "link" {
			if p, err := expandSkillPath(src); err == nil {
				src = p
			}
		}
		return append(out, planAction{Type: "skill", Op: op.rec.Mode, Name: op.name, Path: path, Source: src,
			Network: op.rec.Mode == "copy" && !strings.HasPrefix(src, "file://")})
	}
	argv := append([]string{"claude", "plugin"}, s.args...)
	if s.mcp {
		argv[1] = "mcp"
	}
	a := planAction{Type: "command", Argv: argv, Env: map[string]string{"CLAUDE_CONFIG_DIR": configDir}}
	switch {
	case !s.mcp && len(s.args) > 2 && s.args[0] == "marketplace" && s.args[1] == "add":
		a.Network = !filepath.IsAbs(s.args[2]) // a directory source is local
	case !s.mcp && len(s.args) > 0 && s.args[0] == "install":
		a.Network = true
	case s.mcp && len(s.args) > 2 && s.args[0] == "add-json":
		for _, m := range mcpPlaceholder.FindAllStringSubmatch(s.args[2], -1) {
			if ref := refOf(m[1]); ref != "" {
				if a.Refs == nil {
					a.Refs = map[string]string{}
				}
				a.Refs[m[1]] = ref
			}
		}
	}
	return []planAction{a}
}

// fetchAction is CREATE PLAYBOOK … FROM: the source a real run installs
// into path (to). A local directory is copied; anything else is fetched.
func fetchAction(source, branch, subdir, to string) planAction {
	// A local directory by its form, whether or not it exists yet.
	local := source == "." || source == ".." || source == "~" ||
		strings.HasPrefix(source, "/") || strings.HasPrefix(source, "~/") ||
		strings.HasPrefix(source, "./") || strings.HasPrefix(source, "../")
	if local {
		source = expandHomeDir(source)
		if abs, err := filepath.Abs(source); err == nil {
			source = abs
		}
	}
	return planAction{Type: "fetch", Source: source, Branch: branch, Subdir: subdir, To: to, Network: !local}
}

func expandHomeDir(p string) string {
	if p == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	}
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

// deleteAction is a path a real APPLY would remove, with its size on disk
// (symlinks not followed: a link counts as itself).
func deleteAction(what, path string) planAction {
	n := diskBytes(path)
	return planAction{Type: "delete", What: what, Path: path, Bytes: &n}
}

func diskBytes(root string) int64 {
	var n int64
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if info, err := d.Info(); err == nil {
			n += info.Size()
		}
		return nil
	})
	return n
}

func nonNilActions(a []planAction) []planAction {
	if a == nil {
		return []planAction{}
	}
	return a
}

// stmtTarget is what a statement writes to, resolved.
func stmtTarget(s *grammar.Stmt) *targetJSON {
	switch {
	case s.Dir != "":
		return &targetJSON{Kind: "dir", Path: s.Dir}
	case s.Object == grammar.Playbook:
		return &targetJSON{Kind: "playbook", Name: s.Name}
	case s.Object == grammar.Env:
		return &targetJSON{Kind: "env", Name: s.Name}
	case s.Object == grammar.Defaults:
		return &targetJSON{Kind: "defaults"}
	}
	return nil
}

// applyFailure is an APPLY error with its --json class: 1 when the files
// are refused, 2 for a usage or internal error. Its text is the error's.
type applyFailure struct {
	code int
	file string
	line int
	err  error
}

func (f *applyFailure) Error() string { return f.err.Error() }
func (f *applyFailure) Unwrap() error { return f.err }

func refused(file string, line int, err error) error {
	return &applyFailure{code: 1, file: file, line: line, err: err}
}

func usage(err error) error { return &applyFailure{code: 2, err: err} }

// runApplyJSON runs APPLY with its human output sent to stderr, and prints
// the plan as JSON on stdout, refusals included.
func runApplyJSON(st *grammar.Stmt) error {
	rep := &applyReport{Files: []string{}, Warnings: []applyWarning{}, Statements: []applyStmtJSON{}}
	code := 0
	var err error
	if !st.DryRun {
		err = usage(errors.New("--json needs --dry-run: the JSON form is a plan"))
	} else {
		stdout := os.Stdout
		os.Stdout = os.Stderr
		err = applyRun(st, rep)
		os.Stdout = stdout
	}
	if err != nil {
		code = 2
		var f *applyFailure
		if errors.As(err, &f) {
			code = f.code
		}
		// A refused statement carries its own reason; any other failure
		// is the report's error.
		if !(len(rep.Statements) > 0 && rep.Statements[len(rep.Statements)-1].Verdict == verdictRefused) {
			rep.Error = &applyErrorJSON{Message: err.Error()}
			if f != nil && f.file != "" {
				file, line := f.file, f.line
				rep.Error.File, rep.Error.Line = &file, &line
			}
		}
	}
	return printApplyReport(rep, code)
}

// printApplyReport prints the report and ends with its exit code.
func printApplyReport(rep *applyReport, code int) error {
	rep.Schema, rep.CPBVersion = applySchema, Version
	if rep.Files == nil {
		rep.Files = []string{}
	}
	if rep.Warnings == nil {
		rep.Warnings = []applyWarning{}
	}
	if rep.Statements == nil {
		rep.Statements = []applyStmtJSON{}
	}
	rep.OK = code == 0
	rep.Summary.Warnings = len(rep.Warnings)
	for _, s := range rep.Statements {
		if s.Warning != nil {
			rep.Summary.Warnings++
		}
	}
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil { // cannot happen with these types; still one JSON object
		data = []byte(`{"schema": 1, "ok": false, "error": {"file": null, "line": null, "message": "internal: cannot encode the report"}}`)
		code = 2
	}
	fmt.Println(string(data))
	if code != 0 {
		return &commandExitError{code: code}
	}
	return nil
}

// applyJSONArgs reports a command line that is APPLY … --json.
func applyJSONArgs(args []string) bool {
	words := args
	if len(args) == 1 {
		words = strings.Fields(args[0])
	}
	if len(words) == 0 || !strings.EqualFold(words[0], "APPLY") {
		return false
	}
	for _, w := range words[1:] {
		if w == "--json" {
			return true
		}
	}
	return false
}
