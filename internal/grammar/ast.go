// Package grammar parses cpb's statement grammar,
//
//	cpb <VERB> <OBJECT> <name> <clause> <clause> ...
//
// from command-line arguments or from a setup file (setup.cpb). It is pure:
// it reads no files beyond the source it is handed, checks nothing on disk,
// and runs nothing. Whether a playbook exists, whether an env set is in use,
// and whether a secret reference resolves are the engine's questions; this
// package answers only "is this a well-formed statement, and which one".
//
// The spec is docs/cli-grammar.md.
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
)

// Object is what a statement acts on or reads.
type Object string

const (
	Playbook  Object = "PLAYBOOK"
	Env       Object = "ENV"
	Pilot     Object = "PILOT"
	Defaults  Object = "DEFAULTS"
	Playbooks Object = "PLAYBOOKS"
	Envs      Object = "ENVS"
	Pilots    Object = "PILOTS"
	All       Object = "ALL" // SHOW CREATE ALL
)

// Kind names a clause. The values are the clause as written, so a test
// failure or a debug print reads like the statement.
type Kind string

const (
	SetVar    Kind = "SET"        // SET [VAR] K=V ...
	SetRef    Kind = "SET FROM"   // SET [VAR] K FROM '<ref>'
	BlockVar  Kind = "BLOCK"      // BLOCK [VAR] K ...
	UnsetVar  Kind = "UNSET"      // UNSET [VAR] K ...
	Describe  Kind = "DESCRIBE"   // DESCRIBE '<text>'
	UseEnv    Kind = "USE ENV"    // USE ENV a b ...
	AddEnv    Kind = "ADD ENV"    // ADD ENV a [FIRST | LAST | BEFORE b | AFTER b]
	DropEnv   Kind = "DROP ENV"   // DROP ENV a b ...
	UsePilot  Kind = "USE PILOT"  // USE PILOT p
	DropPilot Kind = "DROP PILOT" // DROP PILOT
	RenameTo  Kind = "RENAME TO"  // RENAME TO n
	Alias     Kind = "ALIAS"      // ALIAS launcher
	NoAlias   Kind = "NO ALIAS"   // NO ALIAS
	From      Kind = "FROM"       // CREATE PLAYBOOK ... FROM <source>
	Branch    Kind = "BRANCH"     // CREATE PLAYBOOK ... BRANCH <ref>
	Subdir    Kind = "SUBDIR"     // CREATE PLAYBOOK ... SUBDIR <dir>
	Link      Kind = "LINK"       // CREATE PLAYBOOK ... LINK <dir>
	Sandbox   Kind = "SANDBOX"    // CREATE PLAYBOOK ... SANDBOX
)

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

	File   string // APPLY <file>
	DryRun bool   // APPLY <file> --dry-run

	Pos Pos
}

// Clause is one clause of a CREATE or ALTER. Which fields are set depends
// on Kind; the rest are zero.
type Clause struct {
	Kind Kind

	Vars   []Var    // SET: one per K=V; SET FROM: exactly one, with Ref
	Keys   []string // BLOCK, UNSET
	Names  []string // USE ENV, DROP ENV; ADD ENV: exactly one
	Arg    string   // USE PILOT, RENAME TO, ALIAS, DESCRIBE, FROM, BRANCH, SUBDIR, LINK
	Where  Where    // ADD ENV
	Anchor string   // ADD ENV ... BEFORE/AFTER <anchor>

	Pos Pos
}

// Var is one variable of a SET clause: a literal Value, or a secret
// reference in Ref (never both). A reference is stored, never resolved, by
// anything in cpb except the launch exec through with-secret.
type Var struct {
	Key   string
	Value string
	Ref   string
}

// Write reports whether the statement changes state.
func (s *Stmt) Write() bool {
	return s.Verb == Create || s.Verb == Alter || s.Verb == Drop
}
