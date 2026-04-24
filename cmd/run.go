package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
)

var runCmd = &cobra.Command{
	Use:   "run <name> [claude-flags...]",
	Short: "Run Claude Code with a playbook",
	// DisableFlagParsing passes all args raw, including parent persistent flags
	// that weren't parsed. We handle them manually below.
	DisableFlagParsing: true,
	RunE:               runRun,
}

func runRun(cmd *cobra.Command, args []string) error {
	// With DisableFlagParsing=true, cobra skips ALL flag parsing — including
	// root persistent flags like --playbooks-dir. Extract them manually so the
	// alias pattern `alias x='claude-playbook --playbooks-dir ... run'` works.
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

	playbooksDir = config.ResolvePlaybooksDir()
	pbPath := filepath.Join(playbooksDir, name)

	info, err := os.Stat(pbPath)
	if os.IsNotExist(err) || (err == nil && !info.IsDir()) {
		return fmt.Errorf("unknown playbook %q. Run 'claude-playbook list' to see available playbooks", name)
	}
	if _, err := os.Stat(filepath.Join(pbPath, ".playbook")); os.IsNotExist(err) {
		return fmt.Errorf("%q is not a playbook (no .playbook file). Use 'claude-playbook list' to see available playbooks", name)
	}

	claudePath, err := exec.LookPath("claude")
	if err != nil {
		return fmt.Errorf("'claude' command not found. Install Claude Code first: https://claude.ai/download")
	}

	c := exec.Command(claudePath, claudeArgs...)
	c.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR="+pbPath)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr

	return c.Run()
}
