package cmd

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

// playbookNameRegex is the charset for a NEW playbook name: alphanumerics,
// underscore and dash, starting with an alphanumeric or underscore.
//
// A playbook name is not just a directory name. It is interpolated into a
// generated shell alias, into that alias's `run <name>` argument, and into
// commands printed for the user to paste. Permitting shell metacharacters made
// every one of those an encoding problem -- an apostrophe alone was a command
// injection into the user's shell config. Rejecting the name at the front door
// removes the whole class instead of escaping it at each site, and matches the
// charset already required of launcher command names.
//
// Deliberately applied to names being CREATED (create/rename/link/install), not
// to names being looked up: delete and the discovery paths keep using
// validateSinglePathSegment so an existing playbook with an odd name can still
// be listed, run and removed.
var playbookNameRegex = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]*$`)

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
