package grammar

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// strip zeroes positions so statements compare by content.
func strip(s *Stmt) *Stmt {
	out := *s
	out.Pos = Pos{}
	out.Clauses = nil
	for _, c := range s.Clauses {
		c.Pos = Pos{}
		out.Clauses = append(out.Clauses, c)
	}
	return &out
}

func w(s string) []string { return strings.Fields(s) }

var validCases = []struct {
	name string
	args []string
	want Stmt
}{
	{"create env with vars",
		w("CREATE ENV evren-router SET ANTHROPIC_BASE_URL=http://tr0:20128/v1 ANTHROPIC_MODEL=glm-5.3"),
		Stmt{Verb: Create, Object: Env, Name: "evren-router", Clauses: []Clause{
			{Kind: SetVar, Vars: []Var{{Key: "ANTHROPIC_BASE_URL", Value: "http://tr0:20128/v1"}, {Key: "ANTHROPIC_MODEL", Value: "glm-5.3"}}}}}},
	{"keywords in any case, secret by reference",
		w("alter Env evren-router set ANTHROPIC_AUTH_TOKEN from keychain:pilot/9router-client"),
		Stmt{Verb: Alter, Object: Env, Name: "evren-router", Clauses: []Clause{
			{Kind: SetRef, Vars: []Var{{Key: "ANTHROPIC_AUTH_TOKEN", Ref: "keychain:pilot/9router-client"}}}}}},
	{"env unset",
		w("ALTER ENV evren-router UNSET ANTHROPIC_MODEL"),
		Stmt{Verb: Alter, Object: Env, Name: "evren-router", Clauses: []Clause{{Kind: UnsetVar, Keys: []string{"ANTHROPIC_MODEL"}}}}},
	{"env block with optional VAR, describe",
		[]string{"ALTER", "ENV", "e", "BLOCK", "VAR", "HTTP_PROXY", "NO_PROXY", "DESCRIBE", "GLM via 9router"},
		Stmt{Verb: Alter, Object: Env, Name: "e", Clauses: []Clause{
			{Kind: BlockVar, Keys: []string{"HTTP_PROXY", "NO_PROXY"}},
			{Kind: Describe, Arg: "GLM via 9router"}}}},
	{"env set with optional VAR",
		w("ALTER ENV e SET VAR A=1"),
		Stmt{Verb: Alter, Object: Env, Name: "e", Clauses: []Clause{{Kind: SetVar, Vars: []Var{{Key: "A", Value: "1"}}}}}},
	{"a variable named VAR",
		w("ALTER ENV e SET VAR=1"),
		Stmt{Verb: Alter, Object: Env, Name: "e", Clauses: []Clause{{Kind: SetVar, Vars: []Var{{Key: "VAR", Value: "1"}}}}}},
	{"empty value and a value holding '='",
		w("ALTER ENV e SET A= B=x=y"),
		Stmt{Verb: Alter, Object: Env, Name: "e", Clauses: []Clause{{Kind: SetVar, Vars: []Var{{Key: "A", Value: ""}, {Key: "B", Value: "x=y"}}}}}},
	{"use env list",
		w("ALTER PLAYBOOK kommander-idea USE ENV glm-5.3 deepseek-flash"),
		Stmt{Verb: Alter, Object: Playbook, Name: "kommander-idea", Clauses: []Clause{{Kind: UseEnv, Names: []string{"glm-5.3", "deepseek-flash"}}}}},
	{"add env first",
		w("ALTER PLAYBOOK k ADD ENV claude-metu FIRST"),
		Stmt{Verb: Alter, Object: Playbook, Name: "k", Clauses: []Clause{{Kind: AddEnv, Names: []string{"claude-metu"}, Where: First}}}},
	{"add env after",
		w("ALTER PLAYBOOK k ADD ENV evren-router AFTER glm-5.3"),
		Stmt{Verb: Alter, Object: Playbook, Name: "k", Clauses: []Clause{{Kind: AddEnv, Names: []string{"evren-router"}, Where: After, Anchor: "glm-5.3"}}}},
	{"add env defaults to last",
		w("ALTER PLAYBOOK k ADD ENV x"),
		Stmt{Verb: Alter, Object: Playbook, Name: "k", Clauses: []Clause{{Kind: AddEnv, Names: []string{"x"}, Where: Last}}}},
	{"drop env",
		w("ALTER PLAYBOOK k DROP ENV deepseek-flash"),
		Stmt{Verb: Alter, Object: Playbook, Name: "k", Clauses: []Clause{{Kind: DropEnv, Names: []string{"deepseek-flash"}}}}},
	{"use pilot and block var",
		w("ALTER PLAYBOOK k USE PILOT ramazan-metu BLOCK VAR HTTP_PROXY"),
		Stmt{Verb: Alter, Object: Playbook, Name: "k", Clauses: []Clause{
			{Kind: UsePilot, Arg: "ramazan-metu"}, {Kind: BlockVar, Keys: []string{"HTTP_PROXY"}}}}},
	{"drop pilot, set var, unset var",
		w("ALTER PLAYBOOK k DROP PILOT SET VAR MAX_THINKING_TOKENS=8000 UNSET VAR FOO"),
		Stmt{Verb: Alter, Object: Playbook, Name: "k", Clauses: []Clause{
			{Kind: DropPilot}, {Kind: SetVar, Vars: []Var{{Key: "MAX_THINKING_TOKENS", Value: "8000"}}}, {Kind: UnsetVar, Keys: []string{"FOO"}}}}},
	{"playbook secret by reference",
		w("ALTER PLAYBOOK k SET VAR TOKEN FROM op://Vault/item/field"),
		Stmt{Verb: Alter, Object: Playbook, Name: "k", Clauses: []Clause{{Kind: SetRef, Vars: []Var{{Key: "TOKEN", Ref: "op://Vault/item/field"}}}}}},
	{"rename and alias",
		w("ALTER PLAYBOOK k RENAME TO kommander-lab ALIAS kl"),
		Stmt{Verb: Alter, Object: Playbook, Name: "k", Clauses: []Clause{{Kind: RenameTo, Arg: "kommander-lab"}, {Kind: Alias, Arg: "kl"}}}},
	{"dotted launcher",
		w("ALTER PLAYBOOK k ALIAS k.san"),
		Stmt{Verb: Alter, Object: Playbook, Name: "k", Clauses: []Clause{{Kind: Alias, Arg: "k.san"}}}},
	{"no alias",
		w("ALTER PLAYBOOK k NO ALIAS"),
		Stmt{Verb: Alter, Object: Playbook, Name: "k", Clauses: []Clause{{Kind: NoAlias}}}},
	{"existing playbook named like a keyword",
		w("ALTER PLAYBOOK sandbox NO ALIAS"),
		Stmt{Verb: Alter, Object: Playbook, Name: "sandbox", Clauses: []Clause{{Kind: NoAlias}}}},
	{"defaults use env",
		w("ALTER DEFAULTS USE ENV claude-default metu-proxy"),
		Stmt{Verb: Alter, Object: Defaults, Clauses: []Clause{{Kind: UseEnv, Names: []string{"claude-default", "metu-proxy"}}}}},
	{"defaults add before, drop",
		w("ALTER DEFAULTS ADD ENV x BEFORE y DROP ENV z"),
		Stmt{Verb: Alter, Object: Defaults, Clauses: []Clause{
			{Kind: AddEnv, Names: []string{"x"}, Where: Before, Anchor: "y"}, {Kind: DropEnv, Names: []string{"z"}}}}},
	{"create playbook from source with alias",
		w("CREATE PLAYBOOK kommander-x FROM https://github.com/ramazanpolat/kommander-playbook ALIAS kx"),
		Stmt{Verb: Create, Object: Playbook, Name: "kommander-x", Clauses: []Clause{
			{Kind: From, Arg: "https://github.com/ramazanpolat/kommander-playbook"}, {Kind: Alias, Arg: "kx"}}}},
	{"create playbook if not exists with every option",
		w("CREATE PLAYBOOK IF NOT EXISTS second FROM repo BRANCH v0.5.0 SUBDIR dist NO ALIAS SANDBOX"),
		Stmt{Verb: Create, Object: Playbook, Name: "second", IfNotExists: true, Clauses: []Clause{
			{Kind: From, Arg: "repo"}, {Kind: Branch, Arg: "v0.5.0"}, {Kind: Subdir, Arg: "dist"}, {Kind: NoAlias}, {Kind: Sandbox}}}},
	{"create playbook link",
		w("CREATE PLAYBOOK my-dev LINK ~/DEV/my-playbook"),
		Stmt{Verb: Create, Object: Playbook, Name: "my-dev", Clauses: []Clause{{Kind: Link, Arg: "~/DEV/my-playbook"}}}},
	{"create empty playbook",
		w("CREATE PLAYBOOK empty"),
		Stmt{Verb: Create, Object: Playbook, Name: "empty"}},
	{"create or replace env",
		w("CREATE OR REPLACE ENV e SET A=1"),
		Stmt{Verb: Create, Object: Env, Name: "e", OrReplace: true, Clauses: []Clause{{Kind: SetVar, Vars: []Var{{Key: "A", Value: "1"}}}}}},
	{"create env if not exists, no clauses",
		w("CREATE ENV IF NOT EXISTS e"),
		Stmt{Verb: Create, Object: Env, Name: "e", IfNotExists: true}},
	{"drop playbook if exists", w("DROP PLAYBOOK IF EXISTS kommander-lab"),
		Stmt{Verb: Drop, Object: Playbook, Name: "kommander-lab", IfExists: true}},
	{"drop env", w("drop env e"), Stmt{Verb: Drop, Object: Env, Name: "e"}},
	{"show envs", w("SHOW ENVS"), Stmt{Verb: Show, Object: Envs}},
	{"show playbooks", w("show playbooks"), Stmt{Verb: Show, Object: Playbooks}},
	{"show pilots", w("SHOW PILOTS"), Stmt{Verb: Show, Object: Pilots}},
	{"show defaults", w("SHOW DEFAULTS"), Stmt{Verb: Show, Object: Defaults}},
	{"show playbook", w("SHOW PLAYBOOK k"), Stmt{Verb: Show, Object: Playbook, Name: "k"}},
	{"show env", w("SHOW ENV e"), Stmt{Verb: Show, Object: Env, Name: "e"}},
	{"show create all", w("SHOW CREATE ALL"), Stmt{Verb: Show, Object: All, ShowCreate: true}},
	{"show create playbook", w("SHOW CREATE PLAYBOOK k"), Stmt{Verb: Show, Object: Playbook, Name: "k", ShowCreate: true}},
	{"show create env", w("SHOW CREATE ENV e"), Stmt{Verb: Show, Object: Env, Name: "e", ShowCreate: true}},
	{"show create all, skipping secrets", w("SHOW CREATE ALL --skip-secrets"),
		Stmt{Verb: Show, Object: All, ShowCreate: true, SkipSecrets: true}},
	{"show create playbook, skipping secrets", w("show create playbook k --skip-secrets"),
		Stmt{Verb: Show, Object: Playbook, Name: "k", ShowCreate: true, SkipSecrets: true}},
	{"explain", w("EXPLAIN PLAYBOOK kommander-idea"), Stmt{Verb: Explain, Object: Playbook, Name: "kommander-idea"}},
	{"apply dry run", w("APPLY setup.cpb --dry-run"), Stmt{Verb: Apply, File: "setup.cpb", DryRun: true}},
	{"apply", w("apply setup.cpb"), Stmt{Verb: Apply, File: "setup.cpb"}},
	{"trailing commas separate K=V",
		w("ALTER ENV e SET A=1, B=2"),
		Stmt{Verb: Alter, Object: Env, Name: "e", Clauses: []Clause{{Kind: SetVar, Vars: []Var{{Key: "A", Value: "1"}, {Key: "B", Value: "2"}}}}}},
	{"a last value keeps its comma",
		w("ALTER ENV e SET A=x,"),
		Stmt{Verb: Alter, Object: Env, Name: "e", Clauses: []Clause{{Kind: SetVar, Vars: []Var{{Key: "A", Value: "x,"}}}}}},
	{"commas in name lists, lone comma",
		w("ALTER PLAYBOOK k USE ENV a, b , c"),
		Stmt{Verb: Alter, Object: Playbook, Name: "k", Clauses: []Clause{{Kind: UseEnv, Names: []string{"a", "b", "c"}}}}},
}

