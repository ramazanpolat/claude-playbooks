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
	Use:   "cpb",
	Short: "cpb (Claude PlayBooks): manage isolated Claude Code instances",
	// Statements never reach cobra, so its help names them itself.
	Long: `cpb (Claude PlayBooks): manage isolated Claude Code instances.

State is changed and read with statements:

  cpb CREATE | ALTER | DROP   PLAYBOOK | ENV | DEFAULTS  <name> <clause> ...
  cpb SHOW [PLAYBOOKS | ENVS | DEFAULTS | PLAYBOOK <name> | ENV <name>] [--json]
  cpb SHOW CREATE { PLAYBOOK <name> | ENV <name> | ALL }
  cpb EXPLAIN PLAYBOOK <name> [--json]
  cpb APPLY <file> [<file> ...] [--dry-run] [--yes]

The grammar: https://github.com/ramazanpolat/claude-playbooks/blob/main/docs/reference/cli-grammar.md`,
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
		fmt.Fprintf(os.Stderr, "Error: unknown playbook %q — this launcher no longer matches any playbook. Remove the link or recreate the playbook. (If this symlink is your own alias for the CLI, name it %q, or use a hard link.)\n", base, "cpb")
		os.Exit(1)
	}
	// A grammar statement never reaches cobra, which would read its words
	// as flags and subcommands (docs/cli-grammar.md).
	if stmt, ok := statementArgs(os.Args[1:]); ok {
		if err := runStatement(stmt); err != nil {
			// APPLY --json has printed its report and exits with its class.
			if code, ok := exitCode(err); ok {
				os.Exit(code)
			}
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		return
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

	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(startCmd)
	rootCmd.AddCommand(authCmd)
	rootCmd.AddCommand(selfUninstallCmd)
	rootCmd.AddCommand(updateCmd)
	rootCmd.AddCommand(selfUpdateCmd)
	rootCmd.AddCommand(completionCmd)
	rootCmd.AddCommand(tuiCmd)
}

func runRoot(cmd *cobra.Command, args []string) error {
	playbooksDir := config.ResolvePlaybooksDir()

	pbs, err := playbook.Discover(playbooksDir)
	if err != nil {
		return err
	}

	fmt.Println("cpb (Claude PlayBooks) -- manage isolated Claude Code instances")
	fmt.Println()
	fmt.Printf("Playbooks directory: %s\n", playbooksDir)

	if len(pbs) == 0 {
		fmt.Println("No playbooks installed yet. Get started with one of:")
		fmt.Println()
		fmt.Println("  # Your own, from scratch:")
		fmt.Println("  cpb CREATE PLAYBOOK <name>")
		fmt.Println()
		fmt.Println("  # One from a Git repository or a directory (SUBDIR picks one out of a monorepo):")
		fmt.Println("  cpb CREATE PLAYBOOK <name> FROM <git-url-or-dir>")
		fmt.Println()
		fmt.Println("Run 'cpb --help' for all commands.")
		printTUIHint()
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
	cmdColW := maxLen + len("cpb run ")

	// Launcher commands take display precedence over manifest aliases: a
	// launcher-only playbook has a working command and must not be shown as
	// "(no launcher)".
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
		runStr := fmt.Sprintf("cpb run %s", pb.Name)
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
			fmt.Printf("  %-*s  %-*s  (no launcher)\n", maxLen, pb.Name, cmdColW, runStr)
		}
	}

	fmt.Println()
	fmt.Println("Run 'cpb --help' for all commands.")
	printTUIHint()
	return nil
}

// printTUIHint is bare cpb's last line on a terminal (v3.25.0). Off a
// terminal the output is exactly what it was.
func printTUIHint() {
	if rootTTY() {
		fmt.Println("Browse and manage them: cpb tui")
	}
}

// rootTTY is whether stdout is a terminal; a variable for tests.
var rootTTY = func() bool { return isTerminal(os.Stdout) }
