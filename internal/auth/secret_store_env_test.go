package auth

import (
	"slices"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// A launched program gets none of cpb's secret-store variables, whatever
// put them in the launch environment.
func TestLaunchDropsSecretStoreVariables(t *testing.T) {
	env := bindOwnEnv([]string{
		"KEEP=1",
		manifest.SecretFileStoreEnv + "=1",
		manifest.SecretStoreEnvPrefix + "SET=1",
		manifest.SecretStoreEnvPrefix + "SECRET_TOOL=/tmp/other",
		"CPB_SECRET_HELPER=/opt/helper",
	}, "/cfg")
	want := []string{"KEEP=1", "CPB_SECRET_HELPER=/opt/helper", "CLAUDE_CONFIG_DIR=/cfg"}
	if !slices.Equal(env, want) {
		t.Fatalf("%q, want %q", env, want)
	}
}
