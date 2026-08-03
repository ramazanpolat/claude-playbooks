//go:build !windows

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// aliasLineFor returns the generated alias line for a playbook from a shell config.
func aliasLineFor(t *testing.T, shellConfig, aliasName string) string {
	t.Helper()
	data, err := os.ReadFile(shellConfig)
	if err != nil {
		t.Fatalf("reading shell config: %v", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "alias "+aliasName+"=") {
			return line
		}
	}
	t.Fatalf("no alias %q in shell config:\n%s", aliasName, data)
	return ""
}

// Renaming a playbook must leave its alias usable. The alias is generated text
// naming the playbook twice -- once as CLAUDE_CONFIG_DIR, once as the `run`
// argument -- and rewriting only the first produces a line that resolves, runs,
// and dies with "unknown playbook". Nothing catches that until the alias is
// actually typed, which is how it survived to a release.
//
// This executes the rewritten alias for real rather than inspecting its text.
func TestRenamedPlaybookAliasStillLaunches(t *testing.T) {
	root := t.TempDir()
	work := t.TempDir()
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
		"CLAUDE_PLAYBOOKS_DIR=" + root,
	}

	cpb := func(args ...string) string {
		t.Helper()
		full := append([]string{"--playbooks-dir", root, "--shell-config", shellConfig}, args...)
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

	line := aliasLineFor(t, shellConfig, "ab")

	// The alias body is what a shell would execute when the user types `ab`.
	body := strings.TrimPrefix(strings.TrimSpace(line), "alias ab=")
	script := "eval " + body

	cmd := exec.Command("sh", "-c", script)
	cmd.Env = baseEnv
	out, err := cmd.CombinedOutput()

	if strings.Contains(string(out), "unknown playbook") {
		t.Fatalf("renamed playbook's alias is dead -- it still names the old playbook:\n"+
			"  alias: %s\n  output: %s", line, out)
	}
	if err != nil {
		t.Fatalf("executing the alias failed: %v\n%s\nalias: %s", err, out, line)
	}
	if _, statErr := os.Stat(dump); statErr != nil {
		t.Fatalf("alias ran but never reached claude (no env dump): %v\noutput: %s", statErr, out)
	}

	// It must land in the RENAMED directory, not merely succeed.
	data, err := os.ReadFile(dump)
	if err != nil {
		t.Fatal(err)
	}
	wantDir := "CLAUDE_CONFIG_DIR=" + filepath.Join(root, "after")
	if !strings.Contains(string(data), wantDir) {
		t.Fatalf("alias launched the wrong config dir; wanted %s", wantDir)
	}
}
