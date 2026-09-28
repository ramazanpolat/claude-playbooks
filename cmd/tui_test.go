package cmd

import (
	"errors"
	"strings"
	"testing"
)

// Off a terminal, cpb tui refuses and names the scriptable form.
func TestTUINeedsATerminal(t *testing.T) {
	sandboxDefaultRoot(t)
	if err := runTUI(nil, nil); !errors.Is(err, errTUINeedsTerminal) {
		t.Fatalf("got %v", err)
	}
}

// Bare cpb ends with the hint on a terminal only; off one its output is
// what it always was.
func TestBareCpbHint(t *testing.T) {
	root := sandboxDefaultRoot(t)
	writePlaybook(t, root, "alpha", nil)
	old := rootTTY
	t.Cleanup(func() { rootTTY = old })
	for _, tty := range []bool{false, true} {
		rootTTY = func() bool { return tty }
		out := captureStdout(t, func() {
			if err := runRoot(rootCmd, nil); err != nil {
				t.Fatal(err)
			}
		})
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		last := lines[len(lines)-1]
		switch {
		case tty && last != "Browse and manage them: cpb tui":
			t.Errorf("on a terminal the last line is %q", last)
		case !tty && strings.Contains(out, "cpb tui"):
			t.Errorf("off a terminal the hint appeared:\n%s", out)
		}
	}
}
