package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/shell"
)

var (
	createAlias   string
	createNoAlias bool
)

var createCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a new top-level playbook",
	Args:  cobra.ExactArgs(1),
	RunE:  runCreate,
}

func init() {
	createCmd.Flags().StringVar(&createAlias, "alias", "", "alias name (default: <name>)")
	createCmd.Flags().BoolVar(&createNoAlias, "no-alias", false, "skip alias creation")
}

func runCreate(cmd *cobra.Command, args []string) error {
	if createNoAlias && createAlias != "" {
		return fmt.Errorf("--no-alias and --alias cannot be used together")
	}

	name := args[0]
	if strings.Contains(name, "/") {
		return fmt.Errorf("'create' only creates top-level playbooks. To add a child, declare it in the parent's .playbook")
	}
	if strings.HasPrefix(name, ".") {
		return fmt.Errorf("playbook name cannot start with '.'")
	}

	playbooksDir := config.ResolvePlaybooksDir()
	if err := os.MkdirAll(playbooksDir, 0755); err != nil {
		return err
	}
	dest := filepath.Join(playbooksDir, name)

	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("playbook %q already exists at %s", name, dest)
	}

	if err := os.MkdirAll(dest, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	if err := manifest.WriteMinimal(dest, name); err != nil {
		return fmt.Errorf("failed to write .playbook: %w", err)
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

	if err := shell.Write(shellConfig, aliasName, dest); err != nil {
		return fmt.Errorf("failed to write alias: %w", err)
	}

	fmt.Printf("Alias %q added to %s\n", aliasName, shellConfig)
	fmt.Printf("\nReload your shell or run:\n  source %s\n\nThen run with:\n  %s\n", shellConfig, aliasName)
	return nil
}

// lastSegment returns the part after the last '/' in name.
func lastSegment(name string) string {
	i := strings.LastIndex(name, "/")
	if i < 0 {
		return name
	}
	return name[i+1:]
}
