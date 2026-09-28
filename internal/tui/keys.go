package tui

import (
	"unicode/utf8"
)

// KeyMsg is one key: a name ("enter", "up", "ctrl+c", …) or, for text,
// the rune itself.
type KeyMsg struct {
	Name string
	Rune rune
}

// String is how the model matches keys: the name, or the rune as text.
func (k KeyMsg) String() string {
	if k.Name != "" {
		return k.Name
	}
	return string(k.Rune)
}

// escTimeout is how long a lone ESC waits for the rest of a sequence
// before it is the esc key.
const escTimeoutMillis = 50

// decode turns terminal input into keys. It returns what it could not
// decode yet: an ESC or a partial sequence or rune at the end of buf,
// which the caller either completes with the next read or, after
// escTimeout with nothing more, passes back with final set (a lone ESC is
// then the esc key). Unknown escape sequences are dropped whole, so a key
// this TUI has no use for can never leak in as text.
func decode(buf []byte, final bool) (keys []KeyMsg, rest []byte) {
	for len(buf) > 0 {
		c := buf[0]
		switch {
		case c == 0x1b:
			if len(buf) == 1 {
				if final {
					return append(keys, KeyMsg{Name: "esc"}), nil
				}
				return keys, buf
			}
			k, n, ok := escape(buf)
			if !ok { // incomplete
				if final {
					// What arrived is all there is: an ESC, then the rest as keys.
					keys = append(keys, KeyMsg{Name: "esc"})
					buf = buf[1:]
					continue
				}
				return keys, buf
			}
			if k.Name != "" {
				keys = append(keys, k)
			}
			buf = buf[n:]
		case c == '\r' || c == '\n':
			keys = append(keys, KeyMsg{Name: "enter"})
			buf = buf[1:]
		case c == '\t':
			keys = append(keys, KeyMsg{Name: "tab"})
			buf = buf[1:]
		case c == 0x7f || c == 0x08:
			keys = append(keys, KeyMsg{Name: "backspace"})
			buf = buf[1:]
		case c == 0x03:
			keys = append(keys, KeyMsg{Name: "ctrl+c"})
			buf = buf[1:]
		case c < 0x20:
			buf = buf[1:] // other control bytes: no key here uses them
		default:
			r, n := utf8.DecodeRune(buf)
			if r == utf8.RuneError && n <= 1 {
				if !utf8.FullRune(buf) && !final {
					return keys, buf // a rune split across reads
				}
				buf = buf[1:]
				continue
			}
			keys = append(keys, KeyMsg{Rune: r})
			buf = buf[n:]
		}
	}
	return keys, nil
}

// escape decodes one sequence at the start of b (b[0] is ESC, len(b) > 1):
// the key (Name "" for one that is dropped), its length, and whether it is
// complete.
func escape(b []byte) (KeyMsg, int, bool) {
	switch b[1] {
	case '[': // CSI: parameters, then a final byte in 0x40–0x7e
		for i := 2; i < len(b); i++ {
			if b[i] >= 0x40 && b[i] <= 0x7e {
				return csiKey(string(b[2:i]), b[i]), i + 1, true
			}
			if b[i] < 0x20 || b[i] > 0x7e {
				return KeyMsg{}, i, true // malformed: drop what came so far
			}
		}
		return KeyMsg{}, 0, false
	case ']', 'P', '_', '^', 'X':
		// OSC, DCS, APC, PM, SOS (a terminal's reply to a query): a string
		// ended by BEL or ST (ESC \\). Dropped whole.
		for i := 2; i < len(b); i++ {
			if b[i] == 0x07 {
				return KeyMsg{}, i + 1, true
			}
			if b[i] == 0x1b && i+1 < len(b) && b[i+1] == '\\' {
				return KeyMsg{}, i + 2, true
			}
		}
		return KeyMsg{}, 0, false
	case 'O': // SS3: one final byte
		if len(b) < 3 {
			return KeyMsg{}, 0, false
		}
		return ss3Key(b[2]), 3, true
	}
	// ESC then an ordinary key (alt+key): the esc key; the other byte is
	// decoded on its own.
	return KeyMsg{Name: "esc"}, 1, true
}

func csiKey(params string, final byte) KeyMsg {
	switch final {
	case 'A':
		return KeyMsg{Name: "up"}
	case 'B':
		return KeyMsg{Name: "down"}
	case 'C':
		return KeyMsg{Name: "right"}
	case 'D':
		return KeyMsg{Name: "left"}
	case 'H':
		return KeyMsg{Name: "home"}
	case 'F':
		return KeyMsg{Name: "end"}
	case 'Z':
		return KeyMsg{Name: "shift+tab"}
	case '~':
		switch params {
		case "1", "7":
			return KeyMsg{Name: "home"}
		case "4", "8":
			return KeyMsg{Name: "end"}
		case "5":
			return KeyMsg{Name: "pgup"}
		case "6":
			return KeyMsg{Name: "pgdown"}
		}
	}
	return KeyMsg{} // a reply, a mouse report, a key not used here
}

func ss3Key(final byte) KeyMsg {
	switch final {
	case 'A':
		return KeyMsg{Name: "up"}
	case 'B':
		return KeyMsg{Name: "down"}
	case 'C':
		return KeyMsg{Name: "right"}
	case 'D':
		return KeyMsg{Name: "left"}
	case 'H':
		return KeyMsg{Name: "home"}
	case 'F':
		return KeyMsg{Name: "end"}
	}
	return KeyMsg{}
}
