package cmd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
	"github.com/ramazanpolat/claude-playbooks/internal/shell"
)

var deleteYes bool

var deleteCmd = &cobra.Command{
	Use:     "delete <name>",
	Aliases: []string{"uninstall", "unlink"},
	Short:   "Delete a top-level playbook (and all its children)",
	Args:    cobra.ExactArgs(1),
	RunE:    runDelete,
}

func init() {
	deleteCmd.Flags().BoolVarP(&deleteYes, "yes", "y", false, "skip confirmation prompt")
}

func runDelete(cmd *cobra.Command, args []string) error {
	name := args[0]
	shellConfig, err := config.ResolveShellConfig()
	if err != nil {
		return err
	}
	playbooksDir := config.ResolvePlaybooksDir()

	if strings.Contains(name, "/") {
		return fmt.Errorf("%q is a child playbook; delete the parent or run 'claude-playbook dealias %s' to drop just the alias", name, name)
	}

	pb, err := playbook.Find(playbooksDir, shellConfig, name)
	if err != nil {
		return err
	}
	if pb == nil {
		// Allow cleanup of dangling state when the directory is already gone:
		// only proceed if a directory exists at the expected path.
		path := filepath.Join(playbooksDir, name)
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			return fmt.Errorf("%q not found under %s", name, playbooksDir)
		}
		return deleteOrphan(playbooksDir, shellConfig, name, path)
	}
	if pb.IsChild {
		return fmt.Errorf("%q is a child playbook; delete the parent %q or run 'claude-playbook dealias %s' to drop just the alias", name, pb.Parent, name)
	}

	children := playbook.Children(playbooksDir, shellConfig, pb)

	if !deleteYes {
		aliasInfo := "(no alias)"
		if pb.HasAlias() {
			aliasInfo = fmt.Sprintf("%s (will be removed from %s)", pb.Alias, shellConfig)
		}
		fileCount, dirCount := countContents(pb.Path)
		fmt.Printf("Playbook: %s\n", pb.Name)
		fmt.Printf("Location: %s\n", pb.Path)
		fmt.Printf("Alias:    %s\n", aliasInfo)
		if len(children) > 0 {
			fmt.Printf("Children: %d\n", len(children))
			for _, c := range children {
				if c.HasAlias() {
					fmt.Printf("  %s    (alias: %s — will be removed)\n", c.Name, c.Alias)
				} else {
					fmt.Printf("  %s    (no alias)\n", c.Name)
				}
			}
		}
		fmt.Printf("Contents: %d files, %d directories\n", fileCount, dirCount)
		if !confirm("\nPermanently delete? [y/N] ") {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	if _, err := shell.RemoveByPathPrefix(shellConfig, pb.Path); err != nil {
		return fmt.Errorf("failed to clean up aliases: %w", err)
	}
	if err := removeAny(pb.Path); err != nil {
		return fmt.Errorf("failed to delete %s: %w", pb.Path, err)
	}
	fmt.Printf("Deleted playbook %q.\n", pb.Name)
	return nil
}

// deleteOrphan handles a directory that exists at the expected path but is
// not a discoverable playbook (e.g. .playbook removed). Cleans up any aliases
// pointing into it and removes the directory.
func deleteOrphan(playbooksDir, shellConfig, name, path string) error {
	if !deleteYes {
		fmt.Printf("Directory %q exists at %s but has no .playbook.\n", name, path)
		if !confirm("Permanently delete the directory and any aliases pointing into it? [y/N] ") {
			fmt.Println("Cancelled.")
			return nil
		}
	}
	if _, err := shell.RemoveByPathPrefix(shellConfig, path); err != nil {
		return fmt.Errorf("failed to clean up aliases: %w", err)
	}
	if err := removeAny(path); err != nil {
		return fmt.Errorf("failed to delete %s: %w", path, err)
	}
	fmt.Printf("Deleted %q.\n", name)
	return nil
}

func removeAny(path string) error {
	linfo, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if linfo.Mode()&os.ModeSymlink != 0 {
		return os.Remove(path)
	}
	return os.RemoveAll(path)
}

func confirm(prompt string) bool {
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))
	return answer == "y" || answer == "yes"
}

func countContents(dir string) (files, dirs int) {
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || path == dir {
			return nil
		}
		if info.IsDir() {
			dirs++
		} else {
			files++
		}
		return nil
	})
	return
}
