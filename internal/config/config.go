package config

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
)

var (
	PlaybooksDir string
	ShellConfig  string
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
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(exe)
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

func ResolveShellConfig() (string, error) {
	if ShellConfig != "" {
		return ShellConfig, nil
	}
	if v := os.Getenv("CLAUDE_SHELL_CONFIG"); v != "" {
		return v, nil
	}
	return detectShellConfig()
}

// rcForShell maps a shell path to the rc file its interactive sessions read.
// ok is false when the shell is empty or unrecognized.
func rcForShell(shell, home string) (path string, ok bool) {
	switch {
	case strings.Contains(shell, "zsh"):
		return filepath.Join(home, ".zshrc"), true
	case strings.Contains(shell, "bash"):
		return filepath.Join(home, ".bashrc"), true
	case strings.Contains(shell, "fish"):
		return filepath.Join(home, ".config", "fish", "config.fish"), true
	}
	// /bin/sh (the useradd default on Debian) and dash never read .bashrc;
	// their login shells read .profile, so the alias must go there.
	switch filepath.Base(shell) {
	case "sh", "dash":
		return filepath.Join(home, ".profile"), true
	}
	return "", false
}

// loginShellFunc resolves the current user's login shell from the user
// database. Overridable in tests.
var loginShellFunc = loginShell

// loginShell returns the current user's login shell from the user database,
// or "" if it cannot be determined. On Darwin accounts live in Directory
// Services, so dscl is authoritative there; getent covers Linux including
// NSS sources; the /etc/passwd scan covers minimal images without getent.
func loginShell() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}
	if runtime.GOOS == "darwin" {
		// Output: "UserShell: /bin/zsh"
		if out, err := exec.Command("dscl", ".", "-read", "/Users/"+u.Username, "UserShell").Output(); err == nil {
			if f := strings.Fields(string(out)); len(f) >= 2 {
				return f[1]
			}
		}
	}
	if out, err := exec.Command("getent", "passwd", u.Username).Output(); err == nil {
		if f := strings.Split(strings.TrimSpace(string(out)), ":"); len(f) >= 7 {
			return f[6]
		}
	}
	if data, err := os.ReadFile("/etc/passwd"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if f := strings.Split(line, ":"); len(f) >= 7 && f[0] == u.Username {
				return f[6]
			}
		}
	}
	return ""
}

// UserShell returns the user's shell: $SHELL when set, otherwise the login
// shell from the user database. Empty when neither is known.
func UserShell() string {
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	return loginShellFunc()
}

func detectShellConfig() (string, error) {
	home, _ := os.UserHomeDir()
	shell := os.Getenv("SHELL")
	if p, ok := rcForShell(shell, home); ok {
		return p, nil
	}
	if shell != "" {
		// A shell we don't map (ksh, nologin, ...): writing to another
		// shell's rc file would report success for an alias that never
		// loads. Callers downgrade this to a warning with manual
		// instructions once the playbook itself is in place.
		return "", fmt.Errorf("unrecognized shell %q. Use --shell-config to specify config file", shell)
	}
	// SHELL is unset (docker exec, cron): ask the user database for the
	// login shell before guessing. A /bin/sh user with a .bashrc lying
	// around must still get .profile, which is what their shell reads.
	if p, ok := rcForShell(loginShellFunc(), home); ok {
		return p, nil
	}
	// Last resort: an rc file the user already has.
	for _, name := range []string{".bashrc", ".zshrc", ".profile"} {
		p := filepath.Join(home, name)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("cannot determine shell (SHELL is unset) and no ~/.bashrc, ~/.zshrc, or ~/.profile found. Use --shell-config to specify config file")
}
