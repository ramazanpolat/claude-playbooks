package cmd

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
	"github.com/ramazanpolat/claude-playbooks/internal/shell"
)

var (
	installName     string
	installAlias    string
	installAliasAll bool
	installNoAlias  bool
	installCopy     bool
)

var installCmd = &cobra.Command{
	Use:   "install <source>",
	Short: "Install a playbook (or a repo of playbooks) from a git URL or local directory",
	Args:  cobra.ExactArgs(1),
	RunE:  runInstall,
}

func init() {
	installCmd.Flags().StringVar(&installName, "name", "", "target directory name under the playbooks root")
	installCmd.Flags().StringVar(&installAlias, "alias", "", "alias name (single-playbook installs only)")
	installCmd.Flags().BoolVar(&installAliasAll, "alias-all", false, "write one alias per discovered playbook (multi-playbook installs)")
	installCmd.Flags().BoolVar(&installNoAlias, "no-alias", false, "skip alias creation")
	installCmd.Flags().BoolVar(&installCopy, "copy", false, "copy instead of symlink (local paths only)")
}

func runInstall(cmd *cobra.Command, args []string) error {
	if installNoAlias && (installAlias != "" || installAliasAll) {
		return fmt.Errorf("--no-alias cannot be combined with --alias or --alias-all")
	}
	if installAlias != "" && installAliasAll {
		return fmt.Errorf("--alias and --alias-all cannot be used together")
	}

	source := args[0]
	isGit := isGitURL(source)

	if installCopy && isGit {
		return fmt.Errorf("--copy only applies to local paths. Git installs always clone")
	}

	playbooksDir := config.ResolvePlaybooksDir()
	if err := os.MkdirAll(playbooksDir, 0755); err != nil {
		return err
	}

	// Determine target directory name.
	targetName := installName
	if targetName == "" {
		targetName = deriveName(source)
	}
	if targetName == "" {
		return fmt.Errorf("could not derive name from source; use --name")
	}
	dest := filepath.Join(playbooksDir, targetName)
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("%q already exists at %s. Use --name to choose a different name", targetName, dest)
	}

	var installMethod string
	if isGit {
		fmt.Printf("Cloning %s...\n", source)
		gitCmd := exec.Command("git", "clone", "--depth=1", source, dest)
		gitCmd.Stdout = os.Stdout
		gitCmd.Stderr = os.Stderr
		if err := gitCmd.Run(); err != nil {
			os.RemoveAll(dest)
			return fmt.Errorf("git clone failed")
		}
		installMethod = "cloned"
	} else {
		abs, err := filepath.Abs(source)
		if err != nil {
			return err
		}
		info, err := os.Stat(abs)
		if os.IsNotExist(err) {
			return fmt.Errorf("%q not found", source)
		}
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("%q is not a directory", source)
		}
		if installCopy {
			if err := copyDir(abs, dest); err != nil {
				os.RemoveAll(dest)
				return fmt.Errorf("failed to copy: %w", err)
			}
			installMethod = "copied"
		} else {
			if err := os.Symlink(abs, dest); err != nil {
				return fmt.Errorf("failed to create symlink: %w", err)
			}
			installMethod = "symlinked"
		}
	}

	// Discover playbooks in the installed tree.
	pbs, err := playbook.DiscoverUnder(playbooksDir, targetName)
	if err != nil {
		return fmt.Errorf("failed to discover playbooks: %w", err)
	}

	// If none found, treat the install root as one playbook: write .playbook there.
	if len(pbs) == 0 {
		// For a symlink, we don't write into the user's source.
		if installMethod == "symlinked" {
			// Use the symlink target.
			target, _ := os.Readlink(dest)
			if err := manifest.WriteMinimal(target); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not write .playbook to %s: %v\n", target, err)
			}
		} else {
			if err := manifest.WriteMinimal(dest); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not write .playbook: %v\n", err)
			}
		}
		pbs = []string{targetName}
	}

	fmt.Printf("\nInstalled %q at %s\n", targetName, dest)
	fmt.Printf("Source:   %s (%s)\n", source, installMethod)
	fmt.Printf("Found %d playbook%s:\n", len(pbs), pluralS(len(pbs)))
	for _, n := range pbs {
		fmt.Printf("  %s\n", n)
	}

	// Warn about missing CLAUDE.md per playbook.
	for _, n := range pbs {
		if _, err := os.Stat(filepath.Join(playbooksDir, n, "CLAUDE.md")); os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "Warning: %s has no CLAUDE.md\n", n)
		}
	}

	// Alias handling.
	if installNoAlias {
		fmt.Printf("\nRun with:\n")
		for _, n := range pbs {
			fmt.Printf("  claude-playbook run %s\n", n)
		}
		return nil
	}

	shellConfig, err := config.ResolveShellConfig()
	if err != nil {
		return err
	}

	switch {
	case len(pbs) == 1:
		name := pbs[0]
		aliasName := installAlias
		if aliasName == "" {
			aliasName = defaultAliasFor(playbooksDir, name)
		}
		pbPath := filepath.Join(playbooksDir, name)
		if err := shell.Write(shellConfig, aliasName, pbPath); err != nil {
			return fmt.Errorf("failed to write alias: %w", err)
		}
		fmt.Printf("Alias:    %s added to %s\n", aliasName, shellConfig)
		fmt.Printf("\nReload your shell or run:\n  source %s\n\nThen run with:\n  %s\n", shellConfig, aliasName)

	case installAlias != "":
		fmt.Fprintf(os.Stderr, "\nWarning: --alias was ignored because the install produced %d playbooks. Use --alias-all or add aliases with 'claude-playbook alias'.\n", len(pbs))
		fmt.Println()
		fmt.Println("Add aliases with:")
		for _, n := range pbs {
			fmt.Printf("  claude-playbook alias %s %s\n", n, lastSegment(n))
		}

	case installAliasAll:
		// Resolve default aliases with collision handling.
		assigned, skipped := assignAliases(playbooksDir, shellConfig, pbs, targetName)
		for _, a := range assigned {
			if err := shell.Write(shellConfig, a.alias, a.path); err != nil {
				return fmt.Errorf("failed to write alias %q: %w", a.alias, err)
			}
			fmt.Printf("Alias:    %s → %s\n", a.alias, a.name)
		}
		for _, s := range skipped {
			fmt.Fprintf(os.Stderr, "Warning: alias %q skipped for %s (%s)\n", s.alias, s.name, s.reason)
		}
		if len(assigned) > 0 {
			fmt.Printf("\nReload your shell or run:\n  source %s\n", shellConfig)
		}

	default:
		// Multi-playbook, no --alias-all: print next-step suggestions, write nothing.
		fmt.Println()
		fmt.Println("No aliases created. Add ones you want:")
		for _, n := range pbs {
			fmt.Printf("  claude-playbook alias %s %s\n", n, lastSegment(n))
		}
		fmt.Println()
		fmt.Println("Or run without an alias:")
		for _, n := range pbs {
			fmt.Printf("  claude-playbook run %s\n", n)
			break // just show one as an example
		}
	}

	return nil
}

