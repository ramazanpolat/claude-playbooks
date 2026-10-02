package e2e

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// underPTY runs a shell command line under script(1), so the binary's
// stdout is a terminal that answers nothing, like a pty in CI or a terminal
// that ignores queries. It returns what the terminal received.
func underPTY(t *testing.T, env []string, line string) ([]byte, time.Duration) {
	t.Helper()
	if _, err := exec.LookPath("script"); err != nil {
		t.Skip("no script(1)")
	}
	var c *exec.Cmd
	if runtime.GOOS == "linux" {
		c = exec.Command("script", "-qec", line, "/dev/null")
	} else {
		c = exec.Command("script", "-q", "/dev/null", "sh", "-c", line)
	}
	c.Env = env
	c.Stdin = nil // /dev/null: nobody types, nobody answers
	start := time.Now()
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", line, err, out)
	}
	return out, time.Since(start)
}

// No cpb command may query the terminal at startup. A dependency that does
// (bubbletea v1's package init sends an OSC 11 background query and a
// cursor-position request, then waits up to 5 s for replies) would stall
// every command and every launcher on a terminal that does not answer, and
// its late replies would land in the input of whatever runs next. This is
// the permanent guard (v3.25.0).
func TestNoTerminalQueryAtStartup(t *testing.T) {
	home := t.TempDir()
	fake := t.TempDir()
	if err := os.WriteFile(filepath.Join(fake, "claude"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := []string{"HOME=" + home, "PATH=" + fake + ":/usr/bin:/bin", "TERM=xterm-256color"}
	create := exec.Command(binPath, "CREATE", "PLAYBOOK", "pty", "NO", "ALIAS")
	create.Env = env
	if out, err := create.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, line := range []string{
		binPath + " SHOW PLAYBOOKS",
		binPath + " SHOW SESSIONS",
		binPath + " run pty --version", // what a launcher runs
		binPath,                        // bare cpb, on a terminal
	} {
		out, took := underPTY(t, env, line)
		for _, q := range [][]byte{[]byte("\x1b]"), []byte("\x1b[6n"), []byte("\x1b[c"), []byte("\x1b[>")} {
			if bytes.Contains(out, q) {
				t.Errorf("%s sent a terminal query %q:\n%q", strings.TrimPrefix(line, binPath), q, out)
			}
		}
		if took > 2*time.Second {
			t.Errorf("%s took %v under a pty: something waits for the terminal", strings.TrimPrefix(line, binPath), took)
		}
	}
}

// No package initializer may cost measurable startup: every cpb command and
// every launcher pays for it. go-runewidth v0.0.27 (which bubbles v2.2+
// requires) builds an East-Asian-width table for all 65,536 BMP runes in
// its init, 48 ms of CPU per process, which is why bubbles is held at
// v2.1.1 (go-runewidth v0.0.24: 0.2 ms). GODEBUG=inittrace=1 reports each
// package's init; any over 5 ms fails.
func TestNoSlowPackageInit(t *testing.T) {
	c := exec.Command(binPath, "--version")
	c.Env = append(os.Environ(), "GODEBUG=inittrace=1")
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	seen := 0
	for _, line := range strings.Split(string(out), "\n") {
		// init github.com/mattn/go-runewidth @1.1 ms, 48 ms clock, 58744 bytes, 61 allocs
		f := strings.Fields(line)
		if len(f) < 7 || f[0] != "init" || f[6] != "clock," {
			continue
		}
		seen++
		ms, err := strconv.ParseFloat(f[4], 64)
		if err != nil {
			continue
		}
		if ms > 5 {
			t.Errorf("package %s takes %.1f ms to initialize (limit 5 ms): %s", f[1], ms, line)
		}
	}
	if seen == 0 {
		t.Fatalf("no inittrace lines: is GODEBUG=inittrace=1 honoured?\n%s", out)
	}
}
