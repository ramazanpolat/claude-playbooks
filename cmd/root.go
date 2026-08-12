package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

// Version is set at build time via -ldflags.
var Version = "dev"

var rootCmd = &cobra.Command{
	Use:           "claude-playbook",
	Short:         "Manage isolated Claude Code instances",
	Version:       Version,
	SilenceErrors: true,
	SilenceUsage:  true,
	RunE:          runRoot,
}

func Execute() {
	// Multicall dispatch: invoked through a launcher symlink, argv[0] names
	// a playbook — behave as `run <name>` with all arguments forwarded.
	if name, ok := multicallPlaybook(); ok {
		if err := runRun(nil, append([]string{name}, os.Args[1:]...)); err != nil {
			if code, ok := exitCode(err); ok {
				os.Exit(code)
			}
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		return
	}
	// A launcher whose name no longer resolves is stale (its playbook was
	// deleted or renamed away). Fail loudly rather than falling through to
	// the CLI overview with exit 0.
	if invokedViaLauncher() {
		fmt.Fprintf(os.Stderr, "Error: unknown playbook %q — this launcher no longer matches any playbook. Remove the link or recreate the playbook.\n", filepath.Base(os.Args[0]))
		os.Exit(1)
	}
	if err := rootCmd.Execute(); err != nil {
		if code, ok := exitCode(err); ok {
			os.Exit(code)
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVar(&config.PlaybooksDir, "playbooks-dir", "", "playbooks directory (default: ~/.claude-playbooks)")
	rootCmd.PersistentFlags().StringVar(&config.ShellConfig, "shell-config", "", "shell config file (default: auto-detect from $SHELL)")
	rootCmd.PersistentFlags().StringVar(&config.LauncherDir, "launcher-dir", "", "directory for launcher commands (default: directory of this binary)")

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
	rootCmd.AddCommand(selfUninstallCmd)
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
		fmt.Println("  # Install a single playbook from a Git repo:")
		fmt.Println("  claude-playbook install https://github.com/user/pai")
		fmt.Println()
		fmt.Println("  # Cherry-pick one playbook out of a monorepo (e.g. DBA):")
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

	maxLen := 0
	for _, pb := range pbs {
		if l := len(pb.Name); l > maxLen {
			maxLen = l
		}
	}
	cmdColW := maxLen + len("claude-playbook run ")

	for _, pb := range pbs {
		runStr := fmt.Sprintf("claude-playbook run %s", pb.Name)
		if pb.HasAlias() {
			fmt.Printf("  %-*s  %-*s  (or: %s)\n", maxLen, pb.Name, cmdColW, runStr, pb.Alias)
		} else {
			fmt.Printf("  %-*s  %-*s  (no alias set)\n", maxLen, pb.Name, cmdColW, runStr)
		}
	}

	fmt.Println()
	fmt.Println("Run 'claude-playbook --help' for all commands.")
	return nil
}
