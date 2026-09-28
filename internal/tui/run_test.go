package tui

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// These drive real bubbletea programs through a pipe: its input decoder,
// its loop, ExecProcess. The terminal restore on a real tty (raw mode and
// the alternate screen after q, a kill -TERM, -HUP, -INT) is checked by the
// arena check tui-ok, under script, with stty.

// recorder is a model that records the keys and pastes bubbletea decodes.
type recorder struct {
	mu   *sync.Mutex
	seen *[]string
}

func (r recorder) Init() tea.Cmd { return nil }
func (r recorder) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch v := msg.(type) {
	case tea.KeyPressMsg:
		*r.seen = append(*r.seen, v.String())
		if v.String() == "ctrl+d" {
			return r, tea.Quit
		}
	case tea.PasteMsg:
		*r.seen = append(*r.seen, "paste:"+v.Content)
	}
	return r, nil
}
func (r recorder) View() tea.View { return tea.NewView("") }

func runWith(t *testing.T, m tea.Model, feed func(w io.Writer)) (error, string) {
	t.Helper()
	// An *os.File, as a terminal is: bubbletea cancels its reads (for
	// ExecProcess, at the end) only on a file descriptor.
	in, w, perr := os.Pipe()
	if perr != nil {
		t.Fatal(perr)
	}
	defer in.Close()
	var out bytes.Buffer
	p := tea.NewProgram(m, tea.WithInput(in), tea.WithOutput(&out), tea.WithWindowSize(100, 30), tea.WithoutSignalHandler())
	done := make(chan error, 1)
	go func() { done <- runProgram(p) }()
	go feed(w)
	select {
	case err := <-done:
		w.Close()
		return err, out.String()
	case <-time.After(10 * time.Second):
		p.Kill()
		t.Fatal("the program did not end")
	}
	return nil, ""
}

// The keys this TUI uses, decoded by bubbletea: a lone ESC is esc once its
// timeout passes, an arrow is not an ESC; a terminal's OSC reply is no key;
// pasted text is a paste, not keystrokes.
func TestKeysThroughBubbletea(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	err, _ := runWith(t, recorder{&mu, &seen}, func(w io.Writer) {
		for _, s := range []string{"\r", " ", "\x7f", "\t", "\x1b[Z", "\x1b[A", "\x1bOB", "\x1b[5~", "\x1b[6~",
			"q", "\x03", "\x1b]11;rgb:0000/0000/0000\x07", "\x1b[200~tuia ✓\x1b[201~", "x"} {
			w.Write([]byte(s))
			time.Sleep(60 * time.Millisecond)
		}
		w.Write([]byte("\x1b"))
		time.Sleep(300 * time.Millisecond)
		w.Write([]byte("\x04"))
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"enter", "space", "backspace", "tab", "shift+tab", "up", "down", "pgup", "pgdown",
		"q", "ctrl+c", "paste:tuia ✓", "x", "esc", "ctrl+d"}
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Fatalf("decoded\n %q\nwant\n %q", seen, want)
	}
}

func fixtureOptions() Options {
	return Options{Runner: fixture(), Now: func() time.Time { return now }, NoColor: true,
		Home: "/home/p", Cwd: "/home/p/DEV/claude-playbooks", Poll: time.Hour}
}

// q quits the real program.
func TestProgramQuits(t *testing.T) {
	err, out := runWith(t, New(fixtureOptions()), func(w io.Writer) {
		time.Sleep(200 * time.Millisecond)
		w.Write([]byte("q"))
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "router") {
		t.Fatalf("never drew the playbooks:\n%q", out)
	}
}

// RESUME goes through tea.ExecProcess, then the TUI is back and running.
func TestProgramResume(t *testing.T) {
	var mu sync.Mutex
	var resumed [][]string
	o := fixtureOptions()
	o.Resume = func(args ...string) *exec.Cmd {
		mu.Lock()
		resumed = append(resumed, args)
		mu.Unlock()
		return exec.Command("true")
	}
	err, out := runWith(t, New(o), func(w io.Writer) {
		for _, k := range []string{"2", "R", "\x1b[B", "\r"} {
			time.Sleep(200 * time.Millisecond)
			w.Write([]byte(k))
		}
		time.Sleep(500 * time.Millisecond)
		w.Write([]byte("q"))
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(resumed) != 1 || resumed[0][2] != "d0a04774-d6ed-49f7-bb32-3c57962348fa" {
		t.Fatalf("resumed %v", resumed)
	}
	if !strings.Contains(out, "done") {
		t.Fatal("the TUI did not report the resume")
	}
}

// A terminal that closes sends SIGHUP to its foreground processes, and
// bubbletea does not end on EOF from its input by itself (wrapping the tty
// to catch EOF would stop bubbletea using it as the terminal), so SIGHUP is
// what ends the TUI when its terminal goes away:
// SIGHUP, which bubbletea leaves to the default action, quits through
// runProgram, so the terminal is restored.
func TestProgramSIGHUP(t *testing.T) {
	err, _ := runWith(t, New(fixtureOptions()), func(w io.Writer) {
		time.Sleep(300 * time.Millisecond)
		syscall.Kill(syscall.Getpid(), syscall.SIGHUP)
	})
	if err != nil {
		t.Fatal(err)
	}
}
