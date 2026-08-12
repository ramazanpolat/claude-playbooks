package cmd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/launcher"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
	"github.com/ramazanpolat/claude-playbooks/internal/shell"
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
	shellConfig, err := config.ResolveShellConfig()
	if err != nil {
		return err
	}
	playbooksDir := config.ResolvePlaybooksDir()

	if err := validateSinglePathSegment("playbook name", name); err != nil {
		return err
	}

	// Discovery and confirmation run on an UNLOCKED snapshot: the prompt
	// waits on human input, and holding the machine-user-global registry
	// lock there would block every concurrent command indefinitely.
	pb, err := playbook.Find(playbooksDir, shellConfig, name)
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
		return deleteOrphan(playbooksDir, shellConfig, name, path)
	}

	if !deleteYes {
		aliasInfo := "(no alias)"
		if pb.HasAlias() {
			aliasInfo = fmt.Sprintf("%s (will be removed from %s)", pb.Alias, shellConfig)
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
	pb, err = playbook.Find(playbooksDir, shellConfig, name)
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
	names := launcherNamesFor(pb)
	if _, err := shell.RemoveByPathPrefix(shellConfig, deletePath); err != nil {
		return fmt.Errorf("failed to clean up aliases: %w", err)
	}
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
func deleteOrphan(playbooksDir, shellConfig, name, path string) error {
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
	if pb, _ := playbook.Find(playbooksDir, shellConfig, name); pb != nil {
		return fmt.Errorf("%q became a discoverable playbook while waiting for confirmation; re-run delete", name)
	}
	if _, err := shell.RemoveByPathPrefix(shellConfig, path); err != nil {
		return fmt.Errorf("failed to clean up aliases: %w", err)
	}
	if err := removeAny(path); err != nil {
		return fmt.Errorf("failed to delete %s: %w", path, err)
	}
	removeUnclaimedLaunchers([]string{name})
	fmt.Printf("Deleted %q.\n", name)
	return nil
}

// removeUnclaimedLaunchers deletes the launcher symlinks for the given
// command names — but only names that no longer resolve in the registry
// AFTER the deletion: a stateless symlink may be serving another playbook
// that claims the same name (and, unenumerably, another registry root — the
// re-resolution keeps every claim we can actually see working). Best-effort:
// the playbook removal must not fail on launcher-dir trouble, and foreign
// files are never touched.
func removeUnclaimedLaunchers(names []string) {
	if !launcherOpsAllowed() {
		fmt.Fprintf(os.Stderr, "Note: launchers are managed only for the default playbooks root; none removed.\n")
		return
	}
	dir, err := config.ResolveLauncherDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not clean up launchers: %v\n", err)
		return
	}
	for _, n := range names {
		if owner, oerr := commandNameOwner(n, ""); oerr == nil && owner != nil {
			fmt.Printf("Kept command %q (still addresses playbook %q)\n", n, owner.Name)
			continue
		}
		removed, err := launcher.Remove(dir, n)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not remove launcher %q: %v\n", n, err)
		} else if removed {
			fmt.Printf("Removed command %q\n", n)
		}
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
