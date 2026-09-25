package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envprofile"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

func writePlaybookFile(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "playbook.cpb")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// apply runs APPLY on a file; args are its flags.
func apply(t *testing.T, path string, flags ...string) (string, error) {
	t.Helper()
	var err error
	out := captureStdout(t, func() { err = runStatement(append([]string{"APPLY", path}, flags...)) })
	return out, err
}

const scratchPlaybook = `-- a machine from nothing
CREATE OR REPLACE ENV glm
  DESCRIBE 'GLM via the router'
  SET BASE=http://tr0:20128/v1 MODEL=glm-5.3;
ALTER DEFAULTS USE ENV glm;
CREATE PLAYBOOK IF NOT EXISTS work NO ALIAS;
ALTER PLAYBOOK work
  USE ENV glm
  SET VAR MAX_THINKING_TOKENS=8000
  BLOCK VAR HTTP_PROXY;
`

func TestApplyBuildsFromScratchAndConverges(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CLAUDE_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	path := writePlaybookFile(t, scratchPlaybook)

	out, err := apply(t, path, "--dry-run")
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	for _, want := range []string{"created   CREATE ENV glm", "changed   ALTER DEFAULTS", "created   CREATE PLAYBOOK work", "changed   ALTER PLAYBOOK work", "Would apply"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run missing %q:\n%s", want, out)
		}
	}
	if readProfile(t, "glm") != nil {
		t.Fatal("a dry run wrote the env set")
	}
	if _, err := os.Stat(filepath.Join(root, "work")); !os.IsNotExist(err) {
		t.Fatal("a dry run created the playbook")
	}

	if out, err := apply(t, path); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	e := readEnv(t, filepath.Join(root, "work"))
	if strings.Join(e.Profiles, ",") != "glm" || e.Set["MAX_THINKING_TOKENS"] != "8000" || strings.Join(e.Unset, ",") != "HTTP_PROXY" {
		t.Fatalf("applied block: %#v", e)
	}
	if d, _ := envprofile.Defaults(envprofile.Dir(root)); strings.Join(d, ",") != "glm" {
		t.Fatalf("DEFAULTS: %q", d)
	}

	// Applying again changes nothing.
	out, err = apply(t, path)
	if err != nil || !strings.Contains(out, "0 created, 0 changed, 4 unchanged, 0 dropped") {
		t.Fatalf("second apply must change nothing: %v\n%s", err, out)
	}
}

// SHOW CREATE ALL writes a file that APPLY finds already true.
func TestShowCreateAllRoundTrips(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CLAUDE_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	helper, _ := fakeHelper(t)
	if out, err := apply(t, writePlaybookFile(t, scratchPlaybook)); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	mustStmt(t, "ALTER DEFAULTS SET SECRET HELPER "+helper)
	mustStmt(t, "ALTER PLAYBOOK work SET VAR API_TOKEN FROM keychain:ok/work")
	writePlaybook(t, root, "src", &manifest.Manifest{Alias: "s", Source: &manifest.Source{Repository: "https://example.com/s.git", Branch: "v1"}})

	dump := mustStmt(t, "SHOW CREATE ALL")
	for _, want := range []string{"CREATE OR REPLACE ENV glm", "ALTER DEFAULTS\n  USE ENV glm\n  SET SECRET HELPER '" + helper + "';",
		"CREATE PLAYBOOK IF NOT EXISTS src\n  FROM https://example.com/s.git\n  BRANCH v1\n  ALIAS s;",
		"SET VAR API_TOKEN FROM 'keychain:ok/work'"} {
		if !strings.Contains(dump, want) {
			t.Errorf("SHOW CREATE ALL missing %q:\n%s", want, dump)
		}
	}
	before := snapshot(t, root)
	out, err := apply(t, writePlaybookFile(t, dump))
	if err != nil || !strings.Contains(out, " 0 created, 0 changed,") {
		t.Fatalf("applying SHOW CREATE ALL's output must change nothing: %v\n%s", err, out)
	}
	if after := snapshot(t, root); after != before {
		t.Fatalf("files changed:\n%s\n---\n%s", before, after)
	}
}

