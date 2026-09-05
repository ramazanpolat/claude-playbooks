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
func TestMain(m *testing.M) {
	findGenericPassword = func(string) ([]byte, error) {
		return nil, errKeychainStubbed
	}
	os.Exit(m.Run())
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
