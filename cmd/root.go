package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

// Version is set at build time via -ldflags.
var Version = "dev"

var rootCmd = &cobra.Command{
	Use:     "claude-playbook",
	Short:   "Manage isolated Claude Code instances",
	Version: Version,
	RunE:    runRoot,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVar(&config.PlaybooksDir, "playbooks-dir", "", "playbooks directory (default: ~/.claude-playbooks)")
	rootCmd.PersistentFlags().StringVar(&config.ShellConfig, "shell-config", "", "shell config file (default: auto-detect from $SHELL)")

	rootCmd.AddCommand(listCmd)
	rootCmd.AddCommand(createCmd)
	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(startCmd)
	rootCmd.AddCommand(installCmd)
	rootCmd.AddCommand(aliasCmd)
	rootCmd.AddCommand(deleteCmd)
}

func runRoot(cmd *cobra.Command, args []string) error {
	shellConfig, err := config.ResolveShellConfig()
	if err != nil {
		return err
	}
	playbooksDir := config.ResolvePlaybooksDir()

	pbs, err := playbook.Discover(playbooksDir, shellConfig)
	if err != nil {
		return err
	}

	fmt.Println("claude-playbook -- manage isolated Claude Code instances")

	if len(pbs) == 0 {
		fmt.Println()
		fmt.Println("No playbooks found. Run 'claude-playbook create <name>' to get started.")
		return nil
	}

	fmt.Println()
	fmt.Println("Available playbooks:")
	fmt.Println()

	maxLen := 0
	for _, pb := range pbs {
		if len(pb.Name) > maxLen {
			maxLen = len(pb.Name)
		}
	}

	for _, pb := range pbs {
		runStr := fmt.Sprintf("claude-playbook run %s", pb.Name)
		if pb.HasAlias() {
			fmt.Printf("  %-*s  %-42s (or: %s)\n", maxLen, pb.Name, runStr, pb.Alias)
		} else {
			fmt.Printf("  %-*s  %-42s (no alias set)\n", maxLen, pb.Name, runStr)
		}
	}

	fmt.Println()
	fmt.Println("Run 'claude-playbook --help' for all commands.")
	return nil
}
