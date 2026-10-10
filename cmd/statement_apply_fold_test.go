package cmd

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

// foldText folds a file's statements as APPLY would and describes the
// result: per statement, what runs ("-" when nothing does) and what was
// dropped, with the index of the statement that replaces it.
func foldText(t *testing.T, src string) []string {
	t.Helper()
	return foldTextExisting(t, src)
}

// foldTextExisting is foldText with the named playbooks existing before
// anything runs.
func foldTextExisting(t *testing.T, src string, existing ...string) []string {
	t.Helper()
	stmts, err := grammar.ParseFile(src)
	if err != nil {
		t.Fatal(err)
	}
	in := make([]located, len(stmts))
	for i, s := range stmts {
		in[i] = located{file: "f.cpb", path: "/f.cpb", s: s}
	}
	res := foldStatements(in, func(name string) bool { return slices.Contains(existing, name) })
	out := make([]string, len(in))
	for i, x := range res.stmts {
		var runs []string
		for _, c := range x.s.Clauses {
			part := string(c.Kind)
			for _, v := range c.Vars {
				part += " " + v.Key
			}
			for _, k := range c.Keys {
				part += " " + k
			}
			for _, n := range c.Names {
				part += " " + n
			}
			for _, v := range c.Settings {
				part += " " + v.Key
			}
			runs = append(runs, part)
		}
		desc := strings.Join(runs, "; ")
		if res.skip[i] {
			desc = "-"
		}
		for _, o := range res.over[i] {
			desc += " | " + o.what + " by " + string(rune('0'+o.by))
		}
		out[i] = desc
	}
	return out
}

