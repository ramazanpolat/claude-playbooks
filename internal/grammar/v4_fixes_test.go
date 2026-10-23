package grammar

import "testing"

// In a playbook file a comma after a quoted item is the separator, never
// part of the item: 'a', b and A='x', B=y.
func TestCommaAfterQuotedItemSeparates(t *testing.T) {
	stmts, err := ParseFile("ALTER ENV r SET ANTHROPIC_BASE_URL='http://h/v1', ANTHROPIC_MODEL=glm;\n" +
		"ALTER PLAYBOOK w ALLOW TOOL 'Bash(git diff *)', 'Read';\n" +
		"ALTER PLAYBOOK w ADD MCP SERVER s COMMAND 'srv' ARGS '--a', '--b' VAR A='1', B=2;\n" +
		"ALTER ENV r SET LAST='ends,';\n")
	if err != nil {
		t.Fatal(err)
	}
	vars := Flatten(stmts[0].Clauses)[0].Vars
	if vars[0].Value != "http://h/v1" || vars[1].Value != "glm" {
		t.Fatalf("vars = %+v", vars)
	}
	if names := Flatten(stmts[1].Clauses)[0].Names; len(names) != 2 || names[0] != "Bash(git diff *)" || names[1] != "Read" {
		t.Fatalf("tool rules = %q", names)
	}
	m := Flatten(stmts[2].Clauses)[0].MCP
	if len(m.Args) != 2 || m.Args[0] != "--a" || m.Args[1] != "--b" {
		t.Fatalf("args = %q", m.Args)
	}
	if len(m.Env) != 2 || m.Env[0].Value != "1" || m.Env[1].Value != "2" {
		t.Fatalf("mcp env = %+v", m.Env)
	}
	// A comma inside the quotes is the value's own.
	if v := Flatten(stmts[3].Clauses)[0].Vars[0].Value; v != "ends," {
		t.Fatalf("quoted trailing comma = %q", v)
	}
}

// A property value quoted in a file keeps the quotes it holds.
func TestQuotedPropertyKeepsInnerQuotes(t *testing.T) {
	stmts, err := ParseFile("ALTER PLAYBOOK w SET statusline.command = '\"/opt/My Tools/sl.sh\"';\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := Flatten(stmts[0].Clauses)[0].Arg; got != `"/opt/My Tools/sl.sh"` {
		t.Fatalf("command = %q", got)
	}
	// On the command line a shell-kept pair of quotes is still removed.
	st, err := ParseArgs([]string{"ALTER", "PLAYBOOK", "w", "SET", "global_instructions='inherited'"})
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := PropertyValue(st.Clauses, "global_instructions"); v != "inherited" {
		t.Fatalf("argv value = %q", v)
	}
}

func TestByteOrderMarkIgnored(t *testing.T) {
	if _, err := ParseFile("\uFEFFCREATE PLAYBOOK w;\n"); err != nil {
		t.Fatalf("BOM file: %v", err)
	}
}
