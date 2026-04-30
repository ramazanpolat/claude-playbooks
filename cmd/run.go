package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

var runCmd = &cobra.Command{
	Use:                "run <name> [claude-flags...]",
	Short:              "Run Claude Code with a playbook",
	DisableFlagParsing: true,
	RunE:               runRun,
}

func runRun(cmd *cobra.Command, args []string) error {
	var playbooksDir, shellConfig string
	var rest []string

	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--playbooks-dir" && i+1 < len(args):
			playbooksDir = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--playbooks-dir="):
			playbooksDir = strings.TrimPrefix(args[i], "--playbooks-dir=")
		case args[i] == "--shell-config" && i+1 < len(args):
			shellConfig = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--shell-config="):
			shellConfig = strings.TrimPrefix(args[i], "--shell-config=")
		case args[i] == "--help" || args[i] == "-h":
			fmt.Println("Usage: claude-playbook run <name> [claude-flags...]")
			fmt.Println()
			fmt.Println("Runs Claude Code with the named playbook.")
			fmt.Println("Any flags after the name are forwarded directly to claude.")
			return nil
		default:
			rest = append(rest, args[i])
		}
	}

	if playbooksDir != "" {
		config.PlaybooksDir = playbooksDir
	}
	if shellConfig != "" {
		config.ShellConfig = shellConfig
	}

	if len(rest) == 0 {
		return fmt.Errorf("playbook name required\nUsage: claude-playbook run <name> [claude-flags...]")
	}

	name := rest[0]
	claudeArgs := rest[1:]

	playbooksDirResolved := config.ResolvePlaybooksDir()
	shellConfigResolved, _ := config.ResolveShellConfig()

	pb, err := playbook.Find(playbooksDirResolved, shellConfigResolved, name)
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

	c := exec.Command(claudePath, claudeArgs...)
	c.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR="+pb.Path)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr

	return c.Run()
}
