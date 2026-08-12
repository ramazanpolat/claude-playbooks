package cmd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/auth"
	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/launcher"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

var (
	linkName    string
	linkAlias   string
	linkNoAlias bool
)

var linkCmd = &cobra.Command{
	Use:   "link <target>",
	Short: "Symlink an external directory into the playbooks root",
	Args:  cobra.ExactArgs(1),
	RunE:  runLink,
}

func init() {
	linkCmd.Flags().StringVar(&linkName, "name", "", "name under the playbooks root (default: target's basename)")
	linkCmd.Flags().StringVar(&linkAlias, "alias", "", "alias name (default: link name)")
	linkCmd.Flags().BoolVar(&linkNoAlias, "no-alias", false, "skip alias creation")
}

func runLink(cmd *cobra.Command, args []string) (retErr error) {
	if linkNoAlias && linkAlias != "" {
		return fmt.Errorf("--no-alias and --alias cannot be used together")
	}

	target := args[0]
	abs, err := filepath.Abs(target)
	if err != nil {
		return fmt.Errorf("invalid target: %w", err)
	}

	info, err := os.Stat(abs)
	if os.IsNotExist(err) {
		return fmt.Errorf("%q not found", target)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%q is not a directory", target)
	}

	playbooksDir := config.ResolvePlaybooksDir()
	if err := os.MkdirAll(playbooksDir, 0755); err != nil {
		return err
	}

	name := linkName
	if name == "" {
		name = filepath.Base(abs)
	}
	if strings.Contains(name, "/") {
		return fmt.Errorf("link name may not contain '/'")
	}
	if err := validateTopLevelName("link name", name); err != nil {
		return err
	}

	dest := filepath.Join(playbooksDir, name)
	if _, err := os.Lstat(dest); err == nil {
		return fmt.Errorf("%q already exists at %s. Use --name to choose a different name", name, dest)
	}

	if linkAlias != "" {
		if err := launcher.ValidateName(linkAlias); err != nil {
			return err
		}
	}

	// Prompt for manifest metadata BEFORE taking the machine-user-global
	// lock: holding it across human think time would block every concurrent
	// command (same rule as delete's confirmation).
	var prompted *manifest.Manifest
	if !manifest.Exists(abs) {
		aliasDefault := name
		if linkAlias != "" {
			aliasDefault = linkAlias
		}
		var perr error
		prompted, perr = promptForManifest(abs, name, aliasDefault)
		if perr != nil {
			return perr
		}
		// An interactively entered alias becomes the manifest alias and the
		// launcher name — a reserved or path-like value would leave the
		// link registered with its advertised command unusable.
		if prompted.Alias != "" {
			if verr := launcher.ValidateName(prompted.Alias); verr != nil {
				return fmt.Errorf("prompted alias rejected: %w", verr)
			}
		}
	}

	// Serialize registration (see lockRegistry), then RE-CHECK the shared
	// manifest under the lock: a concurrent link may have initialized it
	// while the prompt was open — its manifest wins and ours is discarded,
	// falling through to the shared-state policy below.
	unlock, err := lockRegistry()
	if err != nil {
		return err
	}
	defer unlock()

	// A manifest created by THIS invocation is not shared state yet: alias
	// overrides may apply to it freely, and it must not survive a failed
	// link as litter.
	createdManifest := false
	if !manifest.Exists(abs) {
		if prompted == nil {
			return fmt.Errorf("target's %s disappeared while preparing the link; re-run", manifest.FileName)
		}
		if err := manifest.Write(abs, prompted); err != nil {
			return fmt.Errorf("failed to write .playbook to %s: %w", abs, err)
		}
		createdManifest = true
		defer func() {
			if retErr != nil {
				os.Remove(filepath.Join(abs, manifest.FileName))
			}
		}()
		fmt.Printf("Wrote %s\n", filepath.Join(abs, manifest.FileName))
	} else if prompted != nil {
		fmt.Fprintf(os.Stderr, "Note: target's %s was initialized concurrently; using it and discarding the prompted metadata.\n", manifest.FileName)
	}
	m, err := manifest.Read(abs)
	if err != nil {
		return err
	}
	configTarget := abs
	configDest := dest
	if m != nil && m.Subdir != "" {
		configTarget, err = manifest.ResolveSubdir(abs, "subdir", m.Subdir)
		if err != nil {
			return err
		}
		configDest = filepath.Join(dest, filepath.FromSlash(m.Subdir))
	}

	// Preflight command names BEFORE the symlink joins the registry (the
	// link name registers even under --no-alias, and the target manifest's
	// alias registers without any flag).
	effectiveAlias := linkAlias
	if effectiveAlias == "" && m != nil {
		effectiveAlias = m.Alias
	}
	if err := preflightCommandNames("", name, effectiveAlias); err != nil {
		return err
	}

	// A PRE-EXISTING target manifest is SHARED state: the same external
	// directory may already be linked from other registry roots whose
	// launchers resolve through it. Any differing alias mutation — changing
	// one, or adding one where none existed — could break or reroute those
	// registrations, so refuse unless this invocation created the manifest.
	if linkAlias != "" && !createdManifest && m != nil && m.Alias != linkAlias {
		return fmt.Errorf("target's %s is shared state (alias %q); --alias %q would mutate it for every registration of this target. Use the manifest's alias or edit the target's %s directly", manifest.FileName, m.Alias, linkAlias, manifest.FileName)
	}

	// For a manifest created by this invocation, the --alias flag wins over
	// whatever was typed at the prompt. Persist BEFORE the symlink joins
	// the registry: failing afterwards would leave the playbook registered
	// with an unresolvable advertised command.
	if linkAlias != "" && createdManifest && (m == nil || m.Alias != linkAlias) {
		if m == nil {
			m = &manifest.Manifest{Version: "0.1.0", Name: name}
		}
		m.Alias = linkAlias
		if err := manifest.Write(abs, m); err != nil {
			return fmt.Errorf("cannot record alias %q in %s (required for the command to resolve): %w", linkAlias, abs, err)
		}
	}

	if err := auth.SyncCredentials(configTarget); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to sync credentials: %v\n", err)
	}

	if err := os.Symlink(abs, dest); err != nil {
		return fmt.Errorf("failed to create symlink: %w", err)
	}
	fmt.Printf("Linked %s -> %s\n", dest, abs)

	if linkNoAlias {
		fmt.Printf("\nRun with:\n  claude-playbook run %s\n", name)
		return nil
	}

	aliasName := linkAlias
	if aliasName == "" {
		// Prefer alias from manifest if present, else the link name.
		if m != nil && m.Alias != "" {
			aliasName = m.Alias
		} else {
			aliasName = name
		}
	}

	installLauncher(aliasName, name, configDest)
	return nil
}

func promptForManifest(targetDir, defaultName, defaultAlias string) (*manifest.Manifest, error) {
	if !isTTY(os.Stdin) {
		return nil, fmt.Errorf("target has no .playbook and stdin is not a TTY; cannot prompt for metadata. Add a .playbook to the target first")
	}

	fmt.Printf("Target %s has no .playbook file.\n", targetDir)
	fmt.Println("This will write a .playbook into the target directory.")
	fmt.Println()

	reader := bufio.NewReader(os.Stdin)
	name := promptDefault(reader, "Playbook name", defaultName)
	alias := promptDefault(reader, "Alias name", defaultAlias)
	desc := promptDefault(reader, "Description", "")

	return &manifest.Manifest{
		Version:     "0.1.0",
		Name:        name,
		Alias:       alias,
		Description: desc,
	}, nil
}

func isTTY(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

func promptDefault(r *bufio.Reader, label, def string) string {
	if def != "" {
		fmt.Printf("%s [%s]: ", label, def)
	} else {
		fmt.Printf("%s []: ", label)
	}
	line, _ := r.ReadString('\n')
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return def
	}
	return line
}
