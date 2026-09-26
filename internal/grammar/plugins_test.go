package grammar

import (
	"reflect"
	"strings"
	"testing"
)

func TestParsePluginClauses(t *testing.T) {
	st, err := ParseArgs([]string{"ALTER", "PLAYBOOK", "k",
		"ADD", "MARKETPLACE", "kommander", "FROM", "github:ramazanpolat/kommander-playbook",
		"ADD", "PLUGIN", "kommander@kommander",
		"SET", "AGENT", "kommander:kommander",
		"ADD", "ENV", "kommander", // an env set may share a marketplace's name
		"DROP", "PLUGIN", "old@kommander",
		"DROP", "MARKETPLACE", "old"})
	if err != nil {
		t.Fatal(err)
	}
	want := []Clause{
		{Kind: AddMarketplace, Names: []string{"kommander"}, Arg: "github:ramazanpolat/kommander-playbook"},
		{Kind: AddPlugin, Names: []string{"kommander@kommander"}},
		{Kind: SetAgent, Arg: "kommander:kommander"},
		{Kind: AddEnv, Names: []string{"kommander"}, Where: Last},
		{Kind: DropPlugin, Names: []string{"old@kommander"}},
		{Kind: DropMarketplace, Names: []string{"old"}},
	}
	if got := strip(st).Clauses; !reflect.DeepEqual(got, want) {
		t.Errorf("clauses\n got %+v\nwant %+v", got, want)
	}
	// The canonical form parses back to the same statement.
	again, err := ParseFile(st.String())
	if err != nil {
		t.Fatalf("%s: %v", st.String(), err)
	}
	if !reflect.DeepEqual(strip(again[0]), strip(st)) {
		t.Errorf("round trip changed the statement: %s", st.String())
	}
	if _, err := ParseArgs(w("ALTER PLAYBOOK k UNSET AGENT")); err != nil {
		t.Errorf("UNSET AGENT: %v", err)
	}
}

func TestParsePluginErrors(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{w("ALTER PLAYBOOK k ADD MARKETPLACE m"), "ADD MARKETPLACE needs FROM '<source>'"},
		{w("ALTER PLAYBOOK k ADD MARKETPLACE m FROM rel/dir"), "unsupported marketplace source"},
		{w("ALTER PLAYBOOK k ADD MARKETPLACE m FROM github:only-owner"), "'github:<owner>/<repo>'"},
		{w("ALTER PLAYBOOK k ADD MARKETPLACE m FROM https://user:tok@example.com/r.git"), "carrying credentials"},
		{w("ALTER PLAYBOOK k ADD PLUGIN noat"), "<plugin>@<marketplace>"},
		{w("ALTER PLAYBOOK k ADD PLUGIN a@b ADD PLUGIN a@b"), "plugin a@b appears twice"},
		{w("ALTER PLAYBOOK k SET AGENT a UNSET AGENT"), "cannot be combined"},
		{w("ALTER PLAYBOOK k SET AGENT a/b"), "an agent is <name> or <plugin>:<name>"},
		{w("ALTER PLAYBOOK k ADD FOO"), "ADD takes ENV, MARKETPLACE or PLUGIN"},
		{w("ALTER DEFAULTS ADD PLUGIN a@b"), "ADD takes ENV"},
		{w("INCLUDE base.cpb"), "INCLUDE appears only in a playbook file"},
		{w("ALTER PLAYBOOK k ADD MARKETPLACE m FROM ./mkt"), "resolves against its playbook file"},
	}
	for _, tc := range cases {
		_, err := ParseArgs(tc.args)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: error %v, want %q", tc.args, err, tc.want)
		}
	}
	// A source is never echoed: it may carry a token.
	_, err := ParseArgs(w("ALTER PLAYBOOK k ADD MARKETPLACE m FROM https://user:ghp_secret@example.com/r.git"))
	if err == nil || strings.Contains(err.Error(), "ghp_secret") {
		t.Errorf("error echoes the source: %v", err)
	}
}

func TestParseInclude(t *testing.T) {
	stmts, err := ParseFile("INCLUDE 'base.cpb';\nALTER PLAYBOOK k USE ENV a;")
	if err != nil {
		t.Fatal(err)
	}
	if stmts[0].Verb != Include || !reflect.DeepEqual(stmts[0].Files, []string{"base.cpb"}) {
		t.Errorf("got %+v", stmts[0])
	}
	if got := stmts[0].String(); got != "INCLUDE 'base.cpb'" {
		t.Errorf("String() = %s", got)
	}
	if _, err := ParseFile("INCLUDE;"); err == nil {
		t.Error("INCLUDE without a path parsed")
	}
}

func TestQuotedKeywordIsANewName(t *testing.T) {
	stmts, err := ParseFile(`CREATE ENV 'include'; CREATE PLAYBOOK IF NOT EXISTS 'agent' NO ALIAS;`)
	if err != nil {
		t.Fatal(err)
	}
	if stmts[0].Name != "include" || stmts[1].Name != "agent" {
		t.Errorf("names %q %q", stmts[0].Name, stmts[1].Name)
	}
	// Unquoted, it stays a keyword; and SHOW CREATE's form quotes it.
	if _, err := ParseFile(`CREATE ENV include;`); err == nil {
		t.Error("an unquoted keyword named a new env set")
	}
	if got := stmts[0].String(); got != "CREATE ENV 'include'" {
		t.Errorf("String() = %s", got)
	}
}

func TestExpectPlugins(t *testing.T) {
	cases := []struct {
		args []string
		want []string
	}{
		{w("ALTER PLAYBOOK k ADD"), []string{"ENV", "MARKETPLACE", "PLUGIN"}},
		{w("ALTER PLAYBOOK k SET"), []string{"VAR", "AGENT"}},
		{w("ALTER PLAYBOOK k ADD MARKETPLACE m"), []string{"FROM"}},
	}
	for _, tc := range cases {
		if got := Expect(tc.args); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Expect(%q)\n got %q\nwant %q", tc.args, got, tc.want)
		}
	}
}
