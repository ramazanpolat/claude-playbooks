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

// Alias returns the playbook's manifest alias, "" if none.
func (p *Playbook) Alias() string {
	if p.Manifest != nil {
		return p.Manifest.Alias
	}
	return ""
}

// Discover returns all playbooks under playbooksDir, sorted alphabetically
// by name.
func Discover(playbooksDir string) ([]*Playbook, error) {
	pbs, err := discover(playbooksDir)
	if err != nil {
		return nil, err
	}
	sort.Slice(pbs, func(i, j int) bool { return pbs[i].Name < pbs[j].Name })
	return pbs, nil
}

// Find resolves a playbook by name. Returns (nil, nil) when not found.
func Find(playbooksDir, name string) (*Playbook, error) {
	all, err := Discover(playbooksDir)
	if err != nil {
		return nil, err
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
		return nil, fmt.Errorf("unknown playbook %q. Run 'claude-playbook list' to see available playbooks", name)
	}
	return pb, nil
}

// --- internals ---

func discover(root string) ([]*Playbook, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []*Playbook
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
			return nil, err
		}
		configPath := path
		configInfo := info
		if m != nil {
			resolved, resolvedInfo, err := resolveManifestSubdir(path, m)
			if err != nil {
				return nil, err
			}
			configPath = resolved
			configInfo = resolvedInfo
		}
		pb := &Playbook{
			Name:     e.Name(),
			Path:     configPath,
			RootPath: path,
			LastUsed: configInfo.ModTime(),
			Manifest: m,
		}
		if m != nil {
			pb.Description = m.Description
		}
		out = append(out, pb)
	}
	return out, nil
}

func resolveManifestSubdir(root string, m *manifest.Manifest) (string, os.FileInfo, error) {
	if m == nil || m.Subdir == "" {
		info, err := os.Stat(root)
		return root, info, err
	}
	resolved, err := manifest.ResolveSubdir(root, "subdir", m.Subdir)
	if err != nil {
		return "", nil, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", nil, err
	}
	return resolved, info, nil
}
