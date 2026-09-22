package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// A pilot that never exits must not hang the install. Before this was bounded,
// an unbounded Run() waited forever -- and both call sites held the
// machine-global registry lock while waiting, so one pathological optional
// binary wedged claude-playbook for every playbook on the machine.
func TestWirePilotProfileIsBounded(t *testing.T) {
	fakePilot(t, "sleep 300\n")

	done := make(chan struct{})
	start := time.Now()
	go func() {
		wirePilotProfile(t.TempDir())
		close(done)
	}()

	select {
	case <-done:
		if elapsed := time.Since(start); elapsed > pilotWireTimeout+5*time.Second {
			t.Errorf("wire took %s, want it bounded near %s", elapsed, pilotWireTimeout)
		}
	case <-time.After(pilotWireTimeout + 10*time.Second):
		t.Fatal("wirePilotProfile did not return: the call is unbounded")
	}
}

// releaseOnce must be safe to call early AND from the deferred release, or the
// early release in create/install would double-unlock on every success.
func TestReleaseOnceRunsExactlyOnce(t *testing.T) {
	calls := 0
	release := releaseOnce(func() { calls++ })
	release()
	release()
	release()
	if calls != 1 {
		t.Errorf("unlock called %d times, want 1", calls)
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
