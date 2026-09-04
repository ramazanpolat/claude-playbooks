package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/auth"
	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envprofile"
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
	rest, err := takePlaybooksDirArg(args)
	if err != nil {
		return err
	}
	// A --help BEFORE the playbook name prints usage; after the name it
	// belongs to claude (rest[0] is the name by then).
	if restRequestsHelp(rest) {
		fmt.Println("Usage: claude-playbook run " + launchFlagsUsage + " <name> [claude-flags...]")
		fmt.Println()
		fmt.Println("Runs Claude Code with the named playbook.")
		fmt.Println("Launch flags add one-off environment layers on top of the playbook's [env]")
		fmt.Println("block, in order, for this launch only; they go before the name or right after it.")
		fmt.Println("  --env-profile NAME   layer an existing env profile")
		fmt.Println("  --env KEY=VALUE      set one variable")
		fmt.Println("  --unset KEY          remove one variable (CLAUDE_CODE_OAUTH_TOKEN: use the stored login)")
		fmt.Println("  --env-file PATH      layer a dotenv-style file of KEY=VALUE lines")
		fmt.Println("Everything else after the name is forwarded directly to claude.")
		return nil
	}

	// Launch flags may precede the name (`run --env K=V name`) or follow it
	// directly (`name --env K=V ...`, which is what a launcher passes).
	rest, layers, err := takeLaunchFlags(rest)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return fmt.Errorf("playbook name required\nUsage: claude-playbook run " + launchFlagsUsage + " <name> [claude-flags...]")
	}
	name := rest[0]
	claudeArgs, more, err := takeLaunchFlags(rest[1:])
	if err != nil {
		return err
	}
	layers = append(layers, more...)

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

	launchEnv, syncErr := auth.PrepareLaunchEnvWith(pb.Path, layers)
	if errors.Is(syncErr, envprofile.ErrProfile) {
		// Missing, unreadable, or invalid profile: launching with a silently
		// dropped layer could send traffic to the wrong endpoint with the
		// wrong credentials -- refuse, do not warn.
		return syncErr
	}
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
