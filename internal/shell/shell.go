// Package shell manages playbook aliases in the user's shell config file.
//
// Aliases are plain single-line `alias` definitions — no comment markers, no
// metadata, no registry. Discovery works by grepping lines for either the
// alias name or the CLAUDE_CONFIG_DIR path.
package shell

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// AliasEntry represents a single alias line found in the shell config.
type AliasEntry struct {
	AliasName string // e.g. "experiment"
	Line      string // the full alias line as written
	Path      string // the CLAUDE_CONFIG_DIR value (absolute, expanded)
}

// aliasRegex matches: [whitespace] alias name = ... CLAUDE_CONFIG_DIR=<path> ...
// Tolerates leading whitespace and any quote style around the command.
var aliasRegex = regexp.MustCompile(`^\s*alias\s+([A-Za-z_][A-Za-z0-9_-]*)\s*=`)

// Format returns the canonical alias line written by the tool.
func Format(aliasName, playbookDir string) string {
	return fmt.Sprintf("alias %s='CLAUDE_CONFIG_DIR=%s claude'", aliasName, playbookDir)
}

// ReadAll scans the shell config for every alias whose definition contains
// CLAUDE_CONFIG_DIR=<path>. Returns one entry per matching line (there may
// be duplicates with the same path or alias name).
func ReadAll(configFile string) ([]AliasEntry, error) {
	lines, err := readLines(configFile)
	if err != nil {
		return nil, err
	}
	home, _ := os.UserHomeDir()

	var entries []AliasEntry
	for _, line := range lines {
		m := aliasRegex.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		idx := strings.Index(line, "CLAUDE_CONFIG_DIR=")
		if idx < 0 {
			continue
		}
		val := line[idx+len("CLAUDE_CONFIG_DIR="):]
		if end := strings.IndexAny(val, " \t'\""); end >= 0 {
			val = val[:end]
		}
		if val == "" {
			continue
		}
		if strings.HasPrefix(val, "~/") {
			val = filepath.Join(home, val[2:])
		} else if strings.HasPrefix(val, "$HOME/") {
			val = filepath.Join(home, val[len("$HOME/"):])
		}
		entries = append(entries, AliasEntry{
			AliasName: m[1],
			Line:      line,
			Path:      val,
		})
	}
	return entries, nil
}

// FindByPath returns aliases whose CLAUDE_CONFIG_DIR path equals the given path.
func FindByPath(configFile, path string) ([]AliasEntry, error) {
	entries, err := ReadAll(configFile)
	if err != nil {
		return nil, err
	}
	want, _ := filepath.Abs(path)
	var matches []AliasEntry
	for _, e := range entries {
		have, _ := filepath.Abs(e.Path)
		if have == want {
			matches = append(matches, e)
		}
	}
	return matches, nil
}

// FindByAliasName returns the first alias entry whose alias name matches.
func FindByAliasName(configFile, aliasName string) (*AliasEntry, error) {
	entries, err := ReadAll(configFile)
	if err != nil {
		return nil, err
	}
	for i := range entries {
		if entries[i].AliasName == aliasName {
			return &entries[i], nil
		}
	}
	return nil, nil
}

// Write sets an alias: removes any existing lines for this alias name or this
// playbook path, then appends a fresh line. If the alias name is already in
// use by a different CLAUDE_CONFIG_DIR, it is silently overwritten.
func Write(configFile, aliasName, playbookDir string) error {
	lines, err := readLines(configFile)
	if err != nil {
		return err
	}

	absPlaybookDir, _ := filepath.Abs(playbookDir)
	var kept []string
	for _, line := range lines {
		if shouldDrop(line, aliasName, absPlaybookDir) {
			continue
		}
		kept = append(kept, line)
	}

	// Ensure a blank line separator before appending.
	if len(kept) > 0 && kept[len(kept)-1] != "" {
		kept = append(kept, "")
	}
	kept = append(kept, Format(aliasName, playbookDir))
	return writeLines(configFile, kept)
}

// RemoveByPath deletes every alias line whose CLAUDE_CONFIG_DIR matches the given path.
// Returns the number of lines removed.
func RemoveByPath(configFile, playbookDir string) (int, error) {
	lines, err := readLines(configFile)
	if err != nil {
		return 0, err
	}
	absPlaybookDir, _ := filepath.Abs(playbookDir)
	var kept []string
	removed := 0
	for _, line := range lines {
		if matchesPath(line, absPlaybookDir) {
			removed++
			continue
		}
		kept = append(kept, line)
	}
	if removed == 0 {
		return 0, nil
	}
	return removed, writeLines(configFile, kept)
}