func TestParseArgsValid(t *testing.T) {
	for _, tc := range validCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseArgs(tc.args)
			if err != nil {
				t.Fatalf("ParseArgs(%q): %v", tc.args, err)
			}
			if !reflect.DeepEqual(strip(got), &tc.want) {
				t.Errorf("ParseArgs(%q)\n got %+v\nwant %+v", tc.args, *strip(got), tc.want)
			}
		})
	}
}

func TestParseArgsInvalid(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{nil, "empty statement"},
		{w("FOO"), "not a statement"},
		{w("CREATE"), "CREATE needs an object"},
		{w("CREATE PLAYBOOK"), "missing <playbook>"},
		{w("CREATE PLAYBOOK sandbox"), `"sandbox" is a keyword and cannot name a playbook`},
		{w("CREATE ENV use"), `"use" is a keyword and cannot name an env set`},
		{w("CREATE OR REPLACE PLAYBOOK x"), "OR REPLACE applies to ENV only"},
		{w("CREATE OR REPLACE ENV IF NOT EXISTS e"), "cannot be combined"},
		{w("CREATE OR ENV e"), "expected REPLACE after OR"},
		{w("CREATE ENV IF e"), "expected NOT EXISTS after IF"},
		{w("CREATE ENV bad/name"), "invalid env set name"},
		{w("CREATE ENV e UNSET A"), "nothing to forget"},
		{w("ALTER"), "ALTER needs an object"},
		{w("ALTER PLAYBOOK k"), "ALTER PLAYBOOK needs at least one clause"},
		{w("ALTER DEFAULTS"), "ALTER DEFAULTS needs at least one clause"},
		{w("ALTER PLAYBOOK k SET A=1"), "SET inside ALTER PLAYBOOK takes VAR"},
		{w("ALTER PLAYBOOK k BLOCK A"), "BLOCK inside ALTER PLAYBOOK takes VAR"},
		{w("ALTER PLAYBOOK k UNSET A"), "UNSET inside ALTER PLAYBOOK takes VAR"},
		{w("ALTER PLAYBOOK k FOO"), `unexpected "FOO"`},
		{w("ALTER PLAYBOOK k USE"), "USE needs ENV or PILOT"},
		{w("ALTER PLAYBOOK k USE ENV"), "USE ENV needs at least one <env>"},
		{w("ALTER PLAYBOOK k USE ENV a USE ENV b"), "USE ENV appears twice"},
		{w("ALTER PLAYBOOK k USE PILOT a USE PILOT b"), "USE PILOT appears twice"},
		{w("ALTER PLAYBOOK k USE PILOT a DROP PILOT"), "USE PILOT and DROP PILOT cannot be combined"},
		{w("ALTER PLAYBOOK k ALIAS a NO ALIAS"), "ALIAS and NO ALIAS cannot be combined"},
		{w("ALTER PLAYBOOK k SET VAR A=1 BLOCK VAR A"), "A appears twice"},
		{w("ALTER PLAYBOOK k SET VAR A=1 A=2"), "A appears twice"},
		{w("ALTER PLAYBOOK k USE ENV a DROP ENV a"), "env set a appears twice"},
		{w("ALTER PLAYBOOK k ADD ENV a AFTER a"), "relative to itself"},
		{w("ALTER PLAYBOOK k ADD a"), "ADD takes ENV"},
		{w("ALTER PLAYBOOK k ADD ENV a BEFORE"), "missing <env>"},
		{w("ALTER PLAYBOOK k RENAME kx"), "expected TO after RENAME"},
		{w("ALTER PLAYBOOK k RENAME TO use"), "is a keyword"},
		{w("ALTER PLAYBOOK k ALIAS"), "ALIAS needs <launcher>"},
		{w("ALTER PLAYBOOK k ALIAS set"), `"set" is a keyword and cannot name a launcher`},
		{w("ALTER PLAYBOOK k NO"), "expected ALIAS after NO"},
		{w("ALTER DEFAULTS SET VAR A=1"), `unexpected "SET"`},
		{w("ALTER DEFAULTS USE PILOT x"), "USE inside ALTER DEFAULTS takes ENV"},
		{w("ALTER ENV e SET"), "SET needs <key>=<value> or <key> FROM '<ref>'"},
		{w("ALTER ENV e SET 1BAD=x"), "invalid variable name before '='"},
		{w("ALTER ENV e SET CLAUDE_CONFIG_DIR=x"), "managed by claude-playbook"},
		{w("ALTER ENV e SET A"), "expected FROM after the key"},
		{w("ALTER ENV e SET A FROM"), "FROM needs '<ref>'"},
		{w("ALTER ENV e SET A FROM plainword"), "not a secret reference"},
		{w("ALTER ENV e BLOCK"), "BLOCK needs at least one <key>"},
		{w("ALTER ENV e BLOCK 9x"), "BLOCK takes variable names"},
		{w("ALTER ENV e UNSET CLAUDE_CONFIG_DIR"), "managed by claude-playbook"},
		{w("ALTER ENV e DESCRIBE"), "DESCRIBE needs '<text>'"},
		{w("ALTER ENV e DESCRIBE a DESCRIBE b"), "DESCRIBE appears twice"},
		{w("ALTER ENV e USE ENV x"), `unexpected "USE"`},
		{w("DROP"), "DROP needs an object"},
		{w("DROP ENV"), "missing <env>"},
		{w("DROP PLAYBOOK IF k"), "expected EXISTS after IF"},
		{w("DROP PLAYBOOK k extra"), `unexpected "extra"`},
		{w("SHOW"), "SHOW needs an object"},
		{w("SHOW CREATE"), "SHOW CREATE needs an object"},
		{w("SHOW CREATE PILOTS"), "SHOW CREATE needs an object"},
		{w("SHOW ENVS extra"), `unexpected "extra"`},
		{w("SHOW ENVS --skip-secrets"), "unexpected word"},
		{w("SHOW CREATE ALL --dry-run"), "unexpected word"},
		{w("EXPLAIN ENV e"), "EXPLAIN needs PLAYBOOK"},
		{w("APPLY"), "APPLY needs <file>"},
		{w("APPLY f --force"), "unexpected word"},
		{w("CREATE PLAYBOOK x BRANCH main"), "BRANCH needs FROM <source>"},
		{w("CREATE PLAYBOOK x SUBDIR d"), "SUBDIR needs FROM <source>"},
		{w("CREATE PLAYBOOK x FROM a LINK b"), "FROM and LINK cannot be combined"},
		{w("CREATE PLAYBOOK x FROM"), "FROM needs <source>"},
		{w("CREATE PLAYBOOK x FROM a FROM b"), "FROM appears twice"},
		{w("CREATE PLAYBOOK x USE ENV a"), `unexpected "USE"`},
	}
	for _, tc := range cases {
		_, err := ParseArgs(tc.args)
		if err == nil {
			t.Errorf("ParseArgs(%q): no error, want %q", tc.args, tc.want)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("ParseArgs(%q): error %q, want it to contain %q", tc.args, err, tc.want)
		}
	}
}

