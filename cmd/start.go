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
)

var startCmd = &cobra.Command{
	Use:                "start <path> [claude-flags...]",
	Short:              "Start an ad-hoc Claude Code session at a directory",
	DisableFlagParsing: true,
	RunE:               runStart,
}

// noteStartIgnoresOverride reports that start ignored a caller-supplied config
// directory. start names its own on the command line, and the command line
// outranks the environment.
//
// It has to be SAID: the variable is consumed either way, so from outside the
// process "ignored" and "honoured" look identical, and a caller could believe
// its directory was used. Silent when the override names the same directory
// that is being used, since then nothing was ignored -- a notice that fires
// when nothing happened stops being read. A malformed value is reported too,
// as a warning rather than a refusal: start's own path is valid and the
// session runs.
//
// dirShown is the directory start will actually use, spelled as the operator
// will recognise it -- resolved locally, or as typed for a remote start.
func noteStartIgnoresOverride(dirShown string) {
	overrideDir, override, err := config.ResolveConfigDirOverride()
	switch {
	case err != nil:
		fmt.Fprintf(os.Stderr, "Warning: %v (start uses the directory you named, %s)\n", err, dirShown)
	case override && overrideDir != dirShown:
		fmt.Fprintf(os.Stderr, "%s ignored: start uses the directory you named, %s\n", config.ConfigDirOverrideEnv, dirShown)
	}
}

