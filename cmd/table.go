package cmd

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"

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
		widths[i] = displayWidth(h)
	}
	for _, row := range t.rows {
		for i, c := range row {
			if i < len(widths) && displayWidth(c) > widths[i] {
				widths[i] = displayWidth(c)
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
			// A narrow terminal (or a wide USED BY) can leave less than the
			// stub. Clamp UP to it rather than skipping the assignment: doing
			// nothing leaves the flexible column at full content width, which
			// is the overflow this whole block exists to prevent.
			if room := avail - fixed; room < widths[t.flex] {
				if room < 12 {
					room = 12
				}
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
		rule[i] = strings.Repeat("-", displayWidth(h))
	}
	line(rule)
	for _, row := range t.rows {
		line(row)
	}
}

func padLeft(s string, w int) string {
	if n := w - displayWidth(s); n > 0 {
		return strings.Repeat(" ", n) + s
	}
	return s
}

func pad(s string, w int) string {
	if n := w - displayWidth(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// displayWidth is how many terminal CELLS s occupies -- not bytes, and not
// runes. A CJK ideograph or an emoji takes two cells, so 20 Han characters fill
// 40 columns; measuring them as 20 is what makes a table overflow on exactly
// the content the flexible column was meant to absorb. Combining marks take
// none, since they render into the preceding cell.
//
// This is a deliberately small approximation of Unicode TR11 rather than a
// dependency: the wide ranges below cover CJK, Hangul, Kana, fullwidth forms
// and the emoji planes, which is what shows up in a description or a playbook
// name. It never under-counts those, so the table clips early rather than
// overflowing.
func displayWidth(s string) int {
	n := 0
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r):
			// combining: renders into the previous cell
		case isWide(r):
			n += 2
		default:
			n++
		}
	}
	return n
}

func isWide(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x115F, // Hangul Jamo
		r >= 0x2E80 && r <= 0x303E, // CJK radicals, Kangxi
		r >= 0x3041 && r <= 0x33FF, // Kana, CJK compatibility
		r >= 0x3400 && r <= 0x4DBF, // CJK ext A
		r >= 0x4E00 && r <= 0x9FFF, // CJK unified
		r >= 0xA000 && r <= 0xA4CF, // Yi
		r >= 0xAC00 && r <= 0xD7A3, // Hangul syllables
		r >= 0xF900 && r <= 0xFAFF, // CJK compatibility ideographs
		r >= 0xFE10 && r <= 0xFE19, // vertical forms
		r >= 0xFE30 && r <= 0xFE6F, // CJK compatibility forms
		r >= 0xFF00 && r <= 0xFF60, // fullwidth forms
		r >= 0xFFE0 && r <= 0xFFE6,
		r >= 0x1F300 && r <= 0x1FAFF, // emoji and pictographs
		r >= 0x20000 && r <= 0x3FFFD: // CJK ext B and beyond
		return true
	}
	return false
}

// clip shortens s to w terminal cells, marking the loss with an ellipsis (one
// cell). A wide rune is dropped rather than half-printed, so the result can come
// in one cell under w -- never over it, which is what matters for alignment.
func clip(s string, w int) string {
	if w <= 0 || displayWidth(s) <= w {
		return s
	}
	if w == 1 {
		return "…"
	}
	budget := w - 1 // room for the ellipsis
	used := 0
	var b strings.Builder
	for _, r := range s {
		cw := 1
		switch {
		case unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r):
			cw = 0
		case isWide(r):
			cw = 2
		}
		if used+cw > budget {
			break
		}
		b.WriteRune(r)
		used += cw
	}
	return b.String() + "…"
}

// terminalWidth is stdout's width, or 0 when stdout is not a terminal -- which
// is the signal to truncate nothing. A var, not a func, so the width rule is
// testable: it shipped with a hole in it precisely because no test could set a
// narrow terminal.
var terminalWidth = func() int {
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		return 0
	}
	w, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || w < 20 {
		return 0
	}
	return w
}
