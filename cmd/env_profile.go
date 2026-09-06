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

var envProfileCmd = &cobra.Command{
	Use:     "env-profile [name] [set KEY=VALUE... | unset KEY... | clear KEY... | describe TEXT | default | undefault | delete]",
	Short:   "Show or manage shared env profiles",
	Aliases: []string{"envprofile"},
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
  default            make this the registry default: applied under every
                     playbook's own block, the bottom layer above the shell
  undefault          clear the registry default (only if it is this profile)
  delete             remove the profile; refused while a playbook uses it
                     or while it is the registry default`,
	Args: cobra.ArbitraryArgs,
	RunE: runEnvProfile,
}

func runEnvProfile(cmd *cobra.Command, args []string) error {
	playbooksDir := config.ResolvePlaybooksDir()
	dir := envprofile.Dir(playbooksDir)

	if len(args) == 0 {
		profiles, err := envprofile.List(dir)
		if err != nil {
			return err
		}
		// The default is reported even when it is broken: a marker naming a
		// missing profile, or one that cannot be read, refuses every launch,
		// and the listing is where a pilot looks first.
		defaultName, derr := envprofile.Default(dir)
		defaultFound := false
		for _, p := range profiles {
			if isRegistryDefault(dir, defaultName, p.Name) {
				defaultFound = true
			}
		}
		if len(profiles) == 0 {
			fmt.Println("No env profiles defined.")
			fmt.Println("Create one with 'claude-playbook env-profile <name> set KEY=VALUE', then 'claude-playbook env <playbook> use <name>'.")
			reportDefaultProblem(defaultName, derr, defaultFound)
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
			if isRegistryDefault(dir, defaultName, p.Name) {
				summary += "; registry default"
			}
			if u := users[p.Name]; len(u) > 0 {
				summary += "; used by " + strings.Join(u, ", ")
			}
			if p.Description != "" {
				summary = p.Description + " (" + summary + ")"
			}
			fmt.Printf("%-*s  %s\n", maxLen, p.Name, summary)
		}
		reportDefaultProblem(defaultName, derr, defaultFound)
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
			return fmt.Errorf("unknown env profile %q. Create it with 'claude-playbook env-profile %s set KEY=VALUE'", name, name)
		}
		fmt.Printf("Env profile %q", name)
		if p.Description != "" {
			fmt.Printf(": %s", p.Description)
		}
		fmt.Println()
		if d, err := envprofile.Default(dir); err != nil {
			fmt.Printf("Registry default marker is invalid (%v): every launch is refused until 'claude-playbook env-profile %s undefault' clears it.\n", err, name)
		} else if isRegistryDefault(dir, d, name) {
			fmt.Println("Registry default: applied under every playbook's own block.")
		}
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
	case "delete", "default", "undefault":
		if len(rest) != 0 {
			return fmt.Errorf("%s takes no further arguments", verb)
		}
	default:
		return fmt.Errorf("unknown action %q: expected set, unset, clear, describe, default, undefault, or delete\nUsage: claude-playbook env-profile <name> [set KEY=VALUE... | unset KEY... | clear KEY... | describe TEXT | default | undefault | delete]", verb)
	}

	unlock, lerr := lockRegistry()
	if lerr != nil {
		return lerr
	}
	defer unlock()

	// undefault must work even when the profile file is unreadable: that is
	// exactly the situation in which every launch is refused and the pilot
	// needs to clear the marker. Only the marker is consulted.
	if verb == "undefault" {
		current, err := envprofile.Default(dir)
		if err != nil {
			// The marker cannot name any profile (empty, invalid, dangling):
			// there is no default it could belong to, and every launch is
			// refused until it goes. Clearing it is what the pilot asked for.
			if cerr := envprofile.ClearDefault(dir); cerr != nil {
				return fmt.Errorf("%v; and it could not be cleared: %w", err, cerr)
			}
			fmt.Printf("Registry default marker was invalid (%v); cleared. No registry default is set.\n", err)
			return nil
		}
		if current != name && !envprofile.SameProfile(dir, current, name) {
			if current == "" {
				return fmt.Errorf("no registry default is set")
			}
			return fmt.Errorf("the registry default is %q, not %q", current, name)
		}
		if err := envprofile.ClearDefault(dir); err != nil {
			return err
		}
		fmt.Printf("Env profile %q is no longer the registry default.\n", name)
		return nil
	}

	p, err := envprofile.Read(dir, name)
	if err != nil {
		return err
	}

	switch verb {
	case "default":
		if p == nil {
			return fmt.Errorf("unknown env profile %q. Create it with 'claude-playbook env-profile %s set KEY=VALUE'", name, name)
		}
		if err := envprofile.SetDefault(dir, name); err != nil {
			return err
		}
		fmt.Printf("Env profile %q is now the registry default: every playbook launch layers it under its own block.\n", name)
		return nil
	case "delete":
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
		// The default is compared by FILE identity, not spelling: on a
		// case-insensitive filesystem "BASE" and "base" are one profile. An
		// unreadable marker refuses the delete: the profile may still be the
		// one every launch depends on.
		d, err := envprofile.Default(dir)
		if err != nil {
			return fmt.Errorf("cannot determine the registry default: %w", err)
		}
		if isRegistryDefault(dir, d, name) {
			return fmt.Errorf("env profile %q is the registry default; clear it first with 'claude-playbook env-profile %s undefault'", name, d)
		}
		if err := envprofile.Delete(dir, name); err != nil {
			return err
		}
		fmt.Printf("Deleted env profile %q\n", name)
		return nil
	}

	if p == nil {
		if verb != "set" {
			return fmt.Errorf("unknown env profile %q. Create it with 'claude-playbook env-profile %s set KEY=VALUE'", name, name)
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

// isRegistryDefault reports whether name is the registry default defaultName,
// by spelling or by file identity (one file, two spellings, on a
// case-insensitive filesystem). An unset default matches nothing.
func isRegistryDefault(dir, defaultName, name string) bool {
	if defaultName == "" {
		return false
	}
	return defaultName == name || envprofile.SameProfile(dir, defaultName, name)
}

// reportDefaultProblem prints, after a listing, the state that refuses every
// launch: a marker that cannot be read, or one naming a profile that does not
// exist. Nothing is printed when the default is absent or healthy.
func reportDefaultProblem(defaultName string, derr error, found bool) {
	switch {
	case derr != nil:
		fmt.Printf("Registry default marker is invalid (%v): every launch is refused until 'claude-playbook env-profile <name> undefault' clears it.\n", derr)
	case defaultName != "" && !found:
		fmt.Printf("Registry default %q names no profile: every launch is refused until 'claude-playbook env-profile %s set KEY=VALUE' creates it or 'claude-playbook env-profile %s undefault' clears it.\n", defaultName, defaultName, defaultName)
	}
}
