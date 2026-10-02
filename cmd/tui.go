package cmd

import (
	"errors"
	"os"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/tui"
)

// tuiCmd is `cpb tui` (v3.25.0): a terminal UI over the grammar. It reads
// through cpb's own --json outputs, run as subprocesses of this binary,
// and in v1 changes nothing (docs/reference/cli-grammar.md, "cpb tui").
var tuiCmd = &cobra.Command{
	Use:   "tui",
	Short: "Browse playbooks, sessions and env sets in a terminal UI (read-only)",
	Args:  cobra.NoArgs,
	RunE:  runTUI,
}

var errTUINeedsTerminal = errors.New("cpb tui needs a terminal; use cpb SHOW … --json for scripts")

// tuiTTY is whether stdin and stdout are a terminal; a variable for tests.
var tuiTTY = func() bool { return isTerminal(os.Stdin) && isTerminal(os.Stdout) }

func runTUI(cmd *cobra.Command, args []string) error {
	if !tuiTTY() {
		return errTUINeedsTerminal
	}
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	var prefix []string
	if config.PlaybooksDir != "" {
		prefix = []string{"--playbooks-dir", config.PlaybooksDir}
	}
	r := tui.ExecRunner{Bin: bin, Prefix: prefix}
	cwd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	return tui.Run(tui.Options{
		Runner: r,
		Home:   home,
		Cwd:    cwd,
	})
}
