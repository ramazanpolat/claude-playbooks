package grammar

import (
	"reflect"
	"strings"
	"testing"
)

func TestAccountStatements(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want Stmt
	}{
		{"create a gateway account, credential by reference",
			[]string{"CREATE", "ACCOUNT", "glm", "SET", "provider", "=", "gateway,", "base_url", "=", "http://localhost:4000/v1,", "auth_token", "FROM", "keychain:glm,", "model", "=", "glm-5.3"},
			Stmt{Verb: Create, Object: Account, Name: "glm", Clauses: []Clause{{Kind: SetAccount, Settings: []Var{
				{Key: "provider", Value: "gateway"}, {Key: "base_url", Value: "http://localhost:4000/v1"},
				{Key: "auth_token", Ref: "keychain:glm"}, {Key: "model", Value: "glm-5.3"}}}}}},
		{"create or replace, a login account",
			w("CREATE OR REPLACE ACCOUNT mine SET provider=anthropic, auth=claude_login"),
			Stmt{Verb: Create, Object: Account, Name: "mine", OrReplace: true, Clauses: []Clause{{Kind: SetAccount, Settings: []Var{
				{Key: "provider", Value: "anthropic"}, {Key: "auth", Value: "claude_login"}}}}}},
		{"a literal stored knowingly",
			w("CREATE ACCOUNT k SET provider=anthropic, api_key=sk-ant-x AS PLAINTEXT"),
			Stmt{Verb: Create, Object: Account, Name: "k", Clauses: []Clause{{Kind: SetAccount, Plaintext: true, Settings: []Var{
				{Key: "provider", Value: "anthropic"}, {Key: "api_key", Value: "sk-ant-x"}}}}}},
		{"alter: moving with the credential",
			w("ALTER ACCOUNT glm SET base_url=https://new.example/v1, auth_token FROM keychain:glm-new"),
			Stmt{Verb: Alter, Object: Account, Name: "glm", Clauses: []Clause{{Kind: SetAccount, Settings: []Var{
				{Key: "base_url", Value: "https://new.example/v1"}, {Key: "auth_token", Ref: "keychain:glm-new"}}}}}},
		{"alter: reset the address with the credential",
			w("ALTER ACCOUNT glm RESET base_url SET auth_token FROM keychain:glm"),
			Stmt{Verb: Alter, Object: Account, Name: "glm", Clauses: []Clause{{Kind: ResetAccount, Settings: []Var{{Key: "base_url"}}}, {Kind: SetAccount, Settings: []Var{{Key: "auth_token", Ref: "keychain:glm"}}}}}},
		{"alter: reset optional keys",
			w("ALTER ACCOUNT glm RESET identity, model"),
			Stmt{Verb: Alter, Object: Account, Name: "glm", Clauses: []Clause{{Kind: ResetAccount, Settings: []Var{{Key: "identity"}, {Key: "model"}}}}}},
		{"drop if exists",
			w("DROP ACCOUNT IF EXISTS glm"),
			Stmt{Verb: Drop, Object: Account, Name: "glm", IfExists: true}},
		{"show accounts",
			w("SHOW ACCOUNTS --json"),
			Stmt{Verb: Show, Object: Accounts, JSON: true}},
		{"show account",
			w("SHOW ACCOUNT glm"),
			Stmt{Verb: Show, Object: Account, Name: "glm"}},
		{"show create account",
			w("SHOW CREATE ACCOUNT glm"),
			Stmt{Verb: Show, Object: Account, Name: "glm", ShowCreate: true}},
		{"playbook account list",
			w("ALTER PLAYBOOK reviewer USE ACCOUNT glm, claude-work"),
			Stmt{Verb: Alter, Object: Playbook, Name: "reviewer", Clauses: []Clause{{Kind: UseAccount, Names: []string{"glm", "claude-work"}}}}},
		{"the implicit account may be listed",
			w("ALTER PLAYBOOK reviewer USE ACCOUNT machine glm"),
			Stmt{Verb: Alter, Object: Playbook, Name: "reviewer", Clauses: []Clause{{Kind: UseAccount, Names: []string{"machine", "glm"}}}}},
		{"empty account list",
			[]string{"ALTER", "PLAYBOOK", "reviewer", "USE", "ACCOUNT", ""},
			Stmt{Verb: Alter, Object: Playbook, Name: "reviewer", Clauses: []Clause{{Kind: UseAccount}}}},
		{"defaults account",
			w("ALTER DEFAULTS USE ACCOUNT mine"),
			Stmt{Verb: Alter, Object: Defaults, Clauses: []Clause{{Kind: UseAccount, Names: []string{"mine"}}}}},
		{"create playbook with accounts and envs",
			w("CREATE PLAYBOOK r USE ACCOUNT glm USE ENV proxy"),
			Stmt{Verb: Create, Object: Playbook, Name: "r", Clauses: []Clause{{Kind: UseAccount, Names: []string{"glm"}}, {Kind: UseEnv, Names: []string{"proxy"}}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseArgs(tc.args)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if !reflect.DeepEqual(strip(got), strip(&tc.want)) {
				t.Fatalf("\n got %+v\nwant %+v", *strip(got), tc.want)
			}
			// The canonical form parses back to the same statement, in a
			// file too (SHOW CREATE writes it).
			back, err := ParseLine(got.String())
			if err != nil {
				t.Fatalf("reparse %q: %v", got.String(), err)
			}
			if !reflect.DeepEqual(strip(back), strip(got)) {
				t.Fatalf("round trip of %q:\n got %+v\nwant %+v", got.String(), *strip(back), *strip(got))
			}
		})
	}
}

