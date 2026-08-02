package shell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConfig seeds a shell config containing exactly the given lines.
func writeConfig(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "shellrc")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func readConfig(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A rename must update BOTH halves of the alias. Before the fix only the
// CLAUDE_CONFIG_DIR path was rewritten, leaving `run <old>` naming a playbook
// that no longer existed -- the alias resolved, launched, and died with
// "unknown playbook".
func TestRewritePathPrefixUpdatesRunArgument(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_PLAYBOOKS_DIR", root)
	oldDir := filepath.Join(root, "rtest")
	newDir := filepath.Join(root, "rdone")

	cfg := writeConfig(t, "# claude-playbook: rtest", Format("rtest", oldDir))

	n, err := RewritePathPrefix(cfg, oldDir, newDir)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("rewrote %d lines, want 1", n)
	}

	got := readConfig(t, cfg)
	if !strings.Contains(got, newDir) {
		t.Fatalf("path not updated:\n%s", got)
	}
	if strings.Contains(got, "run '\\''rtest'\\''") {
		t.Fatalf("run argument still names the old playbook:\n%s", got)
	}
	if !strings.Contains(got, "run '\\''rdone'\\''") {
		t.Fatalf("run argument not rewritten to the new name:\n%s", got)
	}
	// The generated line must be byte-identical to what Format would emit for
	// the new location, with the alias name preserved.
	want := Format("rtest", newDir)
	if !strings.Contains(got, want) {
		t.Fatalf("rewritten line != canonical form\n got: %s\nwant: %s", got, want)
	}
}

// README documents appending Claude Code flags to a generated alias. A rename
// must not discard them -- which regenerating the line wholesale would.
func TestRewritePathPrefixPreservesUserFlags(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_PLAYBOOKS_DIR", root)
	oldDir := filepath.Join(root, "work")
	newDir := filepath.Join(root, "job")

	custom := `alias work='CLAUDE_CONFIG_DIR="` + oldDir + `" cpb run work --model claude-opus-4-6 --permission-mode auto'`
	cfg := writeConfig(t, custom)

	if _, err := RewritePathPrefix(cfg, oldDir, newDir); err != nil {
		t.Fatal(err)
	}
	got := readConfig(t, cfg)

	if !strings.Contains(got, "--model claude-opus-4-6 --permission-mode auto") {
		t.Fatalf("user flags were discarded:\n%s", got)
	}
	// The argument is re-encoded rather than spliced raw, so its quoting is
	// normalised to the canonical escaped form -- still a single shell word.
	if !strings.Contains(got, `run '\''job'\''`) {
		t.Fatalf("run argument not rewritten:\n%s", got)
	}
	if strings.Contains(got, "run work") {
		t.Fatalf("old playbook name survived:\n%s", got)
	}
}

// The rewrite must not fire on a token that merely starts with the old name.
func TestRewriteRunArgRefusesPartialMatch(t *testing.T) {
	line := `alias a='CLAUDE_CONFIG_DIR="/x/demo" cpb run demo-staging'`
	if out, changed := rewriteRunArg(line, "demo", "lab", '\''); changed {
		t.Fatalf("rewrote a partial match: %s", out)
	}
}

// Every quoting form the tool or a hand edit can produce must be handled.
func TestRewriteRunArgQuotingForms(t *testing.T) {
	// Every input form is recognised; the output is normalised to a single
	// correctly quoted word for the body context (0 = unquoted body here).
	for _, tc := range []struct{ in, want string }{
		{`x run '\''old'\'' y`, `x run 'new' y`},
		{`x run 'old' y`, `x run 'new' y`},
		{`x run "old" y`, `x run 'new' y`},
		{`x run old y`, `x run 'new' y`},
		{`x run old`, `x run 'new'`},
	} {
		got, changed := rewriteRunArg(tc.in, "old", "new", 0)
		if !changed || got != tc.want {
			t.Errorf("rewriteRunArg(%q) = %q (changed=%v), want %q", tc.in, got, changed, tc.want)
		}
	}
}

