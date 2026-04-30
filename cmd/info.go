package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

var infoCmd = &cobra.Command{
	Use:   "info <name>",
	Short: "Show detailed information about a playbook",
	Args:  cobra.ExactArgs(1),
	RunE:  runInfo,
}

func runInfo(cmd *cobra.Command, args []string) error {
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

	typeStr := "directory"
	linfo, _ := os.Lstat(pb.Path)
	if linfo != nil && linfo.Mode()&os.ModeSymlink != 0 {
		target, _ := os.Readlink(pb.Path)
		if _, err := os.Stat(pb.Path); err != nil {
			typeStr = fmt.Sprintf("symlink → %s (BROKEN)", target)
		} else {
			typeStr = fmt.Sprintf("symlink → %s", target)
		}
	}

	alias := pb.Alias
	if alias == "" {
		alias = "(none)"
	}

	fileCount, dirCount := countContents(pb.Path)

	fmt.Printf("Name:        %s\n", pb.Name)
	if pb.IsChild {
		fmt.Printf("Parent:      %s\n", pb.Parent)
	}
	if pb.Manifest != nil && pb.Manifest.Version != "" {
		fmt.Printf("Version:     %s\n", pb.Manifest.Version)
	}
	fmt.Printf("Path:        %s\n", pb.Path)
	fmt.Printf("Type:        %s\n", typeStr)
	fmt.Printf("Alias:       %s\n", alias)
	fmt.Printf("Size:        %d files, %d directories\n", fileCount, dirCount)
	fmt.Printf("Last used:   %s\n", formatAge(pb.LastUsed))
	if pb.Description != "" {
		fmt.Printf("Description: %s\n", pb.Description)
	}

	if !pb.IsChild {
		updater := filepath.Join(pb.Path, "bin", "update-playbook.sh")
		if s, err := os.Stat(updater); err == nil && s.Mode()&0111 != 0 {
			fmt.Printf("Updater:     bin/update-playbook.sh\n")
		} else {
			fmt.Printf("Updater:     (none)\n")
		}

		children := playbook.Children(playbooksDir, shellConfig, pb)
		if len(children) > 0 {
			fmt.Println("Children:")
			nameW := 0
			pathW := 0
			for _, c := range children {
				if l := len(c.Name); l > nameW {
					nameW = l
				}
				if c.ChildSpec != nil {
					if l := len(c.ChildSpec.Path); l > pathW {
						pathW = l
					}
				}
			}
			for _, c := range children {
				ap := "no alias"
				if c.HasAlias() {
					ap = "alias: " + c.Alias
				}
				cp := ""
				if c.ChildSpec != nil {
					cp = c.ChildSpec.Path
				}
				fmt.Printf("  %-*s  %-*s  (%s)\n", nameW, c.Name, pathW, cp, ap)
			}
		}
	}

	return nil
}
