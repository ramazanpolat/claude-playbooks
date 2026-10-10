package grammar

import (
	"reflect"
	"strings"
	"testing"
)

// Playbook properties: one word (k=v, k='v') or three (k = 'v'),
// comma-separated or not; quoted in a file, bare as the shell hands them over.
func TestParsePlaybookProperties(t *testing.T) {
	want := []Var{{Key: "login", Value: "isolated"}, {Key: "memory", Value: "shared"}}
	for _, src := range []string{
		"CREATE PLAYBOOK x SET login = 'isolated', memory = 'shared';",
		"CREATE PLAYBOOK x SET login='isolated', memory='shared';",
		"CREATE PLAYBOOK x SET login='isolated' memory='shared';",
		"CREATE PLAYBOOK x SET login = 'isolated' , memory = 'shared';",
		"CREATE PLAYBOOK x SET login ='isolated', memory= 'Shared';",
		"CREATE PLAYBOOK x SET LOGIN = 'isolated', Memory = 'shared';",
		"CREATE PLAYBOOK x SET launcher = '' SET login = 'isolated', memory = 'shared';",
	} {
		st, err := ParseFile(src)
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		c := st[0].Clauses[len(st[0].Clauses)-1]
		if c.Kind != SetProperties || !reflect.DeepEqual(c.Settings, want) {
			t.Errorf("%s: %+v", src, c)
		}
	}
	// From argv: the shell has removed the quotes.
	st, err := ParseArgs(strings.Fields("ALTER PLAYBOOK x SET memory = isolated, login = shared SET VAR A=1"))
	if err != nil {
		t.Fatal(err)
	}
	if c := st.Clauses[0]; c.Kind != SetProperties || !reflect.DeepEqual(c.Settings, []Var{{Key: "memory", Value: "isolated"}, {Key: "login", Value: "shared"}}) {
		t.Errorf("SET from argv: %+v", c)
	}
	if st.Clauses[1].Kind != SetVar {
		t.Errorf("the next clause: %+v", st.Clauses[1])
	}
	st, err = ParseArgs(strings.Fields("ALTER PLAYBOOK x SET login=isolated"))
	if err != nil || st.Clauses[0].Settings[0].Value != "isolated" {
		t.Fatalf("login=isolated from argv: %v %+v", err, st)
	}
	// A word quoted whole by the shell keeps its inner quotes.
	st, err = ParseArgs([]string{"ALTER", "PLAYBOOK", "x", "SET", "memory='shared'"})
	if err != nil || st.Clauses[0].Settings[0].Value != "shared" {
		t.Fatalf("memory='shared' as one argv word: %v %+v", err, st)
	}
	st, err = ParseArgs(strings.Fields("ALTER PLAYBOOK x DELETE memory, login"))
	if err != nil {
		t.Fatal(err)
	}
	if c := st.Clauses[0]; c.Kind != DeleteProperties || len(c.Settings) != 2 || c.Settings[0].Key != "memory" || c.Settings[1].Key != "login" {
		t.Errorf("DELETE: %+v", c)
	}
	// SET and DELETE together, and the old clauses beside them.
	st, err = ParseArgs(strings.Fields("ALTER PLAYBOOK x SET login = isolated DELETE memory SET model = 'm'"))
	if err != nil {
		t.Fatal(err)
	}
	if k := []Kind{st.Clauses[0].Kind, st.Clauses[1].Kind, st.Clauses[2].Kind}; !reflect.DeepEqual(k, []Kind{SetProperties, DeleteProperties, SetModel}) {
		t.Errorf("clauses: %v", k)
	}
	if v, ok := PropertyValue(st.Clauses, "memory"); !ok || v != "isolated" {
		t.Errorf("DELETE memory gives the default: %q %v", v, ok)
	}
	// A recipe: ALTER PLAYBOOK with no name takes SET and DELETE first.
	for _, src := range []string{"ALTER PLAYBOOK SET memory = 'shared';", "ALTER PLAYBOOK DELETE memory;"} {
		st, err := ParseFile(src)
		if err != nil || st[0].Name != "" {
			t.Errorf("%s: %v %+v", src, err, st)
		}
	}
}

// SHOW CREATE writes properties quoted, and they parse back the same.
func TestFormatPlaybookProperties(t *testing.T) {
	for src, canon := range map[string]string{
		"CREATE PLAYBOOK x SET launcher = '' SET login='isolated' memory='shared'": "CREATE PLAYBOOK x SET launcher = '' SET login = 'isolated', memory = 'shared'",
		"ALTER PLAYBOOK x SET memory = 'isolated'":                                 "ALTER PLAYBOOK x SET memory = 'isolated'",
		"ALTER PLAYBOOK x DELETE login, memory":                                    "ALTER PLAYBOOK x DELETE login, memory",
		"ALTER PLAYBOOK x DELETE login memory":                                     "ALTER PLAYBOOK x DELETE login, memory",
	} {
		st, err := ParseLine(src)
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		if got := st.String(); got != canon {
			t.Errorf("%s\n got %s\nwant %s", src, got, canon)
		}
		back, err := ParseLine(canon)
		if err != nil {
			t.Fatalf("%s: %v", canon, err)
		}
		last := len(st.Clauses) - 1
		if !reflect.DeepEqual(back.Clauses[last].Settings, st.Clauses[last].Settings) {
			t.Errorf("%s does not parse back: %+v", canon, back.Clauses[last])
		}
		pretty := (&Stmt{Verb: st.Verb, Object: st.Object, Name: st.Name, Clauses: st.Clauses}).Pretty()
		again, err := ParseFile(pretty + ";")
		if err != nil {
			t.Fatalf("Pretty %q: %v", pretty, err)
		}
		for i, c := range again[0].Clauses {
			if c.Kind != back.Clauses[i].Kind || !reflect.DeepEqual(c.Settings, back.Clauses[i].Settings) {
				t.Errorf("Pretty %q: clause %d is %+v", pretty, i, c)
			}
		}
	}
}

