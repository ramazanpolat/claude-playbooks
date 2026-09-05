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
	"unicode/utf8"

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

var tomlLinePattern = regexp.MustCompile(`line (\d+)`)

// SanitizeTOMLError reduces a TOML decode error to its line number. The
// parser quotes the offending text in its messages, and since [env.set] and
// profile values may be credentials, that text must never reach a terminal
// or a log through any command that reads the file.
func SanitizeTOMLError(err error) string {
	if m := tomlLinePattern.FindStringSubmatch(err.Error()); m != nil {
		return "TOML syntax error at line " + m[1] + " (content not shown)"
	}
	return "TOML syntax error (content not shown)"
}

// QuoteTOML renders s as a TOML basic string. Go's %q emits escapes TOML
// does not define (\a, \v, \x..), which turn a manifest holding such a value
// unreadable -- and an unreadable manifest aborts registry discovery for
// every command. Control characters go out as \uXXXX; s must be valid
// UTF-8, which validate enforces for env values before anything is written.
func QuoteTOML(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// ValidateEnvValue reports whether value can be stored AND passed on: TOML
// strings must be valid UTF-8, so a value that cannot be written faithfully
// is refused rather than corrupt the manifest; and a NUL byte, which TOML
// can carry as \u0000, would make os/exec reject the child's environment
// and fail every launch, so it is refused on read as well as on write.
func ValidateEnvValue(key, value string) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("value of %s is not valid UTF-8 and cannot be stored in a manifest", key)
	}
	if strings.ContainsRune(value, 0) {
		return fmt.Errorf("value of %s contains a NUL byte, which cannot be passed in an environment", key)
	}
	return nil
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
		return nil, fmt.Errorf("invalid .playbook at %s: %s", path, SanitizeTOMLError(err))
	}
	if err := m.validate(path); err != nil {
		return nil, err
	}
	return &m, nil
}

// Nearest returns the manifest governing dir: the one in dir itself, or the
// closest ancestor's. A config directory that is a manifest `subdir` has no
// manifest of its own; its install root's applies.
//
// An unreadable or invalid manifest on the way up does not stop the walk --
// the closest VALID manifest still governs, so a stray broken file in a
// subdir cannot silently switch off an install root's isolate_auth. The
// first such error is returned alongside whatever was found, so callers can
// report it. Returns (nil, nil) when no ancestor has a manifest.
func Nearest(dir string) (*Manifest, error) {
	var firstErr error
	for {
		m, err := Read(dir)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		if m != nil {
			return m, firstErr
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, firstErr
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
		for key, value := range m.Env.Set {
			if err := ValidateEnvKey(key); err != nil {
				return fmt.Errorf("invalid .playbook at %s: env.set: %w", path, err)
			}
			if err := ValidateEnvValue(key, value); err != nil {
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
		fmt.Fprintf(&b, "version = %s\n", QuoteTOML(m.Version))
	} else {
		b.WriteString(`version = "0.1.0"` + "\n")
	}
	if m.Name != "" {
		fmt.Fprintf(&b, "name = %s\n", QuoteTOML(m.Name))
	}
	if m.Alias != "" {
		fmt.Fprintf(&b, "alias = %s\n", QuoteTOML(m.Alias))
	}
	if m.Subdir != "" {
		fmt.Fprintf(&b, "subdir = %s\n", QuoteTOML(m.Subdir))
	}
	if m.Description != "" {
		fmt.Fprintf(&b, "description = %s\n", QuoteTOML(m.Description))
	}
	if m.Homepage != "" {
		fmt.Fprintf(&b, "homepage = %s\n", QuoteTOML(m.Homepage))
	}
	if m.Author != "" {
		fmt.Fprintf(&b, "author = %s\n", QuoteTOML(m.Author))
	}
	if m.IsolateAuth {
		fmt.Fprintf(&b, "isolate_auth = true\n")
	}
	if m.Source != nil {
		b.WriteString("\n[source]\n")
		if m.Source.Repository != "" {
			fmt.Fprintf(&b, "repository = %s\n", QuoteTOML(m.Source.Repository))
		}
		if m.Source.Branch != "" {
			fmt.Fprintf(&b, "branch = %s\n", QuoteTOML(m.Source.Branch))
		}
		if m.Source.Subdir != "" {
			fmt.Fprintf(&b, "subdir = %s\n", QuoteTOML(m.Source.Subdir))
		}
	}
	if m.Update != nil && len(m.Update.Preserve) > 0 {
		b.WriteString("\n[update]\npreserve = [")
		for i, rel := range m.Update.Preserve {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(QuoteTOML(rel))
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
				b.WriteString(QuoteTOML(name))
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
				b.WriteString(QuoteTOML(key))
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
				fmt.Fprintf(&b, "%s = %s\n", key, QuoteTOML(m.Env.Set[key]))
			}
		}
	}
	// Values under [env.set] can be bearer tokens or API keys, so a manifest
	// carrying any is written private, like an env profile. Existing files
	// are only ever tightened, never loosened.
	// An existing file keeps its mode exactly (as an in-place rewrite would
	// have), and is tightened to owner-only when values are present. It is
	// never loosened, whatever it was.
	perm := os.FileMode(0644)
	if info, err := os.Stat(path); err == nil {
		perm = info.Mode().Perm()
	}
	if m.Env != nil && len(m.Env.Set) > 0 {
		perm &= 0o600
	}
	return WritePrivate(path, []byte(b.String()), perm)
}

// WritePrivate replaces path with data through a temporary file created
// 0600 in the same directory and renamed into place. The content is never
// readable at a looser mode than perm, not even for the duration of the
// write: truncating an existing 0644 file in place and chmodding afterwards
// would expose a freshly written secret to any local reader in between.
func WritePrivate(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() { tmp.Close(); os.Remove(tmpPath) }
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}
