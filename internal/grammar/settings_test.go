package grammar

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseSettingsClauses(t *testing.T) {
	stmts, err := ParseFile(`ALTER PLAYBOOK k
  ALLOW TOOL 'Bash(toolkit-helper *)' 'mcp__sentry'
  DENY TOOL 'Bash(rm -rf *)'
  UNSET TOOL 'Read(~/x)'
  SET STATUSLINE '~/bin/status.sh --short'
  SET MODEL 'claude-opus-5-5';`)
	if err != nil {
		t.Fatal(err)
	}
	c := stmts[0].Clauses
	if c[0].Kind != AllowTool || !reflect.DeepEqual(c[0].Names, []string{"Bash(toolkit-helper *)", "mcp__sentry"}) ||
		c[1].Kind != DenyTool || c[2].Kind != UnsetTool || c[3].Kind != SetStatusline || c[3].Arg != "~/bin/status.sh --short" ||
		c[4].Kind != SetModel || c[4].Arg != "claude-opus-5-5" {
		t.Fatalf("clauses: %+v", c)
	}
	again, err := ParseFile(stmts[0].String())
	if err != nil || !reflect.DeepEqual(strip(again[0]), strip(stmts[0])) {
		t.Fatalf("round trip: %v\n%s", err, stmts[0].String())
	}
	for _, src := range []string{"ALTER PLAYBOOK k UNSET STATUSLINE UNSET MODEL;"} {
		if _, err := ParseFile(src); err != nil {
			t.Errorf("%s: %v", src, err)
		}
	}
	for src, want := range map[string]string{
		"ALTER PLAYBOOK k ALLOW 'x';":                    "ALLOW takes TOOL",
		"ALTER PLAYBOOK k SET MODEL 'a b';":              "SET MODEL needs",
		"ALTER PLAYBOOK k SET MODEL m UNSET MODEL;":      "cannot be combined",
		"ALTER PLAYBOOK k ALLOW TOOL 'x' DENY TOOL 'x';": "tool rule x appears twice",
	} {
		if _, err := ParseFile(src); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", src, err, want)
		}
	}
}