// Errors must never repeat a value, a reference, or anything that may be
// one: a value with an unquoted space, a secret typed where FROM belongs, a
// token pasted as a reference, a key-shaped token.
func TestErrorsNeverEchoSecrets(t *testing.T) {
	const secret = "s3cr3t-Tok3n-9f8e"
	cases := [][]string{
		{"ALTER", "PLAYBOOK", "k", "SET", "VAR", "TOKEN=abc", secret},
		{"ALTER", "ENV", "e", "SET", "TOKEN", secret},
		{"ALTER", "ENV", "e", "SET", "TOKEN", "FROM", secret},
		{"ALTER", "ENV", "e", "SET", "A=1", secret},
		{"ALTER", "ENV", "e", "DESCRIBE", "x", secret},
		{"ALTER", "ENV", "e", "SET", "A", "FROM", "keychain:x", secret},
		{"ALTER", "ENV", "e", "BLOCK", secret},
		{"ALTER", "PLAYBOOK", "k", secret},
		{"ALTER", "ENV", "e", "SET", "ghp_" + "abcDEF123456", "x"},
	}
	for _, args := range cases {
		_, err := ParseArgs(args)
		if err == nil {
			t.Errorf("ParseArgs(%q): no error", args)
			continue
		}
		for _, bad := range []string{secret, "abcDEF123456"} {
			if strings.Contains(err.Error(), bad) {
				t.Errorf("ParseArgs(%q): error %q echoes a possible secret", args, err)
			}
		}
	}
}

