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
	"github.com/ramazanpolat/claude-playbooks/internal/shell"
)

var (
	installName     string
	installAlias    string
	installNoAlias  bool
	installCopy     bool
	installPlaybook string
	installSubdir   string
)

var installCmd = &cobra.Command{
	Use:   "install <source>",
	Short: "Install a playbook from a git repo or local directory",
	Args:  cobra.ExactArgs(1),
	RunE:  runInstall,
}

func init() {
	installCmd.Flags().StringVar(&installName, "name", "", "playbook name (default: derived from source)")
	installCmd.Flags().StringVar(&installAlias, "alias", "", "alias name (default: same as name)")
	installCmd.Flags().BoolVar(&installNoAlias, "no-alias", false, "skip alias creation")
	installCmd.Flags().BoolVar(&installCopy, "copy", false, "copy instead of symlink (local paths only)")
	installCmd.Flags().StringVar(&installPlaybook, "playbook", "", "select a playbook entry by name from .playbook")
	installCmd.Flags().StringVar(&installSubdir, "subdir", "", "use this subdirectory as the playbook root")
}

func runInstall(cmd *cobra.Command, args []string) error {
	if installNoAlias && installAlias != "" {
		return fmt.Errorf("--no-alias and --alias cannot be used together")
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

	var sourceDir string
	var tempDir string
	var installMethod string // "cloned", "symlinked", "copied"

	if isGit {
		// For git: clone to a temp dir first to read manifest, then decide final location.
		var err error
		tempDir, err = os.MkdirTemp("", "claude-playbook-*")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tempDir)

		fmt.Printf("Cloning %s...\n", source)
		gitCmd := exec.Command("git", "clone", "--depth=1", source, tempDir)
		gitCmd.Stdout = os.Stdout
		gitCmd.Stderr = os.Stderr
		if err := gitCmd.Run(); err != nil {
			return fmt.Errorf("git clone failed")
		}
		sourceDir = tempDir
	} else {
		// Local path.
		abs, err := filepath.Abs(source)
		if err != nil {
			return err
		}
		info, err := os.Stat(abs)
		if os.IsNotExist(err) {
			return fmt.Errorf("'%s' not found", source)
		}
		if !info.IsDir() {
			return fmt.Errorf("'%s' is not a directory", source)
		}
		sourceDir = abs
	}

	// Resolve manifest.
	m, err := manifest.Resolve(sourceDir, installPlaybook)
	if err != nil {
		return err
	}

	// Determine subdir (CLI > manifest > none).
	subdir := installSubdir
	if subdir == "" && m != nil && m.Subdir != "" {
		subdir = m.Subdir
	}

	// Determine playbook root within source.
	playbookRoot := sourceDir
	if subdir != "" {
		playbookRoot = filepath.Join(sourceDir, subdir)
		if _, err := os.Stat(playbookRoot); os.IsNotExist(err) {
			return fmt.Errorf("subdirectory '%s' not found in source", subdir)
		}
	}

	// Determine name (CLI > manifest > derived).
	name := installName
	if name == "" && m != nil && m.Name != "" {
		name = m.Name
	}
	if name == "" {
		name = deriveName(source)
	}

	// Check name not already taken.
	dest := filepath.Join(playbooksDir, name)
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("playbook %q already exists. Use --name to choose a different name", name)
	}

	// Install.
	if isGit {
		// For git: if no subdir, clone directly to dest. If subdir, clone to hidden src dir + symlink subdir.
		if subdir == "" {
			srcDir := tempDir
			// Move temp clone to dest.
			if err := os.Rename(srcDir, dest); err != nil {
				// Rename may fail across filesystems; fall back to copy.
				if err := copyDir(srcDir, dest); err != nil {
					return fmt.Errorf("failed to install: %w", err)
				}
			}
			tempDir = "" // Don't clean up — it's now the dest.
		} else {
			// Clone to hidden source dir, symlink the subdir.
			hiddenSrc := filepath.Join(playbooksDir, "."+name+"-src")
			if err := os.Rename(tempDir, hiddenSrc); err != nil {
				if err := copyDir(tempDir, hiddenSrc); err != nil {
					return fmt.Errorf("failed to install source: %w", err)
				}
			}
			tempDir = ""
			symlinkTarget := filepath.Join(hiddenSrc, subdir)
			if err := os.Symlink(symlinkTarget, dest); err != nil {
				return fmt.Errorf("failed to create symlink: %w", err)
			}
		}
		installMethod = "cloned"
	} else {
		// Local: symlink or copy.
		target := playbookRoot
		if installCopy {
			if err := copyDir(target, dest); err != nil {
				return fmt.Errorf("failed to copy: %w", err)
			}
			installMethod = "copied"
		} else {
			if err := os.Symlink(target, dest); err != nil {
				return fmt.Errorf("failed to create symlink: %w", err)
			}
			installMethod = "symlinked"
		}
	}

	// Warn if no CLAUDE.md.
	if _, err := os.Stat(filepath.Join(dest, "CLAUDE.md")); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Warning: no CLAUDE.md found. Any directory is valid, but CLAUDE.md is how Claude Code loads your playbook's instructions.\n")
	}

	// Determine alias (CLI > manifest > name).
	aliasName := installAlias
	if aliasName == "" && m != nil && m.Alias != "" {
		aliasName = m.Alias
	}
	if aliasName == "" {
		aliasName = name
	}

	// Write alias.
	fmt.Printf("\nInstalled playbook %q\n", name)
	fmt.Printf("Source:   %s (%s)\n", source, installMethod)
	if m != nil {
		fmt.Printf("Manifest: .playbook\n")
	}
	fmt.Printf("Path:     %s\n", dest)

	if installNoAlias {
		fmt.Printf("\nRun with:\n  claude-playbook run %s\n", name)
		return nil
	}

	shellConfig, err := config.ResolveShellConfig()
	if err != nil {
		return err
	}
	if err := shell.Write(shellConfig, name, aliasName, dest); err != nil {
		return fmt.Errorf("failed to write alias: %w", err)
	}

	fmt.Printf("Alias:    %s added to %s\n", aliasName, shellConfig)
	fmt.Printf("\nReload your shell or run:\n  source %s\n\nThen run with:\n  %s\n", shellConfig, aliasName)
	return nil
}

func isGitURL(s string) bool {
	return strings.HasPrefix(s, "http://") ||
		strings.HasPrefix(s, "https://") ||
		strings.HasPrefix(s, "git@") ||
		strings.HasPrefix(s, "git://")
}

func deriveName(source string) string {
	// Strip trailing slash.
	source = strings.TrimRight(source, "/")
	// Take last path segment.
	name := filepath.Base(source)
	// Strip .git suffix.
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
