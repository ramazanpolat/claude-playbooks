package config

import (
	"fmt"
	"os"
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

func detectShellConfig() (string, error) {
	shell := os.Getenv("SHELL")
	home, _ := os.UserHomeDir()
	switch {
	case strings.Contains(shell, "zsh"):
		return filepath.Join(home, ".zshrc"), nil
	case strings.Contains(shell, "bash"):
		return filepath.Join(home, ".bashrc"), nil
	case strings.Contains(shell, "fish"):
		return filepath.Join(home, ".config", "fish", "config.fish"), nil
	}
	// /bin/sh (the useradd default on Debian) and dash never read .bashrc;
	// their login shells read .profile, so the alias must go there.
	switch filepath.Base(shell) {
	case "sh", "dash":
		return filepath.Join(home, ".profile"), nil
	}
	// SHELL is unset (docker exec, cron) or names a shell we don't
	// recognize. Fall back to an rc file the user already has instead of
	// failing: .bashrc/.zshrc load in their shells, and .profile is read
	// by sh login shells.
	for _, name := range []string{".bashrc", ".zshrc", ".profile"} {
		p := filepath.Join(home, name)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("unknown shell %q and no ~/.bashrc, ~/.zshrc, or ~/.profile to fall back to. Use --shell-config to specify config file", shell)
}
