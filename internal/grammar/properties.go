package grammar

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/launcher"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// A playbook property: a value the playbook keeps that can change after it
// is created. CREATE PLAYBOOK … SET k = 'v', … gives the starting values,
// ALTER PLAYBOOK … SET k = 'v', … changes them, and ALTER PLAYBOOK … DELETE
// k, … puts them back to the default. What is fixed at creation (FROM,
// BRANCH, SUBDIR, LINK) stays a keyword clause of CREATE.
type playbookProperty struct {
	key string
	// kind is the value's type: one of values, a string check judges,
	// true or false, or a list of strings (['a', 'b']).
	kind valueKind
	// values are an enumerated property's values, its default first: the
	// one DELETE restores and a new playbook gets when its CREATE does not
	// name the key.
	values []string
	// check judges a free value: "" when it is valid, else why not. It
	// never quotes the value back, which might be anything.
	check func(string) string
	// set and del are the clauses a free property's SET and DELETE become
	// (desugarProperties): the ones the commands already carry out.
	set, del Kind
	// placeholder spells a free value in a hint.
	placeholder string
	// sandbox names the [sandbox] key a sandbox.<key> property is: its
	// pairs become SetSandboxKeys and UnsetSandboxKeys clauses.
	sandbox string
	// table marks a table's name (sandbox, statusline, model_picker): DELETE resets
	// every key of it, and SET does not take it. tableDel is the clause its
	// DELETE becomes (the sandbox's is every [sandbox] key).
	table    bool
	tableDel Kind
}

type valueKind int

const (
	kindEnum valueKind = iota
	kindString
	kindBool
	kindList
	kindInt
)

// The properties, in the order SHOW CREATE writes them.
var playbookPropertyTable = []playbookProperty{
	// launcher: the command that runs the playbook; '' for none. DELETE
	// puts back its default, the playbook's own name.
	{key: "launcher", kind: kindString, check: checkLauncher, set: Launcher, del: DefaultLauncher, placeholder: "<name>"},
	// login: shared links the machine's login; isolated shares nothing
	// (the manifest's isolated_login).
	{key: "login", values: []string{"shared", "isolated"}},
	// memory: isolated keeps ~/.claude's CLAUDE.md and rules out of the
	// playbook (claudeMdExcludes in its settings.json); shared loads them,
	// as Claude Code's ancestor walk does for any directory under $HOME.
	{key: "memory", values: []string{"isolated", "shared"}},
	// model: the playbook's default model (settings.json model).
	{key: "model", kind: kindString, check: checkModel, set: SetModel, del: UnsetModel, placeholder: "<model>"},
	// agent: the agent the main session runs as (settings.json agent).
	{key: "agent", kind: kindString, check: checkAgent, set: SetAgent, del: UnsetAgent, placeholder: "<agent>"},
	// The [sandbox] table, key for key (manifest.SandboxKeys): always
	// sandboxes every launch; the rest say how.
	{key: "sandbox.always", kind: kindBool, sandbox: "always"},
	{key: "sandbox.backend", kind: kindString, check: checkSandboxValue, sandbox: "backend", placeholder: "<backend>"},
	{key: "sandbox.host", kind: kindString, check: checkSandboxValue, sandbox: "host", placeholder: "<user@host>"},
	{key: "sandbox.workdir", kind: kindString, check: checkSandboxValue, sandbox: "workdir", placeholder: "<dir>"},
	{key: "sandbox.mounts", kind: kindList, sandbox: "mounts"},
	{key: "sandbox.allow_net", kind: kindList, sandbox: "allow_net"},
	{key: "sandbox.secrets", kind: kindEnum, values: []string{"proxy", "env"}, sandbox: "secrets"},
	{key: "sandbox.claude_version", kind: kindString, check: checkSandboxValue, sandbox: "claude_version", placeholder: "<version>"},
	{key: "sandbox.share_skills", kind: kindBool, sandbox: "share_skills"},
	{key: "sandbox", table: true},
	// The status line (settings.json statusLine): its command, and how
	// often it re-renders, in whole seconds. DELETE statusline (or
	// statusline.command) removes it, the refresh with it.
	{key: "statusline.command", kind: kindString, check: checkStatusline, set: SetStatusline, del: UnsetStatusline, placeholder: "<command>"},
	{key: "statusline.refresh", kind: kindInt, set: SetStatuslineRefresh, del: UnsetStatuslineRefresh},
	{key: "statusline", table: true, tableDel: UnsetStatusline},
	// model_picker.mode: the /model picker shows its rows only, or after
	// the built-in ones. DELETE model_picker is the mode too: the rows are
	// a collection, which DROP MODEL empties.
	{key: "model_picker.mode", kind: kindEnum, values: []string{"append", "only"}, set: SetModelPicker, del: UnsetModelPickerMode},
	{key: "model_picker", table: true, tableDel: UnsetModelPickerMode},
}

