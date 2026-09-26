package grammar

import (
	"reflect"
	"testing"
)

// parseText parses one statement from playbook-file text without the
// write-only rule, so reads and APPLY round-trip too.
func parseText(t *testing.T, text string) *Stmt {
	t.Helper()
	groups, err := lexFile(text)
	if err != nil {
		t.Fatalf("lex %q: %v", text, err)
	}
	if len(groups) != 1 {
		t.Fatalf("%q lexes to %d statements", text, len(groups))
	}
	s, perr := newParser(groups[0], false).statement()
	if perr != nil {
		t.Fatalf("parse %q: %v", text, perr)
	}
	return s
}

// Every valid statement renders to text that parses back to itself.
func TestStringRoundTrip(t *testing.T) {
	for _, tc := range validCases {
		s, err := ParseArgs(tc.args)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		text := s.String()
		if back := parseText(t, text); !reflect.DeepEqual(strip(back), strip(s)) {
			t.Errorf("%s: %q did not round-trip\n got %+v\nwant %+v", tc.name, text, *strip(back), *strip(s))
		}
	}
}

func TestStringQuoting(t *testing.T) {
	cases := []struct {
		stmt Stmt
		want string
	}{
		{Stmt{Verb: Alter, Object: Env, Name: "e", Clauses: []Clause{{Kind: SetVar, Vars: []Var{
			{Key: "A", Value: "hello world"}, {Key: "B", Value: "it's"}, {Key: "C", Value: "x,"}, {Key: "D", Value: ""}, {Key: "E", Value: "SET"}}}}},
			"ALTER ENV e SET A='hello world' B='it''s' C='x,' D= E=SET"},
		{Stmt{Verb: Alter, Object: Playbook, Name: "k", Clauses: []Clause{{Kind: SetVar, Vars: []Var{{Key: "API_KEY", Value: "x y"}}, Plaintext: true}}},
			"ALTER PLAYBOOK k SET VAR API_KEY='x y' AS PLAINTEXT"},
		{Stmt{Verb: Alter, Object: Env, Name: "e", Clauses: []Clause{{Kind: BlockVar, Keys: []string{"SET", "A"}}}},
			"ALTER ENV e BLOCK 'SET' A"},
		{Stmt{Verb: Alter, Object: Playbook, Name: "sandbox", Clauses: []Clause{{Kind: SetVar, Vars: []Var{{Key: "A", Value: "a;b"}}}}},
			"ALTER PLAYBOOK 'sandbox' SET VAR A='a;b'"},
		{Stmt{Verb: Alter, Object: Env, Name: "e", Clauses: []Clause{{Kind: Describe, Arg: "plain"}}},
			"ALTER ENV e DESCRIBE 'plain'"},
		{Stmt{Verb: Alter, Object: Defaults, Clauses: []Clause{{Kind: SetHelper, Arg: "/opt/bin/helper"}}},
			"ALTER DEFAULTS SET SECRET HELPER '/opt/bin/helper'"},
		{Stmt{Verb: Apply, Files: []string{"my playbook.cpb"}, DryRun: true},
			"APPLY 'my playbook.cpb' --dry-run"},
		{Stmt{Verb: Create, Object: Playbook, Name: "x", Clauses: []Clause{{Kind: Link, Arg: "--odd"}}},
			"CREATE PLAYBOOK x LINK '--odd'"},
	}
	for _, tc := range cases {
		if got := tc.stmt.String(); got != tc.want {
			t.Errorf("\n got %s\nwant %s", got, tc.want)
		}
		back := parseText(t, tc.want)
		if !reflect.DeepEqual(strip(back), strip(&tc.stmt)) {
			t.Errorf("%q did not round-trip\n got %+v\nwant %+v", tc.want, *strip(back), tc.stmt)
		}
	}
}

// Pretty, the playbook-file layout, parses back to the same statement too.
func TestPrettyRoundTrip(t *testing.T) {
	for _, tc := range validCases {
		s, err := ParseArgs(tc.args)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		text := s.Pretty()
		if back := parseText(t, text); !reflect.DeepEqual(strip(back), strip(s)) {
			t.Errorf("%s: %q did not round-trip", tc.name, text)
		}
	}
}
