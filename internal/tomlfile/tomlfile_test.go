package tomlfile

import (
	"errors"
	"reflect"
	"testing"
)

type target struct {
	Name string `toml:"name"`
	Env  *struct {
		Sets []string          `toml:"sets"`
		Set  map[string]string `toml:"set"`
	} `toml:"env"`
	MCP map[string]*struct {
		Vars []string `toml:"vars"`
	} `toml:"mcp"`
}

func TestDecodeRefusesEveryUnknownKeyWithItsLine(t *testing.T) {
	data := []byte(`name = "x"
alias = "k"
"isolate_auth" = true

[env]
profiles = ["a"]
sets = ["b"]

[env.set]
ANY_NAME = "v"

[mcp.sentry]
vars = ["A"]
extra = { a = 1 }

[statusline]
command = "x"
refresh = 5

note = """
nope = 1
"""
`)
	var v target
	err := Decode("/p/.playbook", data, &v)
	var uk *UnknownKeyError
	if !errors.As(err, &uk) {
		t.Fatalf("%v", err)
	}
	want := []Unknown{{"alias", 2}, {"isolate_auth", 3}, {"env.profiles", 6}, {"mcp.sentry.extra", 14}, {"statusline", 16}}
	if !reflect.DeepEqual(uk.Keys, want) {
		t.Fatalf("keys %+v, want %+v", uk.Keys, want)
	}
	if got := err.Error(); got != `unknown key "alias" in /p/.playbook:2; unknown key "isolate_auth" in /p/.playbook:3; unknown key "env.profiles" in /p/.playbook:6; unknown key "mcp.sentry.extra" in /p/.playbook:14; unknown key "statusline" in /p/.playbook:16` {
		t.Fatalf("message: %s", got)
	}
	// Everything modelled is still decoded.
	if v.Name != "x" || v.Env.Sets[0] != "b" || v.Env.Set["ANY_NAME"] != "v" || v.MCP["sentry"].Vars[0] != "A" {
		t.Fatalf("decoded %+v", v)
	}
}

func TestDecodeAcceptsAModelledFile(t *testing.T) {
	var v target
	if err := Decode("f", []byte("name = \"x\"\n[env]\nsets = []\n"), &v); err != nil {
		t.Fatal(err)
	}
}

// A syntax error is the toml package's, not an unknown key.
func TestDecodeSyntaxError(t *testing.T) {
	var v target
	err := Decode("f", []byte("name = \n"), &v)
	var uk *UnknownKeyError
	if err == nil || errors.As(err, &uk) {
		t.Fatalf("%v", err)
	}
}

// A key inside an inline table is placed by the line of the key holding it.
func TestDecodePlacesInlineTableKeys(t *testing.T) {
	var v struct {
		Env struct {
			Sets []string `toml:"sets"`
		} `toml:"env"`
	}
	err := Decode("f", []byte("env = { sets = [], unset = [\"A\"] }\n"), &v)
	var uk *UnknownKeyError
	if !errors.As(err, &uk) || !reflect.DeepEqual(uk.Keys, []Unknown{{"env.unset", 1}}) {
		t.Fatalf("%v", err)
	}
}
