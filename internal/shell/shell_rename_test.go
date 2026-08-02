package shell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

// A rename must update BOTH halves of a generated alias. Rewriting only the
// CLAUDE_CONFIG_DIR path leaves `run <old>` naming a playbook that no longer
// exists: the alias resolves, launches, and dies with "unknown playbook".
func TestRewritePathPrefixRegeneratesCanonicalAlias(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_PLAYBOOKS_DIR", root)
	oldDir := filepath.Join(root, "rtest")
	newDir := filepath.Join(root, "rdone")

	cfg := writeConfig(t, "# claude-playbook: rtest", Format("rtest", oldDir))

	n, skipped, err := RewritePathPrefix(cfg, oldDir, newDir)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || len(skipped) != 0 {
		t.Fatalf("changed=%d skipped=%v, want 1 and none", n, skipped)
	}

	got := readConfig(t, cfg)
	if !strings.Contains(got, Format("rtest", newDir)) {
		t.Fatalf("line is not the canonical form for the new location:\n%s", got)
	}
	if strings.Contains(got, `run '\''rtest'\''`) {
		t.Fatalf("run argument still names the old playbook:\n%s", got)
	}
	// The marker names the playbook too.
	if !strings.Contains(got, "# claude-playbook: rdone") {
		t.Fatalf("marker not updated:\n%s", got)
	}
	// The alias NAME is the user's choice and must survive untouched.
	if !strings.Contains(got, "alias rtest=") {
		t.Fatalf("alias name was rewritten:\n%s", got)
	}
}

// A hand-edited alias is left byte-identical and reported, never guessed at.
// README invites appending Claude Code flags; editing shell-encoded text in
// place is what makes silent corruption possible, so this refuses instead.
func TestRewritePathPrefixRefusesHandEditedAlias(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_PLAYBOOKS_DIR", root)
	oldDir := filepath.Join(root, "work")
	newDir := filepath.Join(root, "job")

	custom := `alias wk='CLAUDE_CONFIG_DIR="` + oldDir + `" cpb run work --model claude-opus-4-6'`
	cfg := writeConfig(t, custom)

	n, skipped, err := RewritePathPrefix(cfg, oldDir, newDir)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("changed=%d, want 0 -- a hand-edited line must not be rewritten", n)
	}
	if len(skipped) != 1 || skipped[0] != "wk" {
		t.Fatalf("skipped=%v, want [wk] so the caller can warn", skipped)
	}
	if !strings.Contains(readConfig(t, cfg), custom) {
		t.Fatalf("hand-edited line was modified:\n%s", readConfig(t, cfg))
	}
}

// A playbooks root whose own path contains " run <name>" must not confuse the
// rewrite. Regenerating from the parsed path cannot be fooled by line contents,
// which is the structural reason this class of bug is gone.
func TestRewritePathPrefixPathContainingRunArg(t *testing.T) {
	root := filepath.Join(t.TempDir(), "team run old")
	t.Setenv("CLAUDE_PLAYBOOKS_DIR", root)
	oldDir := filepath.Join(root, "old")
	newDir := filepath.Join(root, "new")

	cfg := writeConfig(t, Format("x", oldDir))
	if _, _, err := RewritePathPrefix(cfg, oldDir, newDir); err != nil {
		t.Fatal(err)
	}
	got := readConfig(t, cfg)

	if !strings.Contains(got, `CLAUDE_CONFIG_DIR="`+newDir+`"`) {
		t.Fatalf("path corrupted:\n%s", got)
	}
	if strings.Contains(got, "team run new") {
		t.Fatalf("rewrite reached inside the path:\n%s", got)
	}
}

// validateSinglePathSegment rejects only / \ CR LF, so an apostrophe is a legal
// playbook name. Regenerating through Format encodes it correctly; splicing it
// raw ended the single-quoted body and executed everything after it when the
// config was sourced.
func TestRewritePathPrefixEncodesInjectingName(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_PLAYBOOKS_DIR", root)
	oldDir := filepath.Join(root, "victim")
	newDir := filepath.Join(root, `x'; touch PWNED; #`)

	cfg := writeConfig(t, Format("v", oldDir))
	if _, _, err := RewritePathPrefix(cfg, oldDir, newDir); err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(readConfig(t, cfg))

	// Byte-identical to what create would have written for the same name, so the
	// encoding is correct by construction rather than by hand.
	if got != Format("v", newDir) {
		t.Fatalf("not the canonical encoding:\n got: %s\nwant: %s", got, Format("v", newDir))
	}
	// Balanced quoting: an odd count means the body was terminated.
	if strings.Count(got, "'")%2 != 0 {
		t.Fatalf("unbalanced quoting -- body terminated:\n%s", got)
	}
	if strings.Contains(got, `'; touch PWNED`) && !strings.Contains(got, `'\''; touch PWNED`) {
		t.Fatalf("name spliced unescaped:\n%s", got)
	}
}

// An alias generated under a different binary name (installed as cpb, renamed
// later via claude-playbook) is still canonical and must keep its binary name.
func TestRewritePathPrefixPreservesBinaryName(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_PLAYBOOKS_DIR", root)
	oldDir := filepath.Join(root, "a")
	newDir := filepath.Join(root, "b")

	cfg := writeConfig(t, formatWith("x", oldDir, "cpb"))
	n, skipped, err := RewritePathPrefix(cfg, oldDir, newDir)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || len(skipped) != 0 {
		t.Fatalf("changed=%d skipped=%v, want 1 and none", n, skipped)
	}
	if got := strings.TrimSpace(readConfig(t, cfg)); got != formatWith("x", newDir, "cpb") {
		t.Fatalf("binary name not preserved:\n%s", got)
	}
}

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