func TestErrorPosition(t *testing.T) {
	_, err := ParseArgs(w("ALTER PLAYBOOK k FOO"))
	var pe *Error
	if !errors.As(err, &pe) {
		t.Fatalf("want *Error, got %T", err)
	}
	if pe.Pos.Word != 4 || pe.AtEnd {
		t.Errorf("pos %+v AtEnd %v, want word 4, not at end", pe.Pos, pe.AtEnd)
	}
	if !strings.HasPrefix(err.Error(), "word 4: ") {
		t.Errorf("error %q should start with its position", err)
	}
	_, err = ParseArgs(w("ALTER PLAYBOOK"))
	if !errors.As(err, &pe) || !pe.AtEnd || pe.Pos.Word != 3 {
		t.Errorf("a statement that runs out should report AtEnd at word 3, got %+v", pe)
	}
}

func TestParseFile(t *testing.T) {
	src := `-- setup.cpb (macminim), from: cpb SHOW CREATE ALL > setup.cpb

CREATE OR REPLACE ENV evren-router
  DESCRIBE 'GLM via 9router on tr0'
  SET ANTHROPIC_BASE_URL=http://tr0:20128/v1 ANTHROPIC_MODEL=glm-5.3
  SET ANTHROPIC_AUTH_TOKEN FROM 'keychain:pilot/9router-client';

CREATE OR REPLACE ENV claude-default
  BLOCK HTTP_PROXY;

ALTER DEFAULTS USE ENV claude-default;

CREATE PLAYBOOK IF NOT EXISTS kommander-idea
  FROM https://github.com/ramazanpolat/kommander-playbook BRANCH v3.12.2 ALIAS ki;

ALTER PLAYBOOK kommander-idea
  USE ENV evren-router
  USE PILOT ramazan-metu
  SET VAR MAX_THINKING_TOKENS=8000
  BLOCK VAR CLAUDE_CODE_OAUTH_TOKEN;
`
	stmts, err := ParseFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(stmts) != 5 {
		t.Fatalf("got %d statements, want 5", len(stmts))
	}
	want := []string{
		"CREATE OR REPLACE ENV evren-router DESCRIBE 'GLM via 9router on tr0' SET ANTHROPIC_BASE_URL=http://tr0:20128/v1 ANTHROPIC_MODEL=glm-5.3 SET ANTHROPIC_AUTH_TOKEN FROM 'keychain:pilot/9router-client'",
		"CREATE OR REPLACE ENV claude-default BLOCK HTTP_PROXY",
		"ALTER DEFAULTS USE ENV claude-default",
		"CREATE PLAYBOOK IF NOT EXISTS kommander-idea FROM https://github.com/ramazanpolat/kommander-playbook BRANCH v3.12.2 ALIAS ki",
		"ALTER PLAYBOOK kommander-idea USE ENV evren-router USE PILOT ramazan-metu SET VAR MAX_THINKING_TOKENS=8000 BLOCK VAR CLAUDE_CODE_OAUTH_TOKEN",
	}
	for i, s := range stmts {
		if got := s.String(); got != want[i] {
			t.Errorf("statement %d:\n got %s\nwant %s", i+1, got, want[i])
		}
	}
	if p := stmts[3].Pos; p.Line != 13 || p.Col != 1 {
		t.Errorf("statement 4 at %v, want line 13, col 1", p)
	}
}

