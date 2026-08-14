// Package shell edits the user's shell rc files in the one narrow way this
// tool still touches them: removing the exact completion lines a user (or a
// pre-3.x installer) added. Playbook commands are launcher symlinks and
// manifest aliases — nothing here writes command definitions into rc files.
//
// Every editor resolves a symlinked rc file to its target (a stow/chezmoi
// link must keep being a link), takes an advisory flock beside the file, and
// replaces content atomically with the original mode preserved.
package shell

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// QuoteArg renders s as a single shell word. Use it for any value
// interpolated into a command the user is told to run.
func QuoteArg(s string) string { return shellQuote(s) }

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// RemoveExactLines deletes every line of configFile that exactly matches one
// of doomed — ONLY those lines: an adjacent comment is user-authored and
// survives. Returns the number of lines removed.
func RemoveExactLines(configFile string, doomed []string) (int, error) {
	set := make(map[string]bool, len(doomed))
	for _, l := range doomed {
		set[l] = true
	}
	configFile = resolveConfigPath(configFile)
	removed := 0
	err := withConfigLock(configFile, func() error {
		lines, err := readLines(configFile)
		if err != nil {
			return err
		}
		var kept []string
		n := 0
		for _, line := range lines {
			if set[line] {
				n++
				continue
			}
			kept = append(kept, line)
		}
		removed = n
		if removed == 0 {
			return nil
		}
		return writeLines(configFile, kept)
	})
	return removed, err
}

// CountExactLines reports how many lines of configFile exactly match one of
// doomed, without modifying anything. Preview counterpart of
// RemoveExactLines.
func CountExactLines(configFile string, doomed []string) (int, error) {
	set := make(map[string]bool, len(doomed))
	for _, l := range doomed {
		set[l] = true
	}
	lines, err := readLines(resolveConfigPath(configFile))
	if err != nil {
		return 0, err
	}
	n := 0
	for _, l := range lines {
		if set[l] {
			n++
		}
	}
	return n, nil
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
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	mode := os.FileMode(0644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".claude-playbook-shell-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func withConfigLock(configFile string, fn func() error) error {
	dir := filepath.Dir(configFile)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	lock, err := os.OpenFile(configFile+".claude-playbook.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return fn()
}

func resolveConfigPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	dir := filepath.Dir(path)
	if resolvedDir, err := filepath.EvalSymlinks(dir); err == nil {
		return filepath.Join(resolvedDir, filepath.Base(path))
	}
	return path
}
