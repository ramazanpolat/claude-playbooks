package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/auth"
	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// createOpts carries CREATE PLAYBOOK's clauses. No state is shared between
// two calls.
type createOpts struct {
	launcher      string
	noLauncher    bool
	sandbox       bool
	isolatedLogin bool // isolated_login = true without a sandbox
}

func doCreate(o createOpts, args []string) error {
	if err := checkLauncherConflict(o.launcher, o.noLauncher); err != nil {
		return err
	}

	name := args[0]
	if strings.Contains(name, "/") {
		return fmt.Errorf("playbook name cannot contain '/'")
	}
	if err := validateTopLevelName("playbook name", name); err != nil {
		return err
	}

	playbooksDir := config.ResolvePlaybooksDir()
	if err := os.MkdirAll(playbooksDir, 0755); err != nil {
		return err
	}
	dest := filepath.Join(playbooksDir, name)

	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("playbook %q already exists at %s", name, dest)
	}

	// The EFFECTIVE launcher name (explicit --alias or the playbook name
	// itself) must be writable, or creation would succeed without its
	// advertised command. --no-alias opts out of a launcher entirely and
	// skips this.
	launcherName, err := resolveLauncherName(o.noLauncher, o.launcher, name, "create the playbook")
	if err != nil {
		return err
	}

	// Preflight launcher names BEFORE the directory exists: once created it
	// joins the registry, and dispatch resolves directory names ahead of
	// aliases, so a clash would silently re-route an existing command.
	// Serialize preflight-through-registration: without the registry lock,
	// two concurrent creates can both pass the ownership check and register
	// duplicate owners for one launcher name.
	unlock, err := lockRegistry()
	if err != nil {
		return err
	}
	defer unlock()

	// The directory name joins the registry even under --no-alias.
	preflightNames := []string{name}
	if launcherName != "" {
		preflightNames = append(preflightNames, launcherName)
	}
	if err := preflightCommandNames("", preflightNames...); err != nil {
		return err
	}

	if err := os.MkdirAll(dest, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// An always-sandboxed playbook authenticates on its own: the machine
	// login cannot follow it into the sandbox. Written before the
	// credential sync so the sync already sees the isolation.
	if o.sandbox || o.isolatedLogin {
		m := &manifest.Manifest{Name: name, IsolatedLogin: true}
		if o.sandbox {
			m.Sandbox = &manifest.Sandbox{Always: true}
		}
		if err := manifest.Write(dest, m); err != nil {
			os.RemoveAll(dest)
			return fmt.Errorf("cannot record the sandbox or login setting in the manifest: %w", err)
		}
	}

	if err := auth.SyncCredentials(dest); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to sync credentials: %v\n", err)
	}

	if err := writeDefaultClaudeMD(dest, name); err != nil {
		return fmt.Errorf("failed to write CLAUDE.md: %w", err)
	}

	fmt.Printf("Created playbook %q at %s\n", name, dest)
	if o.sandbox {
		fmt.Printf("Always sandboxed (%s); authentication isolated: run /login once inside the sandbox.\n", defaultSandboxBackend)
	} else if o.isolatedLogin {
		fmt.Println("Login isolated: it shares no login with ~/.claude; run /login once in it.")
	}

	if o.noLauncher {
		fmt.Printf("\nRun with:\n  cpb run %s\n", name)
	} else {
		// A custom launcher name must be resolvable at invocation time: record
		// it as the manifest alias so multicall dispatch finds the playbook.
		if launcherName != name {
			if err := writeAliasManifest(dest, name, launcherName); err != nil {
				// Without the manifest entry the alias can never resolve; and
				// dest already joined the registry, so leaving it would block a
				// retry under the same name — roll it back, as install does.
				os.RemoveAll(dest)
				return fmt.Errorf("cannot record launcher %q in manifest (required for the launcher to resolve): %w", launcherName, err)
			}
		}

		installLauncher(launcherName, name, dest)
	}

	return nil
}

// defaultClaudeMD is written into a freshly created playbook so that the
// Claude Code session opened inside it knows what a playbook is. It imports
// nothing: the playbook sees only its own config dir until the pilot adds
// instructions of their own. The pilot is expected to replace it.
const defaultClaudeMD = "# Playbook: %[1]s\n\n" +
	"This Claude Code session runs inside a **playbook**: a Claude Code config directory of its own, managed by cpb (Claude PlayBooks).\n\n" +
	"`CLAUDE_CONFIG_DIR` points to this directory, so settings, hooks, memory, conversation history, MCP servers, agents, slash commands and this `CLAUDE.md` belong to this playbook. Nothing here changes `~/.claude` or any other playbook.\n\n" +
	"## Useful cpb statements\n\n" +
	"```\n" +
	"cpb SHOW PLAYBOOKS                      # every playbook and its launcher\n" +
	"cpb SHOW PLAYBOOK %[1]s\n" +
	"cpb ALTER PLAYBOOK %[1]s RENAME TO <name>\n" +
	"cpb DROP PLAYBOOK %[1]s\n" +
	"```\n\n" +
	"Reference: https://github.com/ramazanpolat/claude-playbooks\n\n" +
	"## Customizing\n\n" +
	"Replace this file with instructions for how *this* playbook should behave: that is what makes one playbook different from another.\n"

func writeDefaultClaudeMD(dir, name string) error {
	return os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(fmt.Sprintf(defaultClaudeMD, name)), 0644)
}
