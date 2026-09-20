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
	// "Means unset" has to include being consumed: an empty entry left in the
	// child is still an entry a nested launch would read.
	if v, present := env[overrideEnv]; present {
		t.Errorf("an empty %s reached the child as %q", overrideEnv, v)
	}
}

// REGRESSION (review finding): the override was stripped before the manifest,
// profile and launch-flag layers were applied, so any layer naming it put it
// back into the child -- where it would redirect a nested launch. All four
// declaring doors now refuse the key outright, and the binding happens after
// every layer so the refusal does not have to be trusted.
func TestOverrideCannotBeReintroducedByEnvLayers(t *testing.T) {
	root := t.TempDir()
	dir := playbook(t, root, "pb", false)

	t.Run("launch flag", func(t *testing.T) {
		out := runFailing(t, root, nil, []string{"run", "--env", overrideEnv + "=/leak", "pb"})
		if !strings.Contains(out, "managed by claude-playbook") {
			t.Errorf("--env %s was not refused:\n%s", overrideEnv, out)
		}
	})

	t.Run("manifest env.set", func(t *testing.T) {
		man := filepath.Join(dir, ".playbook")
		original, err := os.ReadFile(man)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.WriteFile(man, original, 0o644) })
		if err := os.WriteFile(man, []byte("name = \"pb\"\n\n[env.set]\n"+overrideEnv+" = \"/leak\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		out := runFailing(t, root, nil, []string{"run", "pb"})
		if !strings.Contains(out, "managed by claude-playbook") {
			t.Errorf("a manifest setting %s was not refused:\n%s", overrideEnv, out)
		}
	})

	t.Run("env file", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), "leak.env")
		if err := os.WriteFile(f, []byte(overrideEnv+"=/leak\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		out := runFailing(t, root, nil, []string{"run", "--env-file", f, "pb"})
		if !strings.Contains(out, "managed by claude-playbook") {
			t.Errorf("an env file setting %s was not refused:\n%s", overrideEnv, out)
		}
	})

	t.Run("env profile", func(t *testing.T) {
		out := runFailing(t, root, nil, []string{"env-profile", "leaky", "set", overrideEnv + "=/leak"})
		if !strings.Contains(out, "managed by claude-playbook") {
			t.Errorf("a profile setting %s was not refused:\n%s", overrideEnv, out)
		}
	})
}

// REGRESSION (review finding): a remote start forwarded over ssh returned
// before the notice was reached, so the override was ignored in silence -- and
// the remote never receives the variable, so it could not report it either.
func TestRemoteStartStillReportsIgnoredOverride(t *testing.T) {
	// Stub ssh: the forward must succeed without a real host.
	shim := t.TempDir()
	if err := os.WriteFile(filepath.Join(shim, "ssh"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// A RELATIVE path, so "named as typed" and "resolved locally" are
	// distinguishable: an absolute one looks identical either way, and the
	// forwarding log would satisfy the assertion on its own.
	const remotePath = "scratch/here"
	work := t.TempDir()
	cmd := exec.Command(binPath, "start", "--sandbox-host", "user@host", remotePath)
	cmd.Dir = work
	cmd.Env = []string{
		"PATH=" + shim + string(os.PathListSeparator) + shimDir(t) + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + work,
		dumpEnv + "=" + filepath.Join(work, "envdump"),
		securityLogEnv + "=" + filepath.Join(work, "security.log"),
		overrideEnv + "=/records/a",
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("remote start: %v\n%s", err, out)
	}

	// Assert on the NOTICE line specifically. The forwarding log also echoes
	// the path, so a whole-output match would pass even with the notice gone
	// or naming the wrong directory.
	var notice string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, overrideEnv) && strings.Contains(line, "ignored") {
			notice = line
			break
		}
	}
	if notice == "" {
		t.Fatalf("a remote start did not report the ignored override:\n%s", out)
	}
	if !strings.Contains(notice, remotePath) {
		t.Errorf("the notice does not name the remote path: %q", notice)
	}
	// The local resolution must not appear: the path belongs to the remote host.
	if localised := filepath.Join(work, remotePath); strings.Contains(notice, localised) {
		t.Errorf("the notice resolved the remote path against the local cwd: %q", notice)
	}
}

// REGRESSION (review round 2): cmd/auth.go builds the `claude auth status`
// probe environment straight from os.Environ() and binds CLAUDE_CONFIG_DIR
// itself. Reverting its consumption would restore the leak without failing any
// other test here.
func TestAuthProbeDoesNotLeakOverride(t *testing.T) {
	root := t.TempDir()
	playbook(t, root, "pb", false)

	work := t.TempDir()
	dump := filepath.Join(work, "envdump")
	cmd := exec.Command(binPath, "--playbooks-dir", root, "auth", "status", "--claude", "pb")
	cmd.Env = []string{
		"PATH=" + shimDir(t) + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + work,
		dumpEnv + "=" + dump,
		securityLogEnv + "=" + filepath.Join(work, "security.log"),
		overrideEnv + "=/records/a",
	}
	// The probe's own exit status does not matter: the stub claude emits no
	// JSON, so auth status records an error for it. The environment it was
	// handed is what this test is about.
	out, _ := cmd.CombinedOutput()

	data, err := os.ReadFile(dump)
	if err != nil {
		t.Fatalf("the claude probe was never executed (no env dump): %v\n%s", err, out)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, overrideEnv+"=") {
			t.Errorf("%s reached the claude auth probe as %q", overrideEnv, line)
		}
	}
}

