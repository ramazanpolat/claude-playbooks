package grammar

import (
	"strings"
	"testing"
)

func TestParseSkills(t *testing.T) {
	stmts, err := ParseFile(`ALTER PLAYBOOK k
  ADD SKILL notes FROM '~/src/skills/notes'
  ADD SKILL rel FROM './local-skill'
  ADD SKILL pub FROM 'https://github.com/o/skills' BRANCH 'v1' SUBDIR 'release-notes'
  DROP SKILL old;`)
	if err != nil {
		t.Fatal(err)
	}
	c := stmts[0].Clauses
	if c[0].Kind != AddSkill || c[0].Skill.From != "~/src/skills/notes" || c[2].Skill.Branch != "v1" || c[2].Skill.Subdir != "release-notes" || c[3].Kind != DropSkill {
		t.Fatalf("clauses: %+v %+v %+v", c[0], c[2], c[3])
	}
	if _, err := ParseFile(stmts[0].String()); err != nil {
		t.Fatalf("round trip: %v\n%s", err, stmts[0].String())
	}
	for src, want := range map[string]string{
		"ALTER PLAYBOOK k ADD SKILL s FROM '/d' BRANCH 'x';":   "BRANCH applies to a git source",
		"ALTER PLAYBOOK k ADD SKILL s FROM 'rel/dir';":         "unsupported skill source",
		"ALTER PLAYBOOK k ADD SKILL s;":                        "ADD SKILL needs FROM",
		"ALTER PLAYBOOK k ADD SKILL s FROM 'https://u:p@x/r';": "carrying credentials",
	} {
		if _, err := ParseFile(src); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", src, err, want)
		}
	}
	if _, err := ParseArgs(w("ALTER PLAYBOOK k ADD SKILL s FROM ./x")); err == nil || !strings.Contains(err.Error(), "resolves against its playbook file") {
		t.Errorf("a relative skill directory on the command line: %v", err)
	}
}
