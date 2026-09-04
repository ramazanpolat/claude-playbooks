package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/auth"
	"github.com/ramazanpolat/claude-playbooks/internal/envprofile"
)

var startCmd = &cobra.Command{
	Use:                "start <path> [claude-flags...]",
	Short:              "Start an ad-hoc Claude Code session at a directory",
	DisableFlagParsing: true,
	RunE:               runStart,
}

func runStart(cmd *cobra.Command, args []string) error {
	// start addresses a path, not a registry name, but the playbooks-dir
	// value still names the root whose .env-profiles/ the path's manifest
	// may reference, so it is applied to this process exactly as run does
	// (and kept out of the args forwarded to claude).
	args, err := takePlaybooksDirArg(args)
	if err != nil {
		return err
	}

	var deleteAfter bool
	var rest []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--delete":
			deleteAfter = true
		default:
			rest = append(rest, args[i])
		}
	}
	// --help BEFORE the path prints usage; after it, the flag is forwarded
	// to claude.
	if restRequestsHelp(rest) {
		fmt.Println("Usage: claude-playbook start " + launchFlagsUsage + " <path> [claude-flags...]")
		fmt.Println()
		fmt.Println("Starts an ad-hoc Claude Code session at the given directory.")
		fmt.Println("Creates the directory if it does not exist.")
		fmt.Println("Launch flags (before the path) add one-off environment layers for this")
		fmt.Println("launch only: --env-profile NAME, --env KEY=VALUE, --unset KEY, --env-file PATH.")
		fmt.Println("Any flags after the path are forwarded directly to claude.")
		fmt.Println()
		fmt.Println("Flags:")
		fmt.Println("  --delete   Delete the directory when the session ends")
		return nil
	}
	rest, layers, err := takeLaunchFlags(rest)
	if err != nil {
		return err
	}

	if len(rest) == 0 {
		return fmt.Errorf("path required\nUsage: claude-playbook start " + launchFlagsUsage + " <path> [claude-flags...]")
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

	launchEnv, syncErr := auth.PrepareLaunchEnvWith(absPath, layers)
	if errors.Is(syncErr, envprofile.ErrProfile) {
		// Missing, unreadable, or invalid profile: launching with a silently
		// dropped layer could send traffic to the wrong endpoint with the
		// wrong credentials -- refuse, do not warn.
		return syncErr
	}
	if syncErr != nil {
		// Neutral wording -- see the matching comment in cmd/run.go.
		fmt.Fprintf(os.Stderr, "Warning: failed to prepare authentication state: %v\n", syncErr)
	}

	c := exec.Command(claudePath, claudeArgs...)
	c.Env = launchEnv
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr

	runErr := c.Run()

	if deleteAfter {
		if rmErr := os.RemoveAll(absPath); rmErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not delete %s: %v\n", absPath, rmErr)
		}
	}

	return preserveExitCode(runErr)
}
