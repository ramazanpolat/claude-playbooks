// Package tomlfile decodes the TOML files cpb reads, strictly: a key the
// target does not model is an error naming the key and its line. A file
// mistyped by hand, or written for another version of cpb, then fails
// loudly instead of being read in part.
package tomlfile

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"

	"github.com/BurntSushi/toml"
)

// UnknownKeyError lists the keys of a file its target does not model, in
// file order. A table nobody models is named once, not with every key in it.
type UnknownKeyError struct {
	File string
	Keys []Unknown
}

// Unknown is one key UnknownKeyError names, with its line (0 when it cannot
// be placed).
type Unknown struct {
	Key  string
	Line int
}

func (e *UnknownKeyError) Error() string {
	parts := make([]string, len(e.Keys))
	for i, k := range e.Keys {
		at := e.File
		if k.Line > 0 {
			at = fmt.Sprintf("%s:%d", e.File, k.Line)
		}
		parts[i] = fmt.Sprintf("unknown key %q in %s", k.Key, at)
	}
	return strings.Join(parts, "; ")
}

// Decode decodes data, read from file, into v. A syntax error comes back
// as the toml package returns it; a key v does not model is an
// *UnknownKeyError.
func Decode(file string, data []byte, v any) error {
	md, err := toml.Decode(string(data), v)
	if err != nil {
		return err
	}
	var keys []toml.Key
	seen := map[string]bool{}
	for _, k := range md.Undecoded() {
		// Under a table already named, or the same table again (an array
		// of tables lists each element's key).
		if n := len(keys); (n > 0 && within(k, keys[n-1])) || seen[k.String()] {
			continue
		}
		seen[k.String()] = true
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return nil
	}
	lines := keyLines(data)
	e := &UnknownKeyError{File: file}
	for _, k := range keys {
		e.Keys = append(e.Keys, Unknown{Key: k.String(), Line: lineOf(lines, k)})
	}
	return e
}

// within reports whether k is in the table t.
func within(k, t toml.Key) bool {
	if len(k) <= len(t) {
		return false
	}
	for i := range t {
		if k[i] != t[i] {
			return false
		}
	}
	return true
}

// lineOf is the line k is defined on, or the line of the nearest table or
// key it sits in (a key inside an inline table has no line of its own).
func lineOf(lines map[string]int, k toml.Key) int {
	for n := len(k); n > 0; n-- {
		if l, ok := lines[strings.Join(k[:n], "\x00")]; ok {
			return l
		}
	}
	return 0
}

// keyLines maps each table header and key of data, and each prefix of a
// dotted one, as its full path joined by NUL, to the line it first appears
// on. It scans just enough TOML to tell a key from a value: strings of the
// four kinds (multi-line ones span lines), comments, and the brackets of
// arrays and inline tables, whose own keys are placed by the key holding
// them.
func keyLines(data []byte) map[string]int {
	out := map[string]int{}
	var table []string
	var v valueState
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if v.open() {
			v.scan(line)
			continue
		}
		t := strings.TrimSpace(line)
		switch {
		case t == "" || t[0] == '#':
			continue
		case t[0] == '[':
			inner := strings.TrimPrefix(strings.TrimPrefix(t, "["), "[")
			if i := outsideQuotes(inner, ']'); i >= 0 {
				if path, ok := splitKey(inner[:i]); ok {
					table = path
					record(out, path, n)
				}
			}
			continue
		}
		eq := outsideQuotes(t, '=')
		if eq <= 0 {
			continue
		}
		if path, ok := splitKey(t[:eq]); ok {
			record(out, append(append([]string(nil), table...), path...), n)
		}
		v.scan(t[eq+1:])
	}
	return out
}

// record notes path, and each of its prefixes, at line, unless seen before.
func record(out map[string]int, path []string, line int) {
	for i := 1; i <= len(path); i++ {
		if k := strings.Join(path[:i], "\x00"); out[k] == 0 {
			out[k] = line
		}
	}
}

// outsideQuotes is the index of the first c in s that is not inside a
// "basic" or 'literal' string, or -1.
func outsideQuotes(s string, c byte) int {
	var q byte
	for i := 0; i < len(s); i++ {
		switch {
		case q == '"' && s[i] == '\\':
			i++
		case q != 0:
			if s[i] == q {
				q = 0
			}
		case s[i] == '"' || s[i] == '\'':
			q = s[i]
		case s[i] == c:
			return i
		}
	}
	return -1
}

// valueState is where a value scan stands at the end of a line: inside a
// multi-line string, or inside depth brackets of arrays and inline tables.
type valueState struct {
	ml    string // `"""` or `'''`, while inside one
	depth int
}

func (v *valueState) open() bool { return v.ml != "" || v.depth > 0 }

// scan reads one line of value text, from where the last one stopped.
func (v *valueState) scan(s string) {
	for i := 0; i < len(s); i++ {
		if v.ml != "" {
			switch {
			case v.ml == `"""` && s[i] == '\\':
				i++
			case strings.HasPrefix(s[i:], v.ml):
				i += 2
				v.ml = ""
			}
			continue
		}
		switch c := s[i]; c {
		case '#':
			return
		case '"', '\'':
			if q := s[i : i+min(3, len(s)-i)]; q == `"""` || q == "'''" {
				v.ml = q
				i += 2
				continue
			}
			for i++; i < len(s) && s[i] != c; i++ {
				if c == '"' && s[i] == '\\' {
					i++
				}
			}
		case '[', '{':
			v.depth++
		case ']', '}':
			if v.depth > 0 {
				v.depth--
			}
		}
	}
}

// splitKey splits a dotted TOML key into its parts: bare, "basic" or
// 'literal'. It reports false for anything else.
func splitKey(s string) ([]string, bool) {
	var parts []string
	s = strings.TrimSpace(s)
	for s != "" {
		var part string
		switch s[0] {
		case '"', '\'':
			end := strings.IndexByte(s[1:], s[0])
			if end < 0 {
				return nil, false
			}
			// A basic string's escapes are rare in keys: it is kept as
			// written, and a key with one is placed by its table.
			part, s = s[1:1+end], s[2+end:]
		default:
			i := strings.IndexAny(s, ". \t")
			if i < 0 {
				i = len(s)
			}
			part, s = s[:i], s[i:]
			if part == "" {
				return nil, false
			}
			for _, c := range part {
				if !(c == '-' || c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
					return nil, false
				}
			}
		}
		parts = append(parts, part)
		s = strings.TrimSpace(s)
		if s == "" {
			break
		}
		if s[0] != '.' {
			return nil, false
		}
		s = strings.TrimSpace(s[1:])
	}
	return parts, len(parts) > 0
}
