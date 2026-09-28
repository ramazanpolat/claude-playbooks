package tui

import (
	"errors"
	"os"
	"os/signal"
	"syscall"

	tea "charm.land/bubbletea/v2"
)

// Run is `cpb tui`: a bubbletea program on the terminal. bubbletea restores
// the terminal however the program ends: a quit, a panic, SIGINT (an
// interrupt) or SIGTERM (a quit). SIGHUP, which it leaves to the default
// action, is handled here as a quit, so a closed terminal window or a kill
// -HUP also restores the terminal and runs the deferred cleanup.
func Run(o Options) error {
	return runProgram(tea.NewProgram(New(fromEnv(o, os.Getenv))))
}

func runProgram(p *tea.Program) error {
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	done := make(chan struct{})
	defer func() { signal.Stop(hup); close(done) }()
	go func() {
		select {
		case <-hup:
			p.Quit()
		case <-done:
		}
	}()
	_, err := p.Run()
	if errors.Is(err, tea.ErrInterrupted) {
		return errors.New("cpb tui: interrupted")
	}
	return err
}
