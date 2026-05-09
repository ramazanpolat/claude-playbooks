package cmd

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/auth"
	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/shell"
)

var (
	installName     string
	installSubdir   string
	installBranch   string
	installAlias    string
	installAliasAll bool
	installNoAlias  bool
	installInit     bool
)

var installCmd = &cobra.Command{
	Use:   "install <source>",
	Short: "Install a playbook from a Git URL or local directory",
	Args:  cobra.ExactArgs(1),
	RunE:  runInstall,
}

func init() {
	installCmd.Flags().StringVar(&installName, "name", "", "directory name under the playbooks root")
	installCmd.Flags().StringVar(&installSubdir, "subdir", "", "cherry-pick: install only this subdirectory of the source")
	installCmd.Flags().StringVar(&installBranch, "branch", "", "Git URL only: clone this ref instead of the default branch")
	installCmd.Flags().StringVar(&installAlias, "alias", "", "alias for the installed top-level playbook")
	installCmd.Flags().BoolVar(&installAliasAll, "alias-all", false, "tree install only: also write aliases for declared children")
	installCmd.Flags().BoolVar(&installNoAlias, "no-alias", false, "skip alias creation entirely")
	installCmd.Flags().BoolVar(&installInit, "init", false, "if source has no .playbook, write a minimal one at the install destination")
}

