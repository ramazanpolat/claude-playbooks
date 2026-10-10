package grammar

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

// keywords are the grammar's reserved words. None may name a new playbook,
// env set or launcher (spec: "a keyword is not a valid name"). An
// EXISTING object whose name happens to be a keyword can still be addressed
// in the name slot, where there is no ambiguity, and quoted in a playbook file.
var keywords = map[string]bool{}

func init() {
	for _, w := range []string{
		"CREATE", "ALTER", "DROP", "SHOW", "EXPLAIN", "APPLY",
		"OR", "REPLACE", "IF", "NOT", "EXISTS",
		"PLAYBOOK", "PLAYBOOKS", "ENV", "ENVS", "DEFAULTS", "ALL",
		"SET", "VAR", "FROM", "BLOCK", "UNSET", "DESCRIBE",
		"USE", "ADD", "FIRST", "LAST", "BEFORE", "AFTER",
		"RENAME", "TO", "LAUNCHER", "NO",
		"BRANCH", "SUBDIR", "LINK", "SANDBOX",
		"SECRET", "HELPER", "AS", "PLAINTEXT",
		"INCLUDE", "MARKETPLACE", "PLUGIN", "AGENT",
		"MCP", "SERVER", "COMMAND", "ARGS", "URL", "TRANSPORT", "SSE", "HEADER",
		"ALLOW", "DENY", "TOOL", "STATUSLINE", "MODEL", "SKILL",
		"PICKER", "ONLY", "APPEND", "LABEL", "DESCRIPTION", "BEHAVES", "REFRESH",
		"DELETE",
	} {
		keywords[w] = true
	}
}

// IsKeyword reports whether word is a reserved word, in any case.
func IsKeyword(word string) bool { return keywords[strings.ToUpper(word)] }

// IsStatement reports whether a command line is a grammar statement rather
// than one of the lowercase commands (run, start, update, ...): its first
// word is a statement verb, in any case, or the whole statement is one
// quoted argument.
func IsStatement(args []string) bool {
	if len(args) == 0 {
		return false
	}
	// One quoted argument holding a whole statement (docs: "On the command
	// line"): the shell has nothing to glob or split in it.
	if strings.ContainsAny(args[0], " \t\r\n") {
		return true
	}
	switch strings.ToUpper(args[0]) {
	case "CREATE", "ALTER", "DROP", "SHOW", "EXPLAIN", "APPLY", "INCLUDE", "USE", "SELECT", "DESCRIBE", "DESC": // INCLUDE and USE, to be refused with their reason
		return true
	}
	return false
}

// Error is a parse error. Expected lists what would have been accepted at
// Pos: keywords in their canonical spelling, and placeholders such as
// "<env>" or "<key>=<value>" for names and values.
type Error struct {
	Pos      Pos
	Msg      string
	Expected []string
	// AtEnd reports that the statement ran out rather than went wrong:
	// the input is a valid prefix. Completion relies on the difference.
	AtEnd bool
}

func (e *Error) Error() string {
	s := e.Pos.String() + ": " + e.Msg
	if len(e.Expected) > 0 {
		s += " (expected " + strings.Join(e.Expected, ", ") + ")"
	}
	return s
}

// ParseArgs parses one statement from command-line arguments, the words
// after "cpb".
func ParseArgs(args []string) (*Stmt, error) {
	if len(args) == 0 {
		return nil, &Error{Pos: Pos{Word: 1}, Msg: "empty statement", AtEnd: true}
	}
	p := newParser(argTokens(args), false)
	s, err := p.statement()
	if err != nil {
		return nil, err
	}
	return s, nil
}

// ParseLine parses a statement given as one quoted command-line argument:
// the playbook-file lexer reads it (quotes, doubled quotes, -- comments, an
// optional trailing ';'), and the command line's rules apply to it. It is
// exactly one statement.
func ParseLine(src string) (*Stmt, error) {
	groups, lerr := lexFile(src)
	if lerr != nil {
		return nil, lerr
	}
	if len(groups) != 1 {
		return nil, &Error{Pos: Pos{Word: 1}, Msg: "one quoted argument holds exactly one statement"}
	}
	p := newParser(groups[0], false)
	p.lexed = true
	s, err := p.statement()
	if err != nil {
		return nil, err
	}
	return s, nil
}

