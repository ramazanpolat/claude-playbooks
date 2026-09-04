// Package manifest reads and writes the .playbook TOML file inside a playbook
// directory. The file is optional and holds metadata only; a directory is a
// valid playbook with or without it.
package manifest

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
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

// Env holds per-install environment overrides applied by `run`, `start`, and
// launcher dispatch to the child claude process, after the process's own
// environment and before CLAUDE_CONFIG_DIR is bound. Set entries override
// inherited values; Unset entries are removed from the child's environment
// even when the shell exports them.
//
// The block is INSTALL-LOCAL state, like `alias`: `update` carries the live
// block forward and ignores the source's, and `install` drops a block the
// source ships. A playbook repository must not be able to point an install's
// ANTHROPIC_BASE_URL somewhere else by publishing a manifest.
//
// Unsetting CLAUDE_CODE_OAUTH_TOKEN has a documented side effect: the
// long-lived token is treated as inactive for that install, so the launch
// takes the stored-credentials path (no quarantine, no injection). Setting
// it supplies a per-install token that wins over the machine-global file.
//
// Profiles names shared env profiles (files under the playbooks root's
// .env-profiles/ directory) layered UNDER this block: profiles apply in
// list order, later ones overriding earlier, and the block's own Set/Unset
// apply last. Resolution happens at launch; the manifest records names only.
type Env struct {
	Profiles []string          `toml:"profiles,omitempty"`
	Set      map[string]string `toml:"set,omitempty"`
	Unset    []string          `toml:"unset,omitempty"`
}

var profileNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ValidateProfileName reports whether name can name an env profile file.
func ValidateProfileName(name string) error {
	if !profileNamePattern.MatchString(name) {
		return fmt.Errorf("invalid profile name %q: use letters, digits, dots, dashes, underscores", name)
	}
	return nil
}

// Uses reports whether profile is listed.
func (e *Env) Uses(profile string) bool {
	if e == nil {
		return false
	}
	for _, p := range e.Profiles {
		if p == profile {
			return true
		}
	}
	return false
}

// MergeEnv flattens layers into one block: each layer's Set entries override
// earlier values and cancel an earlier Unset of the same key; each layer's
// Unset entries drop earlier Set values. Profiles are not carried into the
// result -- callers resolve them into layers first. The result never lists a
// key in both Set and Unset, and Unset keeps first-seen order.
func MergeEnv(layers ...*Env) *Env {
	out := &Env{Set: map[string]string{}}
	for _, layer := range layers {
		if layer == nil {
			continue
		}
		for key, value := range layer.Set {
			out.Unset = dropKey(out.Unset, key)
			out.Set[key] = value
		}
		for _, key := range layer.Unset {
			delete(out.Set, key)
			if !out.Unsets(key) {
				out.Unset = append(out.Unset, key)
			}
		}
	}
	return out
}

func dropKey(list []string, key string) []string {
	out := list[:0:0]
	for _, k := range list {
		if k != key {
			out = append(out, k)
		}
	}
	return out
}

// ReservedEnvKeys cannot be set or unset through the manifest: the tool
// owns them and binds them after every override is applied.
var ReservedEnvKeys = map[string]bool{"CLAUDE_CONFIG_DIR": true}

var envKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidateEnvKey reports whether key is a well-formed, non-reserved
// environment variable name.
func ValidateEnvKey(key string) error {
	if !envKeyPattern.MatchString(key) {
		return fmt.Errorf("invalid environment variable name %q", key)
	}
	if ReservedEnvKeys[key] {
		return fmt.Errorf("%s is managed by claude-playbook and cannot be overridden", key)
	}
	return nil
}

// Empty reports whether the block declares nothing.
func (e *Env) Empty() bool {
	return e == nil || (len(e.Profiles) == 0 && len(e.Set) == 0 && len(e.Unset) == 0)
}

// Unsets reports whether key is listed for removal.
func (e *Env) Unsets(key string) bool {
	if e == nil {
		return false
	}
	for _, k := range e.Unset {
		if k == key {
			return true
		}
	}
	return false
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
	Env         *Env    `toml:"env,omitempty"`
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

// Nearest returns the manifest governing dir: the one in dir itself, or the
// closest ancestor's. A config directory that is a manifest `subdir` has no
// manifest of its own; its install root's applies. Returns (nil, nil) when no
// ancestor has one.
func Nearest(dir string) (*Manifest, error) {
	for {
		m, err := Read(dir)
		if err != nil {
			return nil, err
		}
		if m != nil {
			return m, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, nil
		}
		dir = parent
	}
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
	if m.Env != nil {
		for _, name := range m.Env.Profiles {
			if err := ValidateProfileName(name); err != nil {
				return fmt.Errorf("invalid .playbook at %s: env.profiles: %w", path, err)
			}
		}
		for key := range m.Env.Set {
			if err := ValidateEnvKey(key); err != nil {
				return fmt.Errorf("invalid .playbook at %s: env.set: %w", path, err)
			}
		}
		for _, key := range m.Env.Unset {
			if err := ValidateEnvKey(key); err != nil {
				return fmt.Errorf("invalid .playbook at %s: env.unset: %w", path, err)
			}
			if _, both := m.Env.Set[key]; both {
				return fmt.Errorf("invalid .playbook at %s: env: %s is both set and unset", path, key)
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
	if !m.Env.Empty() {
		// [env] must precede [env.set] in TOML; both are emitted in sorted
		// order so a rewrite never reorders a hand-edited file arbitrarily.
		b.WriteString("\n[env]\n")
		if len(m.Env.Profiles) > 0 {
			b.WriteString("profiles = [")
			for i, name := range m.Env.Profiles {
				if i > 0 {
					b.WriteString(", ")
				}
				fmt.Fprintf(&b, "%q", name)
			}
			b.WriteString("]\n")
		}
		if len(m.Env.Unset) > 0 {
			unset := append([]string(nil), m.Env.Unset...)
			sort.Strings(unset)
			b.WriteString("unset = [")
			for i, key := range unset {
				if i > 0 {
					b.WriteString(", ")
				}
				fmt.Fprintf(&b, "%q", key)
			}
			b.WriteString("]\n")
		}
		if len(m.Env.Set) > 0 {
			keys := make([]string, 0, len(m.Env.Set))
			for key := range m.Env.Set {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			b.WriteString("\n[env.set]\n")
			for _, key := range keys {
				fmt.Fprintf(&b, "%s = %q\n", key, m.Env.Set[key])
			}
		}
	}
	return os.WriteFile(path, []byte(b.String()), 0644)
}
