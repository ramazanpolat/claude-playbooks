package cmd

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
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
	Short: "Update a playbook from its source, its played recipe, or the files last applied to it",
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

A playbook an APPLY gave name-less statements ([apply]) gets those files
applied again: only the statements they give it, after one ALTER
PLAYBOOK that removes what they wrote last time and no longer write the
same way. --dry-run shows the change and the plan.

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
	updateCmd.Flags().BoolVar(&updateYes, "yes", false, "answer the yes without a terminal: run the migrate step; for a played playbook, never confirms a proxy, TLS or a secret")
	updateCmd.Flags().BoolVar(&updateJSON, "json", false, "played or applied playbook, with --dry-run: the plan as JSON")
	updateCmd.Flags().StringVar(&updateSHA256, "sha256", "", "played playbook: refuse any recipe whose sha256 is not this")
	updateCmd.Flags().StringArrayVar(&updateTrustEndpoint, "trust-endpoint", nil, "played playbook, without a terminal: confirm a proxy host (or TLS); repeatable")
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

// updateStaged runs once the source is staged, which happens without the
// registry lock: a test changes the install there, as a statement run
// meanwhile could.
var updateStaged = func() {}

func runUpdate(cmd *cobra.Command, args []string) error {
	name := args[0]
	pb, err := playbook.Require(config.ResolvePlaybooksDir(), name)
	if err != nil {
		return err
	}
	played := pb.Manifest != nil && pb.Manifest.Play != nil
	sourced := pb.Manifest != nil && pb.Manifest.Source != nil && pb.Manifest.Source.Repository != ""
	if !played && !sourced && pb.Manifest != nil && pb.Manifest.Apply != nil {
		// The files APPLY last applied to it, applied again (SPEC.md, "The
		// [apply] record").
		for _, f := range []string{"sha256", "trust-endpoint", "trust-secret"} {
			if cmd.Flags().Changed(f) {
				return fmt.Errorf("--%s applies to a playbook kept by cpb play; %s applies its recorded files again", f, name)
			}
		}
		if updateJSON && !updateDryRun {
			return errors.New("--json goes with --dry-run")
		}
		return applyUpdateRun(pb, updateDryRun, updateJSON)
	}
	if !played {
		// Without a [source] either, runPlaybookUpdate says there is
		// nothing to update from, whatever the flags.
		for _, f := range []string{"json", "sha256", "trust-endpoint", "trust-secret"} {
			if sourced && cmd.Flags().Changed(f) {
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
		return fmt.Errorf("%q has no [source], [play] or [apply] record in .playbook; nothing to update from", name)
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
	// A source's runtime state would replace the install's own (its
	// history, its sessions) in the overlay: it never reaches the install.
	if err := stripSourceRuntime(work, pb.Manifest.Source.Repository); err != nil {
		return err
	}
	updateStaged()

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
		// What the overlay would replace: the live manifest read again under
		// the registry lock, as the update reads it; the manifest assembled,
		// and its mode set, as the update does, in the staging directory;
		// then the staged tree copied as the overlay copies it into a
		// scratch folder (a link inside the source becomes the file or
		// folder it names, each folder and file takes the source's mode),
		// and that copy compared with the install, entry by entry. Both
		// folders are removed after.
		unlock, lerr := lockRegistry()
		if lerr != nil {
			return lerr
		}
		live, err := liveForActivation(name, root, rootInfo, pb.Manifest.Source)
		var cerr error
		if err == nil {
			_, cerr = prepareUpdatedManifest(work, root, stagedManifest, live)
		}
		unlock()
		if err != nil {
			return err
		}
		var changes []string
		if cerr == nil {
			var view string
			var removeView func()
			if view, removeView, cerr = overlayView(work); cerr == nil {
				var skills []string
				for n, r := range live.Skills {
					if r != nil {
						skills = append(skills, n)
					}
				}
				changes, cerr = newUpdateCompare(root, preserve, skills, isGitURL(pb.Manifest.Source.Repository)).updateChanges(view)
				removeView()
			}
		}
		fmt.Fprintf(w, "%s\n", name)
		fmt.Fprintf(w, "  installed: %s\n", displayVersion(fromVersion))
		fmt.Fprintf(w, "  available: %s\n", displayVersion(toVersion))
		switch {
		case cerr != nil:
			fmt.Fprintf(w, "  replaces:  not known (the files could not be compared: %v)\n", cerr)
		case len(changes) == 0:
			fmt.Fprintln(w, "  replaces:  nothing (the source's files are the installed ones)")
		default:
			fmt.Fprintf(w, "  replaces:  %s\n", strings.Join(changes, ", "))
		}
		switch {
		case upToDate && cerr == nil && len(changes) == 0:
			fmt.Fprintln(w, "  up to date")
		case upToDate && cerr == nil:
			fmt.Fprintln(w, "  same version, other files: an update replaces what is listed above")
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
			if !confirm(step.consentPrompt(name)) {
				fmt.Fprintln(w, "Cancelled; nothing was changed.")
				return nil
			}
		default:
			return step.consentRefusal(name)
		}
	}

	// Staging ran unlocked (it may fetch from the network); the overlay must
	// not. Take the registry lock and RE-READ the live manifest: a concurrent
	// ALTER PLAYBOOK … LAUNCHER (or other manifest mutation) that landed while the source was
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
	liveManifest, err := liveForActivation(name, root, rootInfo, pb.Manifest.Source)
	if err != nil {
		return err
	}
	updated, err := prepareUpdatedManifest(work, root, stagedManifest, liveManifest)
	if err != nil {
		return fmt.Errorf("failed to prepare updated manifest: %w", err)
	}

	fmt.Fprintf(w, "Updating %s from %s...\n", name, redactSourceURL(pb.Manifest.Source.Repository))
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
	kept, trimErr := trimUpdateBackup(backupPath, preserve)
	if kept {
		fmt.Fprintf(w, "Updated %q to %s. Replaced files backed up to %s.\n", name, displayVersion(toVersion), backupPath)
	} else {
		fmt.Fprintf(w, "Updated %q to %s.\n", name, displayVersion(toVersion))
	}
	if trimErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: %v\n", trimErr)
	}
	for _, err := range removeUpdateBackups(root, backupPath) {
		fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
	}

	// The overlay can replace skills/ as a whole: put back the skills
	// statements added (SPEC.md, "Skills").
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

// assembleUpdatedManifest is the manifest an update puts live: the staged
// one with every install-local field (alias, isolation, the
// model_picker_defaults opt-out, failover, source, [env], [sandbox], and
// the [mcp], [skills], [play] and [apply] records) taken from the live
// one, and the install's directory name as its name.
func assembleUpdatedManifest(staged, live *manifest.Manifest, root string) *manifest.Manifest {
	updated := &manifest.Manifest{}
	switch {
	case staged == nil && live != nil:
		*updated = *live
	case staged != nil:
		*updated = *staged
		if live == nil {
			live = &manifest.Manifest{}
		}
		updated.Launcher = live.Launcher
		updated.IsolatedLogin = live.IsolatedLogin
		updated.ModelPickerDefaults = live.ModelPickerDefaults
		updated.Failover = live.Failover
		updated.LabelVars = live.LabelVars
		updated.Source = live.Source
		updated.Env = live.Env
		updated.Sandbox = live.Sandbox
		updated.MCP = live.MCP
		updated.Skills = live.Skills
		updated.Play = live.Play
		updated.Apply = live.Apply
	}
	updated.Name = filepath.Base(root)
	return updated
}

// liveForActivation re-reads the install's manifest after staging, with
// the registry lock held, and binds the update to the exact installation
// inspected before it: the DIRECTORY must be the same filesystem object
// (a delete + reinstall from the very same repository passes any manifest
// comparison), and every source field must match. Anything else means the
// playbook was deleted, re-created, or re-sourced while staging ran.
func liveForActivation(name, root string, rootInfo os.FileInfo, source *manifest.Source) (*manifest.Manifest, error) {
	live, err := manifest.Read(root)
	if err != nil {
		return nil, fmt.Errorf("cannot re-read manifest before activation: %w", err)
	}
	liveInfo, lierr := os.Lstat(root)
	if lierr != nil || !os.SameFile(rootInfo, liveInfo) ||
		live == nil || live.Source == nil || source == nil || *live.Source != *source {
		return nil, fmt.Errorf("playbook %q changed while the update was staging (deleted, re-created, or re-sourced); nothing activated -- re-run update", name)
	}
	return live, nil
}

// prepareUpdatedManifest writes the manifest that goes live into the
// staged tree work, with the mode it will have in root, and returns it.
//
// The manifest is assembled in the STAGED tree before the overlay, never
// corrected in place afterwards: the overlay copies the staged .playbook
// over the live one, launches do not take the registry lock, and a
// source-shipped [env] block that was live even briefly -- or permanently,
// had a later rewrite failed -- could redirect the install's endpoint or
// strip its authentication. Install-local fields (alias, isolation, the
// model_picker_defaults opt-out, failover, label_vars, source, [env], and
// the [mcp] and [skills] records statements keep) come from the live
// manifest; the install's name is its directory name (this also heals
// installs whose manifest predates name rewriting).
//
// Manifest.Write's never-loosen rule looks at the STAGED file's mode; the
// overlay replaces the live file with it, so the staged copy takes the
// live file's mode masked by its own -- never looser than either, whatever
// the pilot had chosen.
func prepareUpdatedManifest(work, root string, staged, live *manifest.Manifest) (*manifest.Manifest, error) {
	updated := assembleUpdatedManifest(staged, live, root)
	if err := manifest.Write(work, updated); err != nil {
		return nil, err
	}
	if live, err := os.Stat(filepath.Join(root, manifest.FileName)); err == nil {
		path := filepath.Join(work, manifest.FileName)
		if info, err := os.Stat(path); err == nil {
			if err := os.Chmod(path, live.Mode().Perm()&info.Mode().Perm()); err != nil {
				return nil, err
			}
		}
	}
	return updated, nil
}

// overlayView copies the staged tree work into a new scratch folder as the
// overlay copies it into an install (overlayDir): what the update would
// leave in place of each entry it replaces. The function removes it.
func overlayView(work string) (string, func(), error) {
	view, err := os.MkdirTemp("", "cpb-update-view-")
	if err != nil {
		return "", func() {}, err
	}
	remove := func() { os.RemoveAll(view) }
	if err := overlayDir(work, view); err != nil {
		remove()
		return "", func() {}, err
	}
	return view, remove, nil
}

// updateCompare is how a dry run compares the staged tree, as the overlay
// would write it (overlayView), with the install root. What the update
// puts back after the overlay is left out on both sides, as the update
// itself finds it:
//   - the preserved paths, by spelling and, in the install, under another
//     spelling the filesystem takes for the same entry (as touchedEntry
//     finds it: on a case-insensitive filesystem a shipped Settings.json
//     is the preserved settings.json; leftOut);
//   - the folders a preserved path's restore creates when the source does
//     not ship them, with nothing in them but what is put back;
//   - skills/<name> for each [skills] record, which restoreSkills puts
//     back over the source's skills/.
//
// No comparison follows a link or waits: a link is compared by its target,
// a file or folder is opened without following and without blocking, and
// anything else (a FIFO, a device) differs from what is not the same kind.
type updateCompare struct {
	root      string
	skip      map[string]bool
	preserved []preservedEntry
	ancestors map[string]bool
	// gitURL: the staged tree is cpb's own fresh clone, whose .git is
	// compared by the commit its HEAD names (gitHead). A local source's
	// .git is compared as files, like any folder.
	gitURL bool
}

// newUpdateCompare is the comparison for an install root, its preserved
// paths, the names of its [skills] records and whether its source is a
// git URL.
func newUpdateCompare(root string, preserve, skills []string, gitURL bool) *updateCompare {
	c := &updateCompare{root: root, skip: map[string]bool{}, ancestors: map[string]bool{}, gitURL: gitURL}
	for _, p := range preserve {
		p = filepath.ToSlash(p)
		c.skip[p] = true
		if fi, err := os.Lstat(filepath.Join(root, filepath.FromSlash(p))); err == nil {
			c.preserved = append(c.preserved, preservedEntry{rel: p, info: fi})
		}
		for i := 0; i < len(p); i++ {
			if p[i] == '/' {
				c.ancestors[p[:i]] = true
			}
		}
	}
	for _, n := range skills {
		c.skip["skills/"+n] = true
	}
	return c
}

// preservedEntry is a preserved path, as the preserve list spells it, and
// the install's entry at it.
type preservedEntry struct {
	rel  string
	info os.FileInfo
}

// leftOut reports whether rel is put back by the update: a skipped path,
// or the entry of a preserved path under another spelling, as touchedEntry
// finds it after the overlay. That is rel leading to the preserved file
// while its folders do not list both names as written: on a filesystem
// that ignores case, one entry answers to Settings.json and settings.json
// and its folder lists one of them, whichever spelling it holds. A hard
// link is listed under both names, on any filesystem, and is a file of its
// own, which the update replaces.
func (c *updateCompare) leftOut(rel string) bool {
	if c.skip[rel] {
		return true
	}
	if len(c.preserved) == 0 {
		return false
	}
	path := filepath.Join(c.root, filepath.FromSlash(rel))
	fi, err := os.Lstat(path)
	if err != nil {
		return false
	}
	for _, p := range c.preserved {
		if !os.SameFile(fi, p.info) {
			continue
		}
		if listedAs(path) && listedAs(filepath.Join(c.root, filepath.FromSlash(p.rel))) {
			continue // a file of its own
		}
		return true
	}
	return false
}

// listedAs reports whether path's folder lists an entry spelled exactly as
// path's last element; true when the folder cannot be read, so the entry
// is compared rather than left out.
func listedAs(path string) bool {
	ents, err := readDirNoFollow(filepath.Dir(path))
	if err != nil {
		return true
	}
	name := filepath.Base(path)
	for _, e := range ents {
		if e.Name() == name {
			return true
		}
	}
	return false
}

// updateChanges is what an update would replace in root: each top-level
// entry of view, the staged tree as the overlay would write it
// (overlayView), that is not yet in root ("new") or differs from root's
// ("changed": its type, a file's bytes or mode, a folder's mode or
// entries, the install's own extra entries counting, since the overlay
// replaces the folder whole, or a link's target), what the update puts
// back left out (updateCompare).
func (c *updateCompare) updateChanges(view string) ([]string, error) {
	entries, err := readDirNoFollow(view)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		rel := e.Name()
		if c.leftOut(rel) {
			continue
		}
		shown := rel
		if e.IsDir() {
			shown += "/"
		}
		if _, err := os.Lstat(filepath.Join(c.root, rel)); errors.Is(err, os.ErrNotExist) {
			out = append(out, shown+" (new)")
			continue
		} else if err != nil {
			return nil, err
		}
		same, err := c.sameTree(filepath.Join(view, rel), filepath.Join(c.root, rel), rel)
		if err != nil {
			return nil, err
		}
		if !same {
			out = append(out, shown+" (changed)")
		}
	}
	return out, nil
}

// sameTree reports whether a (as the overlay writes it) and b (installed)
// are the same at rel, what the update puts back left out.
func (c *updateCompare) sameTree(a, b, rel string) (bool, error) {
	ai, err := os.Lstat(a)
	if errors.Is(err, os.ErrNotExist) {
		// A folder the restore of a preserved path below it creates, when
		// the source does not ship it; otherwise the install's own, which
		// the overlay removes.
		if c.ancestors[rel] {
			return c.onlyPutBack(b, rel)
		}
		return false, nil
	} else if err != nil {
		return false, err
	}
	bi, err := os.Lstat(b)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if ai.Mode().Type() != bi.Mode().Type() {
		return false, nil
	}
	switch {
	case ai.Mode()&os.ModeSymlink != 0:
		la, err := os.Readlink(a)
		if err != nil {
			return false, err
		}
		lb, err := os.Readlink(b)
		return err == nil && la == lb, err
	case ai.IsDir():
		// The overlay makes each folder it writes with the source's mode.
		if ai.Mode().Perm() != bi.Mode().Perm() {
			return false, nil
		}
		// A git source's .git is a fresh clone's on every update: its
		// bookkeeping differs each time, the commit it holds need not.
		if rel == ".git" && c.gitURL {
			if ha, hb := gitHead(a), gitHead(b); ha != "" && hb != "" {
				return ha == hb, nil
			}
		}
		names := map[string]bool{}
		for _, dir := range []string{a, b} {
			ents, err := readDirNoFollow(dir)
			if err != nil {
				return false, err
			}
			for _, e := range ents {
				names[e.Name()] = true
			}
		}
		for n := range names {
			r := rel + "/" + n
			if c.leftOut(r) {
				continue
			}
			same, err := c.sameTree(filepath.Join(a, n), filepath.Join(b, n), r)
			if err != nil || !same {
				return false, err
			}
		}
		return true, nil
	case ai.Mode().IsRegular():
		// The manifest's mode is the one prepareUpdatedManifest gave it.
		if ai.Size() != bi.Size() || ai.Mode().Perm() != bi.Mode().Perm() {
			return false, nil
		}
		ab, err := readRegular(a, -1)
		if errors.Is(err, errNotComparable) {
			return false, nil
		} else if err != nil {
			return false, err
		}
		bb, err := readRegular(b, -1)
		if errors.Is(err, errNotComparable) {
			return false, nil
		}
		return err == nil && bytes.Equal(ab, bb), err
	}
	// A FIFO, a device, a socket: never opened, so never the same.
	return false, nil
}

// onlyPutBack reports whether b, an install folder the source does not
// ship at rel, holds only what the update puts back: preserved paths, and
// folders above them that hold only those. The update creates such a
// folder anew for the restore, so its own mode is left out too.
func (c *updateCompare) onlyPutBack(b, rel string) (bool, error) {
	bi, err := os.Lstat(b)
	if err != nil || !bi.IsDir() {
		return false, nil
	}
	ents, err := readDirNoFollow(b)
	if err != nil {
		return false, err
	}
	for _, e := range ents {
		r := rel + "/" + e.Name()
		if c.leftOut(r) {
			continue
		}
		if !c.ancestors[r] {
			return false, nil
		}
		if ok, err := c.onlyPutBack(filepath.Join(b, e.Name()), r); err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

// errNotComparable is an entry a dry run does not read: a link where a
// file or folder was found, a FIFO, a device, or a file over its cap.
var errNotComparable = errors.New("not a regular file or folder, or too large to read")

// openNoFollow opens path for reading without following a link at its
// last step and without waiting (a FIFO opens at once), and keeps it only
// when it is a folder (dir) or a regular file.
func openNoFollow(path string, dir bool) (*os.File, error) {
	flags := os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
	if dir {
		flags |= syscall.O_DIRECTORY
	}
	f, err := os.OpenFile(path, flags, 0)
	if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.ENOTDIR) {
		return nil, errNotComparable
	} else if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if (dir && !fi.IsDir()) || (!dir && !fi.Mode().IsRegular()) {
		f.Close()
		return nil, errNotComparable
	}
	return f, nil
}

// readRegular reads a regular file reached without a link at its last
// step, at most max bytes (none when max < 0).
func readRegular(path string, max int64) ([]byte, error) {
	f, err := openNoFollow(path, false)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if max < 0 {
		return io.ReadAll(f)
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err == nil && int64(len(b)) > max {
		return nil, errNotComparable
	}
	return b, err
}

// readDirNoFollow lists a folder reached without a link at its last step.
func readDirNoFollow(path string) ([]os.DirEntry, error) {
	f, err := openNoFollow(path, true)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	ents, err := f.ReadDir(-1)
	sort.Slice(ents, func(i, j int) bool { return ents[i].Name() < ents[j].Name() })
	return ents, err
}

// gitHead is the commit a .git folder's HEAD names, read from its files
// (HEAD, then the loose ref or packed-refs line it points to) without
// running git in the install; "" when it cannot tell. Each file is read
// only when every step below dir is a real folder and the file a regular
// one, opened without following a link and without waiting, within a cap.
func gitHead(dir string) string {
	b, err := readGitFile(dir, "HEAD", 4<<10)
	if err != nil {
		return ""
	}
	head := strings.TrimSpace(string(b))
	ref, isRef := strings.CutPrefix(head, "ref: ")
	if !isRef {
		if gitObjectID.MatchString(head) {
			return head
		}
		return ""
	}
	if !gitRefName.MatchString(ref) || strings.Contains(ref, "..") {
		return ""
	}
	b, err = readGitFile(dir, ref, 4<<10)
	if err == nil {
		if id := strings.TrimSpace(string(b)); gitObjectID.MatchString(id) {
			return id
		}
		return ""
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "" // a link, a FIFO, a folder: cpb cannot tell
	}
	packed, err := readGitFile(dir, "packed-refs", gitPackedRefsMax)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(packed), "\n") {
		if id, name, ok := strings.Cut(strings.TrimSpace(line), " "); ok && name == ref && gitObjectID.MatchString(id) {
			return id
		}
	}
	return ""
}

// gitPackedRefsMax is the largest packed-refs gitHead reads; a larger one
// leaves .git compared as files.
const gitPackedRefsMax = 8 << 20

// readGitFile reads rel (slash-separated) below dir: each folder on the way
// a real one (a link refuses), the file a regular one opened without
// following and without waiting, at most max bytes.
func readGitFile(dir, rel string, max int64) ([]byte, error) {
	p := dir
	parts := strings.Split(rel, "/")
	for _, part := range parts[:len(parts)-1] {
		p = filepath.Join(p, part)
		fi, err := os.Lstat(p)
		if err != nil {
			return nil, err
		}
		if !fi.IsDir() {
			return nil, errNotComparable
		}
	}
	return readRegular(filepath.Join(p, parts[len(parts)-1]), max)
}

var (
	gitObjectID = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	gitRefName  = regexp.MustCompile(`^refs/[A-Za-z0-9._/-]{1,200}$`)
)

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
	backupPath := filepath.Join(parent, fmt.Sprintf(".%s.bak.%s", filepath.Base(root), time.Now().UTC().Format(backupStamp)))
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
			return fmt.Errorf("rollback incomplete for %s; backup kept at %s", strings.Join(failed, ", "), keepIncompleteBackup(backupPath))
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

// consentPrompt is the question a terminal is asked before an update whose
// source declares a migrate step. What is agreed to is the step, named by
// its declared path, never the playbook itself.
func (m migration) consentPrompt(name string) string {
	if m.skip {
		return fmt.Sprintf("Update %s without running its migrate step (%s)? [y/N] ", name, m.rel)
	}
	return fmt.Sprintf("Update %s and run its migrate step (%s)? [y/N] ", name, m.rel)
}

// consentRefusal is the refusal of an update with a migrate step when
// nothing agreed to it: no --yes, and no terminal to ask.
func (m migration) consentRefusal(name string) error {
	agree := "update and run the step"
	if m.skip {
		agree = "update without running it"
	}
	return fmt.Errorf("%s declares a migrate step (%s); pass --yes to %s, or --dry-run to see the update; nothing was changed", name, m.describe(), agree)
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
// its sibling files through its own path (a migrations/apply.sh that runs
// the numbered steps beside it, say).
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

// backupStamp is the timestamp in an update backup's name,
// .<name>.bak.<stamp>, beside the install.
const backupStamp = "20060102T150405.000000000"

// trimUpdateBackup removes from a successful update's backup what went live
// again: the manifest (the update carries its install-local state forward)
// and the preserved files, which were copied back. Those are the ones that
// hold values ([env] sets, a login, settings), and a second copy of them is
// only a leak. A nested preserved path is removed only when it lies
// physically inside its top-level entry in the backup (a symlinked ancestor
// could point anywhere). It reports whether the backup still holds
// anything (an empty one is removed) and what could not be removed.
func trimUpdateBackup(backupPath string, preserve []string) (bool, error) {
	var failed []string
	for _, rel := range append([]string{manifest.FileName}, preserve...) {
		p := filepath.Join(backupPath, filepath.FromSlash(rel))
		if top, _, nested := strings.Cut(rel, "/"); nested {
			within, err := physicallyWithin(filepath.Join(backupPath, top), p)
			if err != nil && !absentPath(err) {
				failed = append(failed, rel)
				continue
			}
			if !within {
				continue
			}
		}
		if err := removeAny(p); err != nil && !absentPath(err) {
			failed = append(failed, rel)
		}
	}
	var err error
	if len(failed) > 0 {
		err = fmt.Errorf("could not remove %s from the update backup %s; it holds a copy of live values, remove it by hand", strings.Join(failed, ", "), backupPath)
	}
	if entries, rerr := os.ReadDir(backupPath); rerr == nil && len(entries) == 0 {
		if os.Remove(backupPath) == nil {
			return false, err
		}
	}
	return true, err
}

// incompleteSuffix marks a backup an update kept because its rollback did
// not complete: the only copy of what could not be put back. No later
// update or DROP removes it.
const incompleteSuffix = ".incomplete"

// incompleteMarker is the file that marks such a backup when it cannot be
// renamed.
const incompleteMarker = ".cpb-rollback-incomplete"

// keepIncompleteBackup marks a backup as the remains of an incomplete
// rollback, by name, or by a marker file inside when the rename fails, and
// returns where it is.
func keepIncompleteBackup(backupPath string) string {
	kept := backupPath + incompleteSuffix
	if os.Rename(backupPath, kept) == nil {
		return kept
	}
	_ = os.WriteFile(filepath.Join(backupPath, incompleteMarker), []byte("the rollback of this update did not complete; this backup holds what could not be put back\n"), 0o600)
	return backupPath
}

// updateBackups lists the backups updates left beside root, by their exact
// name shape, so a playbook named "a" never matches "a.bak"'s. Symlinks
// and the backups of incomplete rollbacks (incompleteBackups) are never
// listed.
func updateBackups(root string) []string {
	var out []string
	for _, b := range backupsOf(root) {
		if b.complete {
			out = append(out, b.path)
		}
	}
	return out
}

// incompleteBackups lists the backups kept by an incomplete rollback.
func incompleteBackups(root string) []string {
	var out []string
	for _, b := range backupsOf(root) {
		if !b.complete {
			out = append(out, b.path)
		}
	}
	return out
}

type updateBackup struct {
	path     string
	complete bool
}

func backupsOf(root string) []updateBackup {
	parent, prefix := filepath.Dir(root), "."+filepath.Base(root)+".bak."
	entries, err := os.ReadDir(parent)
	if err != nil {
		return nil
	}
	var out []updateBackup
	for _, e := range entries {
		n := e.Name()
		if !e.IsDir() || !strings.HasPrefix(n, prefix) {
			continue
		}
		stamp, incomplete := strings.CutSuffix(n[len(prefix):], incompleteSuffix)
		if _, err := time.Parse(backupStamp, stamp); err != nil {
			continue
		}
		path := filepath.Join(parent, n)
		if _, err := os.Lstat(filepath.Join(path, incompleteMarker)); err == nil {
			incomplete = true
		}
		out = append(out, updateBackup{path: path, complete: !incomplete})
	}
	return out
}

// removeUpdateBackups removes the backups of earlier updates of root, all
// but keep ("" for none): one update's backup is what an undo needs. It
// returns what it could not remove.
func removeUpdateBackups(root, keep string) []error {
	var errs []error
	for _, b := range updateBackups(root) {
		if b != keep {
			if err := os.RemoveAll(b); err != nil {
				errs = append(errs, fmt.Errorf("could not remove the update backup %s: %w", b, err))
			}
		}
	}
	return errs
}
