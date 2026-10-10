package grammar

import "strings"

// A playbook property: a value the playbook keeps that can change after it
// is created. CREATE PLAYBOOK … SET k = 'v', … gives the starting values,
// ALTER PLAYBOOK … SET k = 'v', … changes them, and ALTER PLAYBOOK … DELETE
// k, … puts them back to the default. What is fixed at creation (FROM,
// BRANCH, SUBDIR, LINK) stays a keyword clause of CREATE. Values[0] is the
// default: the one DELETE restores and a new playbook gets when its CREATE
// does not name the key.
type playbookProperty struct {
	key    string
	values []string
}

// The properties, in the order SHOW CREATE writes them.
var playbookPropertyTable = []playbookProperty{
	// login: shared links the machine's login; isolated shares nothing
	// (the manifest's isolated_login).
	{"login", []string{"shared", "isolated"}},
	// memory: isolated keeps ~/.claude's CLAUDE.md and rules out of the
	// playbook (claudeMdExcludes in its settings.json); shared loads them,
	// as Claude Code's ancestor walk does for any directory under $HOME.
	{"memory", []string{"isolated", "shared"}},
}

// PlaybookPropertyKeys lists the playbook properties in SHOW CREATE's order.
func PlaybookPropertyKeys() []string {
	keys := make([]string, len(playbookPropertyTable))
	for i, s := range playbookPropertyTable {
		keys[i] = s.key
	}
	return keys
}

// PlaybookPropertyValues returns the values a property takes, its default
// first; ok is false for a key that is not a property. Keys match in any
// case, as keywords do.
func PlaybookPropertyValues(key string) (values []string, ok bool) {
	for _, s := range playbookPropertyTable {
		if strings.EqualFold(s.key, key) {
			return s.values, true
		}
	}
	return nil, false
}

// PlaybookPropertyDefault is the value DELETE restores.
func PlaybookPropertyDefault(key string) string {
	v, _ := PlaybookPropertyValues(key)
	if len(v) == 0 {
		return ""
	}
	return v[0]
}

// PropertyValue returns the value a clause list gives key through SET or
// DELETE (DELETE gives the default), and whether any clause names it.
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
	for !p.atEnd() && !p.isStarter() {
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
				if v == "" { // k= 'v'
					if p.atEnd() {
						return errAt(t.Pos, kk+" needs a value: "+kk+" = '<value>'")
					}
					v, vq = p.toks[p.i].Text, p.toks[p.i].Quoted
					p.i++
				}
			} else {
				switch {
				case p.atEnd():
					return p.notAPair(t, what, form, keys)
				case p.toks[p.i].Text == "=":
					p.i++
					if p.atEnd() {
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
		values, ok := PlaybookPropertyValues(k)
		if !ok {
			return p.notAProperty(t, k, keys)
		}
		if withValues {
			lv := strings.ToLower(v)
			found := false
			for _, want := range values {
				if lv == want {
					found = true
				}
			}
			if !found {
				return errAt(t.Pos, k+" takes '"+strings.Join(values, "' or '")+"'")
			}
			if p.lexed && !vq {
				return errAt(t.Pos, k+" takes a quoted string: "+k+" = '"+lv+"'")
			}
			v = lv
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
	if _, ok := PlaybookPropertyValues(strings.TrimSuffix(t.Text, ",")); !ok {
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

// The ISOLATED LOGIN forms, replaced by the login property in v4.0.0-rc3,
// say what to write instead. Nothing old is accepted.
const (
	removedLogin      = "ISOLATED LOGIN is a property now: SET login = 'isolated'"
	removedUnsetLogin = "UNSET ISOLATED LOGIN is gone: SET login = 'shared'"
)
