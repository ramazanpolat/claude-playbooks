package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
	"github.com/ramazanpolat/claude-playbooks/internal/shell"
)

var (
	renameAlias   string
	renameNoAlias bool
)

var renameCmd = &cobra.Command{
	Use:               "rename <old-name> <new-name>",
	Short:             "Rename a top-level playbook",
	Args:              cobra.ExactArgs(2),
	ValidArgsFunction: autocompletePlaybookNames,
	RunE:              runRename,
}

func init() {
	renameCmd.Flags().StringVar(&renameAlias, "alias", "", "custom alias for renamed playbook")
	renameCmd.Flags().BoolVar(&renameNoAlias, "no-alias", false, "drop the alias if one existed")
}

func runRename(cmd *cobra.Command, args []string) error {
	if renameNoAlias && renameAlias != "" {
		return fmt.Errorf("--no-alias and --alias cannot be used together")
	}
	oldName := args[0]
	newName := args[1]

	if strings.Contains(oldName, "/") {
		return fmt.Errorf("playbook name cannot contain '/'")
	}
	if strings.Contains(newName, "/") {
		return fmt.Errorf("playbook name cannot contain '/'")
	}
	if err := validateTopLevelName("new name", newName); err != nil {
		return err
	}

	shellConfig, err := config.ResolveShellConfig()
	if err != nil {
		return err
	}
	playbooksDir := config.ResolvePlaybooksDir()

	pb, err := playbook.Require(playbooksDir, shellConfig, oldName)
	if err != nil {
		return err
	}

	oldRoot := pb.RootPath
	if oldRoot == "" {
		oldRoot = pb.Path
	}
	oldConfigPath := pb.Path
	newPath := filepath.Join(playbooksDir, newName)
	newConfigPath := newPath
	if rel, err := filepath.Rel(oldRoot, oldConfigPath); err == nil && rel != "." {
		newConfigPath = filepath.Join(newPath, rel)
	}

	if _, err := os.Stat(newPath); err == nil {
		return fmt.Errorf("%q already exists at %s", newName, newPath)
	}

	if err := os.Rename(oldRoot, newPath); err != nil {
		return fmt.Errorf("failed to rename: %w", err)
	}

	changed, skippedAliases, err := shell.RewritePathPrefix(shellConfig, oldRoot, newPath)
	if err != nil {
		return fmt.Errorf("failed to update aliases: %w", err)
	}

	switch {
	case renameNoAlias:
		if pb.HasAlias() {
			if _, err := shell.RemoveByPathPrefix(shellConfig, newPath); err != nil {
				return fmt.Errorf("failed to drop alias: %w", err)
			}
		}
	case renameAlias != "":
		if _, err := shell.RemoveByPathPrefix(shellConfig, newPath); err != nil {
			return fmt.Errorf("failed to update alias: %w", err)
		}
		if err := shell.Write(shellConfig, renameAlias, newConfigPath); err != nil {
			return fmt.Errorf("failed to write alias: %w", err)
		}
	}

	// Keep the manifest's name in sync with the directory name. Skip linked
	// playbooks: their manifest belongs to the external source tree.
	if info, err := os.Lstat(newPath); err == nil && info.Mode()&os.ModeSymlink == 0 {
		if m, err := manifest.Read(newPath); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not read manifest to update its name: %v\n", err)
		} else if m != nil && m.Name != newName {
			m.Name = newName
			if err := manifest.Write(newPath, m); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not update manifest name: %v\n", err)
			}
		}
	}

	fmt.Printf("Renamed %q → %q\n", oldName, newName)
	if changed > 0 {
		fmt.Printf("Updated %d alias line%s in %s\n", changed, pluralS(changed), shellConfig)
	}
	// A hand-edited alias is left exactly as written rather than guessed at:
	// editing shell-encoded text in place is what makes silent corruption
	// possible. Say so, and name the one command that fixes it — otherwise the
	// alias quietly keeps pointing at the old playbook.
	for _, aliasName := range skippedAliases {
		fmt.Fprintf(os.Stderr,
			"Warning: alias %q in %s was hand-edited, so it was left unchanged and still refers to %q.\n"+
				"         Regenerate it with: %s alias %s %s\n",
			aliasName, shellConfig, oldName, filepath.Base(os.Args[0]), newName, aliasName)
	}
	return nil
}
