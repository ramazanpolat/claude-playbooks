package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
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

var (
	updateDryRun        bool
	updateYes           bool
	updateJSON          bool
	updateSHA256        string
	updateTrustEndpoint []string
	updateTrustSecret   []string
)

var updateCmd = &cobra.Command{
	Use:   "update <name>",
	Short: "Update a playbook from its source, or a played one from its recipe",
	Long: `Update a playbook from where it came from.

A playbook created FROM a source ([source] in its .playbook) is fetched
again and overlaid. Local files (settings.json and anything under
[update] preserve) survive. When the source declares a migrate step
([update] migrate), it is shown with its sha256 and runs after the new
files are in place: on a terminal you are asked, otherwise --yes runs it.

A playbook kept by cpb play ([play]) fetches its recorded recipe again:
the same bytes change nothing; others show the diff and the full preview,
and need your confirmation (--trust-endpoint and --trust-secret without a
terminal).

cpb self-update updates cpb itself.`,
	Args: func(cmd *cobra.Command, args []string) error {
		if len(args) != 1 {
			return errors.New("update takes one playbook name: cpb update <name> (cpb self-update updates cpb itself)")
		}
		return nil
	},
	ValidArgsFunction: autocompletePlaybookNames,
	RunE:              runUpdate,
}

func init() {
	updateCmd.Flags().BoolVar(&updateDryRun, "dry-run", false, "show what the update would do, migrate step included, and change nothing")
	updateCmd.Flags().BoolVar(&updateYes, "yes", false, "answer the yes without a terminal: run the migrate step; for a played playbook, never confirms an endpoint, a proxy, TLS or a secret")
	updateCmd.Flags().BoolVar(&updateJSON, "json", false, "played playbook, with --dry-run: the plan as JSON")
	updateCmd.Flags().StringVar(&updateSHA256, "sha256", "", "played playbook: refuse any recipe whose sha256 is not this")
	updateCmd.Flags().StringArrayVar(&updateTrustEndpoint, "trust-endpoint", nil, "played playbook, without a terminal: confirm a model endpoint or proxy host (or TLS); repeatable")
	updateCmd.Flags().StringArrayVar(&updateTrustSecret, "trust-secret", nil, "played playbook, without a terminal: confirm a secret reference; repeatable")
}

// updateOpts carries update's flags into the [source] path.
type updateOpts struct {
	dryRun bool
	yes    bool
}

// updateAsks is whether the migrate step can be asked about: a terminal on
// both ends. A variable for tests.
var updateAsks = func() bool { return isTerminal(os.Stdin) && isTerminal(os.Stdout) }

func runUpdate(cmd *cobra.Command, args []string) error {
	name := args[0]
	pb, err := playbook.Require(config.ResolvePlaybooksDir(), name)
	if err != nil {
		return err
	}
	played := pb.Manifest != nil && pb.Manifest.Play != nil
	if !played {
		for _, f := range []string{"json", "sha256", "trust-endpoint", "trust-secret"} {
			if cmd.Flags().Changed(f) {
				return fmt.Errorf("--%s applies to a playbook kept by cpb play; %s updates from its [source]", f, name)
			}
		}
		return runPlaybookUpdate(os.Stdout, name, updateOpts{dryRun: updateDryRun, yes: updateYes})
	}
	if updateJSON && !updateDryRun {
		return errors.New("--json goes with --dry-run")
	}
	// The played path reads play's own options, for this call only.
	playDryRun, playJSONF, playYes, playSHA256 = updateDryRun, updateJSON, updateYes, updateSHA256
	playTrustEndpoint, playTrustSecret, playEnvSets = updateTrustEndpoint, updateTrustSecret, nil
	defer func() {
		playDryRun, playJSONF, playYes, playSHA256 = false, false, false, ""
		playTrustEndpoint, playTrustSecret, playEnvSets = nil, nil, nil
	}()
	return playUpdateRun(name)
}

