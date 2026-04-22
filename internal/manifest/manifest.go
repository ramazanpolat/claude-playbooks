package manifest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Manifest struct {
	Name        string `json:"name"`
	Alias       string `json:"alias"`
	Subdir      string `json:"subdir"`
	Description string `json:"description"`
}

// Resolve finds and parses the appropriate .playbook manifest in dir.
// playbook is the value of --playbook flag (empty = auto-detect).
// Returns nil manifest (no error) when no manifest files are present.
func Resolve(dir, playbook string) (*Manifest, error) {
	if playbook != "" {
		path := filepath.Join(dir, playbook+".playbook")
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return nil, fmt.Errorf("no '%s.playbook' found in source root", playbook)
		}
		return parse(path)
	}

	// Check for .playbook (the default dotfile).
	defaultPath := filepath.Join(dir, ".playbook")
	if _, err := os.Stat(defaultPath); err == nil {
		return parse(defaultPath)
	}

	// Count *.playbook files.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var found []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".playbook") {
			found = append(found, e.Name())
		}
	}

	switch len(found) {
	case 0:
		return nil, nil // no manifest — caller uses source root directly
	case 1:
		fmt.Fprintf(os.Stderr, "Using manifest: %s\n", found[0])
		return parse(filepath.Join(dir, found[0]))
	default:
		msg := "Multiple playbooks found. Pick one with --playbook:\n"
		for _, f := range found {
			name := strings.TrimSuffix(f, ".playbook")
			msg += fmt.Sprintf("  %-20s (%s)\n", name, f)
		}
		return nil, fmt.Errorf("%s", strings.TrimRight(msg, "\n"))
	}
}

func parse(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("invalid manifest %s: %w", path, err)
	}
	return &m, nil
}
