package cmd

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/launcher"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// The launcher, the model and the agent are properties: CREATE … SET gives
// them in one step (the settings.json ones by an ALTER of the new playbook,
// within the statement), ALTER … SET changes them, and RESET puts them back
// to the default.
func TestLauncherModelAgentProperties(t *testing.T) {
	root := sandboxDefaultRoot(t)
	t.Setenv("CPB_LAUNCHER_RECEIPT", filepath.Join(t.TempDir(), "launchers"))
	has := func(cmd string) bool {
		_, exists, _ := launcher.Lookup(config.LauncherDir, cmd)
		return exists
	}

	out := mustStmt(t, "CREATE PLAYBOOK pp SET launcher = 'p2', model = 'claude-opus-5-5', agent = 'reviewer'")
	if !has("p2") || has("pp") {
		t.Fatalf("CREATE … SET launcher: p2=%v pp=%v\n%s", has("p2"), has("pp"), out)
	}
	s := settingsOf(t, filepath.Join(root, "pp"))
	if s["model"] != "claude-opus-5-5" || s["agent"] != "reviewer" {
		t.Fatalf("CREATE … SET model, agent: %v", s)
	}

	// RESET launcher: back to the playbook's own name, which it prints.
	if out := mustStmt(t, "ALTER PLAYBOOK pp RESET launcher"); !strings.Contains(out, "Launcher: pp") {
		t.Fatalf("RESET launcher does not name the launcher it wrote:\n%s", out)
	}
	if has("p2") || !has("pp") {
		t.Fatalf("RESET launcher: p2=%v pp=%v", has("p2"), has("pp"))
	}
	if m, _ := manifest.Read(filepath.Join(root, "pp")); m != nil && m.Launcher != "" {
		t.Fatalf("RESET launcher left %q recorded", m.Launcher)
	}

	// RESET model, agent: the keys go.
	mustStmt(t, "ALTER PLAYBOOK pp RESET model, agent")
	s = settingsOf(t, filepath.Join(root, "pp"))
	if _, ok := s["model"]; ok {
		t.Fatalf("RESET model: %v", s)
	}
	if _, ok := s["agent"]; ok {
		t.Fatalf("RESET agent: %v", s)
	}

	// launcher = '<its own name>' is the default: nothing recorded.
	mustStmt(t, "CREATE PLAYBOOK qq SET launcher = 'qq'")
	if m, _ := manifest.Read(filepath.Join(root, "qq")); m != nil && m.Launcher != "" {
		t.Fatalf("launcher = its own name was recorded: %q", m.Launcher)
	}
	if !has("qq") {
		t.Fatal("the default launcher was not written")
	}

	// SHOW CREATE writes the default launcher as RESET launcher (D2: a key
	// at its default is RESET; a name may be a keyword, which is no launcher
	// value), and the round trip holds.
	created := mustStmt(t, "SHOW CREATE PLAYBOOK qq")
	if !strings.Contains(created, "RESET launcher") || strings.Contains(created, "launcher = 'qq'") {
		t.Fatalf("SHOW CREATE does not write the default launcher as RESET launcher:\n%s", created)
	}
	if out, err := apply(t, writePlaybookFile(t, created)); err != nil || !strings.Contains(out, " 0 created, 0 changed,") {
		t.Fatalf("SHOW CREATE did not re-apply unchanged: %v\n%s", err, out)
	}

	// The launcher combines with another key (D1); RENAME TO stands alone.
	mustStmt(t, "ALTER PLAYBOOK qq SET launcher = 'q3' SET model = 'm'")
	if _, err := stmt(t, "ALTER PLAYBOOK qq RENAME TO qr SET model = 'n'"); err == nil || !strings.Contains(err.Error(), "use two statements") {
		t.Fatalf("RENAME TO with another clause: %v", err)
	}
}

// CREATE … LINK takes the launcher only: the target's manifest and
// settings.json are its own.
func TestLinkRefusesSettingsProperties(t *testing.T) {
	sandboxDefaultRoot(t)
	for line, want := range map[string]string{
		"CREATE PLAYBOOK l LINK /x SET model = 'm'":                       "model does not apply to LINK",
		"CREATE PLAYBOOK l LINK /x SET agent = 'a'":                       "agent does not apply to LINK",
		"CREATE PLAYBOOK l LINK /x SET global_instructions = 'inherited'": "global_instructions does not apply to LINK",
	} {
		if _, err := quotedStmt(t, line); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", line, err, want)
		}
	}
}
