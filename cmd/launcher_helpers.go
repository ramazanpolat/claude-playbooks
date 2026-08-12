package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/launcher"
	"github.com/ramazanpolat/claude-playbooks/internal/shell"
)

// installLauncher writes the launcher command for a playbook and reports the
// outcome. The playbook itself is already on disk when this runs, so every
// failure is a warning with manual instructions, never a command failure.
func installLauncher(cmdName, playbookName, configDir string) {
	manual := func() {
		fmt.Printf("\nRun with:\n  claude-playbook run %s\n", shell.QuoteArg(playbookName))
	}

	dir, err := config.ResolveLauncherDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: no launcher written: %v\n", err)
		manual()
		return
	}
	path, err := launcher.Write(dir, cmdName, playbookName, configDir, config.ResolvePlaybooksDir())
	if errors.Is(err, launcher.ErrTaken) {
		fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
		fmt.Fprintf(os.Stderr, "Pick another name with: claude-playbook alias %s <name>\n", shell.QuoteArg(playbookName))
		manual()
		return
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: no launcher written: %v\n", err)
		manual()
		return
	}

	fmt.Printf("Command:  %s  (launcher at %s)\n", cmdName, path)
	warnIfShadowedOrUnreachable(cmdName, path, configDir)
	fmt.Printf("\nRun it now:\n  %s\n", shell.QuoteArg(cmdName))
}

// warnIfShadowedOrUnreachable checks that typing cmdName will actually run
// the launcher just written: the launcher directory must be on PATH and no
// other executable may resolve first, and no rc-file alias from an earlier
// install may shadow it in interactive shells.
func warnIfShadowedOrUnreachable(cmdName, path, configDir string) {
	if resolved, err := exec.LookPath(cmdName); err != nil {
		dir, _ := config.ResolveLauncherDir()
		fmt.Fprintf(os.Stderr, "Warning: %s is not on your PATH. Add it with:\n  export PATH=%s:\"$PATH\"\n", dir, shell.QuoteArg(dir))
	} else if resolved != path {
		fmt.Fprintf(os.Stderr, "Warning: %q resolves to %s, which shadows the launcher at %s\n", cmdName, resolved, path)
	}
	// A same-named alias from an alias-era install wins over PATH in
	// interactive shells. Pointing at another playbook is a trap worth
	// naming; pointing at this one still works, so stay quiet.
	if cfg, err := config.ResolveShellConfig(); err == nil {
		if entries, err := shell.ReadAll(cfg); err == nil {
			for _, e := range entries {
				if e.AliasName == cmdName && e.Path != configDir {
					fmt.Fprintf(os.Stderr, "Warning: alias %q in %s points at %s and shadows this command in interactive shells. Remove it with: claude-playbook dealias %s\n",
						cmdName, cfg, e.Path, shell.QuoteArg(cmdName))
				}
			}
		}
	}
}
