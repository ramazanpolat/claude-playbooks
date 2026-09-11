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
		aliasInfo := "(none)"
		if a := pb.Alias(); a != "" {
			aliasInfo = a // the alias's launcher, if any, gets its own Command line
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
					if launcher.IsReservedEntry(ldir, n) {
						continue // the CLI's own reserved symlink is not this playbook's launcher
					}
					if _, exists, foreign := launcher.Lookup(ldir, n); exists && !foreign {
						fmt.Printf("Command:  %s (%s)\n", n, launcherFate(ldir, n, pb.Name).prompt)
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
	removeUnclaimedLaunchers(names, pb.Name)
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
	removeUnclaimedLaunchers([]string{name}, name)
	fmt.Printf("Deleted %q.\n", name)
	return nil
}

// removeUnclaimedLaunchers retires the launchers named for a playbook that
// is going away: a name another playbook still claims (by spelling, or by
// directory-entry identity on a case-insensitive filesystem) is kept, one
// whose ownership cannot be verified is kept with a warning, and every
// other launcher is removed, receipt line included. See launcherFate.
func removeUnclaimedLaunchers(names []string, playbookName string) {
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
		if launcher.IsReservedEntry(dir, n) {
			continue // a reserved name, or the CLI's own symlink under another spelling
		}
		e, exists, foreign := launcher.Lookup(dir, n)
		if !exists || foreign {
			continue
		}
		fate := launcherFate(dir, n, playbookName)
		switch fate.action {
		case fateClaimed:
			fmt.Printf("Kept command %q (still addresses playbook %q)\n", n, fate.owner)
		case fateUnknown:
			fmt.Fprintf(os.Stderr, "Warning: kept command %q: cannot verify whether another playbook claims it: %v\n", n, fate.err)
		default:
			removed, rerr := launcher.Remove(dir, n)
			if rerr != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not remove launcher %q (%s): %v\n", n, e.Path, rerr)
				continue
			}
			if removed {
				fmt.Printf("Removed command %q\n", n)
			}
		}
	}
}

// launcherClaimedByIdentity reports the name of another playbook whose own
// command name resolves to the same directory entry as dir/n, "" when
// none. Same-spelled names are the registry's business (commandNameOwner);
// this catches the spellings a case-insensitive filesystem folds together.
func launcherClaimedByIdentity(dir, n, exceptName string) (string, error) {
	mine, err := os.Lstat(filepath.Join(dir, n))
	if err != nil {
		return "", nil // no link to protect
	}
	pbs, err := playbook.Discover(config.ResolvePlaybooksDir())
	if err != nil {
		return "", err
	}
	for _, pb := range pbs {
		if pb.Name == exceptName {
			continue
		}
		for _, name := range launcherNamesFor(pb) {
			if name == n {
				continue
			}
			if theirs, err := os.Lstat(filepath.Join(dir, name)); err == nil && os.SameFile(mine, theirs) {
				return pb.Name, nil
			}
		}
	}
	return "", nil
}

type launcherAction int

const (
	fateRemove  launcherAction = iota // nobody else claims the name: removed
	fateClaimed                       // another playbook still claims the name: kept silently
	fateUnknown                       // ownership could not be verified: kept, warning
)

type launcherPlan struct {
	action launcherAction
	owner  string // the claiming playbook, for fateClaimed
	err    error  // the discovery failure, for fateUnknown
	prompt string // the confirmation-prompt phrasing
}

