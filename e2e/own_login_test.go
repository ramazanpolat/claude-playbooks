//go:build !windows

package e2e

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A token account over an isolated playbook's own login (v4.2.0): on Linux
// the login is set aside for the token run and comes back after it. On
// macOS, where the login lives in the Keychain, it stays in place with a
// claude that keeps the token on a 401 (2.1.225, since v4.3.2); an older
// claude is refused before anything changes.
func TestTokenOverOwnLogin(t *testing.T) {
	root := t.TempDir()
	dir := playbookWithEnv(t, root, "acct",
		"isolated_login = true\n\n[env]\naccounts = [\"work\", \"machine\"]\n")
	writeAccount(t, root, "work", "provider = \"anthropic\"\n\n[auth]\nkind = \"claude_token\"\nvalue = \"sk-ant-oat01-ACCOUNT\"\n")
	creds := seedPlaybookCredentials(t, dir)

	if runtime.GOOS == "darwin" {
		old := claudeVersionEnv + "=2.1.224"
		out := launchFails(t, root, launch{env: []string{old}, args: []string{"run", "acct"}})
		for _, want := range []string{"holds a login of its own", "cpb run --account <account> acct", "2.1.225", "this claude is 2.1.224"} {
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
		// cpb start refuses the same launch of the directory.
		if out := launchFails(t, root, launch{env: []string{old}, args: []string{"start", dir}}); !strings.Contains(out, "holds a login of its own") {
			t.Fatalf("start: refusal not explained:\n%s", out)
		}
		if _, present := storeKeys(t, creds)["claudeAiOauth"]; !present {
			t.Fatal("the refused start touched the playbook's own login")
		}
		// A claude that keeps the token: the run carries it, and the login
		// stays where it is, during the run and after.
		during := filepath.Join(t.TempDir(), "store-during")
		got := childEnv(t, root, launch{env: []string{claudeVersionEnv + "=2.1.302", storeDumpEnv + "=" + during}, args: []string{"run", "acct"}})
		if got[tokenEnv] != "sk-ant-oat01-ACCOUNT" {
			t.Fatalf("token = %q, want the account's", got[tokenEnv])
		}
		if data, err := os.ReadFile(during); err != nil || !strings.Contains(string(data), "claudeAiOauth") {
			t.Fatalf("the login did not stay in place during the run (%v): %s", err, data)
		}
		if _, err := os.Lstat(creds + ".cpb-aside"); err == nil {
			t.Fatal("a token run set the login aside on macOS")
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

	// The fix the refusal names works everywhere: this launch on an account
	// that uses the playbook's own login, nothing set aside.
	got := childEnv(t, root, launch{args: []string{"run", "--account", "machine", "acct"}})
	if _, present := got[tokenEnv]; present {
		t.Fatal("the machine account carried a token on an isolated playbook")
	}
	if _, present := storeKeys(t, creds)["claudeAiOauth"]; !present {
		t.Fatal("a launch on the own login lost it")
	}
}
