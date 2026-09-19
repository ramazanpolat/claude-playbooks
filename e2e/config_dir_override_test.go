//go:build !windows

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const overrideEnv = "CLAUDE_CONFIG_DIR_OVERRIDE"

// configDirEnv is the variable the child receives; the override is the request.
const configDirEnv = "CLAUDE_CONFIG_DIR"

// runFailing runs claude-playbook expecting a non-zero exit, and returns the
// combined output. The counterpart to childEnv, which fails the test on a
// non-zero exit and so cannot express a refusal.
func runFailing(t *testing.T, playbooksDir string, env, args []string) string {
	t.Helper()
	work := t.TempDir()
	cmd := exec.Command(binPath, append([]string{"--playbooks-dir", playbooksDir}, args...)...)
	cmd.Env = append([]string{
		"PATH=" + shimDir(t) + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + work,
		dumpEnv + "=" + filepath.Join(work, "envdump"),
		securityLogEnv + "=" + filepath.Join(work, "security.log"),
	}, env...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected claude-playbook %v to fail, it succeeded:\n%s", args, out)
	}
	return string(out)
}

// runOutput runs claude-playbook expecting success, and returns its combined
// output. childEnv asserts on the CHILD's environment; this asserts on what the
// tool itself said to the operator.
func runOutput(t *testing.T, playbooksDir string, env, args []string) string {
	t.Helper()
	work := t.TempDir()
	cmd := exec.Command(binPath, append([]string{"--playbooks-dir", playbooksDir}, args...)...)
	cmd.Env = append([]string{
		"PATH=" + shimDir(t) + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + work,
		dumpEnv + "=" + filepath.Join(work, "envdump"),
		securityLogEnv + "=" + filepath.Join(work, "security.log"),
	}, env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("claude-playbook %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// start names its own config directory, so an override has nothing to override
// and the path argument wins. That has to be SAID: the variable is consumed
// either way, so silence would be indistinguishable from having been honoured.
func TestStartSaysItIgnoredTheOverride(t *testing.T) {
	dir, record := t.TempDir(), t.TempDir()

	out := runOutput(t, t.TempDir(), []string{overrideEnv + "=" + record}, []string{"start", dir})

	if !strings.Contains(out, overrideEnv) || !strings.Contains(out, "ignored") {
		t.Errorf("start did not say it ignored the override:\n%s", out)
	}
	if !strings.Contains(out, dir) {
		t.Errorf("the notice does not name the directory start actually used (%s):\n%s", dir, out)
	}
}

// A malformed override is reported even on the one launch shape that would not
// have used it: a caller whose value is wrong should hear about it. It is a
// warning, not a refusal -- start's own path is valid and the session runs.
func TestStartWarnsOnMalformedOverrideButLaunches(t *testing.T) {
	dir := t.TempDir()

	out := runOutput(t, t.TempDir(), []string{overrideEnv + "=rel/path"}, []string{"start", dir})

	if !strings.Contains(out, "absolute") {
		t.Errorf("start did not report the malformed override:\n%s", out)
	}

	// And it still launched, with its own directory bound.
	env := childEnv(t, t.TempDir(), launch{
		env:  []string{overrideEnv + "=rel/path"},
		args: []string{"start", dir},
	})
	if got := env[configDirEnv]; got != dir {
		t.Errorf("%s = %q, want %q", configDirEnv, got, dir)
	}
}

// Nothing was actually ignored when the override names the same directory, so
// there is nothing to report. Silence here keeps the notice meaningful.
func TestStartSilentWhenOverrideMatchesPath(t *testing.T) {
	dir := t.TempDir()

	out := runOutput(t, t.TempDir(), []string{overrideEnv + "=" + dir}, []string{"start", dir})

	if strings.Contains(out, "ignored") {
		t.Errorf("start reported an override that named its own directory:\n%s", out)
	}
}

// No override: not a word about it.
func TestStartSilentWithoutOverride(t *testing.T) {
	dir := t.TempDir()

	out := runOutput(t, t.TempDir(), nil, []string{"start", dir})

	if strings.Contains(out, overrideEnv) {
		t.Errorf("start mentioned %s although it was never set:\n%s", overrideEnv, out)
	}
}

// The feature itself: a caller that supplies a config directory gets it, and
// the playbook's install directory is not what the child binds.
func TestConfigDirOverrideIsHonoured(t *testing.T) {
	root := t.TempDir()
	install := playbook(t, root, "pb", false)
	record := t.TempDir()

	env := childEnv(t, root, launch{
		env:  []string{overrideEnv + "=" + record},
		args: []string{"run", "pb"},
	})

	if got := env[configDirEnv]; got != record {
		t.Errorf("%s = %q, want the supplied directory %q", configDirEnv, got, record)
	}
	if env[configDirEnv] == install {
		t.Errorf("%s is the playbook install dir %q; the override was ignored", configDirEnv, install)
	}
}

// The override is CONSUMED. Left in the child's environment it would redirect
// any further launch made from inside the session -- `cpb run other` would
// write into this launch's directory.
func TestConfigDirOverrideIsStrippedFromChild(t *testing.T) {
	root := t.TempDir()
	playbook(t, root, "pb", false)
	record := t.TempDir()

	env := childEnv(t, root, launch{
		env:  []string{overrideEnv + "=" + record},
		args: []string{"run", "pb"},
	})

	if v, present := env[overrideEnv]; present {
		t.Errorf("%s reached the child as %q; it must be consumed by the launch", overrideEnv, v)
	}
}

// Stripping does not depend on the override being USED: a launch that ignores
// it (start names its own directory) must not pass it down either.
func TestConfigDirOverrideStrippedEvenWhenUnused(t *testing.T) {
	dir := t.TempDir()
	env := childEnv(t, t.TempDir(), launch{
		env:  []string{overrideEnv + "=" + t.TempDir()},
		args: []string{"start", dir},
	})

	if v, present := env[overrideEnv]; present {
		t.Errorf("%s reached the child as %q from a start launch", overrideEnv, v)
	}
	if got := env[configDirEnv]; got != dir {
		t.Errorf("%s = %q, want start's own argument %q: the command line outranks the environment", configDirEnv, got, dir)
	}
}

// A bare inherited CLAUDE_CONFIG_DIR is deliberately NOT honoured. This is the
// footgun the separate opt-in exists to avoid: a variable left exported in a
// shell must not silently redirect a launcher.
func TestBareConfigDirIsIgnored(t *testing.T) {
	root := t.TempDir()
	install := playbook(t, root, "pb", false)
	stray := t.TempDir()

	env := childEnv(t, root, launch{
		env:  []string{configDirEnv + "=" + stray},
		args: []string{"run", "pb"},
	})

	if got := env[configDirEnv]; got != install {
		t.Errorf("%s = %q, want the playbook install dir %q: a bare inherited value must be discarded", configDirEnv, got, install)
	}
	if env[configDirEnv] == stray {
		t.Errorf("an inherited %s silently redirected the launch to %q", configDirEnv, stray)
	}
}

// With the override set, a stray CLAUDE_CONFIG_DIR alongside it changes
// nothing: the override is the only channel.
func TestOverrideWinsOverBareConfigDir(t *testing.T) {
	root := t.TempDir()
	playbook(t, root, "pb", false)
	record, stray := t.TempDir(), t.TempDir()

	env := childEnv(t, root, launch{
		env:  []string{configDirEnv + "=" + stray, overrideEnv + "=" + record},
		args: []string{"run", "pb"},
	})

	if got := env[configDirEnv]; got != record {
		t.Errorf("%s = %q, want the override %q", configDirEnv, got, record)
	}
}

// The default. Every existing install must behave exactly as before, which is
// the whole reason the opt-in is a separate variable.
func TestNoOverrideBindsInstallDir(t *testing.T) {
	root := t.TempDir()
	install := playbook(t, root, "pb", false)

	env := childEnv(t, root, launch{args: []string{"run", "pb"}})

	if got := env[configDirEnv]; got != install {
		t.Errorf("%s = %q, want %q", configDirEnv, got, install)
	}
	if _, present := env[overrideEnv]; present {
		t.Errorf("%s appeared in the child environment although it was never set", overrideEnv)
	}
}

// Launcher dispatch is how a supervisor actually starts a playbook (argv[0]),
// and it must honour the override identically -- it is the same runRun.
func TestLauncherDispatchHonoursOverride(t *testing.T) {
	root := t.TempDir()
	playbook(t, root, "pb", false)
	record := t.TempDir()

	// A launcher is exactly a symlink to the binary named for the playbook.
	dir := t.TempDir()
	link := filepath.Join(dir, "pb")
	if err := os.Symlink(binPath, link); err != nil {
		t.Fatal(err)
	}

	work := t.TempDir()
	dump := filepath.Join(work, "envdump")
	cmd := exec.Command(link)
	cmd.Env = []string{
		"PATH=" + shimDir(t) + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + work,
		dumpEnv + "=" + dump,
		securityLogEnv + "=" + filepath.Join(work, "security.log"),
		"CLAUDE_PLAYBOOKS_DIR=" + root,
		overrideEnv + "=" + record,
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("launcher dispatch: %v\n%s", err, out)
	}

	data, err := os.ReadFile(dump)
	if err != nil {
		t.Fatalf("stub claude was never executed: %v", err)
	}
	got := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			got[k] = v
		}
	}
	if got[configDirEnv] != record {
		t.Errorf("launcher: %s = %q, want %q", configDirEnv, got[configDirEnv], record)
	}
	if _, present := got[overrideEnv]; present {
		t.Errorf("launcher: %s reached the child", overrideEnv)
	}
}

// A sandboxed launch mounts the config directory, and a supplied one whose
// content is reached through symlinks dangles inside. Refused, not half-done.
// The refusal precedes the backend lookup, so this passes with no sbx present.
func TestOverrideWithSandboxIsRefused(t *testing.T) {
	root := t.TempDir()
	playbook(t, root, "pb", false)
	record := t.TempDir()

	for _, args := range [][]string{
		{"run", "--sandbox", "pb"},
		{"run", "--sandbox-host", "user@host", "pb"},
	} {
		out := runFailing(t, root, []string{overrideEnv + "=" + record}, args)
		if !strings.Contains(out, overrideEnv) || !strings.Contains(out, "sandbox") {
			t.Errorf("%v: refusal does not name the variable and the sandbox:\n%s", args, out)
		}
	}
}

// An always-sandboxed playbook reaches the same refusal without any flag.
func TestOverrideWithAlwaysSandboxedPlaybookIsRefused(t *testing.T) {
	root := t.TempDir()
	dir := playbook(t, root, "pb", false)
	man := filepath.Join(dir, ".playbook")
	if err := os.WriteFile(man, []byte("name = \"pb\"\n\n[sandbox]\nalways = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := runFailing(t, root, []string{overrideEnv + "=" + t.TempDir()}, []string{"run", "pb"})
	if !strings.Contains(out, overrideEnv) {
		t.Errorf("refusal does not name %s:\n%s", overrideEnv, out)
	}
}

// A relative value is refused rather than resolved against the current
// directory: CLAUDE_CONFIG_DIR is resolved by the child against ITS working
// directory, so a relative request means different things from different
// places. Same rule the manifest applies to sandbox.workdir.
func TestRelativeOverrideIsRefused(t *testing.T) {
	root := t.TempDir()
	playbook(t, root, "pb", false)

	out := runFailing(t, root, []string{overrideEnv + "=rel/path"}, []string{"run", "pb"})
	if !strings.Contains(out, "absolute") {
		t.Errorf("refusal does not explain the absolute-path requirement:\n%s", out)
	}
}

// An empty value is "not set": a supervisor clearing the variable must get
// default behavior, not a refusal.
func TestEmptyOverrideIsNotSet(t *testing.T) {
	root := t.TempDir()
	install := playbook(t, root, "pb", false)

	env := childEnv(t, root, launch{
		env:  []string{overrideEnv + "="},
		args: []string{"run", "pb"},
	})

	if got := env[configDirEnv]; got != install {
		t.Errorf("%s = %q, want %q: an empty override means unset", configDirEnv, got, install)
	}
}