// launcherFate decides what delete (or rename, for a name left behind) does
// with the launcher dir/n once playbookName is gone, so the confirmation
// prompt and the action itself cannot disagree. A launcher is a stateless
// symlink and this tool only ever writes launchers for the default
// registry root (launcherOpsAllowed), so a launcher named for a playbook
// leaving that root is that playbook's, unless another playbook claims the
// name. Discovery failing is not "unclaimed": the launcher is kept.
func launcherFate(dir, n, playbookName string) launcherPlan {
	owner, oerr := commandNameOwner(n, playbookName)
	if oerr != nil {
		return launcherPlan{action: fateUnknown, err: oerr, prompt: "launcher kept; ownership could not be verified"}
	}
	if owner != nil {
		return launcherPlan{action: fateClaimed, owner: owner.Name, prompt: fmt.Sprintf("launcher kept; still addresses playbook %q", owner.Name)}
	}
	// The registry compares names literally, but a case-insensitive
	// filesystem makes "Foo" and "foo" one directory entry: a launcher
	// another playbook addresses under a differently-cased name is that
	// playbook's link too, and must not go with this one.
	if other, oerr := launcherClaimedByIdentity(dir, n, playbookName); oerr != nil {
		return launcherPlan{action: fateUnknown, err: oerr, prompt: "launcher kept; ownership could not be verified"}
	} else if other != "" {
		return launcherPlan{action: fateClaimed, owner: other, prompt: fmt.Sprintf("launcher kept; still addresses playbook %q", other)}
	}
	return launcherPlan{action: fateRemove, prompt: "launcher will be removed"}
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
// The store is protected along its whole resolution, exactly as the kernel
// resolves it, one component at a time: the registry entry, every symlink
// met on the way (in the final component or in any parent component,
// `.env-profiles -> .bridge/profiles` with `.bridge -> .leftover/sub`), and
// the final physical directory. Two shapes are refused, both judged by file
// identity (os.SameFile), never by spelling, so a case variant on a
// case-insensitive filesystem, a relative playbooks root, or a symlink on
// either side changes nothing:
//
//   - the path IS one of those elements (Lstat identity, so a leftover
//     symlink that merely points at the store is still deletable: only the
//     link goes, see removeAny). A separate hard link to the entry shares
//     that identity and is refused on the safe side.
//   - the path is a real directory whose subtree contains one of those
//     elements, which RemoveAll would descend into.
//
// A store whose resolution cannot be established (a link loop, a chain the
// kernel refuses, an unreadable component) refuses the delete outright
// rather than trusting a partial picture. Callers run the check before the
// prompt and again under the registry lock against the path actually
// removed, since the store may appear, move, or be linked while the prompt
// is open.
func refuseRegistryOwned(playbooksDir, name, path string) error {
	store := envprofile.Dir(playbooksDir)
	refuse := func() error {
		return fmt.Errorf("%q is the registry's env profile store, not a playbook; remove a profile with 'claude-playbook env-profile <name> delete'", envprofile.DirName)
	}
	cannotVerify := func(err error) error {
		return fmt.Errorf("cannot verify that deleting %q leaves the registry's env profile store intact (%s: %v); nothing removed", name, store, err)
	}
	if name == envprofile.DirName {
		return refuse()
	}
	a, err := os.Lstat(path)
	if err != nil {
		return nil // nothing at the path: nothing the removal could take
	}
	// The registry entry itself, by Lstat identity: a directory, or the
	// symlink the registry keeps when the store lives elsewhere, dangling or
	// not. Catches a case variant of the name on a case-insensitive
	// filesystem. Only an ABSENT entry means there is nothing to protect.
	entry, err := os.Lstat(store)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return cannotVerify(err)
	}
	if os.SameFile(entry, a) {
		return refuse()
	}
	// The kernel's verdict first: a chain it refuses (ELOOP on a long but
	// acyclic chain, a file used as a directory in `file/../dir`) is not
	// one the component walk below may accept on its own.
	physicalInfo, err := os.Stat(store)
	if err != nil {
		return cannotVerify(err)
	}
	physical, links, traversed, err := storeResolution(store)
	if err != nil {
		return cannotVerify(err) // a dangling or looping chain: unverifiable, so refused
	}
	if pi, err := os.Stat(physical); err != nil || !os.SameFile(pi, physicalInfo) {
		return cannotVerify(fmt.Errorf("component resolution disagrees with the kernel"))
	}
	if os.SameFile(a, physicalInfo) {
		return refuse() // the store's physical directory under another name
	}
	for _, link := range links {
		if li, err := os.Lstat(link); err == nil && os.SameFile(li, a) {
			return fmt.Errorf("%q is a link the registry's env profile store resolves through (%s -> %s); the store would become unreachable. Repoint %s first", name, store, link, store)
		}
	}
	if !a.IsDir() {
		// A symlink or a file: removal never descends. The entry may still
		// be the store's own, a profile file or the default marker, when
		// the store resolves to the directory holding it (`.env-profiles
		// -> .`); those are `env-profile <name> delete`'s business, with
		// its reference and default checks. Only entries the store itself
		// would read count: a linked playbook or a stray file beside them
		// is not the store's, and stays deletable.
		if name == envprofile.DefaultMarker || strings.HasSuffix(name, envprofile.FileExt) {
			if parent, err := os.Stat(filepath.Dir(path)); err == nil && os.SameFile(parent, physicalInfo) {
				return fmt.Errorf("%q is an entry of the registry's env profile store (%s resolves to the directory holding it); remove a profile with 'claude-playbook env-profile <name> delete'", name, store)
			}
		}
		return nil
	}
	// A real directory: refuse when it contains any element (its ancestors
	// are walked from the parent up), or when it is a traversed directory
	// itself (entered and left through `..`; not an ancestor of the store,
	// yet required by the kernel to resolve it).
	elements := append(append([]string{}, links...), traversed...)
	for _, elem := range elements {
		for dir := filepath.Dir(elem); ; dir = filepath.Dir(dir) {
			if di, err := os.Stat(dir); err == nil && os.SameFile(di, a) {
				return fmt.Errorf("%q contains the registry's env profile store or a path it resolves through (%s -> %s); move the store out or remove profiles with 'claude-playbook env-profile <name> delete' first", name, store, elem)
			}
			if filepath.Dir(dir) == dir {
				break
			}
		}
	}
	for _, dir := range traversed {
		if di, err := os.Stat(dir); err == nil && os.SameFile(di, a) {
			return fmt.Errorf("%q is a directory the registry's env profile store resolves through (%s -> %s); the store would become unreachable. Repoint %s first", name, store, dir, store)
		}
	}
	// Finally the subtree itself: a protected element can sit INSIDE the
	// directory without being a path descendant of it (a bind mount on
	// Linux: `/data` mounted at <dir>/mounted with the store under /data),
	// and RemoveAll would walk straight into it. Descend the way RemoveAll
	// does, without following symlinks, and compare identities.
	protected := make([]os.FileInfo, 0, len(elements))
	protectedPath := make([]string, 0, len(elements))
	for _, elem := range elements {
		if fi, err := os.Lstat(elem); err == nil {
			protected = append(protected, fi)
			protectedPath = append(protectedPath, elem)
		}
	}
	hit, err := subtreeHolds(path, protected)
	if err != nil {
		return cannotVerify(err)
	}
	if hit >= 0 {
		return fmt.Errorf("%q contains the registry's env profile store or a path it resolves through (%s -> %s); move the store out or remove profiles with 'claude-playbook env-profile <name> delete' first", name, store, protectedPath[hit])
	}
	return nil
}