// snapshot lists every manifest and env-set file with its content.
func snapshot(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if base := filepath.Base(p); base == manifest.FileName || strings.HasSuffix(base, envprofile.FileExt) ||
			base == envprofile.DefaultMarker || base == envprofile.SecretHelperFile {
			data, _ := os.ReadFile(p)
			b.WriteString(p + "\n" + string(data) + "\n")
		}
		return nil
	})
	return b.String()
}

func TestShowCreateWithholdsCredentialLiterals(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	const secret = "sk-live-0123456789abcdef"
	mustStmt(t, "CREATE ENV r SET ANTHROPIC_AUTH_TOKEN="+secret+" MODEL=glm AS PLAINTEXT")
	var err error
	out := captureStdout(t, func() { err = runStatement(words("SHOW CREATE ENV r")) })
	if err == nil || !strings.Contains(err.Error(), "--skip-secrets") {
		t.Fatalf("SHOW CREATE with a credential literal must exit non-zero: %v", err)
	}
	for _, want := range []string{"-- SET ANTHROPIC_AUTH_TOKEN=<withheld> AS PLAINTEXT", "ALTER ENV r SET ANTHROPIC_AUTH_TOKEN FROM '<ref>'", "MODEL=glm"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, secret) || strings.Contains(out, secret[:6]) {
		t.Fatalf("SHOW CREATE printed the secret:\n%s", out)
	}
	if _, err := stmt(t, "SHOW CREATE ENV r --skip-secrets"); err != nil {
		t.Fatalf("--skip-secrets: %v", err)
	}
}

func TestApplyStopsAtTheFirstFailure(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	path := writePlaybookFile(t, "CREATE ENV a;\nALTER PLAYBOOK ghost SET VAR X=1;\nCREATE ENV b;\n")
	_, err := apply(t, path)
	if err == nil || !strings.Contains(err.Error(), path+":2 (ALTER PLAYBOOK ghost)") || !strings.Contains(err.Error(), "applied before it: "+path+" 1 of 3") {
		t.Fatalf("failure report: %v", err)
	}
	if readProfile(t, "a") == nil || readProfile(t, "b") != nil {
		t.Fatal("APPLY must stop at the failing statement, keeping what came before")
	}
}

func TestApplyWritesNothingOnAParseError(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	path := writePlaybookFile(t, "CREATE ENV a;\nSHOW ENVS;\nALTER ENV a FOO;\n")
	_, err := apply(t, path)
	if err == nil || !strings.Contains(err.Error(), "line 2") || !strings.Contains(err.Error(), "line 3") || !strings.Contains(err.Error(), "nothing was written") {
		t.Fatalf("parse errors: %v", err)
	}
	if readProfile(t, "a") != nil {
		t.Fatal("a file with an error wrote its first statement")
	}
}

