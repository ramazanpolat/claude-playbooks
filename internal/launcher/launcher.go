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

// Script renders the launcher script content. The effective playbooks root
// is embedded as --playbooks-dir: `run` resolves the playbook by name inside
// that root, and a launcher created under a --playbooks-dir override would
// otherwise search the default root and die with "unknown playbook".
func Script(playbookName, configDir, playbooksDir, binPath string) string {
	return "#!/bin/sh\n" +
		markerPrefix + playbookName + "\n" +
		configPrefix + configDir + "\n" +
		"CLAUDE_CONFIG_DIR=" + quote(configDir) + " exec " + quote(binPath) +
		" --playbooks-dir " + quote(playbooksDir) + " run " + quote(playbookName) + " \"$@\"\n"
}

// Write installs (or refreshes) the launcher for a playbook as dir/cmdName.
// An existing file is only overwritten when it carries this package's marker;
// anything else returns ErrTaken.
func Write(dir, cmdName, playbookName, configDir, playbooksDir string) (string, error) {
	if strings.ContainsAny(cmdName, "/\x00") || cmdName == "" || cmdName == "." || cmdName == ".." {
		return "", fmt.Errorf("invalid command name %q", cmdName)
	}
	// A relative --playbooks-dir (./pb) baked in verbatim would resolve
	// against whatever directory the launcher is later run from.
	var err error
	if configDir, err = filepath.Abs(configDir); err != nil {
		return "", err
	}
	if playbooksDir, err = filepath.Abs(playbooksDir); err != nil {
		return "", err
	}
	binPath, err := BinPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, cmdName)
	content := []byte(Script(playbookName, configDir, playbooksDir, binPath))

	// Atomic claim: O_EXCL guarantees exactly one of two concurrent
	// creators wins; a plain stat-then-write would let the last writer
	// silently repoint the shared command at its own playbook.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err == nil {
		if _, werr := f.Write(content); werr != nil {
			f.Close()
			os.Remove(path)
			return "", werr
		}
		if cerr := f.Close(); cerr != nil {
			os.Remove(path)
			return "", cerr
		}
		return path, nil
	}
	if !errors.Is(err, os.ErrExist) {
		return "", err
	}

	// The name exists: refresh only a launcher that already belongs to this
	// playbook. Overwriting another playbook's launcher would silently
	// repoint a familiar command at a different isolated configuration.
	e, ok := parse(path)
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrTaken, path)
	}
	if e.ConfigDir != configDir {
		return "", fmt.Errorf("%w: %s belongs to playbook %q (%s)", ErrTaken, path, e.PlaybookName, e.ConfigDir)
	}
	// Replace via temp file + rename so readers never see a partial script.
	tmp, err := os.CreateTemp(dir, "."+cmdName+".tmp-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	if _, werr := tmp.Write(content); werr != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", werr
	}
	if cerr := tmp.Close(); cerr != nil {
		os.Remove(tmpName)
		return "", cerr
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		os.Remove(tmpName)
		return "", err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
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

// ListForPathPrefix returns every launcher in dir whose config dir is path
// or lives under it — the same predicate RemoveForPathPrefix deletes by, so
// previews and removals always agree.
func ListForPathPrefix(dir, path string) ([]Entry, error) {
	entries, err := List(dir)
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, e := range entries {
		if Under(e.ConfigDir, path) {
			out = append(out, e)
		}
	}
	return out, nil
}

// Lookup reports the launcher entry at dir/cmdName. exists is false when no
// file is present; foreign is true when a file exists but is not a launcher
// this tool generated.
func Lookup(dir, cmdName string) (e Entry, exists, foreign bool) {
	path := filepath.Join(dir, cmdName)
	if _, err := os.Lstat(path); err != nil {
		return Entry{}, false, false
	}
	e, ok := parse(path)
	return e, true, !ok
}

// RemoveForPathPrefix deletes every launcher in dir whose config dir is path
// or lives under it. It returns the removed entries.
func RemoveForPathPrefix(dir, path string) ([]Entry, error) {
	entries, err := ListForPathPrefix(dir, path)
	if err != nil {
		return nil, err
	}
	var removed []Entry
	for _, e := range entries {
		if err := os.Remove(e.Path); err != nil {
			return removed, err
		}
		removed = append(removed, e)
	}
	return removed, nil
}

// Under reports whether configDir is path or lives under it. Stored config
// dirs are always absolute (Write absolutizes), so a relative path — e.g. a
// caller still holding the literal `--playbooks-dir ./pb` — is absolutized
// before comparing, or the predicate would silently never match.
func Under(configDir, path string) bool {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return configDir == path || strings.HasPrefix(configDir, path+string(filepath.Separator))
}

func isOurs(path string) bool {
	_, ok := parse(path)
	return ok
}

func parse(path string) (Entry, bool) {
	// A launcher is 4 short lines; anything bigger is not ours. Stat before
	// reading — the launcher dir is often a populated bin directory, and
	// ReadFile on every neighboring executable would load whole binaries.
	if info, err := os.Stat(path); err != nil || info.Size() > 4096 {
		return Entry{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
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
