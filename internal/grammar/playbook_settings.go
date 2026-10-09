package grammar

import "strings"

// A playbook setting, ClickHouse style: CREATE PLAYBOOK … SETTINGS k = 'v',
// ALTER PLAYBOOK … MODIFY SETTING k = 'v' and RESET SETTING k. Values[0] is
// the default: the one RESET SETTING restores and a new playbook gets when
// its CREATE does not name the key.
type playbookSetting struct {
	key    string
	values []string
}

// The settings, in the order SHOW CREATE writes them.
var playbookSettingsTable = []playbookSetting{
	// login: shared links the machine's login; isolated shares nothing
	// (the manifest's isolated_login).
	{"login", []string{"shared", "isolated"}},
	// memory: isolated keeps ~/.claude's CLAUDE.md and rules out of the
	// playbook (claudeMdExcludes in its settings.json); shared loads them,
	// as Claude Code's ancestor walk does for any directory under $HOME.
	{"memory", []string{"isolated", "shared"}},
}

// PlaybookSettingKeys lists the playbook settings in SHOW CREATE's order.
func PlaybookSettingKeys() []string {
	keys := make([]string, len(playbookSettingsTable))
	for i, s := range playbookSettingsTable {
		keys[i] = s.key
	}
	return keys
}

// PlaybookSettingValues returns the values a setting takes, its default
// first; ok is false for a key that is not a setting.
func PlaybookSettingValues(key string) (values []string, ok bool) {
	for _, s := range playbookSettingsTable {
		if s.key == key {
			return s.values, true
		}
	}
	return nil, false
}

// PlaybookSettingDefault is the value RESET SETTING restores.
func PlaybookSettingDefault(key string) string {
	v, _ := PlaybookSettingValues(key)
	if len(v) == 0 {
		return ""
	}
	return v[0]
}

// SettingValue returns the value a clause list gives key through SETTINGS,
// MODIFY SETTING or RESET SETTING (RESET gives the default), and whether
// any clause names it.
func SettingValue(clauses []Clause, key string) (string, bool) {
	for _, c := range clauses {
		switch c.Kind {
		case PlaybookSettings, ModifySetting:
			for _, v := range c.Settings {
				if v.Key == key {
					return v.Value, true
				}
			}
		case ResetSetting:
			for _, v := range c.Settings {
				if v.Key == key {
					return PlaybookSettingDefault(key), true
				}
			}
		}
	}
	return "", false
}

// playbookSettings reads the body of SETTINGS and MODIFY SETTING (k = 'v',
// …) or of RESET SETTING (k, …). A setting is one word (k=v, k='v') or
// three (k = 'v'); items are separated by commas, a word's trailing one or
// a comma of its own. Values may be quoted or not: the shell removes the
// quotes of a command line, and SHOW CREATE writes them quoted.
func (p *parser) playbookSettings(c *Clause, what string, withValues bool) *Error {
	keys := strings.Join(PlaybookSettingKeys(), ", ")
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
		k, v := t.Text, ""
		p.i++
		if withValues {
			if kk, vv, ok := strings.Cut(t.Text, "="); ok {
				k, v = kk, vv
				if v == "" { // k= 'v'
					if p.atEnd() {
						return errAt(t.Pos, what+" "+kk+" needs a value: "+kk+" = '<value>'")
					}
					v = p.toks[p.i].Text
					p.i++
				}
			} else {
				switch {
				case p.atEnd():
					return errAt(t.Pos, what+" takes "+form+" ("+keys+")")
				case p.toks[p.i].Text == "=":
					p.i++
					if p.atEnd() {
						return errAt(t.Pos, what+" "+k+" needs a value: "+k+" = '<value>'")
					}
					v = p.toks[p.i].Text
					p.i++
				case strings.HasPrefix(p.toks[p.i].Text, "="): // k ='v'
					v = p.toks[p.i].Text[1:]
					p.i++
				default:
					return errAt(t.Pos, what+" takes "+form+" ("+keys+")")
				}
			}
		}
		k = strings.TrimSuffix(k, ",")
		v = unquoteArg(strings.TrimSuffix(v, ","))
		values, ok := PlaybookSettingValues(k)
		if !ok {
			if safeWord.MatchString(k) {
				return errAt(t.Pos, k+" is not a playbook setting ("+keys+")")
			}
			return errAt(t.Pos, "not a playbook setting ("+keys+")")
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
			v = lv
		}
		c.Settings = append(c.Settings, Var{Key: k, Value: v})
		p.quiet = true
	}
	if len(c.Settings) == 0 {
		p.note(PlaybookSettingKeys()...)
		return p.fail(what + " takes " + form + " (" + keys + ")")
	}
	p.note(p.starters...)
	return nil
}

// unquoteArg removes one pair of quotes a command-line word kept: a shell
// passes memory='shared' through as one word when it is quoted whole
// ("memory='shared'"). Setting values never hold a quote of their own.
func unquoteArg(v string) string {
	if len(v) >= 2 && (v[0] == '\'' || v[0] == '"') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}

// settingWords renders a settings clause's body: k = 'v', … or k, ….
func settingWords(c *Clause, withValues bool) []string {
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

// removedLogin is the hint for the ISOLATED LOGIN forms that playbook
// settings replaced in v4.0.0-rc3.
const removedLogin = "ISOLATED LOGIN is gone: write SETTINGS login = 'isolated' in CREATE PLAYBOOK, " +
	"MODIFY SETTING login = 'isolated' | 'shared' in ALTER PLAYBOOK"