func TestFoldStatements(t *testing.T) {
	cases := []struct {
		name, src string
		want      []string
	}{
		{"a value set three times: only the last is written, list entries split",
			"ALTER PLAYBOOK p SET VAR A=1 B=2;\nALTER PLAYBOOK p SET VAR A=3;\nALTER PLAYBOOK p SET VAR A=4 C=5;",
			[]string{"SET B | SET VAR A by 2", "- | SET VAR A by 2", "SET A C"}},
		{"a tool rule allowed, then denied",
			"ALTER PLAYBOOK p ALLOW TOOL 'X' 'Y';\nALTER PLAYBOOK p DENY TOOL 'X';",
			[]string{"ALLOW TOOL Y | ALLOW TOOL 'X' by 1", "DENY TOOL X"}},
		{"the model, with an unfoldable clause beside it",
			"ALTER PLAYBOOK p ADD PLUGIN x@m SET model = 'a';\nALTER PLAYBOOK p SET model = 'b';",
			[]string{"ADD PLUGIN x@m | SET model by 1", "SET model"}},
		{"IF UNSET after a status line depends on it: both kept",
			"ALTER PLAYBOOK p SET statusline.command = 'a';\nALTER PLAYBOOK p SET IF UNSET statusline.command = 'b';",
			[]string{"SET statusline.command", "SET IF UNSET"}},
		{"IF UNSET before a status line is replaced",
			"ALTER PLAYBOOK p SET IF UNSET statusline.command = 'a';\nALTER PLAYBOOK p SET statusline.command = 'b';",
			[]string{"- | SET IF UNSET by 1", "SET statusline.command"}},
		{"PREVIOUS reads what came before it",
			"ALTER PLAYBOOK p SET statusline.command = 'a';\nALTER PLAYBOOK p REVERT STATUSLINE;",
			[]string{"SET statusline.command", "REVERT STATUSLINE"}},
		// PREVIOUS restores the refresh too, which the later lines without
		// REFRESH keep: it stays.
		{"PREVIOUS reads the whole history: every status line before it stays",
			"ALTER PLAYBOOK p SET statusline.command = 'a';\nALTER PLAYBOOK p SET statusline.command = 'b';\nALTER PLAYBOOK p REVERT STATUSLINE;\nALTER PLAYBOOK p SET statusline.command = 'c';\nALTER PLAYBOOK p SET statusline.command = 'd';",
			[]string{"SET statusline.command", "SET statusline.command", "REVERT STATUSLINE", "- | SET statusline.command by 4", "SET statusline.command"}},
		{"a refresh the later status line does not set stays, alone",
			"ALTER PLAYBOOK p SET statusline.command = 'a', statusline.refresh = 5;\nALTER PLAYBOOK p SET statusline.command = 'b';",
			[]string{"SET statusline.refresh | SET statusline.command by 1", "SET statusline.command"}},
		{"DELETE statusline before a status line without a refresh: its refresh part stays",
			"ALTER PLAYBOOK p DELETE statusline;\nALTER PLAYBOOK p SET statusline.command = 'x';",
			[]string{"DELETE statusline.refresh | DELETE statusline by 1", "SET statusline.command"}},
		{"each part names the statement that replaces it",
			"ALTER PLAYBOOK p DELETE statusline;\nALTER PLAYBOOK p SET statusline.refresh = 10;\nALTER PLAYBOOK p SET statusline.command = 'new';",
			[]string{"- | DELETE statusline by 2 | DELETE statusline.refresh by 1", "SET statusline.refresh", "SET statusline.command"}},
		{"a PREVIOUS that is itself replaced keeps nothing before it",
			"ALTER PLAYBOOK p SET statusline.command = 'a';\nALTER PLAYBOOK p REVERT STATUSLINE;\nALTER PLAYBOOK p DELETE statusline;",
			[]string{"- | SET statusline.command by 2", "- | REVERT STATUSLINE by 2", "DELETE statusline"}},
		{"a later status line with a refresh replaces both",
			"ALTER PLAYBOOK p SET statusline.command = 'a';\nALTER PLAYBOOK p SET statusline.command = 'b', statusline.refresh = 5;",
			[]string{"- | SET statusline.command by 1", "SET statusline.command"}},
		{"SET IF UNSET is dropped only when every key it names is written later",
			"ALTER PLAYBOOK p SET IF UNSET model = 'a', agent = 'x';\nALTER PLAYBOOK p SET model = 'b';",
			[]string{"SET IF UNSET", "SET model"}},
		{"SET IF UNSET whose every key is written later",
			"ALTER PLAYBOOK p SET IF UNSET model = 'a', agent = 'x';\nALTER PLAYBOOK p SET model = 'b' SET agent = 'y';",
			[]string{"- | SET IF UNSET by 1", "SET model; SET agent"}},
		{"a write before SET IF UNSET is read by it: both kept",
			"ALTER PLAYBOOK p SET model = 'a';\nALTER PLAYBOOK p SET IF UNSET model = 'b', agent = 'x';",
			[]string{"SET model", "SET IF UNSET"}},
		{"ADD ENV builds on USE ENV; a second USE ENV replaces both",
			"ALTER PLAYBOOK p USE ENV a;\nALTER PLAYBOOK p ADD ENV b;\nALTER PLAYBOOK p USE ENV c;",
			[]string{"- | USE ENV by 2", "- | ADD ENV by 2", "USE ENV c"}},
		{"sandbox settings, one unset later",
			"ALTER PLAYBOOK p SET sandbox.backend = 'sbx', sandbox.host = 'h';\nALTER PLAYBOOK p DELETE sandbox.host;",
			[]string{"SET sandbox.<key> backend | SET sandbox.host by 1", "DELETE sandbox.<key> host"}},
		{"two targets do not fold into each other",
			"ALTER PLAYBOOK p SET model = 'a';\nALTER PLAYBOOK q SET model = 'b';",
			[]string{"SET model", "SET model"}},
		{"a playbook dropped and created again starts afresh",
			"ALTER PLAYBOOK p SET model = 'a';\nDROP PLAYBOOK p;\nCREATE PLAYBOOK p;\nALTER PLAYBOOK p SET model = 'b';",
			[]string{"SET model", "", "", "SET model"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := foldText(t, c.src)
			if strings.Join(got, "\n") != strings.Join(c.want, "\n") {
				t.Fatalf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(c.want, "\n"))
			}
		})
	}
}

// CREATE OR ALTER PLAYBOOK on a playbook that exists applies its SET list as
// an ALTER would, so its pairs fold as an ALTER's; on one that does not, it
// creates, and starts the target afresh. IF NOT EXISTS never folds.
func TestFoldConvergingCreate(t *testing.T) {
	src := "CREATE OR ALTER PLAYBOOK p FROM 'https://x/y' SET launcher = 'pp', model = 'a';\nALTER PLAYBOOK p SET model = 'b';"
	if got, want := foldTextExisting(t, src, "p"), []string{"FROM; SET launcher | SET model by 1", "SET model"}; !slices.Equal(got, want) {
		t.Errorf("existing: got %q, want %q", got, want)
	}
	if got, want := foldTextExisting(t, src), []string{"FROM; SET launcher; SET model", "SET model"}; !slices.Equal(got, want) {
		t.Errorf("new: got %q, want %q", got, want)
	}
	// Earlier writes do not fold into a CREATE that converges: it reads as
	// a later ALTER, and wins.
	if got, want := foldTextExisting(t, "CREATE PLAYBOOK IF NOT EXISTS p SET model = 'a';\nALTER PLAYBOOK p SET model = 'b';", "p"), []string{"SET model", "SET model"}; !slices.Equal(got, want) {
		t.Errorf("IF NOT EXISTS: got %q, want %q", got, want)
	}
	src = "ALTER PLAYBOOK p SET model = 'a';\nCREATE OR ALTER PLAYBOOK p SET model = 'b';"
	if got, want := foldTextExisting(t, src, "p"), []string{"- | SET model by 1", "SET model"}; !slices.Equal(got, want) {
		t.Errorf("earlier ALTER: got %q, want %q", got, want)
	}
	// After a DROP, the CREATE creates, whatever was there before.
	src = "DROP PLAYBOOK p;\nCREATE OR ALTER PLAYBOOK p SET model = 'a';\nALTER PLAYBOOK p SET model = 'b';"
	if got, want := foldTextExisting(t, src, "p"), []string{"", "SET model", "SET model"}; !slices.Equal(got, want) {
		t.Errorf("after a DROP: got %q, want %q", got, want)
	}
}

