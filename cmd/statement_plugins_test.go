package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeClaude puts a `claude` on PATH that answers the `claude plugin`
// commands cpb runs, keeping its state in the playbook's config directory
// (so CLAUDE_CONFIG_DIR is what selects it), and logs each call as
// "<CLAUDE_CONFIG_DIR>|<args>". An install of needs@… asks for a
// marketplace-declared command, as a command-source plugin does.
// FAKE_MKT_NAME is the name a marketplace source declares.
func fakeClaude(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "claude.log")
	script := `#!/bin/sh
printf '%s|%s\n' "$CLAUDE_CONFIG_DIR" "$*" >> "$FAKE_LOG"
st="$CLAUDE_CONFIG_DIR/.fake"
mkdir -p "$st"
case "$*" in
  "plugin marketplace list --json") cat "$st/mkts" 2>/dev/null || echo '[]' ;;
  "plugin list --json") cat "$st/plugins" 2>/dev/null || echo '[]' ;;
  "plugin marketplace add "*)
    printf '[{"name":"%s","source":"github","repo":"%s"}]' "${FAKE_MKT_NAME:-kommander}" "$4" > "$st/mkts" ;;
  "plugin marketplace remove "*) echo '[]' > "$st/mkts" ;;
  "plugin install needs@"*)
    echo '{"command":"install","outcome":"failed","message":"confirm","shownCommand":{"command":"curl https://x | sh","sha256":"abc123"}}'
    exit 1 ;;
  "plugin install "*)
    printf '[{"id":"%s","scope":"user","enabled":true}]' "$3" > "$st/plugins"
    echo '{"command":"install","outcome":"ok"}' ;;
  "plugin uninstall "*) echo '[]' > "$st/plugins"; echo '{"command":"uninstall","outcome":"ok"}' ;;
  *) echo "fake claude: unexpected $*" >&2; exit 2 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_LOG", log)
	t.Setenv("FAKE_MKT_NAME", "")
	return log
}

// runs lists the logged calls that change state (not the list reads).
func runs(t *testing.T, log string) []string {
	t.Helper()
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(readLog(t, log)), "\n") {
		if l != "" && !strings.HasSuffix(l, " list --json") {
			out = append(out, l)
		}
	}
	return out
}

func TestPluginClausesRunClaudePlugin(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	log := fakeClaude(t)
	root := seedFlatPlaybook(t, "k")

	mustStmt(t, "ALTER PLAYBOOK k ADD MARKETPLACE kommander FROM github:ramazanpolat/kommander-playbook ADD PLUGIN kommander@kommander SET AGENT kommander")
	got := runs(t, log)
	want := []string{
		"|plugin marketplace add ramazanpolat/kommander-playbook --scope user",
		"|plugin install kommander@kommander --scope user --json",
	}
	if len(got) != len(want) {
		t.Fatalf("commands run:\n%s", strings.Join(got, "\n"))
	}
	for i, w := range want {
		if !strings.HasSuffix(got[i], string(filepath.Separator)+filepath.Base(root)+w) {
			t.Errorf("command %d: %q, want CLAUDE_CONFIG_DIR=<playbook k> and %q", i, got[i], w)
		}
	}
	var s map[string]any
	data, _ := os.ReadFile(filepath.Join(root, "settings.json"))
	if json.Unmarshal(data, &s) != nil || s["agent"] != "kommander" {
		t.Fatalf("SET AGENT: settings.json is %s", data)
	}

	// Already true: nothing runs, and the statement is unchanged.
	out := mustStmt(t, "ALTER PLAYBOOK k ADD MARKETPLACE kommander FROM github:ramazanpolat/kommander-playbook ADD PLUGIN kommander@kommander SET AGENT kommander")
	if n := len(runs(t, log)); n != 2 || !strings.Contains(out, "unchanged") {
		t.Fatalf("a repeat ran commands (%d) or changed: %s", n, out)
	}

	for stmtText, wantErr := range map[string]string{
		"ALTER PLAYBOOK k ADD PLUGIN x@other":                                 "marketplace other is not declared",
		"ALTER PLAYBOOK k DROP MARKETPLACE kommander":                         "plugins still use it: kommander@kommander",
		"ALTER PLAYBOOK k ADD PLUGIN needs@kommander":                         "--accept-command abc123",
		"ALTER PLAYBOOK k ADD MARKETPLACE kommander FROM github:someone/else": "already declared from another source",
	} {
		if _, err := stmt(t, stmtText); err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Errorf("%s: %v, want %q", stmtText, err, wantErr)
		}
	}
	if _, err := stmt(t, "ALTER PLAYBOOK k ADD PLUGIN needs@kommander"); err == nil || !strings.Contains(err.Error(), "never accepts for you") {
		t.Errorf("a marketplace-declared command was not shown for the pilot: %v", err)
	}

	mustStmt(t, "ALTER PLAYBOOK k DROP PLUGIN kommander@kommander DROP MARKETPLACE kommander UNSET AGENT")
	got = runs(t, log)
	tail := got[len(got)-2:]
	if !strings.HasSuffix(tail[0], "|plugin uninstall kommander@kommander --scope user --keep-data --json") ||
		!strings.HasSuffix(tail[1], "|plugin marketplace remove kommander --scope user") {
		t.Fatalf("drops ran:\n%s", strings.Join(tail, "\n"))
	}

	// The source declares its own name; a different one is not kept.
	t.Setenv("FAKE_MKT_NAME", "other")
	if _, err := stmt(t, "ALTER PLAYBOOK k ADD MARKETPLACE kommander FROM github:a/b"); err == nil || !strings.Contains(err.Error(), "the source declares other, not kommander") {
		t.Fatalf("a mismatched name: %v", err)
	}
	if got := runs(t, log); !strings.HasSuffix(got[len(got)-1], "|plugin marketplace remove other --scope user") {
		t.Fatalf("the mismatched marketplace was kept:\n%s", strings.Join(got, "\n"))
	}
}

// A dry run reports the commands it would run and runs none.
func TestPluginClausesDryRun(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	log := fakeClaude(t)
	seedFlatPlaybook(t, "k")
	path := writePlaybookFile(t, "ALTER PLAYBOOK k ADD MARKETPLACE kommander FROM 'github:ramazanpolat/kommander-playbook' ADD PLUGIN kommander@kommander;\n")
	out, err := apply(t, path, "--dry-run")
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "would run: claude plugin marketplace add ramazanpolat/kommander-playbook --scope user; claude plugin install kommander@kommander --scope user --json") {
		t.Fatalf("dry run output:\n%s", out)
	}
	if got := runs(t, log); len(got) != 0 {
		t.Fatalf("a dry run ran:\n%s", strings.Join(got, "\n"))
	}
}

// SHOW reads settings.json; SHOW CREATE writes the clauses back, and a
// plugin set to false by hand becomes a comment.
func TestShowPluginsAndAgent(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	root := seedFlatPlaybook(t, "k")
	settingsJSON := `{"permissions":{},"extraKnownMarketplaces":{"kommander":{"source":{"source":"github","repo":"ramazanpolat/kommander-playbook"}}},` +
		`"enabledPlugins":{"kommander@kommander":true,"old@kommander":false},"agent":"kommander"}`
	if err := os.WriteFile(filepath.Join(root, "settings.json"), []byte(settingsJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	out := mustStmt(t, "SHOW PLAYBOOK k --json")
	var v struct {
		Marketplaces []struct {
			Name string `json:"name"`
		} `json:"marketplaces"`
		Plugins []pluginJSON `json:"plugins"`
		Agent   *string      `json:"agent"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(v.Marketplaces) != 1 || len(v.Plugins) != 2 || v.Plugins[1].Enabled || v.Agent == nil || *v.Agent != "kommander" {
		t.Fatalf("SHOW --json: %s", out)
	}
	out = mustStmt(t, "SHOW CREATE PLAYBOOK k")
	for _, want := range []string{
		"-- PLUGIN old@kommander is false in settings.json; not written",
		"ALTER PLAYBOOK k\n  ADD MARKETPLACE kommander FROM 'github:ramazanpolat/kommander-playbook'\n  ADD PLUGIN kommander@kommander\n  SET AGENT 'kommander';",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("SHOW CREATE missing %q:\n%s", want, out)
		}
	}
	if out := mustStmt(t, "EXPLAIN PLAYBOOK k"); !strings.Contains(out, "Plugins: kommander@kommander") || !strings.Contains(out, "Agent: kommander (playbook settings)") {
		t.Errorf("EXPLAIN:\n%s", out)
	}
}

