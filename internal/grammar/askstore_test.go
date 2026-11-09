package grammar

import (
	"slices"
	"strings"
	"testing"
)

func TestAskStoreGrammar(t *testing.T) {
	s, err := ParseArgs(strings.Fields("SHOW ASK HISTORY FOR PLAYBOOK k BEFORE r-20261102-113912-3f2a LIMIT 50 --json"))
	if err != nil || s.Verb != Show || s.Object != AskHistory || s.For != "k" ||
		s.HistoryBefore != "r-20261102-113912-3f2a" || s.HistoryLimit != 50 || !s.JSON {
		t.Fatalf("%+v %v", s, err)
	}
	// The clauses go in any order, and the canonical form parses back to
	// the same statement.
	s2, err := ParseArgs(strings.Fields("show ask history limit 50 before r-20261102-113912-3f2a for playbook k --json"))
	if err != nil {
		t.Fatal(err)
	}
	if s.String() != s2.String() || s.String() != "SHOW ASK HISTORY FOR PLAYBOOK k BEFORE r-20261102-113912-3f2a LIMIT 50 --json" {
		t.Errorf("%q %q", s.String(), s2.String())
	}
	if back, err := ParseLine(s.String()); err != nil || back.String() != s.String() {
		t.Errorf("round trip: %v %v", back, err)
	}
	s, err = ParseArgs(strings.Fields("SHOW ASK HISTORY"))
	if err != nil || s.Object != AskHistory || s.For != "" || s.HistoryBefore != "" || s.HistoryLimit != 0 || s.JSON {
		t.Fatalf("%+v %v", s, err)
	}
	s, err = ParseArgs(strings.Fields("SHOW ASK RECORD r-20261102-113912-3f2a --json"))
	if err != nil || s.Object != AskRecord || s.Name != "r-20261102-113912-3f2a" || !s.JSON {
		t.Fatalf("%+v %v", s, err)
	}
	if s.String() != "SHOW ASK RECORD r-20261102-113912-3f2a --json" {
		t.Errorf("%q", s.String())
	}
	for line, want := range map[string]string{
		"SHOW ASK":                "HISTORY or RECORD",
		"SHOW ASK RECORD":         "missing <id>",
		"SHOW ASK RECORD notanid": "not the id of a record",
		"SHOW ASK RECORD r-20261102-113912-3f2a x":       "unexpected",
		"SHOW ASK HISTORY LIMIT":                         "missing <n>",
		"SHOW ASK HISTORY LIMIT 0":                       "from 1 to 200",
		"SHOW ASK HISTORY LIMIT 201":                     "from 1 to 200",
		"SHOW ASK HISTORY LIMIT ten":                     "from 1 to 200",
		"SHOW ASK HISTORY LIMIT 5 LIMIT 6":               "LIMIT appears twice",
		"SHOW ASK HISTORY BEFORE x":                      "not the id of a record",
		"SHOW ASK HISTORY FOR k":                         "FOR PLAYBOOK",
		"SHOW ASK HISTORY FOR PLAYBOOK a FOR PLAYBOOK b": "FOR PLAYBOOK appears twice",
		"SHOW ASK HISTORY --json LIMIT 5":                "unexpected",
	} {
		if _, err := ParseArgs(strings.Fields(line)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", line, err)
		}
	}
	// A mistyped id is never quoted back: the slot may hold anything.
	if _, err := ParseArgs([]string{"SHOW", "ASK", "RECORD", "sk-ant-oat01-arena0canary"}); err == nil || strings.Contains(err.Error(), "canary") {
		t.Errorf("id echoed: %v", err)
	}
	if _, err := ParseFile("SHOW ASK HISTORY;"); err == nil || !strings.Contains(err.Error(), "only reads") {
		t.Errorf("SHOW in a file: %v", err)
	}
	for _, w := range []string{"ASK", "HISTORY", "RECORD", "LIMIT"} {
		if IsKeyword(w) {
			t.Errorf("%s is reserved: SHOW ASK reserves no word", w)
		}
	}
	// Completion offers what may come next.
	if got := Expect([]string{"SHOW", "ASK"}); !slices.Equal(got, []string{"HISTORY", "RECORD"}) {
		t.Errorf("after SHOW ASK: %q", got)
	}
	if got := Expect([]string{"SHOW", "ASK", "HISTORY"}); !slices.Contains(got, "FOR") || !slices.Contains(got, "BEFORE") || !slices.Contains(got, "LIMIT") || !slices.Contains(got, "--json") {
		t.Errorf("after SHOW ASK HISTORY: %q", got)
	}
}
