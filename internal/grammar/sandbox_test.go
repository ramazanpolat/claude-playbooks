package grammar

import (
	"reflect"
	"strings"
	"testing"
)

// The [sandbox] table, key for key, as sandbox.<key> properties: true and
// false bare, lists as ['a', 'b'], strings quoted.
func TestParseSandboxProperties(t *testing.T) {
	stmts, err := ParseFile(`ALTER PLAYBOOK k
  SET sandbox.always = true, login = 'isolated'
  SET sandbox.backend = 'sbx', sandbox.host = 'me@buildbox', sandbox.mounts = ['~/libs:ro', '~/data'],
      sandbox.allow_net = ['api.example.com:443'], sandbox.secrets = 'env', sandbox.claude_version = '2.1.0',
      sandbox.share_skills = true, sandbox.workdir = '~/my proj';
ALTER PLAYBOOK k SET sandbox.always = false DELETE sandbox.host, sandbox.mounts;
ALTER PLAYBOOK k DELETE sandbox;`)
	if err != nil {
		t.Fatal(err)
	}
	c := stmts[0].Clauses
	want := []Var{{Key: "backend", Value: "sbx"}, {Key: "host", Value: "me@buildbox"}, {Key: "mounts", Value: "~/libs:ro,~/data"},
		{Key: "allow_net", Value: "api.example.com:443"}, {Key: "secrets", Value: "env"}, {Key: "claude_version", Value: "2.1.0"},
		{Key: "share_skills", Value: "true"}, {Key: "workdir", Value: "~/my proj"}}
	if c[0].Kind != SetSandboxKeys || !reflect.DeepEqual(c[0].Settings, []Var{{Key: "always", Value: "true"}}) || c[1].Kind != SetProperties ||
		c[2].Kind != SetSandboxKeys || !reflect.DeepEqual(c[2].Settings, want) {
		t.Fatalf("SET: %+v", c)
	}
	u := stmts[1].Clauses
	if u[0].Kind != SetSandboxKeys || u[1].Kind != UnsetSandboxKeys || !reflect.DeepEqual(u[1].Settings, []Var{{Key: "host"}, {Key: "mounts"}}) {
		t.Fatalf("DELETE: %+v", u)
	}
	if d := stmts[2].Clauses[0]; d.Kind != UnsetSandboxKeys || len(d.Settings) != 9 {
		t.Fatalf("DELETE sandbox resets the table: %+v", d)
	}
	for _, s := range stmts {
		again, err := ParseFile(s.Pretty() + ";")
		if err != nil || !reflect.DeepEqual(strip(again[0]), strip(s)) {
			t.Fatalf("round trip: %v\n%s", err, s.Pretty())
		}
	}
	if got := stmts[0].String(); !strings.Contains(got, "sandbox.mounts = ['~/libs:ro', '~/data'], sandbox.allow_net = ['api.example.com:443']") || !strings.Contains(got, "sandbox.share_skills = true") {
		t.Errorf("formatted: %s", got)
	}
	// On the command line a list is one argument the pilot quoted whole, or
	// its items comma-joined, which needs no [ ] for the shell to glob.
	for _, args := range [][]string{
		{"ALTER", "PLAYBOOK", "k", "SET", "sandbox.mounts", "=", "['~/a:ro','~/b']"},
		{"ALTER", "PLAYBOOK", "k", "SET", "sandbox.mounts=[~/a:ro, ~/b]"},
		{"ALTER", "PLAYBOOK", "k", "SET", "sandbox.mounts", "=", "~/a:ro,~/b"},
		{"ALTER", "PLAYBOOK", "k", "SET", "sandbox.mounts=~/a:ro,~/b"},
	} {
		st, err := ParseArgs(args)
		if err != nil || st.Clauses[0].Settings[0].Value != "~/a:ro,~/b" {
			t.Errorf("%q: %v %+v", args, err, st)
		}
	}
	// In a file too: a quoted comma-joined string, or bare items. SHOW
	// CREATE writes the [ ] form.
	for _, src := range []string{
		"ALTER PLAYBOOK k SET sandbox.mounts = '~/a:ro,~/b';",
		"ALTER PLAYBOOK k SET sandbox.mounts = '~/a:ro,~/b', sandbox.host = 'h';",
		"ALTER PLAYBOOK k SET sandbox.mounts = [~/a:ro, ~/b];",
		"ALTER PLAYBOOK k SET sandbox.share_skills = 'true', sandbox.mounts = ['~/a:ro', '~/b'];",
	} {
		st, err := ParseFile(src)
		if err != nil {
			t.Errorf("%s: %v", src, err)
			continue
		}
		var mounts string
		for _, v := range st[0].Clauses[0].Settings {
			if v.Key == "mounts" {
				mounts = v.Value
			}
		}
		if mounts != "~/a:ro,~/b" {
			t.Errorf("%s: %+v", src, st[0].Clauses)
		}
		if got := st[0].String(); !strings.Contains(got, "sandbox.mounts = ['~/a:ro', '~/b']") {
			t.Errorf("%s: formatted %s", src, got)
		}
	}
	// A setting is no variable: the same name in SET VAR is not a repeat,
	// and a credential-looking key name (secrets) is not refused.
	if _, err := ParseFile("ALTER PLAYBOOK k SET VAR host=x SET sandbox.host = 'me@buildbox', sandbox.secrets = 'proxy';"); err != nil {
		t.Errorf("a variable and a setting of one name: %v", err)
	}
	for src, want := range map[string]string{
		"ALTER PLAYBOOK k SET sandbox.nope = '1';":                      "sandbox.nope is not a playbook property",
		"ALTER PLAYBOOK k SET sandbox.share_skills = 'yes';":            "sandbox.share_skills takes true or false",
		"ALTER PLAYBOOK k SET sandbox.host = '';":                       "DELETE sandbox.<key> clears it",
		"ALTER PLAYBOOK k SET sandbox.mounts = '';":                     "sandbox.mounts takes a list",
		"ALTER PLAYBOOK k SET sandbox.mounts = [~/a;":                   "sandbox.mounts takes a list",
		"ALTER PLAYBOOK k SET sandbox.secrets = 'all';":                 "sandbox.secrets takes 'proxy' or 'env'",
		"ALTER PLAYBOOK k SET sandbox.host = 'a' DELETE sandbox.host;":  "sandbox.host is named twice",
		"ALTER PLAYBOOK k SET sandbox.host = 'a' DELETE sandbox;":       "sandbox is named twice",
		"ALTER PLAYBOOK k SET sandbox = true;":                          "sandbox is a table",
		"ALTER PLAYBOOK k SET sandbox.always = true, login = 'shared';": "cannot be combined with login = 'shared'",
		"ALTER PLAYBOOK k SET sandbox.always = true DELETE login;":      "cannot be combined with login = 'shared'",
		"CREATE PLAYBOOK k SET sandbox.always = true;":                  "a sandboxed playbook's login is isolated",
		"CREATE PLAYBOOK k LINK /d SET sandbox.backend = 'sbx';":        "sandbox.backend does not apply to LINK",
		"CREATE PLAYBOOK k SANDBOX;":                                    "SANDBOX is a property now: SET sandbox.always = true, login = 'isolated'",
		"ALTER PLAYBOOK k SET SANDBOX;":                                 "SET SANDBOX is a property now",
		"ALTER PLAYBOOK k SET SANDBOX backend=sbx;":                     "SET sandbox.<key> = '<value>'",
		"ALTER PLAYBOOK k UNSET SANDBOX;":                               "SET sandbox.always = false",
		"ALTER PLAYBOOK k UNSET SANDBOX host;":                          "DELETE sandbox.<key>",
	} {
		if _, err := ParseFile(src); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", src, err, want)
		}
	}
	if _, err := ParseFile("CREATE PLAYBOOK k SET sandbox.always = true, login = 'isolated', sandbox.backend = 'sbx';"); err != nil {
		t.Errorf("a sandboxed CREATE: %v", err)
	}
}
