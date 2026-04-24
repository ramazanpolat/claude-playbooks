package manifest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Entry is one [[playbook]] block in a .playbook file.
type Entry struct {
	Name        string `toml:"name"`
	Alias       string `toml:"alias"`
	Subdir      string `toml:"subdir"`
	Description string `toml:"description"`
}

type file struct {
	Playbooks []Entry `toml:"playbook"`
}

// Resolve parses .playbook in dir and returns the selected entry.
// selector is the value of --playbook (empty = first entry).
// Returns nil with no error when no .playbook file exists.
func Resolve(dir, selector string) (*Entry, error) {
	path := filepath.Join(dir, ".playbook")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var f file
	if _, err := toml.Decode(string(data), &f); err != nil {
		return nil, fmt.Errorf("invalid .playbook: %w", err)
	}

	if len(f.Playbooks) == 0 {
		return nil, fmt.Errorf(".playbook has no [[playbook]] entries")
	}

	if selector == "" {
		return &f.Playbooks[0], nil
	}

	for i := range f.Playbooks {
		if f.Playbooks[i].Name == selector {
			return &f.Playbooks[i], nil
		}
	}

	var names []string
	for _, e := range f.Playbooks {
		if e.Name != "" {
			names = append(names, e.Name)
		}
	}
	if len(names) > 0 {
		return nil, fmt.Errorf("no playbook %q in .playbook. Available: %s", selector, strings.Join(names, ", "))
	}
	return nil, fmt.Errorf("no playbook %q found in .playbook", selector)
}
