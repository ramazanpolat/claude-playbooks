// Package manifest reads and writes the .playbook TOML file inside a playbook
// directory. The presence of the file marks the directory as a playbook.
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
	Version     string  `toml:"version"`
	Name        string  `toml:"name"`
	Alias       string  `toml:"alias"`
	Subdir      string  `toml:"subdir"`
	Description string  `toml:"description"`
	Children    []Child `toml:"children"`
}

// Child is one entry in the [[children]] table.
type Child struct {
	Name        string `toml:"name"`
	Path        string `toml:"path"`
	Alias       string `toml:"alias"`
	Description string `toml:"description"`
}

// Read parses the .playbook file inside dir. Returns (nil, nil) if the file
// does not exist. Returns an error if the file exists but is invalid TOML or
// has structural problems (duplicate child names, etc.).
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
	if err := validateRelativePath(path, "subdir", m.Subdir); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, c := range m.Children {
		if c.Name == "" {
			return fmt.Errorf("invalid .playbook at %s: child entry missing 'name'", path)
		}
		if c.Path == "" {
			return fmt.Errorf("invalid .playbook at %s: child %q missing 'path'", path, c.Name)
		}
		if err := validateRelativePath(path, fmt.Sprintf("child %q path", c.Name), c.Path); err != nil {
			return err
		}
		if strings.Contains(c.Name, "/") {
			return fmt.Errorf("invalid .playbook at %s: child name %q must not contain '/'", path, c.Name)
		}
		if seen[c.Name] {
			return fmt.Errorf("invalid .playbook at %s: duplicate child name %q", path, c.Name)
		}
		seen[c.Name] = true
	}
	return nil
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

// WriteMinimal creates a new .playbook file with version and name fields.
func WriteMinimal(dir, name string) error {
	path := filepath.Join(dir, FileName)
	var b strings.Builder
	b.WriteString(`version = "0.1.0"` + "\n")
	if name != "" {
		fmt.Fprintf(&b, "name = %q\n", name)
	}
	return os.WriteFile(path, []byte(b.String()), 0644)
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
	for _, c := range m.Children {
		b.WriteString("\n[[children]]\n")
		fmt.Fprintf(&b, "name = %q\n", c.Name)
		fmt.Fprintf(&b, "path = %q\n", c.Path)
		if c.Alias != "" {
			fmt.Fprintf(&b, "alias = %q\n", c.Alias)
		}
		if c.Description != "" {
			fmt.Fprintf(&b, "description = %q\n", c.Description)
		}
	}
	return os.WriteFile(path, []byte(b.String()), 0644)
}
