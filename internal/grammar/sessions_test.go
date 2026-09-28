package grammar

import (
	"strings"
	"testing"
)

func TestSessionsGrammar(t *testing.T) {
	s, err := ParseArgs(strings.Fields("SHOW SESSIONS FOR PLAYBOOK k --json"))
	if err != nil || s.Verb != Show || s.Object != Sessions || s.For != "k" || !s.JSON {
		t.Fatalf("%+v %v", s, err)
	}
	s, err = ParseArgs(strings.Fields("RESUME FOR PLAYBOOK k SESSION 8ba14a71-aaaa-4bbb-8ccc-123456789abc"))
	if err != nil || s.Verb != Resume || s.For != "k" || s.Session != "8ba14a71-aaaa-4bbb-8ccc-123456789abc" {
		t.Fatalf("%+v %v", s, err)
	}
	s, err = ParseArgs(strings.Fields("RESUME --list --json"))
	if err != nil || !s.List || !s.JSON {
		t.Fatalf("%+v %v", s, err)
	}
	if s, err = ParseArgs([]string{"resume"}); err != nil || s.Verb != Resume {
		t.Fatalf("lowercase: %+v %v", s, err)
	}
	for line, want := range map[string]string{
		"RESUME SESSION a SESSION b":           "appears twice",
		"RESUME SESSION a/b":                   "session id",
		"RESUME --list SESSION a":              "use one of them",
		"RESUME --json":                        "only with --list",
		"SHOW SESSIONS FOR k":                  "FOR PLAYBOOK",
		"RESUME FOR PLAYBOOK a FOR PLAYBOOK b": "appears twice",
		"RESUME now":                           "unexpected",
	} {
		if _, err := ParseArgs(strings.Fields(line)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", line, err)
		}
	}
	if _, err := ParseFile("RESUME;"); err == nil || !strings.Contains(err.Error(), "only on the command line") {
		t.Errorf("in a file: %v", err)
	}
	if _, err := ParseFile("SHOW SESSIONS;"); err == nil || !strings.Contains(err.Error(), "only reads") {
		t.Errorf("SHOW in a file: %v", err)
	}
	if !IsStatement([]string{"RESUME"}) || IsKeyword("SESSIONS") || IsKeyword("RESUME") || IsKeyword("SESSION") || IsKeyword("FOR") {
		t.Error("RESUME is a statement; no new word is reserved")
	}
}
