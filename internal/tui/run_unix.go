//go:build unix

package tui

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// screen is the terminal the loop draws on: the real one (ttyScreen), or a
// fake in tests.
type screen interface {
	enter() error // raw mode, the alternate screen, the cursor hidden
	leave()       // all of enter undone; safe to call twice
	size() (w, h int)
	write(s string)
}

// Run is `cpb tui`: it takes the terminal on stdin and stdout, and gives it
// back as it found it however the loop ends: a quit, a panic, a signal.
// Nothing touches the terminal before Run is called, so linking this
// package costs other commands nothing.
func Run(o Options) error {
	return run(fromEnv(o, os.Getenv), &ttyScreen{in: os.Stdin, out: os.Stdout}, os.Stdin, os.Stdin, os.Stdout, os.Stderr)
}

// errSignal is the loop's end on SIGINT, SIGTERM or SIGHUP.
type errSignal struct{ sig os.Signal }

func (e errSignal) Error() string { return fmt.Sprintf("cpb tui: %v", e.sig) }

// run is the loop. keys is the input it reads; stdin/stdout/stderr are
// what a resumed session gets.
func run(o Options, s screen, keys *os.File, stdin, stdout, stderr *os.File) (err error) {
	if err := s.enter(); err != nil {
		return err
	}
	defer s.leave()
	defer func() {
		if r := recover(); r != nil {
			s.leave()
			panic(r)
		}
	}()

	sig := make(chan os.Signal, 8)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGWINCH)
	defer signal.Stop(sig)

	msgs := make(chan Msg, 64)
	rd := startReader(keys, msgs)
	defer rd.stop()

	m := New(o)
	w, h := s.size()
	m, _ = m.Update(WindowSizeMsg{w, h})
	start := func(c Cmd) {
		if c != nil {
			go func() { msgs <- c() }()
		}
	}
	start(m.Init())
	tick := time.NewTicker(m.o.Poll)
	defer tick.Stop()
	draw := func() {
		if o.hook != nil {
			o.hook(m)
		}
		s.write("\x1b[H" + strings.ReplaceAll(m.View(), "\n", "\x1b[K\r\n") + "\x1b[K\x1b[J")
	}
	draw()
	for {
		var msg Msg
		select {
		case msg = <-msgs:
		case t := <-tick.C:
			msg = tickMsg(t)
		case g := <-sig:
			if g != syscall.SIGWINCH {
				return errSignal{g}
			}
			w, h := s.size()
			msg = WindowSizeMsg{w, h}
		}
		switch x := msg.(type) {
		case quitMsg:
			return nil
		case batchMsg:
			for _, c := range x {
				start(c)
			}
			continue
		case execMsg:
			// The session gets the terminal as the shell would give it: the
			// reader paused so no key is taken from it, cooked mode, the
			// normal screen.
			rd.pause()
			s.leave()
			x.cmd.Stdin, x.cmd.Stdout, x.cmd.Stderr = stdin, stdout, stderr
			runErr := x.cmd.Run()
			if err := s.enter(); err != nil {
				return err
			}
			drainInterrupts(sig)
			dropKeys(msgs)
			rd.resume()
			msg = resumeDoneMsg{x.stmt, runErr}
		}
		var c Cmd
		m, c = m.Update(msg)
		start(c)
		draw()
	}
}

// dropKeys discards keys that were read after the handover began: they
// were typed for the resumed session, not for the TUI. Other messages (a
// read that finished meanwhile) are kept.
func dropKeys(msgs chan Msg) {
	var keep []Msg
	for {
		select {
		case m := <-msgs:
			if _, isKey := m.(KeyMsg); !isKey {
				keep = append(keep, m)
			}
			continue
		default:
		}
		break
	}
	for _, m := range keep {
		msgs <- m
	}
}

// drainInterrupts drops the SIGINTs a resumed session's Ctrl-C sent to the
// whole foreground group: they were the session's, not the TUI's.
func drainInterrupts(sig chan os.Signal) {
	for {
		select {
		case g := <-sig:
			if g != syscall.SIGINT {
				sig <- g
				return
			}
		default:
			return
		}
	}
}

// ttyScreen is the real terminal.
type ttyScreen struct {
	in, out *os.File
	state   *term.State
}