// RemoveByAliasName deletes every alias line whose alias name matches.
// Returns the number of lines removed.
func RemoveByAliasName(configFile, aliasName string) (int, error) {
	lines, err := readLines(configFile)
	if err != nil {
		return 0, err
	}
	var kept []string
	removed := 0
	for _, line := range lines {
		if matchesAliasName(line, aliasName) {
			removed++
			continue
		}
		kept = append(kept, line)
	}
	if removed == 0 {
		return 0, nil
	}
	return removed, writeLines(configFile, kept)
}

// RemoveByPathPrefix deletes every alias line whose CLAUDE_CONFIG_DIR starts
// with the given prefix (used when deleting a container to clean up all nested
// playbook aliases).
func RemoveByPathPrefix(configFile, prefix string) (int, error) {
	lines, err := readLines(configFile)
	if err != nil {
		return 0, err
	}
	absPrefix, _ := filepath.Abs(prefix)
	// Ensure trailing slash so prefix match is directory-bounded.
	if !strings.HasSuffix(absPrefix, string(filepath.Separator)) {
		absPrefix += string(filepath.Separator)
	}
	var kept []string
	removed := 0
	for _, line := range lines {
		if matchesPathPrefix(line, absPrefix) {
			removed++
			continue
		}
		kept = append(kept, line)
	}
	if removed == 0 {
		return 0, nil
	}
	return removed, writeLines(configFile, kept)
}

// RewritePathPrefix updates every alias line whose CLAUDE_CONFIG_DIR starts
// with oldPrefix so it starts with newPrefix instead. Used by `rename`.
func RewritePathPrefix(configFile, oldPrefix, newPrefix string) (int, error) {
	lines, err := readLines(configFile)
	if err != nil {
		return 0, err
	}
	absOld, _ := filepath.Abs(oldPrefix)
	absNew, _ := filepath.Abs(newPrefix)
	changed := 0
	for i, line := range lines {
		rewritten, ok := rewriteLinePathPrefix(line, absOld, absNew)
		if ok {
			lines[i] = rewritten
			changed++
		}
	}
	if changed == 0 {
		return 0, nil
	}
	return changed, writeLines(configFile, lines)
}

// --- internals ---

func shouldDrop(line, aliasName, absPlaybookDir string) bool {
	return matchesAliasName(line, aliasName) || matchesPath(line, absPlaybookDir)
}

func matchesAliasName(line, aliasName string) bool {
	m := aliasRegex.FindStringSubmatch(line)
	return m != nil && m[1] == aliasName
}

func matchesPath(line, absPath string) bool {
	if !aliasRegex.MatchString(line) {
		return false
	}
	have := extractPath(line)
	if have == "" {
		return false
	}
	abs, _ := filepath.Abs(have)
	return abs == absPath
}

func matchesPathPrefix(line, absPrefixWithSlash string) bool {
	if !aliasRegex.MatchString(line) {
		return false
	}
	have := extractPath(line)
	if have == "" {
		return false
	}
	abs, _ := filepath.Abs(have)
	abs += string(filepath.Separator)
	return strings.HasPrefix(abs, absPrefixWithSlash)
}

func rewriteLinePathPrefix(line, absOld, absNew string) (string, bool) {
	if !aliasRegex.MatchString(line) {
		return line, false
	}
	have := extractPath(line)
	if have == "" {
		return line, false
	}
	abs, _ := filepath.Abs(have)
	oldWithSlash := absOld + string(filepath.Separator)
	absWithSlash := abs + string(filepath.Separator)

	var newPath string
	switch {
	case abs == absOld:
		newPath = absNew
	case strings.HasPrefix(absWithSlash, oldWithSlash):
		newPath = absNew + strings.TrimPrefix(abs, absOld)
	default:
		return line, false
	}

	// Replace the raw value of CLAUDE_CONFIG_DIR= in the line with the new absolute path.
	idx := strings.Index(line, "CLAUDE_CONFIG_DIR=")
	if idx < 0 {
		return line, false
	}
	prefix := line[:idx+len("CLAUDE_CONFIG_DIR=")]
	rest := line[idx+len("CLAUDE_CONFIG_DIR="):]
	end := strings.IndexAny(rest, " \t'\"")
	if end < 0 {
		end = len(rest)
	}
	after := rest[end:]
	return prefix + newPath + after, true
}

func extractPath(line string) string {
	idx := strings.Index(line, "CLAUDE_CONFIG_DIR=")
	if idx < 0 {
		return ""
	}
	val := line[idx+len("CLAUDE_CONFIG_DIR="):]
	if end := strings.IndexAny(val, " \t'\""); end >= 0 {
		val = val[:end]
	}
	home, _ := os.UserHomeDir()
	if strings.HasPrefix(val, "~/") {
		val = filepath.Join(home, val[2:])
	} else if strings.HasPrefix(val, "$HOME/") {
		val = filepath.Join(home, val[len("$HOME/"):])
	}
	return val
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
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines, nil
}

func writeLines(path string, lines []string) error {
	content := strings.Join(lines, "\n") + "\n"
	return os.WriteFile(path, []byte(content), 0644)
}