// TestFoldDirectoryTargets: a recipe applied TO a directory folds per
// directory, apart from a playbook and from another directory.
func TestFoldDirectoryTargets(t *testing.T) {
	stmts, err := grammar.ParseFile("ALTER PLAYBOOK x SET model = 'a';\nALTER PLAYBOOK x SET model = 'b';\nALTER PLAYBOOK x SET model = 'c';\nALTER PLAYBOOK d SET model = 'e';\n")
	if err != nil {
		t.Fatal(err)
	}
	dirs := []string{"/d1", "/d1", "/d2", ""}
	in := make([]located, len(stmts))
	for i, s := range stmts {
		if dirs[i] != "" {
			s.Name, s.Dir = "", dirs[i]
		}
		in[i] = located{file: "f.cpb", path: "/f.cpb", s: s}
	}
	res := foldStatements(in, nil)
	if !res.skip[0] || res.skip[1] || res.skip[2] || res.skip[3] || len(res.over[0]) != 1 || res.over[0][0].by != 1 {
		t.Fatalf("skip=%v over=%v", res.skip, res.over)
	}
}

// TestApplyFoldStackConverges: a base and a child that sets the same keys
// again converge (the second APPLY changes nothing), the base's values are
// never written (the status line's history never sees the base's), and the
// plan names what each statement had overridden and where it is set again.
func TestApplyFoldStackConverges(t *testing.T) {
	root := sandboxDefaultRoot(t)
	writePlaybook(t, root, "p", nil)
	dir := t.TempDir()
	writeCpb(t, dir, "base.cpb", "ALTER PLAYBOOK\n  SET VAR LOG_LEVEL=info TEAM=one\n  SET model = 'base-model'\n  SET statusline.command = 'echo base'\n  ALLOW TOOL 'Bash(x)';\n")
	child := writeCpb(t, dir, "child.cpb", "INCLUDE 'base.cpb';\nALTER PLAYBOOK\n  SET VAR LOG_LEVEL=debug\n  SET model = 'child-model'\n  SET statusline.command = 'echo child'\n  DENY TOOL 'Bash(x)';\n")
	plan, err := apply(t, child, "TO", "p", "--dry-run")
	if err != nil || !strings.Contains(plan, "overridden: SET VAR LOG_LEVEL (set again at "+child+":2)") {
		t.Fatalf("the plain plan does not name what is overridden: %v\n%s", err, plan)
	}
	first, err := apply(t, child, "TO", "p")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first, "overridden: SET VAR LOG_LEVEL (set again at "+child+":2), SET model (set again at "+child+":2)") {
		t.Fatalf("the run does not name what was overridden:\n%s", first)
	}
	second, err := apply(t, child, "TO", "p")
	if err != nil || !strings.Contains(second, " 0 created, 0 changed, 2 unchanged,") {
		t.Fatalf("a stack applied again must change nothing: %v\n%s", err, second)
	}
	v := describePlaybookByName(t, "p")
	vars := map[string]string{}
	for _, x := range v.Vars {
		if x.Value != nil {
			vars[x.Key] = *x.Value
		}
	}
	if vars["LOG_LEVEL"] != "debug" || vars["TEAM"] != "one" || v.Model == nil || *v.Model != "child-model" ||
		len(v.Tools.Allow) != 0 || len(v.Tools.Deny) != 1 || v.Statusline.Command == nil || *v.Statusline.Command != "echo child" {
		t.Fatalf("the child does not win: %+v model=%v allow=%v deny=%v statusline.command=%v", vars, v.Model, v.Tools.Allow, v.Tools.Deny, v.Statusline.Command)
	}
	for _, h := range v.Statusline.History {
		if h.Command == "echo base" {
			t.Fatalf("the base's status line was written: %+v", v.Statusline.History)
		}
	}
	planJSON := captureStdout(t, func() { _ = runStatement([]string{"APPLY", child, "TO", "p", "--dry-run", "--json"}) })
	var rep struct {
		Statements []struct {
			Line       int `json:"line"`
			Overridden []struct {
				Clause string `json:"clause"`
				By     struct {
					File string `json:"file"`
					Line int    `json:"line"`
				} `json:"by"`
			} `json:"overridden"`
		} `json:"statements"`
	}
	if err := json.Unmarshal([]byte(planJSON), &rep); err != nil {
		t.Fatalf("%v\n%s", err, planJSON)
	}
	var base []string
	for _, s := range rep.Statements {
		for _, o := range s.Overridden {
			base = append(base, o.Clause+"@"+filepath.Base(o.By.File)+":"+strconv.Itoa(o.By.Line))
		}
	}
	want := "SET VAR LOG_LEVEL@child.cpb:2 SET model@child.cpb:2 SET statusline.command@child.cpb:2 ALLOW TOOL 'Bash(x)'@child.cpb:2"
	if strings.Join(base, " ") != want {
		t.Fatalf("the plan's overridden entries:\n got %s\nwant %s", strings.Join(base, " "), want)
	}
}