// defaultsPropertyTable is DEFAULTS' one property: the secret helper.
var defaultsPropertyTable = []playbookProperty{
	{key: "secret_helper", kind: kindString, check: checkHelper, set: SetHelper, del: UnsetHelper, placeholder: "<command>"},
}

func checkStatusline(v string) string {
	if v == "" || strings.ContainsAny(v, "\r\n") {
		return "statusline.command takes a command (DELETE statusline removes it)"
	}
	return ""
}

// One command, exec'd with an argument vector and never through a shell:
// anything with whitespace would be read as arguments.
func checkHelper(v string) string {
	if v == "" || strings.ContainsAny(v, " \t\r\n") {
		return "the secret helper is one command (a name on PATH or an absolute path), without arguments"
	}
	return ""
}

func checkSandboxValue(v string) string {
	if v == "" || strings.ContainsAny(v, "\r\n") {
		return "a sandbox setting takes a value (DELETE sandbox.<key> clears it)"
	}
	return ""
}

func checkLauncher(v string) string {
	switch {
	case v == "":
		return "" // no launcher
	case IsKeyword(v):
		return "a launcher name is never a keyword"
	case launcher.ValidateName(v) != nil:
		return "invalid launcher name: one word, with no path separator or whitespace (cpb is reserved)"
	}
	return ""
}

func checkModel(v string) string {
	if v == "" || strings.ContainsAny(v, " \t\r\n") {
		return "model takes a model id, one word (DELETE model removes it)"
	}
	return ""
}

func checkAgent(v string) string {
	if !agentPattern.MatchString(v) {
		return "an agent is <name> or <plugin>:<name>, letters, digits, dots, dashes and underscores"
	}
	return ""
}

// propertyIn finds a property of one object's table by key. Keys are
// lowercase and match exactly, so a variable's name (MODEL, LOGIN) is never
// read as a property.
func propertyIn(table []playbookProperty, key string) (*playbookProperty, bool) {
	for i := range table {
		if table[i].key == key {
			return &table[i], true
		}
	}
	return nil, false
}

// propertyOf finds a property of any object: the keys are distinct.
func propertyOf(key string) (*playbookProperty, bool) {
	if pr, ok := propertyIn(playbookPropertyTable, key); ok {
		return pr, true
	}
	return propertyIn(defaultsPropertyTable, key)
}

// props is the table of the object the parser reads: a playbook's unless
// the statement is ALTER DEFAULTS.
func (p *parser) props() []playbookProperty {
	if p.defaults {
		return defaultsPropertyTable
	}
	return playbookPropertyTable
}

// PlaybookPropertyKeys lists the playbook properties in SHOW CREATE's order.
func PlaybookPropertyKeys() []string {
	var keys []string
	for _, s := range playbookPropertyTable {
		if !s.table {
			keys = append(keys, s.key)
		}
	}
	return keys
}

// propertyKeysShown is the key list a message names: the sandbox table's
// keys as one sandbox.<key>.
func propertyKeysShown(table []playbookProperty) string {
	var keys []string
	for _, s := range table {
		switch {
		case s.table:
		case s.sandbox != "":
			if !slices.Contains(keys, "sandbox.<key>") {
				keys = append(keys, "sandbox.<key>")
			}
		default:
			keys = append(keys, s.key)
		}
	}
	return strings.Join(keys, ", ")
}

