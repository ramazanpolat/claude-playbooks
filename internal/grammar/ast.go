// Package grammar parses cpb's statement grammar,
//
//	cpb <VERB> <OBJECT> <name> <clause> <clause> ...
//
// from command-line arguments or from a playbook file (playbook.cpb). It is pure:
// it reads no files beyond the source it is handed, checks nothing on disk,
// and runs nothing. Whether a playbook exists, whether an env set is in use,
// and whether a secret reference resolves are the engine's questions; this
// package answers only "is this a well-formed statement, and which one".
//
// The spec is SPEC.md.
package grammar

// Verb is a statement's first word.
type Verb string

const (
	Create  Verb = "CREATE"
	Alter   Verb = "ALTER"
	Drop    Verb = "DROP"
	Show    Verb = "SHOW"
	Explain Verb = "EXPLAIN"
	Apply   Verb = "APPLY"
	Include Verb = "INCLUDE" // playbook files only: INCLUDE '<path>'
	Use     Verb = "USE"     // playbook files only: USE PLAYBOOK <name>
)

// Object is what a statement acts on or reads.
type Object string

const (
	Playbook  Object = "PLAYBOOK"
	Env       Object = "ENV"
	Defaults  Object = "DEFAULTS"
	Playbooks Object = "PLAYBOOKS"
	Envs      Object = "ENVS"
	All       Object = "ALL"      // SHOW CREATE ALL
	Sessions  Object = "SESSIONS" // SHOW SESSIONS: the live Claude Code sessions of cpb's config dirs
)

// Kind names a clause. The values are the clause as written, so a test
// failure or a debug print reads like the statement.
type Kind string

