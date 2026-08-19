//go:build !windows

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Renaming a playbook must leave its launch command usable. A launcher is a
// stateless symlink to the binary: invocation resolves its name against the
// live registry (directory name first, then the manifest alias) at run time.
// A rename that breaks that resolution produces a command that resolves,
// runs, and dies with "unknown playbook". Nothing catches that until the
// command is actually typed, which is how the alias-era version of this bug
// survived to a release.
//
// This executes the regenerated launcher for real rather than inspecting it.
func TestRenamedPlaybookLauncherStillLaunches(t *testing.T) {
	work := t.TempDir()
	// Launcher mutations only apply to the default playbooks root; HOME is
	// the sandbox, so the default root lives inside it.
	root := filepath.Join(work, ".claude-playbooks")
	shellConfig := filepath.Join(work, "shellrc")
	if err := os.WriteFile(shellConfig, []byte("\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	shim := shimDir(t)
	dump := filepath.Join(work, "envdump")
	// The alias invokes the binary by bare name, so the directory holding the
	// binary under test must be on PATH. Relying on an installed claude-playbook
	// made this pass on a developer machine and fail on a clean CI runner.
	baseEnv := []string{
		"PATH=" + shim +
			string(os.PathListSeparator) + filepath.Dir(binPath) +
			string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + work,
		dumpEnv + "=" + dump,
		securityLogEnv + "=" + filepath.Join(work, "security.log"),
	}

	launcherDir := filepath.Join(work, "bin")
	cpb := func(args ...string) string {
		t.Helper()
		full := append([]string{"--launcher-dir", launcherDir}, args...)
		cmd := exec.Command(binPath, full...)
		cmd.Env = baseEnv
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("claude-playbook %v: %v\n%s", full, err, out)
		}
		return string(out)
	}

	cpb("create", "before", "--alias", "ab")
	cpb("rename", "before", "after")

	// Executing the launcher is what happens when the user types `ab`.
	script := filepath.Join(launcherDir, "ab")
	cmd := exec.Command(script)
	cmd.Env = baseEnv
	out, err := cmd.CombinedOutput()

	if strings.Contains(string(out), "unknown playbook") {
		body, _ := os.ReadFile(script)
		t.Fatalf("renamed playbook's launcher is dead -- it still names the old playbook:\n"+
			"  launcher: %s\n  output: %s", body, out)
	}
	if err != nil {
		body, _ := os.ReadFile(script)
		t.Fatalf("executing the launcher failed: %v\n%s\nlauncher: %s", err, out, body)
	}
	if _, statErr := os.Stat(dump); statErr != nil {
		t.Fatalf("launcher ran but never reached claude (no env dump): %v\noutput: %s", statErr, out)
	}

	// It must land in the RENAMED directory, not merely succeed.
	data, err := os.ReadFile(dump)
	if err != nil {
		t.Fatal(err)
	}
	wantDir := "CLAUDE_CONFIG_DIR=" + filepath.Join(root, "after")
	if !strings.Contains(string(data), wantDir) {
		t.Fatalf("launcher used the wrong config dir; wanted %s", wantDir)
	}
}
