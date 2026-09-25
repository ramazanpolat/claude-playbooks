package cmd

import (
	"fmt"
	"os"
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

var envProfileValues bool

func init() {
	envProfileCmd.Flags().BoolVar(&revealSecrets, "reveal", false, "show credential-looking values instead of redacting them")
	envProfileCmd.Flags().BoolVar(&envProfileValues, "values", false, "expand each profile's sets and unsets instead of counting them")
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
		defaults, derr := envprofile.Defaults(dir)
		var missing []string
		for _, d := range defaults {
			found := false
			for _, p := range profiles {
				if isRegistryDefault(dir, []string{d}, p.Name) {
					found = true
				}
			}
			if !found {
				missing = append(missing, d)
			}
		}
		if len(profiles) == 0 {
			fmt.Println("No env profiles defined.")
			fmt.Println("Create one with 'claude-playbook env-profile <name> set KEY=VALUE', then 'claude-playbook env <playbook> use <name>'.")
			reportDefaultProblem(missing, derr)
			return nil
		}
		users, err := profileUsers(playbooksDir)
		if err != nil {
			return err
		}
		// One row per profile: identity, the two counts, who attaches it, and
		// the description LAST because it is prose and the only part that can
		// be clipped without losing a fact. The previous rendering glued all
		// four into a sentence -- "<description> (6 set, 0 unset; used by a,
		// b)" -- which nested parentheses inside descriptions that already had
		// their own, and wrapped on any normal terminal.
		t := newTable("NAME", "SET", "UNSET", "USED BY", "DESCRIPTION").flexible(4).rightAlign(1, 2)
		anyDefault := false
		for _, p := range profiles {
			name := p.Name
			if isRegistryDefault(dir, defaults, p.Name) {
				// A marker, not a word in a sentence: the eye finds a column.
				name += " *"
				anyDefault = true
			}
			used := strings.Join(users[p.Name], ", ")
			if used == "" {
				used = "-"
			}
			t.add(name, fmt.Sprintf("%d", len(p.Set)), fmt.Sprintf("%d", len(p.Unset)), used, p.Description)
		}
		t.render(os.Stdout)

		fmt.Println()
		if anyDefault {
			fmt.Println("* registry default: applied under every playbook's own block.")
		}
		if envProfileValues {
			printProfileValues(profiles, revealSecrets)
		} else {
			fmt.Printf("%d profile(s). 'claude-playbook env-profile --values' to list what each sets,\n", len(profiles))
			fmt.Println("or 'claude-playbook env-profile <name>' for one.")
		}
		reportDefaultProblem(missing, derr)
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
		if d, err := envprofile.Defaults(dir); err != nil {
			fmt.Printf("Registry default marker is invalid (%v): every launch is refused until 'claude-playbook env-profile %s undefault' clears it.\n", err, name)
		} else if isRegistryDefault(dir, d, name) {
			fmt.Println("Registry default: applied under every playbook's own block.")
		}
		printEnvBlock("  ", p.Env(), revealSecrets)
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
		current, err := envprofile.Defaults(dir)
		if err != nil {
			// The marker cannot name any profile (empty, invalid, dangling):
			// there is no default it could belong to, and every launch is
			// refused until it goes. Clearing it is what the pilot asked for.
			if cerr := envprofile.ClearDefaults(dir); cerr != nil {
				return fmt.Errorf("%v; and it could not be cleared: %w", err, cerr)
			}
			fmt.Printf("Registry default marker was invalid (%v); cleared. No registry default is set.\n", err)
			return nil
		}
		if !isRegistryDefault(dir, current, name) {
			if len(current) == 0 {
				return fmt.Errorf("no registry default is set")
			}
			return fmt.Errorf("env profile %q is not a registry default (the defaults are %s)", name, strings.Join(current, ", "))
		}
		// The marker may hold several defaults since the grammar made
		// DEFAULTS a list; undefault removes this one and keeps the rest.
		var rest []string
		for _, d := range current {
			if !isRegistryDefault(dir, []string{d}, name) {
				rest = append(rest, d)
			}
		}
		if err := envprofile.WriteDefaultsUnchecked(dir, rest); err != nil {
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
		// Replaces the whole list, as it replaced the single default before
		// DEFAULTS became a list.
		if err := envprofile.WriteDefaults(dir, []string{name}); err != nil {
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
		d, err := envprofile.Defaults(dir)
		if err != nil {
			return fmt.Errorf("cannot determine the registry default: %w", err)
		}
		if isRegistryDefault(dir, d, name) {
			// One default keeps the wording this command has always printed.
			if len(d) == 1 {
				return fmt.Errorf("env profile %q is the registry default; clear it first with 'claude-playbook env-profile %s undefault'", name, d[0])
			}
			return fmt.Errorf("env profile %q is a registry default; clear it first with 'claude-playbook env-profile %s undefault'", name, name)
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
		// A launch reads the governing manifest, which in a subdir layout
		// can be a nested one; the root manifest is what `env` edits. Both
		// count, so a set either one names is never deleted from under it.
		named := map[string]bool{}
		governing, _ := governingManifest(pb)
		for _, m := range []*manifest.Manifest{pb.Manifest, governing} {
			if m == nil || m.Env == nil {
				continue
			}
			for _, name := range m.Env.Profiles {
				if !named[name] {
					named[name] = true
					users[name] = append(users[name], pb.Name)
				}
			}
		}
	}
	for name := range users {
		sort.Strings(users[name])
	}
	return users, nil
}

// isRegistryDefault reports whether name is one of the registry defaults,
// by spelling or by file identity (one file, two spellings, on a
// case-insensitive filesystem). No defaults match nothing.
func isRegistryDefault(dir string, defaults []string, name string) bool {
	for _, d := range defaults {
		if d == name || envprofile.SameProfile(dir, d, name) {
			return true
		}
	}
	return false
}

// reportDefaultProblem prints, after a listing, the state that refuses every
// launch: a marker that cannot be read, or one naming a profile that does not
// exist. Nothing is printed when the default is absent or healthy.
func reportDefaultProblem(missing []string, derr error) {
	if derr != nil {
		fmt.Printf("Registry default marker is invalid (%v): every launch is refused until 'claude-playbook env-profile <name> undefault' clears it.\n", derr)
		return
	}
	for _, d := range missing {
		fmt.Printf("Registry default %q names no profile: every launch is refused until 'claude-playbook env-profile %s set KEY=VALUE' creates it or 'claude-playbook env-profile %s undefault' clears it.\n", d, d, d)
	}
}

// printProfileValues expands every profile under the table: description, then
// its keys aligned. VALUES ARE REDACTED for credential-shaped names unless
// --reveal is passed -- this view exists to answer "what does this profile
// set", which is a question about keys, and profiles are where credentials
// live (see manifest.LooksLikeSecretKey).
func printProfileValues(profiles []*envprofile.Profile, reveal bool) {
	if !reveal {
		masked := false
		for _, p := range profiles {
			for k := range p.Set {
				if looksLikeSecretKey(k) {
					masked = true
				}
			}
		}
		if masked {
			fmt.Println()
			fmt.Println("Credential values are shown as first…last; --reveal prints them in full.")
		}
	}
	for _, p := range profiles {
		fmt.Println()
		fmt.Printf("%s\n", p.Name)
		if p.Description != "" {
			fmt.Printf("    %s\n", p.Description)
		}
		if len(p.Set) == 0 && len(p.Unset) == 0 {
			fmt.Println("    (sets nothing, unsets nothing)")
			continue
		}
		keys := make([]string, 0, len(p.Set))
		width := 0
		for k := range p.Set {
			keys = append(keys, k)
			if len(k) > width {
				width = len(k)
			}
		}
		sort.Strings(keys)
		unset := append([]string(nil), p.Unset...)
		sort.Strings(unset)
		for _, k := range unset {
			if len(k) > width {
				width = len(k)
			}
		}
		for _, k := range keys {
			fmt.Printf("    set    %-*s  %s\n", width, k, displayEnvValue(k, p.Set[k], reveal))
		}
		for _, k := range unset {
			fmt.Printf("    unset  %s\n", k)
		}
	}
}
