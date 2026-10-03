package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envset"
	"github.com/ramazanpolat/claude-playbooks/internal/launcher"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// unreadableFixture is a registry of two playbooks: good, with its launcher
// and the env set e, and bad, whose manifest cannot be read (a key cpb
// does not define, on line 2). It returns the root and bad's file:line.
func unreadableFixture(t *testing.T) (root, badAt string) {
	t.Helper()
	root = sandboxDefaultRoot(t)
	t.Setenv("CPB_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	writePlaybook(t, root, "good", &manifest.Manifest{})
	if _, err := launcher.Write(config.LauncherDir, "good"); err != nil {
		t.Fatal(err)
	}
	mustStmt(t, "CREATE ENV e SET A=1")
	mustStmt(t, "ALTER PLAYBOOK good USE ENV e")
	bad := filepath.Join(root, "bad")
	if err := os.MkdirAll(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, manifest.FileName), []byte("name = \"bad\"\nbogus_key = \"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, filepath.Join(bad, manifest.FileName) + ":2"
}

// TestUnreadableManifestIsContained: one playbook whose manifest cannot be
// read takes nothing else down (SPEC.md, "Unreadable manifests").
func TestUnreadableManifestIsContained(t *testing.T) {
	_, badAt := unreadableFixture(t)
	leftOutLine := `playbook "bad" is left out: unknown key "bogus_key" in ` + badAt
	const (
		works   = "works"   // exit 0, and the unreadable one is not mentioned
		refused = "refused" // the error names the unreadable file and line
		list    = "list"    // the readable rows, a stderr line, exit 1
	)
	cases := []struct {
		stmt, kind, want string
	}{
		// Named, on the readable playbook.
		{"SHOW PLAYBOOK good", works, "good"},
		{"SHOW PLAYBOOK good --json", works, `"launcher": "good"`},
		{"EXPLAIN PLAYBOOK good", works, "ENV e"},
		{"SHOW CREATE PLAYBOOK good", works, "CREATE PLAYBOOK IF NOT EXISTS good"},
		{"ALTER PLAYBOOK good SET VAR B=2", works, ""},
		// Named, on the unreadable one: its read error.
		{"SHOW PLAYBOOK bad", refused, badAt},
		{"EXPLAIN PLAYBOOK bad", refused, badAt},
		{"SHOW CREATE PLAYBOOK bad", refused, badAt},
		{"ALTER PLAYBOOK bad SET VAR B=2", refused, badAt},
		{"DROP PLAYBOOK bad --yes", refused, badAt},
		{"CREATE PLAYBOOK bad", refused, badAt},
		// Lists.
		{"SHOW PLAYBOOKS", list, "good"},
		{"SHOW PLAYBOOKS --json", list, `"name": "good"`},
		{"SELECT name FROM PLAYBOOKS", list, "good"},
		{"SELECT playbook, key FROM VARS", list, "good"},
		{"SELECT name, used_by FROM ENVS", list, "good"},
		{"SHOW ENVS", list, "good"},
		{"SHOW ENV e --json", list, `"good"`},
		{"SHOW SESSIONS", list, ""},
		// What needs every manifest: refused, naming the file.
		{"SHOW CREATE ALL", refused, badAt},
		{"CREATE PLAYBOOK fresh", refused, badAt},
		{"ALTER PLAYBOOK good RENAME TO other", refused, badAt},
		{"ALTER PLAYBOOK good LAUNCHER g2", refused, badAt},
		{"DROP ENV e", refused, badAt},
		// No playbook read.
		{"CREATE ENV f SET C=3", works, ""},
	}
	for _, c := range cases {
		t.Run(c.stmt, func(t *testing.T) {
			var out string
			var err error
			errOut := captureStderr(t, func() { out, err = stmt(t, c.stmt) })
			switch c.kind {
			case works:
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				if strings.Contains(out+errOut, "bad") {
					t.Fatalf("mentions the unreadable playbook:\n%s%s", out, errOut)
				}
				if !strings.Contains(out, c.want) {
					t.Fatalf("output lacks %q:\n%s", c.want, out)
				}
			case refused:
				if err == nil || !strings.Contains(err.Error(), c.want) {
					t.Fatalf("want a refusal naming %s, got %v", c.want, err)
				}
			case list:
				if code, ok := exitCode(err); !ok || code != 1 {
					t.Fatalf("want exit 1, got %v", err)
				}
				if !strings.Contains(out, c.want) {
					t.Fatalf("the readable rows are missing %q:\n%s", c.want, out)
				}
				if strings.Count(errOut, "\n") != 1 || !strings.Contains(errOut, leftOutLine) {
					t.Fatalf("want one stderr line %q, got:\n%s", leftOutLine, errOut)
				}
				if strings.HasSuffix(c.stmt, "--json") && !json.Valid([]byte(out)) {
					t.Fatalf("--json is not JSON:\n%s", out)
				}
			}
		})
	}
	if _, err := os.Stat(filepath.Join(envset.Dir(config.ResolvePlaybooksDir()), "e.toml")); err != nil {
		t.Fatalf("DROP ENV e beside an unreadable playbook removed the set: %v", err)
	}
}

// TestUnreadableManifestJSONKeepsItsShape: --json of a list holds the
// readable playbooks only; no error rows.
func TestUnreadableManifestJSONKeepsItsShape(t *testing.T) {
	unreadableFixture(t)
	var out string
	captureStderr(t, func() { out, _ = stmt(t, "SHOW PLAYBOOKS --json") })
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 1 || rows[0]["name"] != "good" {
		t.Fatalf("%v %v\n%s", err, rows, out)
	}
}

// TestUnreadableManifestDispatch: every launcher but the unreadable
// playbook's resolves, and a name no readable playbook claims is not
// called stale while a manifest cannot be read.
func TestUnreadableManifestDispatch(t *testing.T) {
	root, badAt := unreadableFixture(t)
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	resolve := func(name string) (string, bool, error) {
		os.Args = []string{name}
		return multicallPlaybook()
	}
	if name, ok, err := resolve("good"); err != nil || !ok || name != "good" {
		t.Fatalf("good: %q %v %v", name, ok, err)
	}
	if _, _, err := resolve("bad"); err == nil || !strings.Contains(err.Error(), badAt) {
		t.Fatalf("bad: %v", err)
	}
	_, ok, err := resolve("x")
	if ok || err == nil || !strings.Contains(err.Error(), "no readable playbook claims it") || !strings.Contains(err.Error(), badAt) || strings.Contains(err.Error(), "stale") {
		t.Fatalf("x: %v %v", ok, err)
	}
	if err := os.RemoveAll(filepath.Join(root, "bad")); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := resolve("x"); ok || err != nil {
		t.Fatalf("x once every manifest reads: %v %v", ok, err)
	}
}

// TestUnreadableManifestBareAndAuth: bare cpb and auth status are lists.
func TestUnreadableManifestBareAndAuth(t *testing.T) {
	resetCommandTestState(t)
	_, badAt := unreadableFixture(t)
	var out string
	var err error
	errOut := captureStderr(t, func() { out = captureStdout(t, func() { err = runRoot(nil, nil) }) })
	if code, ok := exitCode(err); !ok || code != 1 || !strings.Contains(out, "good") || !strings.Contains(errOut, badAt) {
		t.Fatalf("bare cpb: %v\n%s%s", err, out, errOut)
	}
	authStatusJSON = true
	t.Cleanup(func() { authStatusJSON = false })
	errOut = captureStderr(t, func() { out = captureStdout(t, func() { err = runAuthStatus(nil, nil) }) })
	if code, ok := exitCode(err); !ok || code != 1 || !strings.Contains(out, `"name": "good"`) || !strings.Contains(errOut, badAt) {
		t.Fatalf("auth status: %v\n%s%s", err, out, errOut)
	}
}

// TestUnreadableManifestSelfUninstall: the root is removed as a whole, so a
// manifest that cannot be read refuses it, and nothing is removed.
func TestUnreadableManifestSelfUninstall(t *testing.T) {
	resetCommandTestState(t)
	root, badAt := unreadableFixture(t)
	selfUninstallYes, selfUninstallDryRun = true, true // dry-run too: a missed refusal must not remove anything
	err := runSelfUninstall(nil, nil)
	if err == nil || !strings.Contains(err.Error(), "nothing removed") || !strings.Contains(err.Error(), badAt) {
		t.Fatalf("self-uninstall: %v", err)
	}
	for _, p := range []string{filepath.Join(root, "good"), filepath.Join(root, "bad")} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
	if _, exists, _ := launcher.Lookup(config.LauncherDir, "good"); !exists {
		t.Fatal("a launcher was removed")
	}
}

// TestUnreadableManifestApply: a statement that would claim a launcher name
// refuses before anything is written; the rest applies.
func TestUnreadableManifestApply(t *testing.T) {
	root, badAt := unreadableFixture(t)
	dir := t.TempDir()
	claim := writeCpb(t, dir, "claim.cpb", "CREATE ENV g SET D=4;\nCREATE PLAYBOOK fresh;\n")
	if _, err := apply(t, claim); err == nil || !strings.Contains(err.Error(), "nothing was written") || !strings.Contains(err.Error(), badAt) {
		t.Fatalf("a launcher claim: %v", err)
	}
	if _, err := os.Stat(filepath.Join(envset.Dir(root), "g.toml")); !os.IsNotExist(err) {
		t.Fatal("a statement ran before the refusal")
	}
	plain := writeCpb(t, dir, "plain.cpb", "ALTER PLAYBOOK good SET VAR E=5;\n")
	if _, err := apply(t, plain); err != nil {
		t.Fatalf("a file on the readable playbook: %v", err)
	}
}
