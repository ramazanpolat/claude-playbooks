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
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/shell"
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

func runLink(cmd *cobra.Command, args []string) error {
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
	if strings.HasPrefix(name, ".") {
		return fmt.Errorf("link name cannot start with '.'")
	}

	dest := filepath.Join(playbooksDir, name)
	if _, err := os.Lstat(dest); err == nil {
		return fmt.Errorf("%q already exists at %s. Use --name to choose a different name", name, dest)
	}

	// Ensure target has a .playbook, prompting interactively if it doesn't.
	if !manifest.Exists(abs) {
		m, err := promptForManifest(abs, name)
		if err != nil {
			return err
		}
		if err := manifest.Write(abs, m); err != nil {
			return fmt.Errorf("failed to write .playbook to %s: %w", abs, err)
		}
		fmt.Printf("Wrote %s\n", filepath.Join(abs, manifest.FileName))
	}

	if err := auth.SyncCredentials(abs); err != nil {
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
		if m, _ := manifest.Read(abs); m != nil && m.Alias != "" {
			aliasName = m.Alias
		} else {
			aliasName = name
		}
	}

	shellConfig, err := config.ResolveShellConfig()
	if err != nil {
		return err
	}
	if err := shell.Write(shellConfig, aliasName, dest); err != nil {
		return fmt.Errorf("failed to write alias: %w", err)
	}
	fmt.Printf("Alias %q added to %s\n", aliasName, shellConfig)
	fmt.Printf("\nReload your shell or run:\n  source %s\n\nThen run with:\n  %s\n", shellConfig, aliasName)
	return nil
}

func promptForManifest(targetDir, defaultName string) (*manifest.Manifest, error) {
	if !isTTY(os.Stdin) {
		return nil, fmt.Errorf("target has no .playbook and stdin is not a TTY; cannot prompt for metadata. Add a .playbook to the target first")
	}

	fmt.Printf("Target %s has no .playbook file.\n", targetDir)
	fmt.Println("This will write a .playbook into the target directory.")
	fmt.Println()

	reader := bufio.NewReader(os.Stdin)
	name := promptDefault(reader, "Playbook name", defaultName)
	alias := promptDefault(reader, "Alias name", name)
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
