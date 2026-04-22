package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/shell"
)

var (
	createAlias   string
	createNoAlias bool
)

var createCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a new playbook",
	Args:  cobra.ExactArgs(1),
	RunE:  runCreate,
}

func init() {
	createCmd.Flags().StringVar(&createAlias, "alias", "", "alias name (default: same as playbook name)")
	createCmd.Flags().BoolVar(&createNoAlias, "no-alias", false, "skip alias creation")
}

func runCreate(cmd *cobra.Command, args []string) error {
	if createNoAlias && createAlias != "" {
		return fmt.Errorf("--no-alias and --alias cannot be used together")
	}

	name := args[0]
	playbooksDir := config.ResolvePlaybooksDir()
	dest := filepath.Join(playbooksDir, name)

	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("playbook %q already exists at %s", name, dest)
	}

	if err := os.MkdirAll(dest, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	fmt.Printf("Created playbook %q at %s\n", name, dest)

	if createNoAlias {
		fmt.Printf("\nRun with:\n  claude-playbook run %s\n", name)
		return nil
	}

	aliasName := createAlias
	if aliasName == "" {
		aliasName = name
	}

	shellConfig, err := config.ResolveShellConfig()
	if err != nil {
		return err
	}

	if err := shell.Write(shellConfig, name, aliasName, dest); err != nil {
		return fmt.Errorf("failed to write alias: %w", err)
	}

	fmt.Printf("Alias %q added to %s\n", aliasName, shellConfig)
	fmt.Printf("\nReload your shell or run:\n  source %s\n\nThen run with:\n  %s\n", shellConfig, aliasName)
	return nil
}
