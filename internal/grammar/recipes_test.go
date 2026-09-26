package grammar

import (
	"strings"
	"testing"
)

func TestParseRecipes(t *testing.T) {
	stmts, err := ParseFile("USE PLAYBOOK work;\nALTER PLAYBOOK SET VAR A=1 ALLOW TOOL 'x';\nALTER PLAYBOOK 'set' SET VAR B=2;")
	if err != nil {
		t.Fatal(err)
	}
	if stmts[0].Verb != Use || stmts[0].Name != "work" {
		t.Errorf("USE: %+v", stmts[0])
	}
	if !stmts[1].Recipe || stmts[1].Name != "" || stmts[1].String() != "ALTER PLAYBOOK SET VAR A=1 ALLOW TOOL 'x'" {
		t.Errorf("recipe: %+v %s", stmts[1], stmts[1].String())
	}
	if stmts[2].Recipe || stmts[2].Name != "set" {
		t.Errorf("a quoted keyword is a name: %+v", stmts[2])
	}
	for args, want := range map[string]string{
		"ALTER PLAYBOOK SET VAR A=1": "names its playbook",
		"USE PLAYBOOK work":          "USE PLAYBOOK appears only in a playbook file",
		"APPLY f.cpb TO a TO b":      "TO appears twice",
	} {
		if _, err := ParseArgs(w(args)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", args, err, want)
		}
	}
	st, err := ParseArgs(w("APPLY f.cpb TO ~/.claude --dry-run"))
	if err != nil || st.Target != "~/.claude" || !st.DryRun || st.String() != "APPLY f.cpb TO ~/.claude --dry-run" {
		t.Fatalf("APPLY TO: %+v %v", st, err)
	}
}
