//go:build !windows

// Manifest [env] overrides, observed in the environment the real child
// received. The unit tests prove PrepareLaunchEnv's return value; these prove
// the launcher, run, and start paths hand that value to claude.
package e2e

import (
	"os"
	"path/filepath"
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
