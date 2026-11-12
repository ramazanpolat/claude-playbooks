package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/auth"
	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envset"
)

// A playbook cpb kept before these names were reserved stays readable and
// repairable. `cpb play --keep` of an earlier release wrote the shell's
// credential-looking names into a kept playbook's block list, these among
// them, and SET VAR could set the helper.
func TestKeptPlaybookWithReservedNamesStaysRepairable(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	t.Setenv(envset.SecretHelperEnv, "")
	root := seedFlatPlaybook(t, "kept")
	file := filepath.Join(root, ".playbook")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(file, []byte("version = \"1.0.0\"\nname = \"kept\"\n\n"+body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// A block list: read, shown and launched without the reserved names,
	// which the next write drops.
	write("[env]\nblock = [\"AWS_ACCESS_KEY_ID\", \"CPB_SECRET_FILE_STORE\", \"CPB_SECRET_HELPER\", \"GITHUB_TOKEN\"]\n")
	show := mustStmt(t, "SHOW CREATE PLAYBOOK kept")
	if !strings.Contains(show, "AWS_ACCESS_KEY_ID") || strings.Contains(show, "CPB_SECRET") {
		t.Fatalf("SHOW CREATE:\n%s", show)
	}
	eff, err := auth.EffectiveBlock(root, nil)
	if err != nil {
		t.Fatalf("launch block: %v", err)
	}
	for _, k := range eff.Block {
		if strings.HasPrefix(k, "CPB_SECRET") {
			t.Fatalf("launch block holds %s", k)
		}
	}
	mustStmt(t, "ALTER PLAYBOOK kept SET VAR EDITOR=vi AS PLAINTEXT")
	if data, _ := os.ReadFile(file); strings.Contains(string(data), "CPB_SECRET") || !strings.Contains(string(data), "GITHUB_TOKEN") {
		t.Fatalf("after a write:\n%s", data)
	}

	// A value: read and shown; a launch is refused, naming the file and the
	// statement that removes it, and that statement works.
	write("[env.set]\nCPB_SECRET_HELPER = \"/usr/local/bin/h\"\n")
	if out := mustStmt(t, "SHOW PLAYBOOK kept"); !strings.Contains(out, "CPB_SECRET_HELPER") {
		t.Fatalf("SHOW PLAYBOOK:\n%s", out)
	}
	_, err = auth.EffectiveBlock(root, nil)
	if !errors.Is(err, envset.ErrSet) || !strings.Contains(err.Error(), file) || !strings.Contains(err.Error(), "cpb ALTER PLAYBOOK kept UNSET VAR CPB_SECRET_HELPER") {
		t.Fatalf("launch with a reserved value: %v", err)
	}
	mustStmt(t, "ALTER PLAYBOOK kept UNSET VAR CPB_SECRET_HELPER")
	if _, err := auth.EffectiveBlock(root, nil); err != nil {
		t.Fatalf("after UNSET VAR: %v", err)
	}

	// A reference too, and DROP PLAYBOOK works over it.
	write("[env.refs]\nCPB_SECRET_HELPER = \"keychain:h\"\n")
	if _, err := auth.EffectiveBlock(root, nil); !errors.Is(err, envset.ErrSet) || !strings.Contains(err.Error(), "env.refs") {
		t.Fatalf("launch with a reserved reference: %v", err)
	}
	mustStmt(t, "DROP PLAYBOOK kept --yes")
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("DROP PLAYBOOK left %s: %v", root, err)
	}
}

// An env set holding a reserved name stays readable and repairable the same
// way: SHOW ENVS lists every set, a launch that uses it is refused naming
// the set's file and the statement that removes the name, and ALTER ENV …
// UNSET, CREATE OR REPLACE ENV and DROP ENV work on it.
func TestEnvSetWithReservedNamesStaysRepairable(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	t.Setenv(envset.SecretHelperEnv, "")
	dir := envset.Dir(config.ResolvePlaybooksDir())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) string {
		t.Helper()
		p := envset.Path(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	file := write("e", "block = [\"CPB_SECRET_FILE_STORE\"]\n\n[set]\nCPB_SECRET_HELPER = \"/usr/local/bin/h\"\nEDITOR = \"vi\"\n")
	write("other", "[set]\nPAGER = \"less\"\n")
	if out := mustStmt(t, "SHOW ENVS"); !strings.Contains(out, "other") || !strings.Contains(out, "e") {
		t.Fatalf("SHOW ENVS:\n%s", out)
	}
	root := seedFlatPlaybook(t, "p")
	mustStmt(t, "ALTER PLAYBOOK p USE ENV e")
	_, err := auth.EffectiveBlock(root, nil)
	if !errors.Is(err, envset.ErrSet) || !strings.Contains(err.Error(), file) || !strings.Contains(err.Error(), "cpb ALTER ENV e UNSET CPB_SECRET_HELPER") {
		t.Fatalf("launch with a reserved value in a set: %v", err)
	}
	mustStmt(t, "ALTER ENV e UNSET CPB_SECRET_HELPER")
	if _, err := auth.EffectiveBlock(root, nil); err != nil {
		t.Fatalf("after ALTER ENV … UNSET: %v", err)
	}
	if data, _ := os.ReadFile(file); strings.Contains(string(data), "CPB_SECRET") || !strings.Contains(string(data), "EDITOR") {
		t.Fatalf("the set after the write:\n%s", data)
	}

	write("r", "[set]\nCPB_SECRET_HELPER = \"/usr/local/bin/h\"\n")
	mustStmt(t, "CREATE OR REPLACE ENV r SET PAGER=less AS PLAINTEXT")
	write("d", "[refs]\nCPB_SECRET_HELPER = \"keychain:h\"\n")
	mustStmt(t, "DROP ENV d")
	if _, err := os.Stat(envset.Path(dir, "d")); !os.IsNotExist(err) {
		t.Fatalf("DROP ENV left the set: %v", err)
	}
}

// A settings file's env cannot carry a reserved key either: the playbook's
// settings.json, the working folder's, and --settings.
func TestSettingsEnvRefusesReservedKeys(t *testing.T) {
	for _, key := range []string{envset.SecretHelperEnv, fileStoreOptInEnv, storeEnvPrefix + "DIR", config.PlaybooksDirEnv} {
		data := []byte(`{"env": {"` + key + `": "1"}}`)
		if err := settingsReserved("settings.json", data); err == nil || !strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), "reserved") {
			t.Errorf("%s: %v", key, err)
		}
	}
	if err := settingsReserved("settings.json", []byte(`{"env": {"EDITOR": "vi"}}`)); err != nil {
		t.Fatalf("an ordinary key: %v", err)
	}
	work := t.TempDir()
	if err := os.MkdirAll(filepath.Join(work, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, ".claude", "settings.local.json"), []byte(`{"env": {"CPB_SECRET_HELPER": "/x"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := reservedInSettings(t.TempDir(), work); err == nil || !strings.Contains(err.Error(), "CPB_SECRET_HELPER") {
		t.Fatalf("the working folder's settings: %v", err)
	}
	if err := reservedInSettingsArgs([]string{"--settings", `{"env": {"CPB_SECRET_FILE_STORE": "1"}}`}, "", false); err == nil || !strings.Contains(err.Error(), "CPB_SECRET_FILE_STORE") {
		t.Fatalf("--settings: %v", err)
	}
}

// A layer cannot point a session's cpb at another registry; a .playbook an
// earlier release wrote with it still reads, refuses the launch, and UNSET
// VAR repairs it.
func TestPlaybookCannotDeclareThePlaybooksDir(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	root := seedFlatPlaybook(t, "p")
	for _, line := range []string{
		"ALTER PLAYBOOK p SET VAR " + config.PlaybooksDirEnv + "='/srv/registry' AS PLAINTEXT",
		"ALTER PLAYBOOK p BLOCK VAR " + config.PlaybooksDirEnv,
		"CREATE ENV e SET " + config.PlaybooksDirEnv + "='/srv/registry' AS PLAINTEXT",
	} {
		if err := stmtErr(t, line); err == nil || !strings.Contains(err.Error(), "own shell") {
			t.Errorf("%s: %v", line, err)
		}
	}
	file := filepath.Join(root, ".playbook")
	if err := os.WriteFile(file, []byte("version = \"1.0.0\"\nname = \"p\"\n\n[env.set]\n"+config.PlaybooksDirEnv+" = \"/srv/registry\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.EffectiveBlock(root, nil); !errors.Is(err, envset.ErrSet) || !strings.Contains(err.Error(), "cpb ALTER PLAYBOOK p UNSET VAR "+config.PlaybooksDirEnv) {
		t.Fatalf("launch with it in the file: %v", err)
	}
	mustStmt(t, "ALTER PLAYBOOK p UNSET VAR "+config.PlaybooksDirEnv)
	if _, err := auth.EffectiveBlock(root, nil); err != nil {
		t.Fatalf("after UNSET VAR: %v", err)
	}
}
