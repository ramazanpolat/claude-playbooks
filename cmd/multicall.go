package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/launcher"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

// multicallPlaybook resolves argv[0] against the live playbook registry.
// When the binary is invoked through a launcher symlink, the link's name is
// a playbook name or manifest alias; the CLI's own names never dispatch. A
// discovery failure is returned distinctly from a genuine missing name:
// one broken .playbook elsewhere in the registry must not make every
// launcher report itself as stale.
func multicallPlaybook() (string, bool, error) {
	base := filepath.Base(os.Args[0])
	if launcher.ReservedNames[base] {
		return "", false, nil
	}
	root := config.ResolvePlaybooksDir()
	shellConfig, _ := config.ResolveShellConfig()
	pbs, err := playbook.Discover(root, shellConfig)
	if err != nil {
		return "", false, fmt.Errorf("cannot resolve %q: reading the playbook registry failed: %w", base, err)
	}
	for _, pb := range pbs {
		if pb.Name == base {
			return pb.Name, true, nil
		}
	}
	for _, pb := range pbs {
		if pb.Manifest != nil && pb.Manifest.Alias == base {
			return pb.Name, true, nil
		}
	}
	return "", false, nil
}

// launcherNamesFor returns the command names that may address a playbook:
// its directory name and, when set, its manifest alias.
func launcherNamesFor(pb *playbook.Playbook) []string {
	names := []string{pb.Name}
	if pb.Manifest != nil && pb.Manifest.Alias != "" && pb.Manifest.Alias != pb.Name {
		names = append(names, pb.Manifest.Alias)
	}
	return names
}

// commandNameOwner reports which other playbook (by name or alias) already
// claims cmdName, if any. Launcher symlinks carry no ownership of their own
// — the registry is the single source of truth — so collisions are checked
// here rather than parsed out of files.
func commandNameOwner(cmdName, exceptName string) (*playbook.Playbook, error) {
	root := config.ResolvePlaybooksDir()
	shellConfig, _ := config.ResolveShellConfig()
	pbs, err := playbook.Discover(root, shellConfig)
	if err != nil {
		return nil, err
	}
	for _, pb := range pbs {
		if pb.Name == exceptName {
			continue
		}
		for _, n := range launcherNamesFor(pb) {
			if n == cmdName {
				return pb, nil
			}
		}
	}
	return nil, nil
}

// preflightCommandNames errors when any candidate name already addresses
// another playbook. Callers run this BEFORE registering a playbook: once the
// directory exists it joins the registry, and since dispatch resolves
// directory names ahead of aliases, a clashing registration would silently
// re-route an existing command.
func preflightCommandNames(exceptName string, names ...string) error {
	for _, n := range names {
		if n == "" {
			continue
		}
		owner, err := commandNameOwner(n, exceptName)
		if err != nil {
			// Dispatch relies on the same discovery call: registering a
			// name it cannot verify would advertise a command that cannot
			// resolve.
			return fmt.Errorf("cannot verify command name %q: %w", n, err)
		}
		if owner != nil {
			return fmt.Errorf("command name %q already addresses playbook %q. Pick another name or alias", n, owner.Name)
		}
	}
	return nil
}

// invokedViaLauncher reports whether this process was started through a
// launcher symlink (argv[0] reaches a symlink resolving to this binary).
// Needed to fail loudly on STALE launchers: a link whose playbook no longer
// exists must not fall through to the CLI overview and exit 0 — scripts
// would mistake that for a successful playbook run.
func invokedViaLauncher() bool {
	argv0 := os.Args[0]
	var path string
	if strings.ContainsRune(argv0, os.PathSeparator) {
		path = argv0
	} else if lp, err := exec.LookPath(argv0); err == nil {
		path = lp
	} else {
		return false
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return false
	}
	bin, err := launcher.BinPath()
	if err != nil {
		return false
	}
	resolved, err := filepath.EvalSymlinks(path)
	return err == nil && resolved == bin
}

// lockRegistry takes an exclusive advisory lock serializing
// preflight-through-registration across concurrent processes: without it,
// two creates can both pass the ownership check before either playbook is
// visible and register duplicate owners for one command name. The lock is
// machine-user-global and lives OUTSIDE every registry root (user cache
// dir, tmp as fallback): commands under different --playbooks-dir roots
// contend for shared resources (a linked target's external manifest, the
// launcher directory), so per-root locks would not exclude each other —
// and a lock inside any root would mutate registries the command was told
// to stay away from, or fail when that root is read-only. flock releases
// automatically when the process dies, so a crashed holder never wedges
// the registry.
func lockRegistry() (unlock func(), err error) {
	var path string
	if cache, cerr := os.UserCacheDir(); cerr == nil {
		dir := filepath.Join(cache, "claude-playbook")
		if merr := os.MkdirAll(dir, 0o755); merr == nil {
			path = filepath.Join(dir, "registry.lock")
		}
	}
	if path == "" {
		path = filepath.Join(os.TempDir(), fmt.Sprintf("claude-playbook-registry-%d.lock", os.Getuid()))
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("cannot lock registry: %w", err)
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
