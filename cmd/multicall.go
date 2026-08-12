package cmd

import (
	"os"
	"path/filepath"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/launcher"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

// multicallPlaybook resolves argv[0] against the live playbook registry.
// When the binary is invoked through a launcher symlink, the link's name is
// a playbook name or manifest alias; the CLI's own names never dispatch,
// and an unresolvable name falls through to the normal CLI so a custom
// binary install name keeps working.
func multicallPlaybook() (string, bool) {
	base := filepath.Base(os.Args[0])
	if launcher.ReservedNames[base] {
		return "", false
	}
	root := config.ResolvePlaybooksDir()
	shellConfig, _ := config.ResolveShellConfig()
	pbs, err := playbook.Discover(root, shellConfig)
	if err != nil {
		return "", false
	}
	for _, pb := range pbs {
		if pb.Name == base {
			return pb.Name, true
		}
	}
	for _, pb := range pbs {
		if pb.Manifest != nil && pb.Manifest.Alias == base {
			return pb.Name, true
		}
	}
	return "", false
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
