// Package manifest reads and writes the .playbook TOML file inside a playbook
// directory. The file is optional and holds metadata only; a directory is a
// valid playbook with or without it.
package manifest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

const FileName = ".playbook"

// Manifest holds the parsed contents of a .playbook file.
type Manifest struct {
	Version     string `toml:"version"`
	Name        string `toml:"name"`
	Alias       string `toml:"alias"`
	Subdir      string `toml:"subdir"`
	Description string `toml:"description"`
	Homepage    string `toml:"homepage"`
	Author      string `toml:"author"`
	IsolateAuth bool   `toml:"isolate_auth"`
}

// Read parses the .playbook file inside dir. Returns (nil, nil) if the file
// does not exist. Returns an error if the file exists but is invalid TOML or
// has structural problems.
func Read(dir string) (*Manifest, error) {
	path := filepath.Join(dir, FileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var m Manifest
	if _, err := toml.Decode(string(data), &m); err != nil {
		return nil, fmt.Errorf("invalid .playbook at %s: %w", path, err)
	}
	if err := m.validate(path); err != nil {
		return nil, err
	}
	return &m, nil
}

// Exists reports whether dir contains a .playbook file.
func Exists(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, FileName))
	return err == nil
}

// validate checks structural invariants. Path existence is checked by callers
// that have access to the playbook directory.
func (m *Manifest) validate(path string) error {
	return validateRelativePath(path, "subdir", m.Subdir)
}

func validateRelativePath(manifestPath, field, value string) error {
	if value == "" {
		return nil
	}
	cleaned := filepath.Clean(value)
	if filepath.IsAbs(value) || cleaned == "." || strings.HasPrefix(cleaned, "..") {
		return fmt.Errorf("invalid .playbook at %s: %s must be a relative path below the playbook root", manifestPath, field)
	}
	return nil
}

// Write serializes a manifest to the .playbook file inside dir. Used by `link`
// after collecting metadata interactively.
func Write(dir string, m *Manifest) error {
	path := filepath.Join(dir, FileName)
	var b strings.Builder
	if m.Version != "" {
		fmt.Fprintf(&b, "version = %q\n", m.Version)
	} else {
		b.WriteString(`version = "0.1.0"` + "\n")
	}
	if m.Name != "" {
		fmt.Fprintf(&b, "name = %q\n", m.Name)
	}
	if m.Alias != "" {
		fmt.Fprintf(&b, "alias = %q\n", m.Alias)
	}
	if m.Subdir != "" {
		fmt.Fprintf(&b, "subdir = %q\n", m.Subdir)
	}
	if m.Description != "" {
		fmt.Fprintf(&b, "description = %q\n", m.Description)
	}
	if m.Homepage != "" {
		fmt.Fprintf(&b, "homepage = %q\n", m.Homepage)
	}
	if m.Author != "" {
		fmt.Fprintf(&b, "author = %q\n", m.Author)
	}
	if m.IsolateAuth {
		fmt.Fprintf(&b, "isolate_auth = true\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0644)
}
