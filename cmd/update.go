package cmd

import (
	"syscall"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

// defaultPreserved are the per-install files that must survive an update even
// when the source ships its own copy.
//
// settings.json is the important one: playbooks track it deliberately, so a
// generic installer lands a fully wired install (hooks, statusLine). But the
// live file is the install's own configuration -- API routing, model pins,
// permissions -- and overwriting it silently destroys the pilot's setup. New
// stock settings ship alongside it in settings.json.template to be merged by
// hand. Playbooks name additional files via [update] preserve in .playbook.
var defaultPreserved = []string{
	"settings.json",
	"settings.local.json",
	".credentials.json",
	".claude.json",
}

var updateCmd = &cobra.Command{
	Use:                "update [name]",
	Short:              "Self-update the tool, or update a playbook from its source",
	DisableFlagParsing: true,
	ValidArgsFunction:  autocompletePlaybookNames,
	RunE:               runUpdate,
}

func runUpdate(cmd *cobra.Command, args []string) error {
	rest, err := takePlaybooksDirArg(args)
	if err != nil {
		return err
	}
	// --force is self-update only. --check means the same thing on both paths
	// (report, do not install), so it is also accepted after a playbook name.
	var force, checkOnly bool
consume:
	for len(rest) > 0 {
		switch rest[0] {
		case "--force", "-f":
			force = true
			rest = rest[1:]
		case "--check":
			checkOnly = true
			rest = rest[1:]
		default:
			break consume
		}
	}
	if restRequestsHelp(rest) {
		printUpdateHelp()
		return nil
	}

	if len(rest) == 0 {
		return runSelfUpdate(force, checkOnly)
	}

	name := rest[0]
	for _, arg := range rest[1:] {
		switch arg {
		case "--check":
			checkOnly = true
		case "--help", "-h":
			printUpdateHelp()
			return nil
		default:
			return fmt.Errorf("unexpected argument %q; `update <name>` accepts only --check", arg)
		}
	}
	return runPlaybookUpdate(name, checkOnly)
}

func printUpdateHelp() {
	fmt.Println("Usage: claude-playbook update [name]")
	fmt.Println()
	fmt.Println("Without <name>: self-update the claude-playbook binary to the latest release.")
	fmt.Println("  --check    report the latest version without installing it")
	fmt.Println("  --force    reinstall even if already on the latest version")
	fmt.Println()
	fmt.Println("With <name>: update that playbook from its [source] metadata. Local files")
	fmt.Println("(settings.json and anything under [update] preserve) survive, and the")
	fmt.Println("playbook's migrations/apply.sh runs afterward.")
	fmt.Println("  --check    report the available version without installing it")
}

func runPlaybookUpdate(name string, checkOnly bool) error {
	playbooksDir := config.ResolvePlaybooksDir()

	pb, err := playbook.Require(playbooksDir, name)
	if err != nil {
		return err
	}
	if pb.Manifest == nil || pb.Manifest.Source == nil || pb.Manifest.Source.Repository == "" {
		return fmt.Errorf("%q has no [source] metadata in .playbook; nothing to update from", name)
	}

	root := pb.RootPath
	if root == "" {
		root = pb.Path
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%q is linked; native update is disabled to avoid replacing its external source", name)
	}
	rootAbs, _ := filepath.Abs(root)
	pathAbs, _ := filepath.Abs(pb.Path)
	if rootAbs != pathAbs {
		return fmt.Errorf("%q uses manifest subdir %q; native update requires a flat playbook", name, pb.Manifest.Subdir)
	}

	// Validate the preserve list before touching anything: a manifest that
	// names an escaping path must fail loudly, not halfway through the swap.
	preserve, err := preservePaths(root, pb.Manifest)
	if err != nil {
		return err
	}

	work, cleanup, err := stageSource(pb.Manifest.Source.Repository, isGitURL(pb.Manifest.Source.Repository), pb.Manifest.Source.Branch, pb.Manifest.Source.Subdir)
	if err != nil {
		return fmt.Errorf("failed to fetch latest source: %w", err)
	}
	defer cleanup()

	stagedManifest, err := manifest.Read(work)
	if err != nil {
		return fmt.Errorf("staged source has an invalid manifest: %w", err)
	}
	fromVersion := pb.Manifest.Version
	toVersion := ""
	if stagedManifest != nil {
		toVersion = stagedManifest.Version
	}
	if toVersion == "" {
		toVersion = readVersionFile(work)
	}

	if checkOnly {
		fmt.Printf("%s\n", name)
		fmt.Printf("  installed: %s\n", displayVersion(fromVersion))
		fmt.Printf("  available: %s\n", displayVersion(toVersion))
		if fromVersion != "" && fromVersion == toVersion {
			fmt.Println("  up to date")
		}
		return nil
	}

	// Staging ran unlocked (it may fetch from the network); the overlay must
	// not. Take the registry lock and RE-READ the live manifest: a concurrent
	// `alias` (or other manifest mutation) that landed while the source was
	// staging would otherwise be resurrected from the stale pre-staging
	// snapshot, leaving launchers and manifest disagreeing.
	unlock, lerr := lockRegistry()
	if lerr != nil {
		return lerr
	}
	defer unlock()
	liveManifest, err := manifest.Read(root)
	if err != nil {
		return fmt.Errorf("cannot re-read manifest before activation: %w", err)
	}
	// Bind activation to the exact installation we inspected: the DIRECTORY
	// must be the same filesystem object as before staging (a delete +
	// reinstall from the very same repository passes any manifest comparison),
	// and every source field must match. Anything else means the playbook was
	// deleted, re-created, or re-sourced while staging ran.
	liveInfo, lierr := os.Lstat(root)
	if lierr != nil || !os.SameFile(rootInfo, liveInfo) ||
		liveManifest == nil || liveManifest.Source == nil ||
		*liveManifest.Source != *pb.Manifest.Source {
		return fmt.Errorf("playbook %q changed while the update was staging (deleted, re-created, or re-sourced); nothing activated -- re-run update", name)
	}

	// The manifest that goes live is assembled in the STAGED tree before the
	// overlay, never corrected in place afterwards: the overlay copies the
	// staged .playbook over the live one, launches do not take the registry
	// lock, and a source-shipped [env] block that was live even briefly --
	// or permanently, had a later rewrite failed -- could redirect the
	// install's endpoint or strip its authentication. Install-local fields
	// (alias, isolation, source, [env]) come from the live manifest; the
	// install's name is its directory name (this also heals installs whose
	// manifest predates name rewriting).
	updated := stagedManifest
	if updated == nil {
		updated = &manifest.Manifest{}
		*updated = *liveManifest
	} else {
		copied := *updated
		updated = &copied
		updated.Alias = liveManifest.Alias
		updated.IsolateAuth = liveManifest.IsolateAuth
		updated.Source = liveManifest.Source
		updated.Env = liveManifest.Env
	}
	updated.Name = filepath.Base(root)
	updated.Subdir = ""
	if err := manifest.Write(work, updated); err != nil {
		return fmt.Errorf("failed to prepare updated manifest: %w", err)
	}
	// Manifest.Write's never-loosen rule looked at the STAGED file's mode;
	// the overlay is about to replace the live file with it, so the staged
	// copy takes the live file's mode masked by its own -- never looser
	// than either, whatever the pilot had chosen.
	if live, err := os.Stat(filepath.Join(root, manifest.FileName)); err == nil {
		staged := filepath.Join(work, manifest.FileName)
		if info, err := os.Stat(staged); err == nil {
			if err := os.Chmod(staged, live.Mode().Perm()&info.Mode().Perm()); err != nil {
				return fmt.Errorf("failed to prepare updated manifest: %w", err)
			}
		}
	}

	fmt.Printf("Updating %s from %s...\n", name, pb.Manifest.Source.Repository)
	backupPath, err := overlaySource(work, root, preserve)
	if err != nil {
		return err
	}

	fmt.Printf("Updated %q to %s. Replaced files backed up to %s.\n", name, displayVersion(toVersion), backupPath)

	if err := runMigrations(name, root, fromVersion, toVersion); err != nil {
		return fmt.Errorf("%q is at code version %s but migrations failed: %w", name, displayVersion(toVersion), err)
	}
	return nil
}

// overlaySource replaces root's copy of every top-level entry the staged
// source provides, then restores the preserved local files over the top.
//
// The overlay is applied IN PLACE rather than staged into a candidate
// directory and swapped: an install is a live CLAUDE_CONFIG_DIR whose runtime
// state (data/, projects/, sessions/, history.jsonl) is written continuously
// by any running session. Copying the whole install aside and renaming it back
// silently discards every such write that lands while the copy is in flight.
// Overlaying in place never reads or writes those paths at all -- only entries
// the source itself ships are touched, and those are moved into the backup
// first so a failure mid-overlay can be rolled back.
func overlaySource(work, root string, preserve []string) (string, error) {
	entries, err := os.ReadDir(work)
	if err != nil {
		return "", err
	}

	parent := filepath.Dir(root)
	backupPath := filepath.Join(parent, fmt.Sprintf(".%s.bak.%s", filepath.Base(root), time.Now().UTC().Format("20060102T150405.000000000")))
	if err := os.MkdirAll(backupPath, 0700); err != nil {
		return "", err
	}

	// moved: top-level entries that existed live and went into the backup.
	// introduced: top-level entries the source ships that the install did
	// NOT have. Both matter: rollback must remove what was introduced as
	// well as restore what was moved, and preservation must restore the
	// previous ABSENCE of a protected file the source ships (a source must
	// never inject credentials or settings the install did not own).
	moved := map[string]bool{}
	introduced := map[string]bool{}
	rollback := func() error {
		var failed []string
		for name := range introduced {
			if err := removeAny(filepath.Join(root, name)); err != nil {
				failed = append(failed, name)
			}
		}
		for name := range moved {
			if err := removeAny(filepath.Join(root, name)); err != nil {
				failed = append(failed, name)
				continue
			}
			if err := os.Rename(filepath.Join(backupPath, name), filepath.Join(root, name)); err != nil {
				failed = append(failed, name)
			}
		}
		if len(failed) > 0 {
			// Keep the backup: it is the only copy of what could not be
			// put back, and a silent RemoveAll here would destroy it.
			sort.Strings(failed)
			return fmt.Errorf("rollback incomplete for %s; backup kept at %s", strings.Join(failed, ", "), backupPath)
		}
		return os.RemoveAll(backupPath)
	}
	fail := func(err error) (string, error) {
		if rerr := rollback(); rerr != nil {
			return "", fmt.Errorf("%w (and %v)", err, rerr)
		}
		return "", err
	}

	for _, e := range entries {
		name := e.Name()
		live := filepath.Join(root, name)
		if _, err := os.Lstat(live); err != nil {
			if os.IsNotExist(err) {
				introduced[name] = true
				continue
			}
			return fail(err)
		}
		if err := os.Rename(live, filepath.Join(backupPath, name)); err != nil {
			return fail(fmt.Errorf("failed to back up %s: %w", name, err))
		}
		moved[name] = true
	}

	if err := overlayDir(work, root); err != nil {
		return fail(fmt.Errorf("failed to apply update: %w", err))
	}

	for _, rel := range preserve {
		if err := restoreLocalEntry(backupPath, root, rel, moved, introduced); err != nil {
			return fail(fmt.Errorf("failed to preserve %s: %w", rel, err))
		}
	}
	return backupPath, nil
}

// absentPath reports whether a stat error means "nothing at this path":
// plain not-found, or a regular file where the path expects a directory
// (ENOTDIR -- the install had a FILE named config and the source introduced
// a config/ directory, so backup/config/private.json cannot exist).
func absentPath(err error) bool {
	return os.IsNotExist(err) || errors.Is(err, syscall.ENOTDIR)
}

// physicallyWithin reports whether the DIRECTORY holding p -- its parent, or
// when that does not exist yet, the nearest existing ancestor -- resolves to
// tree or below it with symlinks evaluated. tree is the top-level entry the
// preserved path belongs to (root/<top>), not the whole install: a symlinked
// ancestor may point outside the install, or -- just as bad -- into a sibling
// entry the overlay never touched and never backed up (`config -> data`), so
// "somewhere under root" is not good enough.
//
// The final component is deliberately NOT followed: a preserved entry that is
// itself a symlink (the ordinary `.credentials.json -> ~/.claude/...`) points
// outside by design and is recreated with Readlink, never read through. Only
// a symlinked ancestor can make a write land elsewhere.
func physicallyWithin(tree, p string) (bool, error) {
	// tree itself may be the symlink (top-level `config -> data`): resolve
	// its PARENT and require the resolved tree to be exactly parent/<top>.
	treeParentReal, err := filepath.EvalSymlinks(filepath.Dir(tree))
	if err != nil {
		return false, err
	}
	treeReal := filepath.Join(treeParentReal, filepath.Base(tree))
	if info, err := os.Lstat(tree); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return false, nil // the entry itself is an alias; nothing below it is ours
	}
	probe := filepath.Dir(p)
	for {
		if real, err := filepath.EvalSymlinks(probe); err == nil {
			return pathWithin(treeReal, real), nil
		} else if !absentPath(err) {
			return false, err
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return false, nil
		}
		probe = parent
	}
}

