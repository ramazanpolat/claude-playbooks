package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/play"
)

// playFlags sets cpb play's flags for one call, and resets them after.
func playFlags(t *testing.T, check, dry, asJSON bool, sha string) {
	t.Helper()
	playCheck, playDryRun, playJSONF, playSHA256 = check, dry, asJSON, sha
	t.Cleanup(func() { playCheck, playDryRun, playJSONF, playSHA256 = false, false, false, "" })
}

func writeRecipe(t *testing.T, dir, name, text string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const routerRecipe = "-- title: Router\n-- description: GLM through a router.\n\nALTER PLAYBOOK\n  SET VAR ANTHROPIC_BASE_URL=https://router.example.net/v1 SENTRY_URL=https://sentry.example.com/1\n  SET MODEL 'glm-5.3';\n"

// The playbook play writes first: never the pilot profile, no launcher;
// and when the endpoint moves, a login of its own and the credentials a
// launch would inherit blocked, by name (a shell secret's name included,
// never its value).
func TestPlaySetup(t *testing.T) {
	t.Setenv("MY_SECRET_TOKEN", "do-not-print")
	plain := playSetup("play-x-000000", play.Check([]byte("ALTER PLAYBOOK SET MODEL 'm';\n")))
	if plain != "CREATE PLAYBOOK IF NOT EXISTS play-x-000000 NO ALIAS NO PILOT PROFILE;\n" {
		t.Fatalf("plain: %q", plain)
	}
	moved := playSetup("play-x-000000", play.Check([]byte(routerRecipe)))
	for _, want := range []string{"NO ALIAS NO PILOT PROFILE ISOLATED LOGIN;", "ALTER PLAYBOOK play-x-000000 BLOCK VAR ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN CLAUDE_CODE_OAUTH_TOKEN", "MY_SECRET_TOKEN"} {
		if !strings.Contains(moved, want) {
			t.Errorf("endpoint moved: %q lacks %q", moved, want)
		}
	}
	if strings.Contains(moved, "do-not-print") {
		t.Fatal("a shell secret's value reached the setup")
	}
}

func TestPlayCheckAndDryRun(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	dir := t.TempDir()
	bad := writeRecipe(t, dir, "bad.cpb", "ALTER PLAYBOOK\n  USE ENV work\n  SET VAR GITHUB_TOKEN=abc AS PLAINTEXT;\n")
	good := writeRecipe(t, dir, "router.cpb", routerRecipe)

	// --check: refusals exit 1, with their lines.
	playFlags(t, true, false, false, "")
	var err error
	out := captureStdout(t, func() { err = runPlay(playCmd, []string{bad}) })
	if code, _ := exitCode(err); code != 1 || !strings.Contains(out, "line 2   USE ENV") || !strings.Contains(out, "line 3   SET VAR GITHUB_TOKEN") {
		t.Fatalf("check bad: %v\n%s", err, out)
	}
	out = captureStdout(t, func() { err = runPlay(playCmd, []string{good}) })
	if err != nil || !strings.Contains(out, "→ to run it, you will type: router.example.net") || strings.Count(out, "you will type") != 1 {
		t.Fatalf("check router: %v\n%s", err, out)
	}

	// --sha256 refuses another recipe before anything is shown.
	playFlags(t, true, false, false, "00")
	out = captureStdout(t, func() { err = runPlay(playCmd, []string{good}) })
	if err == nil || !strings.Contains(err.Error(), "refused before anything was shown") || out != "" {
		t.Fatalf("sha256: %v\n%s", err, out)
	}

	// --dry-run --json: APPLY's plan with the play block, against a
	// throwaway store: the user's DEFAULTS never layer in.
	mustStmt(t, "CREATE ENV mine SET ANTHROPIC_BASE_URL=https://mine.example")
	mustStmt(t, "ALTER DEFAULTS USE ENV mine")
	playFlags(t, false, true, true, "")
	out = captureStdout(t, func() { err = runPlay(playCmd, []string{good}) })
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	var rep struct {
		OK         bool `json:"ok"`
		Statements []struct {
			Statement string `json:"statement"`
		} `json:"statements"`
		Play *playJSON `json:"play"`
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !rep.OK || rep.Play == nil || rep.Play.Endpoint != "router.example.net" || !strings.HasPrefix(rep.Play.Playbook, "play-router-") ||
		len(rep.Statements) != 3 || rep.Play.SHA256 == "" || rep.Play.Header.Title != "Router" {
		t.Fatalf("dry run report: %s", out)
	}
	if strings.Contains(out, "mine.example") {
		t.Fatalf("the user's DEFAULTS layered into a played recipe:\n%s", out)
	}
	// The user's store is untouched: no playbook was created there.
	if out := mustStmt(t, "SHOW PLAYBOOKS --json"); strings.Contains(out, "play-router-") {
		t.Fatalf("a dry run left a playbook: %s", out)
	}

	// A refused recipe's dry run is the report with the refusals, exit 1.
	out = captureStdout(t, func() { err = runPlay(playCmd, []string{bad}) })
	if code, _ := exitCode(err); code != 1 || !strings.Contains(out, `"refused": [`) || !strings.Contains(out, `"ok": false`) {
		t.Fatalf("dry run of a refused recipe: %v\n%s", err, out)
	}

	// Running is the next slice: said, nothing done.
	playFlags(t, false, false, false, "")
	if err := runPlay(playCmd, []string{good}); err == nil || !strings.Contains(err.Error(), "--dry-run") {
		t.Fatalf("run: %v", err)
	}
}

// --check on a template directory, as the website's CI runs it: each
// template checked, a title and a description required, and index.txt
// listing exactly the templates, sorted.
func TestPlayCheckDir(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	dir := t.TempDir()
	writeRecipe(t, dir, "router.cpb", routerRecipe)
	writeRecipe(t, dir, "code-reviewer.cpb", "-- title: Reviewer\n-- description: Reads code.\n-- min-cpb: 3.28.0\n\nALTER PLAYBOOK SET MODEL 'opus';\n")
	writeRecipe(t, dir, "index.txt", "code-reviewer\nrouter\n")
	playFlags(t, true, false, false, "")
	var err error
	out := captureStdout(t, func() { err = runPlay(playCmd, []string{dir}) })
	if err != nil || !strings.Contains(out, "ok   index.txt (2 template(s))") || strings.Contains(out, "FAIL") {
		t.Fatalf("a good directory: %v\n%s", err, out)
	}
	writeRecipe(t, dir, "index.txt", "router\ncode-reviewer\n")
	writeRecipe(t, dir, "untitled.cpb", "-- colour: blue\n-- min-cpb: soon\nALTER PLAYBOOK SET MODEL 'x';\n")
	out = captureStdout(t, func() { err = runPlay(playCmd, []string{dir}) })
	for _, want := range []string{"FAIL index.txt", "FAIL untitled.cpb", "needs -- title: and -- description:", "unknown key colour", `min-cpb "soon"`} {
		if !strings.Contains(out, want) {
			t.Errorf("a bad directory lacks %q:\n%s", want, out)
		}
	}
	if code, _ := exitCode(err); code != 1 {
		t.Fatalf("exit %v", err)
	}
}
