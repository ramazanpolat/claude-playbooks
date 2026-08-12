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
)

var (
	installName    string
	installSubdir  string
	installBranch  string
	installAlias   string
	installNoAlias bool
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
	installCmd.Flags().StringVar(&installAlias, "alias", "", "alias for the installed playbook")
	installCmd.Flags().BoolVar(&installNoAlias, "no-alias", false, "skip alias creation entirely")
}

func runInstall(cmd *cobra.Command, args []string) error {
	if installNoAlias && installAlias != "" {
		return fmt.Errorf("--no-alias and --alias cannot be used together")
	}

	source := args[0]

	// Parse GitHub /tree/<ref>/<path> URLs into source + branch + subdir.
	repoURL, parsedRef, parsedSubdir, parsed := parseGitTreeURLWithRef(source, installBranch)
	if parsed {
		source = repoURL
		if installBranch == "" {
			treePath := path.Join(parsedRef, parsedSubdir)
			if resolvedRef, resolvedSubdir, ok := resolveRemoteTreeRef(repoURL, treePath); ok {
				parsedRef = resolvedRef
				parsedSubdir = resolvedSubdir
			}
		}
		if installBranch == "" {
			installBranch = parsedRef
		}
		if installSubdir == "" {
			installSubdir = parsedSubdir
		}
	}

	subdir := strings.Trim(installSubdir, "/")
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
	mPre, err := manifest.Read(work)
	if err != nil {
		return err
	}

	// Stage 2: pick the target name. Order: --name, manifest's name, then a
	// fallback derived from the source.
	targetName := installName
	if targetName == "" {
		if mPre != nil && mPre.Name != "" {
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
	if err := validateTopLevelName("install name", targetName); err != nil {
		return err
	}
	dest := filepath.Join(playbooksDir, targetName)
	if _, err := os.Lstat(dest); err == nil {
		return fmt.Errorf("%q already exists at %s. Use --name to choose a different name", targetName, dest)
	}
	// Preflight command names BEFORE the directory joins the registry:
	// dispatch resolves directory names ahead of aliases, so a clash would
	// silently re-route an existing command. The target name joins the
	// registry even under --no-alias, and an imported manifest's alias
	// registers without any flag.
	effectiveAlias := installAlias
	if effectiveAlias == "" && mPre != nil {
		effectiveAlias = mPre.Alias
	}
	if err := preflightCommandNames("", targetName, effectiveAlias); err != nil {
		return err
	}

	// Read the optional manifest from staging to check if we need to cherry-pick a subdir.
	copySrc := work
	hasManifestSubdir := mPre != nil && mPre.Subdir != ""
	if hasManifestSubdir {
		copySrc, err = manifest.ResolveSubdir(work, "subdir", mPre.Subdir)
		if err != nil {
			return err
		}
	}

	// Stage 3: move staged tree to its final destination.
	if err := copyDir(copySrc, dest); err != nil {
		os.RemoveAll(dest)
		return fmt.Errorf("failed to copy from staging: %w", err)
	}

	needsManifestWrite := false
	if mPre == nil {
		mPre = &manifest.Manifest{}
	}
	sourceSubdir := subdir
	if hasManifestSubdir {
		sourceSubdir = path.Join(sourceSubdir, mPre.Subdir)
		mPre.Subdir = ""
		needsManifestWrite = true
	}
	if isGit {
		updateScript := ""
		if mPre.Source != nil {
			updateScript = mPre.Source.UpdateScript
		}
		mPre.Source = &manifest.Source{
			Repository:   source,
			Branch:       installBranch,
			Subdir:       sourceSubdir,
			UpdateScript: updateScript,
		}
		needsManifestWrite = true
	}
	// The installed manifest's name always mirrors the top-level install
	// directory: the source's name only seeds the default targetName above.
	// Side-by-side installs of the same source stay distinguishable by name.
	if mPre.Name != targetName {
		mPre.Name = targetName
		needsManifestWrite = true
	}
	if needsManifestWrite {
		if err := manifest.Write(dest, mPre); err != nil {
			os.RemoveAll(dest)
			return fmt.Errorf("failed to write manifest: %w", err)
		}
	}

	// Read the optional .playbook at the install destination. A missing
	// manifest is fine: the installed directory is a valid flat playbook.
	m, err := manifest.Read(dest)
	if err != nil {
		os.RemoveAll(dest)
		return err
	}
	configDest := dest
	if m != nil && m.Subdir != "" {
		configDest, err = manifest.ResolveSubdir(dest, "subdir", m.Subdir)
		if err != nil {
			os.RemoveAll(dest)
			return err
		}
	}

	if err := auth.SyncCredentials(configDest); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to sync credentials: %v\n", err)
	}

	fmt.Printf("Installed %q at %s\n", targetName, dest)

	// CLAUDE.md warning.
	warnIfNoClaudeMD(configDest, targetName)

	// Alias handling.
	if installNoAlias {
		fmt.Printf("\nRun with:\n  claude-playbook run %s\n", targetName)
		return nil
	}

	// Pick the command name: --alias, manifest's alias, manifest's name,
	// then the install directory name.
	aliasName := installAlias
	if aliasName == "" {
		switch {
		case m != nil && m.Alias != "":
			aliasName = m.Alias
		case m != nil && m.Name != "":
			aliasName = m.Name
		default:
			aliasName = targetName
		}
	}

	// A custom command name must be resolvable at invocation time: record
	// it as the manifest alias so multicall dispatch finds the playbook.
	if installAlias != "" && (m == nil || m.Alias != installAlias) {
		if m == nil {
			m = &manifest.Manifest{Version: "0.1.0", Name: targetName}
		}
		m.Alias = installAlias
		if err := manifest.Write(dest, m); err != nil {
			// Without the manifest entry the alias can never resolve.
			return fmt.Errorf("cannot record alias %q in manifest (required for the command to resolve): %w", installAlias, err)
		}
	}

	installLauncher(aliasName, targetName, configDest)
	return nil
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
			work, err = manifest.ResolveSubdir(tmp, "source.subdir", subdir)
			if err != nil {
				cleanup()
				return "", func() {}, err
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
		work, err = manifest.ResolveSubdir(abs, "source.subdir", subdir)
		if err != nil {
			return "", func() {}, err
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
	return parseGitTreeURLWithRef(s, "")
}

func parseGitTreeURLWithRef(s, preferredRef string) (string, string, string, bool) {
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
	treePath := path.Join(parts[3:]...)
	ref, sub := splitTreePath(treePath, preferredRef)
	if ref == "" {
		return "", "", "", false
	}
	clone := fmt.Sprintf("https://%s/%s/%s", u.Host, owner, repo)
	return clone, ref, sub, true
}

func splitTreePath(treePath, preferredRef string) (string, string) {
	if preferredRef != "" && (treePath == preferredRef || strings.HasPrefix(treePath, preferredRef+"/")) {
		return preferredRef, strings.TrimPrefix(strings.TrimPrefix(treePath, preferredRef), "/")
	}
	parts := strings.SplitN(treePath, "/", 2)
	if len(parts) == 1 {
		return parts[0], ""
	}
	return parts[0], parts[1]
}

func resolveRemoteTreeRef(repoURL, treePath string) (string, string, bool) {
	out, err := exec.Command("git", "ls-remote", "--heads", "--tags", repoURL).Output()
	if err != nil {
		return "", "", false
	}
	var refs []string
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.HasSuffix(fields[1], "^{}") {
			continue
		}
		ref := strings.TrimPrefix(fields[1], "refs/heads/")
		ref = strings.TrimPrefix(ref, "refs/tags/")
		refs = append(refs, ref)
	}
	return splitTreePathByRefs(treePath, refs)
}

func splitTreePathByRefs(treePath string, refs []string) (string, string, bool) {
	best := ""
	for _, ref := range refs {
		if (treePath == ref || strings.HasPrefix(treePath, ref+"/")) && len(ref) > len(best) {
			best = ref
		}
	}
	if best == "" {
		return "", "", false
	}
	return best, strings.TrimPrefix(strings.TrimPrefix(treePath, best), "/"), true
}

func isGitURL(s string) bool {
	return strings.HasPrefix(s, "http://") ||
		strings.HasPrefix(s, "https://") ||
		strings.HasPrefix(s, "git@") ||
		strings.HasPrefix(s, "git://") ||
		strings.HasPrefix(s, "ssh://") ||
		strings.HasPrefix(s, "file://")
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

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// copyDir recursively copies the tree rooted at src into dst.
//
// Internal symlinks are dereferenced so installed playbooks are self-contained
// regular files/directories. Symlinks that resolve outside the source tree are
// preserved to avoid unexpectedly copying unrelated local data into an install.
func copyDir(src, dst string) error {
	root, err := filepath.EvalSymlinks(src)
	if err != nil {
		return err
	}
	return copyDirWithinRoot(root, dst, root, map[string]bool{root: true})
}

// visited holds resolved directories already being copied; a symlink that
// resolves to one of them would recurse forever, so it is preserved as-is.
func copyDirWithinRoot(src, dst, root string, visited map[string]bool) error {
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
			if targetInfo, statErr := os.Lstat(target); statErr == nil && (!targetInfo.IsDir() || targetInfo.Mode()&os.ModeSymlink != 0) {
				if err := removeAny(target); err != nil {
					return err
				}
			} else if statErr != nil && !os.IsNotExist(statErr) {
				return statErr
			}
			if err := os.MkdirAll(target, info.Mode()); err != nil {
				return err
			}
			return os.Chmod(target, info.Mode().Perm())
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if resolved, err := filepath.EvalSymlinks(path); err == nil {
				if targetInfo, err := os.Stat(resolved); err == nil && pathWithin(root, resolved) {
					if targetInfo.IsDir() {
						if visited[resolved] {
							if err := removeAny(target); err != nil {
								return err
							}
							return os.Symlink(link, target)
						}
						visited[resolved] = true
						return copyDirWithinRoot(resolved, target, root, visited)
					}
					return copyFile(resolved, target, targetInfo.Mode())
				}
			}
			if err := removeAny(target); err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		return copyFile(path, target, info.Mode())
	})
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if info, err := os.Lstat(dst); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		if err := removeAny(dst); err != nil {
			return err
		}
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode.Perm())
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	return out.Chmod(mode.Perm())
}
