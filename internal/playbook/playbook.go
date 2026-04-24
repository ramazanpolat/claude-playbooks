// Package playbook discovers and describes playbooks on disk.
//
// A playbook is any directory under the playbooks root that contains a
// `.playbook` file. Discovery walks to a maximum of two levels deep. Once a
// `.playbook` is found at a given level, discovery does not descend into that
// directory.
package playbook

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ramazanpolat/claude-playbooks/internal/shell"
)

// manifestFile is the marker that identifies a directory as a playbook.
const manifestFile = ".playbook"

// Playbook is a discovered playbook on disk.
type Playbook struct {
	Name      string    // path relative to playbooks root, e.g. "experiment" or "multi-repo/work"
	Path      string    // absolute directory path
	Alias     string    // alias name, "" if none
	AliasLine string    // full alias line from shell config, "" if none
	LastUsed  time.Time // directory mtime
}

func (p *Playbook) HasAlias() bool {
	return p.Alias != ""
}

// IsPlaybook returns true if dir contains a .playbook file.
func IsPlaybook(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, manifestFile))
	return err == nil
}

// Discover returns all playbooks under playbooksDir using the 2-level rule,
// enriched with alias info from shellConfig.
func Discover(playbooksDir, shellConfig string) ([]*Playbook, error) {
	names, err := discoverNames(playbooksDir)
	if err != nil {
		return nil, err
	}
	sort.Strings(names)

	aliases, _ := shell.ReadAll(shellConfig)

	var pbs []*Playbook
	for _, name := range names {
		path := filepath.Join(playbooksDir, name)
		info, _ := os.Stat(path)
		pb := &Playbook{
			Name: name,
			Path: path,
		}
		if info != nil {
			pb.LastUsed = info.ModTime()
		}
		attachAlias(pb, aliases)
		pbs = append(pbs, pb)
	}
	return pbs, nil
}

// DiscoverUnder walks inside a single top-level directory, returning playbook
// names relative to playbooksDir. Used by `install` to report what was found.
func DiscoverUnder(playbooksDir, topLevel string) ([]string, error) {
	topPath := filepath.Join(playbooksDir, topLevel)
	info, err := os.Stat(topPath)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", topPath)
	}
	var out []string
	if IsPlaybook(topPath) {
		out = append(out, topLevel)
		return out, nil
	}
	entries, err := os.ReadDir(topPath)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		sub := filepath.Join(topPath, e.Name())
		if IsPlaybook(sub) {
			out = append(out, topLevel+"/"+e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// Find resolves a playbook by name. Returns nil (no error) when not found.
func Find(playbooksDir, shellConfig, name string) (*Playbook, error) {
	path := filepath.Join(playbooksDir, name)
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if !info.IsDir() {
		return nil, nil
	}
	if !IsPlaybook(path) {
		return nil, nil
	}
	pb := &Playbook{
		Name:     name,
		Path:     path,
		LastUsed: info.ModTime(),
	}
	aliases, _ := shell.ReadAll(shellConfig)
	attachAlias(pb, aliases)
	return pb, nil
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

// ResolveTarget resolves a name to either a playbook or a container directory.
// Used by commands that accept both (update, delete).
type Target struct {
	Name        string
	Path        string
	IsPlaybook  bool
	IsContainer bool
}

func ResolveTarget(playbooksDir, name string) (*Target, error) {
	path := filepath.Join(playbooksDir, name)
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%q not found under %s", name, playbooksDir)
		}
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", path)
	}
	t := &Target{Name: name, Path: path}
	if IsPlaybook(path) {
		t.IsPlaybook = true
		return t, nil
	}
	// Check if it contains playbooks (2-level rule).
	kids, _ := DiscoverUnder(playbooksDir, name)
	if len(kids) > 0 {
		t.IsContainer = true
		return t, nil
	}
	// Directory but neither playbook nor container; treat as container with no children.
	t.IsContainer = true
	return t, nil
}

// --- internals ---

func discoverNames(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		topPath := filepath.Join(root, e.Name())
		if IsPlaybook(topPath) {
			out = append(out, e.Name())
			continue
		}
		// Descend one level.
		kids, err := os.ReadDir(topPath)
		if err != nil {
			continue
		}
		for _, k := range kids {
			if !k.IsDir() {
				continue
			}
			if strings.HasPrefix(k.Name(), ".") {
				continue
			}
			if IsPlaybook(filepath.Join(topPath, k.Name())) {
				out = append(out, e.Name()+"/"+k.Name())
			}
		}
	}
	return out, nil
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
