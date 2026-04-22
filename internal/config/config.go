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
	default:
		return "", fmt.Errorf("unknown shell %q. Use --shell-config to specify config file", shell)
	}
}
