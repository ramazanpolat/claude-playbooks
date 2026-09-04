//go:build !windows

// Manifest [env] overrides, observed in the environment the real child
// received. The unit tests prove PrepareLaunchEnv's return value; these prove
// the launcher, run, and start paths hand that value to claude.
package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func playbookWithEnv(t *testing.T, root, name, envBlock string) string {
	t.Helper()
	dir := playbook(t, root, name, false)
	if err := os.WriteFile(filepath.Join(dir, ".playbook"),
		[]byte("name = \""+name+"\"\n"+envBlock), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The auth case: a playbook that unsets the token keeps its own login and
// gets no token, while the machine-global token file is still there.
func TestManifestUnsetTokenKeepsGrantAndStripsToken(t *testing.T) {
	root := t.TempDir()
	dir := playbookWithEnv(t, root, "account",
		"[env]\nunset = [\"CLAUDE_CODE_OAUTH_TOKEN\"]\n")
	creds := seedPlaybookCredentials(t, dir)

	got := childEnv(t, root, launch{
		env:  []string{tokenFile(t, "sk-ant-oat01-FROMFILE"), tokenEnv + "=sk-ant-oat01-INHERITED"},
		args: []string{"run", "account"},
	})

	if v, present := got[tokenEnv]; present {
		t.Fatalf("child received %s=%q despite the manifest unset", tokenEnv, v)
	}
	if _, present := storeKeys(t, creds)["claudeAiOauth"]; !present {
		t.Fatal("stored grant was quarantined; the playbook has nothing left to authenticate with")
	}
}

// A sibling playbook without the block is untouched by it: the token still
// applies there. This is the whole point over renaming the token file.
func TestSiblingPlaybookStillReceivesToken(t *testing.T) {
	root := t.TempDir()
	playbookWithEnv(t, root, "account", "[env]\nunset = [\"CLAUDE_CODE_OAUTH_TOKEN\"]\n")
	playbook(t, root, "shared", false)

	got := childEnv(t, root, launch{
		env:  []string{tokenFile(t, "sk-ant-oat01-FROMFILE")},
		args: []string{"run", "shared"},
	})
	if got[tokenEnv] != "sk-ant-oat01-FROMFILE" {
		t.Fatalf("sibling lost the token: %q", got[tokenEnv])
	}
}

func TestManifestSetAndUnsetReachChild(t *testing.T) {
	root := t.TempDir()
	playbookWithEnv(t, root, "router",
		"[env]\nunset = [\"NOISY\"]\n\n[env.set]\nANTHROPIC_BASE_URL = \"http://proxy/v1\"\n")

	got := childEnv(t, root, launch{
		env: []string{
			tokenFileEnv + "=" + filepath.Join(t.TempDir(), "absent"),
			"ANTHROPIC_BASE_URL=http://shell/v1",
			"NOISY=1",
		},
		args: []string{"run", "router"},
	})
	if got["ANTHROPIC_BASE_URL"] != "http://proxy/v1" {
		t.Fatalf("ANTHROPIC_BASE_URL = %q, want the manifest's", got["ANTHROPIC_BASE_URL"])
	}
	if v, present := got["NOISY"]; present {
		t.Fatalf("NOISY=%q survived the manifest unset", v)
	}
}

// `start` binds a directory directly and has regressed independently of run.
func TestStartHonorsManifestEnv(t *testing.T) {
	root := t.TempDir()
	cfg := filepath.Join(t.TempDir(), "startcfg")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, ".playbook"),
		[]byte("name = \"startcfg\"\n\n[env.set]\nFROM_MANIFEST = \"yes\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := childEnv(t, root, launch{
		env:  []string{tokenFileEnv + "=" + filepath.Join(t.TempDir(), "absent")},
		args: []string{"start", cfg},
	})
	if got["FROM_MANIFEST"] != "yes" {
		t.Fatalf("start ignored the manifest env: %v", got)
	}
}

// launchFails runs claude-playbook expecting a non-zero exit and returns its
// combined output; the stub claude must never have been reached.
func launchFails(t *testing.T, playbooksDir string, l launch) string {
	t.Helper()
	work := t.TempDir()
	dump := filepath.Join(work, "envdump")
	args := append([]string{"--playbooks-dir", playbooksDir}, l.args...)
	cmd := exec.Command(binPath, args...)
	cmd.Env = append([]string{
		"PATH=" + shimDir(t) + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + work,
		dumpEnv + "=" + dump,
		securityLogEnv + "=" + filepath.Join(work, "security.log"),
	}, l.env...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("claude-playbook %v exited 0:\n%s", args, out)
	}
	if _, serr := os.Stat(dump); serr == nil {
		t.Fatalf("stub claude was launched despite the refusal:\n%s", out)
	}
	return string(out)
}

// A profile stored under the playbooks root reaches the child, layered under
// the playbook's own entries.
func TestProfileReachesChild(t *testing.T) {
	root := t.TempDir()
	profiles := filepath.Join(root, ".env-profiles")
	if err := os.MkdirAll(profiles, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profiles, "glm.toml"),
		[]byte("unset = [\"CLAUDE_CODE_OAUTH_TOKEN\"]\n\n[set]\nANTHROPIC_BASE_URL = \"http://profile/v1\"\nMODEL = \"profile\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	playbookWithEnv(t, root, "router", "[env]\nprofiles = [\"glm\"]\n\n[env.set]\nMODEL = \"own\"\n")

	got := childEnv(t, root, launch{
		env:  []string{tokenFile(t, "sk-ant-oat01-FROMFILE")},
		args: []string{"run", "router"},
	})
	if got["ANTHROPIC_BASE_URL"] != "http://profile/v1" || got["MODEL"] != "own" {
		t.Fatalf("ANTHROPIC_BASE_URL=%q MODEL=%q", got["ANTHROPIC_BASE_URL"], got["MODEL"])
	}
	if v, present := got[tokenEnv]; present {
		t.Fatalf("token reached the child despite the profile's unset: %q", v)
	}
}

// A referenced profile that does not exist refuses the launch outright.
func TestMissingProfileRefusesLaunch(t *testing.T) {
	root := t.TempDir()
	playbookWithEnv(t, root, "router", "[env]\nprofiles = [\"ghost\"]\n")

	out := launchFails(t, root, launch{
		env:  []string{tokenFileEnv + "=" + filepath.Join(t.TempDir(), "absent")},
		args: []string{"run", "router"},
	})
	if !strings.Contains(out, `env profile "ghost" not found`) {
		t.Fatalf("refusal did not name the profile:\n%s", out)
	}
}
