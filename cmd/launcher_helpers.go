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

	if !launcherOpsAllowed() {
		fmt.Fprintf(os.Stderr, "Note: launchers are managed only for the default playbooks root (%s); none written for custom root %s.\n", defaultPlaybooksRoot(), config.ResolvePlaybooksDir())
		fmt.Printf("\nRun with:\n  claude-playbook --playbooks-dir %s run %s\n", shell.QuoteArg(config.ResolvePlaybooksDir()), shell.QuoteArg(playbookName))
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
		fmt.Fprintf(os.Stderr, "Register a different command name with: claude-playbook alias %s <name> — rename the playbook (and its command) with: claude-playbook rename %s <new-name> — or remove the conflicting file.\n", shell.QuoteArg(playbookName), shell.QuoteArg(playbookName))
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
// other executable may resolve first.
func warnIfShadowedOrUnreachable(cmdName, path, configDir string) {
	if resolved, err := exec.LookPath(cmdName); err != nil {
		dir, _ := config.ResolveLauncherDir()
		fmt.Fprintf(os.Stderr, "Warning: %s is not on your PATH. Add it with:\n  export PATH=%s:\"$PATH\"\n", dir, shell.QuoteArg(dir))
	} else if resolved != path {
		fmt.Fprintf(os.Stderr, "Warning: %q resolves to %s, which shadows the launcher at %s\n", cmdName, resolved, path)
	}
}

// defaultPlaybooksRoot is the root multicall dispatch resolves when neither
// flag nor environment applies.
func defaultPlaybooksRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude-playbooks")
}

// launcherOpsAllowed gates EVERY launcher mutation (create, delete, rename)
// on operating against the default playbooks root. A symlink carries no
// root identity, so a launcher can only ever mean "this name in the default
// registry": a --playbooks-dir flag exists just for the current process,
// and an environment override naming a NON-default root cannot be
// distinguished from the documented one-shot assignment — mutating links
// for either would corrupt the default registry's commands. How the default
// root was expressed does not matter: an exported
// CLAUDE_PLAYBOOKS_DIR=$HOME/.claude-playbooks resolves to the same
// registry dispatch will see and is allowed.
func launcherOpsAllowed() bool {
	return samePath(config.ResolvePlaybooksDir(), defaultPlaybooksRoot())
}

// samePath compares canonicalized paths: ~/.claude-playbooks may itself be
// a symlink, and an override naming its physical target addresses the same
// registry dispatch resolves.
func samePath(a, b string) bool {
	return canonPath(a) == canonPath(b)
}

func canonPath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}
	return p
}
