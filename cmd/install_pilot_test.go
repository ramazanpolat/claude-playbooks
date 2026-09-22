package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// fakePilot puts an executable named `pilot` on PATH for the duration of the
// test and returns the directory holding it. The script body decides how that
// pilot behaves, which is how the failure paths below are exercised without
// depending on a real pilot-profile install being present on the machine.
func fakePilot(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "pilot")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("writing fake pilot: %v", err)
	}
	t.Setenv("PATH", dir)
	// Opt back in to a real lookup: resetCommandTestState stubs pilot to "not
	// installed" so no other test reaches the machine's own pilot.
	origLookPilot := lookPilot
	lookPilot = func() (string, error) { return exec.LookPath("pilot") }
	t.Cleanup(func() { lookPilot = origLookPilot })
	return dir
}

// A playbook create or install must never fail because the profile tool is
// absent. This is the whole acceptance criterion for the wiring half of
// docs/handoffs/pilot-profile-integration.md.
func TestWirePilotProfileAbsentIsSilentNoOp(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // nothing on PATH at all
	wirePilotProfile(t.TempDir()) // must simply return
}

// The discarded error in wirePilotProfile is a contract, not an oversight: a
// broken or half-installed pilot must not be able to break claude-playbook.
func TestWirePilotProfileIgnoresFailure(t *testing.T) {
	fakePilot(t, "echo boom >&2\nexit 1\n")
	wirePilotProfile(t.TempDir()) // must swallow the failure
}

// When pilot IS present it must be invoked as `pilot wire <dest>` -- the
// subcommand and the destination are the entire contract with that component.
func TestWirePilotProfileInvokesWireWithDest(t *testing.T) {
	dir := fakePilot(t, "printf '%s\\n' \"$@\" > \"$PILOT_ARGS_FILE\"\n")
	argsFile := filepath.Join(dir, "args.txt")
	t.Setenv("PILOT_ARGS_FILE", argsFile)

	dest := t.TempDir()
	wirePilotProfile(dest)

	got, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("fake pilot did not run: %v", err)
	}
	want := "wire\n" + dest + "\n"
	if string(got) != want {
		t.Errorf("pilot invoked with %q, want %q", got, want)
	}
}

// A hanging or chatty pilot must not leak into claude-playbook's own output;
// Run() with no Stdout/Stderr set discards the child's streams.
func TestWirePilotProfileDoesNotInheritOutput(t *testing.T) {
	fakePilot(t, "echo stdout-noise\necho stderr-noise >&2\nexit 0\n")

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	stdout, stderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = w, w
	wirePilotProfile(t.TempDir())
	os.Stdout, os.Stderr = stdout, stderr
	w.Close()

	var buf [256]byte
	n, _ := r.Read(buf[:])
	if n > 0 {
		t.Errorf("pilot output leaked into our streams: %q", buf[:n])
	}
}

// The wire step must be bounded: a pilot that starts and never exits must not
// hang create or install. The fake execs a REAL sleep by absolute path --
// fakePilot leaves only its own directory on PATH, and an earlier version of
// this test used a bare `sleep`, which was not found, so the fake exited 127
// at once and the test passed without ever reaching the timeout. It would also
// have passed with the bound removed. The lower bound on elapsed time below is
// what proves the sleeper actually ran until the timeout killed it.
func TestWirePilotProfileIsBounded(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep binary to build a hanging fake pilot from")
	}
	fakePilot(t, "exec "+sleep+" 300\n")

	done := make(chan struct{})
	start := time.Now()
	go func() {
		wirePilotProfile(t.TempDir())
		close(done)
	}()

	select {
	case <-done:
		elapsed := time.Since(start)
		if elapsed < pilotWireTimeout-time.Second {
			t.Fatalf("wire returned after %s, before the %s timeout could fire: the fake did not hang, so the bound was never exercised", elapsed, pilotWireTimeout)
		}
		if elapsed > pilotWireTimeout+5*time.Second {
			t.Errorf("wire took %s, want it bounded near %s", elapsed, pilotWireTimeout)
		}
	case <-time.After(pilotWireTimeout + 10*time.Second):
		t.Fatal("wirePilotProfile did not return: the call is unbounded")
	}
}

// The four @import lines are inert when the profile is absent (Claude Code
// skips a missing import silently), so they ship unconditionally in a created
// playbook's untracked CLAUDE.md. Guards the handoff's acceptance grep.
func TestDefaultClaudeMDCarriesProfileImports(t *testing.T) {
	content := defaultClaudeMD
	for _, want := range []string{
		"\n@~/.pilot-profile/PROFILE.md\n",
		"\n@~/.pilot-profile/identity.md\n",
		"\n@~/.pilot-profile/preferences.md\n",
		"\n@~/.pilot-profile/capture-protocol.md\n",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("defaultClaudeMD missing import line %q", strings.TrimSpace(want))
		}
	}
	if got := strings.Count(content, "\n@~/.pilot-profile/"); got != 4 {
		t.Errorf("defaultClaudeMD has %d profile imports, want 4", got)
	}
}