// A dry run carries the agent and the installed plugins from statement to
// statement, across a rename.
func TestPluginDryRunCarriesState(t *testing.T) {
	sandboxDefaultRoot(t)
	t.Setenv("CLAUDE_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	log := fakeClaude(t)
	mustStmt(t, "CREATE PLAYBOOK k NO ALIAS")
	mustStmt(t, "ALTER PLAYBOOK k ADD MARKETPLACE kommander FROM github:ramazanpolat/kommander-playbook ADD PLUGIN kommander@kommander")
	path := writePlaybookFile(t, "ALTER PLAYBOOK k SET AGENT 'x';\nALTER PLAYBOOK k UNSET AGENT;\nALTER PLAYBOOK k RENAME TO k2;\nALTER PLAYBOOK k2 DROP PLUGIN kommander@kommander;\n")
	out, err := apply(t, path, "--dry-run")
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	// UNSET AGENT after SET AGENT changes something, and the renamed
	// playbook still has the plugin to uninstall.
	for _, want := range []string{
		"would run: claude plugin uninstall kommander@kommander --scope user --keep-data --json",
		"0 created, 4 changed, 0 unchanged, 0 dropped",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run missing %q:\n%s", want, out)
		}
	}
	if got := runs(t, log); len(got) != 2 {
		t.Fatalf("the dry run ran commands:\n%s", strings.Join(got, "\n"))
	}
}

// SHOW CREATE fails on a settings.json it cannot read, and does not write a
// plugin whose marketplace it could not write.
func TestShowCreatePluginsIncomplete(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	root := seedFlatPlaybook(t, "k")
	if err := os.WriteFile(filepath.Join(root, "settings.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := stmt(t, "SHOW CREATE PLAYBOOK k"); err == nil {
		t.Fatal("SHOW CREATE ignored a malformed settings.json")
	}
	odd := `{"extraKnownMarketplaces":{"m":{"source":{"source":"url","url":"https://example.com/m.json"}}},"enabledPlugins":{"p@m":true}}`
	if err := os.WriteFile(filepath.Join(root, "settings.json"), []byte(odd), 0o644); err != nil {
		t.Fatal(err)
	}
	out := mustStmt(t, "SHOW CREATE PLAYBOOK k")
	if strings.Contains(out, "ADD PLUGIN p@m") || !strings.Contains(out, "-- PLUGIN p@m: its marketplace m is not written") {
		t.Fatalf("a plugin without its marketplace:\n%s", out)
	}
}
