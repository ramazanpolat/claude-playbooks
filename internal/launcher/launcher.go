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
	"os/exec"
	"path/filepath"
	"strings"
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

// BinPath returns the fully resolved absolute path of the running binary,
// used for OWNERSHIP checks: a launcher is ours when it resolves here.
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

// TargetPath returns the path launchers point AT: the command as invoked
// (argv[0], or its PATH entry), with the final symlink deliberately NOT
// resolved. Package managers repoint that stable entry on upgrade — a
// launcher targeting the resolved versioned binary would keep running the
// old version or dangle after cleanup, and the new binary would disown it.
func TargetPath() (string, error) {
	argv0 := os.Args[0]
	var p string
	if strings.ContainsRune(argv0, os.PathSeparator) {
		p = argv0
	} else if lp, err := exec.LookPath(argv0); err == nil {
		p = lp
	} else {
		exe, err := os.Executable()
		if err != nil {
			return "", err
		}
		p = exe
	}
	return filepath.Abs(p)
}

// Write installs (or refreshes) the launcher symlink dir/cmdName -> binary.
// Creation is atomic-exclusive (os.Symlink fails on an existing name); an
// existing entry is replaced only when it is already a launcher, via a
// temporary link renamed over the old one so the command never dangles.
func Write(dir, cmdName string) (string, error) {
	if err := ValidateName(cmdName); err != nil {
		return "", err
	}
	target, err := TargetPath()
	if err != nil {
		return "", err
	}
	bin, err := BinPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, cmdName)

	err = os.Symlink(target, path)
	if err == nil {
		receipt(path)
		return path, nil
	}
	if !errors.Is(err, os.ErrExist) {
		return "", err
	}
	if !isOurs(path, bin) {
		return "", fmt.Errorf("%w: %s", ErrTaken, path)
	}
	if existing, rerr := os.Readlink(path); rerr == nil && existing == target {
		// Identical content, nothing to write. This also makes concurrent
		// creators converge without coordination. Still recorded: the link
		// may predate the receipt.
		receipt(path)
		return path, nil
	}
	// Ours but pointing elsewhere (e.g. at a versioned physical binary from
	// before the stable-entry policy): migrate by renaming a fresh link
	// over it. Tmp names carry an attempt counter so concurrent migrators
	// never collide.
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
		receipt(path)
		return path, nil
	}
	return "", fmt.Errorf("could not refresh launcher %s", path)
}

// receipt records a written launcher path, warning instead of failing:
// the symlink already exists, and a working command beats a complete
// ledger.
func receipt(path string) {
	if err := record(path); err != nil {
		fmt.Fprintf(os.Stderr, "warning: launcher created but not recorded in receipt: %v\n", err)
	}
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
	// Reserved or malformed names are never playbook launchers: a crafted
	// or imported manifest alias like "cpb" must not delete the CLI's own
	// shortcut symlink (which also resolves to this binary), and a
	// path-carrying name must not escape the launcher directory.
	if ValidateName(cmdName) != nil {
		return false, nil
	}
	e, exists, foreign := Lookup(dir, cmdName)
	if !exists || foreign {
		return false, nil
	}
	if err := os.Remove(e.Path); err != nil {
		return false, err
	}
	if err := unrecord(e.Path); err != nil {
		fmt.Fprintf(os.Stderr, "warning: launcher removed but receipt not updated: %v\n", err)
	}
	return true, nil
}

// isOurs reports whether path is a symlink resolving to this binary. A
// dangling link is never ours: claiming it by its target's basename would
// let cleanup delete a user's own `foo -> /old/tool/cpb`, and the binary is
// always alive while this code runs, so genuine launchers always resolve.
func isOurs(path, binPath string) bool {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return false
	}
	resolved, err := filepath.EvalSymlinks(path)
	return err == nil && resolved == binPath
}

// ValidateName reports whether cmdName may name a launcher. Exposed so
// callers can reject an impossible --alias before mutating anything.
func ValidateName(cmdName string) error {
	if cmdName == "" || cmdName == "." || cmdName == ".." ||
		filepath.Base(cmdName) != cmdName {
		return fmt.Errorf("invalid command name %q", cmdName)
	}
	if ReservedNames[cmdName] {
		return fmt.Errorf("command name %q is reserved for the CLI itself", cmdName)
	}
	return nil
}
