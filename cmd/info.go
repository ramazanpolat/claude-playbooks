package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

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
	playbooksDir := config.ResolvePlaybooksDir()

	pb, err := playbook.Require(playbooksDir, name)
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

	alias := pb.Alias()
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
	if pb.Manifest != nil && !pb.Manifest.Env.Empty() {
		label := "Env:         "
		if len(pb.Manifest.Env.Profiles) > 0 {
			fmt.Printf("%sprofiles %s\n", label, strings.Join(pb.Manifest.Env.Profiles, ", "))
			label = "             "
		}
		keys := make([]string, 0, len(pb.Manifest.Env.Set))
		for key := range pb.Manifest.Env.Set {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Printf("%sset %s=%s\n", label, key, pb.Manifest.Env.Set[key])
			label = "             "
		}
		unset := append([]string(nil), pb.Manifest.Env.Unset...)
		sort.Strings(unset)
		for _, key := range unset {
			fmt.Printf("%sunset %s\n", label, key)
			label = "             "
		}
	}

	if pb.Manifest != nil && pb.Manifest.Source != nil && pb.Manifest.Source.Repository != "" {
		fmt.Printf("Update from: %s\n", pb.Manifest.Source.Repository)
	} else {
		fmt.Printf("Update from: (no [source] metadata; cannot update)\n")
	}
	migrations := filepath.Join(pb.Path, "migrations", "apply.sh")
	if s, err := os.Stat(migrations); err == nil && !s.IsDir() && s.Mode()&0111 != 0 {
		fmt.Printf("Migrations:  migrations/apply.sh\n")
	}

	return nil
}