const (
	SetVar      Kind = "SET"         // SET [VAR] K=V ...
	SetRef      Kind = "SET FROM"    // SET [VAR] K FROM '<ref>'
	BlockVar    Kind = "BLOCK"       // BLOCK [VAR] K ...
	UnsetVar    Kind = "UNSET"       // UNSET [VAR] K ...
	Description Kind = "DESCRIPTION" // DESCRIPTION '<text>'
	UseEnv      Kind = "USE ENV"     // USE ENV a b ...
	AddEnv      Kind = "ADD ENV"     // ADD ENV a [FIRST | LAST | BEFORE b | AFTER b]
	DropEnv     Kind = "DROP ENV"    // DROP ENV a b ...
	RenameTo    Kind = "RENAME TO"   // RENAME TO n
	// The launcher property: SET launcher = '<name>', SET launcher = ''
	// (none), DELETE launcher (back to the playbook's name).
	Launcher        Kind = "SET launcher"
	NoLauncher      Kind = "SET launcher = ''"
	DefaultLauncher Kind = "DELETE launcher"
	From            Kind = "FROM"   // CREATE PLAYBOOK ... FROM <source>
	Branch          Kind = "BRANCH" // CREATE PLAYBOOK ... BRANCH <ref>
	Subdir          Kind = "SUBDIR" // CREATE PLAYBOOK ... SUBDIR <dir>
	Link            Kind = "LINK"   // CREATE PLAYBOOK ... LINK <dir>

	// Playbook properties (properties.go): CREATE PLAYBOOK ... SET k = 'v',
	// ... gives the starting values, ALTER PLAYBOOK ... SET k = 'v', ...
	// changes them, and ALTER PLAYBOOK ... DELETE k, ... puts them back to
	// the default. Settings holds (Key, Value); DELETE's values are empty.
	SetProperties    Kind = "SET <key> = <value>"
	DeleteProperties Kind = "DELETE <key>"

	// The [sandbox] table's keys, the sandbox.<key> properties: SET
	// sandbox.<key> = <value> (Settings, the value as manifest.Sandbox.SetKey
	// takes it, a list comma-joined) and DELETE sandbox.<key> (Key only).
	// always is one key like the others; it changes nothing else.
	SetSandboxKeys   Kind = "SET sandbox.<key>"
	UnsetSandboxKeys Kind = "DELETE sandbox.<key>"

	SetHelper   Kind = "SET secret_helper"    // ALTER DEFAULTS SET secret_helper = '<command>'
	UnsetHelper Kind = "DELETE secret_helper" // ALTER DEFAULTS DELETE secret_helper

	// Plugins and the agent (ALTER PLAYBOOK only): they write the playbook's
	// settings.json, never the manifest.
	AddMarketplace  Kind = "ADD MARKETPLACE"  // ADD MARKETPLACE m FROM '<source>'
	DropMarketplace Kind = "DROP MARKETPLACE" // DROP MARKETPLACE m
	AddPlugin       Kind = "ADD PLUGIN"       // ADD PLUGIN p@m
	DropPlugin      Kind = "DROP PLUGIN"      // DROP PLUGIN p@m
	SetAgent        Kind = "SET agent"        // SET agent = '<agent>'
	UnsetAgent      Kind = "DELETE agent"     // DELETE agent

	// MCP servers (ALTER PLAYBOOK only): claude mcp add-json / remove.
	AddMCP  Kind = "ADD MCP SERVER"  // ADD MCP SERVER n COMMAND … | URL …, VAR …, HEADER …
	DropMCP Kind = "DROP MCP SERVER" // DROP MCP SERVER n

	// Tool permissions, status line and model (ALTER PLAYBOOK only): keys
	// of the playbook's settings.json, which Claude Code has no CLI for.
	AllowTool       Kind = "ALLOW TOOL"        // ALLOW TOOL '<rule>' ...
	DenyTool        Kind = "DENY TOOL"         // DENY TOOL '<rule>' ...
	UnsetTool       Kind = "UNSET TOOL"        // UNSET TOOL '<rule>' ...
	SetStatusline   Kind = "SET statusline"    // SET [IF UNSET] statusline = '<command>'[, statusline_refresh = <n>]
	UnsetStatusline Kind = "DELETE statusline" // DELETE statusline
	SetModel        Kind = "SET model"         // SET model = '<model>'
	UnsetModel      Kind = "DELETE model"      // DELETE model

	// Skills (ALTER PLAYBOOK only): <config>/skills/<name>.
	AddSkill  Kind = "ADD SKILL"  // ADD SKILL n FROM <source> [BRANCH <ref>] [SUBDIR <dir>]
	DropSkill Kind = "DROP SKILL" // DROP SKILL n

	// The model picker (v3.22.0): settings.json modelPicker.
	// The status line's refresh (v3.23.0): statusLine.refreshInterval.
	SetStatuslineRefresh   Kind = "SET statusline_refresh" // SET statusline_refresh = <n>
	UnsetStatuslineRefresh Kind = "DELETE statusline_refresh"
	// SetStatuslinePrevious: REVERT STATUSLINE, the status line cpb
	// replaced last, from its history.
	SetStatuslinePrevious Kind = "REVERT STATUSLINE"

	AddModel             Kind = "ADD MODEL"                // ADD MODEL '<id>' [LABEL '…'] [DESCRIPTION '…'] [BEHAVES AS '<id>']
	DropModel            Kind = "DROP MODEL"               // DROP MODEL '<id>'
	SetModelPicker       Kind = "SET model_picker.mode"    // SET model_picker.mode = 'only' | 'append' (Arg ONLY or APPEND)
	UnsetModelPicker     Kind = "DELETE model_picker"      // DELETE model_picker: the whole picker, rows too
	UnsetModelPickerMode Kind = "DELETE model_picker.mode" // DELETE model_picker.mode: the mode only
)

// Skill is where an ADD SKILL takes a skill from: a directory (linked) or a
// git source (cloned and copied), with BRANCH and SUBDIR for git only.
// PickerRow is one ADD MODEL: a row of the model picker. A field left out
// (nil) keeps what the row already has.
type PickerRow struct {
	Model       string
	Label       *string
	Description *string
	BehavesAs   *string
}

type Skill struct {
	From   string
	Branch string
	Subdir string
}

