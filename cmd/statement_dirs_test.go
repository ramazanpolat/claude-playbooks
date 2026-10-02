package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyToPlainDirectory(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CPB_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	cfg, _ := filepath.EvalSymlinks(t.TempDir()) // stands in for ~/.claude
	if err := os.WriteFile(filepath.Join(cfg, "settings.json"), []byte(`{"theme": "dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	skill := makeSkill(t, filepath.Join(dir, "notes"))
	recipe := writeCpb(t, dir, "agent.cpb", "ALTER PLAYBOOK\n  ALLOW TOOL 'Bash(toolkit-helper *)'\n  SET MODEL 'claude-opus-5-5'\n  SET VAR FOO=bar\n  ADD SKILL notes FROM './notes';\n")

	// Not a terminal and no --yes: refused before anything is written.
	if _, err := apply(t, recipe, "TO", cfg); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("without --yes: %v", err)
	}
	out, err := apply(t, recipe, "TO", cfg, "--dry-run")
	if err != nil || !strings.Contains(out, "back up "+filepath.Join(cfg, "settings.json")) || !strings.Contains(out, "link skills/notes") {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	if _, err := os.Lstat(filepath.Join(cfg, "skills", "notes")); !os.IsNotExist(err) {
		t.Fatal("the dry run wrote")
	}

	if out, err := apply(t, recipe, "TO", cfg, "--yes"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	var s struct {
		Theme       string              `json:"theme"`
		Model       string              `json:"model"`
		Env         map[string]string   `json:"env"`
		Permissions map[string][]string `json:"permissions"`
	}
	data, _ := os.ReadFile(filepath.Join(cfg, "settings.json"))
	if json.Unmarshal(data, &s) != nil || s.Theme != "dark" || s.Model != "claude-opus-5-5" || s.Env["FOO"] != "bar" || s.Permissions["allow"][0] != "Bash(toolkit-helper *)" {
		t.Fatalf("settings: %s", data)
	}
	backups, _ := filepath.Glob(filepath.Join(cfg, "settings.json.cpb-backup-*"))
	if len(backups) != 1 {
		t.Fatalf("backups: %v", backups)
	}
	if b, _ := os.ReadFile(backups[0]); string(b) != `{"theme": "dark"}` {
		t.Fatalf("backup holds %s", b)
	}
	if got, err := os.Readlink(filepath.Join(cfg, "skills", "notes")); err != nil || got != skill {
		t.Fatalf("skill link: %q %v", got, err)
	}
	state, _ := os.ReadFile(filepath.Join(root, ".state", "dirs.toml"))
	if !strings.Contains(string(state), "notes") || strings.Contains(string(state), "FOO") {
		t.Fatalf("dirs.toml: %s", state)
	}
	if _, err := os.Stat(filepath.Join(cfg, ".playbook")); !os.IsNotExist(err) {
		t.Fatal("a manifest was written into the plain directory")
	}

	// Again: nothing changes, and no new backup.
	if out, err := apply(t, recipe, "TO", cfg, "--yes"); err != nil || !strings.Contains(out, "0 changed, 1 unchanged") {
		t.Fatalf("repeat: %v\n%s", err, out)
	}
	if backups, _ := filepath.Glob(filepath.Join(cfg, "settings.json.cpb-backup-*")); len(backups) != 1 {
		t.Fatalf("a no-op backed up again: %v", backups)
	}
}

func TestPlainDirectoryRefusals(t *testing.T) {
	sandboxDefaultRoot(t)
	cfg, _ := filepath.EvalSymlinks(t.TempDir())
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	for text, want := range map[string]string{
		"ALTER PLAYBOOK SET VAR K FROM 'keychain:x';": "secret reference",
		"ALTER PLAYBOOK USE ENV e;":                   "env sets are layered by the launcher",
		"ALTER PLAYBOOK BLOCK VAR K;":                 "launcher's job",
		"ALTER PLAYBOOK SET ISOLATED LOGIN;":          "isolated_login is recorded in a playbook's manifest",
		"ALTER PLAYBOOK SET SANDBOX;":                 "[sandbox] is recorded in a playbook's manifest",
		"ALTER PLAYBOOK UNSET SANDBOX host;":          "[sandbox] is recorded in a playbook's manifest",
		"ALTER PLAYBOOK ADD MCP SERVER s URL 'https://x.example/mcp' HEADER 'Authorization' FROM 'keychain:x';": "only cpb's launcher resolves",
	} {
		f := writeCpb(t, dir, "r.cpb", text+"\n")
		if _, err := apply(t, f, "TO", cfg, "--yes"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", text, err, want)
		}
	}
}

// A skill-only change backs up settings.json too; a dry run plans each
// backup once per run, as the real run makes it; a skill whose record
// cannot be written to cpb's state is taken away again.
func TestPlainDirectoryBackupsAndSkillRecords(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CPB_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	cfg, _ := filepath.EvalSymlinks(t.TempDir())
	if err := os.WriteFile(filepath.Join(cfg, "settings.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	makeSkill(t, filepath.Join(dir, "notes"))

	twice := writeCpb(t, dir, "twice.cpb", "ALTER PLAYBOOK SET MODEL 'a';\nALTER PLAYBOOK SET MODEL 'b';\n")
	out, err := apply(t, twice, "TO", cfg, "--dry-run")
	if err != nil || strings.Count(out, "back up "+filepath.Join(cfg, "settings.json")) != 1 {
		t.Fatalf("dry run backs up once: %v\n%s", err, out)
	}

	skill := writeCpb(t, dir, "skill.cpb", "ALTER PLAYBOOK ADD SKILL notes FROM './notes';\n")
	if os.Geteuid() != 0 {
		state := filepath.Join(root, ".state")
		if err := os.MkdirAll(state, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(state, 0o555); err != nil {
			t.Fatal(err)
		}
		_, err := apply(t, skill, "TO", cfg, "--yes")
		_ = os.Chmod(state, 0o755)
		if err == nil || !strings.Contains(err.Error(), "taken away again") {
			t.Fatalf("state write failure: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(cfg, "skills", "notes")); !os.IsNotExist(err) {
			t.Fatal("an unrecorded skill was left in place")
		}
		for _, b := range mustGlob(t, filepath.Join(cfg, "settings.json.cpb-backup-*")) {
			_ = os.Remove(b)
		}
	}
	if out, err := apply(t, skill, "TO", cfg, "--yes"); err != nil {
		t.Fatalf("skill only: %v\n%s", err, out)
	}
	if b := mustGlob(t, filepath.Join(cfg, "settings.json.cpb-backup-*")); len(b) != 1 {
		t.Fatalf("a skill-only change made no settings backup: %v", b)
	}
}

func mustGlob(t *testing.T, pattern string) []string {
	t.Helper()
	m, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	return m
}
