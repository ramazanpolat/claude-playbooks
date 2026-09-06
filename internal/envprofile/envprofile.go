// Package envprofile reads and writes shared env profiles: named set/unset
// blocks stored as TOML files under <playbooks root>/.env-profiles/ and
// referenced from playbook manifests by name. A profile is the reusable half
// of a playbook's [env] block -- "the proxy and model pins for provider X" --
// so ten playbooks can share one definition instead of ten copies.
//
// The directory is dot-prefixed so playbook discovery skips it. A profile
// file has the same shape as a manifest's [env] block, hoisted to top level:
//
//	description = "GLM 5.3 through the local router"
//	unset = ["CLAUDE_CODE_OAUTH_TOKEN"]
//
//	[set]
//	ANTHROPIC_BASE_URL = "http://router:1/v1"
package envprofile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// DirName is the profiles directory under the playbooks root.
const DirName = ".env-profiles"

// Dir returns the profiles directory for a playbooks root.
func Dir(playbooksDir string) string {
	return filepath.Join(playbooksDir, DirName)
}

// Profile is one named env layer.
type Profile struct {
	Name        string            `toml:"-"`
	Description string            `toml:"description,omitempty"`
	Set         map[string]string `toml:"set,omitempty"`
	Unset       []string          `toml:"unset,omitempty"`
}

// Env returns the profile as a manifest env layer.
func (p *Profile) Env() *manifest.Env {
	return &manifest.Env{Set: p.Set, Unset: p.Unset}
}

// Empty reports whether the profile declares no variables.
func (p *Profile) Empty() bool {
	return p == nil || (len(p.Set) == 0 && len(p.Unset) == 0)
}

// ErrProfile is matched (errors.Is) by every error Expand returns, whether
// the profile is missing, unreadable, malformed, or invalid. Launch paths
// treat all of them as fatal: running with a silently dropped layer could
// send traffic to the wrong endpoint with the wrong credentials, and a
// broken profile is no safer than an absent one.
var ErrProfile = errors.New("env profile cannot be resolved")

// MissingError reports a manifest referencing a profile that does not exist.
type MissingError struct {
	Name string
	Dir  string
}

func (e *MissingError) Error() string {
	return fmt.Sprintf("env profile %q not found in %s (create it with: claude-playbook env-profile %s set KEY=VALUE)", e.Name, e.Dir, e.Name)
}

func (e *MissingError) Is(target error) bool { return target == ErrProfile }

// ResolveError wraps a read or validation failure of a referenced profile.
type ResolveError struct {
	Name string
	Err  error
}

func (e *ResolveError) Error() string        { return fmt.Sprintf("env profile %q: %v", e.Name, e.Err) }
func (e *ResolveError) Unwrap() error        { return e.Err }
func (e *ResolveError) Is(target error) bool { return target == ErrProfile }

func path(dir, name string) string {
	return filepath.Join(dir, name+".toml")
}

// Read parses one profile. Returns (nil, nil) when it does not exist.
func Read(dir, name string) (*Profile, error) {
	if err := manifest.ValidateProfileName(name); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path(dir, name))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var p Profile
	if _, err := toml.Decode(string(data), &p); err != nil {
		return nil, fmt.Errorf("invalid env profile at %s: %s", path(dir, name), manifest.SanitizeTOMLError(err))
	}
	p.Name = name
	if err := validate(&p, path(dir, name)); err != nil {
		return nil, err
	}
	return &p, nil
}

func validate(p *Profile, at string) error {
	for key, value := range p.Set {
		if err := manifest.ValidateEnvKey(key); err != nil {
			return fmt.Errorf("invalid env profile at %s: set: %w", at, err)
		}
		if err := manifest.ValidateEnvValue(key, value); err != nil {
			return fmt.Errorf("invalid env profile at %s: set: %w", at, err)
		}
	}
	for _, key := range p.Unset {
		if err := manifest.ValidateEnvKey(key); err != nil {
			return fmt.Errorf("invalid env profile at %s: unset: %w", at, err)
		}
		if _, both := p.Set[key]; both {
			return fmt.Errorf("invalid env profile at %s: %s is both set and unset", at, key)
		}
	}
	return nil
}

