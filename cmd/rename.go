package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
	"github.com/ramazanpolat/claude-playbooks/internal/shell"
)

var (
	renameAlias   string
	renameNoAlias bool
)

var renameCmd = &cobra.Command{
	Use:   "rename <old-name> <new-name>",
	Short: "Rename a top-level playbook",
	Args:  cobra.ExactArgs(2),
	RunE:  runRename,
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

	changed, err := shell.RewritePathPrefix(shellConfig, oldRoot, newPath)
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

	fmt.Printf("Renamed %q → %q\n", oldName, newName)
	if changed > 0 {
		fmt.Printf("Updated %d alias line%s in %s\n", changed, pluralS(changed), shellConfig)
	}
	return nil
}
