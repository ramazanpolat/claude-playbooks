package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/auth"
	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

var runCmd = &cobra.Command{
	Use:                "run <name> [claude-flags...]",
	Short:              "Run Claude Code with a playbook",
	DisableFlagParsing: true,
	ValidArgsFunction:  autocompletePlaybookNames,
	RunE:               runRun,
}

func runRun(cmd *cobra.Command, args []string) error {
	rest, playbooksDir := scanPlaybooksDirArg(args)
	// A --help BEFORE the playbook name prints usage; after the name it
	// belongs to claude (rest[0] is the name by then).
	if len(rest) > 0 && (rest[0] == "--help" || rest[0] == "-h") {
		fmt.Println("Usage: claude-playbook run <name> [claude-flags...]")
		fmt.Println()
		fmt.Println("Runs Claude Code with the named playbook.")
		fmt.Println("Any flags after the name are forwarded directly to claude.")
		return nil
	}
	if playbooksDir != "" {
		config.PlaybooksDir = playbooksDir
	}

	if len(rest) == 0 {
		return fmt.Errorf("playbook name required\nUsage: claude-playbook run <name> [claude-flags...]")
	}

	name := rest[0]
	claudeArgs := rest[1:]

	playbooksDirResolved := config.ResolvePlaybooksDir()

	pb, err := playbook.Find(playbooksDirResolved, name)
	if err != nil {
		return err
	}
	if pb == nil {
		return fmt.Errorf("unknown playbook %q. Run 'claude-playbook list' to see available playbooks", name)
	}

	claudePath, err := exec.LookPath("claude")
	if err != nil {
		return fmt.Errorf("'claude' command not found. Install Claude Code first: https://claude.ai/download")
	}

	launchEnv, syncErr := auth.PrepareLaunchEnv(pb.Path)
	if syncErr != nil {
		// Neutral wording: PrepareLaunchEnv may have been syncing credentials,
		// account metadata, or detaching for an isolated playbook. Naming
		// credentials specifically sent users after credential files and symlinks
		// for failures in paths where credential syncing was deliberately skipped.
		fmt.Fprintf(os.Stderr, "Warning: failed to prepare authentication state: %v\n", syncErr)
	}

	c := exec.Command(claudePath, claudeArgs...)
	c.Env = launchEnv
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr

	return preserveExitCode(c.Run())
}
