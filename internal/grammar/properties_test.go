package grammar

import (
	"reflect"
	"strings"
	"testing"
)

// Playbook properties: one word (k=v, k='v') or three (k = 'v'),
// comma-separated or not; a one-word value quoted or bare, in a file and on
// the command line alike.
func TestParsePlaybookProperties(t *testing.T) {
	want := []Var{{Key: "login", Value: "isolated"}, {Key: "global_instructions", Value: "inherited"}}
	for _, src := range []string{
		"CREATE PLAYBOOK x SET login = 'isolated', global_instructions = 'inherited';",
		"CREATE PLAYBOOK x SET login='isolated', global_instructions='inherited';",
		"CREATE PLAYBOOK x SET login='isolated' global_instructions='inherited';",
		"CREATE PLAYBOOK x SET login = 'isolated' , global_instructions = 'inherited';",
		"CREATE PLAYBOOK x SET login ='isolated', global_instructions= 'Inherited';",
		"CREATE PLAYBOOK x SET login = isolated, global_instructions = inherited;",
		"CREATE PLAYBOOK x SET login = \"isolated\", global_instructions = 'inherited';",
		"CREATE PLAYBOOK x SET launcher = '' SET login = 'isolated', global_instructions = 'inherited';",
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
	st, err := ParseArgs(strings.Fields("ALTER PLAYBOOK x SET global_instructions = excluded, login = shared SET VAR A=1"))
	if err != nil {
		t.Fatal(err)
	}
	if c := st.Clauses[0]; c.Kind != SetProperties || !reflect.DeepEqual(c.Settings, []Var{{Key: "global_instructions", Value: "excluded"}, {Key: "login", Value: "shared"}}) {
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
	st, err = ParseArgs([]string{"ALTER", "PLAYBOOK", "x", "SET", "global_instructions='inherited'"})
	if err != nil || st.Clauses[0].Settings[0].Value != "inherited" {
		t.Fatalf("global_instructions='inherited' as one argv word: %v %+v", err, st)
	}
	st, err = ParseArgs(strings.Fields("ALTER PLAYBOOK x RESET global_instructions, login"))
	if err != nil {
		t.Fatal(err)
	}
	if c := st.Clauses[0]; c.Kind != ResetProperties || len(c.Settings) != 2 || c.Settings[0].Key != "global_instructions" || c.Settings[1].Key != "login" {
		t.Errorf("RESET: %+v", c)
	}
	// SET and RESET together, and the old clauses beside them.
	st, err = ParseArgs(strings.Fields("ALTER PLAYBOOK x SET login = isolated RESET global_instructions SET model = 'm'"))
	if err != nil {
		t.Fatal(err)
	}
	if k := []Kind{st.Clauses[0].Kind, st.Clauses[1].Kind, st.Clauses[2].Kind}; !reflect.DeepEqual(k, []Kind{SetProperties, ResetProperties, SetModel}) {
		t.Errorf("clauses: %v", k)
	}
	if v, ok := PropertyValue(st.Clauses, "global_instructions"); !ok || v != "excluded" {
		t.Errorf("RESET global_instructions gives the default: %q %v", v, ok)
	}
	// A recipe: ALTER PLAYBOOK with no name takes SET and RESET first.
	for _, src := range []string{"ALTER PLAYBOOK SET global_instructions = 'inherited';", "ALTER PLAYBOOK RESET global_instructions;"} {
		st, err := ParseFile(src)
		if err != nil || st[0].Name != "" {
			t.Errorf("%s: %v %+v", src, err, st)
		}
	}
}

// SHOW CREATE writes properties quoted, and they parse back the same.
func TestFormatPlaybookProperties(t *testing.T) {
	for src, canon := range map[string]string{
		"CREATE PLAYBOOK x SET launcher = '' SET login='isolated' global_instructions='inherited'":         "CREATE PLAYBOOK x SET launcher = '', login = 'isolated', global_instructions = 'inherited'",
		"ALTER PLAYBOOK x SET global_instructions = 'excluded'":                                            "ALTER PLAYBOOK x SET global_instructions = 'excluded'",
		"ALTER PLAYBOOK x RESET login, global_instructions":                                                "ALTER PLAYBOOK x RESET login, global_instructions",
		"ALTER PLAYBOOK x RESET login global_instructions":                                                 "ALTER PLAYBOOK x RESET login, global_instructions",
		"ALTER PLAYBOOK x RESET model, agent, sandbox.host, launcher":                                      "ALTER PLAYBOOK x RESET model, agent, sandbox.host, launcher",
		"ALTER PLAYBOOK x SET model = m SET login = shared SET sandbox.host = h":                           "ALTER PLAYBOOK x SET model = 'm', login = 'shared', sandbox.host = 'h'",
		"ALTER PLAYBOOK x SET login = isolated, model = m, global_instructions = inherited ADD PLUGIN p@m": "ALTER PLAYBOOK x SET login = 'isolated', model = 'm', global_instructions = 'inherited' ADD PLUGIN p@m",
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
		"CREATE PLAYBOOK x SET;":                                                                    "SET takes <key> = '<value>' (launcher, login, global_instructions, trusted_endpoint, model, agent, sandbox.<key>, statusline.command, statusline.refresh, model_picker.mode)",
		"CREATE PLAYBOOK x SET colour = 'blue';":                                                    "colour is not a playbook property (launcher, login, global_instructions, trusted_endpoint, model, agent, sandbox.<key>, statusline.command, statusline.refresh, model_picker.mode)",
		"CREATE PLAYBOOK x SET global_instructions = 'sealed';":                                     "global_instructions takes 'excluded' or 'inherited'",
		"CREATE PLAYBOOK x SET global_instructions;":                                                "SET takes <key> = '<value>'",
		"CREATE PLAYBOOK x SET global_instructions = ;":                                             "needs a value",
		"CREATE PLAYBOOK x RESET global_instructions;":                                              "RESET is for ALTER PLAYBOOK",
		"ALTER PLAYBOOK x SET;":                                                                     "SET inside ALTER PLAYBOOK takes VAR or properties",
		"ALTER PLAYBOOK x SET global_instructions = 'inherited', global_instructions = 'excluded';": "global_instructions is named twice in one statement",
		"ALTER PLAYBOOK x SET global_instructions = 'inherited' RESET global_instructions;":         "global_instructions is named twice in one statement",
		"ALTER PLAYBOOK x SET login = 'shared' SET login = 'isolated';":                             "login is named twice in one statement",
		"ALTER PLAYBOOK x RESET;":                                                                   "RESET takes <key> (launcher, login, global_instructions, trusted_endpoint, model, agent, sandbox.<key>, statusline.command, statusline.refresh, model_picker.mode)",
		"ALTER PLAYBOOK x RESET colour;":                                                            "colour is not a playbook property",
		"ALTER PLAYBOOK x SET FOO=1;":                                                               "FOO is not a playbook property; a variable is SET VAR FOO=<value>",
		"CREATE PLAYBOOK x SET sandbox.always = true, login = 'shared';":                            "cannot be combined with login = 'shared'",
		"CREATE PLAYBOOK x LINK /tmp/d SET global_instructions = 'inherited';":                      "global_instructions does not apply to LINK",
		// Keys are lowercase: a key in capitals is a variable's name, and
		// one in mixed case is told the key's spelling.
		// CREATE has no SET VAR, so its hint never names it.
		"CREATE PLAYBOOK x SET LOGIN = 'isolated';":                "property keys are lowercase: login",
		"CREATE PLAYBOOK x SET FOO = 'bar';":                       "a variable is set by a separate statement after the CREATE",
		"ALTER PLAYBOOK x SET MODEL=glm-5.3;":                      "MODEL is not a playbook property; a variable is SET VAR MODEL=<value>",
		"ALTER PLAYBOOK x SET MODEL = glm-5.3;":                    "MODEL is not a playbook property; a variable is SET VAR MODEL=<value>",
		"CREATE PLAYBOOK x SET Global_Instructions = 'inherited';": "property keys are lowercase: global_instructions",
		// A bare value is one word without a comma.
		"ALTER PLAYBOOK x SET model = a,b;": "model: a value with a comma is quoted",
		// The forms the properties replaced say what to write instead.
		"CREATE PLAYBOOK x ISOLATED LOGIN;":      "ISOLATED LOGIN is a property now: SET login = 'isolated'",
		"ALTER PLAYBOOK x SET ISOLATED LOGIN;":   "ISOLATED LOGIN is a property now: SET login = 'isolated'",
		"ALTER PLAYBOOK x UNSET ISOLATED LOGIN;": "UNSET ISOLATED LOGIN is gone: SET login = 'shared'",
	} {
		if _, err := ParseFile(src); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", src, err, want)
		}
	}
	// A bare one-word value reads the same in the one-argument form (the
	// file lexer) and on argv: one grammar.
	if _, err := ParseLine("ALTER PLAYBOOK x SET global_instructions = inherited"); err != nil {
		t.Errorf("one quoted argument, bare value: %v", err)
	}
	if _, err := ParseArgs(strings.Fields("ALTER PLAYBOOK x SET global_instructions = inherited")); err != nil {
		t.Errorf("argv, bare value: %v", err)
	}
	// SET MODEL=glm-5.3 on argv, a variable without its VAR, is refused,
	// never taken as the model property.
	if _, err := ParseArgs(strings.Fields("ALTER PLAYBOOK x SET MODEL=glm-5.3")); err == nil || !strings.Contains(err.Error(), "SET VAR MODEL=<value>") {
		t.Errorf("argv SET MODEL=glm-5.3: %v", err)
	}
	// A comma inside quotes is the value's own; one after them separates.
	for src, want := range map[string]string{
		"ALTER PLAYBOOK x SET statusline.command = 'cmd a,b,';":                    "cmd a,b,",
		"ALTER PLAYBOOK x SET statusline.command = 'cmd a,b', model = 'm';":        "cmd a,b",
		"ALTER PLAYBOOK x SET statusline.command = 'cmd', statusline.refresh = 2;": "cmd",
	} {
		st, err := ParseFile(src)
		if err != nil || st[0].Clauses[0].Arg != want {
			t.Errorf("%s: %v %+v, want %q", src, err, st, want)
		}
	}
	// The ClickHouse-settings words of the draft are not grammar.
	for _, src := range []string{"CREATE PLAYBOOK x SETTINGS global_instructions = 'inherited';", "ALTER PLAYBOOK x MODIFY SETTING global_instructions = 'inherited';", "ALTER PLAYBOOK x RESET SETTING global_instructions;"} {
		if _, err := ParseFile(src); err == nil {
			t.Errorf("%s parses", src)
		}
	}
}

func TestExpectPlaybookProperties(t *testing.T) {
	w := strings.Fields
	if got := Expect(w("ALTER PLAYBOOK k RESET")); !reflect.DeepEqual(got, []string{"launcher", "login", "global_instructions", "trusted_endpoint", "model", "agent", "sandbox.always", "sandbox.backend", "sandbox.host", "sandbox.workdir", "sandbox.mounts", "sandbox.allow_net", "sandbox.secrets", "sandbox.claude_version", "sandbox.share_skills", "statusline.command", "statusline.refresh", "model_picker.mode"}) {
		t.Errorf("ALTER PLAYBOOK k RESET offers %q", got)
	}
	if got := Expect(w("ALTER PLAYBOOK k SET")); !contains(got, "VAR") || !contains(got, "login") || !contains(got, "global_instructions") {
		t.Errorf("ALTER PLAYBOOK k SET offers %q", got)
	}
	if got := Expect(w("CREATE PLAYBOOK k")); !contains(got, "SET") || contains(got, "ISOLATED") || contains(got, "SETTINGS") {
		t.Errorf("CREATE PLAYBOOK k offers %q", got)
	}
	if got := Expect(w("CREATE PLAYBOOK k SET")); !reflect.DeepEqual(got, []string{"launcher", "login", "global_instructions", "trusted_endpoint", "model", "agent", "sandbox.always", "sandbox.backend", "sandbox.host", "sandbox.workdir", "sandbox.mounts", "sandbox.allow_net", "sandbox.secrets", "sandbox.claude_version", "sandbox.share_skills", "statusline.command", "statusline.refresh", "model_picker.mode"}) {
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

func TestCheckTrustedEndpoint(t *testing.T) {
	for _, ok := range []string{"gateway.corp.example", "gw.example:8443", "localhost", "a-b.c1.example", "10.0.0.1"} {
		if why := checkTrustedEndpoint(ok); why != "" {
			t.Errorf("%q refused: %s", ok, why)
		}
	}
	for _, bad := range []string{"", "Gateway.example", "https://gw.example", "gw.example/v1", "user@gw.example",
		"gw.example:", "gw.example:123456", "gw.example:8a", "-gw.example", "gw-.example", "gw..example", ".gw", ":443", "gw example"} {
		if checkTrustedEndpoint(bad) == "" {
			t.Errorf("%q accepted", bad)
		}
	}
}