// Write serializes a profile, creating the directory on first use. Keys are
// emitted sorted so a rewrite never reorders the file arbitrarily.
func Write(dir string, p *Profile) error {
	if err := manifest.ValidateProfileName(p.Name); err != nil {
		return err
	}
	at := path(dir, p.Name)
	if err := validate(p, at); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var b strings.Builder
	if p.Description != "" {
		fmt.Fprintf(&b, "description = %s\n", manifest.QuoteTOML(p.Description))
	}
	if len(p.Unset) > 0 {
		unset := append([]string(nil), p.Unset...)
		sort.Strings(unset)
		b.WriteString("unset = [")
		for i, key := range unset {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(manifest.QuoteTOML(key))
		}
		b.WriteString("]\n")
	}
	if len(p.Set) > 0 {
		keys := make([]string, 0, len(p.Set))
		for key := range p.Set {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("[set]\n")
		for _, key := range keys {
			fmt.Fprintf(&b, "%s = %s\n", key, manifest.QuoteTOML(p.Set[key]))
		}
	}
	// Through a 0600 temp file and a rename: a profile created 0644 by an
	// editor or shell redirection must not expose the value being written,
	// not even between a truncating write and a later chmod.
	return manifest.WritePrivate(at, []byte(b.String()), 0o600)
}

// Delete removes a profile file. Removing one that does not exist is not an
// error; the caller decides what a missing profile means.
func Delete(dir, name string) error {
	if err := manifest.ValidateProfileName(name); err != nil {
		return err
	}
	err := os.Remove(path(dir, name))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// List returns every profile in dir, sorted by name. A directory that does
// not exist lists nothing. An unparsable file is an error: listing must not
// hide the profile a launch is about to fail on.
func List(dir string) ([]*Profile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []*Profile
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".toml")
		if !ok || e.IsDir() {
			continue
		}
		p, err := Read(dir, name)
		if err != nil {
			return nil, err
		}
		if p != nil {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// DefaultMarker is the file under the profiles directory naming the registry
// default profile: applied under every playbook's own block, the bottom layer
// above the shell environment. Absent means no default.
const DefaultMarker = ".default"

// Default returns the registry default profile's name, "" when none is set.
// Only an ABSENT marker means none: an empty one, one holding an invalid
// name, or a dangling symlink is an error, because silently treating it as
// "no default" would let a launch proceed without the layer every playbook
// depends on (and with the machine-global token the default may unset).
func Default(dir string) (string, error) {
	marker := filepath.Join(dir, DefaultMarker)
	data, err := os.ReadFile(marker)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if _, lerr := os.Lstat(marker); lerr == nil {
				return "", fmt.Errorf("%s: dangling symlink", marker)
			}
			return "", nil
		}
		return "", err
	}
	name := strings.TrimSpace(string(data))
	if name == "" {
		return "", fmt.Errorf("%s: empty registry default marker", marker)
	}
	if err := manifest.ValidateProfileName(name); err != nil {
		return "", fmt.Errorf("%s: %w", marker, err)
	}
	return name, nil
}

// SetDefault records name as the registry default; the profile must exist.
func SetDefault(dir, name string) error {
	p, err := Read(dir, name)
	if err != nil {
		return err
	}
	if p == nil {
		return &MissingError{Name: name, Dir: dir}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return manifest.WritePrivate(filepath.Join(dir, DefaultMarker), []byte(name+"\n"), 0o600)
}

// ClearDefault removes the registry default; clearing an absent one is fine.
func ClearDefault(dir string) error {
	err := os.Remove(filepath.Join(dir, DefaultMarker))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// SameProfile reports whether two profile names address the same file, which
// on a case-insensitive filesystem they can while spelled differently.
func SameProfile(dir, a, b string) bool {
	ia, err := os.Stat(path(dir, a))
	if err != nil {
		return false
	}
	ib, err := os.Stat(path(dir, b))
	if err != nil {
		return false
	}
	return os.SameFile(ia, ib)
}

// ExpandWithDefault is Expand with the registry default profile, when one is
// set, layered UNDER e: default first, then e's profiles in order, then e's
// own set/unset. A default that is named but missing or broken refuses the
// launch like any other profile (errors.Is(err, ErrProfile)).
func ExpandWithDefault(dir string, e *manifest.Env) (*manifest.Env, error) {
	name, err := Default(dir)
	if err != nil {
		return nil, &ResolveError{Name: DefaultMarker, Err: err}
	}
	if name == "" {
		return Expand(dir, e)
	}
	base, err := Expand(dir, &manifest.Env{Profiles: []string{name}})
	if err != nil {
		return nil, err
	}
	own, err := Expand(dir, e)
	if err != nil {
		return nil, err
	}
	return manifest.MergeEnv(base, own), nil
}

// Expand resolves e's profiles from dir and flattens everything into one
// block: profiles in list order, then e's own set/unset on top. A nil or
// profile-less e is returned as is. Every error satisfies errors.Is(err,
// ErrProfile): a missing profile is a *MissingError, anything else a
// *ResolveError.
func Expand(dir string, e *manifest.Env) (*manifest.Env, error) {
	if e == nil || len(e.Profiles) == 0 {
		return e, nil
	}
	layers := make([]*manifest.Env, 0, len(e.Profiles)+1)
	for _, name := range e.Profiles {
		p, err := Read(dir, name)
		if err != nil {
			return nil, &ResolveError{Name: name, Err: err}
		}
		if p == nil {
			return nil, &MissingError{Name: name, Dir: dir}
		}
		layers = append(layers, p.Env())
	}
	layers = append(layers, &manifest.Env{Set: e.Set, Unset: e.Unset})
	return manifest.MergeEnv(layers...), nil
}
