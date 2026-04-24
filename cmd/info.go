package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
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

	// Type: directory, symlink, or broken symlink.
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

	m, _ := manifest.Read(pb.Path)
	alias := pb.Alias
	if alias == "" {
		alias = "(none)"
	}

	fileCount, dirCount := countContents(pb.Path)

	fmt.Printf("Name:        %s\n", pb.Name)
	if m != nil && m.Version != "" {
		fmt.Printf("Version:     %s\n", m.Version)
	}
	fmt.Printf("Path:        %s\n", pb.Path)
	fmt.Printf("Type:        %s\n", typeStr)
	fmt.Printf("Alias:       %s\n", alias)
	fmt.Printf("Size:        %d files, %d directories\n", fileCount, dirCount)
	fmt.Printf("Last used:   %s\n", formatAge(pb.LastUsed))
	if m != nil && m.Description != "" {
		fmt.Printf("Description: %s\n", m.Description)
	}

	updater := filepath.Join(pb.Path, "bin", "update-playbook.sh")
	if s, err := os.Stat(updater); err == nil && s.Mode()&0111 != 0 {
		fmt.Printf("Updater:     bin/update-playbook.sh\n")
	} else {
		fmt.Printf("Updater:     (none)\n")
	}

	return nil
}
