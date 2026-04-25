package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

var listCmd = &cobra.Command{
	Use:   "list [prefix]",
	Short: "List all playbooks (optionally filtered by prefix)",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runList,
}

func runList(cmd *cobra.Command, args []string) error {
	shellConfig, err := config.ResolveShellConfig()
	if err != nil {
		return err
	}
	playbooksDir := config.ResolvePlaybooksDir()

	pbs, err := playbook.Discover(playbooksDir, shellConfig)
	if err != nil {
		return err
	}

	prefix := ""
	if len(args) == 1 {
		prefix = args[0]
	}
	if prefix != "" {
		filtered := pbs[:0]
		for _, pb := range pbs {
			if strings.HasPrefix(pb.Name, prefix) {
				filtered = append(filtered, pb)
			}
		}
		pbs = filtered
	}

	if len(pbs) == 0 {
		fmt.Println("No playbooks found. Run 'claude-playbook create <name>' to get started.")
		return nil
	}

	// Dynamic widths.
	nameW, pathW, aliasW := 4, 4, 5
	for _, pb := range pbs {
		if w := len(pb.Name); w > nameW {
			nameW = w
		}
		if w := len(pb.Path); w > pathW {
			pathW = w
		}
		a := pb.Alias
		if a == "" {
			a = "-"
		}
		if w := len(a); w > aliasW {
			aliasW = w
		}
	}

	fmt.Printf("%-*s  %-*s  %-*s  %s\n", nameW, "NAME", pathW, "PATH", aliasW, "ALIAS", "LAST USED")
	fmt.Printf("%-*s  %-*s  %-*s  %s\n", nameW, "----", pathW, "----", aliasW, "-----", "---------")
	for _, pb := range pbs {
		alias := pb.Alias
		if alias == "" {
			alias = "-"
		}
		fmt.Printf("%-*s  %-*s  %-*s  %s\n",
			nameW, pb.Name,
			pathW, pb.Path,
			aliasW, alias,
			formatAge(pb.LastUsed),
		)
	}
	return nil
}

func formatAge(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	case d < 48*time.Hour:
		return "yesterday"
	default:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
}