// REGRESSION (review finding): the migration runner built its environment
// straight from os.Environ(), so an exported override survived into
// migrations/apply.sh -- and into anything that script launched.
func TestMigrationRunnerDoesNotLeakOverride(t *testing.T) {
	root := t.TempDir()
	src := t.TempDir()
	// A source the playbook can update from, shipping a migration that records
	// the environment it was given.
	if err := os.WriteFile(filepath.Join(src, "CLAUDE.md"), []byte("# pb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, ".playbook"), []byte("name = \"pb\"\nversion = \"2.0.0\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, "migrations"), 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "migration-env")
	script := "#!/bin/sh\nenv > " + log + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(src, "migrations", "apply.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	// Install it, then update so the migration runs.
	work := t.TempDir()
	base := []string{
		"PATH=" + shimDir(t) + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + work,
		dumpEnv + "=" + filepath.Join(work, "envdump"),
		securityLogEnv + "=" + filepath.Join(work, "security.log"),
	}
	install := exec.Command(binPath, "--playbooks-dir", root, "install", src, "--name", "pb", "--no-alias")
	install.Env = base
	if out, err := install.CombinedOutput(); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	// Make the installed copy older so the update has something to do.
	if err := os.WriteFile(filepath.Join(root, "pb", ".playbook"), []byte("name = \"pb\"\nversion = \"1.0.0\"\n\n[source]\nrepository = \""+src+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	update := exec.Command(binPath, "--playbooks-dir", root, "update", "pb")
	update.Env = append(append([]string{}, base...), overrideEnv+"=/records/a")
	if out, err := update.CombinedOutput(); err != nil {
		t.Fatalf("update: %v\n%s", err, out)
	}

	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("the migration never ran (no env log): %v", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, overrideEnv+"=") {
			t.Errorf("%s reached migrations/apply.sh as %q", overrideEnv, line)
		}
	}
}

// noAgentEnv is a curated environment with NO `claude` anywhere on PATH, for
// asserting what a launch says when the agent is missing. shimDir always
// provides a stub, which is exactly what must not be there for these.
func noAgentEnv(t *testing.T, home string) []string {
	t.Helper()
	bin := filepath.Join(home, "onlybin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	// coreutils the CLI itself may shell out to, without an agent among them.
	for _, tool := range []string{"git", "sh", "uname"} {
		if p, err := exec.LookPath(tool); err == nil {
			_ = os.Symlink(p, filepath.Join(bin, tool))
		}
	}
	return []string{
		"PATH=" + bin,
		"HOME=" + home,
		securityLogEnv + "=" + filepath.Join(home, "security.log"),
	}
}

// REGRESSION: `run` looked up `claude` before validating the launch flags, so
// five of the six ways to get a flag wrong reported "'claude' command not
// found" -- sending a pilot who mistyped to install an agent they may already
// have. Input is the pilot's and is decided first; the agent is the machine's.
func TestInputErrorsPrecedeTheAgentLookup(t *testing.T) {
	root := t.TempDir()
	playbook(t, root, "pb", false)
	home := t.TempDir()
	env := noAgentEnv(t, home)

	badFile := filepath.Join(home, "bad.env")
	if err := os.WriteFile(badFile, []byte("NOTKEYVALUE\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) string {
		cmd := exec.Command(binPath, append([]string{"--playbooks-dir", root}, args...)...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("%v unexpectedly succeeded:\n%s", args, out)
		}
		return string(out)
	}

	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"flag without a value", []string{"run", "--env"}, "needs an argument"},
		{"--env not KEY=VALUE", []string{"run", "--env", "NOTKV", "pb"}, "expects KEY=VALUE"},
		{"reserved key", []string{"run", "--env", overrideEnv + "=/x", "pb"}, "managed by claude-playbook"},
		{"--env-file missing", []string{"run", "--env-file", filepath.Join(home, "nope.env"), "pb"}, "--env-file"},
		{"--env-file malformed", []string{"run", "--env-file", badFile, "pb"}, "--env-file"},
		{"profile does not resolve", []string{"run", "--env-profile", "ghost", "pb"}, "env profile"},
	} {
		t.Run(c.name, func(t *testing.T) {
			out := run(c.args...)
			if !strings.Contains(out, c.want) {
				t.Errorf("want %q, got:\n%s", c.want, out)
			}
			if strings.Contains(out, "command not found") {
				t.Errorf("the missing agent was reported instead of the input error:\n%s", out)
			}
		})
	}

	// The control: with nothing wrong in the input, the missing agent IS the
	// error. Without this the test above would pass if the lookup vanished.
	t.Run("no input error: the agent is reported", func(t *testing.T) {
		if out := run("run", "pb"); !strings.Contains(out, "command not found") {
			t.Errorf("want the missing-agent error, got:\n%s", out)
		}
	})

	// start shares the shape, and shares the fix.
	t.Run("start validates first too", func(t *testing.T) {
		out := run("start", filepath.Join(home, "adhoc"), "--env-profile", "ghost")
		if !strings.Contains(out, "env profile") || strings.Contains(out, "command not found") {
			t.Errorf("start reported the agent instead of the input error:\n%s", out)
		}
	})
}
