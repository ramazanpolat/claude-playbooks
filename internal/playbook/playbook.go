// Package playbook discovers and describes playbooks on disk.
//
// Discovery is flat: every direct child directory of the playbooks root is
// exactly one playbook. A .playbook manifest is optional and supplies metadata
// only; a bare directory is a perfectly valid playbook. There is no nesting and
// no notion of child playbooks.
package playbook

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// Playbook represents a discovered playbook.
type Playbook struct {
	Name        string             // directory name under the playbooks root
	Path        string             // absolute Claude config directory path
	RootPath    string             // absolute installed root directory path; same as Path unless a manifest subdir is set
	LastUsed    time.Time          // directory mtime
	Manifest    *manifest.Manifest // nil when the directory has no .playbook
	Description string             // resolved from manifest, if any
}

// NewNamePattern is the charset for a NEW playbook name: alphanumerics,
// underscore and dash, starting with an alphanumeric or underscore.
//
// A playbook name is not just a directory name. It is interpolated into a
// generated shell alias, into that alias's `run <name>` argument, and into
// commands printed for the pilot to paste. Permitting shell metacharacters made
// every one of those an encoding problem -- an apostrophe alone was a command
// injection into the pilot's shell config. Rejecting the name at the front door
// removes the whole class instead of escaping it at each site, and matches the
// charset already required of launcher names.
//
// Deliberately applied to names being CREATED (create/rename/link/install), not
// to names being looked up: delete and the discovery paths keep using
// validateSinglePathSegment so an existing playbook with an odd name can still
// be listed, run and removed.
var NewNamePattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]*$`)

// Alias returns the playbook's manifest alias, "" if none.
func (p *Playbook) Alias() string {
	if p.Manifest != nil {
		return p.Manifest.Launcher
	}
	return ""
}

// Unreadable is a playbook directory whose manifest cannot be read. It is
// contained: the other playbooks stay usable, and anything that names this
// one gets Err, which says the file and the line.
type Unreadable struct {
	Name string // directory name under the playbooks root
	Path string // absolute directory path
	Err  error  // the manifest's read error
}

// UnreadableError is Discover's error when some manifest cannot be read:
// each one's own error, in name order.
type UnreadableError struct {
	List []Unreadable
}

func (e *UnreadableError) Error() string {
	msgs := make([]string, len(e.List))
	for i, u := range e.List {
		msgs[i] = u.Err.Error()
	}
	return strings.Join(msgs, "; ")
}

// Scan returns the playbooks whose manifests read and, apart, the ones whose
// manifests do not, each sorted by name. Only a root it cannot list is an
// error. Lists use it: they show what they can and report the rest.
func Scan(playbooksDir string) ([]*Playbook, []Unreadable, error) {
	pbs, bad, err := discover(playbooksDir)
	if err != nil {
		return nil, nil, err
	}
	sort.Slice(pbs, func(i, j int) bool { return pbs[i].Name < pbs[j].Name })
	sort.Slice(bad, func(i, j int) bool { return bad[i].Name < bad[j].Name })
	return pbs, bad, nil
}

// Discover returns all playbooks under playbooksDir, sorted alphabetically
// by name, or an *UnreadableError when any manifest cannot be read. It is
// for what needs the whole registry: a launcher name checked against every
// playbook, SHOW CREATE ALL.
func Discover(playbooksDir string) ([]*Playbook, error) {
	pbs, bad, err := Scan(playbooksDir)
	if err != nil {
		return nil, err
	}
	if len(bad) > 0 {
		return nil, &UnreadableError{List: bad}
	}
	return pbs, nil
}

// Find resolves a playbook by name. Returns (nil, nil) when not found. A
// playbook whose manifest cannot be read is its read error; another
// playbook's unreadable manifest does not matter.
func Find(playbooksDir, name string) (*Playbook, error) {
	all, bad, err := Scan(playbooksDir)
	if err != nil {
		return nil, err
	}
	for _, u := range bad {
		if u.Name == name {
			return nil, u.Err
		}
	}
	for _, pb := range all {
		if pb.Name == name {
			return pb, nil
		}
	}
	return nil, nil
}

// Require returns a playbook or a user-facing error.
func Require(playbooksDir, name string) (*Playbook, error) {
	pb, err := Find(playbooksDir, name)
	if err != nil {
		return nil, err
	}
	if pb == nil {
		return nil, fmt.Errorf("unknown playbook %q. `cpb SHOW PLAYBOOKS` lists them", name)
	}
	return pb, nil
}

// --- internals ---

func discover(root string) ([]*Playbook, []Unreadable, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	var out []*Playbook
	var bad []Unreadable
	for _, e := range entries {
		if !e.IsDir() && (e.Type()&os.ModeSymlink) == 0 {
			continue
		}
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		path := filepath.Join(root, e.Name())
		// Resolve symlinks for stat to detect directory through links.
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			continue
		}
		m, err := manifest.Read(path)
		if err != nil {
			bad = append(bad, Unreadable{Name: e.Name(), Path: path, Err: err})
			continue
		}
		pb := &Playbook{
			Name:     e.Name(),
			Path:     path,
			RootPath: path,
			LastUsed: info.ModTime(),
			Manifest: m,
		}
		if m != nil {
			pb.Description = m.Description
		}
		out = append(out, pb)
	}
	return out, bad, nil
}
