package grammar

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseStatuslineRefresh(t *testing.T) {
	st, err := ParseLine("ALTER PLAYBOOK k SET statusline = 'bash sl.sh', statusline_refresh = 10")
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
		"ALTER PLAYBOOK k SET statusline_refresh = 5":                            {Kind: SetStatuslineRefresh, Refresh: 5},
		"ALTER PLAYBOOK k DELETE statusline_refresh":                             {Kind: UnsetStatuslineRefresh},
		"ALTER PLAYBOOK k DELETE statusline":                                     {Kind: UnsetStatusline},
		"ALTER PLAYBOOK k SET statusline = 'x'":                                  {Kind: SetStatusline, Arg: "x"},
		"ALTER PLAYBOOK k SET statusline = 'refresh'":                            {Kind: SetStatusline, Arg: "refresh"},
		"ALTER PLAYBOOK k SET IF UNSET statusline = 'x'":                         {Kind: SetStatusline, Arg: "x", IfUnset: true},
		"ALTER PLAYBOOK k SET IF UNSET statusline = 'x', statusline_refresh = 5": {Kind: SetStatusline, Arg: "x", Refresh: 5, IfUnset: true},
		"ALTER PLAYBOOK k SET statusline = 'x' SET statusline_refresh = 5":       {Kind: SetStatusline, Arg: "x", Refresh: 5},
	} {
		st, err := ParseLine(line)
		if err != nil || !reflect.DeepEqual(strip(st).Clauses, []Clause{want}) {
			t.Errorf("%s: %v %+v", line, err, st)
		}
	}
}

func TestStatuslineRefreshErrors(t *testing.T) {
	for line, want := range map[string]string{
		"ALTER PLAYBOOK k SET statusline_refresh = 0":                               "at least 1",
		"ALTER PLAYBOOK k SET statusline_refresh = 10s":                             "with no unit",
		"ALTER PLAYBOOK k SET statusline_refresh = -3":                              "whole number",
		"ALTER PLAYBOOK k SET statusline = 'x', statusline_refresh = 2.5":           "whole number",
		"ALTER PLAYBOOK k SET statusline_refresh = 5 DELETE statusline_refresh":     "statusline_refresh is named twice",
		"ALTER PLAYBOOK k SET statusline_refresh = 5 SET statusline_refresh = 6":    "statusline_refresh is named twice",
		"ALTER PLAYBOOK k SET statusline = 'x' SET IF UNSET statusline_refresh = 5": "IF UNSET applies to both",
		"ALTER PLAYBOOK k DELETE statusline DELETE statusline_refresh":              "cannot be combined",
	} {
		if _, err := ParseLine(line); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", line, err, want)
		}
	}
}

// IF UNSET round-trips, and IF takes only UNSET.
func TestStatuslineIfUnset(t *testing.T) {
	st, err := ParseLine("ALTER PLAYBOOK k SET IF UNSET statusline = 'bash sl.sh', statusline_refresh = 10")
	if err != nil {
		t.Fatal(err)
	}
	if got := st.String(); !strings.Contains(got, "SET IF UNSET statusline = 'bash sl.sh', statusline_refresh = 10") {
		t.Fatalf("format: %s", got)
	}
	again, err := ParseFile(st.String())
	if err != nil || !reflect.DeepEqual(strip(again[0]), strip(st)) {
		t.Fatalf("round trip %q: %v", st.String(), err)
	}
	for line, want := range map[string]string{
		"ALTER PLAYBOOK k SET statusline = 'x' IF UNSET":       "IF UNSET goes right after SET",
		"ALTER PLAYBOOK k SET IF statusline = 'x'":             "expected UNSET after IF",
		"ALTER PLAYBOOK k SET IF UNSET model = 'm'":            "IF UNSET takes the status line",
		"ALTER PLAYBOOK k SET IF UNSET statusline_refresh = 5": "IF UNSET takes the status line",
	} {
		if _, err := ParseLine(line); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", line, err, want)
		}
	}
}
