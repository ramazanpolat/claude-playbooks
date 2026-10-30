package cmd

import (
	"os"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/auth"
	"github.com/ramazanpolat/claude-playbooks/internal/testenv"
)

// No cmd test may reach a real Keychain (CI's macOS runner has one): the
// presence probe answers absent unless a test installs its own. PATH is a
// controlled directory, so a launcher a test creates never meets a command
// that only some hosts have.
func TestMain(m *testing.M) {
	// A launch with account headers execs cpb's header step, and in a test
	// cpb's own binary is this one (account_headers.go).
	if len(os.Args) > 1 && os.Args[1] == headerStepArg {
		if err := runHeaderStep(os.Args[2:]); err != nil {
			os.Stderr.WriteString("Error: " + err.Error() + "\n")
		}
		os.Exit(1)
	}
	auth.KeychainProbe = func(string, string) auth.KeychainState { return auth.KeychainAbsent }
	path, err := testenv.HermeticPath()
	if err != nil {
		panic(err)
	}
	os.Setenv("PATH", path)
	code := m.Run()
	os.RemoveAll(path)
	os.Exit(code)
}
