// Package settings edits a playbook's settings.json, Claude Code's user-scope
// settings for that playbook, without disturbing what it does not change:
// every other key, and the order of every key, is kept as it was.
//
// It understands JSON objects only as far as editing needs: an Object is an
// ordered list of members whose values stay raw JSON until asked for.
package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FileName is the settings file inside a playbook's config directory.
const FileName = "settings.json"

// Object is a JSON object that keeps its members in order.
type Object struct {
	keys []string
	vals map[string]json.RawMessage
}

// NewObject returns an empty object.
func NewObject() *Object { return &Object{vals: map[string]json.RawMessage{}} }

// ParseObject parses a JSON object, keeping member order.
func ParseObject(data []byte) (*Object, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errors.New("not a JSON object")
	}
	o := NewObject()
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		k, ok := tok.(string)
		if !ok {
			return nil, errors.New("not a JSON object")
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		if _, dup := o.vals[k]; !dup {
			o.keys = append(o.keys, k)
		}
		o.vals[k] = raw
	}
	if _, err := dec.Token(); err != nil { // the closing brace
		return nil, err
	}
	if _, err := dec.Token(); err == nil {
		return nil, errors.New("trailing data after the JSON object")
	}
	return o, nil
}

// Keys returns the member names in order.
func (o *Object) Keys() []string { return append([]string(nil), o.keys...) }

// Has reports whether key is a member.
func (o *Object) Has(key string) bool { _, ok := o.vals[key]; return ok }

// Raw returns a member's raw JSON, or nil.
func (o *Object) Raw(key string) json.RawMessage { return o.vals[key] }

// Get decodes a member into v; it reports false when the member is absent.
func (o *Object) Get(key string, v any) (bool, error) {
	raw, ok := o.vals[key]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(raw, v)
}

// Object returns a member that is itself an object; an absent member is an
// empty object, and a member of another type is an error.
func (o *Object) Object(key string) (*Object, error) {
	raw, ok := o.vals[key]
	if !ok {
		return NewObject(), nil
	}
	sub, err := ParseObject(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", key, err)
	}
	return sub, nil
}

// Set replaces a member in place, or appends it.
func (o *Object) Set(key string, v any) error {
	raw, err := encode(v)
	if err != nil {
		return err
	}
	if _, ok := o.vals[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = raw
	return nil
}

// SetObject stores sub as a member; an empty sub removes the member, so an
// edit never leaves an empty object behind that was not there before.
func (o *Object) SetObject(key string, sub *Object) {
	if len(sub.keys) == 0 {
		o.Delete(key)
		return
	}
	raw, _ := sub.MarshalJSON()
	if _, ok := o.vals[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = raw
}

// Delete removes a member; it reports whether one was there.
func (o *Object) Delete(key string) bool {
	if _, ok := o.vals[key]; !ok {
		return false
	}
	delete(o.vals, key)
	for i, k := range o.keys {
		if k == key {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
	return true
}

// MarshalJSON writes the object compactly, members in order.
func (o *Object) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, err := encode(k)
		if err != nil {
			return nil, err
		}
		b.Write(kb)
		b.WriteByte(':')
		b.Write(o.vals[k])
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// Equal reports whether two objects hold the same members in the same order
// with the same JSON, compared compactly.
func Equal(a, b *Object) bool {
	x, _ := a.MarshalJSON()
	y, _ := b.MarshalJSON()
	var cx, cy bytes.Buffer
	if json.Compact(&cx, x) != nil || json.Compact(&cy, y) != nil {
		return false
	}
	return bytes.Equal(cx.Bytes(), cy.Bytes())
}

func encode(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// File is a loaded settings.json.
type File struct {
	Path    string
	Root    *Object
	Exists  bool
	indent  string
	mode    os.FileMode
	Content []byte // the bytes read, for a restore
}

// Load reads dir's settings.json. A missing file is an empty object.
func Load(dir string) (*File, error) {
	path := filepath.Join(dir, FileName)
	f := &File{Path: path, indent: "  ", mode: 0o644}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		f.Root = NewObject()
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	if info, serr := os.Stat(path); serr == nil {
		f.mode = info.Mode().Perm()
	}
	if len(bytes.TrimSpace(data)) == 0 {
		f.Root = NewObject()
	} else if f.Root, err = ParseObject(data); err != nil {
		return nil, fmt.Errorf("%s is not a JSON object: %w", path, err)
	}
	f.Exists, f.Content = true, data
	f.indent = detectIndent(data)
	return f, nil
}

// detectIndent returns the indentation of the first indented line, so a
// rewrite keeps the file's own style; two spaces when there is none.
func detectIndent(data []byte) string {
	for _, line := range strings.Split(string(data), "\n")[1:] {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed != "" && len(trimmed) < len(line) {
			return line[:len(line)-len(trimmed)]
		}
	}
	return "  "
}

// Bytes renders the file as it would be written.
func (f *File) Bytes() ([]byte, error) {
	compact, err := f.Root.MarshalJSON()
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, compact, "", f.indent); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

// Write replaces the file through a temporary file and a rename.
func (f *File) Write() error {
	data, err := f.Bytes()
	if err != nil {
		return err
	}
	return WriteAtomic(f.Path, data, f.mode)
}

// Restore puts back what Load read: the old bytes, or no file.
func (f *File) Restore() error {
	if !f.Exists {
		err := os.Remove(f.Path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return WriteAtomic(f.Path, f.Content, f.mode)
}

// WriteAtomic writes data to path through a temporary file in the same
// directory and a rename.
func WriteAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
