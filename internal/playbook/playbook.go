package playbook

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ramazanpolat/claude-playbooks/internal/shell"
)

type Playbook struct {
	Name      string
	Path      string
	AliasLine string // full alias line from shell config, "" if none
	Alias     string // just the alias name, "" if none
	LastUsed  time.Time
}

func (p *Playbook) HasAlias() bool {
	return p.AliasLine != ""
}

// Discover returns all playbooks in playbooksDir, enriched with alias info.
func Discover(playbooksDir, shellConfig string) ([]*Playbook, error) {
	entries, err := os.ReadDir(playbooksDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	allAliases, _ := shell.ReadAll(shellConfig)
	allByPath, _ := shell.ReadAllByPath(shellConfig)

	var pbs []*Playbook
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		info, _ := e.Info()
		pb := &Playbook{
			Name: name,
			Path: filepath.Join(playbooksDir, name),
		}
		if info != nil {
			pb.LastUsed = info.ModTime()
		}
		// Prefer managed (comment-marked) alias; fall back to path-based detection.
		if aliasLine, ok := allAliases[name]; ok {
			pb.AliasLine = aliasLine
			pb.Alias = shell.ExtractAliasName(aliasLine)
		} else if aliasLine, ok := allByPath[pb.Path]; ok {
			pb.AliasLine = aliasLine
			pb.Alias = shell.ExtractAliasName(aliasLine)
		}
		pbs = append(pbs, pb)
	}
	return pbs, nil
}

// Find returns a single playbook by name, or nil if not found.
func Find(playbooksDir, shellConfig, name string) (*Playbook, error) {
	path := filepath.Join(playbooksDir, name)
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	pb := &Playbook{
		Name:     name,
		Path:     path,
		LastUsed: info.ModTime(),
	}
	aliasLine, _ := shell.ReadAlias(shellConfig, name)
	if aliasLine != "" {
		pb.AliasLine = aliasLine
		pb.Alias = shell.ExtractAliasName(aliasLine)
	}
	return pb, nil
}

// Require returns a playbook or a user-facing error if not found.
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
