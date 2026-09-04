package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envprofile"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

var profileCmd = &cobra.Command{
	Use:   "profile [name] [set KEY=VALUE... | unset KEY... | clear KEY... | describe TEXT | delete]",
	Short: "Show or manage shared env profiles",
	Long: `An env profile is a named, reusable set of environment overrides stored
under <playbooks root>/.env-profiles/<name>.toml. Playbooks opt in with
'claude-playbook env <playbook> use <profile>'; at launch the profiles apply
in the order listed, then the playbook's own [env] entries on top.

With no arguments: list every profile.
With a name: show that profile and which playbooks use it.
  set KEY=VALUE...   record values (creates the profile on first use)
  unset KEY...       remove the variables from every launch using the profile
  clear KEY...       forget the entries
  describe TEXT      set the one-line description
  delete             remove the profile; refused while a playbook uses it`,
	Args: cobra.ArbitraryArgs,
	RunE: runProfile,
}

func runProfile(cmd *cobra.Command, args []string) error {
	playbooksDir := config.ResolvePlaybooksDir()
	dir := envprofile.Dir(playbooksDir)

	if len(args) == 0 {
		profiles, err := envprofile.List(dir)
		if err != nil {
			return err
		}
		if len(profiles) == 0 {
			fmt.Println("No env profiles defined.")
			fmt.Println("Create one with 'claude-playbook profile <name> set KEY=VALUE', then 'claude-playbook env <playbook> use <name>'.")
			return nil
		}
		users, err := profileUsers(playbooksDir)
		if err != nil {
			return err
		}
		maxLen := 0
		for _, p := range profiles {
			if len(p.Name) > maxLen {
				maxLen = len(p.Name)
			}
		}
		for _, p := range profiles {
			summary := fmt.Sprintf("%d set, %d unset", len(p.Set), len(p.Unset))
			if u := users[p.Name]; len(u) > 0 {
				summary += "; used by " + strings.Join(u, ", ")
			}
			if p.Description != "" {
				summary = p.Description + " (" + summary + ")"
			}
			fmt.Printf("%-*s  %s\n", maxLen, p.Name, summary)
		}
		return nil
	}

	name := args[0]
	if err := manifest.ValidateProfileName(name); err != nil {
		return err
	}

	if len(args) == 1 {
		p, err := envprofile.Read(dir, name)
		if err != nil {
			return err
		}
		if p == nil {
			return fmt.Errorf("unknown env profile %q. Create it with 'claude-playbook profile %s set KEY=VALUE'", name, name)
		}
		fmt.Printf("Env profile %q", name)
		if p.Description != "" {
			fmt.Printf(": %s", p.Description)
		}
		fmt.Println()
		printEnvBlock("  ", p.Env())
		users, err := profileUsers(playbooksDir)
		if err != nil {
			return err
		}
		if u := users[name]; len(u) > 0 {
			fmt.Printf("Used by: %s\n", strings.Join(u, ", "))
		} else {
			fmt.Printf("Used by no playbook. Attach it with 'claude-playbook env <playbook> use %s'.\n", name)
		}
		return nil
	}

	verb := args[1]
	rest := args[2:]
	var description string
	set := map[string]string{}
	var keys []string
	switch verb {
	case "set", "unset", "clear":
		if len(rest) == 0 {
			if verb == "set" {
				return fmt.Errorf("set requires at least one KEY=VALUE")
			}
			return fmt.Errorf("%s requires at least one KEY", verb)
		}
		for _, arg := range rest {
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
			keys = append(keys, key)
		}
	case "describe":
		if len(rest) == 0 {
			return fmt.Errorf("describe requires the description text")
		}
		description = strings.Join(rest, " ")
	case "delete":
		if len(rest) != 0 {
			return fmt.Errorf("delete takes no further arguments")
		}
	default:
		return fmt.Errorf("unknown action %q: expected set, unset, clear, describe, or delete\nUsage: claude-playbook profile <name> [set KEY=VALUE... | unset KEY... | clear KEY... | describe TEXT | delete]", verb)
	}

	unlock, lerr := lockRegistry()
	if lerr != nil {
		return lerr
	}
	defer unlock()

	p, err := envprofile.Read(dir, name)
	if err != nil {
		return err
	}

	if verb == "delete" {
		if p == nil {
			return fmt.Errorf("unknown env profile %q", name)
		}
		users, err := profileUsers(playbooksDir)
		if err != nil {
			return err
		}
		if u := users[name]; len(u) > 0 {
			return fmt.Errorf("env profile %q is used by %s; detach it first with 'claude-playbook env <playbook> unuse %s'", name, strings.Join(u, ", "), name)
		}
		if err := envprofile.Delete(dir, name); err != nil {
			return err
		}
		fmt.Printf("Deleted env profile %q\n", name)
		return nil
	}

	if p == nil {
		if verb != "set" {
			return fmt.Errorf("unknown env profile %q. Create it with 'claude-playbook profile %s set KEY=VALUE'", name, name)
		}
		p = &envprofile.Profile{Name: name}
	}
	if p.Set == nil {
		p.Set = map[string]string{}
	}
	if verb == "describe" {
		p.Description = description
	}
	for _, key := range keys {
		p.Unset = dropString(p.Unset, key)
		delete(p.Set, key)
		switch verb {
		case "set":
			p.Set[key] = set[key]
		case "unset":
			p.Unset = append(p.Unset, key)
		}
	}
	if err := envprofile.Write(dir, p); err != nil {
		return fmt.Errorf("cannot write env profile: %w", err)
	}
	switch verb {
	case "set":
		for _, key := range keys {
			fmt.Printf("Set %s in env profile %q\n", key, name)
		}
	case "unset":
		for _, key := range keys {
			fmt.Printf("Unset %s in env profile %q\n", key, name)
		}
	case "clear":
		for _, key := range keys {
			fmt.Printf("Cleared %s from env profile %q\n", key, name)
		}
	case "describe":
		fmt.Printf("Described env profile %q\n", name)
	}
	return nil
}

// profileUsers maps profile name to the sorted playbooks referencing it.
func profileUsers(playbooksDir string) (map[string][]string, error) {
	pbs, err := playbook.Discover(playbooksDir)
	if err != nil {
		return nil, err
	}
	users := map[string][]string{}
	for _, pb := range pbs {
		if pb.Manifest == nil || pb.Manifest.Env == nil {
			continue
		}
		for _, name := range pb.Manifest.Env.Profiles {
			users[name] = append(users[name], pb.Name)
		}
	}
	for name := range users {
		sort.Strings(users[name])
	}
	return users, nil
}
