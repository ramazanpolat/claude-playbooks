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
	Short: "Delete a playbook or a container",
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

	target, err := playbook.ResolveTarget(playbooksDir, name)
	if err != nil {
		return err
	}

	if target.IsPlaybook {
		return deletePlaybook(playbooksDir, shellConfig, target.Name, target.Path)
	}
	return deleteContainer(playbooksDir, shellConfig, target.Name, target.Path)
}

func deletePlaybook(playbooksDir, shellConfig, name, path string) error {
	pb, err := playbook.Find(playbooksDir, shellConfig, name)
	if err != nil {
		return err
	}

	if !deleteYes {
		aliasInfo := "(no alias)"
		if pb != nil && pb.HasAlias() {
			aliasInfo = fmt.Sprintf("%s (will be removed from %s)", pb.Alias, shellConfig)
		}
		fileCount, dirCount := countContents(path)
		fmt.Printf("Playbook: %s\n", name)
		fmt.Printf("Location: %s\n", path)
		fmt.Printf("Alias:    %s\n", aliasInfo)
		fmt.Printf("Contents: %d files, %d directories\n", fileCount, dirCount)
		if !confirm("\nPermanently delete? [y/N] ") {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	if _, err := shell.RemoveByPath(shellConfig, path); err != nil {
		return fmt.Errorf("failed to clean up aliases: %w", err)
	}
	if err := removeAny(path); err != nil {
		return fmt.Errorf("failed to delete %s: %w", path, err)
	}
	fmt.Printf("Deleted playbook %q.\n", name)
	return nil
}

func deleteContainer(playbooksDir, shellConfig, name, path string) error {
	kids, _ := playbook.DiscoverUnder(playbooksDir, name)

	if !deleteYes {
		fmt.Printf("Container: %s\n", name)
		fmt.Printf("Location:  %s\n", path)
		if len(kids) == 0 {
			fmt.Println("Contains no playbooks.")
		} else {
			fmt.Printf("Contains %d playbook%s:\n", len(kids), pluralS(len(kids)))
			pbs, _ := playbook.Discover(playbooksDir, shellConfig)
			for _, k := range kids {
				aliasInfo := "no alias"
				for _, pb := range pbs {
					if pb.Name == k && pb.HasAlias() {
						aliasInfo = "alias: " + pb.Alias
					}
				}
				fmt.Printf("  %s  (%s)\n", k, aliasInfo)
			}
		}
		fileCount, dirCount := countContents(path)
		fmt.Printf("Total:     %d files, %d directories\n", fileCount, dirCount)
		if !confirm("\nPermanently delete container and all playbooks inside? [y/N] ") {
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
	fmt.Printf("Deleted container %q (%d playbook%s).\n", name, len(kids), pluralS(len(kids)))
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