// The `# claude-playbook: <name>` marker Write emits above each alias names the
// playbook too, so a rename must update it for the same reason as the run
// argument. Nothing parses the name, but a marker naming a playbook that no
// longer exists is misleading in a file users are invited to edit.
func TestRewritePathPrefixUpdatesMarkerComment(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_PLAYBOOKS_DIR", root)
	oldDir := filepath.Join(root, "m1")
	newDir := filepath.Join(root, "m2")

	cfg := writeConfig(t, "# claude-playbook: m1", Format("m1", oldDir))
	if _, err := RewritePathPrefix(cfg, oldDir, newDir); err != nil {
		t.Fatal(err)
	}
	got := readConfig(t, cfg)

	if strings.Contains(got, "# claude-playbook: m1") {
		t.Fatalf("marker still names the old playbook:\n%s", got)
	}
	if !strings.Contains(got, "# claude-playbook: m2") {
		t.Fatalf("marker not updated:\n%s", got)
	}
	// The alias NAME is user-chosen and must survive a rename untouched.
	if !strings.Contains(got, "alias m1=") {
		t.Fatalf("alias name was rewritten; it is the user's choice:\n%s", got)
	}
}

// Indentation and unrelated comments must survive the marker rewrite.
func TestRewriteMarkerCommentLeavesOtherLinesAlone(t *testing.T) {
	if got := rewriteMarkerComment("# some other comment", "x"); got != "# some other comment" {
		t.Fatalf("rewrote an unrelated comment: %q", got)
	}
	if got := rewriteMarkerComment("    # claude-playbook: old", "new"); got != "    # claude-playbook: new" {
		t.Fatalf("indentation not preserved: %q", got)
	}
	if got := rewriteMarkerComment("# claude-playbook: old", ""); got != "# claude-playbook: old" {
		t.Fatalf("empty name should be a no-op: %q", got)
	}
}

// The run-argument rewrite must be confined to the command text after the
// CLAUDE_CONFIG_DIR value. A playbooks root whose own path contains " run
// <name>" would otherwise match inside the path first: the directory gets
// corrupted, the real argument is left stale, and the alias becomes
// undiscoverable by path (every lookup parses CLAUDE_CONFIG_DIR).
func TestRewritePathPrefixDoesNotMatchInsideThePath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "team run old")
	t.Setenv("CLAUDE_PLAYBOOKS_DIR", root)
	oldDir := filepath.Join(root, "old")
	newDir := filepath.Join(root, "new")

	// Bare (hand-edited / legacy) form, which is what makes the collision reachable.
	cfg := writeConfig(t, `alias x='CLAUDE_CONFIG_DIR="`+oldDir+`" cpb run old'`)
	if _, err := RewritePathPrefix(cfg, oldDir, newDir); err != nil {
		t.Fatal(err)
	}
	got := readConfig(t, cfg)

	if !strings.Contains(got, `CLAUDE_CONFIG_DIR="`+newDir+`"`) {
		t.Fatalf("config dir corrupted by the run-argument rewrite:\n got: %s\nwant path: %s", got, newDir)
	}
	if !strings.Contains(got, `run '\''new'\''`) {
		t.Fatalf("run argument not rewritten:\n%s", got)
	}
	if strings.Contains(got, "team run new") {
		t.Fatalf("rewrite reached inside the path:\n%s", got)
	}
}

// A playbook name is not a safe shell token. validateSinglePathSegment rejects
// only / \ CR LF, so an apostrophe is a legal name -- and splicing one raw into
// the single-quoted alias body ends the quote, making everything after it
// execute when the config is sourced. Names reach rename from the CLI and from
// a .playbook manifest inside an installed repo.
func TestRewritePathPrefixEscapesInjectingName(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_PLAYBOOKS_DIR", root)
	evil := `x'; touch PWNED; #`
	oldDir := filepath.Join(root, "victim")
	newDir := filepath.Join(root, evil)

	cfg := writeConfig(t, Format("v", oldDir))
	if _, err := RewritePathPrefix(cfg, oldDir, newDir); err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(readConfig(t, cfg))

	// Every apostrophe introduced by the name must be escaped, so the body's
	// quoting is still balanced. An odd count means the body was terminated.
	if strings.Count(got, "'")%2 != 0 {
		t.Fatalf("unbalanced quoting -- body was terminated:\n%s", got)
	}
	// The payload must not appear as bare shell text outside a quoted string.
	if strings.Contains(got, `'; touch PWNED`) && !strings.Contains(got, `'\''; touch PWNED`) {
		t.Fatalf("name spliced unescaped:\n%s", got)
	}
}

func TestEscapeForBody(t *testing.T) {
	if got := escapeForBody(`a'b`, '\''); got != `a'\''b` {
		t.Errorf("single-quote context: got %q", got)
	}
	if got := escapeForBody(`a"b$c`, '"'); got != `a\"b\$c` {
		t.Errorf("double-quote context: got %q", got)
	}
	if got := escapeForBody(`a'b`, 0); got != `a'b` {
		t.Errorf("unquoted context must pass through: got %q", got)
	}
}
