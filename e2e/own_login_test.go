package e2e

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A token over an isolated playbook's own login (v4.1.0): on Linux the login
// is set aside for the token run and comes back after it; on macOS, where the
// login lives in the Keychain, the launch is refused before anything changes.
func TestTokenOverOwnLogin(t *testing.T) {
	root := t.TempDir()
	dir := playbookWithEnv(t, root, "acct",
		"isolated_login = true\n\n[env.set]\nCLAUDE_CODE_OAUTH_TOKEN = \"sk-ant-oat01-ACCOUNT\"\n")
	creds := seedPlaybookCredentials(t, dir)

	if runtime.GOOS == "darwin" {
		out := launchFails(t, root, launch{args: []string{"run", "acct"}})
		for _, want := range []string{"holds a login of its own", "cpb run --block CLAUDE_CODE_OAUTH_TOKEN acct"} {
			if !strings.Contains(out, want) {
				t.Errorf("the refusal lacks %q:\n%s", want, out)
			}
		}
		if strings.Contains(out, "ACCOUNT") || strings.Contains(out, "STALE") {
			t.Fatalf("the refusal printed a credential:\n%s", out)
		}
		if _, present := storeKeys(t, creds)["claudeAiOauth"]; !present {
			t.Fatal("the refused launch touched the playbook's own login")
		}
	} else {
		during := filepath.Join(t.TempDir(), "store-during")
		got := childEnv(t, root, launch{env: []string{storeDumpEnv + "=" + during}, args: []string{"run", "acct"}})
		if got[tokenEnv] != "sk-ant-oat01-ACCOUNT" {
			t.Fatalf("token = %q, want the playbook's own", got[tokenEnv])
		}
		data, err := os.ReadFile(during)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "claudeAiOauth") {
			t.Fatalf("the own login was readable during the token run: %s", data)
		}
		if !strings.Contains(string(data), "mcpOAuth") {
			t.Fatalf("MCP logins left the store with the login: %s", data)
		}
		if _, present := storeKeys(t, creds)["claudeAiOauth"]; !present {
			t.Fatal("the own login did not come back after the run")
		}
		if _, err := os.Lstat(creds + ".cpb-aside"); err == nil {
			t.Fatal("aside file left behind after the run")
		}
	}

	// The fix the refusal names works everywhere: this launch on the
	// playbook's own login, nothing set aside.
	got := childEnv(t, root, launch{args: []string{"run", "--block", tokenEnv, "acct"}})
	if _, present := got[tokenEnv]; present {
		t.Fatal("--block left the token in the launch")
	}
	if _, present := storeKeys(t, creds)["claudeAiOauth"]; !present {
		t.Fatal("a launch on the own login lost it")
	}
}
