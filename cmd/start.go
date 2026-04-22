package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

var startCmd = &cobra.Command{
	Use:                "start <path> [claude-flags...]",
	Short:              "Start an ad-hoc Claude Code session at a directory",
	DisableFlagParsing: true,
	RunE:               runStart,
}

func runStart(cmd *cobra.Command, args []string) error {
	var deleteAfter bool
	var rest []string

	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--playbooks-dir" && i+1 < len(args):
			i++
		case strings.HasPrefix(args[i], "--playbooks-dir="):
			// ignore
		case args[i] == "--shell-config" && i+1 < len(args):
			i++
		case strings.HasPrefix(args[i], "--shell-config="):
			// ignore
		case args[i] == "--delete":
			deleteAfter = true
		case args[i] == "--help" || args[i] == "-h":
			fmt.Println("Usage: claude-playbook start <path> [claude-flags...]")
			fmt.Println()
			fmt.Println("Starts an ad-hoc Claude Code session at the given directory.")
			fmt.Println("Creates the directory if it does not exist.")
			fmt.Println("Any flags after the path are forwarded directly to claude.")
			fmt.Println()
			fmt.Println("Flags:")
			fmt.Println("  --delete   Delete the directory when the session ends")
			return nil
		default:
			rest = append(rest, args[i])
		}
	}

	if len(rest) == 0 {
		return fmt.Errorf("path required\nUsage: claude-playbook start <path> [claude-flags...]")
	}

	path := rest[0]
	claudeArgs := rest[1:]

	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("invalid path %q: %w", path, err)
	}

	if info, err := os.Stat(absPath); err == nil && !info.IsDir() {
		return fmt.Errorf("%q is not a directory", absPath)
	} else if os.IsNotExist(err) {
		if mkErr := os.MkdirAll(absPath, 0755); mkErr != nil {
			return fmt.Errorf("could not create %q: %w", absPath, mkErr)
		}
	}

	claudePath, err := exec.LookPath("claude")
	if err != nil {
		return fmt.Errorf("'claude' command not found. Install Claude Code first: https://claude.ai/download")
	}

	c := exec.Command(claudePath, claudeArgs...)
	c.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR="+absPath)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr

	runErr := c.Run()

	if deleteAfter {
		if rmErr := os.RemoveAll(absPath); rmErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not delete %s: %v\n", absPath, rmErr)
		}
	}

	return runErr
}
