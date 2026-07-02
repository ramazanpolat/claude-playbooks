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
	"github.com/ramazanpolat/claude-playbooks/internal/shell"
)

// Playbook represents a discovered playbook.
type Playbook struct {
	Name        string             // directory name under the playbooks root
	Path        string             // absolute Claude config directory path
	RootPath    string             // absolute installed root directory path; same as Path unless a manifest subdir is set
	Alias       string             // alias name, "" if none
	AliasLine   string             // full alias line, "" if none
	LastUsed    time.Time          // directory mtime
	Manifest    *manifest.Manifest // nil when the directory has no .playbook
	Description string             // resolved from manifest, if any
}

func (p *Playbook) HasAlias() bool { return p.Alias != "" }

// Discover returns all playbooks under playbooksDir, enriched with alias info.
// Playbooks are sorted alphabetically by name.
func Discover(playbooksDir, shellConfig string) ([]*Playbook, error) {
	pbs, err := discover(playbooksDir)
	if err != nil {
		return nil, err
	}
	sort.Slice(pbs, func(i, j int) bool { return pbs[i].Name < pbs[j].Name })

	aliases, _ := shell.ReadAll(shellConfig)
	for _, pb := range pbs {
		attachAlias(pb, aliases)
	}
	return pbs, nil
}

// Find resolves a playbook by name. Returns (nil, nil) when not found.
func Find(playbooksDir, shellConfig, name string) (*Playbook, error) {
	all, err := Discover(playbooksDir, shellConfig)
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
func Require(playbooksDir, shellConfig, name string) (*Playbook, error) {
	pb, err := Find(playbooksDir, shellConfig, name)
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
		// A manifest is optional; if present it supplies metadata. An invalid
		// manifest is treated as absent so the directory stays discoverable;
		// the error surfaces via commands that load the manifest directly.
		m, _ := manifest.Read(path)
		configPath := path
		configInfo := info
		if m != nil {
			if resolved, resolvedInfo := resolveManifestSubdir(path, m); resolved != "" {
				configPath = resolved
				configInfo = resolvedInfo
			}
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

func resolveManifestSubdir(root string, m *manifest.Manifest) (string, os.FileInfo) {
	if m == nil || m.Subdir == "" {
		return "", nil
	}
	resolved := filepath.Join(root, filepath.FromSlash(m.Subdir))
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", nil
	}
	return resolved, info
}

func attachAlias(pb *Playbook, aliases []shell.AliasEntry) {
	absPath, _ := filepath.Abs(pb.Path)
	for _, a := range aliases {
		aAbs, _ := filepath.Abs(a.Path)
		if aAbs == absPath {
			pb.Alias = a.AliasName
			pb.AliasLine = a.Line
			return
		}
	}
}
