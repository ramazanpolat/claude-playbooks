// Package launcher manages per-playbook launcher commands: small executable
// #!/bin/sh scripts installed next to the claude-playbook binary, whose
// directory is on PATH by construction whenever the tool itself was invoked
// by name. Unlike shell aliases they work identically from any shell, need
// no rc-file edit and no reload, and are visible to non-interactive callers
// (scripts, cron, other tools).
package launcher

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrTaken is returned by Write when the target name exists in the launcher
// directory but is not a launcher this tool generated.
var ErrTaken = errors.New("command name taken by a file this tool did not generate")

const (
	markerPrefix = "# claude-playbook launcher for playbook: "
	configPrefix = "# config-dir: "
)

// Entry describes one launcher script found in the launcher directory.
type Entry struct {
	CmdName      string // file name = the command the user types
	Path         string // absolute path of the script
	PlaybookName string // from the marker line
	ConfigDir    string // from the config-dir line
}

// BinPath returns the absolute path of the running binary, for embedding in
// generated scripts.
func BinPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Abs(exe)
}

// Script renders the launcher script content.
func Script(playbookName, configDir, binPath string) string {
	return "#!/bin/sh\n" +
		markerPrefix + playbookName + "\n" +
		configPrefix + configDir + "\n" +
		"CLAUDE_CONFIG_DIR=" + quote(configDir) + " exec " + quote(binPath) + " run " + quote(playbookName) + " \"$@\"\n"
}

// Write installs (or refreshes) the launcher for a playbook as dir/cmdName.
// An existing file is only overwritten when it carries this package's marker;
// anything else returns ErrTaken.
func Write(dir, cmdName, playbookName, configDir string) (string, error) {
	if strings.ContainsAny(cmdName, "/\x00") || cmdName == "" || cmdName == "." || cmdName == ".." {
		return "", fmt.Errorf("invalid command name %q", cmdName)
	}
	binPath, err := BinPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, cmdName)
	if _, err := os.Lstat(path); err == nil && !isOurs(path) {
		return "", fmt.Errorf("%w: %s", ErrTaken, path)
	}
	if err := os.WriteFile(path, []byte(Script(playbookName, configDir, binPath)), 0o755); err != nil {
		return "", err
	}
	// WriteFile does not chmod an existing file.
	if err := os.Chmod(path, 0o755); err != nil {
		return "", err
	}
	return path, nil
}

// List returns every launcher script in dir.
func List(dir string) ([]Entry, error) {
	des, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Entry
	for _, de := range des {
		if de.IsDir() {
			continue
		}
		path := filepath.Join(dir, de.Name())
		if e, ok := parse(path); ok {
			out = append(out, e)
		}
	}
	return out, nil
}

// RemoveForPathPrefix deletes every launcher in dir whose config dir is path
// or lives under it. It returns the removed entries.
func RemoveForPathPrefix(dir, path string) ([]Entry, error) {
	entries, err := List(dir)
	if err != nil {
		return nil, err
	}
	var removed []Entry
	for _, e := range entries {
		if e.ConfigDir == path || strings.HasPrefix(e.ConfigDir, path+string(filepath.Separator)) {
			if err := os.Remove(e.Path); err != nil {
				return removed, err
			}
			removed = append(removed, e)
		}
	}
	return removed, nil
}

func isOurs(path string) bool {
	_, ok := parse(path)
	return ok
}

func parse(path string) (Entry, bool) {
	// A launcher is 4 short lines; anything bigger is not ours.
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 4096 {
		return Entry{}, false
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) < 3 || lines[0] != "#!/bin/sh" ||
		!strings.HasPrefix(lines[1], markerPrefix) || !strings.HasPrefix(lines[2], configPrefix) {
		return Entry{}, false
	}
	return Entry{
		CmdName:      filepath.Base(path),
		Path:         path,
		PlaybookName: strings.TrimPrefix(lines[1], markerPrefix),
		ConfigDir:    strings.TrimPrefix(lines[2], configPrefix),
	}, true
}

// quote renders s as a single shell word (POSIX single-quoting).
func quote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
