// Package manifest reads the .playbook TOML file inside a playbook directory.
//
// The presence of the file marks the directory as a playbook. All fields are
// optional defaults and metadata.
package manifest

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

const FileName = ".playbook"

// Manifest holds the parsed contents of a .playbook file.
type Manifest struct {
	Version     string `toml:"version"`
	Name        string `toml:"name"`
	Alias       string `toml:"alias"`
	Description string `toml:"description"`
}

// Read parses the .playbook file inside dir. Returns (nil, nil) if the file
// does not exist. Returns an error if the file exists but is invalid TOML.
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
		return nil, fmt.Errorf("invalid .playbook at %s: %w", path, err)
	}
	return &m, nil
}

// WriteMinimal creates a new .playbook with just a version field. Used by
// `create` and `install` when no manifest is present in the source.
func WriteMinimal(dir string) error {
	path := filepath.Join(dir, FileName)
	content := `version = "0.1.0"
`
	return os.WriteFile(path, []byte(content), 0644)
}
