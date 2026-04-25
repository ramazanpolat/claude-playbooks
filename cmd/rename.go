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
	Short: "Rename a playbook or container",
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

	if strings.HasPrefix(newName, ".") {
		return fmt.Errorf("new name cannot start with '.'")
	}

	shellConfig, err := config.ResolveShellConfig()
	if err != nil {
		return err
	}
	playbooksDir := config.ResolvePlaybooksDir()

	target, err := playbook.ResolveTarget(playbooksDir, oldName)
	if err != nil {
		return err
	}

	oldPath := target.Path
	newPath := filepath.Join(playbooksDir, newName)

	if _, err := os.Stat(newPath); err == nil {
		return fmt.Errorf("%q already exists at %s", newName, newPath)
	}

	// Ensure parent of newPath exists (for nested renames).
	if err := os.MkdirAll(filepath.Dir(newPath), 0755); err != nil {
		return fmt.Errorf("failed to prepare destination: %w", err)
	}

	if err := os.Rename(oldPath, newPath); err != nil {
		return fmt.Errorf("failed to rename: %w", err)
	}

	// Rewrite aliases pointing into the old path.
	changed, err := shell.RewritePathPrefix(shellConfig, oldPath, newPath)
	if err != nil {
		return fmt.Errorf("failed to update aliases: %w", err)
	}

	// Apply --alias / --no-alias (playbook-only).
	if target.IsPlaybook {
		pb, _ := playbook.Find(playbooksDir, shellConfig, newName)
		switch {
		case renameNoAlias:
			if pb != nil && pb.HasAlias() {
				if _, err := shell.RemoveByPath(shellConfig, pb.Path); err != nil {
					return fmt.Errorf("failed to drop alias: %w", err)
				}
			}
		case renameAlias != "":
			if _, err := shell.RemoveByPath(shellConfig, newPath); err != nil {
				return fmt.Errorf("failed to update alias: %w", err)
			}
			if err := shell.Write(shellConfig, renameAlias, newPath); err != nil {
				return fmt.Errorf("failed to write alias: %w", err)
			}
		}
	}

	fmt.Printf("Renamed %q → %q\n", oldName, newName)
	if changed > 0 {
		fmt.Printf("Updated %d alias line%s in %s\n", changed, pluralS(changed), shellConfig)
	}
	return nil
}
