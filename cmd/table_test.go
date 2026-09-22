package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestTableAlignsAndRules(t *testing.T) {
	tb := newTable("NAME", "N", "NOTE").rightAlign(1)
	tb.add("short", "6", "first")
	tb.add("much-longer-name", "10", "second")

	var b bytes.Buffer
	tb.render(&b)
	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("want header + rule + 2 rows, got %d:\n%s", len(lines), b.String())
	}
	if !strings.HasPrefix(lines[1], "----") {
		t.Errorf("second line should be the rule: %q", lines[1])
	}
	// Right-aligned numbers line up on their last digit: "6" sits under the
	// "0" of "10", which is the only reason to right-align at all.
	col := strings.Index(lines[0], "N ")
	if col < 0 {
		col = strings.Index(lines[0], "N")
	}
	if !strings.Contains(lines[2], " 6") {
		t.Errorf("6 not right-aligned: %q", lines[2])
	}
	// No trailing whitespace on any line: it shows up in diffs and in copy-paste.
	for i, l := range lines {
		if l != strings.TrimRight(l, " ") {
			t.Errorf("line %d has trailing space: %q", i, l)
		}
	}
}

func TestTableDoesNotTruncateWhenPiped(t *testing.T) {
	// Tests do not run on a terminal, which is the same signal a pipe gives:
	// print everything, because a `| grep` that loses the end of a line is
	// worse than a long line.
	long := strings.Repeat("x", 400)
	tb := newTable("NAME", "DESCRIPTION").flexible(1)
	tb.add("a", long)

	var b bytes.Buffer
	tb.render(&b)
	if !strings.Contains(b.String(), long) {
		t.Error("a flexible column was truncated with stdout not a terminal")
	}
}

func TestClipMarksTheLoss(t *testing.T) {
	for _, c := range []struct {
		in   string
		w    int
		want string
	}{
		{"abcdef", 10, "abcdef"},
		{"abcdef", 6, "abcdef"},
		{"abcdef", 5, "abcd…"},
		{"abcdef", 1, "…"},
		{"ünïcödé-description", 6, "ünïcö…"}, // runes, not bytes
	} {
		if got := clip(c.in, c.w); got != c.want {
			t.Errorf("clip(%q, %d) = %q, want %q", c.in, c.w, got, c.want)
		}
	}
}

// A wide rune occupies two terminal cells. Measuring it as one rune is what
// makes the flexible column overflow on exactly the content it should absorb.
func TestDisplayWidthCountsCellsNotRunes(t *testing.T) {
	for _, c := range []struct {
		in   string
		want int
	}{
		{"abc", 3},
		{"", 0},
		{"日本語", 6},        // 3 runes, 6 cells
		{"a日b", 4},        // mixed
		{"\U0001F600", 2}, // emoji
		{"é", 1},         // e + combining acute renders in one cell
		{"ｆｕｌｌ", 8},       // fullwidth forms
	} {
		if got := displayWidth(c.in); got != c.want {
			t.Errorf("displayWidth(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// clip must never return something WIDER than the budget -- that is what breaks
// alignment. Coming in one cell under is fine: a wide rune is dropped rather
// than half-printed.
func TestClipNeverExceedsTheCellBudget(t *testing.T) {
	for _, in := range []string{
		"abcdefghijklmnop",
		"日本語のテキストです",
		"mixed 日本語 text here",
		"\U0001F600\U0001F600\U0001F600\U0001F600",
	} {
		for w := 1; w <= 12; w++ {
			got := clip(in, w)
			if displayWidth(got) > w {
				t.Errorf("clip(%q, %d) = %q, width %d exceeds %d", in, w, got, displayWidth(got), w)
			}
		}
	}
}

// A narrow terminal must clip the flexible column, not leave it at full content
// width. The original guard skipped the assignment entirely when the remaining
// room fell under the stub, which meant the narrowest terminals -- the only ones
// that needed it -- got no truncation at all.
func TestNarrowTerminalStillClipsTheFlexibleColumn(t *testing.T) {
	orig := terminalWidth
	t.Cleanup(func() { terminalWidth = orig })
	terminalWidth = func() int { return 24 }

	long := strings.Repeat("x", 200)
	tb := newTable("NAME", "DESCRIPTION").flexible(1)
	tb.add("a-very-long-playbook-name-indeed", long)

	var buf bytes.Buffer
	tb.render(&buf)
	for _, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		if displayWidth(line) >= displayWidth(long) {
			t.Fatalf("flexible column was not clipped: line width %d, description width %d\n%s",
				displayWidth(line), displayWidth(long), line)
		}
	}
	if !strings.Contains(buf.String(), "…") {
		t.Error("expected an ellipsis marking the clipped description")
	}
}
