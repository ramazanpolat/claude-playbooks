package shell

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const commentPrefix = "# claude-playbook: "

func commentLine(name string) string {
	return commentPrefix + name
}

func AliasLine(aliasName, playbookDir string) string {
	return fmt.Sprintf("alias %s='CLAUDE_CONFIG_DIR=%s claude'", aliasName, playbookDir)
}

// ReadAlias returns the full alias line for a playbook, or "" if not set.
func ReadAlias(configFile, name string) (string, error) {
	lines, err := readLines(configFile)
	if err != nil {
		return "", err
	}
	target := commentLine(name)
	for i, line := range lines {
		if line == target && i+1 < len(lines) {
			return lines[i+1], nil
		}
	}
	return "", nil
}

// ReadAll returns a map of playbook name → full alias line for all managed aliases.
func ReadAll(configFile string) (map[string]string, error) {
	lines, err := readLines(configFile)
	if err != nil {
		return map[string]string{}, err
	}
	result := make(map[string]string)
	for i, line := range lines {
		if strings.HasPrefix(line, commentPrefix) {
			name := strings.TrimPrefix(line, commentPrefix)
			if i+1 < len(lines) {
				result[name] = lines[i+1]
			}
		}
	}
	return result, nil
}

// Write adds or updates the alias block for a playbook.
func Write(configFile, name, aliasName, playbookDir string) error {
	comment := commentLine(name)
	alias := AliasLine(aliasName, playbookDir)

	lines, err := readLines(configFile)
	if err != nil {
		return err
	}

	for i, line := range lines {
		if line == comment && i+1 < len(lines) {
			lines[i+1] = alias
			return writeLines(configFile, lines)
		}
	}

	// Not found — append.
	if len(lines) > 0 && lines[len(lines)-1] != "" {
		lines = append(lines, "")
	}
	lines = append(lines, comment, alias)
	return writeLines(configFile, lines)
}

// Remove removes the alias block for a playbook. Returns true if found.
func Remove(configFile, name string) (bool, error) {
	lines, err := readLines(configFile)
	if err != nil {
		return false, err
	}
	comment := commentLine(name)
	var out []string
	found := false
	for i := 0; i < len(lines); i++ {
		if lines[i] == comment {
			found = true
			i++ // skip the alias line too
			// drop preceding blank line if present
			if len(out) > 0 && out[len(out)-1] == "" {
				out = out[:len(out)-1]
			}
			continue
		}
		out = append(out, lines[i])
	}
	if !found {
		return false, nil
	}
	return true, writeLines(configFile, out)
}

// ReadAllByPath scans the shell config for any alias containing CLAUDE_CONFIG_DIR=<path>
// and returns a map of absPath → full alias line. This catches manually created aliases
// that don't have the managed comment marker.
func ReadAllByPath(configFile string) (map[string]string, error) {
	lines, err := readLines(configFile)
	if err != nil {
		return nil, err
	}
	home, _ := os.UserHomeDir()
	result := make(map[string]string)
	for _, line := range lines {
		if !strings.HasPrefix(line, "alias ") {
			continue
		}
		idx := strings.Index(line, "CLAUDE_CONFIG_DIR=")
		if idx < 0 {
			continue
		}
		val := line[idx+len("CLAUDE_CONFIG_DIR="):]
		// Value ends at next space, tab, quote, or end of string.
		if end := strings.IndexAny(val, " \t'\""); end >= 0 {
			val = val[:end]
		}
		// Expand leading ~.
		if strings.HasPrefix(val, "~/") {
			val = filepath.Join(home, val[2:])
		}
		if val != "" {
			result[val] = line
		}
	}
	return result, nil
}

// ExtractAliasName extracts the alias name from a line like:
// alias foo='CLAUDE_CONFIG_DIR=...'
func ExtractAliasName(line string) string {
	line = strings.TrimPrefix(line, "alias ")
	idx := strings.IndexByte(line, '=')
	if idx < 0 {
		return ""
	}
	return line[:idx]
}

func readLines(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	content := string(data)
	// Split but preserve trailing newline awareness.
	lines := strings.Split(content, "\n")
	// Remove the last empty element that Split produces when file ends with \n.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines, nil
}

func writeLines(path string, lines []string) error {
	content := strings.Join(lines, "\n") + "\n"
	return os.WriteFile(path, []byte(content), 0644)
}
