//go:build !windows

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A token launch of an isolated playbook that holds its own login is
// refused by run and by start, before the stub claude starts and before the
// store changes: the launch would otherwise have to delete that login.
func TestTokenLaunchOverIsolatedOwnLoginRefused(t *testing.T) {
	root := t.TempDir()
	dir := playbookWithEnv(t, root, "own",
		"isolated_login = true\n\n[env.set]\nCLAUDE_CODE_OAUTH_TOKEN = \"sk-ant-oat01-OWN\"\n")
	creds := seedPlaybookCredentials(t, dir)
	before, err := os.ReadFile(creds)
	if err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{{"run", "own"}, {"start", dir}} {
		out := launchFails(t, root, launch{args: args})
		if !strings.Contains(out, "has a login of its own") || !strings.Contains(out, "--block CLAUDE_CODE_OAUTH_TOKEN") {
			t.Fatalf("%v: refusal not explained:\n%s", args, out)
		}
		if strings.Contains(out, "sk-ant-oat01") || strings.Contains(out, "STALE") {
			t.Fatalf("%v: the refusal printed a credential:\n%s", args, out)
		}
		if after, _ := os.ReadFile(creds); string(after) != string(before) {
			t.Fatalf("%v: the store changed: %s", args, after)
		}
	}

	// Blocking the token for one launch uses the login, untouched.
	got := childEnv(t, root, launch{args: []string{"run", "--block", tokenEnv, "own"}})
	if _, present := got[tokenEnv]; present {
		t.Fatal("the blocked token reached the child")
	}
	if after, _ := os.ReadFile(filepath.Clean(creds)); string(after) != string(before) {
		t.Fatalf("the store changed on a login launch: %s", after)
	}
}