func (t *ttyScreen) enter() error {
	st, err := term.MakeRaw(int(t.in.Fd()))
	if err != nil {
		return err
	}
	t.state = st
	t.write("\x1b[?1049h\x1b[?25l\x1b[H\x1b[2J")
	return nil
}

func (t *ttyScreen) leave() {
	if t.state == nil {
		return
	}
	t.write("\x1b[0m\x1b[?25h\x1b[?1049l")
	_ = term.Restore(int(t.in.Fd()), t.state)
	t.state = nil
}

func (t *ttyScreen) size() (int, int) {
	w, h, err := term.GetSize(int(t.out.Fd()))
	if err != nil || w <= 0 || h <= 0 {
		return 80, 24
	}
	return w, h
}

func (t *ttyScreen) write(s string) { _, _ = t.out.WriteString(s) }

// reader turns input into KeyMsgs on out. It can be paused, which it
// acknowledges only between reads, and a pause wakes its poll at once (a
// self-pipe), so while a resumed session runs it takes none of that
// session's keys. At the end of input it sends quitMsg: a terminal that is
// gone cannot type q.
type reader struct {
	in                *os.File
	wakeR, wakeW      *os.File
	out               chan<- Msg
	pauseReq, ack     chan struct{}
	resumeReq, done   chan struct{}
	exited            chan struct{}
	stopOnce, wakeOne sync.Once
}

func startReader(in *os.File, out chan<- Msg) *reader {
	r := &reader{in: in, out: out, pauseReq: make(chan struct{}), ack: make(chan struct{}),
		resumeReq: make(chan struct{}), done: make(chan struct{}), exited: make(chan struct{})}
	if pr, pw, err := os.Pipe(); err == nil {
		r.wakeR, r.wakeW = pr, pw
	}
	go r.loop()
	return r
}

func (r *reader) wake() {
	if r.wakeW != nil {
		_, _ = r.wakeW.Write([]byte{0})
	}
}

func (r *reader) pause() {
	r.wake()
	select {
	case r.pauseReq <- struct{}{}:
		select {
		case <-r.ack:
		case <-r.exited:
		}
	case <-r.exited:
	}
}

func (r *reader) resume() {
	select {
	case r.resumeReq <- struct{}{}:
	case <-r.exited:
	}
}

func (r *reader) stop() {
	r.stopOnce.Do(func() { close(r.done) })
	r.wake()
	<-r.exited
	if r.wakeR != nil {
		r.wakeR.Close()
		r.wakeW.Close()
	}
}

func (r *reader) send(msgs ...Msg) {
	for _, k := range msgs {
		select {
		case r.out <- k:
		case <-r.done:
			return
		}
	}
}

func (r *reader) loop() {
	defer close(r.exited)
	fd := int(r.in.Fd())
	buf := make([]byte, 256)
	var pending []byte
	for {
		select {
		case <-r.done:
			return
		case <-r.pauseReq:
			pending = nil
			r.ack <- struct{}{}
			select {
			case <-r.resumeReq:
			case <-r.done:
				return
			}
			continue
		default:
		}
		timeout := 100
		if len(pending) > 0 {
			timeout = escTimeoutMillis
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		if r.wakeR != nil {
			fds = append(fds, unix.PollFd{Fd: int32(r.wakeR.Fd()), Events: unix.POLLIN})
		}
		n, err := unix.Poll(fds, timeout)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return
		}
		if len(fds) > 1 && fds[1].Revents != 0 {
			// A pause or a stop: take the wake byte, not a key, and look again.
			_, _ = r.wakeR.Read(make([]byte, 16))
			continue
		}
		if n == 0 {
			if len(pending) > 0 {
				keys, _ := decode(pending, true)
				pending = nil
				r.sendKeys(keys)
			}
			continue
		}
		k, err := r.in.Read(buf)
		if k == 0 && err != nil {
			r.send(quitMsg{})
			return
		}
		keys, rest := decode(append(pending, buf[:k]...), false)
		pending = rest
		r.sendKeys(keys)
	}
}

func (r *reader) sendKeys(keys []KeyMsg) {
	for _, k := range keys {
		r.send(k)
	}
}
