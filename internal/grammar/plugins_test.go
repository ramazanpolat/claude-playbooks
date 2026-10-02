package grammar

import (
	"reflect"
	"strings"
	"testing"
)

func TestParsePluginClauses(t *testing.T) {
	st, err := ParseArgs([]string{"ALTER", "PLAYBOOK", "k",
		"ADD", "MARKETPLACE", "toolkit", "FROM", "github:example/toolkit",
		"ADD", "PLUGIN", "toolkit@toolkit",
		"SET", "AGENT", "toolkit:toolkit",
		"ADD", "ENV", "toolkit", // an env set may share a marketplace's name
		"DROP", "PLUGIN", "old@toolkit",
		"DROP", "MARKETPLACE", "old"})
	if err != nil {
		t.Fatal(err)
	}
	want := []Clause{
		{Kind: AddMarketplace, Names: []string{"toolkit"}, Arg: "github:example/toolkit"},
		{Kind: AddPlugin, Names: []string{"toolkit@toolkit"}},
		{Kind: SetAgent, Arg: "toolkit:toolkit"},
		{Kind: AddEnv, Names: []string{"toolkit"}, Where: Last},
		{Kind: DropPlugin, Names: []string{"old@toolkit"}},
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
		{w("ALTER PLAYBOOK k ADD FOO"), "ADD takes ENV, MARKETPLACE, PLUGIN, MCP SERVER, SKILL or MODEL"},
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
	stmts, err := ParseFile(`CREATE ENV 'include'; CREATE PLAYBOOK IF NOT EXISTS 'agent' NO LAUNCHER;`)
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
		{w("ALTER PLAYBOOK k ADD"), []string{"ENV", "MARKETPLACE", "PLUGIN", "MCP", "SKILL", "MODEL"}},
		{w("ALTER PLAYBOOK k SET"), []string{"VAR", "AGENT", "STATUSLINE", "MODEL", "ISOLATED", "SANDBOX"}},
		{w("ALTER PLAYBOOK k ADD MARKETPLACE m"), []string{"FROM"}},
	}
	for _, tc := range cases {
		if got := Expect(tc.args); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Expect(%q)\n got %q\nwant %q", tc.args, got, tc.want)
		}
	}
}

// 'github:<owner>/<repo>' takes a branch or tag as #<ref> or @<ref>
// (v3.27.0); a ref that looks like a commit is refused, since Claude Code
// clones a marketplace by branch or tag only. No ref stays as it was.
func TestGitHubSourceRef(t *testing.T) {
	for src, want := range map[string][2]string{
		"github:a/b":             {"a/b", ""},
		"github:a/b#v1.2.0":      {"a/b", "v1.2.0"},
		"github:a/b@v1.2.0":      {"a/b", "v1.2.0"},
		"github:a/b#main":        {"a/b", "main"},
		"github:a/b@feature/x-y": {"a/b", "feature/x-y"},
		"github:a.b/c_d#release": {"a.b/c_d", "release"},
		"github:a/b#deadbe":      {"a/b", "deadbe"}, // six hex characters: a name, not a SHA
	} {
		repo, ref, err := GitHubSource(src)
		if err != nil || repo != want[0] || ref != want[1] {
			t.Errorf("%s: %q %q %v, want %q %q", src, repo, ref, err, want[0], want[1])
		}
		if kind, err := MarketplaceSource(src); err != nil || kind != SourceGitHub {
			t.Errorf("MarketplaceSource(%s): %q %v", src, kind, err)
		}
	}
	for src, want := range map[string]string{
		"github:a/b#0123abc": "a commit cannot be pinned",
		"github:a/b@0123456789abcdef0123456789abcdef01234567": "a commit cannot be pinned",
		"github:a/b#DEADBEEF": "a commit cannot be pinned",
		"github:a/b#":         "optionally with '#<branch or tag>'",
		"github:a/b@":         "optionally with '#<branch or tag>'",
		"github:a/b#-flag":    "optionally with '#<branch or tag>'",
		"github:a/b#x..y":     "optionally with '#<branch or tag>'",
		"github:a/b#x/":       "optionally with '#<branch or tag>'",
		"github:a/b#x y":      "optionally with '#<branch or tag>'",
		"github:a#v1":         "'github:<owner>/<repo>'",
		"github:a/b#v1#v2":    "optionally with '#<branch or tag>'",
	} {
		if _, err := MarketplaceSource(src); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", src, err, want)
		}
	}
	// Parsed as a clause, the ref is part of the source as written.
	st, err := ParseArgs([]string{"ALTER", "PLAYBOOK", "k", "ADD", "MARKETPLACE", "m", "FROM", "github:a/b#v1"})
	if err != nil || len(st.Clauses) != 1 || st.Clauses[0].Arg != "github:a/b#v1" {
		t.Fatalf("parse: %+v %v", st, err)
	}
	if _, err := ParseArgs([]string{"ALTER", "PLAYBOOK", "k", "ADD", "MARKETPLACE", "m", "FROM", "github:a/b#0123abc"}); err == nil || !strings.Contains(err.Error(), "a commit cannot be pinned") {
		t.Fatalf("a SHA must be refused at parse time: %v", err)
	}
}
