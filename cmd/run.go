package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/auth"
	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envprofile"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

var runCmd = &cobra.Command{
	Use:                "run <name> [claude-flags...]",
	Short:              "Run Claude Code with a playbook",
	DisableFlagParsing: true,
	ValidArgsFunction:  autocompletePlaybookNames,
	RunE:               runRun,
}

// errConfigDirOverrideSandbox refuses a sandboxed launch that also carries a
// caller-supplied config directory. A sandbox mounts the config directory, and
// a directory whose playbook content is reached through symlinks (the shape a
// caller provisioning its own necessarily produces) dangles inside, because
// the backend mounts directories: the same failure a shared login's symlinked
// credentials have. Half-supporting that is worse than saying so.
func errConfigDirOverrideSandbox() error {
	return fmt.Errorf("%s and a sandboxed launch together are not supported: a sandbox mounts the config directory, and content reached through symlinks dangles inside it. Launch on the host, or unset %s",
		config.ConfigDirOverrideEnv, config.ConfigDirOverrideEnv)
}

func runRun(cmd *cobra.Command, args []string) error {
	original := args
	rest, err := takePlaybooksDirArg(args)
	if err != nil {
		return err
	}
	// Launch flags may precede the name (`run --env K=V name`) or follow it
	// directly (`name --env K=V ...`, which is what a launcher passes). The
	// --sandbox family is scanned in the same leading runs.
	var sopts sandboxOpts
	rest, tokens, err := takeRunFlags(rest, &sopts, nil)
	if err != nil {
		return err
	}
	// A --help at the NAME position prints usage, whether or not launch
	// flags preceded it; after the name it belongs to claude.
	if restRequestsHelp(rest) {
		fmt.Println("Usage: claude-playbook run " + runFlagsUsage + " <name> [claude-flags...]")
		fmt.Println()
		fmt.Println("Runs Claude Code with the named playbook.")
		fmt.Println("Launch flags add one-off environment layers on top of the playbook's [env]")
		fmt.Println("block, in order, for this launch only; they go before the name or right after it.")
		fmt.Println("  --env-profile NAME   layer an existing env profile")
		fmt.Println("  --env KEY=VALUE      set one variable")
		fmt.Println("  --unset KEY          remove one variable (CLAUDE_CODE_OAUTH_TOKEN: use the stored login)")
		fmt.Println("  --env-file PATH      layer a dotenv-style file of KEY=VALUE lines")
		fmt.Println("Sandbox flags run the playbook inside a sandbox (backend sbx, Docker Sandboxes):")
		fmt.Println("  --sandbox[=BACKEND]  launch in the playbook's sandbox cpb-<name> (created on first use); --sbx is a synonym")
		fmt.Println("  --no-sandbox         launch on the host although the manifest says [sandbox] always = true")
		fmt.Println("  --sandbox-host U@H   run the sandboxed launch on that machine over ssh (claude-playbook and the playbook installed there)")
		fmt.Println("  --sandbox-fresh      remove and recreate that sandbox first")
		fmt.Println("  --clone              at creation, work on a private clone of the working directory's repo")
		fmt.Println("  --workdir PATH       working directory to mount and enter (default: current directory)")
		fmt.Println("  --mount PATH[:ro]    extra host path to mount (repeatable)")
		fmt.Println("Everything else after the name is forwarded directly to claude.")
		return nil
	}

	if len(rest) == 0 {
		return fmt.Errorf("playbook name required\nUsage: claude-playbook run " + runFlagsUsage + " <name> [claude-flags...]")
	}
	name := rest[0]
	claudeArgs, more, err := takeRunFlags(rest[1:], &sopts, nil)
	if err != nil {
		return err
	}
	tokens = append(tokens, more...)

	// A caller-supplied config directory, resolved before any sandbox
	// decision so the refusal below happens whatever shape the sandbox
	// request takes. Absent: every launch behaves exactly as before.
	overrideDir, override, err := config.ResolveConfigDirOverride()
	if err != nil {
		return err
	}

	// An explicit --sandbox-host (before or after the name) runs the whole
	// launch on that host: the playbook need not be registered here, and
	// no launch flag is evaluated here (env files stay unread).
	if sopts.host != "" {
		if sopts.disabled {
			return fmt.Errorf("--sandbox-host and --no-sandbox together: pick one")
		}
		if override {
			return errConfigDirOverrideSandbox()
		}
		return forwardToSandboxHost(sopts.host, "run", original, &sopts, tokens, nil, name, claudeArgs)
	}
	playbooksDirResolved := config.ResolvePlaybooksDir()

	pb, err := playbook.Find(playbooksDirResolved, name)
	if err != nil {
		return err
	}
	if pb == nil {
		return fmt.Errorf("unknown playbook %q. Run 'claude-playbook list' to see available playbooks", name)
	}

	var sbm *manifest.Sandbox
	if pb.Manifest != nil {
		sbm = pb.Manifest.Sandbox
	}
	sandboxed, backend, err := resolveSandbox(sbm, &sopts, fmt.Sprintf("playbook %q", pb.Name))
	if err != nil {
		return err
	}
	if sandboxed {
		if override {
			return errConfigDirOverrideSandbox()
		}
		if host := sandboxHost(sbm, &sopts); host != "" {
			return forwardToSandboxHost(host, "run", original, &sopts, tokens, nil, name, claudeArgs)
		}
		// Only now is the launch known to be local: evaluate the launch
		// flags (env files are read here and nowhere earlier).
		layers, err := launchLayers(tokens)
		if err != nil {
			return err
		}
		// The registry's spelling of the config directory, made absolute
		// so a relative --playbooks-dir cannot leak a relative
		// CLAUDE_CONFIG_DIR into a sandbox whose working directory is
		// elsewhere.
		configPath, err := filepath.Abs(pb.Path)
		if err != nil {
			return err
		}
		rootPath := pb.RootPath
		if rootPath == "" {
			rootPath = pb.Path
		}
		if rootPath, err = filepath.Abs(rootPath); err != nil {
			return err
		}
		_, err = runSandboxed(sandboxTarget{
			label: fmt.Sprintf("playbook %q", pb.Name), name: sandboxName(pb.Name),
			configPath: configPath, rootPath: rootPath, manifest: sbm, backend: backend,
		}, layers, claudeArgs, sopts)
		return err
	}

	// The pilot's input is validated before the machine is inspected.
	//
	// launchLayers is what rejects a --env that is not KEY=VALUE, a reserved
	// key, an unreadable or malformed --env-file, and an unknown profile. With
	// the claude lookup first, all of those reported "'claude' command not
	// found" instead: a pilot who mistyped a flag was sent to install an agent
	// they may already have. Only the token scan above survived, so of six ways
	// to get the flags wrong, one named itself.
	//
	// Order is the whole fix, and it is safe in both directions: launchLayers
	// only validates and reads (an --env-file), while everything that MUTATES
	// -- credential quarantine and sync in PrepareLaunchEnv below -- still
	// happens after the lookup, so a machine without an agent is never touched.
	// It also stays after the remote-forward decision, which is what keeps a
	// --sandbox-host launch from reading local env files at all.
	layers, err := launchLayers(tokens)
	if err != nil {
		return err
	}

	// The config directory this launch binds: the playbook's install
	// directory, or the one the caller supplied. Everything downstream --
	// the authentication decision, credential sync, quarantine, the
	// governing manifest lookup -- then operates on it uniformly, with no
	// special case for where it came from.
	configDir := pb.Path
	if override {
		configDir = overrideDir
	}

	// The sixth way to get the flags wrong: a profile that does not resolve.
	// Its refusal lives inside PrepareLaunchEnv, which cannot move above the
	// lookup because it mutates credentials -- so resolve the block here,
	// purely, and let the input name itself like the other five. EffectiveBlock
	// is the same resolution PrepareLaunchEnv performs, reading only; doing it
	// twice costs a few file reads and cannot diverge, being one function.
	if _, perr := auth.EffectiveBlock(configDir, layers); errors.Is(perr, envprofile.ErrProfile) {
		return perr
	}

	claudePath, err := exec.LookPath("claude")
	if err != nil {
		return fmt.Errorf("'claude' command not found. Install Claude Code first: https://claude.ai/download")
	}
	launchEnv, syncErr := auth.PrepareLaunchEnvWith(configDir, layers)
	if errors.Is(syncErr, envprofile.ErrProfile) {
		// Missing, unreadable, or invalid profile: launching with a silently
		// dropped layer could send traffic to the wrong endpoint with the
		// wrong credentials -- refuse, do not warn.
		return syncErr
	}
	if syncErr != nil {
		// Neutral wording: PrepareLaunchEnv may have been syncing credentials,
		// account metadata, or detaching for an isolated playbook. Naming
		// credentials specifically sent users after credential files and symlinks
		// for failures in paths where credential syncing was deliberately skipped.
		fmt.Fprintf(os.Stderr, "Warning: failed to prepare authentication state: %v\n", syncErr)
	}

	c := exec.Command(claudePath, claudeArgs...)
	c.Env = launchEnv
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr

	return preserveExitCode(c.Run())
}
