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
	Use:   "delete <name>",
	Short: "Delete a playbook and its alias",
	Args:  cobra.ExactArgs(1),
	RunE:  runDelete,
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

	pb, err := playbook.Require(playbooksDir, shellConfig, name)
	if err != nil {
		return err
	}

	if !deleteYes {
		aliasInfo := "(no alias)"
		if pb.HasAlias() {
			aliasInfo = fmt.Sprintf("%s (will be removed from %s)", pb.Alias, shellConfig)
		}
		fileCount, dirCount := countContents(pb.Path)
		fmt.Printf("Playbook: %s\n", pb.Name)
		fmt.Printf("Location: %s\n", pb.Path)
		fmt.Printf("Alias:    %s\n", aliasInfo)
		fmt.Printf("Contents: %d files, %d directories\n", fileCount, dirCount)
		fmt.Print("\nPermanently delete? [y/N] ")

		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		answer = strings.TrimSpace(strings.ToLower(answer))
		if answer != "y" && answer != "yes" {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	// Remove alias from shell config.
	if pb.HasAlias() {
		if _, err := shell.Remove(shellConfig, name); err != nil {
			return fmt.Errorf("failed to remove alias: %w", err)
		}
	}

	// Delete directory (resolve symlink target too if it's a symlink).
	linfo, err := os.Lstat(pb.Path)
	if err == nil {
		if linfo.Mode()&os.ModeSymlink != 0 {
			if err := os.Remove(pb.Path); err != nil {
				return fmt.Errorf("failed to remove symlink: %w", err)
			}
		} else {
			if err := os.RemoveAll(pb.Path); err != nil {
				return fmt.Errorf("failed to delete directory: %w", err)
			}
		}
	}

	fmt.Printf("Deleted playbook %q.\n", name)
	return nil
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
