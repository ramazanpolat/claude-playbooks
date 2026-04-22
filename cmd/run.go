package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
)

var runCmd = &cobra.Command{
	Use:                "run <name> [claude-flags...]",
	Short:              "Run Claude Code with a playbook",
	DisableFlagParsing: true,
	RunE:               runRun,
}

func runRun(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("playbook name required\nUsage: claude-playbook run <name> [claude-flags...]")
	}

	name := args[0]

	// Support --help passed to this command before the playbook name.
	if name == "--help" || name == "-h" {
		fmt.Println("Usage: claude-playbook run <name> [claude-flags...]")
		fmt.Println()
		fmt.Println("Runs Claude Code with the named playbook. Any flags after the name are forwarded to claude.")
		fmt.Println()
		fmt.Println("Note: global flags (--playbooks-dir, --shell-config) must be set via environment")
		fmt.Println("variables when using 'run': CLAUDE_PLAYBOOKS_DIR, CLAUDE_SHELL_CONFIG")
		return nil
	}

	playbooksDir := config.ResolvePlaybooksDir()
	pbPath := filepath.Join(playbooksDir, name)

	if _, err := os.Stat(pbPath); os.IsNotExist(err) {
		return fmt.Errorf("unknown playbook %q. Run 'claude-playbook list' to see available playbooks", name)
	}

	claudePath, err := exec.LookPath("claude")
	if err != nil {
		return fmt.Errorf("'claude' command not found. Install Claude Code first: https://claude.ai/download")
	}

	claudeArgs := args[1:]
	c := exec.Command(claudePath, claudeArgs...)
	c.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR="+pbPath)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr

	return c.Run()
}
