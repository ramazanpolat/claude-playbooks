package cmd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envprofile"
	"github.com/ramazanpolat/claude-playbooks/internal/launcher"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

var deleteYes bool

var deleteCmd = &cobra.Command{
	Use:               "delete <name>",
	Aliases:           []string{"uninstall", "unlink"},
	Short:             "Delete a playbook",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: autocompletePlaybookNames,
	RunE:              runDelete,
}

func init() {
	deleteCmd.Flags().BoolVarP(&deleteYes, "yes", "y", false, "skip confirmation prompt")
}

func runDelete(cmd *cobra.Command, args []string) error {
	name := args[0]
	playbooksDir := config.ResolvePlaybooksDir()

	if err := validateSinglePathSegment("playbook name", name); err != nil {
		return err
	}
	if err := refuseRegistryOwned(playbooksDir, name, filepath.Join(playbooksDir, name)); err != nil {
		return err
	}

	// Discovery and confirmation run on an UNLOCKED snapshot: the prompt
	// waits on human input, and holding the machine-user-global registry
	// lock there would block every concurrent command indefinitely.
	pb, err := playbook.Find(playbooksDir, name)
	if err != nil {
		return err
	}
	if pb == nil {
		// Allow cleanup of dangling state when the directory is already gone:
		// only proceed if a directory exists at the expected path.
		path := filepath.Join(playbooksDir, name)
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			return fmt.Errorf("%q not found under %s", name, playbooksDir)
		}
		return deleteOrphan(playbooksDir, name, path)
	}

	if !deleteYes {
		aliasInfo := "(no alias)"
		if a := pb.Alias(); a != "" {
			aliasInfo = fmt.Sprintf("%s (its launcher will be removed)", a)
		}
		deletePath := pb.RootPath
		if deletePath == "" {
			deletePath = pb.Path
		}
		fileCount, dirCount := countContents(deletePath)
		fmt.Printf("Playbook: %s\n", pb.Name)
		fmt.Printf("Location: %s\n", deletePath)
		if deletePath != pb.Path {
			fmt.Printf("Config:   %s\n", pb.Path)
		}
		fmt.Printf("Alias:    %s\n", aliasInfo)
		if launcherOpsAllowed() {
			if ldir, lerr := config.ResolveLauncherDir(); lerr == nil {
				for _, n := range launcherNamesFor(pb) {
					if _, exists, foreign := launcher.Lookup(ldir, n); exists && !foreign {
						fmt.Printf("Command:  %s (launcher kept; removal hint printed after delete)\n", n)
					}
				}
			}
		}
		fmt.Printf("Contents: %d files, %d directories\n", fileCount, dirCount)
		if !confirm("\nPermanently delete? [y/N] ") {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	// Now lock and REVALIDATE: a concurrent rename may have moved the
	// playbook while the prompt was open (see lockRegistry).
	unlock, err := lockRegistry()
	if err != nil {
		return err
	}
	defer unlock()
	pb, err = playbook.Find(playbooksDir, name)
	if err != nil {
		return err
	}
	if pb == nil {
		return fmt.Errorf("%q disappeared while waiting for confirmation (deleted or renamed concurrently); nothing removed", name)
	}

	deletePath := pb.RootPath
	if deletePath == "" {
		deletePath = pb.Path
	}
	// Re-checked under the lock, against the path actually removed: the
	// store may have been created, moved, or linked while the prompt was open.
	if err := refuseRegistryOwned(playbooksDir, name, deletePath); err != nil {
		return err
	}
	names := launcherNamesFor(pb)
	if err := removeAny(deletePath); err != nil {
		return fmt.Errorf("failed to delete %s: %w", deletePath, err)
	}
	removeUnclaimedLaunchers(names)
	fmt.Printf("Deleted playbook %q.\n", pb.Name)
	return nil
}

// deleteOrphan handles a directory that exists at the expected path but is
// not a discoverable playbook (e.g. a dotfile-named entry). Cleans up any
// aliases pointing into it and removes the directory.
func deleteOrphan(playbooksDir, name, path string) error {
	if !deleteYes {
		fmt.Printf("Directory %q exists at %s but is not a discoverable playbook.\n", name, path)
		if !confirm("Permanently delete the directory and any aliases pointing into it? [y/N] ") {
			fmt.Println("Cancelled.")
			return nil
		}
	}
	// Lock only after the prompt (see runDelete), then re-verify the
	// directory is still present and still not a discoverable playbook.
	unlock, err := lockRegistry()
	if err != nil {
		return err
	}
	defer unlock()
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return fmt.Errorf("%q disappeared while waiting for confirmation; nothing removed", name)
	}
	if pb, _ := playbook.Find(playbooksDir, name); pb != nil {
		return fmt.Errorf("%q became a discoverable playbook while waiting for confirmation; re-run delete", name)
	}
	if err := refuseRegistryOwned(playbooksDir, name, path); err != nil {
		return err
	}
	if err := removeAny(path); err != nil {
		return fmt.Errorf("failed to delete %s: %w", path, err)
	}
	removeUnclaimedLaunchers([]string{name})
	fmt.Printf("Deleted %q.\n", name)
	return nil
}

