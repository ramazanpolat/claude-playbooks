package grammar

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseStatuslineRefresh(t *testing.T) {
	st, err := ParseLine("ALTER PLAYBOOK k SET STATUSLINE 'bash sl.sh' REFRESH 10")
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
		"ALTER PLAYBOOK k SET STATUSLINE REFRESH 5":              {Kind: SetStatuslineRefresh, Refresh: 5},
		"ALTER PLAYBOOK k UNSET STATUSLINE REFRESH":              {Kind: UnsetStatuslineRefresh},
		"ALTER PLAYBOOK k UNSET STATUSLINE":                      {Kind: UnsetStatusline},
		"ALTER PLAYBOOK k SET STATUSLINE 'x'":                    {Kind: SetStatusline, Arg: "x"},
		"ALTER PLAYBOOK k SET STATUSLINE 'refresh'":              {Kind: SetStatusline, Arg: "refresh"},
		"ALTER PLAYBOOK k SET STATUSLINE 'x' IF UNSET":           {Kind: SetStatusline, Arg: "x", IfUnset: true},
		"ALTER PLAYBOOK k SET STATUSLINE 'x' REFRESH 5 IF UNSET": {Kind: SetStatusline, Arg: "x", Refresh: 5, IfUnset: true},
	} {
		st, err := ParseLine(line)
		if err != nil || !reflect.DeepEqual(strip(st).Clauses, []Clause{want}) {
			t.Errorf("%s: %v %+v", line, err, st)
		}
	}
}

func TestStatuslineRefreshErrors(t *testing.T) {
	for line, want := range map[string]string{
		"ALTER PLAYBOOK k SET STATUSLINE REFRESH 0":                          "at least 1",
		"ALTER PLAYBOOK k SET STATUSLINE REFRESH 10s":                        "with no unit",
		"ALTER PLAYBOOK k SET STATUSLINE REFRESH -3":                         "whole number",
		"ALTER PLAYBOOK k SET STATUSLINE 'x' REFRESH 2.5":                    "whole number",
		"ALTER PLAYBOOK k SET STATUSLINE REFRESH 5 UNSET STATUSLINE REFRESH": "cannot be combined",
		"ALTER PLAYBOOK k SET STATUSLINE 'x' SET STATUSLINE REFRESH 5":       "cannot be combined",
		"ALTER PLAYBOOK k UNSET STATUSLINE UNSET STATUSLINE REFRESH":         "cannot be combined",
	} {
		if _, err := ParseLine(line); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", line, err, want)
		}
	}
}

// IF UNSET round-trips, and IF takes only UNSET.
func TestStatuslineIfUnset(t *testing.T) {
	st, err := ParseLine("ALTER PLAYBOOK k SET STATUSLINE 'bash sl.sh' REFRESH 10 IF UNSET")
	if err != nil {
		t.Fatal(err)
	}
	if got := st.String(); !strings.Contains(got, "SET STATUSLINE 'bash sl.sh' REFRESH 10 IF UNSET") {
		t.Fatalf("format: %s", got)
	}
	again, err := ParseFile(st.String())
	if err != nil || !reflect.DeepEqual(strip(again[0]), strip(st)) {
		t.Fatalf("round trip %q: %v", st.String(), err)
	}
	for line, want := range map[string]string{
		"ALTER PLAYBOOK k SET STATUSLINE 'x' IF NOT EXISTS": "expected UNSET after IF",
		"ALTER PLAYBOOK k SET STATUSLINE 'x' IF":            "expected UNSET after IF",
	} {
		if _, err := ParseLine(line); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", line, err, want)
		}
	}
}
