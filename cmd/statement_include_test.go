package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCpb(t *testing.T, dir, name, text string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// INCLUDE runs the included file in place; a file reached twice (by two
// INCLUDEs, or by another spelling) runs once.
func TestApplyIncludeStacksFiles(t *testing.T) {
	sandboxDefaultRoot(t)
	t.Setenv("CLAUDE_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	dir, _ := filepath.EvalSymlinks(t.TempDir()) // reports name files by their resolved path
	writeCpb(t, dir, "base.cpb", "CREATE OR REPLACE ENV glm SET MODEL=glm-5.3;\n")
	if err := os.Symlink("base.cpb", filepath.Join(dir, "base-link.cpb")); err != nil {
		t.Fatal(err)
	}
	writeCpb(t, dir, "kommander.cpb", "INCLUDE 'base.cpb';\nCREATE PLAYBOOK IF NOT EXISTS work NO ALIAS;\n")
	top := writeCpb(t, dir, "chaos.cpb", "INCLUDE 'kommander.cpb';\nINCLUDE 'base-link.cpb';\nALTER PLAYBOOK work USE ENV glm;\n")

	out, err := apply(t, top)
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	if !strings.Contains(out, "2 created, 1 changed, 0 unchanged, 0 dropped") {
		t.Fatalf("the shared base ran more than once, or a statement was lost:\n%s", out)
	}
	if !strings.Contains(out, filepath.Join(dir, "base.cpb")+":1: CREATE ENV glm") {
		t.Fatalf("an included statement is not located as file:line:\n%s", out)
	}
}

func TestApplyIncludeRefusals(t *testing.T) {
	sandboxDefaultRoot(t)
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	writeCpb(t, dir, "a.cpb", "CREATE ENV one;\nINCLUDE 'b.cpb';\n")
	writeCpb(t, dir, "b.cpb", "INCLUDE 'a.cpb';\n")
	for text, want := range map[string]string{
		"INCLUDE 'a.cpb';":                     "INCLUDE cycle: ",
		"INCLUDE 'https://example.com/x.cpb';": "INCLUDE takes a local file, not a URL",
		"INCLUDE '.';":                         "INCLUDE takes a regular file",
		"INCLUDE 'missing.cpb';":               "INCLUDE: ",
		"INCLUDE 'a.cpb'; SHOW ENVS;":          "SHOW only reads",
	} {
		root := writeCpb(t, dir, "root.cpb", text+"\n")
		_, err := apply(t, root)
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "nothing was written") {
			t.Errorf("%s: %v, want %q and nothing written", text, err, want)
		}
	}
	if readProfile(t, "one") != nil {
		t.Fatal("a refused run wrote a statement")
	}
	_, err := apply(t, writeCpb(t, dir, "root.cpb", "INCLUDE 'a.cpb';\n"))
	if err == nil || !strings.Contains(err.Error(), "a.cpb -> "+filepath.Join(dir, "b.cpb")+" -> "+filepath.Join(dir, "a.cpb")) {
		t.Errorf("the cycle is not named as a chain: %v", err)
	}
}