func TestParseFileQuotedKeywords(t *testing.T) {
	stmts, err := ParseFile(`ALTER ENV e BLOCK 'SET' A; ALTER PLAYBOOK 'use' NO ALIAS`)
	if err != nil {
		t.Fatal(err)
	}
	if got := stmts[0].Clauses[0].Keys; !reflect.DeepEqual(got, []string{"SET", "A"}) {
		t.Errorf("quoted keyword key: got %q", got)
	}
	if stmts[1].Name != "use" {
		t.Errorf("name %q, want use", stmts[1].Name)
	}
}

func TestParseFileRefusesReadsAndApply(t *testing.T) {
	for src, want := range map[string]string{
		"SHOW ENVS;":            "SHOW only reads",
		"EXPLAIN PLAYBOOK k;":   "EXPLAIN only reads",
		"APPLY other.cpb;":      "APPLY cannot appear inside a setup file",
		"ALTER PLAYBOOK k FOO;": `unexpected "FOO"`,
	} {
		_, err := ParseFile(src)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseFile(%q): error %v, want %q", src, err, want)
		}
	}
}

func TestParseFileReportsEveryError(t *testing.T) {
	_, err := ParseFile("FOO;\nALTER ENV e SET A=1;\nBAR;")
	if err == nil {
		t.Fatal("no error")
	}
	for _, want := range []string{"line 1, col 1", "line 3, col 1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}

func TestExpect(t *testing.T) {
	cases := []struct {
		args []string
		want []string
	}{
		{nil, []string{"CREATE", "ALTER", "DROP", "SHOW", "EXPLAIN", "APPLY"}},
		{w("CREATE"), []string{"OR", "PLAYBOOK", "ENV"}},
		{w("CREATE PLAYBOOK"), []string{"IF", "<playbook>"}},
		{w("ALTER"), []string{"PLAYBOOK", "ENV", "DEFAULTS"}},
		{w("ALTER PLAYBOOK"), []string{"<playbook>"}},
		{w("ALTER PLAYBOOK k"), alterPlaybookStarters},
		{w("ALTER PLAYBOOK k USE"), []string{"ENV", "PILOT"}},
		{w("ALTER PLAYBOOK k USE ENV a"), append([]string{"<env>"}, alterPlaybookStarters...)},
		{w("ALTER PLAYBOOK k ADD ENV a"), append([]string{"FIRST", "LAST", "BEFORE", "AFTER"}, alterPlaybookStarters...)},
		{w("ALTER ENV e SET"), []string{"VAR", "<key>=<value>", "<key>"}},
		{w("ALTER ENV e SET A=1"), append([]string{"<key>=<value>"}, envStarters...)},
		{w("ALTER DEFAULTS"), defaultsStarters},
		{w("SHOW"), []string{"CREATE", "PLAYBOOKS", "ENVS", "PILOTS", "DEFAULTS", "PLAYBOOK", "ENV"}},
		{w("APPLY f"), []string{"--dry-run"}},
		{w("SHOW CREATE ALL"), []string{"--skip-secrets"}},
		{w("SHOW CREATE ENV e"), []string{"--skip-secrets"}},
		{w("DROP PLAYBOOK k"), nil},
		{w("ALTER FOO"), nil},
	}
	for _, tc := range cases {
		if got := Expect(tc.args); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Expect(%q)\n got %q\nwant %q", tc.args, got, tc.want)
		}
	}
}

func TestIsStatement(t *testing.T) {
	for args, want := range map[string]bool{
		"":                          false,
		"create kommander-x":        false, // the short form
		"create":                    false,
		"create playbook x":         true,
		"CREATE ENV e":              true,
		"create or replace env e":   true,
		"alter playbook k NO ALIAS": true,
		"drop playbook k":           true,
		"show envs":                 true,
		"explain playbook k":        true,
		"apply setup.cpb":           true,
		"env k set A=1":             false,
		"install https://x":         false,
		"list":                      false,
	} {
		if got := IsStatement(w(args)); got != want {
			t.Errorf("IsStatement(%q) = %v, want %v", args, got, want)
		}
	}
}

func TestKeywordsAreReserved(t *testing.T) {
	for _, k := range []string{"playbook", "ENV", "Pilot", "sandbox", "all", "--dry-run"} {
		if got := IsKeyword(k); got != (k != "--dry-run") {
			t.Errorf("IsKeyword(%q) = %v", k, got)
		}
	}
}
