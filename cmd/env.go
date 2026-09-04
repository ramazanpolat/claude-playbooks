package cmd

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/auth"
	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

var envCmd = &cobra.Command{
	Use:   "env [name] [set KEY=VALUE... | unset KEY... | clear KEY...]",
	Short: "Show or manage a playbook's environment overrides",
	Long: `A playbook can declare environment variables in the [env] block of its
.playbook manifest. Every launch of that playbook (its launcher command,
'run', or 'start' at its directory) applies them to the child claude
process: 'set' entries override whatever the shell exported, 'unset'
entries are removed even when the shell exports them.

With no arguments: list every playbook that declares overrides.
With a name: show that playbook's overrides.
  set KEY=VALUE...   record values (replacing any previous ones)
  unset KEY...       remove the variables from every launch
  clear KEY...       forget the entries; the shell's values apply again

Unsetting CLAUDE_CODE_OAUTH_TOKEN switches the playbook to stored
credentials: the machine-global long-lived token is neither injected nor
allowed to quarantine the playbook's own login, so /login sticks there.

The block is install-local. 'update' keeps it and ignores the source's;
'install' drops one the source ships. CLAUDE_CONFIG_DIR cannot be
overridden.`,
	Args:              cobra.ArbitraryArgs,
	ValidArgsFunction: autocompletePlaybookNames,
	RunE:              runEnv,
}

func runEnv(cmd *cobra.Command, args []string) error {
	playbooksDir := config.ResolvePlaybooksDir()

	if len(args) == 0 {
		pbs, err := playbook.Discover(playbooksDir)
		if err != nil {
			return err
		}
		shown := 0
		for _, pb := range pbs {
			if pb.Manifest == nil || pb.Manifest.Env.Empty() {
				continue
			}
			fmt.Printf("%s\n", pb.Name)
			printEnvBlock("  ", pb.Manifest.Env)
			shown++
		}
		if shown == 0 {
			fmt.Println("No playbook declares environment overrides.")
			fmt.Println("Use 'claude-playbook env <name> set KEY=VALUE' or 'claude-playbook env <name> unset KEY' to add some.")
		}
		return nil
	}

	name := args[0]
	if len(args) == 1 {
		pb, err := playbook.Require(playbooksDir, name)
		if err != nil {
			return err
		}
		if pb.Manifest == nil || pb.Manifest.Env.Empty() {
			fmt.Printf("Playbook %q declares no environment overrides.\n", name)
			fmt.Printf("Use 'claude-playbook env %s set KEY=VALUE' or 'claude-playbook env %s unset KEY' to add some.\n", name, name)
			return nil
		}
		fmt.Printf("Environment overrides for %q:\n", name)
		printEnvBlock("  ", pb.Manifest.Env)
		return nil
	}

	verb := args[1]
	keys := args[2:]
	switch verb {
	case "set", "unset", "clear":
	default:
		return fmt.Errorf("unknown action %q: expected set, unset, or clear\nUsage: claude-playbook env <name> [set KEY=VALUE... | unset KEY... | clear KEY...]", verb)
	}
	if len(keys) == 0 {
		if verb == "set" {
			return fmt.Errorf("%s requires at least one KEY=VALUE", verb)
		}
		return fmt.Errorf("%s requires at least one KEY", verb)
	}

	// Parse and validate everything BEFORE locking or touching the manifest:
	// a bad third argument must not leave the first two applied.
	set := map[string]string{}
	var names []string
	for _, arg := range keys {
		key, value := arg, ""
		if verb == "set" {
			k, v, ok := strings.Cut(arg, "=")
			if !ok {
				return fmt.Errorf("set expects KEY=VALUE, got %q", arg)
			}
			key, value = k, v
		} else if strings.Contains(arg, "=") {
			return fmt.Errorf("%s expects a variable name, got %q", verb, arg)
		}
		if err := manifest.ValidateEnvKey(key); err != nil {
			return err
		}
		set[key] = value
		names = append(names, key)
	}

	// Manifest mutations serialize under the registry lock like alias does:
	// two concurrent edits would otherwise race on the read-modify-write.
	unlock, lerr := lockRegistry()
	if lerr != nil {
		return lerr
	}
	defer unlock()

	pb, err := playbook.Require(playbooksDir, name)
	if err != nil {
		return err
	}
	// A linked playbook's manifest is shared with every registration of the
	// target directory -- same refusal as alias and rename.
	if info, lerr := os.Lstat(pb.RootPath); lerr == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("cannot change environment overrides on a linked target's shared %s. Edit the target's manifest directly if you really mean it", manifest.FileName)
	}

	m := pb.Manifest
	if m == nil {
		m = &manifest.Manifest{Name: pb.Name}
	}
	if m.Env == nil {
		m.Env = &manifest.Env{}
	}
	if m.Env.Set == nil {
		m.Env.Set = map[string]string{}
	}
	for _, key := range names {
		m.Env.Unset = dropString(m.Env.Unset, key)
		delete(m.Env.Set, key)
		switch verb {
		case "set":
			m.Env.Set[key] = set[key]
		case "unset":
			m.Env.Unset = append(m.Env.Unset, key)
		}
	}
	if m.Env.Empty() {
		m.Env = nil
	}
	if err := manifest.Write(pb.RootPath, m); err != nil {
		return fmt.Errorf("cannot record environment overrides: %w", err)
	}

	switch verb {
	case "set":
		for _, key := range names {
			fmt.Printf("Set %s for playbook %q\n", key, name)
		}
	case "unset":
		for _, key := range names {
			fmt.Printf("Unset %s for playbook %q\n", key, name)
		}
	case "clear":
		for _, key := range names {
			fmt.Printf("Cleared %s for playbook %q (the shell's value applies again)\n", key, name)
		}
	}
	for _, key := range names {
		if key == auth.OAuthTokenEnv && verb == "unset" {
			fmt.Printf("Playbook %q now authenticates from stored credentials: the long-lived token is not injected and its login is left alone. Run it and /login once if it asks.\n", name)
		}
	}
	return nil
}

func printEnvBlock(indent string, e *manifest.Env) {
	keys := make([]string, 0, len(e.Set))
	for key := range e.Set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Printf("%sset    %s=%s\n", indent, key, e.Set[key])
	}
	unset := append([]string(nil), e.Unset...)
	sort.Strings(unset)
	for _, key := range unset {
		fmt.Printf("%sunset  %s\n", indent, key)
	}
}

func dropString(list []string, s string) []string {
	out := list[:0:0]
	for _, v := range list {
		if v != s {
			out = append(out, v)
		}
	}
	return out
}
