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
  "plugin marketplace add https://"*|"plugin marketplace add git@"*)
    # As Claude Code does: url and #ref recorded apart.
    u="${4%%#*}"; ref=""
    case "$4" in *#*) ref="${4#*#}" ;; esac
    if [ -n "$ref" ]; then
      printf '[{"name":"%s","source":"git","url":"%s","ref":"%s"}]' "${FAKE_MKT_NAME:-toolkit}" "$u" "$ref" > "$st/mkts"
    else
      printf '[{"name":"%s","source":"git","url":"%s"}]' "${FAKE_MKT_NAME:-toolkit}" "$u" > "$st/mkts"
    fi ;;
  "plugin marketplace add "*)
    # owner/repo#ref: repo and ref apart, as Claude Code records them.
    repo="${4%%#*}"
    case "$4" in
      *#*) printf '[{"name":"%s","source":"github","repo":"%s","ref":"%s"}]' "${FAKE_MKT_NAME:-toolkit}" "$repo" "${4#*#}" > "$st/mkts" ;;
      *) printf '[{"name":"%s","source":"github","repo":"%s"}]' "${FAKE_MKT_NAME:-toolkit}" "$repo" > "$st/mkts" ;;
    esac ;;
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

// runs lists the logged calls that change state (not the list reads, nor
// the --version probe).
func runs(t *testing.T, log string) []string {
	t.Helper()
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(readLog(t, log)), "\n") {
		if l != "" && !strings.HasSuffix(l, " list --json") && !strings.HasSuffix(l, "|--version") {
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

	mustStmt(t, "ALTER PLAYBOOK k ADD MARKETPLACE toolkit FROM github:example/toolkit ADD PLUGIN toolkit@toolkit SET AGENT toolkit")
	got := runs(t, log)
	want := []string{
		"|plugin marketplace add example/toolkit --scope user",
		"|plugin install toolkit@toolkit --scope user --json",
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
	if json.Unmarshal(data, &s) != nil || s["agent"] != "toolkit" {
		t.Fatalf("SET AGENT: settings.json is %s", data)
	}

	// Already true: nothing runs, and the statement is unchanged.
	out := mustStmt(t, "ALTER PLAYBOOK k ADD MARKETPLACE toolkit FROM github:example/toolkit ADD PLUGIN toolkit@toolkit SET AGENT toolkit")
	if n := len(runs(t, log)); n != 2 || !strings.Contains(out, "unchanged") {
		t.Fatalf("a repeat ran commands (%d) or changed: %s", n, out)
	}

	for stmtText, wantErr := range map[string]string{
		"ALTER PLAYBOOK k ADD PLUGIN x@other":                               "marketplace other is not declared",
		"ALTER PLAYBOOK k DROP MARKETPLACE toolkit":                         "plugins still use it: toolkit@toolkit",
		"ALTER PLAYBOOK k ADD PLUGIN needs@toolkit":                         "--accept-command abc123",
		"ALTER PLAYBOOK k ADD MARKETPLACE toolkit FROM github:someone/else": "already declared from another source",
	} {
		if _, err := stmt(t, stmtText); err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Errorf("%s: %v, want %q", stmtText, err, wantErr)
		}
	}
	if _, err := stmt(t, "ALTER PLAYBOOK k ADD PLUGIN needs@toolkit"); err == nil || !strings.Contains(err.Error(), "never accepts for you") {
		t.Errorf("a marketplace-declared command was not shown for the pilot: %v", err)
	}

	mustStmt(t, "ALTER PLAYBOOK k DROP PLUGIN toolkit@toolkit DROP MARKETPLACE toolkit UNSET AGENT")
	got = runs(t, log)
	tail := got[len(got)-2:]
	if !strings.HasSuffix(tail[0], "|plugin uninstall toolkit@toolkit --scope user --keep-data --json") ||
		!strings.HasSuffix(tail[1], "|plugin marketplace remove toolkit --scope user") {
		t.Fatalf("drops ran:\n%s", strings.Join(tail, "\n"))
	}

	// The source declares its own name; a different one is not kept.
	t.Setenv("FAKE_MKT_NAME", "other")
	if _, err := stmt(t, "ALTER PLAYBOOK k ADD MARKETPLACE toolkit FROM github:a/b"); err == nil || !strings.Contains(err.Error(), "the source declares other, not toolkit") {
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
	path := writePlaybookFile(t, "ALTER PLAYBOOK k ADD MARKETPLACE toolkit FROM 'github:example/toolkit' ADD PLUGIN toolkit@toolkit;\n")
	out, err := apply(t, path, "--dry-run")
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "would run: claude plugin marketplace add example/toolkit --scope user; claude plugin install toolkit@toolkit --scope user --json") {
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
	settingsJSON := `{"permissions":{},"extraKnownMarketplaces":{"toolkit":{"source":{"source":"github","repo":"example/toolkit"}}},` +
		`"enabledPlugins":{"toolkit@toolkit":true,"old@toolkit":false},"agent":"toolkit"}`
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
	if len(v.Marketplaces) != 1 || len(v.Plugins) != 2 || v.Plugins[1].Enabled || v.Agent == nil || *v.Agent != "toolkit" {
		t.Fatalf("SHOW --json: %s", out)
	}
	out = mustStmt(t, "SHOW CREATE PLAYBOOK k")
	for _, want := range []string{
		"-- PLUGIN old@toolkit is false in settings.json; not written",
		"ALTER PLAYBOOK k\n  ADD MARKETPLACE toolkit FROM 'github:example/toolkit'\n  ADD PLUGIN toolkit@toolkit\n  SET AGENT 'toolkit';",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("SHOW CREATE missing %q:\n%s", want, out)
		}
	}
	if out := mustStmt(t, "EXPLAIN PLAYBOOK k"); !strings.Contains(out, "Plugins: toolkit@toolkit") || !strings.Contains(out, "Agent: toolkit (playbook settings)") {
		t.Errorf("EXPLAIN:\n%s", out)
	}
}

// A dry run carries the agent and the installed plugins from statement to
// statement, across a rename.
func TestPluginDryRunCarriesState(t *testing.T) {
	sandboxDefaultRoot(t)
	t.Setenv("CPB_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	log := fakeClaude(t)
	mustStmt(t, "CREATE PLAYBOOK k NO LAUNCHER")
	mustStmt(t, "ALTER PLAYBOOK k ADD MARKETPLACE toolkit FROM github:example/toolkit ADD PLUGIN toolkit@toolkit")
	path := writePlaybookFile(t, "ALTER PLAYBOOK k SET AGENT 'x';\nALTER PLAYBOOK k UNSET AGENT;\nALTER PLAYBOOK k RENAME TO k2;\nALTER PLAYBOOK k2 DROP PLUGIN toolkit@toolkit;\n")
	out, err := apply(t, path, "--dry-run")
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	// UNSET AGENT after SET AGENT changes something, and the renamed
	// playbook still has the plugin to uninstall.
	for _, want := range []string{
		"would run: claude plugin uninstall toolkit@toolkit --scope user --keep-data --json",
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

// A git source with #ref: Claude Code records url and ref apart, so an
// unchanged source reads as unchanged, and a changed ref is still refused.
func TestMarketplaceGitRefSource(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	log := fakeClaude(t)
	seedFlatPlaybook(t, "k")
	quoted := func(line string) (string, error) {
		var err error
		out := captureStdout(t, func() { err = runStatement([]string{line}) })
		return out, err
	}
	add := "ALTER PLAYBOOK k ADD MARKETPLACE toolkit FROM 'https://example.com/toolkit.git#v1.2.0'"
	if _, err := quoted(add); err != nil {
		t.Fatal(err)
	}
	if got := runs(t, log); len(got) != 1 || !strings.HasSuffix(got[0], "|plugin marketplace add https://example.com/toolkit.git#v1.2.0 --scope user") {
		t.Fatalf("add ran:\n%s", strings.Join(got, "\n"))
	}
	out, err := quoted(add)
	if err != nil || !strings.Contains(out, "unchanged") || len(runs(t, log)) != 1 {
		t.Fatalf("an unchanged #ref source: %v\n%s", err, out)
	}
	for _, other := range []string{"https://example.com/toolkit.git#v1.3.0", "https://example.com/toolkit.git", "https://example.com/other.git#v1.2.0"} {
		if _, err := quoted("ALTER PLAYBOOK k ADD MARKETPLACE toolkit FROM '" + other + "'"); err == nil || !strings.Contains(err.Error(), "already declared from another source") {
			t.Errorf("%s: %v, want the refusal", other, err)
		}
	}
}

// SHOW CREATE writes a git source's ref back as url#ref.
func TestSourceStringGitRef(t *testing.T) {
	for raw, want := range map[string]string{
		`{"source":"git","url":"https://example.com/k.git","ref":"v1"}`: "https://example.com/k.git#v1",
		`{"source":"git","url":"https://example.com/k.git"}`:            "https://example.com/k.git",
		`{"source":"github","repo":"a/b"}`:                              "github:a/b",
		`{"source":"github","repo":"a/b","ref":"v1"}`:                   "github:a/b#v1",
	} {
		if got, ok := sourceString(json.RawMessage(raw)); !ok || got != want {
			t.Errorf("%s: %q %v, want %q", raw, got, ok, want)
		}
	}
	for _, raw := range []string{
		`{"source":"git","url":"https://example.com/k.git","ref":""}`,
		`{"source":"github","repo":"a/b#x"}`,
		`{"source":"github","repo":"a/b","ref":"0123abc"}`,
		`{"source":"github","repo":"a/b","ref":""}`,
		`{"source":"git","url":"https://example.com/k.git#x","ref":"v1"}`,
		`{"source":"git","url":"https://example.com/k.git#x"}`,
	} {
		if got, ok := sourceString(json.RawMessage(raw)); ok {
			t.Errorf("%s: written as %q, want not written", raw, got)
		}
	}
}

// A plan that installs or uninstalls a plugin is refused, in one line, when
// claude is older than the first version with `plugin install --json`; a
// current or unknown version runs, and a plan without install runs anyway.
func TestPluginClausesNeedClaudeVersion(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	log := fakeClaude(t)
	seedFlatPlaybook(t, "k")
	old := claudeVersion
	t.Cleanup(func() { claudeVersion = old })

	claudeVersion = func() string { return "2.1.245" }
	_, err := stmt(t, "ALTER PLAYBOOK k ADD MARKETPLACE toolkit FROM github:example/toolkit ADD PLUGIN toolkit@toolkit")
	if err == nil || !strings.Contains(err.Error(), "need Claude Code 2.1.268 or newer") || !strings.Contains(err.Error(), "this claude is 2.1.245") {
		t.Fatalf("an old claude: %v", err)
	}
	if got := runs(t, log); len(got) != 0 {
		t.Fatalf("commands ran before the refusal: %v", got)
	}
	// No install or uninstall in the plan: the version does not matter.
	if _, err := stmt(t, "ALTER PLAYBOOK k ADD MARKETPLACE toolkit FROM github:example/toolkit"); err != nil {
		t.Fatalf("marketplace only: %v", err)
	}
	claudeVersion = func() string { return "2.1.99" } // numbers, not strings: 99 < 268
	if err := checkClaudeForPlugins([]pluginStep{{args: []string{"install", "p@m"}}}); err == nil {
		t.Error("2.1.99 was let through")
	}
	for out, want := range map[string]string{"2.1.283 (Claude Code)": "2.1.283", "Claude Code 2.1.268": "2.1.268",
		"v2.1.99\n": "2.1.99", "claude: not a version": ""} {
		if got := parseClaudeVersion(out); got != want {
			t.Errorf("parseClaudeVersion(%q) = %q, want %q", out, got, want)
		}
	}
	for _, v := range []string{"2.1.268", "2.1.283", ""} {
		claudeVersion = func() string { return v }
		if err := checkClaudeForPlugins([]pluginStep{{args: []string{"install", "p@m"}}}); err != nil {
			t.Errorf("version %q: %v", v, err)
		}
	}
}

// 'github:<owner>/<repo>#<ref>' (or @<ref>) passes owner/repo#ref to
// claude, compares repo and ref as Claude Code records them, and SHOW
// CREATE writes it back so that APPLY changes nothing (v3.27.0). A ref that
// looks like a commit is refused before anything runs.
func TestPluginGitHubRef(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	log := fakeClaude(t)
	root := seedFlatPlaybook(t, "k")
	t.Setenv("FAKE_MKT_NAME", "m")

	mustStmt(t, "ALTER PLAYBOOK k ADD MARKETPLACE m FROM github:a/b#v1.2.0")
	if got := runs(t, log); len(got) != 1 || !strings.HasSuffix(got[0], "|plugin marketplace add a/b#v1.2.0 --scope user") {
		t.Fatalf("add with a tag ran:\n%s", strings.Join(got, "\n"))
	}
	// The same source, as # or @: unchanged, nothing runs.
	for _, src := range []string{"github:a/b#v1.2.0", "github:a/b@v1.2.0"} {
		if out := mustStmt(t, "ALTER PLAYBOOK k ADD MARKETPLACE m FROM "+src); !strings.Contains(out, "unchanged") || len(runs(t, log)) != 1 {
			t.Fatalf("%s again: %s", src, out)
		}
	}
	// Another ref, or none, is another source.
	for _, src := range []string{"github:a/b#v2", "github:a/b"} {
		if _, err := stmt(t, "ALTER PLAYBOOK k ADD MARKETPLACE m FROM "+src); err == nil || !strings.Contains(err.Error(), "already declared from another source") {
			t.Errorf("%s: %v", src, err)
		}
	}
	// A commit is refused before anything runs.
	if _, err := stmt(t, "ALTER PLAYBOOK k ADD MARKETPLACE n FROM github:a/b#0123abcd"); err == nil || !strings.Contains(err.Error(), "Claude Code clones marketplaces by branch or tag; a commit cannot be pinned") {
		t.Fatalf("a SHA ref: %v", err)
	}
	if n := len(runs(t, log)); n != 1 {
		t.Fatalf("a refused SHA ran a command (%d)", n)
	}

	// SHOW CREATE writes the ref back (from settings.json, as Claude Code
	// records it), and APPLY of that output changes nothing.
	settingsJSON := `{"extraKnownMarketplaces":{"m":{"source":{"source":"github","repo":"a/b","ref":"v1.2.0"}}}}`
	if err := os.WriteFile(filepath.Join(root, "settings.json"), []byte(settingsJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	created := mustStmt(t, "SHOW CREATE PLAYBOOK k")
	if !strings.Contains(created, "ADD MARKETPLACE m FROM 'github:a/b#v1.2.0'") {
		t.Fatalf("SHOW CREATE lost the ref:\n%s", created)
	}
	path := writePlaybookFile(t, created)
	if out := mustStmt(t, "APPLY "+path+" --yes"); !strings.Contains(out, " 0 created, 0 changed,") {
		t.Fatalf("APPLY of SHOW CREATE changed something:\n%s", out)
	}
	if n := len(runs(t, log)); n != 1 {
		t.Fatalf("the round trip ran a command (%d)", n)
	}
}

// A git source whose #ref looks like a commit is accepted, as before
// v3.27.0, and warned about (marketplace_ref_not_cloneable): Claude Code
// clones a marketplace by branch or tag, so it would not clone. The warning
// names the marketplace and the ref, never the URL; a tag is not warned
// about.
func TestMarketplaceGitRefLooksLikeCommit(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	fakeClaude(t)
	seedFlatPlaybook(t, "k")
	t.Setenv("FAKE_MKT_NAME", "gitmkt")
	const url = "https://git.example/secret-path/m.git"

	recipe := writePlaybookFile(t, "ALTER PLAYBOOK k ADD MARKETPLACE gitmkt FROM '"+url+"#0123abcd';\n")
	rep, _, code := applyJSON(t, recipe, "--dry-run", "--json")
	if code != 0 {
		t.Fatalf("dry run exit %d: %v", code, rep)
	}
	ws, _ := stmts(rep)[0]["warnings"].([]any)
	if len(ws) != 1 {
		t.Fatalf("dry run warnings: %v", stmts(rep)[0]["warnings"])
	}
	w, _ := ws[0].(map[string]any)
	if w == nil || w["code"] != "marketplace_ref_not_cloneable" {
		t.Fatalf("dry run warning: %v", w)
	}
	msg, _ := w["message"].(string)
	if !strings.Contains(msg, "MARKETPLACE gitmkt #0123abcd: Claude Code clones marketplaces by branch or tag; this ref looks like a commit and will not clone: use a tag at that commit") || strings.Contains(msg, "secret-path") {
		t.Fatalf("warning message: %q", msg)
	}
	// The statement still runs, as before, and warns on stderr.
	var err error
	stderr := captureStderr(t, func() { _, err = stmt(t, "ALTER PLAYBOOK k ADD MARKETPLACE gitmkt FROM "+url+"#0123abcd") })
	if err != nil || !strings.Contains(stderr, "marketplaces by branch or tag; this ref looks like a commit") {
		t.Fatalf("a commit ref on a git source: %v\n%s", err, stderr)
	}
	// A tag, or no ref, is not warned about.
	for _, src := range []string{url + "#v1.2.0", url} {
		rep, _, _ := applyJSON(t, writePlaybookFile(t, "ALTER PLAYBOOK k ADD MARKETPLACE gitmkt FROM '"+src+"';\n"), "--dry-run", "--json")
		if ws, _ := stmts(rep)[0]["warnings"].([]any); len(ws) != 0 {
			t.Errorf("%s: warned %v", src, ws)
		}
	}
	// Two in one statement are two warnings, each with its code, and the
	// summary counts both.
	rep, _, code = applyJSON(t, writePlaybookFile(t, "ALTER PLAYBOOK k ADD MARKETPLACE gitmkt FROM '"+url+"#0123abcd' ADD MARKETPLACE other FROM '"+url+"#89abcdef';\n"), "--dry-run", "--json")
	if code != 0 {
		t.Fatalf("two commit refs: exit %d: %v", code, rep)
	}
	ws, _ = stmts(rep)[0]["warnings"].([]any)
	if len(ws) != 2 {
		t.Fatalf("two commit refs: warnings %v", ws)
	}
	for i, name := range []string{"gitmkt #0123abcd", "other #89abcdef"} {
		w, _ := ws[i].(map[string]any)
		if msg, _ := w["message"].(string); w["code"] != "marketplace_ref_not_cloneable" || !strings.HasPrefix(msg, "MARKETPLACE "+name+":") {
			t.Errorf("warning %d: %v", i, w)
		}
	}
	if sum, _ := rep["summary"].(map[string]any); sum["warnings"] != float64(2) {
		t.Errorf("summary: %v", rep["summary"])
	}
}
