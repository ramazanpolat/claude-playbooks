package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/launcher"
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

	// Launcher commands take display precedence over rc-file aliases. A
	// launcher is addressed by name: a symlink matching the playbook's
	// name or manifest alias is its command.
	launcherNames := map[string]bool{}
	// Launchers exist only for the default root; under a custom root a
	// same-named global launcher would dispatch the DEFAULT playbook, so
	// advertising it here would be wrong.
	if ldir, lerr := config.ResolveLauncherDir(); lerr == nil && launcherOpsAllowed() {
		if les, lerr := launcher.List(ldir); lerr == nil {
			for _, e := range les {
				// The installer's own CLI shortcut (cpb -> claude-playbook)
				// is a symlink to this binary too; reserved names never
				// dispatch, so never advertise them as a playbook's command.
				if launcher.ReservedNames[e.CmdName] {
					continue
				}
				launcherNames[e.CmdName] = true
			}
		}
	}
	command := func(pb *playbook.Playbook) string {
		for _, n := range launcherNamesFor(pb) {
			if launcherNames[n] {
				return n
			}
		}
		return pb.Alias
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

	nameW, pathW, aliasW := 4, 4, 7
	for _, pb := range pbs {
		if w := len(pb.Name); w > nameW {
			nameW = w
		}
		if w := len(pb.Path); w > pathW {
			pathW = w
		}
		a := command(pb)
		if a == "" {
			a = "-"
		}
		if w := len(a); w > aliasW {
			aliasW = w
		}
	}

	fmt.Printf("%-*s  %-*s  %-*s  %s\n", nameW, "NAME", pathW, "PATH", aliasW, "COMMAND", "LAST USED")
	fmt.Printf("%-*s  %-*s  %-*s  %s\n", nameW, "----", pathW, "----", aliasW, "-------", "---------")
	for _, pb := range pbs {
		alias := command(pb)
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