// MCP is one ADD MCP SERVER declaration: a stdio server (Command, Args) or
// a remote one (URL, SSE), with its environment and, remote only, headers.
// A value is a literal or a secret reference, never both.
type MCP struct {
	Command string
	Args    []string
	URL     string
	SSE     bool // TRANSPORT SSE; a remote server is HTTP otherwise
	Env     []Var
	Headers []Var // Key is the header name
}

// Where places an env set added with ADD ENV.
type Where string

const (
	Last   Where = "LAST" // the default: a later set wins, so appending is the common case
	First  Where = "FIRST"
	Before Where = "BEFORE"
	After  Where = "AFTER"
)

// Stmt is one parsed statement.
type Stmt struct {
	Verb   Verb
	Object Object
	Name   string // empty for DEFAULTS, the plural SHOWs and SHOW CREATE ALL

	ShowCreate  bool // SHOW CREATE ...
	OrReplace   bool // CREATE OR REPLACE ENV
	IfNotExists bool // CREATE ... IF NOT EXISTS
	IfExists    bool // DROP ... IF EXISTS

	Clauses []Clause

	Files  []string // APPLY <file> [<file> ...]; INCLUDE: exactly one
	Target string   // APPLY … TO <playbook|dir>

	// Recipe: ALTER PLAYBOOK with no name (playbook files only); the
	// playbook is decided when the file is applied (APPLY … TO, USE
	// PLAYBOOK).
	Recipe bool
	// Dir: a recipe applied TO a plain Claude Code config directory (not
	// a playbook), set by APPLY; Name is then empty.
	Dir    string
	DryRun bool // APPLY <file> --dry-run

	// For: SHOW SESSIONS … FOR PLAYBOOK <name>.
	For string

	SkipSecrets bool // SHOW CREATE ... --skip-secrets
	Yes         bool // DROP PLAYBOOK ... --yes, APPLY ... --yes
	JSON        bool // SHOW ... --json, EXPLAIN ... --json, APPLY ... --dry-run --json

	Pos Pos
}

// Clause is one clause of a CREATE or ALTER. Which fields are set depends
// on Kind; the rest are zero.
type Clause struct {
	Kind Kind

	Vars   []Var    // SET: one per K=V; SET FROM: exactly one, with Ref
	Keys   []string // BLOCK, UNSET
	Names  []string // USE ENV, DROP ENV; ADD ENV, the MARKETPLACE and PLUGIN clauses: exactly one
	Arg    string   // RENAME TO, LAUNCHER, DESCRIPTION, FROM, BRANCH, SUBDIR, LINK, SET SECRET HELPER, SET AGENT, ADD MARKETPLACE's source
	Where  Where    // ADD ENV
	Anchor string   // ADD ENV ... BEFORE/AFTER <anchor>

	Plaintext bool // SET ... AS PLAINTEXT: credential-looking literals stored knowingly

	// Settings: the [sandbox] keys of SET sandbox.<key> = <value> (Key,
	// Value) and DELETE sandbox.<key> (Key only); and the playbook
	// properties of SET <key> = <value> and DELETE <key> (Key only). Apart
	// from Vars and Keys, which name variables.
	Settings []Var

	MCP   *MCP       // ADD MCP SERVER
	Skill *Skill     // ADD SKILL
	Row   *PickerRow // ADD MODEL
	// Refresh: SET STATUSLINE … REFRESH <n> and SET STATUSLINE REFRESH <n>,
	// whole seconds (0: not given).
	Refresh int
	// IfUnset: SET IF UNSET statusline = …, which applies only to a config
	// dir with no status line yet.
	IfUnset bool

	Pos Pos
}

// Var is one variable of a SET clause: a literal Value, or a secret
// reference in Ref (never both). A reference is stored, never resolved, by
// anything in cpb except the launch exec through the secret helper.
type Var struct {
	Key   string
	Value string
	Ref   string
}

// Write reports whether the statement changes state.
func (s *Stmt) Write() bool {
	return s.Verb == Create || s.Verb == Alter || s.Verb == Drop
}
