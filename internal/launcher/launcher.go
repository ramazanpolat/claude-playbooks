// Package launcher manages per-playbook launcher commands: symlinks to the
// claude-playbook binary placed in a directory on PATH. When the binary is
// invoked through such a link it sees the link's name in argv[0] and
// dispatches to `run <name>` — the multicall pattern used by busybox and
// git. The launcher itself carries no state: name resolution happens at
// invocation time against the live playbook registry, so there is nothing
// to go stale when playbooks are renamed, moved, or deleted, nothing to
// quote, and no script content to observe half-written.
package launcher

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrTaken is returned by Write when the target name exists in the launcher
// directory but is not a symlink to this binary.
var ErrTaken = errors.New("command name taken by a file this tool did not generate")

// ReservedNames are argv[0] values that always mean the CLI itself and may
// never name a launcher.
var ReservedNames = map[string]bool{
	"claude-playbook": true,
	"cpb":             true,
}

// Entry describes one launcher symlink found in the launcher directory.
type Entry struct {
	CmdName string // link name = the command the user types
	Path    string // absolute path of the symlink
	Target  string // what the link points to
}

// BinPath returns the resolved absolute path of the running binary — the
// symlink target embedded in launchers. Symlinks in argv[0] are resolved so
// a launcher always points at the real binary, not at another launcher.
func BinPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Abs(exe)
}

// Write installs (or refreshes) the launcher symlink dir/cmdName -> binary.
// Creation is atomic-exclusive (os.Symlink fails on an existing name); an
// existing entry is replaced only when it is already a launcher, via a
// temporary link renamed over the old one so the command never dangles.
func Write(dir, cmdName string) (string, error) {
	if err := validName(cmdName); err != nil {
		return "", err
	}
	target, err := BinPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, cmdName)

	err = os.Symlink(target, path)
	if err == nil {
		return path, nil
	}
	if !errors.Is(err, os.ErrExist) {
		return "", err
	}
	if !isOurs(path, target) {
		return "", fmt.Errorf("%w: %s", ErrTaken, path)
	}
	// Already resolves to this binary: identical content, nothing to write.
	// This also makes concurrent creators converge without coordination.
	if _, err := filepath.EvalSymlinks(path); err == nil {
		return path, nil
	}
	// Dangling launcher (the binary moved since it was created): replace by
	// renaming a fresh link over it (atomic on POSIX). Tmp names carry an
	// attempt counter so concurrent replacers never collide.
	for i := 0; i < 10; i++ {
		tmp := filepath.Join(dir, fmt.Sprintf(".%s.tmp-%d-%d", cmdName, os.Getpid(), i))
		if err := os.Symlink(target, tmp); err != nil {
			if errors.Is(err, os.ErrExist) {
				continue
			}
			return "", err
		}
		if err := os.Rename(tmp, path); err != nil {
			os.Remove(tmp)
			return "", err
		}
		return path, nil
	}
	return "", fmt.Errorf("could not refresh launcher %s", path)
}

// List returns every launcher symlink in dir pointing at this binary.
func List(dir string) ([]Entry, error) {
	target, err := BinPath()
	if err != nil {
		return nil, err
	}
	des, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Entry
	for _, de := range des {
		path := filepath.Join(dir, de.Name())
		if !isOurs(path, target) {
			continue
		}
		t, _ := os.Readlink(path)
		out = append(out, Entry{CmdName: de.Name(), Path: path, Target: t})
	}
	return out, nil
}

// Lookup reports the launcher at dir/cmdName. exists is false when no file
// is present; foreign is true when a file exists but is not a launcher.
func Lookup(dir, cmdName string) (e Entry, exists, foreign bool) {
	path := filepath.Join(dir, cmdName)
	if _, err := os.Lstat(path); err != nil {
		return Entry{}, false, false
	}
	target, err := BinPath()
	if err != nil || !isOurs(path, target) {
		return Entry{}, true, true
	}
	t, _ := os.Readlink(path)
	return Entry{CmdName: cmdName, Path: path, Target: t}, true, false
}

// Remove deletes the launcher dir/cmdName if it is one. It reports whether
// a launcher was removed; foreign files are left untouched.
func Remove(dir, cmdName string) (bool, error) {
	e, exists, foreign := Lookup(dir, cmdName)
	if !exists || foreign {
		return false, nil
	}
	if err := os.Remove(e.Path); err != nil {
		return false, err
	}
	return true, nil
}

// isOurs reports whether path is a symlink resolving to this binary.
func isOurs(path, binPath string) bool {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return false
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		// Dangling link: judge by the literal target's basename so cleanup
		// still recognizes launchers after the binary moved.
		t, rerr := os.Readlink(path)
		return rerr == nil && ReservedNames[filepath.Base(t)]
	}
	return resolved == binPath
}

func validName(cmdName string) error {
	if cmdName == "" || cmdName == "." || cmdName == ".." ||
		filepath.Base(cmdName) != cmdName {
		return fmt.Errorf("invalid command name %q", cmdName)
	}
	if ReservedNames[cmdName] {
		return fmt.Errorf("command name %q is reserved for the CLI itself", cmdName)
	}
	return nil
}