// touchedEntry reports whether the top-level entry holding a preserved path
// was moved or introduced by the overlay, by filesystem identity rather than
// by name: on a case-insensitive filesystem the source's SETTINGS.JSON and
// the preserved settings.json are one file, and a map lookup on the spelling
// would let the upstream copy stay active.
func touchedEntry(root, top string, sets ...map[string]bool) bool {
	for _, set := range sets {
		if set[top] {
			return true
		}
	}
	ti, err := os.Lstat(filepath.Join(root, top))
	if err != nil {
		return false
	}
	for _, set := range sets {
		for name := range set {
			if name == top {
				continue
			}
			if ni, err := os.Lstat(filepath.Join(root, name)); err == nil && os.SameFile(ti, ni) {
				return true
			}
		}
	}
	return false
}

// preservePaths returns the local files that must survive the overlay: the
// built-in set plus whatever the playbook declares under [update] preserve.
func preservePaths(root string, m *manifest.Manifest) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	add := func(rel string) {
		clean := path.Clean(filepath.ToSlash(rel))
		if seen[clean] {
			return
		}
		seen[clean] = true
		out = append(out, clean)
	}
	for _, name := range defaultPreserved {
		add(name)
	}
	if m != nil && m.Update != nil {
		for _, rel := range m.Update.Preserve {
			if err := manifest.ValidateRelativePath(filepath.Join(root, manifest.FileName), "update.preserve", rel); err != nil {
				return nil, err
			}
			add(rel)
		}
	}
	// A descendant of a preserved ancestor is covered by it: restoring the
	// ancestor restores (or removes) the descendant too, and a second pass
	// would find the ancestor already gone. Drop such entries.
	var collapsed []string
	for _, rel := range out {
		covered := false
		for _, other := range out {
			if other != rel && strings.HasPrefix(rel, other+"/") {
				covered = true
				break
			}
		}
		if !covered {
			collapsed = append(collapsed, rel)
		}
	}
	return collapsed, nil
}

