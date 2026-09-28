//go:build unix

package tui

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// fakeScreen records what the loop does to the terminal.
type fakeScreen struct {
	mu     sync.Mutex
	w, h   int
	on     bool
	events []string
	out    strings.Builder
}

func (f *fakeScreen) enter() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.on = true
	f.events = append(f.events, "enter")
	return nil
}

func (f *fakeScreen) leave() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.on {
		f.events = append(f.events, "leave")
	}
	f.on = false
}

func (f *fakeScreen) size() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.w, f.h
}

func (f *fakeScreen) write(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.out.WriteString(s)
}

func (f *fakeScreen) state() (bool, []string, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.on, append([]string{}, f.events...), f.out.String()
}

type loopRun struct {
	scr  *fakeScreen
	keys *os.File // write end: what the "user" types
	done chan error
}

func startLoop(t *testing.T, o Options) *loopRun {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close(); w.Close() })
	if o.Runner == nil {
		o.Runner = fixture()
	}
	o.Now = func() time.Time { return now }
	o.NoColor = true
	if o.Poll == 0 {
		o.Poll = time.Hour
	}
	lr := &loopRun{scr: &fakeScreen{w: 100, h: 30}, keys: w, done: make(chan error, 1)}
	go func() {
		defer func() {
			if p := recover(); p != nil {
				lr.done <- errors.New("panic: " + p.(string))
			}
		}()
		lr.done <- run(o, lr.scr, r, nil, nil, nil)
	}()
	return lr
}

func (lr *loopRun) wait(t *testing.T) error {
	t.Helper()
	select {
	case err := <-lr.done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("the loop did not end")
	}
	return nil
}

func eventually(t *testing.T, what string, f func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if f() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("never: %s", what)
}

func TestLoopQuitRestores(t *testing.T) {
	lr := startLoop(t, Options{})
	eventually(t, "the playbooks drawn", func() bool { _, _, out := lr.scr.state(); return strings.Contains(out, "router") })
	lr.keys.WriteString("q")
	if err := lr.wait(t); err != nil {
		t.Fatal(err)
	}
	if on, ev, _ := lr.scr.state(); on || strings.Join(ev, ",") != "enter,leave" {
		t.Fatalf("terminal left %v, events %v", on, ev)
	}
}

// A panic in a draw still gives the terminal back, then panics on.
func TestLoopPanicRestores(t *testing.T) {
	lr := startLoop(t, Options{hook: func(m Model) {
		if m.loaded {
			panic("boom")
		}
	}})
	err := lr.wait(t)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("the panic did not propagate: %v", err)
	}
	if on, _, _ := lr.scr.state(); on {
		t.Fatal("a panic left the terminal raw")
	}
}

// SIGTERM, SIGHUP and SIGINT end the loop with the terminal given back.
func TestLoopSignalsRestore(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT} {
		lr := startLoop(t, Options{})
		eventually(t, "drawn", func() bool { _, _, out := lr.scr.state(); return strings.Contains(out, "router") })
		if err := syscall.Kill(os.Getpid(), sig); err != nil {
			t.Fatal(err)
		}
		err := lr.wait(t)
		var es errSignal
		if !errors.As(err, &es) || es.sig != sig {
			t.Fatalf("%v: %v", sig, err)
		}
		if on, _, _ := lr.scr.state(); on {
			t.Fatalf("%v left the terminal raw", sig)
		}
	}
}

// SIGWINCH redraws at the new size.
func TestLoopResize(t *testing.T) {
	lr := startLoop(t, Options{})
	eventually(t, "drawn", func() bool { _, _, out := lr.scr.state(); return strings.Contains(out, "router") })
	lr.scr.mu.Lock()
	lr.scr.w = 60
	lr.scr.out.Reset()
	lr.scr.mu.Unlock()
	if err := syscall.Kill(os.Getpid(), syscall.SIGWINCH); err != nil {
		t.Fatal(err)
	}
	eventually(t, "a 60-column redraw", func() bool {
		_, _, out := lr.scr.state()
		return strings.Contains(out, strings.Repeat("─", 60)+"\x1b[K") && !strings.Contains(out, strings.Repeat("─", 61))
	})
	lr.keys.WriteString("q")
	lr.wait(t)
}

// RESUME hands the terminal over and takes it back: leave, the process,
// enter; the reader takes none of the session's input meanwhile.
func TestLoopResumeSuspends(t *testing.T) {
	ran := make(chan struct{}, 1)
	var scr *fakeScreen
	lr := startLoop(t, Options{Cwd: "/home/p/DEV/claude-playbooks", Resume: func(args ...string) *exec.Cmd {
		c := exec.Command("sh", "-c", "exit 0")
		return c
	}})
	scr = lr.scr
	eventually(t, "drawn", func() bool { _, _, out := scr.state(); return strings.Contains(out, "router") })
	lr.keys.WriteString("2")
	time.Sleep(100 * time.Millisecond)
	lr.keys.WriteString("R")
	eventually(t, "recent drawn", func() bool { _, _, out := scr.state(); return strings.Contains(out, "d0a04774") })
	lr.keys.WriteString("\x1b[B")
	time.Sleep(100 * time.Millisecond)
	lr.keys.WriteString("\r")
	eventually(t, "resumed and back", func() bool {
		_, ev, out := scr.state()
		return strings.Join(ev, ",") == "enter,leave,enter" && strings.Contains(out, "done")
	})
	close(ran)
	lr.keys.WriteString("q")
	if err := lr.wait(t); err != nil {
		t.Fatal(err)
	}
	if on, ev, _ := scr.state(); on || strings.Join(ev, ",") != "enter,leave,enter,leave" {
		t.Fatalf("events %v", ev)
	}
}

// NO_COLOR draws no attributes; without it the selection is reverse video.
func TestNoColor(t *testing.T) {
	hs := newHarness(t, 100, 30)
	if strings.Contains(hs.view(), "\x1b[") {
		t.Fatal("NoColor drew attributes")
	}
	m := hs.m
	m.o.NoColor = false
	if !strings.Contains(m.View(), "\x1b[7m▸ ") {
		t.Fatal("no reverse video for the selection")
	}
	t.Setenv("NO_COLOR", "1")
	o := Options{}
	if os.Getenv("NO_COLOR") != "" {
		o.NoColor = true
	}
	if !o.NoColor {
		t.Fatal("NO_COLOR is honoured by Run")
	}
}