// subtreeHolds walks dir the way os.RemoveAll would, without following
// symlinks, and reports the index of the first protected info whose file
// identity an entry matches (-1 when none). An unreadable entry is an
// error: the walk cannot vouch for what it could not see.
func subtreeHolds(dir string, protected []os.FileInfo) (int, error) {
	hit := -1
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if p == dir {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		for i, pi := range protected {
			if os.SameFile(fi, pi) {
				hit = i
				return filepath.SkipAll
			}
		}
		return nil
	})
	return hit, err
}

// storeResolution resolves store the way the kernel does, one path
// component at a time, and records every symlink met on the way, in any
// component, plus every real directory traversed (a directory entered and
// then left through `..` is still required for the kernel to resolve the
// path). Relative link targets are resolved against the directory the link
// lives in (already physical at that point, so `..` behaves as the
// kernel's does). Returns the physical path, the links, the traversed
// directories (the physical path last), or an error when resolution does
// not converge within 255 hops (a loop) or a component cannot be inspected
// (a dangling link).
func storeResolution(store string) (physical string, links, traversed []string, err error) {
	split := func(p string) []string {
		p = strings.TrimPrefix(p, filepath.VolumeName(p))
		return strings.Split(strings.Trim(p, string(filepath.Separator)), string(filepath.Separator))
	}
	var cur string
	if filepath.IsAbs(store) {
		cur = filepath.VolumeName(store) + string(filepath.Separator)
	} else {
		// A relative store resolves from the PHYSICAL working directory,
		// as the kernel resolves it. filepath.Abs would use the logical
		// $PWD and collapse `..` lexically: after `cd /a/link` with
		// `/a/link -> /b/sub`, `..` means /b to the kernel and /a to Abs.
		wd, err := os.Getwd()
		if err != nil {
			return "", nil, nil, err
		}
		if cur, err = filepath.EvalSymlinks(wd); err != nil {
			return "", nil, nil, err
		}
	}
	rest := split(store)
	hops := 0
	for len(rest) > 0 {
		comp := rest[0]
		rest = rest[1:]
		switch comp {
		case "", ".":
			continue
		case "..":
			cur = filepath.Dir(cur)
			continue
		}
		next := filepath.Join(cur, comp)
		fi, err := os.Lstat(next)
		if err != nil {
			return "", nil, nil, err
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			if !fi.IsDir() && len(rest) > 0 {
				return "", nil, nil, fmt.Errorf("%s: not a directory", next)
			}
			traversed = append(traversed, next)
			cur = next
			continue
		}
		hops++
		if hops > 255 {
			return "", nil, nil, fmt.Errorf("too many levels of symbolic links")
		}
		links = append(links, next)
		target, err := os.Readlink(next)
		if err != nil {
			return "", nil, nil, err
		}
		if filepath.IsAbs(target) {
			cur = filepath.VolumeName(target) + string(filepath.Separator)
		}
		rest = append(split(target), rest...)
	}
	if len(traversed) == 0 || traversed[len(traversed)-1] != cur {
		traversed = append(traversed, cur)
	}
	return cur, links, traversed, nil
}
