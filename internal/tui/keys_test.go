package tui

import (
	"reflect"
	"testing"
)

func names(keys []KeyMsg) []string {
	out := []string{}
	for _, k := range keys {
		out = append(out, k.String())
	}
	return out
}

func TestDecode(t *testing.T) {
	for _, tc := range []struct {
		in    string
		final bool
		want  []string
		rest  string
	}{
		{"q", false, []string{"q"}, ""},
		{"\r", false, []string{"enter"}, ""},
		{"\n", false, []string{"enter"}, ""},
		{"\x7f", false, []string{"backspace"}, ""},
		{"\x03", false, []string{"ctrl+c"}, ""},
		{"\t", false, []string{"tab"}, ""},
		{"\x1b[A\x1b[B\x1b[C\x1b[D", false, []string{"up", "down", "right", "left"}, ""},
		{"\x1bOA\x1bOB", false, []string{"up", "down"}, ""},
		{"\x1b[5~\x1b[6~\x1b[H\x1b[F\x1b[Z", false, []string{"pgup", "pgdown", "home", "end", "shift+tab"}, ""},
		// A lone ESC waits for the rest of a sequence …
		{"\x1b", false, []string{}, "\x1b"},
		// … and is the esc key once the timeout passes with nothing more.
		{"\x1b", true, []string{"esc"}, ""},
		// An arrow split across reads waits, then completes.
		{"\x1b[", false, []string{}, "\x1b["},
		{"\x1b[", true, []string{"esc", "["}, ""},
		// ESC then a key (alt+key): esc, then the key.
		{"\x1bq", false, []string{"esc", "q"}, ""},
		// Unknown sequences (a terminal's reply, a mouse report) vanish
		// whole instead of arriving as text.
		{"\x1b[?1;2c\x1b[12;40Rq", false, []string{"q"}, ""},
		// Pasted text is one key per rune, UTF-8 included.
		{"tuia ✓", false, []string{"t", "u", "i", "a", " ", "✓"}, ""},
		// A rune split across reads waits for its other bytes.
		{"\xe2\x9c", false, []string{}, "\xe2\x9c"},
		{"x\x01y", false, []string{"x", "y"}, ""},
	} {
		keys, rest := decode([]byte(tc.in), tc.final)
		if got := names(keys); !reflect.DeepEqual(got, tc.want) || string(rest) != tc.rest {
			t.Errorf("decode(%q, %v) = %q, rest %q; want %q, rest %q", tc.in, tc.final, got, rest, tc.want, tc.rest)
		}
	}
}