func TestAccountFileForm(t *testing.T) {
	src := `CREATE ACCOUNT glm SET provider = 'gateway', base_url = 'http://localhost:4000/v1',
  auth_token FROM 'keychain:glm', description = 'GLM, through the router';
ALTER PLAYBOOK reviewer USE ACCOUNT 'glm', claude-work;`
	stmts, err := ParseFile(src)
	if err != nil {
		t.Fatal(err)
	}
	set := stmts[0].Clauses[0].Settings
	if len(set) != 4 || set[2] != (Var{Key: "auth_token", Ref: "keychain:glm"}) || set[3].Value != "GLM, through the router" {
		t.Fatalf("settings: %+v", set)
	}
	if names := stmts[1].Clauses[0].Names; !reflect.DeepEqual(names, []string{"glm", "claude-work"}) {
		t.Fatalf("names: %v", names)
	}
}

func TestAccountErrors(t *testing.T) {
	cases := []struct {
		args string
		want string
	}{
		{"CREATE ACCOUNT glm SET base_url=http://localhost:4000 auth=none", "needs provider"},
		{"CREATE ACCOUNT glm SET provider=gateway base_url=http://localhost:4000", "needs its credential"},
		{"CREATE ACCOUNT glm SET provider=gateway auth_token FROM keychain:a, api_key FROM keychain:b", "one credential"},
		{"CREATE ACCOUNT glm SET provider=gateway auth=none, helper=x", "one credential"},
		{"CREATE ACCOUNT k SET provider=anthropic api_key=sk-ant-secret123", "holds a secret"},
		{"CREATE ACCOUNT k SET provider=anthropic auth=claude_login AS PLAINTEXT", "AS PLAINTEXT applies"},
		{"CREATE ACCOUNT k SET provider=anthropic api_key FROM sk-ant-secret123", "not a secret reference"},
		{"CREATE ACCOUNT machine SET provider=anthropic auth=claude_login", "implicit account"},
		{"CREATE ACCOUNT k SET provider=openai auth=none", "provider takes"},
		{"CREATE ACCOUNT k SET provider=gateway base_url=http://router.example auth=none", "unencrypted"},
		{"CREATE ACCOUNT k SET provider=gateway base_url=https://u:p@router.example auth=none", "user or password"},
		{"CREATE ACCOUNT k SET provider=anthropic auth=claude_login RESET identity", "RESET is for ALTER"},
		{"CREATE OR ALTER ACCOUNT k SET provider=anthropic auth=claude_login", "OR ALTER applies to PLAYBOOK only"},
		{"ALTER ACCOUNT glm SET base_url=https://new.example/v1", "state the credential"},
		{"ALTER ACCOUNT glm SET provider=anthropic", "state the credential"},
		{"ALTER ACCOUNT glm RESET base_url", "state the credential"},
		{"ALTER ACCOUNT glm RESET provider", "provider is the account itself"},
		{"ALTER ACCOUNT glm RESET auth_token", "one credential"},
		{"ALTER ACCOUNT glm SET ANTHROPIC_BASE_URL=x", "not an account key"},
		{"ALTER ACCOUNT glm SET Provider=anthropic auth=none", "lowercase"},
		{"ALTER ACCOUNT glm SET identity=a identity=b", "named twice"},
		{"ALTER PLAYBOOK r USE ACCOUNT glm USE ACCOUNT other", "appears twice"},
		{"ALTER PLAYBOOK r USE ACCOUNT glm glm", "account glm appears twice"},
		{"ALTER PLAYBOOK r USE glm", "USE takes ENV or ACCOUNT"},
		{"CREATE PLAYBOOK r LINK /tmp/x USE ACCOUNT glm", "does not apply to LINK"},
	}
	for _, tc := range cases {
		t.Run(tc.args, func(t *testing.T) {
			_, err := ParseArgs(w(tc.args))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
			for _, secret := range []string{"sk-ant-secret123", "u:p@"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error quotes a credential: %v", err)
				}
			}
		})
	}
}

func TestAccountIsKeyword(t *testing.T) {
	for _, k := range []string{"ACCOUNT", "accounts"} {
		if !IsKeyword(k) {
			t.Errorf("%s is not a keyword", k)
		}
	}
	if _, err := ParseArgs(w("CREATE ENV account SET A=1")); err == nil {
		t.Fatal("an unquoted keyword named a new env set")
	}
}
