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
	for line, want := range map[string]string{
		"SHOW SESSIONS FOR k": "FOR PLAYBOOK",
		// There is no RESUME: claude's own --resume, through a launcher or
		// cpb run, resumes a session.
		"RESUME":               "not a statement",
		"RESUME SESSION a":     "not a statement",
		"SHOW SESSIONS --list": "unexpected",
	} {
		if _, err := ParseArgs(strings.Fields(line)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", line, err)
		}
	}
	if _, err := ParseFile("SHOW SESSIONS;"); err == nil || !strings.Contains(err.Error(), "only reads") {
		t.Errorf("SHOW in a file: %v", err)
	}
	if IsStatement([]string{"RESUME"}) || IsKeyword("SESSIONS") || IsKeyword("RESUME") || IsKeyword("SESSION") || IsKeyword("FOR") {
		t.Error("RESUME is no statement, and SHOW SESSIONS reserves no word")
	}
}