// PlaybookPropertyValues returns the values an enumerated property takes,
// its default first (nil for a free one); ok is false for a key that is not
// a property.
func PlaybookPropertyValues(key string) (values []string, ok bool) {
	pr, ok := propertyOf(key)
	if !ok {
		return nil, false
	}
	return pr.values, true
}

// PlaybookPropertyDefault is the value DELETE restores to an enumerated
// property.
func PlaybookPropertyDefault(key string) string {
	v, _ := PlaybookPropertyValues(key)
	if len(v) == 0 {
		return ""
	}
	return v[0]
}

// PropertyValue returns the value a clause list gives an enumerated key
// through SET or DELETE (DELETE gives the default), and whether any clause
// names it. A free property's pairs are their own clauses after parsing
// (desugarProperties).
func PropertyValue(clauses []Clause, key string) (string, bool) {
	for _, c := range clauses {
		switch c.Kind {
		case SetProperties:
			for _, v := range c.Settings {
				if v.Key == key {
					return v.Value, true
				}
			}
		case DeleteProperties:
			for _, v := range c.Settings {
				if v.Key == key {
					return PlaybookPropertyDefault(key), true
				}
			}
		}
	}
	return "", false
}

// desugarProperties turns each pair of a free property into the clause the
// commands already carry out: SET launcher = 'k' is a Launcher clause (the
// empty launcher a NoLauncher), SET model = 'm' a SetModel, DELETE agent an
// UnsetAgent.
// login and memory stay in their SET and DELETE clauses. A key named twice
// in one statement, in one clause or across several, is refused here.
func desugarProperties(in []Clause) ([]Clause, *Error) {
	seen := map[string]bool{}
	named := func(pos Pos, key string) *Error {
		// A table (sandbox, model_picker) and one of its keys are the
		// same key twice.
		table, _, dotted := strings.Cut(key, ".")
		dup := seen[key] || (dotted && seen[table])
		for k := range seen {
			if strings.HasPrefix(k, key+".") {
				dup = true
			}
		}
		if dup {
			return errAt(pos, key+" is named twice in one statement")
		}
		seen[key] = true
		return nil
	}
	var out []Clause
	for _, c := range in {
		if c.Kind != SetProperties && c.Kind != DeleteProperties {
			out = append(out, c)
			continue
		}
		// The pairs become clauses in the order written: login and memory
		// side by side stay one SET (or DELETE) clause, sandbox keys side
		// by side one sandbox clause, so the statement renders back as
		// written (mergedClauses).
		var pairs []Clause
		into := func(kind Kind, v Var) {
			if n := len(pairs); n > 0 && pairs[n-1].Kind == kind && (kind == SetProperties || kind == DeleteProperties || kind == SetSandboxKeys || kind == UnsetSandboxKeys) {
				pairs[n-1].Settings = append(pairs[n-1].Settings, v)
				return
			}
			pairs = append(pairs, Clause{Pos: c.Pos, Kind: kind, Settings: []Var{v}})
		}
		sandboxKind := SetSandboxKeys
		if c.Kind == DeleteProperties {
			sandboxKind = UnsetSandboxKeys
		}
		for _, v := range c.Settings {
			if err := named(c.Pos, v.Key); err != nil {
				return nil, err
			}
			pr, _ := propertyOf(v.Key)
			switch {
			case pr.table && pr.tableDel == "":
				for _, k := range manifest.SandboxKeys {
					into(UnsetSandboxKeys, Var{Key: k})
				}
				continue
			case pr.table:
				pairs = append(pairs, Clause{Pos: c.Pos, Kind: pr.tableDel})
				continue
			case pr.sandbox != "":
				into(sandboxKind, Var{Key: pr.sandbox, Value: v.Value})
				continue
			case pr.set == "":
				into(c.Kind, v) // login, memory
				continue
			}
			k := Clause{Pos: c.Pos}
			switch {
			case c.Kind == DeleteProperties:
				k.Kind = pr.del
			case pr.set == Launcher && v.Value == "":
				k.Kind = NoLauncher
			case pr.kind == kindInt:
				k.Kind = pr.set
				k.Refresh, _ = strconv.Atoi(v.Value)
			case pr.set == SetModelPicker:
				k.Kind, k.Arg = pr.set, strings.ToUpper(v.Value)
			default:
				k.Kind, k.Arg = pr.set, v.Value
			}
			pairs = append(pairs, k)
		}
		if !c.IfUnset {
			out = append(out, pairs...)
			continue
		}
		// SET IF UNSET: one clause, which applies whole or not at all.
		group, err := mergeStatusline(pairs)
		if err != nil {
			return nil, err
		}
		out = append(out, Clause{Kind: SetIfUnset, Group: group, Pos: c.Pos})
	}
	return mergeStatusline(joinAdjacent(out))
}

