package cmd

import (
	"errors"
	"os"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/auth"
	"github.com/ramazanpolat/claude-playbooks/internal/testenv"
)

// errKeychainStubbed is what the Keychain secret lookup answers in cmd
// tests: no item, whatever the service.
var errKeychainStubbed = errors.New("the Keychain is stubbed out in cmd tests")

// No cmd test may reach a real Keychain (CI's macOS runner has one, and so
// does a developer's Mac): the presence probe answers absent and the secret
// lookup finds no item, unless a test installs its own. Without the second,
// a sync a test runs on macOS would read the machine's real login and copy
// it into the test's HOME. PATH is a controlled directory, so a launcher a
// test creates never meets a command that only some hosts have.
func TestMain(m *testing.M) {
	// A launch with account headers execs cpb's header step, and in a test
	// cpb's own binary is this one (account_headers.go). The step reads no
	// login: it joins the headers and execs.
	if len(os.Args) > 1 && os.Args[1] == headerStepArg {
		if err := runHeaderStep(os.Args[2:]); err != nil {
			os.Stderr.WriteString("Error: " + err.Error() + "\n")
		}
		os.Exit(1)
	}
	auth.KeychainProbe = func(string, string) auth.KeychainState { return auth.KeychainAbsent }
	auth.SetKeychainSecretForTest(func(string) ([]byte, error) { return nil, errKeychainStubbed })
	path, err := testenv.HermeticPath()
	if err != nil {
		panic(err)
	}
	os.Setenv("PATH", path)
	code := m.Run()
	os.RemoveAll(path)
	os.Exit(code)
}

// Sentinel: the Keychain secret lookup is the stub for every cmd test. If
// TestMain stops installing it, this fails the suite on every OS, not only
// on a Mac where a real item could be read. It never calls the lookup.
func TestKeychainSecretIsStubbed(t *testing.T) {
	if !auth.KeychainSecretStubbedForTest() {
		t.Fatal("the Keychain secret lookup is the real one: TestMain must install auth.SetKeychainSecretForTest")
	}
}
