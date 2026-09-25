package cmd

import (
	"github.com/spf13/cobra"
)

var dealiasCmd = &cobra.Command{
	Use:               "dealias <name>",
	Short:             "Remove the alias for a playbook",
	Long:              `Removes the playbook's alias and its launcher command. Equivalent to 'alias <name> --remove'.`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: autocompletePlaybookNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		return doAlias(aliasOpts{remove: true}, args)
	},
}
