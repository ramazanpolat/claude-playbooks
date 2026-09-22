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
