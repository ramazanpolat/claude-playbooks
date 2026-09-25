package grammar

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// keywords are the grammar's reserved words. None may name a new playbook,
// env set or launcher (spec: "a keyword is not a valid name"). An
// EXISTING object whose name happens to be a keyword can still be addressed
// in the name slot, where there is no ambiguity, and quoted in a setup file.
var keywords = map[string]bool{}

func init() {
	for _, w := range []string{
		"CREATE", "ALTER", "DROP", "SHOW", "EXPLAIN", "APPLY",
		"OR", "REPLACE", "IF", "NOT", "EXISTS",
		"PLAYBOOK", "PLAYBOOKS", "ENV", "ENVS", "DEFAULTS", "ALL",
		"SET", "VAR", "FROM", "BLOCK", "UNSET", "DESCRIBE",
		"USE", "ADD", "FIRST", "LAST", "BEFORE", "AFTER",
		"RENAME", "TO", "ALIAS", "NO",
		"BRANCH", "SUBDIR", "LINK", "SANDBOX",
		"SECRET", "HELPER",
	} {
		keywords[w] = true
	}
}

// IsKeyword reports whether word is a reserved word, in any case.
func IsKeyword(word string) bool { return keywords[strings.ToUpper(word)] }

// IsStatement reports whether a command line is a grammar statement rather
// than one of the short forms (the pre-grammar commands, kept indefinitely).
// Only "create" is both: "cpb create x" is the short form, "cpb create
// playbook x" is the grammar, told apart by whether an object keyword (or
// OR, of CREATE OR REPLACE) follows.
func IsStatement(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch strings.ToUpper(args[0]) {
	case "ALTER", "DROP", "SHOW", "EXPLAIN", "APPLY":
		return true
	case "CREATE":
		if len(args) < 2 {
			return false
		}
		switch strings.ToUpper(args[1]) {
		case "PLAYBOOK", "ENV", "OR":
			return true
		}
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

// ParseFile parses a setup file. Every statement is parsed even after an
// error, and all errors are returned together, so one run reports every
// problem in the file. A setup file holds only CREATE, ALTER and DROP.
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
		s, err := p.statement()
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !s.Write() {
			errs = append(errs, &Error{Pos: s.Pos,
				Msg: string(s.Verb) + " only reads; a setup file holds CREATE, ALTER and DROP statements"})
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
	envStarters            = []string{"SET", "BLOCK", "UNSET", "DESCRIBE"}
	alterPlaybookStarters  = []string{"USE", "ADD", "DROP", "SET", "BLOCK", "UNSET", "RENAME", "ALIAS", "NO"}
	defaultsStarters       = []string{"USE", "ADD", "DROP", "SET", "UNSET"}
	createPlaybookStarters = []string{"FROM", "BRANCH", "SUBDIR", "LINK", "ALIAS", "NO", "SANDBOX"}
)

var (
	// keyPattern matches manifest's own env key rule; it is checked here
	// first so that a malformed key, which may be a pasted secret, is
	// refused without being echoed.
	keyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	// refPattern is the shape shared by every secret reference form:
	// scheme, colon, the rest. Anything else is almost certainly a value
	// pasted where a reference belongs.
	refPattern = regexp.MustCompile(`^[a-z][a-z0-9+.-]*:.+`)
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

// take consumes one required argument token. A keyword is refused unless
// quoted, so "ALIAS" followed by the next clause reads as a missing argument.
func (p *parser) take(what, placeholder string) (Token, *Error) {
	if p.atEnd() || (!p.toks[p.i].Quoted && IsKeyword(p.toks[p.i].Text)) {
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
	if isNew && IsKeyword(t.Text) {
		return errAt(t.Pos, fmt.Sprintf("%q is a keyword and cannot name %s", t.Text, label))
	}
	if obj == Env && manifest.ValidateProfileName(t.Text) != nil {
		return errAt(t.Pos, fmt.Sprintf("invalid env set name %q: use letters, digits, dots, dashes, underscores", t.Text))
	}
	return nil
}

func (p *parser) launcher() (string, *Error) {
	if p.atEnd() {
		p.note("<launcher>")
		return "", p.fail("ALIAS needs <launcher>")
	}
	t := p.toks[p.i]
	if IsKeyword(t.Text) {
		return "", errAt(t.Pos, fmt.Sprintf("%q is a keyword and cannot name a launcher", t.Text))
	}
	p.i++
	return t.Text, nil
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
	switch p.kw("CREATE", "ALTER", "DROP", "SHOW", "EXPLAIN", "APPLY") {
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
	}
	if err == nil && !p.atEnd() {
		err = p.unexpected()
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
	s.Name = name
	return err
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
	switch w := p.kw("PLAYBOOKS", "ENVS", "DEFAULTS", "PLAYBOOK", "ENV"); w {
	case "":
		return p.fail("SHOW needs an object")
	case "PLAYBOOK", "ENV":
		s.Object = Object(w)
		name, err := p.name(s.Object, false)
		s.Name = name
		return err
	default:
		s.Object = Object(w)
	}
	return nil
}

func (p *parser) explain(s *Stmt) *Error {
	if p.kw("PLAYBOOK") == "" {
		return p.fail("EXPLAIN needs PLAYBOOK")
	}
	s.Object = Playbook
	name, err := p.name(Playbook, false)
	s.Name = name
	return err
}

func (p *parser) apply(s *Stmt) *Error {
	if p.file {
		return errAt(s.Pos, "APPLY cannot appear inside a setup file")
	}
	if p.atEnd() {
		p.note("<file>")
		return p.fail("APPLY needs <file>")
	}
	s.File = p.toks[p.i].Text
	p.i++
	if p.kw("--dry-run") != "" {
		s.DryRun = true
	}
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
	case "DESCRIBE":
		c.Kind = Describe
		if p.atEnd() {
			p.note("'<text>'")
			return nil, p.fail("DESCRIBE needs '<text>'")
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
	case "USE", "DROP":
		return c, p.envList(c, w)
	case "ADD":
		return c, p.addEnv(c)
	case "SET":
		if p.kw("VAR") == "" {
			return nil, p.fail("SET inside ALTER PLAYBOOK takes VAR: SET VAR <key>=<value>")
		}
		return c, p.set(c)
	case "BLOCK":
		if p.kw("VAR") == "" {
			return nil, p.fail("BLOCK inside ALTER PLAYBOOK takes VAR: BLOCK VAR <key>")
		}
		c.Kind = BlockVar
		keys, err := p.keys("BLOCK VAR")
		c.Keys = keys
		return c, err
	case "UNSET":
		if p.kw("VAR") == "" {
			return nil, p.fail("UNSET inside ALTER PLAYBOOK takes VAR: UNSET VAR <key>")
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
	case "ALIAS":
		c.Kind = Alias
		name, err := p.launcher()
		c.Arg = name
		return c, err
	case "NO":
		if p.kw("ALIAS") == "" {
			return nil, p.fail("expected ALIAS after NO")
		}
		c.Kind = NoAlias
		return c, nil
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
	case "ALIAS":
		c.Kind = Alias
		name, err := p.launcher()
		c.Arg = name
		return c, err
	case "NO":
		if p.kw("ALIAS") == "" {
			return nil, p.fail("expected ALIAS after NO")
		}
		c.Kind = NoAlias
		return c, nil
	case "SANDBOX":
		c.Kind = Sandbox
		return c, nil
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
	for !p.atEnd() && !p.isStarter() {
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
		c.Vars = append(c.Vars, Var{Key: k, Value: v})
		p.i++
		p.quiet = true
	}
	p.note("<key>=<value>")
	p.note(p.starters...)
	return nil
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
	if !refPattern.MatchString(r.Text) {
		return errAt(r.Pos, "not a secret reference: use a scheme form such as 'keychain:<service>' or 'op://<vault>/<item>/<field>' (the value itself is never stored)")
	}
	p.i++
	p.quiet = true
	c.Kind = SetRef
	c.Vars = []Var{{Key: t.Text, Ref: r.Text}}
	return nil
}

// validate checks what the parser cannot see one clause at a time:
// repetition, contradictions and missing companions.
func validate(s *Stmt) *Error {
	seen := map[Kind]Pos{}
	keys := map[string]bool{}
	envs := map[string]bool{}
	once := map[Kind]bool{
		Describe: true, UseEnv: true, RenameTo: true,
		Alias: true, NoAlias: true, From: true, Branch: true, Subdir: true, Link: true, Sandbox: true,
		SetHelper: true, UnsetHelper: true,
	}
	for _, c := range s.Clauses {
		if _, dup := seen[c.Kind]; dup && once[c.Kind] {
			return errAt(c.Pos, string(c.Kind)+" appears twice")
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
			if envs[n] {
				return errAt(c.Pos, "env set "+n+" appears twice")
			}
			envs[n] = true
		}
		if c.Kind == AddEnv && c.Anchor == c.Names[0] {
			return errAt(c.Pos, "ADD ENV "+c.Anchor+" cannot be placed relative to itself")
		}
	}
	pairs := [][2]Kind{{Alias, NoAlias}, {From, Link}, {SetHelper, UnsetHelper}}
	for _, pr := range pairs {
		_, a := seen[pr[0]]
		_, b := seen[pr[1]]
		if a && b {
			return errAt(s.Pos, string(pr[0])+" and "+string(pr[1])+" cannot be combined")
		}
	}
	if s.Verb == Create && s.Object == Playbook {
		_, from := seen[From]
		for _, k := range []Kind{Branch, Subdir} {
			if pos, ok := seen[k]; ok && !from {
				return errAt(pos, string(k)+" needs FROM <source>")
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
