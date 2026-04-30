package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
	"github.com/ramazanpolat/claude-playbooks/internal/shell"
)

var dealiasCmd = &cobra.Command{
	Use:   "dealias <name>",
	Short: "Remove the shell alias for a playbook",
	Long:  `Removes any aliases pointing at the named playbook. Equivalent to 'alias <name> --remove'.`,
	Args:  cobra.ExactArgs(1),
	RunE:  runDealias,
}

func runDealias(cmd *cobra.Command, args []string) error {
	name := args[0]
	shellConfig, err := config.ResolveShellConfig()
	if err != nil {
		return err
	}
	playbooksDir := config.ResolvePlaybooksDir()

	pb, err := playbook.Require(playbooksDir, shellConfig, name)
	if err != nil {
		return err
	}
	if !pb.HasAlias() {
		fmt.Printf("Playbook %q has no alias set.\n", name)
		return nil
	}
	removed, err := shell.RemoveByPath(shellConfig, pb.Path)
	if err != nil {
		return err
	}
	fmt.Printf("Removed %d alias(es) for playbook %q from %s\n", removed, name, shellConfig)
	return nil
}
