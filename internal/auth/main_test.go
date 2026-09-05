package auth

import (
	"errors"
	"os"
	"testing"
)

// errKeychainStubbed is returned only by the test stub, so a sentinel test can
// tell "the stub is installed" from "the real provider failed" (no `security`
// on Linux, no item on a fresh Mac) -- which would otherwise look identical.
var errKeychainStubbed = errors.New("keychain access is stubbed out in tests")

// The Keychain lookup is stubbed for the whole package: a test that forgets
// to set a temporary HOME, or sets one and assumes that is enough, would
// otherwise read the developer's real Claude credentials on darwin and
// materialise them into a fixture (or into the real ~/.claude). Tests that
// need a Keychain answer install their own stub and restore this one.
// realHome is the developer's HOME before TestMain replaced it; the sentinel
// below proves no test can see it.
var realHome string

func TestMain(m *testing.M) {
	findGenericPassword = func(string) ([]byte, error) {
		return nil, errKeychainStubbed
	}
	// The FILE store is as real as the Keychain: a test that forgets to set a
	// temporary HOME would read ~/.claude/.credentials.json and could link it
	// into a fixture. The whole package runs under a scratch HOME, with the
	// token file pointing at nothing and no exported token; tests that need
	// their own layout set HOME again.
	realHome = os.Getenv("HOME")
	scratch, err := os.MkdirTemp("", "auth-tests-home-")
	if err != nil {
		panic(err)
	}
	// The playbooks root is NOT pinned here: it defaults to $HOME/.claude-playbooks,
	// which the scratch HOME already isolates, and several tests build their
	// layout under the HOME they set themselves.
	os.Setenv("HOME", scratch)
	os.Setenv("CLAUDE_PLAYBOOKS_OAUTH_TOKEN_FILE", scratch+"/no-token")
	os.Unsetenv("CLAUDE_PLAYBOOKS_DIR")
	os.Unsetenv("CLAUDE_CODE_OAUTH_TOKEN")
	code := m.Run()
	os.RemoveAll(scratch)
	os.Exit(code)
}

// Sentinel: the real file store is unreachable too.
func TestHomeIsScratch(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if realHome != "" && home == realHome {
		t.Fatalf("tests run under the developer's real HOME %s", home)
	}
	if _, err := os.Stat(home + "/.claude/.credentials.json"); err == nil {
		t.Fatalf("a credential store exists under the test HOME %s before any test created one", home)
	}
}

// Sentinel: the real credential provider is unreachable from this package's
// tests. If a future change drops the TestMain stub, this fails before any
// other test can touch a developer's Keychain.
func TestKeychainIsStubbed(t *testing.T) {
	out, err := findGenericPassword("Claude Code-credentials")
	if !errors.Is(err, errKeychainStubbed) || out != nil {
		t.Fatalf("findGenericPassword is not the test stub: out=%q err=%v", out, err)
	}
}
