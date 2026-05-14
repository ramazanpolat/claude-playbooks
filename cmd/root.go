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
	rootCmd.AddCommand(linkCmd)
	rootCmd.AddCommand(infoCmd)
	rootCmd.AddCommand(renameCmd)
	rootCmd.AddCommand(aliasCmd)
	rootCmd.AddCommand(dealiasCmd)
	rootCmd.AddCommand(deleteCmd)
	rootCmd.AddCommand(updateCmd)
	rootCmd.AddCommand(completionCmd)
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
	fmt.Println()
	fmt.Printf("Playbooks directory: %s\n", playbooksDir)

	if len(pbs) == 0 {
		fmt.Println("No playbooks installed yet. Get started with one of:")
		fmt.Println()
		fmt.Println("  # Install the example multi-playbook tree (DBA, SRE, SecOps, frontend, ...):")
		fmt.Println("  claude-playbook install https://github.com/ramazanpolat/awesome-playbooks --alias-all")
		fmt.Println()
		fmt.Println("  # Cherry-pick a single role (e.g. DBA):")
		fmt.Println("  claude-playbook install https://github.com/ramazanpolat/awesome-playbooks/tree/main/playbooks/dba")
		fmt.Println()
		fmt.Println("  # Create your own from scratch:")
		fmt.Println("  claude-playbook create <name>")
		fmt.Println()
		fmt.Println("Run 'claude-playbook --help' for all commands.")
		return nil
	}

	fmt.Println()
	fmt.Println("Available playbooks:")
	fmt.Println()

	display := func(pb *playbook.Playbook) string {
		if pb.IsChild {
			return "  " + pb.Name
		}
		return pb.Name
	}

	maxLen := 0
	for _, pb := range pbs {
		if l := len(display(pb)); l > maxLen {
			maxLen = l
		}
	}
	cmdColW := maxLen + len("claude-playbook run ")

	for _, pb := range pbs {
		runStr := fmt.Sprintf("claude-playbook run %s", pb.Name)
		if pb.HasAlias() {
			fmt.Printf("  %-*s  %-*s  (or: %s)\n", maxLen, display(pb), cmdColW, runStr, pb.Alias)
		} else {
			fmt.Printf("  %-*s  %-*s  (no alias set)\n", maxLen, display(pb), cmdColW, runStr)
		}
	}

	fmt.Println()
	fmt.Println("Run 'claude-playbook --help' for all commands.")
	return nil
}
