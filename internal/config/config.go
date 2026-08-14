package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var (
	PlaybooksDir string
	LauncherDir  string
)

// ResolveLauncherDir returns the directory launcher commands are written to:
// the --launcher-dir flag, then $CLAUDE_LAUNCHER_DIR, then the directory of
// the running binary — which is on PATH by construction whenever the tool
// itself was invoked by name. A system-installed binary (e.g. in
// /usr/local/bin) sits in a directory an unprivileged user cannot write, so
// an unwritable binary dir falls back to ~/.local/bin.
func ResolveLauncherDir() (string, error) {
	if LauncherDir != "" {
		return LauncherDir, nil
	}
	if v := os.Getenv("CLAUDE_LAUNCHER_DIR"); v != "" {
		return v, nil
	}
	// Prefer the directory of the command as INVOKED (argv[0] resolved via
	// PATH, without following the final symlink): os.Executable resolves
	// through symlinks, and for installs managed via a PATH symlink whose
	// target lives elsewhere (package/version managers), the target dir is
	// writable but not on PATH — launchers written there would be
	// unreachable.
	dir := invokedBinDir()
	if dir == "" {
		exe, err := os.Executable()
		if err != nil {
			return "", err
		}
		dir = filepath.Dir(exe)
	}
	if dirWritable(dir) {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	// Resolution only computes the path — creation happens on the write
	// path (launcher.Write), so read-only callers (list, --dry-run, a
	// cancelled uninstall prompt) never mutate the filesystem.
	return filepath.Join(home, ".local", "bin"), nil
}

// dirWritable reports whether the current user can create files in dir.
// Permission-bit inspection lies under ACLs and containers, so probe by
// creating (and removing) a temp file.
func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".cpb-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

func ResolvePlaybooksDir() string {
	if PlaybooksDir != "" {
		return PlaybooksDir
	}
	if v := os.Getenv("CLAUDE_PLAYBOOKS_DIR"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude-playbooks")
}

// invokedBinDir returns the directory of the command as the user reached it:
// the literal argv[0] directory, or its PATH entry — deliberately without
// resolving the final symlink. Empty when argv[0] cannot be located.
func invokedBinDir() string {
	argv0 := os.Args[0]
	var p string
	if strings.ContainsRune(argv0, os.PathSeparator) {
		p = argv0
	} else if lp, err := exec.LookPath(argv0); err == nil {
		p = lp
	} else {
		return ""
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	return filepath.Dir(p)
}
