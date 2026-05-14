// Package playbook discovers and describes playbooks on disk.
//
// A directory is a playbook if it contains a .playbook file. Discovery looks
// at the immediate children of the playbooks root only. A top-level playbook's
// .playbook may declare child playbooks under a [[children]] table; those
// children are exposed as <top-level>/<child-name>.
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
	Name        string    // top-level: "experiment". child: "awesome/dba".
	Path        string    // absolute Claude config directory path
	RootPath    string    // absolute installed root directory path; same as Path for flat playbooks
	Alias       string    // alias name, "" if none
	AliasLine   string    // full alias line, "" if none
	LastUsed    time.Time // directory mtime
	IsChild     bool      // true if this playbook is a child of another
	Parent      string    // top-level name; "" for top-level playbooks
	ChildSpec   *manifest.Child
	Manifest    *manifest.Manifest // nil for children that have no own manifest
	Description string             // resolved from manifest (own > parent's children entry)
}

func (p *Playbook) HasAlias() bool { return p.Alias != "" }

// Discover returns all playbooks under playbooksDir, enriched with alias info.
// Sorted: top-level playbooks alphabetically, with each top-level immediately
// followed by its children in declaration order.
func Discover(playbooksDir, shellConfig string) ([]*Playbook, error) {
	tops, err := discoverTopLevel(playbooksDir)
	if err != nil {
		return nil, err
	}
	sort.Slice(tops, func(i, j int) bool { return tops[i].Name < tops[j].Name })

	aliases, _ := shell.ReadAll(shellConfig)

	var out []*Playbook
	for _, top := range tops {
		attachAlias(top, aliases)
		out = append(out, top)
		if top.Manifest == nil {
			continue
		}
		for i := range top.Manifest.Children {
			c := &top.Manifest.Children[i]
			child := buildChild(top, c)
			if child == nil {
				continue
			}
			attachAlias(child, aliases)
			out = append(out, child)
		}
	}
	return out, nil
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

// Children returns the resolved child playbooks of a top-level playbook.
// Returns nil if pb is not a top-level playbook with a manifest declaring
// children.
func Children(playbooksDir, shellConfig string, pb *Playbook) []*Playbook {
	if pb == nil || pb.IsChild || pb.Manifest == nil {
		return nil
	}
	aliases, _ := shell.ReadAll(shellConfig)
	var out []*Playbook
	for i := range pb.Manifest.Children {
		c := &pb.Manifest.Children[i]
		child := buildChild(pb, c)
		if child == nil {
			continue
		}
		attachAlias(child, aliases)
		out = append(out, child)
	}
	return out
}

// --- internals ---

func discoverTopLevel(root string) ([]*Playbook, error) {
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
		if !manifest.Exists(path) {
			continue
		}
		m, err := manifest.Read(path)
		if err != nil {
			// Surface invalid manifest by skipping the entry but keep going so
			// other playbooks remain discoverable. The error will surface via
			// commands that load the manifest directly.
			continue
		}
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

func buildChild(parent *Playbook, c *manifest.Child) *Playbook {
	rootPath := parent.RootPath
	if rootPath == "" {
		rootPath = parent.Path
	}
	childPath := filepath.Join(rootPath, c.Path)
	info, err := os.Stat(childPath)
	if err != nil || !info.IsDir() {
		return nil
	}
	pb := &Playbook{
		Name:        parent.Name + "/" + c.Name,
		Path:        childPath,
		RootPath:    childPath,
		LastUsed:    info.ModTime(),
		IsChild:     true,
		Parent:      parent.Name,
		ChildSpec:   c,
		Description: c.Description,
	}
	if own, _ := manifest.Read(childPath); own != nil {
		pb.Manifest = own
		if pb.Description == "" {
			pb.Description = own.Description
		}
	}
	return pb
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
