package grammar

import (
	"reflect"
	"strings"
	"testing"
)

// Playbook settings, ClickHouse style: one word (k=v, k='v') or three
// (k = 'v'), comma-separated; quoted or not, as the shell hands them over.
func TestParsePlaybookSettings(t *testing.T) {
	want := []Var{{Key: "login", Value: "isolated"}, {Key: "memory", Value: "shared"}}
	for _, src := range []string{
		"CREATE PLAYBOOK x SETTINGS login = 'isolated', memory = 'shared';",
		"CREATE PLAYBOOK x SETTINGS login='isolated', memory='shared';",
		"CREATE PLAYBOOK x SETTINGS login=isolated memory=shared;",
		"CREATE PLAYBOOK x SETTINGS login = isolated , memory = shared;",
		"CREATE PLAYBOOK x SETTINGS login ='isolated', memory= 'Shared';",
	} {
		st, err := ParseFile(src)
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		c := st[0].Clauses[0]
		if c.Kind != PlaybookSettings || !reflect.DeepEqual(c.Settings, want) {
			t.Errorf("%s: %+v", src, c)
		}
	}
	// From argv: the shell has removed the quotes.
	st, err := ParseArgs(strings.Fields("ALTER PLAYBOOK x MODIFY SETTING memory = isolated, login = shared SET VAR A=1"))
	if err != nil {
		t.Fatal(err)
	}
	if c := st.Clauses[0]; c.Kind != ModifySetting || !reflect.DeepEqual(c.Settings, []Var{{Key: "memory", Value: "isolated"}, {Key: "login", Value: "shared"}}) {
		t.Errorf("MODIFY SETTING from argv: %+v", c)
	}
	if st.Clauses[1].Kind != SetVar {
		t.Errorf("the next clause: %+v", st.Clauses[1])
	}
	// A word quoted whole by the shell keeps its inner quotes.
	st, err = ParseArgs([]string{"ALTER", "PLAYBOOK", "x", "MODIFY", "SETTING", "memory='shared'"})
	if err != nil || st.Clauses[0].Settings[0].Value != "shared" {
		t.Fatalf("memory='shared' as one argv word: %v %+v", err, st)
	}
	st, err = ParseArgs(strings.Fields("ALTER PLAYBOOK x RESET SETTING memory, login"))
	if err != nil {
		t.Fatal(err)
	}
	if c := st.Clauses[0]; c.Kind != ResetSetting || len(c.Settings) != 2 || c.Settings[0].Key != "memory" || c.Settings[1].Key != "login" {
		t.Errorf("RESET SETTING: %+v", c)
	}
}

// SHOW CREATE writes settings quoted, and they parse back the same.
func TestFormatPlaybookSettings(t *testing.T) {
	for src, canon := range map[string]string{
		"CREATE PLAYBOOK x NO LAUNCHER SETTINGS login=isolated memory=shared": "CREATE PLAYBOOK x NO LAUNCHER SETTINGS login = 'isolated', memory = 'shared'",
		"ALTER PLAYBOOK x MODIFY SETTING memory = isolated":                   "ALTER PLAYBOOK x MODIFY SETTING memory = 'isolated'",
		"ALTER PLAYBOOK x RESET SETTING login, memory":                        "ALTER PLAYBOOK x RESET SETTING login, memory",
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
		if !reflect.DeepEqual(back.Clauses[0].Settings, st.Clauses[0].Settings) {
			t.Errorf("%s does not parse back: %+v", canon, back.Clauses[0])
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

func TestPlaybookSettingErrors(t *testing.T) {
	for src, want := range map[string]string{
		"CREATE PLAYBOOK x SETTINGS;":                                             "SETTINGS takes <key> = '<value>' (login, memory)",
		"CREATE PLAYBOOK x SETTINGS colour = 'blue';":                             "colour is not a playbook setting (login, memory)",
		"CREATE PLAYBOOK x SETTINGS memory = 'sealed';":                           "memory takes 'isolated' or 'shared'",
		"CREATE PLAYBOOK x SETTINGS memory;":                                      "SETTINGS takes <key> = '<value>'",
		"CREATE PLAYBOOK x SETTINGS memory = ;":                                   "needs a value",
		"ALTER PLAYBOOK x MODIFY SETTING memory = 'shared', memory = 'isolated';": "setting memory appears twice",
		"ALTER PLAYBOOK x MODIFY SETTING memory = 'shared' RESET SETTING memory;": "setting memory appears twice",
		"ALTER PLAYBOOK x MODIFY memory = 'shared';":                              "MODIFY takes SETTING",
		"ALTER PLAYBOOK x RESET SETTING colour;":                                  "colour is not a playbook setting",
		"CREATE PLAYBOOK x SANDBOX SETTINGS login = 'shared';":                    "SANDBOX isolates the login",
		"CREATE PLAYBOOK x LINK /tmp/d SETTINGS memory = 'shared';":               "SETTINGS does not apply to LINK",
		// The forms the settings replaced say what to write instead.
		"CREATE PLAYBOOK x ISOLATED LOGIN;":      "write SETTINGS login = 'isolated'",
		"ALTER PLAYBOOK x SET ISOLATED LOGIN;":   "MODIFY SETTING login = 'isolated' | 'shared'",
		"ALTER PLAYBOOK x UNSET ISOLATED LOGIN;": "ISOLATED LOGIN is gone",
	} {
		if _, err := ParseFile(src); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", src, err, want)
		}
	}
}

func TestExpectPlaybookSettings(t *testing.T) {
	w := strings.Fields
	for _, tc := range []struct {
		args []string
		want []string
	}{
		{w("ALTER PLAYBOOK k MODIFY"), []string{"SETTING"}},
		{w("ALTER PLAYBOOK k MODIFY SETTING"), []string{"login", "memory"}},
		{w("ALTER PLAYBOOK k RESET SETTING"), []string{"login", "memory"}},
	} {
		if got := Expect(tc.args); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Expect(%q)\n got %q\nwant %q", tc.args, got, tc.want)
		}
	}
	if got := Expect(w("CREATE PLAYBOOK k")); !contains(got, "SETTINGS") || contains(got, "ISOLATED") {
		t.Errorf("CREATE PLAYBOOK k offers %q", got)
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
