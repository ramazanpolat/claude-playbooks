//go:build !windows

// One-off launch flags, observed in the environment the real child received,
// through run (flags before and after the name), start, and a launcher.
package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func seedProfile(t *testing.T, root, name, body string) {
	t.Helper()
	dir := filepath.Join(root, ".env-profiles")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLaunchFlagsBeforeAndAfterName(t *testing.T) {
	root := t.TempDir()
	playbookWithEnv(t, root, "router", "[env.set]\nMODEL = \"manifest\"\nKEEP = \"manifest\"\n")
	seedProfile(t, root, "work", "unset = [\"CLAUDE_CODE_OAUTH_TOKEN\"]\n\n[set]\nFROM_PROFILE = \"yes\"\n")
	envFile := filepath.Join(t.TempDir(), "extra.env")
	if err := os.WriteFile(envFile, []byte("FROM_FILE=yes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "router")
	creds := seedPlaybookCredentials(t, dir)

	got := childEnv(t, root, launch{
		env: []string{tokenFile(t, "sk-ant-oat01-FROMFILE"), "NOISY=1"},
		args: []string{"run", "--env-profile", "work", "--env", "MODEL=flag",
			"router", "--unset", "NOISY", "--env-file", envFile},
	})
	if got["MODEL"] != "flag" || got["KEEP"] != "manifest" || got["FROM_PROFILE"] != "yes" || got["FROM_FILE"] != "yes" {
		t.Fatalf("layers not applied: MODEL=%q KEEP=%q FROM_PROFILE=%q FROM_FILE=%q",
			got["MODEL"], got["KEEP"], got["FROM_PROFILE"], got["FROM_FILE"])
	}
	if v, present := got["NOISY"]; present {
		t.Fatalf("--unset after the name not honoured: NOISY=%q", v)
	}
	if v, present := got[tokenEnv]; present {
		t.Fatalf("one-off profile's token unset not honoured: %q", v)
	}
	if _, present := storeKeys(t, creds)["claudeAiOauth"]; !present {
		t.Fatal("stored grant quarantined on a launch whose one-off layer made the token inactive")
	}
	if m, _ := os.ReadFile(filepath.Join(dir, ".playbook")); strings.Contains(string(m), "flag") || strings.Contains(string(m), "work") {
		t.Fatalf("one-off flags were written into the manifest:\n%s", m)
	}
}

// After the first non-flag argument everything belongs to claude: a later
// --env is forwarded, not applied.
func TestLaunchFlagsStopAtFirstClaudeArg(t *testing.T) {
	root := t.TempDir()
	playbook(t, root, "router", false)
	got := childEnv(t, root, launch{
		env:  []string{tokenFileEnv + "=" + filepath.Join(t.TempDir(), "absent")},
		args: []string{"run", "router", "--version", "--env", "LATE=1"},
	})
	if _, present := got["LATE"]; present {
		t.Fatal("--env after a claude argument was applied instead of forwarded")
	}
}

func TestStartHonoursLaunchFlags(t *testing.T) {
	root := t.TempDir()
	cfg := filepath.Join(t.TempDir(), "startcfg")
	got := childEnv(t, root, launch{
		env:  []string{tokenFileEnv + "=" + filepath.Join(t.TempDir(), "absent")},
		args: []string{"start", "--env", "ONE_OFF=yes", cfg},
	})
	if got["ONE_OFF"] != "yes" {
		t.Fatalf("start ignored --env: %v", got)
	}
}

func TestMissingOneOffProfileRefusesLaunch(t *testing.T) {
	root := t.TempDir()
	playbook(t, root, "router", false)
	out := launchFails(t, root, launch{
		env:  []string{tokenFileEnv + "=" + filepath.Join(t.TempDir(), "absent")},
		args: []string{"run", "--env-profile", "ghost", "router"},
	})
	if !strings.Contains(out, `env profile "ghost" not found`) {
		t.Fatalf("refusal did not name the profile:\n%s", out)
	}
}

// The launcher form: `router --env K=V -p hi` dispatches as run with the
// flags immediately after the name.
func TestLauncherPassesLaunchFlags(t *testing.T) {
	work := t.TempDir()
	root := filepath.Join(work, ".claude-playbooks")
	dump := filepath.Join(work, "envdump")
	launcherDir := filepath.Join(work, "bin")
	baseEnv := []string{
		"PATH=" + shimDir(t) + string(os.PathListSeparator) + filepath.Dir(binPath) + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + work,
		dumpEnv + "=" + dump,
		securityLogEnv + "=" + filepath.Join(work, "security.log"),
		tokenFileEnv + "=" + filepath.Join(work, "absent"),
	}
	create := exec.Command(binPath, "--launcher-dir", launcherDir, "create", "router")
	create.Env = baseEnv
	if out, err := create.CombinedOutput(); err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(root, "router")); err != nil {
		t.Fatalf("playbook not under the default root: %v", err)
	}

	cmd := exec.Command(filepath.Join(launcherDir, "router"), "--env", "VIA_LAUNCHER=yes", "--version")
	cmd.Env = baseEnv
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("launcher: %v\n%s", err, out)
	}
	data, err := os.ReadFile(dump)
	if err != nil {
		t.Fatalf("launcher never reached claude: %v", err)
	}
	if !strings.Contains(string(data), "VIA_LAUNCHER=yes\n") {
		t.Fatalf("launcher dropped the launch flag:\n%s", data)
	}
}