func runPlaybookUpdate(w io.Writer, name string, o updateOpts) error {
	playbooksDir := config.ResolvePlaybooksDir()

	pb, err := playbook.Require(playbooksDir, name)
	if err != nil {
		return err
	}
	if pb.Manifest == nil || pb.Manifest.Source == nil || pb.Manifest.Source.Repository == "" {
		return fmt.Errorf("%q has no [source] or [play] record in .playbook; nothing to update from", name)
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

	work, cleanup, err := stageSource(w, pb.Manifest.Source.Repository, isGitURL(pb.Manifest.Source.Repository), pb.Manifest.Source.Branch, pb.Manifest.Source.Subdir)
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

	upToDate := fromVersion != "" && fromVersion == toVersion
	step, err := migrateStep(work, stagedManifest, fromVersion, toVersion)
	if err != nil {
		return err
	}

	if o.dryRun {
		fmt.Fprintf(w, "%s\n", name)
		fmt.Fprintf(w, "  installed: %s\n", displayVersion(fromVersion))
		fmt.Fprintf(w, "  available: %s\n", displayVersion(toVersion))
		if upToDate {
			fmt.Fprintln(w, "  up to date")
		}
		fmt.Fprintf(w, "  migrate:   %s\n", step.describe())
		fmt.Fprintln(w, "Nothing was changed (--dry-run).")
		return nil
	}
	// A declared migrate step is agreed to before anything changes, even one
	// that will not run (a version unknown on one side): declined, the
	// update does not happen at all, so the files never run ahead of it.
	if step.rel != "" {
		switch {
		case o.yes:
		case updateAsks():
			fmt.Fprintf(w, "%s declares a migrate step: %s\n", name, step.describe())
			if !confirm(fmt.Sprintf("Update %s and run it? [y/N] ", name)) {
				fmt.Fprintln(w, "Cancelled; nothing was changed.")
				return nil
			}
		default:
			return fmt.Errorf("%s declares a migrate step (%s); pass --yes to run it, or --dry-run to see the update; nothing was changed", name, step.describe())
		}
	}

	// Staging ran unlocked (it may fetch from the network); the overlay must
	// not. Take the registry lock and RE-READ the live manifest: a concurrent
	// ALTER PLAYBOOK … ALIAS (or other manifest mutation) that landed while the source was
	// staging would otherwise be resurrected from the stale pre-staging
	// snapshot, leaving launchers and manifest disagreeing.
	lockedUnlock, lerr := lockRegistry()
	if lerr != nil {
		return lerr
	}
	// The migrate step runs after the lock is released (it may run cpb
	// statements, which take it); every earlier return releases it here.
	var once sync.Once
	unlock := func() { once.Do(lockedUnlock) }
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
	// (alias, isolation, source, [env], and the [mcp] and [skills] records
	// statements keep) come from the live manifest; the
	// install's name is its directory name (this also heals installs whose
	// manifest predates name rewriting).
	updated := stagedManifest
	if updated == nil {
		updated = &manifest.Manifest{}
		*updated = *liveManifest
	} else {
		copied := *updated
		updated = &copied
		updated.Launcher = liveManifest.Launcher
		updated.IsolatedLogin = liveManifest.IsolatedLogin
		updated.Source = liveManifest.Source
		updated.Env = liveManifest.Env
		updated.Sandbox = liveManifest.Sandbox
		updated.MCP = liveManifest.MCP
		updated.Skills = liveManifest.Skills
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

	fmt.Fprintf(w, "Updating %s from %s...\n", name, pb.Manifest.Source.Repository)
	// The skills the source ships, before the overlay moves them: a
	// recorded skill of the same name is put back over them.
	shipped := map[string]bool{}
	if entries, derr := os.ReadDir(filepath.Join(work, "skills")); derr == nil {
		for _, e := range entries {
			shipped[e.Name()] = true
		}
	}
	backupPath, err := overlaySource(work, root, preserve)
	if err != nil {
		return err
	}

	fmt.Fprintf(w, "Updated %q to %s. Replaced files backed up to %s.\n", name, displayVersion(toVersion), backupPath)

	// The overlay can replace skills/ as a whole: put back the skills
	// statements added (docs/reference/cli-grammar.md, "Skills").
	if updated.Skills != nil {
		if err := restoreSkills(w, pb.Path, updated.Skills, shipped); err != nil {
			return fmt.Errorf("%q is updated, but restoring its skills failed: %w (run its ADD SKILL statements again)", name, err)
		}
	}

	// The installed step is checked while the lock is still held, so no
	// other cpb process can change it between the check and the release.
	if err := step.verify(root); err != nil {
		return fmt.Errorf("%q is at code version %s, but its migrate step was not run: %w", name, displayVersion(toVersion), err)
	}
	unlock()
	if err := step.run(w, name, root); err != nil {
		return fmt.Errorf("%q is at code version %s, but its migrate step failed: %w", name, displayVersion(toVersion), err)
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

// migration is a source's declared migrate step ([update] migrate), as an
// update previews and runs it.
type migration struct {
	rel      string // the declared path, relative to the playbook root; "" for none
	sha256   string // of the staged script, previewed and checked again before it runs
	from, to string
	skip     bool // declared, but a version is unknown on one side
}

// migrateStep reads the staged source's declared step, and only the source's:
// the installed copy's [update] never counts (a rewrite by an older cpb may
// have dropped it, and the step belongs to the version being installed). A
// declared step must resolve inside the staged tree to an executable file.
func migrateStep(work string, staged *manifest.Manifest, from, to string) (migration, error) {
	if staged == nil || staged.Update == nil || staged.Update.Migrate == "" {
		return migration{}, nil
	}
	m := migration{rel: staged.Update.Migrate, from: from, to: to, skip: from == "" || to == ""}
	sum, err := migrateScriptSum(work, m.rel)
	if err != nil {
		return migration{}, fmt.Errorf("the source's migrate step: %w", err)
	}
	m.sha256 = sum
	return m, nil
}

// migrateScriptSum resolves rel inside root (symlinks may not leave it) to an
// executable regular file and hashes it.
func migrateScriptSum(root, rel string) (string, error) {
	p, err := manifest.ResolvePath(root, "update.migrate", rel)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return "", fmt.Errorf("update.migrate %q is not an executable file", rel)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:]), nil
}

func (m migration) describe() string {
	switch {
	case m.rel == "":
		return "none"
	case m.skip:
		return fmt.Sprintf("%s (sha256 %s), not run: the version is unknown on one side", m.rel, shortSHA(m.sha256))
	default:
		return fmt.Sprintf("%s (sha256 %s), run as %s %s %s <install dir>", m.rel, shortSHA(m.sha256), m.rel, m.from, m.to)
	}
}

// verify checks the installed step: still inside the playbook, and the same
// bytes that were previewed and agreed to.
func (m migration) verify(root string) error {
	if m.rel == "" || m.skip {
		return nil
	}
	sum, err := migrateScriptSum(root, m.rel)
	if err != nil {
		return err
	}
	if sum != m.sha256 {
		return fmt.Errorf("%s changed between the preview and the run (sha256 %s, previewed %s)", m.rel, shortSHA(sum), shortSHA(m.sha256))
	}
	return nil
}

// run runs the verified step from the installed playbook, as <script>
// <from> <to> <install dir>, in the install dir. Between verify, under the
// registry lock, and this exec there is a window in which a writer to the
// playbook directory could swap the script. That writer is the same user,
// already able to change anything the playbook runs, so the window is
// accepted: running a private copy instead would break a script that finds
// its sibling files through its own path, as kommander's apply.sh does.
func (m migration) run(w io.Writer, name, root string) error {
	if m.rel == "" {
		return nil
	}
	if m.skip {
		fmt.Fprintf(os.Stderr, "Warning: %s's migrate step %s was not run: the version is unknown on one side\n", name, m.rel)
		return nil
	}
	fmt.Fprintf(w, "Running the migrate step %s %s -> %s...\n", m.rel, m.from, m.to)
	c := exec.Command(filepath.Join(root, filepath.FromSlash(m.rel)), m.from, m.to, root)
	c.Dir = root
	c.Env = append(config.WithoutConfigDirOverride(os.Environ()),
		"CLAUDE_CONFIG_DIR="+root,
		"CPB_PLAYBOOK_NAME="+name,
		"CPB_PLAYBOOK_DIR="+root,
	)
	c.Stdin = os.Stdin
	c.Stdout = w
	c.Stderr = w
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
		// ENOTDIR here means the source introduced a regular FILE where the
		// preserved path expects a directory: nothing at dst, already absent.
		if err := removeAny(dst); err != nil && !absentPath(err) {
			return err
		}
		return nil
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
