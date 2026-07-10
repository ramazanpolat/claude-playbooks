package cmd

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

func validateTopLevelName(field, name string) error {
	if name == "" {
		return fmt.Errorf("%s cannot be empty", field)
	}
	if filepath.IsAbs(name) || strings.ContainsAny(name, `/\`) || filepath.Clean(name) != name {
		return fmt.Errorf("%s must be a top-level playbook name, not a path", field)
	}
	if strings.HasPrefix(name, ".") {
		return fmt.Errorf("%s cannot start with '.'", field)
	}
	return nil
}

func autocompletePlaybookNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	playbooksDir := config.ResolvePlaybooksDir()
	shellConfig, _ := config.ResolveShellConfig()
	pbs, err := playbook.Discover(playbooksDir, shellConfig)
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	var names []string
	for _, pb := range pbs {
		names = append(names, pb.Name)
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}
