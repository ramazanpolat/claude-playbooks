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
// KNOWN LIMITATION: this is a hand-maintained PARTIAL approximation of Unicode
// TR11, not the full East_Asian_Width table. It covers CJK, Hangul, Kana,
// fullwidth forms, the emoji planes and the common double-width symbols, which
// is what realistically appears in a description or a playbook name. It is NOT
// complete: some unconditional W/F characters are counted as one cell -- e.g.
// U+2329/U+232A and supplementary Kana such as U+1B000 -- so a description made
// of those can overflow the flexible column. Only terminal-attached output is
// affected; piped output is never truncated, so nothing is lost.
//
// Four review rounds each found the next missing range, which is the evidence
// that patching ranges by hand does not converge. The fix is to derive widths
// from real Unicode data rather than extend this list again: see
// docs/known-issues/table-width-partial-unicode.md.
func displayWidth(s string) int {
	n := 0
	forEachCell(s, func(_ rune, add int) { n += add })
	return n
}

// forEachCell walks s reporting how many terminal cells each rune adds, and is
// the ONE place that decides. displayWidth and clip both go through it because
// they had drifted apart twice: a rule fixed in one stayed missing from the
// other, which is how the variation-selector case survived two rounds.
//
// Width is not a property of a rune in isolation. A variation selector modifies
// what came BEFORE it: U+FE0F promotes a text-default symbol to emoji
// presentation, which terminals render in two cells -- so a bare U+2764 is one
// cell and U+2764 U+FE0F is two. Counting the selector as a combining mark worth
// zero under-counts every such sequence.
func forEachCell(s string, f func(r rune, add int)) {
	prev := 0 // cells claimed by the last visible rune
	for _, r := range s {
		add := 1
		switch {
		case r == 0xFE0F:
			// Emoji presentation selector: promote a narrow base to two cells.
			// A base that is already wide gains nothing.
			if prev == 1 {
				add = 1
			} else {
				add = 0
			}
		case r == 0xFE0E:
			add = 0 // text presentation selector: the base stays narrow
		case unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r):
			add = 0
		case isWide(r):
			add = 2
		}
		f(r, add)
		switch {
		case r == 0xFE0F && add == 1:
			prev = 2 // the pair now occupies two cells
		case add > 0:
			prev = add
		}
	}
}

func isWide(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x115F, // Hangul Jamo
		// Double-width symbols below the CJK blocks. These render two cells
		// wide in a terminal despite sitting among narrow neighbours, so the
		// range cannot simply jump from Hangul Jamo to U+2E80: a description
		// holding a watch, a star or a check mark would be mismeasured.
		r == 0x231A || r == 0x231B, // watch, hourglass
		r >= 0x23E9 && r <= 0x23EC,
		r == 0x23F0 || r == 0x23F3,
		r >= 0x25FD && r <= 0x25FE,
		r >= 0x2614 && r <= 0x2615,
		r >= 0x2648 && r <= 0x2653,
		r == 0x267F || r == 0x2693 || r == 0x26A1,
		r >= 0x26AA && r <= 0x26AB,
		r >= 0x26BD && r <= 0x26BE,
		r >= 0x26C4 && r <= 0x26C5,
		r == 0x26CE || r == 0x26D4 || r == 0x26EA,
		r >= 0x26F2 && r <= 0x26F3,
		r == 0x26F5 || r == 0x26FA || r == 0x26FD,
		r == 0x2705,
		r >= 0x270A && r <= 0x270B,
		r == 0x2728 || r == 0x274C || r == 0x274E,
		r >= 0x2753 && r <= 0x2755,
		r == 0x2757,
		r >= 0x2795 && r <= 0x2797,
		r == 0x27B0 || r == 0x27BF,
		r >= 0x2B1B && r <= 0x2B1C,
		r == 0x2B50 || r == 0x2B55,
		r == 0x1F004 || r == 0x1F0CF,
		r == 0x1F18E,
		r >= 0x1F191 && r <= 0x1F19A,
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
// cell). A wide rune or an emoji-presentation sequence is dropped whole rather
// than split, so the result can come in a cell under w -- never over it, which
// is what alignment depends on.
func clip(s string, w int) string {
	if w <= 0 || displayWidth(s) <= w {
		return s
	}
	ell := "\u2026"
	if w == 1 {
		return ell
	}
	budget := w - 1 // room for the ellipsis
	used, stop := 0, false
	var b strings.Builder
	forEachCell(s, func(r rune, add int) {
		if stop {
			return
		}
		if used+add > budget {
			stop = true
			return
		}
		b.WriteRune(r)
		used += add
	})
	return b.String() + ell
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
	return renderBudget(w, err)
}

// renderBudget turns a measured terminal width into the render budget. It is
// separate from the syscall so the rule itself is testable -- stubbing
// terminalWidth exercises render's clamp, never this.
//
// 0 is the sentinel for "not a terminal", which means "truncate nothing". A
// narrow terminal must not collapse into it: doing so gave the narrowest
// terminals no truncation at all, the opposite of what they need. The stub
// clamp in render handles the small end.
func renderBudget(w int, err error) int {
	if err != nil || w <= 0 {
		return 0
	}
	return w
}
