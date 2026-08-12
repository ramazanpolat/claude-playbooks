package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

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

	// A launcher resolves against the DEFAULT registry at invocation time
	// (env or ~/.claude-playbooks): a symlink cannot carry a --playbooks-dir
	// flag. Writing one for a different root would at best dangle and at
	// worst run a same-named playbook from the default root — skip it and
	// say how to run the playbook instead.
	if effective, dispatch := config.ResolvePlaybooksDir(), dispatchPlaybooksRoot(); !samePath(effective, dispatch) {
		fmt.Fprintf(os.Stderr, "Note: custom playbooks root %s is not what launchers resolve against at invocation time (%s); no launcher written.\n", effective, dispatch)
		fmt.Printf("\nRun with:\n  claude-playbook --playbooks-dir %s run %s\n", shell.QuoteArg(effective), shell.QuoteArg(playbookName))
		return
	}

	// The registry is the ownership authority: refuse a command name that
	// already addresses another playbook, or the shared name would resolve
	// to whichever playbook wins the registry scan.
	if owner, oerr := commandNameOwner(cmdName, playbookName); oerr == nil && owner != nil {
		fmt.Fprintf(os.Stderr, "Warning: command name %q already addresses playbook %q; no launcher written\n", cmdName, owner.Name)
		manual()
		return
	}

	dir, err := config.ResolveLauncherDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: no launcher written: %v\n", err)
		manual()
		return
	}
	path, err := launcher.Write(dir, cmdName)
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

// dispatchPlaybooksRoot is the playbooks root multicall dispatch will
// resolve when a launcher is invoked: the environment override or the
// default — never the --playbooks-dir flag, which exists only for the
// current process.
func dispatchPlaybooksRoot() string {
	if v := os.Getenv("CLAUDE_PLAYBOOKS_DIR"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude-playbooks")
}

func samePath(a, b string) bool {
	if aa, err := filepath.Abs(a); err == nil {
		a = aa
	}
	if bb, err := filepath.Abs(b); err == nil {
		b = bb
	}
	return a == b
}