type aliasAssignment struct {
	name  string // playbook name
	alias string // alias to write
	path  string // playbook path
}

type aliasSkip struct {
	name   string
	alias  string
	reason string
}

func assignAliases(playbooksDir, shellConfig string, pbs []string, containerName string) ([]aliasAssignment, []aliasSkip) {
	existing, _ := shell.ReadAll(shellConfig)
	takenInShell := map[string]bool{}
	for _, e := range existing {
		takenInShell[e.AliasName] = true
	}

	// First pass: desired alias per playbook.
	desired := make(map[string]string, len(pbs))
	for _, n := range pbs {
		desired[n] = defaultAliasFor(playbooksDir, n)
	}

	// Detect internal collisions (two playbooks wanting the same alias).
	count := map[string]int{}
	for _, a := range desired {
		count[a]++
	}
	for n, a := range desired {
		if count[a] > 1 {
			desired[n] = containerName + "-" + a
		}
	}

	var assigned []aliasAssignment
	var skipped []aliasSkip
	for _, n := range pbs {
		alias := desired[n]
		if takenInShell[alias] {
			skipped = append(skipped, aliasSkip{name: n, alias: alias, reason: "alias already in use"})
			continue
		}
		assigned = append(assigned, aliasAssignment{
			name:  n,
			alias: alias,
			path:  filepath.Join(playbooksDir, n),
		})
		takenInShell[alias] = true
	}
	return assigned, skipped
}

// defaultAliasFor returns the preferred alias for a playbook: manifest alias,
// else manifest name, else last segment of the playbook name.
func defaultAliasFor(playbooksDir, pbName string) string {
	path := filepath.Join(playbooksDir, pbName)
	m, _ := manifest.Read(path)
	if m != nil {
		if m.Alias != "" {
			return m.Alias
		}
		if m.Name != "" {
			return m.Name
		}
	}
	return lastSegment(pbName)
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func isGitURL(s string) bool {
	return strings.HasPrefix(s, "http://") ||
		strings.HasPrefix(s, "https://") ||
		strings.HasPrefix(s, "git@") ||
		strings.HasPrefix(s, "git://")
}

func deriveName(source string) string {
	source = strings.TrimRight(source, "/")
	name := filepath.Base(source)
	name = strings.TrimSuffix(name, ".git")
	return name
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		return copyFile(path, target, info.Mode())
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
