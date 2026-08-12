package config

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
)

var (
	PlaybooksDir string
	ShellConfig  string
)

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
// or "" if it cannot be determined. getent covers Linux including NSS
// sources; the /etc/passwd scan covers minimal images without getent.
func loginShell() string {
	u, err := user.Current()
	if err != nil {
		return ""
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

func detectShellConfig() (string, error) {
	home, _ := os.UserHomeDir()
	shell := os.Getenv("SHELL")
	if p, ok := rcForShell(shell, home); ok {
		return p, nil
	}
	// SHELL is unset (docker exec, cron) or unrecognized. Ask the user
	// database for the login shell before guessing: a /bin/sh user with a
	// .bashrc lying around must still get .profile, which is what their
	// shell actually reads.
	if shell == "" {
		if p, ok := rcForShell(loginShellFunc(), home); ok {
			return p, nil
		}
	}
	// Last resort: an rc file the user already has.
	for _, name := range []string{".bashrc", ".zshrc", ".profile"} {
		p := filepath.Join(home, name)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("unknown shell %q and no ~/.bashrc, ~/.zshrc, or ~/.profile to fall back to. Use --shell-config to specify config file", shell)
}