// joinAdjacent makes property clauses of one kind side by side one clause
// (SET login = 'isolated' SET memory = 'shared' is one SET list), so a
// statement parses to the same clauses as its rendering (mergedClauses).
func joinAdjacent(in []Clause) []Clause {
	var out []Clause
	for _, c := range in {
		if n := len(out); n > 0 && out[n-1].Kind == c.Kind {
			switch c.Kind {
			case SetProperties, DeleteProperties, SetSandboxKeys, UnsetSandboxKeys:
				out[n-1].Settings = append(slices.Clone(out[n-1].Settings), c.Settings...)
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

// Flatten lists the clauses with each SET IF UNSET's pairs in its place.
func Flatten(clauses []Clause) []Clause {
	var out []Clause
	for _, c := range clauses {
		if c.Kind == SetIfUnset {
			out = append(out, c.Group...)
			continue
		}
		out = append(out, c)
	}
	return out
}

// mergeStatusline makes statusline.command and statusline.refresh, set in
// one list, one clause: the command and its refresh are one statusLine.
func mergeStatusline(in []Clause) ([]Clause, *Error) {
	line, refresh := -1, -1
	for i, c := range in {
		switch c.Kind {
		case SetStatusline:
			line = i
		case SetStatuslineRefresh:
			refresh = i
		}
	}
	if line < 0 || refresh < 0 {
		return in, nil
	}
	in[line].Refresh = in[refresh].Refresh
	return slices.Delete(in, refresh, refresh+1), nil
}

// atPropertyKey reports a property where a list item is expected: a pair
// for SET, a key for DELETE.
func (p *parser) atPropertyKey(withValues bool) bool {
	if withValues {
		return p.atProperty()
	}
	if p.atEnd() || p.toks[p.i].Quoted {
		return false
	}
	_, ok := propertyIn(p.props(), strings.TrimSuffix(p.toks[p.i].Text, ","))
	return ok
}

// atProperty reports whether the next words are a property pair: a key,
// then = (k = v, k=v, k= v, k =v). It decides between a property and the
// clause keywords that share a word with one (SET MODEL PICKER).
func (p *parser) atProperty() bool {
	if p.atEnd() {
		return false
	}
	t := p.toks[p.i]
	k, _, cut := strings.Cut(t.Text, "=")
	if _, ok := propertyIn(p.props(), k); !ok {
		return false
	}
	if cut {
		return true
	}
	return p.i+1 < len(p.toks) && strings.HasPrefix(p.toks[p.i+1].Text, "=")
}

// playbookProperties reads the body of SET (k = 'v', …) or of DELETE (k, …).
// A pair is one word (k=v, k='v') or three (k = 'v'); items are separated
// by commas, a word's trailing one or a comma of its own, and the comma may
// be left out. In a playbook file, and in a statement given as one quoted
// argument, a value is quoted, as SHOW CREATE writes it; on the command line
// the shell has removed the quotes, so a bare word is the value.
func (p *parser) playbookProperties(c *Clause, what string, withValues bool) *Error {
	keys := propertyKeysShown(p.props())
	form := "<key> = '<value>'"
	if !withValues {
		form = "<key>"
	}
	// A key may share a word with a clause keyword (launcher, model): a
	// pair, or under DELETE a key, is read as one before it ends the list.
	for !p.atEnd() && (!p.isStarter() || p.atPropertyKey(withValues)) {
		t := p.toks[p.i]
		if t.Text == "," && !t.Quoted {
			p.i++
			continue
		}
		// vt is the word the value came from: whether it was quoted, and
		// whether a comma after its quotes separates it from the next pair.
		k, v, vt := t.Text, "", t
		p.i++
		if withValues {
			if kk, vv, ok := strings.Cut(t.Text, "="); ok {
				k, v = kk, vv
				// k= 'v': the value is the next word, unless the quotes
				// were in this one (k='', an empty value) or the next word
				// is not a value (the end, a clause, another pair).
				if v == "" && !t.Quoted && !p.atEnd() && !p.isStarter() && !p.atProperty() && p.toks[p.i].Text != "," {
					v, vt = p.toks[p.i].Text, p.toks[p.i]
					p.i++
				}
			} else {
				switch {
				case p.atEnd():
					return p.notAPair(t, what, form, keys)
				case p.toks[p.i].Text == "=":
					p.i++
					if p.atEnd() || p.isStarter() {
						return errAt(t.Pos, k+" needs a value: "+k+" = '<value>'")
					}
					v, vt = p.toks[p.i].Text, p.toks[p.i]
					p.i++
				case strings.HasPrefix(p.toks[p.i].Text, "="): // k ='v'
					v, vt = p.toks[p.i].Text[1:], p.toks[p.i]
					p.i++
				default:
					return p.notAPair(t, what, form, keys)
				}
			}
		}
		k = strings.TrimSuffix(k, ",")
		pr, ok := propertyIn(p.props(), k)
		if !ok {
			return p.notAProperty(t, k, keys)
		}
		// A comma after the value separates it from the next pair; a comma
		// inside quotes is the value's own. On the command line the shell
		// has removed the quotes, so a trailing comma is a separator.
		raw := v // a list reads its own words
		if !vt.Quoted || vt.Comma {
			v = strings.TrimSuffix(v, ",")
		}
		v = unquoteArg(v)
		// A one-word value reads the same quoted or not, in a file and on the
		// command line; a value with a space or a comma is quoted (SHOW
		// CREATE writes every string quoted).
		if p.lexed && !vt.Quoted && pr.kind != kindList && strings.Contains(v, ",") {
			return errAt(t.Pos, k+": a value with a comma is quoted: "+k+" = '<value>'")
		}
		switch {
		case !withValues:
		case pr.table:
			return errAt(t.Pos, k+" is a table: SET "+k+".<key> = …, or DELETE "+k+" to reset it")
		case pr.kind == kindList:
			items, err := p.listValue(t, k, raw, vt)
			if err != nil {
				return err
			}
			v = strings.Join(items, ",")
		case pr.kind == kindInt:
			if n, err := strconv.Atoi(v); err != nil || n < 1 {
				return errAt(t.Pos, k+" takes a whole number of seconds, at least 1, with no unit")
			}
		case pr.kind == kindBool:
			v = strings.ToLower(v)
			if v != "true" && v != "false" {
				return errAt(t.Pos, k+" takes true or false")
			}
		case pr.kind == kindEnum:
			v = strings.ToLower(v)
			if !slices.Contains(pr.values, v) {
				return errAt(t.Pos, k+" takes '"+strings.Join(pr.values, "' or '")+"'")
			}
		default:
			if why := pr.check(v); why != "" {
				return errAt(t.Pos, why)
			}
		}
		c.Settings = append(c.Settings, Var{Key: k, Value: v})
		p.quiet = true
	}
	if len(c.Settings) == 0 {
		for _, pr := range p.props() {
			if !pr.table {
				p.note(pr.key)
			}
		}
		return p.fail(what + " takes " + form + " (" + keys + ")")
	}
	p.note(p.starters...)
	return nil
}

// listValue reads a list: ['a', 'b'], which may run over several words
// (first is the word it starts in), or 'a,b', the items comma-joined in one
// word, which needs no [ ] on the command line (zsh reads [ ] as a pattern,
// and bash may replace the word with a file name). An item never holds a
// comma; SHOW CREATE writes the [ ] form.
func (p *parser) listValue(t Token, k, first string, ft Token) ([]string, *Error) {
	hint := k + " takes a list: ['a', 'b'], or 'a,b'"
	split := func(text string) ([]string, *Error) {
		var items []string
		for _, item := range strings.Split(text, ",") {
			item = unquoteArg(strings.TrimSpace(item))
			if strings.ContainsAny(item, "\r\n") {
				return nil, errAt(t.Pos, hint)
			}
			if item != "" {
				items = append(items, item)
			}
		}
		return items, nil
	}
	if !strings.HasPrefix(first, "[") {
		if !ft.Quoted || ft.Comma {
			first = strings.TrimSuffix(first, ",")
		}
		items, err := split(unquoteArg(first))
		if err == nil && len(items) == 0 {
			return nil, errAt(t.Pos, hint+"; DELETE "+k+" empties it")
		}
		return items, err
	}
	text := first
	for !strings.HasSuffix(strings.TrimSuffix(text, ","), "]") {
		if p.atEnd() {
			return nil, errAt(t.Pos, hint)
		}
		text += " " + p.toks[p.i].Text
		p.i++
	}
	text = strings.TrimSuffix(text, ",")
	return split(text[1 : len(text)-1])
}

// notAPair is the error for a word where a pair belongs.
func (p *parser) notAPair(t Token, what, form, keys string) *Error {
	if _, ok := propertyIn(p.props(), strings.TrimSuffix(t.Text, ",")); !ok {
		return p.notAProperty(t, strings.TrimSuffix(t.Text, ","), keys)
	}
	return errAt(t.Pos, what+" takes "+form+" ("+keys+")")
}

// notAProperty is the error for a key the table does not have. A key shaped
// like a variable (FOO, MAX_TOKENS) gets the variable clause; anything that
// might be a pasted value is shown by position only.
func (p *parser) notAProperty(t Token, k, keys string) *Error {
	word, _, _ := strings.Cut(t.Text, "=")
	object := "a playbook property"
	if p.defaults {
		object = "a DEFAULTS property"
	}
	if !t.Quoted && word == t.Text {
		if hint := removedWordHint(word); hint != "" {
			return errAt(t.Pos, hint)
		}
	}
	switch {
	case !t.Quoted && strings.EqualFold(word, "IF"):
		return errAt(t.Pos, "IF UNSET goes right after SET: SET IF UNSET <key> = '<value>', …")
	case !p.defaults && isVarName(word):
		return errAt(t.Pos, word+" is not a playbook property; a variable is SET VAR "+word+"=<value>")
	case !t.Quoted && IsKeyword(word):
		return errAt(t.Pos, word+" is not "+object+" ("+keys+")")
	case safeWord.MatchString(k) && func() bool { _, ok := propertyIn(p.props(), strings.ToLower(k)); return ok }():
		return errAt(t.Pos, "property keys are lowercase: "+strings.ToLower(k))
	case safeWord.MatchString(k), sandboxKeyWord.MatchString(k):
		return errAt(t.Pos, k+" is not "+object+" ("+keys+")")
	}
	return errAt(t.Pos, "not "+object+" ("+keys+")")
}

// isVarName reports a word shaped like an environment variable (FOO,
// MAX_TOKENS): all capitals, so never a property key, which is lowercase.
func isVarName(w string) bool {
	return keyPattern.MatchString(w) && strings.ToUpper(w) == w && strings.ToLower(w) != w
}

// sandboxKeyWord is a mistyped sandbox.<key>, safe to quote back.
var sandboxKeyWord = regexp.MustCompile(`^sandbox\.[a-z_]{1,24}$`)

// unquoteArg removes one pair of quotes a command-line word kept: a shell
// passes memory='shared' through as one word when it is quoted whole
// ("memory='shared'"). Property values never hold a quote of their own.
func unquoteArg(v string) string {
	if len(v) >= 2 && (v[0] == '\'' || v[0] == '"') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}

// propertyWords renders a property clause's body: k = 'v', … or k, ….
func propertyWords(c *Clause, withValues bool) []string {
	var w []string
	for i, v := range c.Settings {
		last := i == len(c.Settings)-1
		if !withValues {
			k := v.Key
			if !last {
				k += ","
			}
			w = append(w, k)
			continue
		}
		val := quote(v.Value)
		if !last {
			val += ","
		}
		w = append(w, v.Key, "=", val)
	}
	return w
}

// sandboxAlwaysOn reports a clause that sets sandbox.always = true.
func sandboxAlwaysOn(clauses []Clause) (Pos, bool) {
	for _, c := range clauses {
		if c.Kind == SetSandboxKeys {
			for _, v := range c.Settings {
				if v.Key == "always" && v.Value == "true" {
					return c.Pos, true
				}
			}
		}
	}
	return Pos{}, false
}

// sandboxWords renders SET sandbox.<key> = <value>, … with each value in
// its type: true or false bare, a list as ['a', 'b'], a string quoted.
func sandboxWords(c *Clause) []string {
	var w []string
	for i, v := range c.Settings {
		val := quote(v.Value)
		switch pr, _ := propertyOf("sandbox." + v.Key); pr.kind {
		case kindBool:
			val = v.Value
		case kindList:
			var items []string
			for _, item := range strings.Split(v.Value, ",") {
				if item != "" {
					items = append(items, quote(item))
				}
			}
			val = "[" + strings.Join(items, ", ") + "]"
		}
		if i < len(c.Settings)-1 {
			val += ","
		}
		w = append(w, "sandbox."+v.Key, "=", val)
	}
	return w
}

// removedWordHint is the hint for a word that began a clause the
// properties replaced (LAUNCHER, NO LAUNCHER, SANDBOX, ISOLATED LOGIN).
// The words are not reserved, so a name may be one; where a clause starts,
// they still say what to write instead.
func removedWordHint(w string) string {
	switch strings.ToUpper(w) {
	case "LAUNCHER":
		return removedLauncher
	case "NO":
		return removedNoLauncher
	case "SANDBOX":
		return removedSandbox
	case "ISOLATED":
		return removedLogin
	}
	return ""
}

// removedClauseHint is removedWordHint for the next word, where a clause
// starts.
func removedClauseHint(p *parser) string {
	if p.atEnd() || p.toks[p.i].Quoted {
		return ""
	}
	return removedWordHint(p.toks[p.i].Text)
}

// The clauses the properties replaced say what to write instead. Nothing
// old is accepted.
const (
	removedLogin      = "ISOLATED LOGIN is a property now: SET login = 'isolated'"
	removedUnsetLogin = "UNSET ISOLATED LOGIN is gone: SET login = 'shared'"
	removedLauncher   = "LAUNCHER is a property now: SET launcher = '<name>'"
	removedNoLauncher = "NO LAUNCHER is a property now: SET launcher = ''"
	removedSetModel   = "SET MODEL is a property now: SET model = '<model>'"
	removedUnsetModel = "UNSET MODEL is gone: DELETE model"
	removedSetAgent   = "SET AGENT is a property now: SET agent = '<agent>'"
	removedUnsetAgent = "UNSET AGENT is gone: DELETE agent"
	removedSandbox    = "SANDBOX is a property now: SET sandbox.always = true, login = 'isolated'"
	removedSetSandbox = "SET SANDBOX is a property now: SET sandbox.always = true, login = 'isolated'"
	removedSandboxKey = "SET SANDBOX <key>=<value> is a property now: SET sandbox.<key> = '<value>'"
	removedUnsetBox   = "UNSET SANDBOX is gone: SET sandbox.always = false"
	removedUnsetKey   = "UNSET SANDBOX <key> is gone: DELETE sandbox.<key>"

	removedStatusline      = "SET STATUSLINE is a property now: SET statusline.command = '<command>'[, statusline.refresh = <n>] (SET IF UNSET … for IF UNSET)"
	removedRefresh         = "SET STATUSLINE REFRESH is a property now: SET statusline.refresh = <n>"
	removedPrevious        = "SET STATUSLINE PREVIOUS is gone: REVERT STATUSLINE"
	removedUnsetStatusline = "UNSET STATUSLINE is gone: DELETE statusline"
	removedUnsetRefresh    = "UNSET STATUSLINE REFRESH is gone: DELETE statusline.refresh"
	removedPicker          = "SET MODEL PICKER is a property now: SET model_picker.mode = 'only' | 'append'"
	removedUnsetPicker     = "UNSET MODEL PICKER is gone: DELETE model_picker.mode, and DROP MODEL for its rows"
	removedSetHelper       = "SET SECRET HELPER is a property now: SET secret_helper = '<command>'"
	removedUnsetHelper     = "UNSET SECRET HELPER is gone: DELETE secret_helper"
)
