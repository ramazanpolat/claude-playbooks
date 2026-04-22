package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all playbooks",
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

	if len(pbs) == 0 {
		fmt.Println("No playbooks found. Run 'claude-playbook create <name>' to get started.")
		return nil
	}

	fmt.Printf("%-20s  %-45s  %-12s  %s\n", "NAME", "PATH", "ALIAS", "LAST USED")
	fmt.Printf("%-20s  %-45s  %-12s  %s\n", "----", "----", "-----", "---------")
	for _, pb := range pbs {
		alias := "-"
		if pb.HasAlias() {
			alias = pb.Alias
		}
		fmt.Printf("%-20s  %-45s  %-12s  %s\n",
			pb.Name,
			pb.Path,
			alias,
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
