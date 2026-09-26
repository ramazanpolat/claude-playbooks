package cmd

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

var hiddenNames = []string{"env", "env-profile", "create", "link", "delete", "rename", "alias", "dealias", "list", "info"}

// The pre-grammar commands are gone from help and completion, and every
// one of them is still registered and runnable.
func TestPreGrammarCommandsAreHidden(t *testing.T) {
	for _, name := range hiddenNames {
		c, _, err := rootCmd.Find([]string{name})
		if err != nil || c.Name() != name {
			t.Fatalf("%s is no longer registered: %v", name, err)
		}
		if !c.Hidden {
			t.Errorf("%s is visible", name)
		}
	}
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	defer rootCmd.SetOut(nil)
	if err := rootCmd.Help(); err != nil {
		t.Fatal(err)
	}
	help := out.String()
	for _, name := range hiddenNames {
		if strings.Contains(help, "\n  "+name+" ") {
			t.Errorf("--help lists %s:\n%s", name, help)
		}
	}
	for _, name := range []string{"install", "run", "start", "update", "auth", "self-uninstall"} {
		if !strings.Contains(help, "\n  "+name+" ") {
			t.Errorf("--help lost %s:\n%s", name, help)
		}
	}
	if !strings.Contains(help, "cpb APPLY <file>") {
		t.Errorf("--help does not name the statements:\n%s", help)
	}
}

// A hidden command's output is unchanged: with stderr not a terminal (a
// script, a test) the hint is never printed, and stdout never carries it.
func TestHiddenCommandPrintsNoHintOffTerminal(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	seedFlatPlaybook(t, "p")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldErr := os.Stderr
	os.Stderr = w
	var stdout string
	func() {
		defer func() { os.Stderr = oldErr }()
		rootCmd.SetArgs([]string{"list"})
		defer rootCmd.SetArgs(nil)
		stdout = captureStdout(t, func() {
			if err := rootCmd.Execute(); err != nil {
				t.Errorf("list: %v", err)
			}
		})
	}()
	w.Close()
	var stderr bytes.Buffer
	_, _ = stderr.ReadFrom(r)
	if strings.Contains(stdout+stderr.String(), "hidden command") {
		t.Fatalf("a hint was printed off a terminal:\nstdout: %s\nstderr: %s", stdout, stderr.String())
	}
	if !strings.Contains(stdout, "p") {
		t.Fatalf("list output changed:\n%s", stdout)
	}
}

// The hint names the statement for the arguments given, and never a value.
func TestGrammarForm(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"create", "x", "--alias", "xx", "--sandbox"}, "cpb CREATE PLAYBOOK x ALIAS xx SANDBOX"},
		{[]string{"link", "/src/dev", "--no-alias"}, "cpb CREATE PLAYBOOK dev LINK /src/dev NO ALIAS"},
		{[]string{"delete", "x", "--yes"}, "cpb DROP PLAYBOOK x --yes"},
		{[]string{"rename", "a", "b", "--alias", "bb"}, "cpb ALTER PLAYBOOK a RENAME TO b ALIAS bb"},
		{[]string{"alias", "x", "--remove"}, "cpb ALTER PLAYBOOK x NO ALIAS"},
		{[]string{"alias", "x", "y"}, "cpb ALTER PLAYBOOK x ALIAS y"},
		{[]string{"dealias", "x"}, "cpb ALTER PLAYBOOK x NO ALIAS"},
		{[]string{"list"}, "cpb SHOW PLAYBOOKS"},
		{[]string{"info", "x"}, "cpb SHOW PLAYBOOK x"},
		{[]string{"env", "x"}, "cpb EXPLAIN PLAYBOOK x"},
		{[]string{"env", "x", "set", "TOKEN=sk-live-secret", "A=1"}, "cpb ALTER PLAYBOOK x SET VAR TOKEN=<value> A=<value>"},
		{[]string{"env", "x", "unset", "HTTP_PROXY"}, "cpb ALTER PLAYBOOK x BLOCK VAR HTTP_PROXY"},
		{[]string{"env", "x", "use", "a", "b"}, "cpb ALTER PLAYBOOK x ADD ENV a ADD ENV b"},
		{[]string{"env-profile"}, "cpb SHOW ENVS"},
		{[]string{"env-profile", "p", "describe", "secret words"}, "cpb ALTER ENV p DESCRIBE '<text>'"},
		{[]string{"env-profile", "p", "default"}, "cpb ALTER DEFAULTS USE ENV p"},
	}
	for _, tc := range cases {
		c, rest, err := rootCmd.Find(tc.args)
		if err != nil {
			t.Fatalf("%q: %v", tc.args, err)
		}
		c.Flags().VisitAll(func(f *pflag.Flag) { f.Changed = false })
		if err := c.ParseFlags(rest); err != nil {
			t.Fatalf("%q: %v", tc.args, err)
		}
		got := grammarForm(c, c.Flags().Args())
		c.Flags().VisitAll(func(f *pflag.Flag) { _ = f.Value.Set(f.DefValue); f.Changed = false })
		if got != tc.want {
			t.Errorf("%q:\n got %s\nwant %s", tc.args, got, tc.want)
		}
		if strings.Contains(got, "sk-live-secret") || strings.Contains(got, "secret words") {
			t.Errorf("%q: the hint echoes a value: %s", tc.args, got)
		}
	}
}