func runStart(cmd *cobra.Command, args []string) error {
	// start addresses a path, not a registry name, but the playbooks-dir
	// value still names the root whose .env-profiles/ the path's manifest
	// may reference, so it is applied to this process exactly as run does
	// (and kept out of the args forwarded to claude).
	original := args
	args, err := takePlaybooksDirArg(args)
	if err != nil {
		return err
	}

	// Wrapper flags (--delete and the launch flags) are recognised only as
	// a leading run before the path and again immediately after it. A
	// --delete anywhere else -- after another claude argument, as a claude
	// flag's value (`-p --delete`), or after "--" -- is claude's, never a
	// licence to remove the directory. The previous scan took ANY literal
	// --delete in the argument list.
	var deleteAfter bool
	var sopts sandboxOpts
	wrapper := map[string]*bool{"--delete": &deleteAfter}
	rest, tokens, err := takeRunFlags(args, &sopts, wrapper)
	if err != nil {
		return err
	}
	// A --help at the PATH position prints usage, whether or not launch
	// flags preceded it; after the path, the flag is forwarded to claude.
	if restRequestsHelp(rest) {
		fmt.Println("Usage: claude-playbook start " + runFlagsUsage + " [--delete] <path> [claude-flags...]")
		fmt.Println()
		fmt.Println("Starts an ad-hoc Claude Code session at the given directory.")
		fmt.Println("Creates the directory if it does not exist.")
		fmt.Println("Wrapper flags go before the path or immediately after it; the first other")
		fmt.Println("argument (or --) ends them and everything from there on is claude's.")
		fmt.Println("  --delete             delete the directory when the session ends")
		fmt.Println("  --env-profile NAME   layer an existing env profile, this launch only")
		fmt.Println("  --env KEY=VALUE      set one variable")
		fmt.Println("  --unset KEY          remove one variable")
		fmt.Println("  --env-file PATH      layer a dotenv-style file of KEY=VALUE lines")
		fmt.Println("Sandbox flags run the session inside a sandbox (backend sbx, Docker Sandboxes):")
		fmt.Println("  --sandbox[=BACKEND]  launch in the directory's sandbox cpbstart-<dir> (created on first use); --sbx is a synonym")
		fmt.Println("  --no-sandbox         launch on the host although the directory's manifest says [sandbox] always = true")
		fmt.Println("  --sandbox-host U@H   run the sandboxed start on that machine over ssh (the path is a path there)")
		fmt.Println("  --sandbox-fresh      remove and recreate that sandbox first")
		fmt.Println("  --clone              at creation, work on a private clone of the working directory's repo")
		fmt.Println("  --workdir PATH       working directory to mount and enter (default: current directory)")
		fmt.Println("  --mount PATH[:ro]    extra host path to mount (repeatable)")
		fmt.Println("With --sandbox, --delete also removes the sandbox when the session ends.")
		return nil
	}

	if len(rest) == 0 || rest[0] == "--" {
		// "--" is never a path: taking it as one would resume wrapper
		// parsing right after it, and `start -- --delete` would remove a
		// directory literally named "--".
		return fmt.Errorf("path required\nUsage: claude-playbook start " + runFlagsUsage + " [--delete] <path> [claude-flags...]")
	}

	path := rest[0]
	claudeArgs, more, err := takeRunFlags(rest[1:], &sopts, wrapper)
	if err != nil {
		return err
	}
	tokens = append(tokens, more...)

	// An explicit --sandbox-host (before or after the path) runs the whole
	// start on that host; the path is a path there, and no launch flag is
	// evaluated here.
	if sopts.host != "" {
		if sopts.disabled {
			return fmt.Errorf("--sandbox-host and --no-sandbox together: pick one")
		}
		var wrapper []string
		if deleteAfter {
			wrapper = []string{"--delete"}
		}
		// The whole start runs on the remote host, which never receives this
		// variable, so the notice has to happen here or nowhere. The path is
		// named as given: it is a path THERE, and resolving it against the
		// local working directory would print a directory that is not the one
		// being used.
		noteStartIgnoresOverride(path)
		return forwardToSandboxHost(sopts.host, "start", original, &sopts, tokens, wrapper, path, claudeArgs)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("invalid path %q: %w", path, err)
	}

	noteStartIgnoresOverride(absPath)

	if info, err := os.Stat(absPath); err == nil && !info.IsDir() {
		return fmt.Errorf("%q is not a directory", absPath)
	} else if os.IsNotExist(err) {
		if mkErr := os.MkdirAll(absPath, 0755); mkErr != nil {
			return fmt.Errorf("could not create %q: %w", absPath, mkErr)
		}
	}

	// The directory's own manifest supplies [sandbox] (always and the
	// defaults); an unreadable one is reported by the launch preparation.
	var sbm *manifest.Sandbox
	if m, err := manifest.Read(absPath); err == nil && m != nil {
		sbm = m.Sandbox
	}
	sandboxed, backend, err := resolveSandbox(sbm, &sopts, "directory "+absPath)
	if err != nil {
		return err
	}
	if sandboxed {
		// The directory's manifest may name the host: the whole start runs
		// there, the path being a path there, and nothing local follows.
		if host := sandboxHost(sbm, &sopts); host != "" {
			var wrapper []string
			if deleteAfter {
				wrapper = []string{"--delete"}
			}
			return forwardToSandboxHost(host, "start", original, &sopts, tokens, wrapper, path, claudeArgs)
		}
		// Refused here, before anything else: with --delete, a refusal
		// must never be followed by the cleanup below.
		if auth.IsGlobalConfigDir(absPath) {
			return fmt.Errorf("%s is the machine's Claude config directory: a sandbox would mount the machine login. Sandbox a playbook or another directory", absPath)
		}
		// Only now is the start known to be local: evaluate the launch
		// flags (env files are read here and nowhere earlier).
		layers, err := launchLayers(tokens)
		if err != nil {
			return err
		}
		name := startSandboxName(absPath)
		started, runErr := runSandboxed(sandboxTarget{
			label: "directory " + absPath, name: name,
			configPath: absPath, rootPath: absPath, manifest: sbm, backend: backend,
		}, layers, claudeArgs, sopts)
		// Cleanup only after a session actually ran: a launch refused
		// before attaching (a bad mount, sbx missing) leaves the directory
		// and any sandbox exactly as they were.
		if deleteAfter && started {
			removeSandbox(backend, name)
			if rmErr := os.RemoveAll(absPath); rmErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not delete %s: %v\n", absPath, rmErr)
			}
		}
		return runErr
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

	// The sixth way to get the flags wrong: a profile that does not resolve.
	// Its refusal lives inside PrepareLaunchEnv, which cannot move above the
	// lookup because it mutates credentials -- so resolve the block here,
	// purely, and let the input name itself like the other five. EffectiveBlock
	// is the same resolution PrepareLaunchEnv performs, reading only; doing it
	// twice costs a few file reads and cannot diverge, being one function.
	if _, perr := auth.EffectiveBlock(absPath, layers); errors.Is(perr, envprofile.ErrProfile) {
		return perr
	}

	claudePath, err := exec.LookPath("claude")
	if err != nil {
		return fmt.Errorf("'claude' command not found. Install Claude Code first: https://claude.ai/download")
	}
	launchEnv, syncErr := auth.PrepareLaunchEnvWith(absPath, layers)
	if errors.Is(syncErr, envprofile.ErrProfile) {
		// Missing, unreadable, or invalid profile: launching with a silently
		// dropped layer could send traffic to the wrong endpoint with the
		// wrong credentials -- refuse, do not warn.
		return syncErr
	}
	if syncErr != nil {
		// Neutral wording -- see the matching comment in cmd/run.go.
		fmt.Fprintf(os.Stderr, "Warning: failed to prepare authentication state: %v\n", syncErr)
	}

	c := exec.Command(claudePath, claudeArgs...)
	c.Env = launchEnv
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr

	runErr := c.Run()

	if deleteAfter {
		if rmErr := os.RemoveAll(absPath); rmErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not delete %s: %v\n", absPath, rmErr)
		}
	}

	return preserveExitCode(runErr)
}
