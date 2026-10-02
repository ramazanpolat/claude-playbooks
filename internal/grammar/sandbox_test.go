package grammar

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseSandboxClauses(t *testing.T) {
	stmts, err := ParseFile(`ALTER PLAYBOOK k
  SET SANDBOX
  SET SANDBOX backend=openshell host=me@buildbox mounts=~/libs:ro,~/data allow_net=api.example.com:443 secrets=env claude_version=2.1.0 share_skills=true workdir='~/my proj';
ALTER PLAYBOOK k UNSET SANDBOX UNSET SANDBOX host mounts;`)
	if err != nil {
		t.Fatal(err)
	}
	c := stmts[0].Clauses
	want := []Var{{Key: "backend", Value: "openshell"}, {Key: "host", Value: "me@buildbox"}, {Key: "mounts", Value: "~/libs:ro,~/data"},
		{Key: "allow_net", Value: "api.example.com:443"}, {Key: "secrets", Value: "env"}, {Key: "claude_version", Value: "2.1.0"},
		{Key: "share_skills", Value: "true"}, {Key: "workdir", Value: "~/my proj"}}
	if c[0].Kind != SetSandbox || c[1].Kind != SetSandboxKeys || !reflect.DeepEqual(c[1].Settings, want) {
		t.Fatalf("SET: %+v", c)
	}
	u := stmts[1].Clauses
	if u[0].Kind != UnsetSandbox || u[1].Kind != UnsetSandboxKeys || !reflect.DeepEqual(u[1].Settings, []Var{{Key: "host"}, {Key: "mounts"}}) {
		t.Fatalf("UNSET: %+v", u)
	}
	for _, s := range stmts {
		again, err := ParseFile(s.Pretty() + ";")
		if err != nil || !reflect.DeepEqual(strip(again[0]), strip(s)) {
			t.Fatalf("round trip: %v\n%s", err, s.Pretty())
		}
	}
	// A setting is no variable: the same name in SET VAR is not a repeat,
	// and a credential-looking key name (secrets) is not refused.
	if _, err := ParseFile("ALTER PLAYBOOK k SET VAR host=x SET SANDBOX host=me@buildbox secrets=proxy;"); err != nil {
		t.Errorf("a variable and a setting of one name: %v", err)
	}
	for src, want := range map[string]string{
		"ALTER PLAYBOOK k SET SANDBOX nope=1;":                         "nope is not a [sandbox] key",
		"ALTER PLAYBOOK k UNSET SANDBOX nope;":                         "nope is not a [sandbox] key",
		"ALTER PLAYBOOK k SET SANDBOX share_skills=yes;":               "sandbox.share_skills takes true or false",
		"ALTER PLAYBOOK k SET SANDBOX host=;":                          "UNSET SANDBOX host clears it",
		"ALTER PLAYBOOK k SET SANDBOX backend;":                        "SET SANDBOX takes <key>=<value>",
		"ALTER PLAYBOOK k SET SANDBOX host=a UNSET SANDBOX host;":      "sandbox.host appears twice",
		"ALTER PLAYBOOK k SET SANDBOX UNSET SANDBOX;":                  "cannot be combined",
		"ALTER PLAYBOOK k SET SANDBOX UNSET ISOLATED LOGIN;":           "cannot be combined",
		"ALTER PLAYBOOK k SET SANDBOX host=a SET SANDBOX backend=sbx;": "appears twice",
	} {
		if _, err := ParseFile(src); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", src, err, want)
		}
	}
}