// A file never consents to DROP PLAYBOOK on its own.
func TestApplyDropsNeedYes(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CLAUDE_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	writePlaybook(t, root, "old", &manifest.Manifest{})
	path := writePlaybookFile(t, "CREATE ENV e;\nDROP PLAYBOOK old;\n")

	_, err := apply(t, path)
	if err == nil || !strings.Contains(err.Error(), path+":2: DROP PLAYBOOK old") || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("a drop without --yes: %v", err)
	}
	if readProfile(t, "e") != nil {
		t.Fatal("a refused file wrote its first statement")
	}
	out, err := apply(t, path, "--dry-run")
	if err != nil || !strings.Contains(out, "dropped   DROP PLAYBOOK old  (deletes "+filepath.Join(config.ResolvePlaybooksDir(), "old")) {
		t.Fatalf("dry run shows the drop: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(root, "old")); err != nil {
		t.Fatal("the dry run deleted the playbook")
	}
	if out, err := apply(t, path, "--yes"); err != nil {
		t.Fatalf("--yes: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(root, "old")); !os.IsNotExist(err) {
		t.Fatal("--yes did not drop")
	}
}

// A CREATE PLAYBOOK IF NOT EXISTS whose source differs from the install's
// is a warning, never an error, and changes nothing.
func TestApplyWarnsOnSourceDrift(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CLAUDE_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	writePlaybook(t, root, "src", &manifest.Manifest{Alias: "s", Source: &manifest.Source{Repository: "https://example.com/s.git", Branch: "v1"}})
	before := snapshot(t, root)

	same := writePlaybookFile(t, "CREATE PLAYBOOK IF NOT EXISTS src FROM https://example.com/s.git BRANCH v1;\n")
	out, err := apply(t, same)
	if err != nil || strings.Contains(out, "warning") {
		t.Fatalf("no drift: %v\n%s", err, out)
	}

	drift := writePlaybookFile(t, "CREATE PLAYBOOK IF NOT EXISTS src FROM https://example.com/s.git BRANCH v2;\n")
	out, err = apply(t, drift, "--dry-run")
	want := "WARNING: PLAYBOOK src exists; source differs (installed https://example.com/s.git branch v1, file says https://example.com/s.git branch v2)"
	if err != nil || !strings.Contains(out, want) || !strings.Contains(out, "1 warning(s)") {
		t.Fatalf("dry run with drift: %v\n%s", err, out)
	}
	out, err = apply(t, drift)
	if err != nil || !strings.Contains(out, "0 created, 0 changed, 1 unchanged, 0 dropped, 1 warning(s)") {
		t.Fatalf("drift must warn and exit 0: %v\n%s", err, out)
	}
	if after := snapshot(t, root); after != before {
		t.Fatal("a drift warning changed a file")
	}
}

// Several files: all validated first, then run in order; a failure reports
// how much of each file was applied.
func TestApplySeveralFiles(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CLAUDE_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	base := writePlaybookFile(t, "CREATE OR REPLACE ENV base SET A=1;\n")
	machine := writePlaybookFile(t, "ALTER DEFAULTS USE ENV base;\nCREATE PLAYBOOK IF NOT EXISTS work NO ALIAS;\n")
	broken := writePlaybookFile(t, "CREATE ENV other;\nALTER ENV other FOO;\n")

	// A later file that does not parse: nothing from the first is written.
	if _, err := apply(t, base, broken); err == nil || !strings.Contains(err.Error(), "nothing was written") {
		t.Fatalf("a broken second file: %v", err)
	}
	if readProfile(t, "base") != nil {
		t.Fatal("the first file was applied although the second does not parse")
	}

	// In order: the second file uses what the first creates, in a dry run too.
	if out, err := apply(t, base, machine, "--dry-run"); err != nil {
		t.Fatalf("dry run across files: %v\n%s", err, out)
	}
	if out, err := apply(t, base, machine); err != nil {
		t.Fatalf("apply across files: %v\n%s", err, out)
	}
	if d, _ := envprofile.Defaults(envprofile.Dir(root)); strings.Join(d, ",") != "base" {
		t.Fatalf("DEFAULTS: %q", d)
	}

	// A failure in the second file reports each file's count.
	failing := writePlaybookFile(t, "CREATE ENV more;\nALTER PLAYBOOK ghost SET VAR X=1;\n")
	_, err := apply(t, base, failing)
	if err == nil || !strings.Contains(err.Error(), base+" 1 of 1, "+failing+" 1 of 2") {
		t.Fatalf("per-file counts: %v", err)
	}

	// --yes covers drops in any file, and a drop is listed with file:line.
	drop := writePlaybookFile(t, "DROP PLAYBOOK work;\n")
	if _, err := apply(t, base, drop); err == nil || !strings.Contains(err.Error(), drop+":1: DROP PLAYBOOK work") {
		t.Fatalf("a drop in the second file: %v", err)
	}
	if _, err := apply(t, base, drop, "--yes"); err != nil {
		t.Fatalf("--yes across files: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "work")); !os.IsNotExist(err) {
		t.Fatal("the drop in the second file did not run")
	}
}
