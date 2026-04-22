package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
	"github.com/ramazanpolat/claude-playbooks/internal/shell"
)

var aliasRemove bool

var aliasCmd = &cobra.Command{
	Use:   "alias [name] [alias]",
	Short: "Show or manage playbook aliases",
	Long: `With no arguments: show full alias lines for all playbooks.
With one argument: show the alias for that playbook, or create one using the playbook name.
With two arguments: set the alias.
With --remove: remove the alias.`,
	Args: cobra.RangeArgs(0, 2),
	RunE: runAlias,
}

func init() {
	aliasCmd.Flags().BoolVar(&aliasRemove, "remove", false, "remove the alias from shell config")
}

func runAlias(cmd *cobra.Command, args []string) error {
	shellConfig, err := config.ResolveShellConfig()
	if err != nil {
		return err
	}
	playbooksDir := config.ResolvePlaybooksDir()

	// No args — show full alias lines for all playbooks.
	if len(args) == 0 {
		pbs, err := playbook.Discover(playbooksDir, shellConfig)
		if err != nil {
			return err
		}
		if len(pbs) == 0 {
			fmt.Println("No playbooks found.")
			return nil
		}
		maxLen := 0
		for _, pb := range pbs {
			if len(pb.Name) > maxLen {
				maxLen = len(pb.Name)
			}
		}
		for _, pb := range pbs {
			if pb.HasAlias() {
				fmt.Printf("%-*s  %s\n", maxLen, pb.Name, pb.AliasLine)
			} else {
				fmt.Printf("%-*s  (no alias)\n", maxLen, pb.Name)
			}
		}
		return nil
	}

	name := args[0]

	pb, err := playbook.Require(playbooksDir, shellConfig, name)
	if err != nil {
		return err
	}

	// --remove
	if aliasRemove {
		if !pb.HasAlias() {
			fmt.Printf("Playbook %q has no alias set.\n", name)
			return nil
		}
		if _, err := shell.Remove(shellConfig, name); err != nil {
			return err
		}
		fmt.Printf("Alias %q removed from %s\n", pb.Alias, shellConfig)
		return nil
	}

	// Two args — set alias.
	if len(args) == 2 {
		newAlias := args[1]
		if err := shell.Write(shellConfig, name, newAlias, pb.Path); err != nil {
			return err
		}
		fmt.Printf("Alias %q set for playbook %q in %s\n", newAlias, name, shellConfig)
		fmt.Printf("\nReload your shell or run:\n  source %s\n", shellConfig)
		return nil
	}

	// One arg — show or create.
	if pb.HasAlias() {
		fmt.Printf("Alias for %q: %s\n", name, pb.AliasLine)
		return nil
	}

	// No alias — create one using the playbook name.
	if err := shell.Write(shellConfig, name, name, pb.Path); err != nil {
		return err
	}
	fmt.Printf("Alias %q created for playbook %q in %s\n", name, name, shellConfig)
	fmt.Printf("\nReload your shell or run:\n  source %s\n", shellConfig)
	return nil
}
