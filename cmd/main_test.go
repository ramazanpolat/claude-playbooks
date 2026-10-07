package cmd

import (
	"os"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/auth"
)

// No cmd test may reach a real Keychain (CI's macOS runner has one): the
// presence probe answers absent unless a test installs its own.
func TestMain(m *testing.M) {
	auth.KeychainProbe = func(string, string) auth.KeychainState { return auth.KeychainAbsent }
	os.Exit(m.Run())
}
