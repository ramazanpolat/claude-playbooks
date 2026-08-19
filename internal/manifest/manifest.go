// Package manifest reads and writes the .playbook TOML file inside a playbook
// directory. The file is optional and holds metadata only; a directory is a
// valid playbook with or without it.
package manifest

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

const FileName = ".playbook"

// Source holds provenance data used by native updates.
type Source struct {
	Repository string `toml:"repository,omitempty"`
	Branch     string `toml:"branch,omitempty"`
	Subdir     string `toml:"subdir,omitempty"`
}

// Update holds per-playbook update policy. Preserve names install-local files
// that must survive an update even though the source ships its own copy; the
// CLI already preserves settings.json and the Claude Code state files, so this
// is for anything beyond that. Paths are relative to the playbook root.
type Update struct {
	Preserve []string `toml:"preserve,omitempty"`
}

// Manifest holds the parsed contents of a .playbook file.
type Manifest struct {
	Version     string  `toml:"version"`
	Name        string  `toml:"name"`
	Alias       string  `toml:"alias"`
	Subdir      string  `toml:"subdir"`
	Description string  `toml:"description"`
	Homepage    string  `toml:"homepage"`
	Author      string  `toml:"author"`
	IsolateAuth bool    `toml:"isolate_auth"`
	Source      *Source `toml:"source,omitempty"`
	Update      *Update `toml:"update,omitempty"`
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
	if err := validateRelativePath(path, "subdir", m.Subdir); err != nil {
		return err
	}
	if m.Source != nil {
		if err := validateRelativePath(path, "source.subdir", m.Source.Subdir); err != nil {
			return err
		}
	}
	if m.Update != nil {
		for _, rel := range m.Update.Preserve {
			if err := validateRelativePath(path, "update.preserve", rel); err != nil {
				return err
			}
		}
	}
	return nil
}

// ValidateRelativePath reports whether value is a relative path that stays
// below a playbook root. manifestPath appears in the error text only.
func ValidateRelativePath(manifestPath, field, value string) error {
	return validateRelativePath(manifestPath, field, value)
}

func validateRelativePath(manifestPath, field, value string) error {
	if value == "" {
		return nil
	}
	cleaned := path.Clean(filepath.ToSlash(value))
	if filepath.IsAbs(value) || strings.HasPrefix(cleaned, "/") || cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("invalid .playbook at %s: %s must be a relative path below the playbook root", manifestPath, field)
	}
	return nil
}

// ResolveSubdir resolves a manifest or source subdirectory and verifies that
// symlinks do not escape the supplied root.
func ResolveSubdir(root, field, value string) (string, error) {
	if value == "" {
		return root, nil
	}
	candidate, err := ResolvePath(root, field, value)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(candidate)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("%s %q is not a directory below %s", field, value, root)
	}
	return candidate, nil
}

// ResolvePath resolves a relative path and verifies that symlinks keep it
// physically below root. The returned path retains root's lexical form.
func ResolvePath(root, field, value string) (string, error) {
	if err := validateRelativePath(filepath.Join(root, FileName), field, value); err != nil {
		return "", err
	}
	rootResolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	candidate := filepath.Join(root, filepath.FromSlash(value))
	candidateResolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("%s %q not found below %s: %w", field, value, root, err)
	}
	rel, err := filepath.Rel(rootResolved, candidateResolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s %q resolves outside %s", field, value, root)
	}
	return candidate, nil
}

// Write serializes a manifest to the .playbook file inside dir. Used by `link`
// after collecting metadata interactively.
func Write(dir string, m *Manifest) error {
	path := filepath.Join(dir, FileName)
	if err := m.validate(path); err != nil {
		return err
	}
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
	if m.Source != nil {
		b.WriteString("\n[source]\n")
		if m.Source.Repository != "" {
			fmt.Fprintf(&b, "repository = %q\n", m.Source.Repository)
		}
		if m.Source.Branch != "" {
			fmt.Fprintf(&b, "branch = %q\n", m.Source.Branch)
		}
		if m.Source.Subdir != "" {
			fmt.Fprintf(&b, "subdir = %q\n", m.Source.Subdir)
		}
	}
	if m.Update != nil && len(m.Update.Preserve) > 0 {
		b.WriteString("\n[update]\npreserve = [")
		for i, rel := range m.Update.Preserve {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%q", rel)
		}
		b.WriteString("]\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0644)
}
