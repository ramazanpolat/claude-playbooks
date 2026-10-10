package grammar

import (
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/launcher"
)

// A playbook property: a value the playbook keeps that can change after it
// is created. CREATE PLAYBOOK … SET k = 'v', … gives the starting values,
// ALTER PLAYBOOK … SET k = 'v', … changes them, and ALTER PLAYBOOK … DELETE
// k, … puts them back to the default. What is fixed at creation (FROM,
// BRANCH, SUBDIR, LINK) stays a keyword clause of CREATE.
type playbookProperty struct {
	key string
	// values are an enumerated property's values, its default first: the
	// one DELETE restores and a new playbook gets when its CREATE does not
	// name the key. Nil for a free value, which check judges.
	values []string
	// check judges a free value: "" when it is valid, else why not. It
	// never quotes the value back, which might be anything.
	check func(string) string
	// set and del are the clauses a free property's SET and DELETE become
	// (desugarProperties): the ones the commands already carry out.
	set, del Kind
	// placeholder spells a free value in a hint.
	placeholder string
}

// The properties, in the order SHOW CREATE writes them.
var playbookPropertyTable = []playbookProperty{
	// launcher: the command that runs the playbook; '' for none. DELETE
	// puts back its default, the playbook's own name.
	{key: "launcher", check: checkLauncher, set: Launcher, del: DefaultLauncher, placeholder: "<name>"},
	// login: shared links the machine's login; isolated shares nothing
	// (the manifest's isolated_login).
	{key: "login", values: []string{"shared", "isolated"}},
	// memory: isolated keeps ~/.claude's CLAUDE.md and rules out of the
	// playbook (claudeMdExcludes in its settings.json); shared loads them,
	// as Claude Code's ancestor walk does for any directory under $HOME.
	{key: "memory", values: []string{"isolated", "shared"}},
	// model: the playbook's default model (settings.json model).
	{key: "model", check: checkModel, set: SetModel, del: UnsetModel, placeholder: "<model>"},
	// agent: the agent the main session runs as (settings.json agent).
	{key: "agent", check: checkAgent, set: SetAgent, del: UnsetAgent, placeholder: "<agent>"},
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

// propertyOf finds a property by key, in any case, as keywords match.
func propertyOf(key string) (*playbookProperty, bool) {
	for i := range playbookPropertyTable {
		if strings.EqualFold(playbookPropertyTable[i].key, key) {
			return &playbookPropertyTable[i], true
		}
	}
	return nil, false
}

// PlaybookPropertyKeys lists the playbook properties in SHOW CREATE's order.
func PlaybookPropertyKeys() []string {
	keys := make([]string, len(playbookPropertyTable))
	for i, s := range playbookPropertyTable {
		keys[i] = s.key
	}
	return keys
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
// commands already carry out: SET launcher = 'k' is a Launcher clause (”
// is NoLauncher), SET model = 'm' a SetModel, DELETE agent an UnsetAgent.
// login and memory stay in their SET and DELETE clauses. A key named twice
// in one statement, in one clause or across several, is refused here.
func desugarProperties(in []Clause) ([]Clause, *Error) {
	seen := map[string]bool{}
	var out []Clause
	for _, c := range in {
		if c.Kind != SetProperties && c.Kind != DeleteProperties {
			out = append(out, c)
			continue
		}
		rest := c
		rest.Settings = nil
		var free []Clause
		for _, v := range c.Settings {
			if seen[v.Key] {
				return nil, errAt(c.Pos, v.Key+" is named twice in one statement")
			}
			seen[v.Key] = true
			pr, _ := propertyOf(v.Key)
			if pr.values != nil {
				rest.Settings = append(rest.Settings, v)
				continue
			}
			k := Clause{Pos: c.Pos}
			switch {
			case c.Kind == DeleteProperties:
				k.Kind = pr.del
			case pr.set == Launcher && v.Value == "":
				k.Kind = NoLauncher
			default:
				k.Kind, k.Arg = pr.set, v.Value
			}
			free = append(free, k)
		}
		if len(rest.Settings) > 0 {
			out = append(out, rest)
		}
		out = append(out, free...)
	}
	return out, nil
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
	_, ok := propertyOf(strings.TrimSuffix(p.toks[p.i].Text, ","))
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
	if _, ok := propertyOf(k); !ok {
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
	keys := strings.Join(PlaybookPropertyKeys(), ", ")
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
		k, v, vq := t.Text, "", false
		p.i++
		if withValues {
			if kk, vv, ok := strings.Cut(t.Text, "="); ok {
				k, v, vq = kk, vv, t.Quoted
				// k= 'v': the value is the next word, unless the quotes
				// were in this one (k='', an empty value) or the next word
				// is not a value (the end, a clause, another pair).
				if v == "" && !t.Quoted && !p.atEnd() && !p.isStarter() && !p.atProperty() && p.toks[p.i].Text != "," {
					v, vq = p.toks[p.i].Text, p.toks[p.i].Quoted
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
					v, vq = p.toks[p.i].Text, p.toks[p.i].Quoted
					p.i++
				case strings.HasPrefix(p.toks[p.i].Text, "="): // k ='v'
					v, vq = p.toks[p.i].Text[1:], p.toks[p.i].Quoted
					p.i++
				default:
					return p.notAPair(t, what, form, keys)
				}
			}
		}
		k = strings.ToLower(strings.TrimSuffix(k, ","))
		v = unquoteArg(strings.TrimSuffix(v, ","))
		pr, ok := propertyOf(k)
		if !ok {
			return p.notAProperty(t, k, keys)
		}
		switch {
		case !withValues:
		case pr.values != nil:
			lv := strings.ToLower(v)
			found := false
			for _, want := range pr.values {
				if lv == want {
					found = true
				}
			}
			if !found {
				return errAt(t.Pos, k+" takes '"+strings.Join(pr.values, "' or '")+"'")
			}
			if p.lexed && !vq {
				return errAt(t.Pos, k+" takes a quoted string: "+k+" = '"+lv+"'")
			}
			v = lv
		default:
			if why := pr.check(v); why != "" {
				return errAt(t.Pos, why)
			}
			if p.lexed && !vq {
				return errAt(t.Pos, k+" takes a quoted string: "+k+" = '"+pr.placeholder+"'")
			}
		}
		c.Settings = append(c.Settings, Var{Key: k, Value: v})
		p.quiet = true
	}
	if len(c.Settings) == 0 {
		p.note(PlaybookPropertyKeys()...)
		return p.fail(what + " takes " + form + " (" + keys + ")")
	}
	p.note(p.starters...)
	return nil
}

// notAPair is the error for a word where a pair belongs.
func (p *parser) notAPair(t Token, what, form, keys string) *Error {
	if _, ok := propertyOf(strings.TrimSuffix(t.Text, ",")); !ok {
		return p.notAProperty(t, strings.TrimSuffix(t.Text, ","), keys)
	}
	return errAt(t.Pos, what+" takes "+form+" ("+keys+")")
}

// notAProperty is the error for a key the table does not have. A key shaped
// like a variable (FOO, MAX_TOKENS) gets the variable clause; anything that
// might be a pasted value is shown by position only.
func (p *parser) notAProperty(t Token, k, keys string) *Error {
	word, _, _ := strings.Cut(t.Text, "=")
	switch {
	case keyPattern.MatchString(word) && strings.ToUpper(word) == word && strings.ToLower(word) != word:
		return errAt(t.Pos, word+" is not a playbook property; a variable is SET VAR "+word+"=<value>")
	case safeWord.MatchString(k):
		return errAt(t.Pos, k+" is not a playbook property ("+keys+")")
	}
	return errAt(t.Pos, "not a playbook property ("+keys+")")
}

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
)