func TestPlaybookPropertyErrors(t *testing.T) {
	for src, want := range map[string]string{
		"CREATE PLAYBOOK x SET;":                                        "SET takes <key> = '<value>' (launcher, login, memory, model, agent)",
		"CREATE PLAYBOOK x SET colour = 'blue';":                        "colour is not a playbook property (launcher, login, memory, model, agent)",
		"CREATE PLAYBOOK x SET memory = 'sealed';":                      "memory takes 'isolated' or 'shared'",
		"CREATE PLAYBOOK x SET memory;":                                 "SET takes <key> = '<value>'",
		"CREATE PLAYBOOK x SET memory = ;":                              "needs a value",
		"CREATE PLAYBOOK x DELETE memory;":                              "DELETE is for ALTER PLAYBOOK",
		"ALTER PLAYBOOK x SET;":                                         "SET inside ALTER PLAYBOOK takes VAR, STATUSLINE, MODEL PICKER, SANDBOX or properties",
		"ALTER PLAYBOOK x SET memory = 'shared', memory = 'isolated';":  "memory is named twice in one statement",
		"ALTER PLAYBOOK x SET memory = 'shared' DELETE memory;":         "memory is named twice in one statement",
		"ALTER PLAYBOOK x SET login = 'shared' SET login = 'isolated';": "login is named twice in one statement",
		"ALTER PLAYBOOK x DELETE;":                                      "DELETE takes <key> (launcher, login, memory, model, agent)",
		"ALTER PLAYBOOK x DELETE colour;":                               "colour is not a playbook property",
		"ALTER PLAYBOOK x SET FOO=1;":                                   "FOO is not a playbook property; a variable is SET VAR FOO=<value>",
		"CREATE PLAYBOOK x SANDBOX SET login = 'shared';":               "SANDBOX isolates the login",
		"CREATE PLAYBOOK x LINK /tmp/d SET memory = 'shared';":          "memory does not apply to LINK",
		// In a file a value is quoted, as SHOW CREATE writes it.
		"CREATE PLAYBOOK x SET login = isolated;": "login takes a quoted string: login = 'isolated'",
		"ALTER PLAYBOOK x SET memory=shared;":     "memory takes a quoted string: memory = 'shared'",
		// The forms the properties replaced say what to write instead.
		"CREATE PLAYBOOK x ISOLATED LOGIN;":      "ISOLATED LOGIN is a property now: SET login = 'isolated'",
		"ALTER PLAYBOOK x SET ISOLATED LOGIN;":   "ISOLATED LOGIN is a property now: SET login = 'isolated'",
		"ALTER PLAYBOOK x UNSET ISOLATED LOGIN;": "UNSET ISOLATED LOGIN is gone: SET login = 'shared'",
	} {
		if _, err := ParseFile(src); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", src, err, want)
		}
	}
	// The one-argument form is read by the file lexer, so it quotes too;
	// argv cannot, since the shell removed the quotes.
	if _, err := ParseLine("ALTER PLAYBOOK x SET memory = shared"); err == nil || !strings.Contains(err.Error(), "quoted string") {
		t.Errorf("one quoted argument, bare value: %v", err)
	}
	if _, err := ParseArgs(strings.Fields("ALTER PLAYBOOK x SET memory = shared")); err != nil {
		t.Errorf("argv, bare value: %v", err)
	}
	// The ClickHouse-settings words of the draft are not grammar.
	for _, src := range []string{"CREATE PLAYBOOK x SETTINGS memory = 'shared';", "ALTER PLAYBOOK x MODIFY SETTING memory = 'shared';", "ALTER PLAYBOOK x RESET SETTING memory;"} {
		if _, err := ParseFile(src); err == nil {
			t.Errorf("%s parses", src)
		}
	}
}

func TestExpectPlaybookProperties(t *testing.T) {
	w := strings.Fields
	if got := Expect(w("ALTER PLAYBOOK k DELETE")); !reflect.DeepEqual(got, []string{"launcher", "login", "memory", "model", "agent"}) {
		t.Errorf("ALTER PLAYBOOK k DELETE offers %q", got)
	}
	if got := Expect(w("ALTER PLAYBOOK k SET")); !contains(got, "VAR") || !contains(got, "login") || !contains(got, "memory") {
		t.Errorf("ALTER PLAYBOOK k SET offers %q", got)
	}
	if got := Expect(w("CREATE PLAYBOOK k")); !contains(got, "SET") || contains(got, "ISOLATED") || contains(got, "SETTINGS") {
		t.Errorf("CREATE PLAYBOOK k offers %q", got)
	}
	if got := Expect(w("CREATE PLAYBOOK k SET")); !reflect.DeepEqual(got, []string{"launcher", "login", "memory", "model", "agent"}) {
		t.Errorf("CREATE PLAYBOOK k SET offers %q", got)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
