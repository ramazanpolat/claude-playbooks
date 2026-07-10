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
	Use:               "info <name>",
	Short:             "Show detailed information about a playbook",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: autocompletePlaybookNames,
	RunE:              runInfo,
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
	typePath := pb.RootPath
	if typePath == "" {
		typePath = pb.Path
	}
	linfo, _ := os.Lstat(typePath)
	if linfo != nil && linfo.Mode()&os.ModeSymlink != 0 {
		target, _ := os.Readlink(typePath)
		if _, err := os.Stat(typePath); err != nil {
			typeStr = fmt.Sprintf("symlink → %s (BROKEN)", target)
		} else {
			typeStr = fmt.Sprintf("symlink → %s", target)
		}
	}

	alias := pb.Alias
	if alias == "" {
		alias = "(none)"
	}

	countPath := pb.Path
	if resolved, err := filepath.EvalSymlinks(countPath); err == nil {
		countPath = resolved
	}
	fileCount, dirCount := countContents(countPath)

	fmt.Printf("Name:        %s\n", pb.Name)
	if pb.Manifest != nil && pb.Manifest.Version != "" {
		fmt.Printf("Version:     %s\n", pb.Manifest.Version)
	}
	fmt.Printf("Path:        %s\n", pb.Path)
	if typePath != pb.Path {
		fmt.Printf("Root:        %s\n", typePath)
	}
	fmt.Printf("Type:        %s\n", typeStr)
	fmt.Printf("Alias:       %s\n", alias)
	fmt.Printf("Size:        %d files, %d directories\n", fileCount, dirCount)
	fmt.Printf("Last used:   %s\n", formatAge(pb.LastUsed))
	if pb.Description != "" {
		fmt.Printf("Description: %s\n", pb.Description)
	}
	if pb.Manifest != nil && pb.Manifest.Homepage != "" {
		fmt.Printf("Homepage:    %s\n", pb.Manifest.Homepage)
	}
	if pb.Manifest != nil && pb.Manifest.Author != "" {
		fmt.Printf("Author:      %s\n", pb.Manifest.Author)
	}

	updaterName := "bin/update-playbook.sh"
	if pb.Manifest != nil && pb.Manifest.Source != nil && pb.Manifest.Source.UpdateScript != "" {
		updaterName = pb.Manifest.Source.UpdateScript
	}
	updater := filepath.Join(pb.Path, filepath.FromSlash(updaterName))
	if s, err := os.Stat(updater); err == nil && s.Mode()&0111 != 0 {
		fmt.Printf("Updater:     %s\n", updaterName)
	} else {
		fmt.Printf("Updater:     (none)\n")
	}

	return nil
}
