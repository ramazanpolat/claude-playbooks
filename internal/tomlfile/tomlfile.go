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
	for _, k := range md.Undecoded() {
		if n := len(keys); n > 0 && within(k, keys[n-1]) {
			continue
		}
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

// keyLines maps each table header and key of data, as its full path joined
// by NUL, to the line it first appears on. It reads only what it needs to
// place keys: headers, `key =` lines, and multi-line strings to skip.
func keyLines(data []byte) map[string]int {
	out := map[string]int{}
	var table []string
	inString := ""
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if inString != "" {
			if strings.Count(line, inString)%2 == 1 {
				inString = ""
			}
			continue
		}
		switch {
		case line == "" || line[0] == '#':
			continue
		case strings.HasPrefix(line, "["):
			inner := strings.TrimLeft(line, "[")
			if i := strings.Index(inner, "]"); i >= 0 {
				if path, ok := splitKey(inner[:i]); ok {
					table = path
					record(out, path, n)
				}
			}
			continue
		}
		eq := strings.Index(line, "=")
		if eq <= 0 {
			continue
		}
		path, ok := splitKey(line[:eq])
		if !ok {
			continue
		}
		record(out, append(append([]string(nil), table...), path...), n)
		for _, q := range []string{`"""`, `'''`} {
			if strings.Count(line[eq+1:], q)%2 == 1 {
				inString = q
			}
		}
	}
	return out
}

func record(out map[string]int, path []string, line int) {
	if k := strings.Join(path, "\x00"); out[k] == 0 {
		out[k] = line
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
