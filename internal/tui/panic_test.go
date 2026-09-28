//go:build !race

package tui

import (
	"errors"
	"io"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// Not under -race: on a panic, bubbletea v2.0.10 closes its input file
// (tty.go) while its own reader goroutine still reads it, which the race
// detector reports. That is inside the dependency's recovery path; this
// test checks the outcome, that the panic is caught and returned.

// panicker panics in View once the program runs.
type panicker struct{}

func (panicker) Init() tea.Cmd                         { return nil }
func (p panicker) Update(tea.Msg) (tea.Model, tea.Cmd) { return p, nil }
func (panicker) View() tea.View                        { panic("boom") }

// A panic is caught by bubbletea, which restores the terminal and returns
// an error instead of crashing with the terminal raw.
func TestProgramPanicRecovered(t *testing.T) {
	err, _ := runWith(t, panicker{}, func(w io.Writer) {})
	if err == nil || !errors.Is(err, tea.ErrProgramPanic) {
		t.Fatalf("got %v", err)
	}
}
