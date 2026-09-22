package cmd

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"
)

// Aligned column output, in one place.
//
// Five commands grew their own `%-*s` pairs (list, alias, env-profile, the
// no-arg overview, update --all), and one of them -- env-profile -- never grew
// one at all: it concatenated four kinds of data into a sentence with nested
// parentheses. Sharing the renderer is what makes width-awareness affordable;
// implementing it five times is what kept it from happening.
//
// Width rule: when stdout is a terminal, the FLEXIBLE column absorbs whatever
// space is left and is truncated with an ellipsis. When it is not -- a pipe, a
// file, CI -- nothing is truncated, because a `| grep` that silently loses the
// end of a line is worse than a long line.
type table struct {
	headers []string
	rows    [][]string
	// flex is the index of the column that yields to the terminal width.
	// -1 (the default) truncates nothing.
	flex int
	// right holds the columns rendered right-aligned. Counts belong there:
	// a column of left-aligned numbers cannot be compared down its length,
	// which is the only reason to put numbers in a column at all.
	right map[int]bool
}

func newTable(headers ...string) *table {
	return &table{headers: headers, flex: -1, right: map[int]bool{}}
}

func (t *table) add(cells ...string) {
	t.rows = append(t.rows, cells)
}

// rightAlign renders these columns right-aligned (headers included, so the
// heading sits over the digits rather than beside them).
func (t *table) rightAlign(cols ...int) *table {
	for _, c := range cols {
		t.right[c] = true
	}
	return t
}

// flexible marks the column that absorbs the remaining width.
func (t *table) flexible(col int) *table {
	t.flex = col
	return t
}

func (t *table) render(w io.Writer) {
	if len(t.rows) == 0 {
		return
	}
	widths := make([]int, len(t.headers))
	for i, h := range t.headers {
		widths[i] = utf8.RuneCountInString(h)
	}
	for _, row := range t.rows {
		for i, c := range row {
			if i < len(widths) && utf8.RuneCountInString(c) > widths[i] {
				widths[i] = utf8.RuneCountInString(c)
			}
		}
	}

	// Give the flexible column what is left, but never less than a stub:
	// a column squeezed to nothing is noise, and a narrow terminal is not a
	// reason to render something unreadable.
	if t.flex >= 0 && t.flex < len(widths) {
		if avail := terminalWidth(); avail > 0 {
			fixed := 0
			for i, x := range widths {
				if i != t.flex {
					fixed += x + 2
				}
			}
			if room := avail - fixed; room >= 12 && room < widths[t.flex] {
				widths[t.flex] = room
			}
		}
	}

	line := func(cells []string) {
		var b strings.Builder
		for i, c := range cells {
			if i < len(widths) {
				c = clip(c, widths[i])
			}
			switch {
			case i == len(cells)-1:
				b.WriteString(c) // no trailing padding
			case t.right[i]:
				b.WriteString(padLeft(c, widths[i]))
				b.WriteString("  ")
			default:
				b.WriteString(pad(c, widths[i]))
				b.WriteString("  ")
			}
		}
		fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
	}

	line(t.headers)
	rule := make([]string, len(t.headers))
	for i, h := range t.headers {
		rule[i] = strings.Repeat("-", utf8.RuneCountInString(h))
	}
	line(rule)
	for _, row := range t.rows {
		line(row)
	}
}

func padLeft(s string, w int) string {
	if n := w - utf8.RuneCountInString(s); n > 0 {
		return strings.Repeat(" ", n) + s
	}
	return s
}

func pad(s string, w int) string {
	if n := w - utf8.RuneCountInString(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// clip shortens s to w runes, marking the loss. Runes, not bytes: a description
// may hold anything the pilot typed.
func clip(s string, w int) string {
	if w <= 0 || utf8.RuneCountInString(s) <= w {
		return s
	}
	r := []rune(s)
	if w == 1 {
		return "…"
	}
	return string(r[:w-1]) + "…"
}

// terminalWidth is stdout's width, or 0 when stdout is not a terminal -- which
// is the signal to truncate nothing.
func terminalWidth() int {
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		return 0
	}
	w, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || w < 20 {
		return 0
	}
	return w
}
