package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

var updateCmd = &cobra.Command{
	Use:                "update [name]",
	Short:              "Self-update the tool, or update a playbook via its bin/update-playbook.sh",
	DisableFlagParsing: true,
	RunE:               runUpdate,
}

func runUpdate(cmd *cobra.Command, args []string) error {
	// Strip root persistent flags that leak through DisableFlagParsing.
	var playbooksDir, shellConfigOverride string
	var rest []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--playbooks-dir" && i+1 < len(args):
			playbooksDir = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--playbooks-dir="):
			playbooksDir = strings.TrimPrefix(args[i], "--playbooks-dir=")
		case args[i] == "--shell-config" && i+1 < len(args):
			shellConfigOverride = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--shell-config="):
			shellConfigOverride = strings.TrimPrefix(args[i], "--shell-config=")
		case args[i] == "--help" || args[i] == "-h":
			printUpdateHelp()
			return nil
		default:
			rest = append(rest, args[i])
		}
	}
	if playbooksDir != "" {
		config.PlaybooksDir = playbooksDir
	}
	if shellConfigOverride != "" {
		config.ShellConfig = shellConfigOverride
	}

	if len(rest) == 0 {
		return runSelfUpdate()
	}

	name := rest[0]
	scriptArgs := rest[1:]
	return runPlaybookUpdate(name, scriptArgs)
}

func printUpdateHelp() {
	fmt.Println("Usage: claude-playbook update [name] [script-args...]")
	fmt.Println()
	fmt.Println("Without <name>: self-update the claude-playbook binary.")
	fmt.Println("With <name>: run <playbook-or-container>/bin/update-playbook.sh, forwarding extra args.")
}

func runSelfUpdate() error {
	// Minimal placeholder: direct users to the install script.
	fmt.Printf("Current version: %s\n", Version)
	fmt.Println()
	fmt.Println("Self-update is not yet implemented. To update, re-run:")
	fmt.Println("  curl -fsSL https://raw.githubusercontent.com/ramazanpolat/claude-playbooks/main/install.sh | sh")
	return nil
}

func runPlaybookUpdate(name string, scriptArgs []string) error {
	playbooksDir := config.ResolvePlaybooksDir()
	target, err := playbook.ResolveTarget(playbooksDir, name)
	if err != nil {
		return err
	}

	script := filepath.Join(target.Path, "bin", "update-playbook.sh")
	info, err := os.Stat(script)
	if err != nil {
		return fmt.Errorf("%q has no update script at bin/update-playbook.sh. This target does not support updates; see its documentation", name)
	}
	if info.Mode()&0111 == 0 {
		return fmt.Errorf("update script is not executable: %s", script)
	}

	c := exec.Command(script, scriptArgs...)
	c.Dir = target.Path
	c.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR="+target.Path)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			fmt.Fprintf(os.Stderr, "update-playbook.sh exited with code %d\n", exitErr.ExitCode())
			os.Exit(exitErr.ExitCode())
		}
		return err
	}
	return nil
}
