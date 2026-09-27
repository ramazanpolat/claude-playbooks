package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsHostCommand(t *testing.T) {
	for cmd, want := range map[string]bool{
		"statusmux render":                        true,
		"/home/p/.local/bin/statusmux render":     true,
		`"$HOME/.local/bin/statusmux" render`:     true,
		"env A=1 statusmux render --config-dir x": true,
		"exec statusmux render":                   true,
		"statusmux":                               false,
		"statusmux preview":                       false,
		"bash statusmux.sh render":                false,
		"echo statusmux render":                   false,
		"":                                        false,
	} {
		if got := isHostCommand(cmd); got != want {
			t.Errorf("isHostCommand(%q) = %v, want %v", cmd, got, want)
		}
	}
}

// A host (statusmux) holds the slot: a recipe's SET STATUSLINE leaves the
// command as it is and warns (statusline_held_by_host), in a real run and a
// dry run alike; REFRESH on it still applies; UNSET takes the slot back.
func TestStatuslineHeldByHost(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	root := seedFlatPlaybook(t, "k")
	host := `"$HOME/.local/bin/statusmux" render`
	data := `{"statusLine": {"type": "command", "command": "\"$HOME/.local/bin/statusmux\" render", "refreshInterval": 10}}`
	if err := os.WriteFile(filepath.Join(root, "settings.json"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	sl := func() map[string]any {
		m, _ := settingsOf(t, root)["statusLine"].(map[string]any)
		return m
	}

	recipe := writePlaybookFile(t, "ALTER PLAYBOOK k SET STATUSLINE 'echo plain';\n")
	rep, _, code := applyJSON(t, recipe, "--dry-run", "--json")
	if code != 0 {
		t.Fatalf("dry run exit %d", code)
	}
	if w, _ := stmts(rep)[0]["warning"].(map[string]any); w == nil || w["code"] != "statusline_held_by_host" {
		t.Fatalf("dry run warning: %v", stmts(rep)[0]["warning"])
	}
	var err error
	var out string
	stderr := captureStderr(t, func() { out, err = apply(t, recipe) })
	if err != nil {
		t.Fatal(err)
	}
	if m := sl(); m["command"] != host || m["refreshInterval"] != float64(10) {
		t.Fatalf("a recipe replaced the host: %v", m)
	}
	if !strings.Contains(out, "0 changed, 1 unchanged") || !strings.Contains(out+stderr, "held by a host") {
		t.Fatalf("report:\n%s\n%s", out, stderr)
	}

	// REFRESH on a host-held slot applies the interval; the command stays.
	if _, err := quotedStmt(t, "ALTER PLAYBOOK k SET STATUSLINE 'echo plain' REFRESH 5"); err != nil {
		t.Fatal(err)
	}
	if m := sl(); m["command"] != host || m["refreshInterval"] != float64(5) {
		t.Fatalf("REFRESH on a host-held slot: %v", m)
	}
	// The host's own command set again is simply unchanged, with no warning.
	if o, err := quotedStmt(t, `ALTER PLAYBOOK k SET STATUSLINE '"$HOME/.local/bin/statusmux" render'`); err != nil || !strings.Contains(o, "unchanged") {
		t.Fatalf("the host's own command: %v\n%s", err, o)
	}

	// UNSET STATUSLINE takes the slot back; then SET applies.
	mustStmt(t, "ALTER PLAYBOOK k UNSET STATUSLINE")
	if _, err := quotedStmt(t, "ALTER PLAYBOOK k SET STATUSLINE 'echo plain'"); err != nil {
		t.Fatal(err)
	}
	if m := sl(); m["command"] != "echo plain" {
		t.Fatalf("after UNSET, SET did not apply: %v", m)
	}
}

// The same holds for a plain config directory (TO '<dir>').
func TestStatuslineHeldByHostOnDir(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	cfg := t.TempDir()
	if err := os.WriteFile(filepath.Join(cfg, "settings.json"), []byte(`{"statusLine":{"type":"command","command":"statusmux render"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	recipe := writePlaybookFile(t, "ALTER PLAYBOOK SET STATUSLINE 'echo plain';\n")
	var err error
	captureStderr(t, func() { _, err = apply(t, recipe, "TO", cfg, "--yes") })
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(cfg, "settings.json"))
	if !strings.Contains(string(data), "statusmux render") || strings.Contains(string(data), "echo plain") {
		t.Fatalf("a recipe replaced the host in a plain directory:\n%s", data)
	}
}