func runInstall(cmd *cobra.Command, args []string) error {
	if installNoAlias && (installAlias != "" || installAliasAll) {
		return fmt.Errorf("--no-alias cannot be combined with --alias or --alias-all")
	}
	if installAlias != "" && installAliasAll {
		return fmt.Errorf("--alias and --alias-all cannot be used together")
	}

	source := args[0]

	// Parse GitHub /tree/<ref>/<path> URLs into source + branch + subdir.
	repoURL, parsedRef, parsedSubdir, parsed := parseGitTreeURL(source)
	if parsed {
		source = repoURL
		if installBranch == "" {
			installBranch = parsedRef
		}
		if installSubdir == "" {
			installSubdir = parsedSubdir
		}
	}

	subdir := strings.TrimPrefix(strings.TrimSuffix(installSubdir, "/"), "/")
	cherryPick := subdir != ""

	playbooksDir := config.ResolvePlaybooksDir()
	if err := os.MkdirAll(playbooksDir, 0755); err != nil {
		return err
	}

	isGit := isGitURL(source)

	// Stage 1: place the source tree in a working area so we can read its
	// .playbook before choosing a final name.
	work, cleanup, err := stageSource(source, isGit, installBranch, subdir)
	if err != nil {
		return err
	}
	defer cleanup()

	// Stage 2: pick the target name. Order: --name, manifest's name, then a
	// fallback derived from the source.
	targetName := installName
	if targetName == "" {
		if mPre, _ := manifest.Read(work); mPre != nil && mPre.Name != "" {
			targetName = mPre.Name
		}
	}
	if targetName == "" {
		if cherryPick {
			targetName = lastSegmentOfPath(subdir)
		} else if isGit {
			targetName = deriveNameFromURL(source)
		} else {
			targetName = deriveNameFromLocal(source)
		}
	}
	if targetName == "" {
		return fmt.Errorf("could not derive name from source; use --name")
	}
	dest := filepath.Join(playbooksDir, targetName)
	if _, err := os.Lstat(dest); err == nil {
		return fmt.Errorf("%q already exists at %s. Use --name to choose a different name", targetName, dest)
	}

	// Stage 3: move staged tree to its final destination.
	if err := copyDir(work, dest); err != nil {
		os.RemoveAll(dest)
		return fmt.Errorf("failed to copy from staging: %w", err)
	}

	if err := auth.SyncCredentials(dest); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to sync credentials: %v\n", err)
	}

	// Find / require / write a .playbook at the install destination.
	m, err := manifest.Read(dest)
	if err != nil {
		os.RemoveAll(dest)
		return err
	}
	if m == nil {
		if !installInit {
			os.RemoveAll(dest)
			return fmt.Errorf("%s has no .playbook. Add one to the source, or pass --init", dest)
		}
		if err := manifest.WriteMinimal(dest, targetName); err != nil {
			os.RemoveAll(dest)
			return fmt.Errorf("failed to write minimal .playbook: %w", err)
		}
		// Re-read so downstream resolution sees the freshly written file.
		m, _ = manifest.Read(dest)
	}

	// Validate children for tree install. Cherry-pick installs are always flat.
	if !cherryPick && m != nil {
		for _, c := range m.Children {
			childPath := filepath.Join(dest, c.Path)
			info, err := os.Stat(childPath)
			if err != nil || !info.IsDir() {
				os.RemoveAll(dest)
				return fmt.Errorf("child %q path %q not found", c.Name, c.Path)
			}
		}
	}

	if cherryPick {
		fmt.Printf("Installed %q at %s\n", targetName, dest)
	} else {
		childCount := 0
		if m != nil {
			childCount = len(m.Children)
		}
		fmt.Printf("Installed %q at %s\n", targetName, dest)
		if childCount == 0 {
			fmt.Println("1 playbook (no children).")
		} else {
			fmt.Printf("1 root playbook + %d child%s:\n", childCount, childPlural(childCount))
		}
	}

	// CLAUDE.md warnings.
	warnIfNoClaudeMD(dest, targetName)
	if !cherryPick && m != nil {
		for _, c := range m.Children {
			cp := filepath.Join(dest, c.Path)
			warnIfNoClaudeMD(cp, targetName+"/"+c.Name)
		}
	}

	// Alias handling.
	if installNoAlias {
		fmt.Printf("\nRun with:\n  claude-playbook run %s\n", targetName)
		return nil
	}

	shellConfig, err := config.ResolveShellConfig()
	if err != nil {
		return err
	}

	// Always write the root alias unless --no-alias.
	rootAlias := installAlias
	if rootAlias == "" {
		switch {
		case m != nil && m.Alias != "":
			rootAlias = m.Alias
		case m != nil && m.Name != "":
			rootAlias = m.Name
		default:
			rootAlias = targetName
		}
	}

	existing, _ := shell.ReadAll(shellConfig)
	taken := map[string]bool{}
	for _, e := range existing {
		taken[e.AliasName] = true
	}

	if writeAlias(shellConfig, rootAlias, dest, taken) {
		fmt.Printf("Alias:    %s → %s\n", rootAlias, targetName)
	} else {
		fmt.Fprintf(os.Stderr, "Warning: alias %q already in use; skipped. Set one manually with 'claude-playbook alias %s <alias>'\n", rootAlias, targetName)
	}

	// Children aliases (tree install only) when --alias-all is set.
	if !cherryPick && installAliasAll && m != nil {
		for _, c := range m.Children {
			if c.Alias == "" {
				continue
			}
			childPath := filepath.Join(dest, c.Path)
			childName := targetName + "/" + c.Name
			alias := c.Alias
			if taken[alias] {
				prefixed := targetName + "-" + alias
				if taken[prefixed] {
					fmt.Fprintf(os.Stderr, "Warning: alias %q (and fallback %q) already in use for %s; skipped\n", alias, prefixed, childName)
					continue
				}
				alias = prefixed
			}
			if writeAlias(shellConfig, alias, childPath, taken) {
				fmt.Printf("Alias:    %s → %s\n", alias, childName)
			}
		}
	} else if !cherryPick && m != nil && len(m.Children) > 0 && !installAliasAll {
		fmt.Println()
		fmt.Println("Children installed without aliases. Pass --alias-all on next install, or:")
		for _, c := range m.Children {
			suggestion := c.Alias
			if suggestion == "" {
				suggestion = c.Name
			}
			fmt.Printf("  claude-playbook alias %s/%s %s\n", targetName, c.Name, suggestion)
		}
	}

	fmt.Printf("\nReload your shell or run:\n  source %s\n", shellConfig)
	return nil
}

func writeAlias(shellConfig, name, path string, taken map[string]bool) bool {
	if taken[name] {
		return false
	}
	if err := shell.Write(shellConfig, name, path); err != nil {
		return false
	}
	taken[name] = true
	return true
}

func warnIfNoClaudeMD(dir, name string) {
	if _, err := os.Stat(filepath.Join(dir, "CLAUDE.md")); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Warning: %s has no CLAUDE.md\n", name)
	}
}

