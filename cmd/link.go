package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ramazanpolat/claude-playbooks/internal/auth"
	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// linkOpts carries CREATE PLAYBOOK … LINK's clauses. No state is shared
// between two calls.
type linkOpts struct {
	name       string
	launcher   string
	noLauncher bool
}

func doLink(o linkOpts, args []string) error {
	if err := checkLauncherConflict(o.launcher, o.noLauncher); err != nil {
		return err
	}

	target := args[0]
	abs, err := filepath.Abs(target)
	if err != nil {
		return fmt.Errorf("invalid target: %w", err)
	}

	info, err := os.Stat(abs)
	if os.IsNotExist(err) {
		return fmt.Errorf("%q not found", target)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%q is not a directory", target)
	}

	playbooksDir := config.ResolvePlaybooksDir()
	if err := os.MkdirAll(playbooksDir, 0755); err != nil {
		return err
	}

	name := o.name
	if strings.Contains(name, "/") {
		return fmt.Errorf("link name may not contain '/'")
	}
	if err := validateTopLevelName("link name", name); err != nil {
		return err
	}

	dest := filepath.Join(playbooksDir, name)
	if _, err := os.Lstat(dest); err == nil {
		return fmt.Errorf("%q already exists at %s; choose another name", name, dest)
	}

	// Serialize registration (see lockRegistry). LINK registers a
	// directory that already describes itself, so the target's manifest
	// must be there, checked under the lock.
	unlock, err := lockRegistry()
	if err != nil {
		return err
	}
	defer unlock()
	if !manifest.Exists(abs) {
		return fmt.Errorf("LINK %s: the directory has no %s; add one to the target first", target, manifest.FileName)
	}
	m, err := manifest.Read(abs)
	if err != nil {
		return err
	}
	configTarget := abs
	configDest := dest

	// Preflight launcher names BEFORE the symlink joins the registry (the
	// link name registers even under --no-alias, and the target manifest's
	// alias registers without any flag).
	effectiveAlias := o.launcher
	if effectiveAlias == "" && m != nil {
		effectiveAlias = m.Launcher
	}
	// The launcher that will actually be written uses the effective alias,
	// falling back to the link name — an unwritable name (reserved link
	// name, invalid manifest alias) must fail before dest joins the
	// registry, not as a post-link warning.
	launcherName, err := resolveLauncherName(o.noLauncher, effectiveAlias, name, "link")
	if err != nil {
		return err
	}
	if err := preflightCommandNames("", name, effectiveAlias); err != nil {
		return err
	}

	// The target manifest is SHARED state: the same external directory may
	// already be linked from other registry roots whose launchers resolve
	// through it. Any differing alias mutation — changing one, or adding
	// one where none existed — could break or reroute those registrations,
	// so it is refused.
	if o.launcher != "" && m != nil && m.Launcher != o.launcher {
		return fmt.Errorf("target's %s is shared state (launcher %q); LAUNCHER %s would change it for every registration of this target. Use the manifest's launcher or edit the target's %s directly", manifest.FileName, m.Launcher, o.launcher, manifest.FileName)
	}

	// A linked directory is the pilot's own, so nothing in it is deleted: a
	// login it carries is set aside before the sync, which would otherwise
	// copy it over the machine's login (an isolated directory keeps its own).
	credsTo, keys, backup, err := auth.SetAsideSourceLogin(configTarget, time.Now())
	if err != nil {
		return fmt.Errorf("cannot set aside the login %s carries: %w", target, err)
	}
	if credsTo != "" {
		fmt.Fprintf(os.Stderr, "Warning: ignored %s's %s: a playbook source never carries a login (moved to %s)\n", target, auth.CredentialsFileName, filepath.Base(credsTo))
	}
	if len(keys) > 0 {
		fmt.Fprintf(os.Stderr, "Warning: ignored %s's account state in %s (%s): a playbook source never carries a login (backup: %s)\n", target, auth.StateFileName, strings.Join(keys, ", "), filepath.Base(backup))
	}

	if err := auth.SyncCredentials(configTarget); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to sync credentials: %v\n", err)
	}

	if err := os.Symlink(abs, dest); err != nil {
		return fmt.Errorf("failed to create symlink: %w", err)
	}
	fmt.Printf("Linked %s -> %s\n", dest, abs)

	if o.noLauncher {
		fmt.Printf("\nRun with:\n  cpb run %s\n", name)
		return nil
	}

	// launcherName was resolved from the same chain (--alias, then the
	// target manifest's alias, then the link name) before the symlink went
	// in; the lock was held for the whole registration, so m cannot have
	// changed since.
	installLauncher(launcherName, name, configDest)
	return nil
}