// runMigrations hands off to the playbook's own migration runner. Migrations
// are data transforms the CLI cannot know the shape of; the convention is
// migrations/apply.sh <from-version> <to-version> <install-dir>, invoked after
// the new code is in place. Runners are expected to be idempotent -- the CLI
// re-invokes on every update and does not track which ones have run.
func runMigrations(name, root, from, to string) error {
	script := filepath.Join(root, "migrations", "apply.sh")
	info, err := os.Stat(script)
	if err != nil {
		return nil // no runner: this playbook has no migrations
	}
	if info.IsDir() {
		return nil
	}
	if info.Mode()&0111 == 0 {
		fmt.Fprintf(os.Stderr, "Warning: %s is not executable; skipping migrations\n", script)
		return nil
	}
	if from == "" || to == "" {
		fmt.Fprintln(os.Stderr, "Warning: .playbook carries no version on one side of the update; skipping migrations")
		return nil
	}

	fmt.Printf("Running migrations %s -> %s...\n", from, to)
	c := exec.Command(script, from, to, root)
	c.Dir = root
	c.Env = append(os.Environ(),
		"CLAUDE_CONFIG_DIR="+root,
		"CLAUDE_PLAYBOOK_TARGET="+name,
		"CLAUDE_PLAYBOOK_PATH="+root,
	)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

func readVersionFile(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "VERSION"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func displayVersion(v string) string {
	if v == "" {
		return "unknown"
	}
	return v
}

// restoreLocalEntry copies rel back from the backup over the freshly overlaid
// copy. Entries whose top-level component the overlay never touched are left
// alone; an entry the source ships but the install did not have -- whether
// under a moved top-level entry or a newly introduced one -- is removed, so
// upstream never injects a credentials or state file the pilot did not own.
//
// Both the backup source and the live destination are checked for PHYSICAL
// containment before anything is removed or written: the incoming tree may
// have turned a directory on the preserved path into a symlink pointing
// outside the install, and the copy deliberately preserves such symlinks.
func restoreLocalEntry(backup, root, rel string, moved, introduced map[string]bool) error {
	top := rel
	if i := strings.IndexByte(rel, '/'); i >= 0 {
		top = rel[:i]
	}
	if !touchedEntry(root, top, moved, introduced) {
		return nil // the overlay never touched this entry
	}

	src := filepath.Join(backup, filepath.FromSlash(rel))
	dst := filepath.Join(root, filepath.FromSlash(rel))
	info, err := os.Lstat(src)
	if absentPath(err) {
		// Restore the previous ABSENCE. If the top-level entry is already
		// gone (an earlier preserved path removed it) there is nothing left
		// to check or remove.
		if _, terr := os.Lstat(filepath.Join(root, top)); os.IsNotExist(terr) {
			return nil
		}
		if top != rel {
			if within, err := physicallyWithin(filepath.Join(root, top), dst); err != nil {
				return err
			} else if !within {
				return fmt.Errorf("%s resolves outside %s/ after the update (a symlinked ancestor); refusing to restore through it", rel, top)
			}
		}
		return removeAny(dst)
	}
	if err != nil {
		return err
	}
	if top != rel {
		// A nested path: every ancestor between the entry and the file must
		// stay inside that entry, in the live tree and in the backup. A
		// top-level entry that no longer exists is fine: MkdirAll below
		// recreates plain directories, and nothing can be aliased through
		// a tree that is not there.
		if _, terr := os.Lstat(filepath.Join(root, top)); terr == nil {
			if within, err := physicallyWithin(filepath.Join(root, top), dst); err != nil {
				return err
			} else if !within {
				return fmt.Errorf("%s resolves outside %s/ after the update (a symlinked ancestor); refusing to restore through it", rel, top)
			}
		}
	}
	if top != rel {
		if within, err := physicallyWithin(filepath.Join(backup, top), src); err != nil {
			return err
		} else if !within {
			return fmt.Errorf("%s resolves outside the backup's %s/; refusing to restore from it", rel, top)
		}
	}
	if err := removeAny(dst); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(target, dst)
	}
	if info.IsDir() {
		return copyDir(src, dst)
	}
	return copyFile(src, dst, info.Mode())
}