// stageSource fetches the source into a working directory and returns its
// path. For Git URLs it clones into a temp dir; for local paths it returns
// the resolved source directory directly. The cleanup func removes any temp
// state created.
func stageSource(source string, isGit bool, ref, subdir string) (string, func(), error) {
	if isGit {
		if _, err := exec.LookPath("git"); err != nil {
			return "", func() {}, fmt.Errorf("'git' command not found")
		}
		tmp, err := os.MkdirTemp("", "claude-playbook-clone-")
		if err != nil {
			return "", func() {}, err
		}
		cleanup := func() { os.RemoveAll(tmp) }

		args := []string{"clone", "--depth=1"}
		if ref != "" {
			args = append(args, "--branch", ref)
		}
		args = append(args, source, tmp)

		fmt.Printf("Cloning %s", source)
		if ref != "" {
			fmt.Printf(" (branch %s)", ref)
		}
		if subdir != "" {
			fmt.Printf(" (subdir %s)", subdir)
		}
		fmt.Println("...")

		gitCmd := exec.Command("git", args...)
		gitCmd.Stdout = os.Stdout
		gitCmd.Stderr = os.Stderr
		if err := gitCmd.Run(); err != nil {
			cleanup()
			return "", func() {}, fmt.Errorf("git clone failed")
		}

		work := tmp
		if subdir != "" {
			work = filepath.Join(tmp, filepath.FromSlash(subdir))
			info, err := os.Stat(work)
			if err != nil || !info.IsDir() {
				cleanup()
				return "", func() {}, fmt.Errorf("subdir %q not found in source", subdir)
			}
		}
		return work, cleanup, nil
	}

	if ref != "" {
		return "", func() {}, fmt.Errorf("--branch only applies to Git URLs")
	}
	abs, err := filepath.Abs(source)
	if err != nil {
		return "", func() {}, err
	}
	info, err := os.Stat(abs)
	if os.IsNotExist(err) {
		return "", func() {}, fmt.Errorf("%q not found", source)
	}
	if err != nil {
		return "", func() {}, err
	}
	if !info.IsDir() {
		return "", func() {}, fmt.Errorf("%q is not a directory", source)
	}
	work := abs
	if subdir != "" {
		work = filepath.Join(abs, filepath.FromSlash(subdir))
		info, err := os.Stat(work)
		if err != nil || !info.IsDir() {
			return "", func() {}, fmt.Errorf("subdir %q not found in source", subdir)
		}
	}
	return work, func() {}, nil
}

func deriveNameFromLocal(source string) string {
	abs, err := filepath.Abs(source)
	if err != nil {
		return ""
	}
	return filepath.Base(strings.TrimRight(abs, string(filepath.Separator)))
}

// parseGitTreeURL recognises GitHub /tree/<ref>/<path...> URLs and returns
// (clone-url, ref, subdir, true). For other URLs returns ("","","",false).
func parseGitTreeURL(s string) (string, string, string, bool) {
	u, err := url.Parse(s)
	if err != nil {
		return "", "", "", false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", "", "", false
	}
	if u.Host != "github.com" && u.Host != "www.github.com" {
		return "", "", "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 4 || parts[2] != "tree" {
		return "", "", "", false
	}
	owner := parts[0]
	repo := parts[1]
	ref := parts[3]
	sub := ""
	if len(parts) > 4 {
		sub = path.Join(parts[4:]...)
	}
	clone := fmt.Sprintf("https://%s/%s/%s", u.Host, owner, repo)
	return clone, ref, sub, true
}

func isGitURL(s string) bool {
	return strings.HasPrefix(s, "http://") ||
		strings.HasPrefix(s, "https://") ||
		strings.HasPrefix(s, "git@") ||
		strings.HasPrefix(s, "git://")
}

func deriveNameFromURL(source string) string {
	source = strings.TrimRight(source, "/")
	name := filepath.Base(source)
	name = strings.TrimSuffix(name, ".git")
	return name
}

func lastSegmentOfPath(p string) string {
	p = strings.TrimSuffix(p, "/")
	i := strings.LastIndex(p, "/")
	if i < 0 {
		return p
	}
	return p[i+1:]
}

func childPlural(n int) string {
	if n == 1 {
		return ""
	}
	return "ren"
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// copyDir recursively copies the tree rooted at src into dst.
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
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
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