// removeUnclaimedLaunchers retires the launcher symlinks for the given
// command names after a mutation. A name still resolving in the visible
// registry keeps its launcher outright. An unclaimed name's launcher is
// ALSO retained — a stateless symlink may be serving a playbook in another
// registry root selected via environment or flag, which is unenumerable
// from here — but with a manual-removal hint: invoking it without such a
// root fails loudly as stale, so retention is noisy, never silently wrong.
func removeUnclaimedLaunchers(names []string) {
	if !launcherOpsAllowed() {
		fmt.Fprintf(os.Stderr, "Note: launchers are managed only for the default playbooks root; none removed.\n")
		return
	}
	dir, err := config.ResolveLauncherDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not inspect launchers: %v\n", err)
		return
	}
	for _, n := range names {
		e, exists, foreign := launcher.Lookup(dir, n)
		if !exists || foreign {
			continue
		}
		if owner, oerr := commandNameOwner(n, ""); oerr == nil && owner != nil {
			fmt.Printf("Kept command %q (still addresses playbook %q)\n", n, owner.Name)
			continue
		}
		fmt.Printf("Kept command %q — launchers may serve other registry roots; remove it manually if unused:\n  rm %s\n", n, e.Path)
	}
}

func removeAny(path string) error {
	linfo, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if linfo.Mode()&os.ModeSymlink != 0 {
		return os.Remove(path)
	}
	return os.RemoveAll(path)
}

func confirm(prompt string) bool {
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))
	return answer == "y" || answer == "yes"
}

func countContents(dir string) (files, dirs int) {
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || path == dir {
			return nil
		}
		if info.IsDir() {
			dirs++
		} else {
			files++
		}
		return nil
	})
	return
}

// refuseRegistryOwned rejects a delete whose removal would take the
// registry's own env profile store with it. Discovery skips dot-prefixed
// entries, so the store's name would otherwise fall through to the orphan
// path and remove every profile and the default marker in one confirmation.
//
// Three shapes are refused: the name itself; a path that IS the store by
// file identity (a case variant on a case-insensitive filesystem), judged
// with Lstat so a leftover symlink pointing at the store is still deletable
// (only the link goes, see removeAny); and a real directory whose physical
// subtree contains the store's resolved location (the store symlinked into a
// leftover, `.env-profiles -> .leftover/profiles`), which RemoveAll would
// otherwise descend into. Callers run it before the prompt and again under
// the registry lock against the path actually removed, since the store may
// appear, move, or be linked while the prompt is open.
func refuseRegistryOwned(playbooksDir, name, path string) error {
	store := envprofile.Dir(playbooksDir)
	refuse := func() error {
		return fmt.Errorf("%q is the registry's env profile store, not a playbook; remove a profile with 'claude-playbook env-profile <name> delete'", envprofile.DirName)
	}
	if name == envprofile.DirName {
		return refuse()
	}
	a, err := os.Lstat(path)
	if err != nil {
		return nil // nothing at the path: nothing the removal could take
	}
	// The store's own registry entry, by identity: a directory, or the
	// symlink the registry keeps when the store lives elsewhere. Catches a
	// case variant of the name on a case-insensitive filesystem.
	if entry, err := os.Lstat(store); err == nil && os.SameFile(a, entry) {
		return refuse()
	}
	target, err := os.Stat(store)
	if err != nil {
		return nil // no store behind the entry (a dangling link is not one)
	}
	if os.SameFile(a, target) {
		return refuse() // the store's physical directory under another name
	}
	if !a.IsDir() {
		return nil // a symlink or a file: removal never descends
	}
	// Containment, by identity rather than by string: RemoveAll(path) must
	// not descend into the store's physical location. Each ancestor of the
	// resolved store is compared to the directory about to be removed with
	// SameFile, which is immune to spelling (case-insensitive filesystems),
	// to relative playbooks roots, and to symlinks along either path.
	physStore, err := filepath.EvalSymlinks(store)
	if err != nil {
		return nil
	}
	if physStore, err = filepath.Abs(physStore); err != nil {
		return nil
	}
	for dir := physStore; ; dir = filepath.Dir(dir) {
		if di, err := os.Stat(dir); err == nil && os.SameFile(di, a) {
			return fmt.Errorf("%q contains the registry's env profile store (%s resolves to %s); move the store out or remove profiles with 'claude-playbook env-profile <name> delete' first", name, store, physStore)
		}
		if filepath.Dir(dir) == dir {
			return nil
		}
	}
}