// TestApplyFoldFleetAndFlatRecipe: a fleet file that applies one base to two
// playbooks converges, and so does a single recipe that sets a key twice.
func TestApplyFoldFleetAndFlatRecipe(t *testing.T) {
	root := sandboxDefaultRoot(t)
	writePlaybook(t, root, "a", nil)
	writePlaybook(t, root, "b", nil)
	dir := t.TempDir()
	writeCpb(t, dir, "base.cpb", "ALTER PLAYBOOK SET VAR LEVEL=base SET model = 'base-model';\n")
	writeCpb(t, dir, "a.cpb", "INCLUDE 'base.cpb';\nALTER PLAYBOOK SET VAR LEVEL=a;\n")
	writeCpb(t, dir, "b.cpb", "INCLUDE 'base.cpb';\nALTER PLAYBOOK SET model = 'b-model';\n")
	fleet := writeCpb(t, dir, "fleet.cpb", "USE PLAYBOOK a;\nINCLUDE 'a.cpb';\nUSE PLAYBOOK b;\nINCLUDE 'b.cpb';\n")
	if _, err := apply(t, fleet); err != nil {
		t.Fatal(err)
	}
	if out, err := apply(t, fleet); err != nil || !strings.Contains(out, " 0 created, 0 changed, ") {
		t.Fatalf("the fleet applied again must change nothing: %v\n%s", err, out)
	}
	// Each playbook folds on its own: a keeps the base's model, b the
	// base's level.
	a, b := describePlaybookByName(t, "a"), describePlaybookByName(t, "b")
	level := func(v playbookJSON) string {
		for _, x := range v.Vars {
			if x.Key == "LEVEL" && x.Value != nil {
				return *x.Value
			}
		}
		return ""
	}
	if a.Model == nil || *a.Model != "base-model" || level(a) != "a" || b.Model == nil || *b.Model != "b-model" || level(b) != "base" {
		t.Fatalf("targets folded into each other: a model=%v level=%s, b model=%v level=%s", a.Model, level(a), b.Model, level(b))
	}
	flat := writeCpb(t, dir, "flat.cpb", "ALTER PLAYBOOK SET model = 'first';\nALTER PLAYBOOK SET model = 'second';\n")
	if _, err := apply(t, flat, "TO", "a"); err != nil {
		t.Fatal(err)
	}
	if out, err := apply(t, flat, "TO", "a"); err != nil || !strings.Contains(out, " 0 created, 0 changed, 2 unchanged,") {
		t.Fatalf("a recipe that sets a key twice must converge: %v\n%s", err, out)
	}
	if m := describePlaybookByName(t, "a").Model; m == nil || *m != "second" {
		t.Fatalf("the last value must win: %v", m)
	}
}

// describePlaybookByName is SHOW PLAYBOOK <name> --json's object.
func describePlaybookByName(t *testing.T, name string) playbookJSON {
	t.Helper()
	pb, err := playbook.Require(config.ResolvePlaybooksDir(), name)
	if err != nil {
		t.Fatal(err)
	}
	return describePlaybook(pb)
}
