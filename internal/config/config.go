package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var (
	PlaybooksDir string
	LauncherDir  string
)

// ResolveLauncherDir returns the directory launcher commands are written to:
// the --launcher-dir flag, then $CLAUDE_LAUNCHER_DIR, then the directory of
// the running binary — which is on PATH by construction whenever the tool
// itself was invoked by name. A system-installed binary (e.g. in
// /usr/local/bin) sits in a directory an unprivileged user cannot write, so
// an unwritable binary dir falls back to ~/.local/bin.
func ResolveLauncherDir() (string, error) {
	if LauncherDir != "" {
		return LauncherDir, nil
	}
	if v := os.Getenv("CLAUDE_LAUNCHER_DIR"); v != "" {
		return v, nil
	}
	// Prefer the directory of the command as INVOKED (argv[0] resolved via
	// PATH, without following the final symlink): os.Executable resolves
	// through symlinks, and for installs managed via a PATH symlink whose
	// target lives elsewhere (package/version managers), the target dir is
	// writable but not on PATH — launchers written there would be
	// unreachable.
	dir := invokedBinDir()
	if dir == "" {
		exe, err := os.Executable()
		if err != nil {
			return "", err
		}
		dir = filepath.Dir(exe)
	}
	if dirWritable(dir) {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	// Resolution only computes the path — creation happens on the write
	// path (launcher.Write), so read-only callers (list, --dry-run, a
	// cancelled uninstall prompt) never mutate the filesystem.
	return filepath.Join(home, ".local", "bin"), nil
}

// dirWritable reports whether the current user can create files in dir.
// Permission-bit inspection lies under ACLs and containers, so probe by
// creating (and removing) a temp file.
func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".cpb-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

// ConfigDirOverrideEnv is the variable a caller sets to supply the config
// directory a launch binds, in place of the playbook's install directory.
//
// It exists because a playbook directory serves two roles at once: it is the
// playbook's CONTENT (CLAUDE.md, settings.json, hooks/, skills/) and also the
// sink for Claude Code's STATE (.claude.json, sessions/, projects/, cache/).
// A consumer that wants several sessions to share one playbook's content
// while each keeps its own memory cannot express that with one directory, so
// it provisions its own and names it here.
//
// A bare inherited CLAUDE_CONFIG_DIR is deliberately NOT honoured: a variable
// left exported in a shell would silently redirect every launcher on the
// machine, running a playbook's content against an unrelated directory. The
// opt-in has to be its own name, set on purpose. Keeping that name separate
// from CLAUDE_CONFIG_DIR also keeps the two halves of the handoff distinct —
// this variable is what the caller REQUESTS, CLAUDE_CONFIG_DIR is what the
// child RECEIVES.
//
// The variable is consumed: it is stripped from the launched process's
// environment (see auth.PrepareLaunchEnvWith), so it cannot redirect a
// further launch made from inside the session.
const ConfigDirOverrideEnv = "CLAUDE_CONFIG_DIR_OVERRIDE"

// ResolveConfigDirOverride returns the caller-supplied config directory and
// whether one was requested. Unset or empty means no override, which is the
// default and leaves every existing launch unchanged.
//
// The path is expanded (a leading ~) and must be absolute: CLAUDE_CONFIG_DIR
// is resolved by the child against its own working directory, so a relative
// value would name different directories depending on where the caller
// happened to stand. Resolving it here against the current directory would
// hide that, so it is refused instead — the same rule the manifest applies to
// sandbox.workdir and sandbox.mounts.
//
// Existence is NOT checked and the directory is NOT created. Provisioning it
// (and any playbook content it needs to expose) belongs to the caller; cpb's
// job is to bind what it is given, not to have an opinion about what is
// inside.
func ResolveConfigDirOverride() (string, bool, error) {
	v := os.Getenv(ConfigDirOverrideEnv)
	if v == "" {
		return "", false, nil
	}
	if strings.HasPrefix(v, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false, fmt.Errorf("%s %q cannot be expanded: %w", ConfigDirOverrideEnv, v, err)
		}
		if v == "~" {
			v = home
		} else if strings.HasPrefix(v, "~/") {
			v = filepath.Join(home, v[2:])
		} else {
			// ~user is not expanded by anything else in the tool either.
			return "", false, fmt.Errorf("%s %q must be an absolute or ~-prefixed path", ConfigDirOverrideEnv, v)
		}
	}
	if !filepath.IsAbs(v) {
		return "", false, fmt.Errorf("%s %q must be an absolute or ~-prefixed path", ConfigDirOverrideEnv, v)
	}
	return filepath.Clean(v), true, nil
}

// WithoutConfigDirOverride removes CLAUDE_CONFIG_DIR_OVERRIDE from an
// environment slice. Every subprocess the tool starts with an explicitly
// chosen CLAUDE_CONFIG_DIR uses it: that choice is authoritative, and leaving
// a request for a different directory beside it is incoherent. It also stops
// the variable reaching anything the subprocess itself launches -- a migration
// runner that calls `claude-playbook run` would otherwise be redirected.
//
// Launches go through auth.PrepareLaunchEnv, which consumes the variable as
// part of binding CLAUDE_CONFIG_DIR; this is for the paths that build an
// environment directly.
func WithoutConfigDirOverride(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, ConfigDirOverrideEnv+"=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func ResolvePlaybooksDir() string {
	if PlaybooksDir != "" {
		return PlaybooksDir
	}
	if v := os.Getenv("CLAUDE_PLAYBOOKS_DIR"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude-playbooks")
}

// invokedBinDir returns the directory of the command as the user reached it:
// the literal argv[0] directory, or its PATH entry — deliberately without
// resolving the final symlink. Empty when argv[0] cannot be located.
func invokedBinDir() string {
	argv0 := os.Args[0]
	var p string
	if strings.ContainsRune(argv0, os.PathSeparator) {
		p = argv0
	} else if lp, err := exec.LookPath(argv0); err == nil {
		p = lp
	} else {
		return ""
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	return filepath.Dir(p)
}
