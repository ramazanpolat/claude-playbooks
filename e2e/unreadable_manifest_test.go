//go:build !windows

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A launch refuses a config dir whose manifest cpb cannot read. It may ask
// for an isolated login or a sandbox, so launching as if it said nothing
// would hand over the shared login, or run on the host. A manifest from an
// older cpb (here v3's isolate_auth) is the common case.
func TestLaunchRefusesAnUnreadableManifest(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".playbook"), []byte("isolate_auth = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pbs := t.TempDir()
	playbook(t, pbs, "shared", false)
	for _, c := range []struct {
		name      string
		env, args []string
	}{
		{"start", nil, []string{"start", dir}},
		{"run with a supplied config dir", []string{overrideEnv + "=" + dir}, []string{"run", "shared"}},
	} {
		out := runFailing(t, pbs, c.env, c.args)
		if !strings.Contains(out, `unknown key "isolate_auth" in `+filepath.Join(dir, ".playbook")+":1") ||
			!strings.Contains(out, "does not launch over a manifest it cannot read") {
			t.Errorf("%s:\n%s", c.name, out)
		}
		if _, err := os.Lstat(filepath.Join(dir, ".credentials.json")); err == nil {
			t.Errorf("%s linked a login into the directory", c.name)
		}
	}
}