// pilotArgsRecorder is a fake pilot that records how it was invoked. PATH is
// the fake's directory followed by the real PATH, so create and install still
// find anything else they need, while the fake wins the pilot lookup.
func pilotArgsRecorder(t *testing.T) (argsFile string) {
	t.Helper()
	orig := os.Getenv("PATH")
	dir := fakePilot(t, "printf '%s\\n' \"$@\" > \"$PILOT_ARGS_FILE\"\n")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+orig)
	argsFile = filepath.Join(t.TempDir(), "args.txt")
	t.Setenv("PILOT_ARGS_FILE", argsFile)
	return argsFile
}

func assertWired(t *testing.T, argsFile, dest string) {
	t.Helper()
	got, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("pilot was never invoked (%v) -- the wire step was skipped", err)
	}
	if want := "wire\n" + dest + "\n"; string(got) != want {
		t.Fatalf("pilot invoked with %q, want %q", got, want)
	}
}

// --no-alias used to return from runCreate before reaching the wire call, so
// the step was silently skipped on an otherwise successful create. The
// handoff's own acceptance run uses --no-alias and still passed, because the
// four @import lines come from the template and appear whether or not pilot
// ever runs -- it proved the template half and could not see the wiring half.
// This asserts the wiring half directly.
func TestCreateNoAliasStillWiresPilot(t *testing.T) {
	resetCommandTestState(t)
	config.PlaybooksDir = filepath.Join(t.TempDir(), "playbooks")
	argsFile := pilotArgsRecorder(t)
	createNoAlias = true

	captureStdout(t, func() {
		if err := runCreate(nil, []string{"pb"}); err != nil {
			t.Fatal(err)
		}
	})
	assertWired(t, argsFile, filepath.Join(config.PlaybooksDir, "pb"))
}

// The same early return existed in runInstall.
func TestInstallNoAliasStillWiresPilot(t *testing.T) {
	resetCommandTestState(t)
	config.PlaybooksDir = filepath.Join(t.TempDir(), "playbooks")
	src := t.TempDir()
	if err := manifest.Write(src, &manifest.Manifest{Name: "pb"}); err != nil {
		t.Fatal(err)
	}
	argsFile := pilotArgsRecorder(t)
	installNoAlias = true

	captureStdout(t, func() {
		if err := runInstall(nil, []string{src}); err != nil {
			t.Fatal(err)
		}
	})
	assertWired(t, argsFile, filepath.Join(config.PlaybooksDir, "pb"))
}

// No test may reach the real pilot on the machine running the suite. With the
// seam stubbed by resetCommandTestState, even a pilot sitting on PATH is not
// found -- which is what keeps the suite's result independent of what the
// developer happens to have installed.
func TestResetKeepsTheRealPilotOutOfReach(t *testing.T) {
	resetCommandTestState(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pilot"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if p, err := lookPilot(); err == nil {
		t.Fatalf("after reset, lookPilot found %q; tests would run the machine's own pilot", p)
	}
}

// registryHeldDuring replaces the wire step with a probe that tries to take the
// registry lock without blocking, and reports whether it was already held.
// flock locks belong to an open file description, so a second open of the same
// file conflicts with the holder even inside one process.
func registryHeldDuring(t *testing.T) (held *bool) {
	t.Helper()
	held = new(bool)
	called := false
	orig := wirePlaybook
	wirePlaybook = func(string) {
		called = true
		f, err := os.OpenFile(registryLockPath(), os.O_CREATE|os.O_RDWR, 0o644)
		if err != nil {
			t.Errorf("opening the registry lock: %v", err)
			return
		}
		defer f.Close()
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			*held = true // someone -- the command under test -- holds it
			return
		}
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	}
	t.Cleanup(func() {
		wirePlaybook = orig
		if !called {
			t.Error("the wire step never ran")
		}
	})
	return held
}

// The registry lock must still be held while pilot wires the playbook. It also
// serializes delete, rename and update, which move or remove the directory being
// wired; released early, a concurrent delete-and-recreate could have the old
// install's pilot edit the replacement.
func TestCreateHoldsRegistryLockWhileWiring(t *testing.T) {
	resetCommandTestState(t)
	config.PlaybooksDir = filepath.Join(t.TempDir(), "playbooks")
	held := registryHeldDuring(t)
	createNoAlias = true
	captureStdout(t, func() {
		if err := runCreate(nil, []string{"pb"}); err != nil {
			t.Fatal(err)
		}
	})
	if !*held {
		t.Fatal("create released the registry lock before wiring")
	}
}

func TestInstallHoldsRegistryLockWhileWiring(t *testing.T) {
	resetCommandTestState(t)
	config.PlaybooksDir = filepath.Join(t.TempDir(), "playbooks")
	src := t.TempDir()
	if err := manifest.Write(src, &manifest.Manifest{Name: "pb"}); err != nil {
		t.Fatal(err)
	}
	held := registryHeldDuring(t)
	installNoAlias = true
	captureStdout(t, func() {
		if err := runInstall(nil, []string{src}); err != nil {
			t.Fatal(err)
		}
	})
	if !*held {
		t.Fatal("install released the registry lock before wiring")
	}
}
