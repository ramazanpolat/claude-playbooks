package cmd

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

// playbookNameRegex is playbook.NewNamePattern; see there for why.
var playbookNameRegex = playbook.NewNamePattern

func validateTopLevelName(field, name string) error {
	if err := validateSinglePathSegment(field, name); err != nil {
		return err
	}
	if strings.HasPrefix(name, ".") {
		return fmt.Errorf("%s cannot start with '.'", field)
	}
	if !playbookNameRegex.MatchString(name) {
		return fmt.Errorf(
			"%s %q is not valid: use letters, digits, dashes and underscores only "+
				"(a playbook name is interpolated into a shell alias, so it must be a single safe word)",
			field, name)
	}
	return nil
}

func validateSinglePathSegment(field, name string) error {
	if name == "" {
		return fmt.Errorf("%s cannot be empty", field)
	}
	if name == "." || name == ".." || filepath.IsAbs(name) || strings.ContainsAny(name, "/\\\r\n") || filepath.Clean(name) != name {
		return fmt.Errorf("%s must be a top-level playbook name, not a path", field)
	}
	return nil
}

func autocompletePlaybookNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	playbooksDir := config.ResolvePlaybooksDir()
	pbs, err := playbook.Discover(playbooksDir)
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	var names []string
	for _, pb := range pbs {
		names = append(names, pb.Name)
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}
