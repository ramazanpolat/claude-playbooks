package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/launcher"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

// Version is set at build time via -ldflags.
var Version = "dev"

var rootCmd = &cobra.Command{
	Use:           "claude-playbook",
	Short:         "Manage isolated Claude Code instances",
	Version:       Version,
	SilenceErrors: true,
	SilenceUsage:  true,
	RunE:          runRoot,
}

func Execute() {
	// Multicall dispatch happens ONLY when invoked through a launcher
	// symlink: a regular binary installed under a name that happens to
	// match a playbook must keep the CLI reachable. Within a launcher
	// invocation, a resolvable name dispatches and an unresolvable one is
	// stale (its playbook was deleted or renamed away) and fails loudly
	// rather than falling through to the CLI overview with exit 0.
	if base := filepath.Base(os.Args[0]); !launcher.ReservedNames[base] && invokedViaLauncher() {
		// Registry overrides passed to the launcher must apply BEFORE name
		// resolution, or the name resolves against the default registry and
		// can pick a same-named playbook from the wrong root.
		applyRegistryOverrides(os.Args[1:])
		name, ok, derr := multicallPlaybook()
		if derr != nil {
			// The registry itself is unreadable — very different from this
			// launcher being stale; name the real cause.
			fmt.Fprintf(os.Stderr, "Error: %v\n", derr)
			os.Exit(1)
		}
		if ok {
			if err := runRun(nil, append([]string{name}, os.Args[1:]...)); err != nil {
				if code, ok := exitCode(err); ok {
					os.Exit(code)
				}
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		fmt.Fprintf(os.Stderr, "Error: unknown playbook %q — this launcher no longer matches any playbook. Remove the link or recreate the playbook. (If this symlink is your own alias for the CLI, name it %q or %q, or use a hard link.)\n", base, "claude-playbook", "cpb")
		os.Exit(1)
	}
	if err := rootCmd.Execute(); err != nil {
		if code, ok := exitCode(err); ok {
			os.Exit(code)
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVar(&config.PlaybooksDir, "playbooks-dir", "", "playbooks directory (default: ~/.claude-playbooks)")
	rootCmd.PersistentFlags().StringVar(&config.LauncherDir, "launcher-dir", "", "directory for launcher commands (default: directory of this binary)")

	rootCmd.AddCommand(listCmd)
	rootCmd.AddCommand(createCmd)
	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(startCmd)
	rootCmd.AddCommand(installCmd)
	rootCmd.AddCommand(linkCmd)
	rootCmd.AddCommand(infoCmd)
	rootCmd.AddCommand(renameCmd)
	rootCmd.AddCommand(aliasCmd)
	rootCmd.AddCommand(dealiasCmd)
	rootCmd.AddCommand(envCmd)
	rootCmd.AddCommand(envProfileCmd)
	rootCmd.AddCommand(authCmd)
	rootCmd.AddCommand(deleteCmd)
	rootCmd.AddCommand(selfUninstallCmd)
	rootCmd.AddCommand(updateCmd)
	rootCmd.AddCommand(completionCmd)
}

func runRoot(cmd *cobra.Command, args []string) error {
	playbooksDir := config.ResolvePlaybooksDir()

	pbs, err := playbook.Discover(playbooksDir)
	if err != nil {
		return err
	}

	fmt.Println("claude-playbook -- manage isolated Claude Code instances")
	fmt.Println()
	fmt.Printf("Playbooks directory: %s\n", playbooksDir)

	if len(pbs) == 0 {
		fmt.Println("No playbooks installed yet. Get started with one of:")
		fmt.Println()
		fmt.Println("  # Install a single playbook from a Git repo:")
		fmt.Println("  claude-playbook install https://github.com/user/pai")
		fmt.Println()
		fmt.Println("  # Cherry-pick one playbook out of a monorepo (e.g. DBA):")
		fmt.Println("  claude-playbook install https://github.com/ramazanpolat/awesome-playbooks/tree/main/playbooks/dba")
		fmt.Println()
		fmt.Println("  # Create your own from scratch:")
		fmt.Println("  claude-playbook create <name>")
		fmt.Println()
		fmt.Println("Run 'claude-playbook --help' for all commands.")
		return nil
	}

	fmt.Println()
	fmt.Println("Available playbooks:")
	fmt.Println()

	maxLen := 0
	for _, pb := range pbs {
		if l := len(pb.Name); l > maxLen {
			maxLen = l
		}
	}
	cmdColW := maxLen + len("claude-playbook run ")

	// Launcher commands take display precedence over manifest aliases,
	// exactly as in `list` — a launcher-only playbook has a working command
	// and must not be shown as "(no alias set)".
	// Gate before resolving: ResolveLauncherDir probes directory writability
	// by creating a temp file, which a custom-root invocation must not do.
	launcherNames := map[string]bool{}
	if launcherOpsAllowed() {
		if ldir, lerr := config.ResolveLauncherDir(); lerr == nil {
			if les, lerr := launcher.List(ldir); lerr == nil {
				for _, e := range les {
					if !launcher.ReservedNames[e.CmdName] {
						launcherNames[e.CmdName] = true
					}
				}
			}
		}
	}
	for _, pb := range pbs {
		runStr := fmt.Sprintf("claude-playbook run %s", pb.Name)
		command := ""
		for _, n := range launcherNamesFor(pb) {
			if launcherNames[n] {
				command = n
				break
			}
		}
		if command != "" {
			fmt.Printf("  %-*s  %-*s  (or: %s)\n", maxLen, pb.Name, cmdColW, runStr, command)
		} else {
			fmt.Printf("  %-*s  %-*s  (no command registered)\n", maxLen, pb.Name, cmdColW, runStr)
		}
	}

	fmt.Println()
	fmt.Println("Run 'claude-playbook --help' for all commands.")
	return nil
}
