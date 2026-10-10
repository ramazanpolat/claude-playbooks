package grammar

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseStatuslineRefresh(t *testing.T) {
	st, err := ParseLine("ALTER PLAYBOOK k SET statusline.command = 'bash sl.sh', statusline.refresh = 10")
	if err != nil {
		t.Fatal(err)
	}
	if got := strip(st).Clauses; !reflect.DeepEqual(got, []Clause{{Kind: SetStatusline, Arg: "bash sl.sh", Refresh: 10}}) {
		t.Fatalf("clauses: %+v", got)
	}
	again, err := ParseFile(st.String())
	if err != nil || !reflect.DeepEqual(strip(again[0]), strip(st)) {
		t.Fatalf("round trip %q: %v", st.String(), err)
	}
	for line, want := range map[string]Clause{
		"ALTER PLAYBOOK k SET statusline.refresh = 5":                                    {Kind: SetStatuslineRefresh, Refresh: 5},
		"ALTER PLAYBOOK k DELETE statusline.refresh":                                     {Kind: UnsetStatuslineRefresh},
		"ALTER PLAYBOOK k DELETE statusline":                                             {Kind: UnsetStatusline},
		"ALTER PLAYBOOK k SET statusline.command = 'x'":                                  {Kind: SetStatusline, Arg: "x"},
		"ALTER PLAYBOOK k SET statusline.command = 'refresh'":                            {Kind: SetStatusline, Arg: "refresh"},
		"ALTER PLAYBOOK k SET IF UNSET statusline.command = 'x'":                         {Kind: SetIfUnset, Group: []Clause{{Kind: SetStatusline, Arg: "x"}}},
		"ALTER PLAYBOOK k SET IF UNSET statusline.command = 'x', statusline.refresh = 5": {Kind: SetIfUnset, Group: []Clause{{Kind: SetStatusline, Arg: "x", Refresh: 5}}},
		"ALTER PLAYBOOK k SET statusline.command = 'x' SET statusline.refresh = 5":       {Kind: SetStatusline, Arg: "x", Refresh: 5},
	} {
		st, err := ParseLine(line)
		if err != nil || !reflect.DeepEqual(strip(st).Clauses, []Clause{want}) {
			t.Errorf("%s: %v %+v", line, err, st)
		}
	}
}

func TestStatuslineRefreshErrors(t *testing.T) {
	for line, want := range map[string]string{
		"ALTER PLAYBOOK k SET statusline.refresh = 0":                             "at least 1",
		"ALTER PLAYBOOK k SET statusline.refresh = 10s":                           "with no unit",
		"ALTER PLAYBOOK k SET statusline.refresh = -3":                            "whole number",
		"ALTER PLAYBOOK k SET statusline.command = 'x', statusline.refresh = 2.5": "whole number",
		"ALTER PLAYBOOK k SET statusline.refresh = 5 DELETE statusline.refresh":   "statusline.refresh is named twice",
		"ALTER PLAYBOOK k SET statusline.refresh = 5 SET statusline.refresh = 6":  "statusline.refresh is named twice",
		"ALTER PLAYBOOK k DELETE statusline DELETE statusline.refresh":            "statusline.refresh is named twice",
		"ALTER PLAYBOOK k DELETE statusline.command, statusline":                  "statusline is named twice",
		"ALTER PLAYBOOK k SET statusline = 'x'":                                   "statusline is a table",
	} {
		if _, err := ParseLine(line); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", line, err, want)
		}
	}
}

// SET IF UNSET takes any property, as one clause that applies whole or not
// at all; it round-trips, and IF takes only UNSET.
func TestStatuslineIfUnset(t *testing.T) {
	st, err := ParseLine("ALTER PLAYBOOK k SET IF UNSET statusline.command = 'bash sl.sh', statusline.refresh = 10")
	if err != nil {
		t.Fatal(err)
	}
	if got := st.String(); !strings.Contains(got, "SET IF UNSET statusline.command = 'bash sl.sh', statusline.refresh = 10") {
		t.Fatalf("format: %s", got)
	}
	again, err := ParseFile(st.String())
	if err != nil || !reflect.DeepEqual(strip(again[0]), strip(st)) {
		t.Fatalf("round trip %q: %v", st.String(), err)
	}
	for line, want := range map[string]string{
		"ALTER PLAYBOOK k SET statusline.command = 'x' IF UNSET": "IF UNSET goes right after SET",
		"ALTER PLAYBOOK k SET IF statusline.command = 'x'":       "expected UNSET after IF",
		"ALTER PLAYBOOK k SET IF UNSET model = 'm', model = 'n'": "model is named twice",
		"ALTER PLAYBOOK k SET IF UNSET model = 'm' DELETE model": "model is named twice",
	} {
		if _, err := ParseLine(line); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", line, err, want)
		}
	}
	for _, line := range []string{
		"ALTER PLAYBOOK k SET IF UNSET model = 'm', agent = 'a', sandbox.host = 'me@box', memory = 'shared'",
		"ALTER PLAYBOOK k SET IF UNSET statusline.refresh = 5 SET VAR A=1 SET IF UNSET launcher = 'kk'",
	} {
		st, err := ParseLine(line)
		if err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		if st.Clauses[0].Kind != SetIfUnset || len(st.Clauses[0].Group) == 0 {
			t.Errorf("%s: %+v", line, st.Clauses)
		}
		again, err := ParseFile(st.String())
		if err != nil || !reflect.DeepEqual(strip(again[0]), strip(st)) {
			t.Errorf("round trip %q: %v", st.String(), err)
		}
	}
}
