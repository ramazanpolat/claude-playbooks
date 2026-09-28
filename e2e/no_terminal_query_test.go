package e2e

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
// the permanent guard (v3.26.0).
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
		binPath + " sessions",
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