// ParseFile parses a playbook file. Every statement is parsed even after an
// error, and all errors are returned together, so one run reports every
// problem in the file. A playbook file holds CREATE, ALTER and DROP, and
// INCLUDE, which the caller expands (the parser reads no files).
func ParseFile(src string) ([]*Stmt, error) {
	groups, lerr := lexFile(src)
	if lerr != nil {
		return nil, lerr
	}
	var (
		out  []*Stmt
		errs []error
	)
	for _, g := range groups {
		p := newParser(g, true)
		p.lexed = true
		s, err := p.statement()
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !s.Write() && s.Verb != Include && s.Verb != Use {
			errs = append(errs, &Error{Pos: s.Pos,
				Msg: string(s.Verb) + " only reads; a playbook file holds CREATE, ALTER and DROP statements, and INCLUDE"})
			continue
		}
		out = append(out, s)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return out, nil
}

// Expect returns what may follow a valid prefix of a statement: keywords in
// canonical spelling and placeholders. It returns nil when args already go
// wrong, or form a statement that takes nothing more. Tab completion is
// built on it, so the words it offers are, by construction, the words the
// parser accepts.
func Expect(args []string) []string {
	p := newParser(argTokens(args), false)
	_, err := p.statement()
	if err != nil && !err.AtEnd {
		return nil
	}
	if p.triedAt != len(p.toks) {
		return nil
	}
	return slices.Clone(p.tried)
}

// Clause starters per context. A list (of keys, env sets) runs until the
// next unquoted starter, so these are also the words that end a list.
var (
	envStarters            = []string{"SET", "BLOCK", "UNSET", "DESCRIPTION"}
	alterPlaybookStarters  = []string{"USE", "ADD", "DROP", "SET", "BLOCK", "UNSET", "RENAME", "LAUNCHER", "NO", "ALLOW", "DENY", "DELETE"}
	defaultsStarters       = []string{"USE", "ADD", "DROP", "SET", "UNSET"}
	createPlaybookStarters = []string{"FROM", "BRANCH", "SUBDIR", "LINK", "LAUNCHER", "NO", "SANDBOX", "SET", "DELETE"}
)

var (
	// keyPattern matches manifest's own env key rule; it is checked here
	// first so that a malformed key, which may be a pasted secret, is
	// refused without being echoed.
	keyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	// safeWord is what an error message may quote back: letters and dashes,
	// the shape of a mistyped keyword. Values, references, and anything
	// token-like (tokens nearly always carry digits) are shown by position
	// only.
	safeWord = regexp.MustCompile(`^[A-Za-z][A-Za-z-]{0,23}$`)
)

type parser struct {
	toks []Token
	i    int
	end  Pos
	file bool
	// lexed: the tokens came through the playbook-file lexer (a file, or a
	// statement given as one quoted argument), so Token.Quoted is known. On
	// the command line the shell has removed the quotes.
	lexed bool

	tried   []string // what was acceptable at position triedAt
	triedAt int

	starters []string // the current clause context
	quiet    bool     // the last token was a value: never echo the next one
}

func newParser(toks []Token, file bool) *parser {
	p := &parser{toks: toks, file: file, triedAt: -1}
	switch {
	case len(toks) == 0:
		p.end = Pos{Word: 1}
	case file:
		last := toks[len(toks)-1].Pos
		p.end = Pos{Line: last.Line, Col: last.Col + len([]rune(toks[len(toks)-1].Text))}
	default:
		p.end = Pos{Word: len(toks) + 1}
	}
	return p
}

func (p *parser) atEnd() bool { return p.i >= len(p.toks) }

func (p *parser) pos() Pos {
	if p.atEnd() {
		return p.end
	}
	return p.toks[p.i].Pos
}

// note records alternatives acceptable at the current position.
func (p *parser) note(alts ...string) {
	if p.triedAt != p.i {
		p.tried, p.triedAt = nil, p.i
	}
	for _, a := range alts {
		if !slices.Contains(p.tried, a) {
			p.tried = append(p.tried, a)
		}
	}
}

// kw consumes the next token when it is one of words (unquoted, any case)
// and returns the word as given. Otherwise it records words as expected here
// and returns "".
func (p *parser) kw(words ...string) string {
	if !p.atEnd() && !p.toks[p.i].Quoted {
		for _, w := range words {
			if strings.EqualFold(p.toks[p.i].Text, w) {
				p.i++
				p.quiet = false
				return w
			}
		}
	}
	p.note(words...)
	return ""
}

func (p *parser) isStarter() bool {
	if p.atEnd() || p.toks[p.i].Quoted {
		return false
	}
	for _, w := range p.starters {
		if strings.EqualFold(p.toks[p.i].Text, w) {
			return true
		}
	}
	return false
}

func (p *parser) fail(msg string) *Error {
	e := &Error{Pos: p.pos(), Msg: msg, AtEnd: p.atEnd()}
	if p.triedAt == p.i {
		e.Expected = slices.Clone(p.tried)
	}
	return e
}

func errAt(pos Pos, msg string) *Error { return &Error{Pos: pos, Msg: msg} }

// unexpected reports the next token, quoting it only when that is safe.
func (p *parser) unexpected() *Error {
	if p.atEnd() {
		return p.fail("statement ends too early")
	}
	t := p.toks[p.i]
	if p.quiet || !safeWord.MatchString(t.Text) {
		msg := "unexpected word"
		if p.quiet {
			msg += " after a value (a value with spaces must be quoted)"
		}
		return p.fail(msg)
	}
	return p.fail(fmt.Sprintf("unexpected %q", t.Text))
}

// take consumes one required argument token: a source, a ref, a directory,
// a command. Any word is accepted, keywords included: only NEW NAMES are
// reserved, and on the command line the shell has already removed any
// quotes that could have said otherwise.
func (p *parser) take(what, placeholder string) (Token, *Error) {
	if p.atEnd() {
		p.note(placeholder)
		return Token{}, p.fail(what + " needs " + placeholder)
	}
	t := p.toks[p.i]
	p.i++
	return t, nil
}

var placeholders = map[Object]string{Playbook: "<playbook>", Env: "<env>"}

// name reads an object name. A new name (CREATE, RENAME TO) may not be a
// keyword; an existing one may, since the slot is unambiguous.
func (p *parser) name(obj Object, isNew bool) (string, *Error) {
	ph := placeholders[obj]
	if p.atEnd() {
		p.note(ph)
		return "", p.fail("missing " + ph)
	}
	t := p.toks[p.i]
	if err := checkName(obj, t, isNew); err != nil {
		return "", err
	}
	p.i++
	return t.Text, nil
}

func checkName(obj Object, t Token, isNew bool) *Error {
	label := "a " + strings.Trim(placeholders[obj], "<>")
	if obj == Env {
		label = "an env set"
	}
	if t.Text == "" {
		return errAt(t.Pos, "empty name for "+label)
	}
	if isNew && IsKeyword(t.Text) && !t.Quoted { // quoted, a keyword is a name: SHOW CREATE writes it so
		return errAt(t.Pos, fmt.Sprintf("%q is a keyword and cannot name %s", t.Text, label))
	}
	// The invalid name is never quoted back: whatever landed in a name slot
	// may be a value, a reference or a pasted token.
	if obj == Env && manifest.ValidateSetName(t.Text) != nil {
		return errAt(t.Pos, "invalid env set name: use letters, digits, dots, dashes and underscores")
	}
	if obj == Playbook && isNew && !playbook.NewNamePattern.MatchString(t.Text) {
		return errAt(t.Pos, "invalid playbook name: use letters, digits, underscores and dashes (a name is one safe word)")
	}
	return nil
}

// list reads one or more items up to the next clause starter. An unquoted
// trailing comma is a separator and is dropped; a lone comma is skipped.
func (p *parser) list(what, placeholder string, check func(Token) *Error) ([]string, *Error) {
	var out []string
	for !p.atEnd() && !p.isStarter() {
		t := p.toks[p.i]
		if !t.Quoted {
			t.Text = strings.TrimSuffix(t.Text, ",")
			if t.Text == "" {
				p.i++
				continue
			}
		}
		if err := check(t); err != nil {
			return nil, err
		}
		out = append(out, t.Text)
		p.i++
	}
	p.note(placeholder)
	p.note(p.starters...)
	if len(out) == 0 {
		return nil, p.fail(what + " needs at least one " + placeholder)
	}
	return out, nil
}

func (p *parser) envNames(what string) ([]string, *Error) {
	return p.list(what, "<env>", func(t Token) *Error { return checkName(Env, t, false) })
}

func (p *parser) keys(what string) ([]string, *Error) {
	return p.list(what, "<key>", func(t Token) *Error {
		if !keyPattern.MatchString(t.Text) {
			return errAt(t.Pos, what+" takes variable names")
		}
		if err := manifest.ValidateEnvKey(t.Text); err != nil {
			return errAt(t.Pos, err.Error())
		}
		return nil
	})
}

func (p *parser) statement() (*Stmt, *Error) {
	s := &Stmt{Pos: p.pos()}
	var err *Error
	verbs := []string{"CREATE", "ALTER", "DROP", "SHOW", "EXPLAIN", "APPLY"}
	if p.file {
		verbs = append(verbs, "INCLUDE", "USE")
	} else if p.at("INCLUDE") {
		return nil, errAt(s.Pos, "INCLUDE appears only in a playbook file; on the command line, APPLY <file> [<file> ...] runs several")
	} else if p.at("USE") {
		return nil, errAt(s.Pos, "USE PLAYBOOK appears only in a playbook file; on the command line, APPLY <file> TO <playbook> picks the target")
	}
	switch p.kw(verbs...) {
	case "":
		return nil, p.fail("not a statement")
	case "CREATE":
		s.Verb = Create
		err = p.create(s)
	case "ALTER":
		s.Verb = Alter
		err = p.alter(s)
	case "DROP":
		s.Verb = Drop
		err = p.drop(s)
	case "SHOW":
		s.Verb = Show
		err = p.show(s)
	case "EXPLAIN":
		s.Verb = Explain
		err = p.explain(s)
	case "APPLY":
		s.Verb = Apply
		err = p.apply(s)
	case "INCLUDE":
		s.Verb = Include
		err = p.include(s)
	case "USE":
		s.Verb = Use
		if p.kw("PLAYBOOK") == "" {
			err = p.fail("USE takes PLAYBOOK: USE PLAYBOOK <name>")
			break
		}
		s.Object = Playbook
		s.Name, err = p.name(Playbook, false)
	}
	if err == nil && !p.atEnd() {
		err = p.unexpected()
	}
	if err == nil {
		s.Clauses, err = desugarProperties(s.Clauses)
	}
	if err == nil {
		err = validate(s)
	}
	if err != nil {
		return nil, err
	}
	return s, nil
}

func (p *parser) create(s *Stmt) *Error {
	if p.kw("OR") != "" {
		if p.kw("REPLACE") == "" {
			return p.fail("expected REPLACE after OR")
		}
		s.OrReplace = true
	}
	switch p.kw("PLAYBOOK", "ENV") {
	case "":
		return p.fail("CREATE needs an object")
	case "PLAYBOOK":
		s.Object = Playbook
	case "ENV":
		s.Object = Env
	}
	if s.OrReplace && s.Object == Playbook {
		return errAt(s.Pos, "OR REPLACE applies to ENV only: a playbook is never re-created in place")
	}
	if err := p.ifNotExists(s); err != nil {
		return err
	}
	if s.OrReplace && s.IfNotExists {
		return errAt(s.Pos, "OR REPLACE and IF NOT EXISTS cannot be combined")
	}
	name, err := p.name(s.Object, true)
	if err != nil {
		return err
	}
	s.Name = name
	if s.Object == Env {
		p.starters = envStarters
		return p.clauses(s, p.envClause, 0)
	}
	p.starters = createPlaybookStarters
	return p.clauses(s, p.createPlaybookClause, 0)
}

func (p *parser) ifNotExists(s *Stmt) *Error {
	if p.kw("IF") == "" {
		return nil
	}
	if p.kw("NOT") == "" {
		return p.fail("expected NOT EXISTS after IF")
	}
	if p.kw("EXISTS") == "" {
		return p.fail("expected EXISTS after IF NOT")
	}
	s.IfNotExists = true
	return nil
}

func (p *parser) alter(s *Stmt) *Error {
	switch p.kw("PLAYBOOK", "ENV", "DEFAULTS") {
	case "":
		return p.fail("ALTER needs an object")
	case "PLAYBOOK":
		s.Object = Playbook
		p.starters = alterPlaybookStarters
	case "ENV":
		s.Object = Env
		p.starters = envStarters
	case "DEFAULTS":
		s.Object = Defaults
		p.starters = defaultsStarters
		return p.clauses(s, p.defaultsClause, 1)
	}
	// ALTER PLAYBOOK <clause> …: no name, a recipe. A name is never an
	// unquoted keyword, so a clause word here is unambiguous.
	if s.Object == Playbook && !p.atEnd() && p.isStarter() {
		if !p.file {
			return errAt(p.pos(), "on the command line a statement names its playbook: ALTER PLAYBOOK <name> …")
		}
		s.Recipe = true
		return p.clauses(s, p.playbookClause, 1)
	}
	name, err := p.name(s.Object, false)
	if err != nil {
		return err
	}
	s.Name = name
	if s.Object == Env {
		return p.clauses(s, p.envClause, 1)
	}
	return p.clauses(s, p.playbookClause, 1)
}

func (p *parser) drop(s *Stmt) *Error {
	switch p.kw("PLAYBOOK", "ENV") {
	case "":
		return p.fail("DROP needs an object")
	case "PLAYBOOK":
		s.Object = Playbook
	case "ENV":
		s.Object = Env
	}
	if p.kw("IF") != "" {
		if p.kw("EXISTS") == "" {
			return p.fail("expected EXISTS after IF")
		}
		s.IfExists = true
	}
	name, err := p.name(s.Object, false)
	if err != nil {
		return err
	}
	s.Name = name
	// DROP PLAYBOOK confirms on a terminal; --yes skips the question.
	if s.Object == Playbook && p.kw("--yes") != "" {
		s.Yes = true
	}
	return nil
}

func (p *parser) show(s *Stmt) *Error {
	if p.kw("CREATE") != "" {
		s.ShowCreate = true
		switch p.kw("PLAYBOOK", "ENV", "ALL") {
		case "":
			return p.fail("SHOW CREATE needs an object")
		case "PLAYBOOK":
			s.Object = Playbook
		case "ENV":
			s.Object = Env
		case "ALL":
			s.Object = All
		}
		if s.Object != All {
			name, err := p.name(s.Object, false)
			if err != nil {
				return err
			}
			s.Name = name
		}
		// Without it, SHOW CREATE refuses a playbook or env set that holds a
		// credential-looking literal rather than print it.
		if p.kw("--skip-secrets") != "" {
			s.SkipSecrets = true
		}
		return nil
	}
	// A bare SHOW (or SHOW --json) is SHOW PLAYBOOKS. Anything else that is
	// not an object is still an error, never a silent fallback, and
	// completion still offers every object here.
	if p.atEnd() || p.at("--json") {
		p.kw("PLAYBOOKS", "ENVS", "DEFAULTS", "PLAYBOOK", "ENV", "SESSIONS")
		s.Object = Playbooks
		p.jsonFlag(s)
		return nil
	}
	switch w := p.kw("PLAYBOOKS", "ENVS", "DEFAULTS", "PLAYBOOK", "ENV", "SESSIONS"); w {
	case "":
		return p.fail("SHOW needs an object")
	case "SESSIONS":
		s.Object = Sessions
		if err := p.forPlaybook(s); err != nil {
			return err
		}
	case "PLAYBOOK", "ENV":
		s.Object = Object(w)
		name, err := p.name(s.Object, false)
		if err != nil {
			return err
		}
		s.Name = name
	default:
		s.Object = Object(w)
	}
	p.jsonFlag(s)
	return nil
}

// forPlaybook reads an optional FOR PLAYBOOK <name> (SHOW SESSIONS).
func (p *parser) forPlaybook(s *Stmt) *Error {
	if p.kw("FOR") == "" {
		return nil
	}
	if s.For != "" {
		return errAt(p.toks[p.i-1].Pos, "FOR PLAYBOOK appears twice")
	}
	if p.kw("PLAYBOOK") == "" {
		return p.fail("FOR takes PLAYBOOK: FOR PLAYBOOK <name>")
	}
	name, err := p.name(Playbook, false)
	if err != nil {
		return err
	}
	s.For = name
	return nil
}

// sessionID is a Claude Code session id as cpb uses one: one safe word (a
// UUID, in practice), since it names a file under projects/.
var sessionID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

// ValidSessionID reports whether id is a session id cpb uses in a path or a
// printed command; one read from Claude Code's files that is not is ignored.
func ValidSessionID(id string) bool { return sessionID.MatchString(id) }

// jsonFlag reads the optional --json of SHOW and EXPLAIN: the stable,
// scriptable form of their output.
func (p *parser) jsonFlag(s *Stmt) {
	if p.kw("--json") != "" {
		s.JSON = true
	}
}

func (p *parser) explain(s *Stmt) *Error {
	if p.kw("PLAYBOOK") == "" {
		return p.fail("EXPLAIN needs PLAYBOOK")
	}
	s.Object = Playbook
	name, err := p.name(Playbook, false)
	if err != nil {
		return err
	}
	s.Name = name
	p.jsonFlag(s)
	return nil
}

func (p *parser) apply(s *Stmt) *Error {
	if p.file {
		return errAt(s.Pos, "APPLY cannot appear inside a playbook file")
	}
	// One or more files, run in the order given; --yes is the second
	// confirmation a file with DROP PLAYBOOK needs.
	for !p.atEnd() {
		switch p.kw("--dry-run", "--yes", "--json", "TO") {
		case "--dry-run":
			s.DryRun = true
		case "--yes":
			s.Yes = true
		case "--json":
			s.JSON = true
		case "TO":
			if s.Target != "" {
				return errAt(p.toks[p.i-1].Pos, "TO appears twice")
			}
			t, err := p.take("TO", "<playbook|dir>")
			if err != nil {
				return err
			}
			if t.Text == "" {
				return errAt(t.Pos, "TO needs <playbook|dir>")
			}
			s.Target = t.Text
		default:
			t := p.toks[p.i]
			if !t.Quoted && strings.HasPrefix(t.Text, "--") {
				return nil // an unknown flag: the caller reports it
			}
			s.Files = append(s.Files, t.Text)
			p.i++
		}
	}
	p.note("<file>")
	p.kw("--dry-run", "--yes", "--json")
	if len(s.Files) == 0 {
		return p.fail("APPLY needs <file>")
	}
	return nil
}

// include reads INCLUDE '<path>'. Which files may be included, and how a
// relative path resolves, is the caller's question: this package reads no
// files.
func (p *parser) include(s *Stmt) *Error {
	t, err := p.take("INCLUDE", "'<path>'")
	if err != nil {
		return err
	}
	if t.Text == "" {
		return errAt(t.Pos, "INCLUDE needs a path")
	}
	s.Files = []string{t.Text}
	return nil
}

// clauses reads clauses with one until the statement ends.
func (p *parser) clauses(s *Stmt, one func() (*Clause, *Error), min int) *Error {
	for !p.atEnd() {
		c, err := one()
		if err != nil {
			return err
		}
		s.Clauses = append(s.Clauses, *c)
	}
	p.note(p.starters...)
	if len(s.Clauses) < min {
		return p.fail(fmt.Sprintf("%s %s needs at least one clause", s.Verb, s.Object))
	}
	return nil
}

func (p *parser) envClause() (*Clause, *Error) {
	c := &Clause{Pos: p.pos()}
	switch p.kw(envStarters...) {
	case "":
		return nil, p.unexpected()
	case "SET":
		p.kw("VAR")
		return c, p.set(c)
	case "BLOCK":
		p.kw("VAR")
		c.Kind = BlockVar
		keys, err := p.keys("BLOCK")
		c.Keys = keys
		return c, err
	case "UNSET":
		p.kw("VAR")
		c.Kind = UnsetVar
		keys, err := p.keys("UNSET")
		c.Keys = keys
		return c, err
	case "DESCRIPTION":
		c.Kind = Description
		if p.atEnd() {
			p.note("'<text>'")
			return nil, p.fail("DESCRIPTION needs '<text>'")
		}
		c.Arg = p.toks[p.i].Text
		p.i++
		p.quiet = true
		return c, nil
	}
	return nil, nil
}

func (p *parser) playbookClause() (*Clause, *Error) {
	c := &Clause{Pos: p.pos()}
	switch w := p.kw(alterPlaybookStarters...); w {
	case "":
		return nil, p.unexpected()
	case "USE":
		return c, p.envList(c, w)
	case "ADD", "DROP":
		switch p.kw("ENV", "MARKETPLACE", "PLUGIN", "MCP", "SKILL", "MODEL") {
		case "ENV":
			if w == "ADD" {
				return c, p.addEnvRest(c)
			}
			return c, p.envListRest(c, w)
		case "MARKETPLACE":
			return c, p.marketplace(c, w)
		case "PLUGIN":
			return c, p.plugin(c, w)
		case "MCP":
			return c, p.mcpServer(c, w)
		case "SKILL":
			return c, p.skill(c, w)
		case "MODEL":
			return c, p.pickerModel(c, w)
		}
		return nil, p.fail(w + " takes ENV, MARKETPLACE, PLUGIN, MCP SERVER, SKILL or MODEL")
	case "DELETE":
		c.Kind = DeleteProperties
		return c, p.playbookProperties(c, "DELETE", false)
	case "SET":
		switch {
		case p.atProperty():
			// k = 'v', …: a property whose key may share a word with a
			// clause keyword (model, SET MODEL PICKER).
			c.Kind = SetProperties
			return c, p.playbookProperties(c, "SET", true)
		case p.at("ISOLATED"):
			return nil, p.fail(removedLogin)
		case p.at("AGENT"):
			return nil, p.fail(removedSetAgent)
		case p.at("SANDBOX"):
			if p.i++; p.atEnd() || p.isStarter() {
				return nil, errAt(c.Pos, removedSetSandbox)
			}
			return nil, errAt(c.Pos, removedSandboxKey)
		}
		switch p.kw("VAR", "STATUSLINE", "MODEL") {
		case "STATUSLINE":
			if p.kw("PREVIOUS") != "" {
				c.Kind = SetStatuslinePrevious
				return c, nil
			}
			if p.kw("REFRESH") != "" {
				c.Kind = SetStatuslineRefresh
				n, err := p.refreshSeconds()
				c.Refresh = n
				return c, err
			}
			c.Kind = SetStatusline
			if err := p.oneWord(c, "SET STATUSLINE", "'<command>'", false); err != nil {
				return c, err
			}
			if p.kw("REFRESH") != "" {
				n, err := p.refreshSeconds()
				c.Refresh = n
				if err != nil {
					return c, err
				}
			}
			if p.kw("IF") != "" {
				if p.kw("UNSET") == "" {
					return c, p.fail("expected UNSET after IF: SET STATUSLINE '<command>' IF UNSET")
				}
				c.IfUnset = true
			}
			return c, nil
		case "MODEL":
			if p.kw("PICKER") != "" {
				c.Kind = SetModelPicker
				switch m := p.kw("ONLY", "APPEND"); m {
				case "":
					return nil, p.fail("SET MODEL PICKER takes ONLY (the picker shows these rows only) or APPEND (after the built-in ones)")
				default:
					c.Arg = m
				}
				return c, nil
			}
			return nil, p.fail(removedSetModel)
		case "":
			// Not a clause keyword: the playbook's properties, k = 'v', ….
			// Another keyword here (SET SECRET HELPER) is a clause of
			// another object, never a property.
			if p.atEnd() || p.isStarter() || (!p.toks[p.i].Quoted && IsKeyword(p.toks[p.i].Text)) {
				p.note(PlaybookPropertyKeys()...)
				return nil, p.fail("SET inside ALTER PLAYBOOK takes VAR, STATUSLINE, MODEL PICKER or properties: <key> = '<value>' (" + propertyKeysShown() + ")")
			}
			c.Kind = SetProperties
			return c, p.playbookProperties(c, "SET", true)
		}
		return c, p.set(c)
	case "ALLOW", "DENY":
		if p.kw("TOOL") == "" {
			return nil, p.fail(w + " takes TOOL: " + w + " TOOL '<rule>'")
		}
		c.Kind = AllowTool
		if w == "DENY" {
			c.Kind = DenyTool
		}
		rules, err := p.toolRules(w + " TOOL")
		c.Names = rules
		return c, err
	case "BLOCK":
		if p.kw("VAR") == "" {
			return nil, p.fail("BLOCK inside ALTER PLAYBOOK takes VAR: BLOCK VAR <key>")
		}
		c.Kind = BlockVar
		keys, err := p.keys("BLOCK VAR")
		c.Keys = keys
		return c, err
	case "UNSET":
		switch {
		case p.at("ISOLATED"):
			return nil, p.fail(removedUnsetLogin)
		case p.at("AGENT"):
			return nil, p.fail(removedUnsetAgent)
		case p.at("SANDBOX"):
			if p.i++; p.atEnd() || p.isStarter() {
				return nil, errAt(c.Pos, removedUnsetBox)
			}
			return nil, errAt(c.Pos, removedUnsetKey)
		}
		switch p.kw("VAR", "TOOL", "STATUSLINE", "MODEL") {
		case "STATUSLINE":
			c.Kind = UnsetStatusline
			if p.kw("REFRESH") != "" {
				c.Kind = UnsetStatuslineRefresh
			}
			return c, nil
		case "MODEL":
			if p.kw("PICKER") == "" {
				return nil, p.fail(removedUnsetModel)
			}
			c.Kind = UnsetModelPicker
			return c, nil
		case "TOOL":
			c.Kind = UnsetTool
			rules, err := p.toolRules("UNSET TOOL")
			c.Names = rules
			return c, err
		case "":
			return nil, p.fail("UNSET inside ALTER PLAYBOOK takes VAR, AGENT, TOOL, STATUSLINE, MODEL or SANDBOX")
		}
		c.Kind = UnsetVar
		keys, err := p.keys("UNSET VAR")
		c.Keys = keys
		return c, err
	case "RENAME":
		if p.kw("TO") == "" {
			return nil, p.fail("expected TO after RENAME")
		}
		c.Kind = RenameTo
		name, err := p.name(Playbook, true)
		c.Arg = name
		return c, err
	case "LAUNCHER":
		return nil, errAt(c.Pos, removedLauncher)
	case "NO":
		return nil, errAt(c.Pos, removedNoLauncher)
	}
	return nil, nil
}

func (p *parser) defaultsClause() (*Clause, *Error) {
	c := &Clause{Pos: p.pos()}
	switch w := p.kw(defaultsStarters...); w {
	case "":
		return nil, p.unexpected()
	case "USE", "DROP":
		return c, p.envList(c, w)
	case "ADD":
		return c, p.addEnv(c)
	case "SET", "UNSET":
		if p.kw("SECRET") == "" || p.kw("HELPER") == "" {
			return nil, p.fail(w + " inside ALTER DEFAULTS takes SECRET HELPER: " + w + " SECRET HELPER")
		}
		if w == "UNSET" {
			c.Kind = UnsetHelper
			return c, nil
		}
		c.Kind = SetHelper
		t, err := p.take("SET SECRET HELPER", "'<command>'")
		if err != nil {
			return nil, err
		}
		// One command, exec'd with an argument vector and never through a
		// shell: anything with whitespace would be read as arguments.
		if t.Text == "" || strings.ContainsAny(t.Text, " \t\r\n") {
			return nil, errAt(t.Pos, "the secret helper is one command (a name on PATH or an absolute path), without arguments")
		}
		c.Arg = t.Text
		return c, nil
	}
	return nil, nil
}

// envList reads the rest of USE ENV <env> ... or DROP ENV <env> ...
func (p *parser) envList(c *Clause, verb string) *Error {
	if p.kw("ENV") == "" {
		return p.fail(verb + " takes ENV: " + verb + " ENV <env> ...")
	}
	return p.envListRest(c, verb)
}

func (p *parser) envListRest(c *Clause, verb string) *Error {
	c.Kind = UseEnv
	if verb == "DROP" {
		c.Kind = DropEnv
	}
	names, err := p.envNames(verb + " ENV")
	c.Names = names
	return err
}

func (p *parser) addEnv(c *Clause) *Error {
	if p.kw("ENV") == "" {
		return p.fail("ADD takes ENV: ADD ENV <env>")
	}
	return p.addEnvRest(c)
}

func (p *parser) addEnvRest(c *Clause) *Error {
	c.Kind = AddEnv
	name, err := p.name(Env, false)
	if err != nil {
		return err
	}
	c.Names = []string{name}
	c.Where = Last
	switch w := p.kw("FIRST", "LAST", "BEFORE", "AFTER"); w {
	case "FIRST", "LAST":
		c.Where = Where(w)
	case "BEFORE", "AFTER":
		c.Where = Where(w)
		anchor, err := p.name(Env, false)
		if err != nil {
			return err
		}
		c.Anchor = anchor
	}
	return nil
}

func (p *parser) createPlaybookClause() (*Clause, *Error) {
	c := &Clause{Pos: p.pos()}
	if p.at("ISOLATED") {
		return nil, p.fail(removedLogin)
	}
	w := p.kw(createPlaybookStarters...)
	switch w {
	case "":
		return nil, p.unexpected()
	case "FROM", "BRANCH", "SUBDIR", "LINK":
		c.Kind = Kind(w)
		ph := map[string]string{"FROM": "<source>", "BRANCH": "<ref>", "SUBDIR": "<dir>", "LINK": "<dir>"}[w]
		t, err := p.take(w, ph)
		c.Arg = t.Text
		return c, err
	case "LAUNCHER":
		return nil, errAt(c.Pos, removedLauncher)
	case "NO":
		return nil, errAt(c.Pos, removedNoLauncher)
	case "SANDBOX":
		return nil, errAt(c.Pos, removedSandbox)
	case "SET":
		c.Kind = SetProperties
		return c, p.playbookProperties(c, "SET", true)
	case "DELETE":
		return nil, p.fail("CREATE starts every property at its default; DELETE is for ALTER PLAYBOOK")
	}
	return nil, nil
}

// set reads the body of SET [VAR]: a list of K=V, or one K FROM '<ref>'.
// Nothing here echoes a value or a reference, nor a token that may be one.
func (p *parser) set(c *Clause) *Error {
	if p.atEnd() || p.isStarter() {
		p.note("<key>=<value>", "<key>")
		return p.fail("SET needs <key>=<value> or <key> FROM '<ref>'")
	}
	t := p.toks[p.i]
	if _, _, ok := strings.Cut(t.Text, "="); !ok {
		return p.setRef(c, t)
	}
	c.Kind = SetVar
	var credentials []Token // credential-looking literals, checked once AS PLAINTEXT is known
	for !p.atEnd() && !p.isStarter() && !p.at("AS") {
		t := p.toks[p.i]
		k, v, ok := strings.Cut(t.Text, "=")
		if !ok {
			return errAt(t.Pos, "expected <key>=<value> (a value with spaces must be quoted)")
		}
		if !keyPattern.MatchString(k) {
			return errAt(t.Pos, "invalid variable name before '='")
		}
		if err := manifest.ValidateEnvKey(k); err != nil {
			return errAt(t.Pos, err.Error())
		}
		// An unquoted trailing comma separates list items: A=1, B=2. It is
		// dropped only when another K=V follows, so a last value that
		// really ends in a comma survives.
		if !t.Quoted && strings.HasSuffix(v, ",") && p.kvAt(p.i+1) {
			v = strings.TrimSuffix(v, ",")
		}
		if err := manifest.ValidateEnvValue(k, v); err != nil {
			return errAt(t.Pos, err.Error())
		}
		if manifest.LooksLikeSecretKey(k) && !manifest.PlainSetting(v) {
			credentials = append(credentials, Token{Text: k, Pos: t.Pos})
		}
		c.Vars = append(c.Vars, Var{Key: k, Value: v})
		p.i++
		p.quiet = true
	}
	p.note("<key>=<value>")
	if p.kw("AS") != "" {
		if p.kw("PLAINTEXT") == "" {
			return p.fail("expected PLAINTEXT after AS")
		}
		c.Plaintext = true
	}
	// A credential is never stored as plain text by accident: it takes a
	// reference, or AS PLAINTEXT saying so. The error names the key only.
	if len(credentials) > 0 && !c.Plaintext {
		k := credentials[0]
		set := "SET" // the fix must parse where it is suggested: a playbook takes SET VAR
		if slices.Equal(p.starters, alterPlaybookStarters) {
			set = "SET VAR"
		}
		return errAt(k.Pos, fmt.Sprintf("%s looks like a credential: use %s %s FROM '<ref>' (needs a secret helper), "+
			"or add AS PLAINTEXT to store the literal knowingly", k.Text, set, k.Text))
	}
	p.note(p.starters...)
	return nil
}

// at reports whether the next token is the unquoted keyword w.
func (p *parser) at(w string) bool {
	return !p.atEnd() && !p.toks[p.i].Quoted && strings.EqualFold(p.toks[p.i].Text, w)
}

func (p *parser) kvAt(i int) bool {
	if i >= len(p.toks) {
		return false
	}
	t := p.toks[i]
	if !t.Quoted {
		for _, w := range p.starters {
			if strings.EqualFold(t.Text, w) {
				return false
			}
		}
	}
	return strings.Contains(t.Text, "=")
}

func (p *parser) setRef(c *Clause, t Token) *Error {
	if !keyPattern.MatchString(t.Text) {
		return errAt(t.Pos, "SET needs <key>=<value> or <key> FROM '<ref>'")
	}
	if err := manifest.ValidateEnvKey(t.Text); err != nil {
		return errAt(t.Pos, err.Error())
	}
	p.i++
	p.quiet = true // what follows the key may be a value typed where FROM belongs
	// The key is not named: a key-shaped word (ghp_..., a bare token) is
	// just as likely a secret pasted where K=V belongs.
	if p.kw("FROM") == "" {
		return p.fail("expected FROM after the key: SET <key>=<value>, or SET <key> FROM '<ref>'")
	}
	if p.atEnd() {
		p.note("'<ref>'")
		return p.fail("FROM needs '<ref>'")
	}
	r := p.toks[p.i]
	if !manifest.LooksLikeRef(r.Text) {
		return errAt(r.Pos, "not a secret reference: use a scheme form such as 'keychain:<service>' or 'op://<vault>/<item>/<field>' (the value itself is never stored)")
	}
	// Keys whose value cpb itself reads never take a reference, at any
	// layer (manifest.RefRefusedKeys).
	if err := manifest.ValidateRefKey(t.Text); err != nil {
		return errAt(t.Pos, err.Error())
	}
	p.i++
	p.quiet = true
	c.Kind = SetRef
	c.Vars = []Var{{Key: t.Text, Ref: r.Text}}
	if p.at("AS") {
		return p.fail("AS PLAINTEXT applies to literal values, not to a reference")
	}
	return nil
}

// validate checks what the parser cannot see one clause at a time:
// repetition, contradictions and missing companions.
func validate(s *Stmt) *Error {
	seen := map[Kind]Pos{}
	keys := map[string]bool{}
	envs := map[string]bool{}
	once := map[Kind]bool{
		Description: true, UseEnv: true, RenameTo: true,
		Launcher: true, NoLauncher: true, DefaultLauncher: true, From: true, Branch: true, Subdir: true, Link: true,
		SetHelper: true, UnsetHelper: true, SetAgent: true, UnsetAgent: true,
		SetStatusline: true, UnsetStatusline: true, SetModel: true, UnsetModel: true,
		SetModelPicker: true, UnsetModelPicker: true,
		SetStatuslineRefresh: true, UnsetStatuslineRefresh: true, SetStatuslinePrevious: true,
	}
	settings := map[string]bool{}
	for _, c := range s.Clauses {
		if _, dup := seen[c.Kind]; dup && once[c.Kind] {
			return errAt(c.Pos, string(c.Kind)+" appears twice")
		}
		for _, v := range c.Settings {
			// A property may be set or deleted in several SET and DELETE
			// clauses, but each key once.
			if c.Kind == SetProperties || c.Kind == DeleteProperties {
				if settings["property "+v.Key] {
					return errAt(c.Pos, v.Key+" is named twice in one statement")
				}
				settings["property "+v.Key] = true
				continue
			}
			name := "sandbox." + v.Key
			if settings[name] {
				return errAt(c.Pos, name+" appears twice; one statement changes a setting once")
			}
			settings[name] = true
		}
		seen[c.Kind] = c.Pos
		var ks []string
		for _, v := range c.Vars {
			ks = append(ks, v.Key)
		}
		for _, k := range append(ks, c.Keys...) {
			if keys[k] {
				return errAt(c.Pos, k+" appears twice; one statement changes a variable once")
			}
			keys[k] = true
		}
		for _, n := range c.Names {
			what := map[Kind]string{AddMarketplace: "marketplace", DropMarketplace: "marketplace",
				AddPlugin: "plugin", DropPlugin: "plugin", AddMCP: "MCP server", DropMCP: "MCP server",
				AllowTool: "tool rule", DenyTool: "tool rule", UnsetTool: "tool rule",
				AddSkill: "skill", DropSkill: "skill", AddModel: "model", DropModel: "model"}[c.Kind]
			if what == "" {
				what = "env set"
			}
			if envs[what+" "+n] {
				return errAt(c.Pos, what+" "+n+" appears twice")
			}
			envs[what+" "+n] = true
		}
		if c.Kind == AddEnv && c.Anchor == c.Names[0] {
			return errAt(c.Pos, "ADD ENV "+c.Anchor+" cannot be placed relative to itself")
		}
	}
	pairs := [][2]Kind{{Launcher, NoLauncher}, {From, Link}, {SetHelper, UnsetHelper}, {SetAgent, UnsetAgent},
		{SetStatusline, UnsetStatusline}, {SetModel, UnsetModel}, {SetModelPicker, UnsetModelPicker},
		{AddModel, UnsetModelPicker}, {DropModel, UnsetModelPicker},
		{SetStatuslineRefresh, UnsetStatuslineRefresh}, {SetStatuslineRefresh, UnsetStatusline},
		{SetStatusline, SetStatuslineRefresh}, {UnsetStatusline, UnsetStatuslineRefresh},
		{SetStatuslinePrevious, SetStatusline}, {SetStatuslinePrevious, UnsetStatusline},
		{SetStatuslinePrevious, SetStatuslineRefresh}, {SetStatuslinePrevious, UnsetStatuslineRefresh}}
	for _, pr := range pairs {
		_, a := seen[pr[0]]
		_, b := seen[pr[1]]
		if a && b {
			return errAt(s.Pos, string(pr[0])+" and "+string(pr[1])+" cannot be combined")
		}
	}
	// A sandbox shares no login with the machine: sandbox.always = true
	// needs login = 'isolated', which it never sets on its own. A CREATE
	// says so in the same statement; an ALTER is checked against the
	// playbook too, when it runs.
	if pos, on := sandboxAlwaysOn(s.Clauses); on {
		login, ok := PropertyValue(s.Clauses, "login")
		switch {
		case ok && login == "shared":
			return errAt(pos, "sandbox.always = true cannot be combined with login = 'shared': a sandbox shares no login with the machine")
		case s.Verb == Create && login != "isolated":
			return errAt(pos, "a sandboxed playbook's login is isolated: SET sandbox.always = true, login = 'isolated'")
		}
	}
	if s.Verb == Create && s.Object == Playbook {
		_, from := seen[From]
		_, link := seen[Link]
		// A linked playbook's manifest and settings.json are the target's:
		// of the properties, only its launcher is this registry's.
		if link {
			for _, c := range s.Clauses {
				key := map[Kind]string{SetModel: "model", SetAgent: "agent"}[c.Kind]
				switch c.Kind {
				case SetProperties:
					key = c.Settings[0].Key
				case SetSandboxKeys:
					key = "sandbox." + c.Settings[0].Key
				}
				if key != "" {
					return errAt(c.Pos, key+" does not apply to LINK: a linked playbook's manifest and settings.json belong to the target")
				}
			}
		}
		for _, k := range []Kind{Branch, Subdir} {
			if pos, ok := seen[k]; ok && !from {
				e := errAt(pos, string(k)+" needs FROM <source>")
				// Unfinished rather than wrong: FROM may still follow, so
				// completion keeps offering it. LINK rules it out.
				e.AtEnd = !link
				return e
			}
		}
	}
	if s.Verb == Create && s.Object == Env {
		if pos, ok := seen[UnsetVar]; ok {
			return errAt(pos, "UNSET has nothing to forget in a new env set")
		}
	}
	return nil
}

// marketplace reads the rest of ADD MARKETPLACE m FROM '<source>' or
// DROP MARKETPLACE m.
func (p *parser) marketplace(c *Clause, verb string) *Error {
	c.Kind = AddMarketplace
	if verb == "DROP" {
		c.Kind = DropMarketplace
	}
	if p.atEnd() {
		p.note("<marketplace>")
		return p.fail(verb + " MARKETPLACE needs <marketplace>")
	}
	t := p.toks[p.i]
	if manifest.ValidateSetName(t.Text) != nil {
		return errAt(t.Pos, "invalid marketplace name: use letters, digits, dots, dashes and underscores")
	}
	p.i++
	c.Names = []string{t.Text}
	if verb == "DROP" {
		return nil
	}
	if p.kw("FROM") == "" {
		return p.fail("ADD MARKETPLACE needs FROM '<source>'")
	}
	src, err := p.take("FROM", "'<source>'")
	if err != nil {
		return err
	}
	if _, serr := MarketplaceSource(src.Text); serr != nil {
		return errAt(src.Pos, serr.Error())
	}
	if RelativeSource(src.Text) && !p.file {
		return errAt(src.Pos, "a relative directory source resolves against its playbook file; on the command line, use '/abs/path' or '~/path'")
	}
	c.Arg = src.Text
	return nil
}

// plugin reads the rest of ADD PLUGIN p@m or DROP PLUGIN p@m.
func (p *parser) plugin(c *Clause, verb string) *Error {
	c.Kind = AddPlugin
	if verb == "DROP" {
		c.Kind = DropPlugin
	}
	if p.atEnd() {
		p.note("<plugin>@<marketplace>")
		return p.fail(verb + " PLUGIN needs <plugin>@<marketplace>")
	}
	t := p.toks[p.i]
	if _, _, ok := PluginID(t.Text); !ok {
		return errAt(t.Pos, "a plugin id is <plugin>@<marketplace>, each part letters, digits, dots, dashes and underscores")
	}
	p.i++
	c.Names = []string{t.Text}
	return nil
}

var agentPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+(:[A-Za-z0-9_.-]+)?$`)

// agent reads the rest of SET AGENT '<agent>': a bare agent name or a
// plugin-namespaced one, stored as typed.
// ValidAgent reports whether SET AGENT accepts a: <name> or <plugin>:<name>.
func ValidAgent(a string) bool { return agentPattern.MatchString(a) }

// PluginID splits a plugin id, <plugin>@<marketplace>.
func PluginID(id string) (plugin, marketplace string, ok bool) {
	plugin, marketplace, ok = strings.Cut(id, "@")
	if !ok || manifest.ValidateSetName(plugin) != nil || manifest.ValidateSetName(marketplace) != nil {
		return "", "", false
	}
	return plugin, marketplace, true
}

// Source kinds of ADD MARKETPLACE, named as Claude Code's settings.json
// names them.
const (
	SourceGitHub    = "github"
	SourceGit       = "git"
	SourceDirectory = "directory"
)

var (
	githubRepo = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	// A branch or tag name, as a marketplace ref: no leading '-' (it would
	// read as a flag), no '..', no trailing '/'.
	githubRef = regexp.MustCompile(`^[A-Za-z0-9_.][A-Za-z0-9_./+-]*$`)
	// What a commit looks like: Claude Code clones a marketplace by branch
	// or tag only, so a SHA would be written and then fail at session start.
	commitSHA = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)
)

// LooksLikeCommit reports a ref of 7 to 40 hex characters: a commit SHA,
// which Claude Code cannot clone a marketplace at.
func LooksLikeCommit(ref string) bool { return commitSHA.MatchString(ref) }

// GitHubSource splits 'github:<owner>/<repo>' with an optional
// '#<ref>' or '@<ref>' (a branch or tag; v3.27.0) into the repository and
// the ref, "" when there is none. A ref that looks like a commit SHA is
// refused: Claude Code cannot pin a marketplace to one.
func GitHubSource(src string) (repo, ref string, err error) {
	rest, ok := strings.CutPrefix(src, "github:")
	if !ok {
		return "", "", errors.New("not a github source")
	}
	repo = rest
	if i := strings.IndexAny(rest, "#@"); i >= 0 {
		repo, ref = rest[:i], rest[i+1:]
		if ref == "" || !githubRef.MatchString(ref) || strings.Contains(ref, "..") || strings.HasSuffix(ref, "/") {
			return "", "", errors.New("a github source is 'github:<owner>/<repo>', optionally with '#<branch or tag>'")
		}
		if LooksLikeCommit(ref) {
			return "", "", errors.New("Claude Code clones marketplaces by branch or tag; a commit cannot be pinned: use a tag at that commit")
		}
	}
	if !githubRepo.MatchString(repo) {
		return "", "", errors.New("a github source is 'github:<owner>/<repo>', optionally with '#<branch or tag>'")
	}
	return repo, ref, nil
}

// MarketplaceSource classifies an ADD MARKETPLACE source by its shape only:
// 'github:<owner>/<repo>' (with an optional #<ref> or @<ref>), a git URL (https://… or git@…), or a directory
// ('/abs/path' or '~/path'). Anything else is refused. The error never
// quotes the source: a URL may carry a token.
func MarketplaceSource(src string) (string, error) {
	switch {
	case strings.HasPrefix(src, "github:"):
		if _, _, err := GitHubSource(src); err != nil {
			return "", err
		}
		return SourceGitHub, nil
	case strings.HasPrefix(src, "https://"):
		u, err := url.Parse(src)
		if err != nil || u.Host == "" {
			return "", errors.New("not a valid git URL")
		}
		if u.User != nil {
			return "", errors.New("a source URL carrying credentials is refused: a playbook's settings.json is not a secret store")
		}
		return SourceGit, nil
	case strings.HasPrefix(src, "git@"):
		return SourceGit, nil
	case strings.HasPrefix(src, "/"), strings.HasPrefix(src, "~/"), RelativeSource(src):
		return SourceDirectory, nil
	}
	return "", errors.New("unsupported marketplace source: use 'github:<owner>/<repo>[#<ref>]', a git URL (https://… or git@…), or a directory ('/abs/path', '~/path', or in a playbook file './path')")
}

// RelativeSource reports a directory source written relative to its
// playbook file ('./…' or '../…'). APPLY resolves it against the file's
// directory before the statement runs; the command line refuses it.
func RelativeSource(src string) bool {
	return strings.HasPrefix(src, "./") || strings.HasPrefix(src, "../")
}

// credentialHeaders are header names that always carry a credential.
var credentialHeaders = map[string]bool{"authorization": true, "proxy-authorization": true, "cookie": true}

// CredentialHeader reports whether an MCP header must take a reference.
func CredentialHeader(name string) bool {
	return credentialHeaders[strings.ToLower(name)] || manifest.LooksLikeSecretKey(strings.ReplaceAll(name, "-", "_"))
}

// mcpServer reads the rest of ADD MCP SERVER n <target> [<part> ...] or
// DROP MCP SERVER n. A credential in VAR or HEADER takes a reference:
// AS PLAINTEXT is not accepted here, because the literal would be written
// into Claude Code's config.
func (p *parser) mcpServer(c *Clause, verb string) *Error {
	if p.kw("SERVER") == "" {
		return p.fail(verb + " MCP takes SERVER: " + verb + " MCP SERVER <name>")
	}
	c.Kind = AddMCP
	if verb == "DROP" {
		c.Kind = DropMCP
	}
	if p.atEnd() {
		p.note("<server>")
		return p.fail(verb + " MCP SERVER needs <server>")
	}
	t := p.toks[p.i]
	if manifest.ValidateSetName(t.Text) != nil {
		return errAt(t.Pos, "invalid MCP server name: use letters, digits, dots, dashes and underscores")
	}
	p.i++
	c.Names = []string{t.Text}
	if verb == "DROP" {
		return nil
	}
	m := &MCP{}
	c.MCP = m
	switch p.kw("COMMAND", "URL") {
	case "":
		return p.fail("ADD MCP SERVER needs COMMAND '<command>' or URL '<url>'")
	case "COMMAND":
		cmd, err := p.take("COMMAND", "'<command>'")
		if err != nil {
			return err
		}
		if cmd.Text == "" {
			return errAt(cmd.Pos, "COMMAND needs a command")
		}
		m.Command = cmd.Text
		if p.kw("ARGS") != "" {
			for !p.atEnd() && !p.mcpPartOrStarter() {
				m.Args = append(m.Args, p.toks[p.i].Text)
				p.i++
			}
			if len(m.Args) == 0 {
				p.note("'<arg>'")
				return p.fail("ARGS needs at least one '<arg>'")
			}
		}
	case "URL":
		u, err := p.take("URL", "'<url>'")
		if err != nil {
			return err
		}
		if !strings.HasPrefix(u.Text, "https://") && !strings.HasPrefix(u.Text, "http://") {
			return errAt(u.Pos, "URL needs an http:// or https:// address")
		}
		if pu, perr := url.Parse(u.Text); perr != nil || pu.User != nil {
			return errAt(u.Pos, "a URL carrying credentials is refused: put them in a HEADER … FROM '<ref>'")
		}
		m.URL = u.Text
		if p.kw("TRANSPORT") != "" {
			if p.kw("SSE") == "" {
				return p.fail("TRANSPORT takes SSE (a remote server is HTTP otherwise)")
			}
			m.SSE = true
		}
	}
	for {
		switch p.kw("VAR", "HEADER") {
		case "VAR":
			if m.URL != "" {
				return errAt(p.toks[p.i-1].Pos, "VAR applies to a COMMAND server; a remote server (URL) takes HEADER")
			}
			if err := p.mcpEnv(m); err != nil {
				return err
			}
		case "HEADER":
			if m.URL == "" {
				return errAt(p.toks[p.i-1].Pos, "HEADER applies to a remote server (URL), not a COMMAND")
			}
			if err := p.mcpHeader(m); err != nil {
				return err
			}
		default:
			p.note(p.starters...)
			return nil
		}
	}
}

// mcpPartOrStarter reports whether the next word ends an ARGS list.
func (p *parser) mcpPartOrStarter() bool {
	return p.at("VAR") || p.at("HEADER") || p.isStarter()
}

func (p *parser) mcpEnv(m *MCP) *Error {
	if p.atEnd() || p.mcpPartOrStarter() {
		p.note("<key>=<value>", "<key>")
		return p.fail("VAR needs <key>=<value> or <key> FROM '<ref>'")
	}
	t := p.toks[p.i]
	k, v, isKV := strings.Cut(t.Text, "=")
	if !isKV {
		if !keyPattern.MatchString(t.Text) {
			return errAt(t.Pos, "VAR needs <key>=<value> or <key> FROM '<ref>'")
		}
		p.i++
		p.quiet = true
		if p.kw("FROM") == "" {
			return p.fail("expected FROM after the key: VAR <key>=<value>, or VAR <key> FROM '<ref>'")
		}
		r, err := p.take("FROM", "'<ref>'")
		if err != nil {
			return err
		}
		if !manifest.LooksLikeRef(r.Text) {
			return errAt(r.Pos, "not a secret reference: use a scheme form such as 'keychain:<service>'")
		}
		m.Env = append(m.Env, Var{Key: t.Text, Ref: r.Text})
		return nil
	}
	for !p.atEnd() && !p.mcpPartOrStarter() {
		t := p.toks[p.i]
		k, v, isKV = strings.Cut(t.Text, "=")
		if !isKV {
			return errAt(t.Pos, "expected <key>=<value> (a value with spaces must be quoted)")
		}
		if !keyPattern.MatchString(k) {
			return errAt(t.Pos, "invalid variable name before '='")
		}
		if manifest.LooksLikeSecretKey(k) && !manifest.PlainSetting(v) {
			return errAt(t.Pos, fmt.Sprintf("%s looks like a credential: on an MCP server it takes a reference, VAR %s FROM '<ref>' (a literal would be written into Claude Code's config)", k, k))
		}
		m.Env = append(m.Env, Var{Key: k, Value: v})
		p.i++
		p.quiet = true
	}
	return nil
}

func (p *parser) mcpHeader(m *MCP) *Error {
	n, err := p.take("HEADER", "'<name>'")
	if err != nil {
		return err
	}
	if n.Text == "" || strings.ContainsAny(n.Text, ": \t\r\n") {
		return errAt(n.Pos, "a header name is one word, without ':'")
	}
	if p.kw("FROM") != "" {
		r, err := p.take("FROM", "'<ref>'")
		if err != nil {
			return err
		}
		if !manifest.LooksLikeRef(r.Text) {
			return errAt(r.Pos, "not a secret reference: use a scheme form such as 'keychain:<service>'")
		}
		m.Headers = append(m.Headers, Var{Key: n.Text, Ref: r.Text})
		return nil
	}
	if CredentialHeader(n.Text) {
		return errAt(n.Pos, fmt.Sprintf("header %s carries a credential: it takes a reference, HEADER '%s' FROM '<ref>' (the whole value, e.g. 'Bearer …')", n.Text, n.Text))
	}
	v, err := p.take("HEADER", "'<value>'")
	if err != nil {
		return err
	}
	p.quiet = true
	m.Headers = append(m.Headers, Var{Key: n.Text, Value: v.Text})
	return nil
}

// toolRules reads the rules of ALLOW / DENY / UNSET TOOL: Claude Code's own
// permission syntax, stored as typed (quote a rule with spaces).
func (p *parser) toolRules(what string) ([]string, *Error) {
	return p.list(what, "'<rule>'", func(t Token) *Error {
		if t.Text == "" || strings.ContainsAny(t.Text, "\r\n") {
			return errAt(t.Pos, what+" takes a rule such as 'Bash(git status)' or 'mcp__server'")
		}
		return nil
	})
}

// refreshSeconds reads the <n> of REFRESH: a whole number of seconds, at
// least 1, with no unit.
func (p *parser) refreshSeconds() (int, *Error) {
	t, err := p.take("REFRESH", "<seconds>")
	if err != nil {
		return 0, err
	}
	n, cerr := strconv.Atoi(t.Text)
	if cerr != nil || n < 1 || strings.TrimLeft(t.Text, "0123456789") != "" {
		return 0, errAt(t.Pos, "REFRESH takes a whole number of seconds, at least 1, with no unit: REFRESH 10")
	}
	return n, nil
}

// pickerModel reads the rest of ADD MODEL '<id>' [LABEL '…'] [DESCRIPTION
// '…'] [BEHAVES AS '<id>'] or DROP MODEL '<id>'.
func (p *parser) pickerModel(c *Clause, w string) *Error {
	c.Kind = AddModel
	if w == "DROP" {
		c.Kind = DropModel
	}
	if err := p.oneWord(c, w+" MODEL", "'<id>'", true); err != nil {
		return err
	}
	c.Names = []string{c.Arg}
	if w == "DROP" {
		return nil
	}
	row := &PickerRow{Model: c.Arg}
	c.Row = row
	for {
		at := p.pos()
		var dst **string
		what := ""
		switch p.kw("LABEL", "DESCRIPTION", "BEHAVES") {
		case "LABEL":
			dst, what = &row.Label, "LABEL"
		case "DESCRIPTION":
			dst, what = &row.Description, "DESCRIPTION"
		case "BEHAVES":
			if p.kw("AS") == "" {
				return p.fail("BEHAVES takes AS: BEHAVES AS '<model>'")
			}
			dst, what = &row.BehavesAs, "BEHAVES AS"
		default:
			return nil
		}
		if *dst != nil {
			return errAt(at, what+" appears twice")
		}
		t, err := p.take(what, "'<text>'")
		if err != nil {
			return err
		}
		if strings.ContainsAny(t.Text, "\r\n") || (what == "BEHAVES AS" && (t.Text == "" || strings.ContainsAny(t.Text, " \t"))) {
			return errAt(t.Pos, what+" needs a one-line value")
		}
		v := t.Text
		*dst = &v
	}
}

// oneWord reads the one argument of SET STATUSLINE / SET MODEL; a model id
// has no whitespace.
func (p *parser) oneWord(c *Clause, what, placeholder string, noSpace bool) *Error {
	t, err := p.take(what, placeholder)
	if err != nil {
		return err
	}
	if t.Text == "" || strings.ContainsAny(t.Text, "\r\n") || (noSpace && strings.ContainsAny(t.Text, " \t")) {
		return errAt(t.Pos, what+" needs "+placeholder)
	}
	c.Arg = t.Text
	return nil
}

// Skill source kinds.
const (
	SkillDirectory = "directory"
	SkillGit       = "git"
)

// SkillSource classifies an ADD SKILL source by its shape: a directory
// ('/abs', '~/path', or in a playbook file './path' or '../path'), or a git
// source (https://, http://, git@, file://, or github:<owner>/<repo>).
func SkillSource(src string) (string, error) {
	switch {
	case strings.HasPrefix(src, "/"), strings.HasPrefix(src, "~/"), RelativeSource(src):
		return SkillDirectory, nil
	case strings.HasPrefix(src, "https://"), strings.HasPrefix(src, "http://"), strings.HasPrefix(src, "file://"):
		if u, err := url.Parse(src); err != nil || u.User != nil {
			return "", errors.New("a git URL carrying credentials is refused")
		}
		return SkillGit, nil
	case strings.HasPrefix(src, "git@"):
		return SkillGit, nil
	case strings.HasPrefix(src, "github:"):
		if !githubRepo.MatchString(strings.TrimPrefix(src, "github:")) {
			return "", errors.New("a github source is 'github:<owner>/<repo>'")
		}
		return SkillGit, nil
	}
	return "", errors.New("unsupported skill source: use a directory ('/abs/dir', '~/dir', or in a playbook file './dir') or a git source (https://…, git@…, github:<owner>/<repo>)")
}

// skill reads the rest of ADD SKILL n FROM <source> [BRANCH b] [SUBDIR d]
// or DROP SKILL n.
func (p *parser) skill(c *Clause, verb string) *Error {
	c.Kind = AddSkill
	if verb == "DROP" {
		c.Kind = DropSkill
	}
	if p.atEnd() {
		p.note("<skill>")
		return p.fail(verb + " SKILL needs <skill>")
	}
	t := p.toks[p.i]
	if manifest.ValidateSetName(t.Text) != nil {
		return errAt(t.Pos, "invalid skill name: use letters, digits, dots, dashes and underscores")
	}
	p.i++
	c.Names = []string{t.Text}
	if verb == "DROP" {
		return nil
	}
	if p.kw("FROM") == "" {
		return p.fail("ADD SKILL needs FROM <source>")
	}
	src, err := p.take("FROM", "<source>")
	if err != nil {
		return err
	}
	kind, serr := SkillSource(src.Text)
	if serr != nil {
		return errAt(src.Pos, serr.Error())
	}
	if RelativeSource(src.Text) && !p.file {
		return errAt(src.Pos, "a relative directory resolves against its playbook file; on the command line, use '/abs/dir' or '~/dir'")
	}
	sk := &Skill{From: src.Text}
	c.Skill = sk
	for {
		switch w := p.kw("BRANCH", "SUBDIR"); w {
		case "":
			return nil
		default:
			if kind != SkillGit {
				return errAt(p.toks[p.i-1].Pos, w+" applies to a git source; a directory is linked as it is")
			}
			t, err := p.take(w, "<"+strings.ToLower(w)+">")
			if err != nil {
				return err
			}
			if w == "BRANCH" {
				sk.Branch = t.Text
			} else {
				sk.Subdir = t.Text
			}
		}
	}
}
