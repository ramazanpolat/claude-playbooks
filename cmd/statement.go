package cmd

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/auth"
	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envprofile"
	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

// Statements (docs/cli-grammar.md) are recognised before cobra runs, so
// cobra never parses a statement's words: a value such as OPTS=-v must not
// become a flag, and --dry-run / --json belong to the statement. Only the
// global registry flags may precede the verb.

// statementArgs returns the statement in args (os.Args[1:]) and applies any
// leading global flags, or reports false and touches nothing when args are
// not a statement.
func statementArgs(args []string) ([]string, bool) {
	var playbooksDir, launcherDir string
	i := 0
scan:
	for i < len(args) {
		switch a := args[i]; {
		case a == "--playbooks-dir" || a == "--launcher-dir":
			if i+1 >= len(args) {
				return nil, false // cobra reports the missing value
			}
			if a == "--playbooks-dir" {
				playbooksDir = args[i+1]
			} else {
				launcherDir = args[i+1]
			}
			i += 2
		case strings.HasPrefix(a, "--playbooks-dir="):
			playbooksDir = strings.TrimPrefix(a, "--playbooks-dir=")
			i++
		case strings.HasPrefix(a, "--launcher-dir="):
			launcherDir = strings.TrimPrefix(a, "--launcher-dir=")
			i++
		default:
			break scan
		}
	}
	rest := args[i:]
	if !grammar.IsStatement(rest) {
		return nil, false
	}
	if playbooksDir != "" {
		config.PlaybooksDir = playbooksDir
	}
	if launcherDir != "" {
		config.LauncherDir = launcherDir
	}
	return rest, true
}

// runStatement parses and executes one statement.
func runStatement(args []string) error {
	st, err := grammar.ParseArgs(args)
	if err != nil {
		return err
	}
	switch {
	case st.Object == grammar.Env && st.Write():
		return envStatement(st)
	case st.Verb == grammar.Alter && st.Object == grammar.Defaults:
		return defaultsStatement(st)
	case st.Verb == grammar.Alter && st.Object == grammar.Playbook:
		return playbookStatement(st)
	}
	return notYet(st.String())
}

// notYet refuses a statement the grammar accepts but this build does not
// carry out yet. It is refused before anything is written.
func notYet(what string) error {
	return fmt.Errorf("not implemented yet: %s (the grammar work lands in phases; see docs/cli-grammar.md)", what)
}

// refuseUnbuilt refuses clauses whose phase has not landed, before any
// lock or write, so a statement never applies partly.
func refuseUnbuilt(st *grammar.Stmt) error {
	for _, c := range st.Clauses {
		switch c.Kind {
		case grammar.SetRef, grammar.SetHelper, grammar.UnsetHelper:
			return notYet(fmt.Sprintf("%s (secret references)", c.Kind))
		case grammar.RenameTo, grammar.Alias, grammar.NoAlias:
			return notYet(fmt.Sprintf("ALTER PLAYBOOK … %s", c.Kind))
		}
	}
	return nil
}

func envStatement(st *grammar.Stmt) error {
	if err := refuseUnbuilt(st); err != nil {
		return err
	}
	playbooksDir := config.ResolvePlaybooksDir()
	dir := envprofile.Dir(playbooksDir)

	unlock, err := lockRegistry()
	if err != nil {
		return err
	}
	defer unlock()

	p, err := envprofile.Read(dir, st.Name)
	if err != nil {
		return err
	}
	switch st.Verb {
	case grammar.Create:
		if p != nil && st.IfNotExists {
			fmt.Printf("ENV %s already exists; unchanged\n", st.Name)
			return nil
		}
		if p != nil && !st.OrReplace {
			return fmt.Errorf("env set %q already exists: change it with ALTER ENV %s, or use CREATE OR REPLACE ENV / CREATE ENV IF NOT EXISTS", st.Name, st.Name)
		}
		verb := "Created"
		if p != nil {
			verb = "Replaced"
		}
		p = &envprofile.Profile{Name: st.Name, Set: map[string]string{}}
		lines := applyVarClauses(&p.Set, &p.Unset, &p.Description, st.Clauses)
		if err := envprofile.Write(dir, p); err != nil {
			return fmt.Errorf("cannot write env set: %w", err)
		}
		report(verb+" ENV "+st.Name, lines)
		return nil

	case grammar.Alter:
		if p == nil {
			return fmt.Errorf("no env set %q: create it with CREATE ENV %s", st.Name, st.Name)
		}
		if p.Set == nil {
			p.Set = map[string]string{}
		}
		lines := applyVarClauses(&p.Set, &p.Unset, &p.Description, st.Clauses)
		if err := envprofile.Write(dir, p); err != nil {
			return fmt.Errorf("cannot write env set: %w", err)
		}
		report("Altered ENV "+st.Name, lines)
		return nil
	}

	// DROP ENV
	if p == nil {
		if st.IfExists {
			fmt.Printf("No env set %s; nothing to drop\n", st.Name)
			return nil
		}
		return fmt.Errorf("no env set %q", st.Name)
	}
	users, err := profileUsers(playbooksDir)
	if err != nil {
		return err
	}
	if u := users[st.Name]; len(u) > 0 {
		return fmt.Errorf("env set %q is used by %s: detach it first with ALTER PLAYBOOK <playbook> DROP ENV %s", st.Name, strings.Join(u, ", "), st.Name)
	}
	// An unreadable marker refuses the drop: the set may be one every
	// launch depends on.
	defaults, err := envprofile.Defaults(dir)
	if err != nil {
		return fmt.Errorf("cannot read DEFAULTS: %w", err)
	}
	if isRegistryDefault(dir, defaults, st.Name) {
		return fmt.Errorf("env set %q is in DEFAULTS: remove it first with ALTER DEFAULTS DROP ENV %s", st.Name, st.Name)
	}
	if err := envprofile.Delete(dir, st.Name); err != nil {
		return err
	}
	fmt.Printf("Dropped ENV %s\n", st.Name)
	return nil
}

func defaultsStatement(st *grammar.Stmt) error {
	if err := refuseUnbuilt(st); err != nil {
		return err
	}
	dir := envprofile.Dir(config.ResolvePlaybooksDir())

	unlock, err := lockRegistry()
	if err != nil {
		return err
	}
	defer unlock()

	current, err := envprofile.Defaults(dir)
	if err != nil {
		// A broken marker can still be replaced outright: USE ENV states
		// the whole list and needs nothing from the old one.
		if st.Clauses[0].Kind != grammar.UseEnv {
			return fmt.Errorf("DEFAULTS cannot be read (%v); replace the list with ALTER DEFAULTS USE ENV …", err)
		}
		current = nil
	}
	names, lines, err := applyEnvList(dir, current, st.Clauses)
	if err != nil {
		return err
	}
	// Removing needs no profile to be readable, which matters exactly when
	// one is broken; adding checks every name.
	write := envprofile.WriteDefaultsUnchecked
	for _, c := range st.Clauses {
		if c.Kind == grammar.UseEnv || c.Kind == grammar.AddEnv {
			write = envprofile.WriteDefaults
		}
	}
	if err := write(dir, names); err != nil {
		return fmt.Errorf("cannot write DEFAULTS: %w", err)
	}
	report("Altered DEFAULTS", lines)
	return nil
}

func playbookStatement(st *grammar.Stmt) error {
	if err := refuseUnbuilt(st); err != nil {
		return err
	}
	playbooksDir := config.ResolvePlaybooksDir()
	dir := envprofile.Dir(playbooksDir)

	unlock, err := lockRegistry()
	if err != nil {
		return err
	}
	defer unlock()

	pb, err := playbook.Require(playbooksDir, st.Name)
	if err != nil {
		return err
	}
	// A linked playbook's manifest is shared with every registration of
	// the target directory: same refusal as the pre-grammar env command.
	if info, lerr := os.Lstat(pb.RootPath); lerr == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("cannot change the environment of %q: it is linked, and its %s is shared with the target. Edit the target's manifest directly if you really mean it", st.Name, manifest.FileName)
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
	profiles, lines, err := applyEnvList(dir, m.Env.Profiles, st.Clauses)
	if err != nil {
		return err
	}
	m.Env.Profiles = profiles
	lines = append(lines, applyVarClauses(&m.Env.Set, &m.Env.Unset, nil, st.Clauses)...)
	if m.Env.Empty() {
		m.Env = nil
	}
	if err := manifest.Write(pb.RootPath, m); err != nil {
		return fmt.Errorf("cannot record the environment: %w", err)
	}
	report("Altered PLAYBOOK "+st.Name, lines)
	for _, c := range st.Clauses {
		if c.Kind == grammar.BlockVar && slices.Contains(c.Keys, auth.OAuthTokenEnv) {
			fmt.Printf("Playbook %q now authenticates from stored credentials: the long-lived token is not injected and its login is left alone. Run it and /login once if it asks.\n", st.Name)
		}
	}
	return nil
}

// applyEnvList applies USE / ADD / DROP ENV to an ordered list of env sets
// and returns the new list with one report line per change. Every set a
// clause attaches must exist.
func applyEnvList(dir string, list []string, clauses []grammar.Clause) ([]string, []string, error) {
	out := slices.Clone(list)
	var lines []string
	changed := false
	for _, c := range clauses {
		switch c.Kind {
		case grammar.UseEnv:
			for _, n := range c.Names {
				if err := requireEnv(dir, n); err != nil {
					return nil, nil, err
				}
			}
			out = slices.Clone(c.Names)
			changed = true
		case grammar.AddEnv:
			n := c.Names[0]
			if err := requireEnv(dir, n); err != nil {
				return nil, nil, err
			}
			out = dropString(out, n)
			switch c.Where {
			case grammar.First:
				out = slices.Insert(out, 0, n)
			case grammar.Before, grammar.After:
				at := slices.Index(out, c.Anchor)
				if at < 0 {
					return nil, nil, fmt.Errorf("ADD ENV %s %s %s: %s is not attached", n, c.Where, c.Anchor, c.Anchor)
				}
				if c.Where == grammar.After {
					at++
				}
				out = slices.Insert(out, at, n)
			default:
				out = append(out, n)
			}
			changed = true
		case grammar.DropEnv:
			for _, n := range c.Names {
				if !slices.Contains(out, n) {
					lines = append(lines, fmt.Sprintf("%s was not attached", n))
					continue
				}
				out = dropString(out, n)
				changed = true
			}
		}
	}
	if changed {
		shown := strings.Join(out, ", ")
		if shown == "" {
			shown = "(none)"
		}
		lines = append(lines, "env sets  "+shown)
	}
	return out, lines, nil
}

func requireEnv(dir, name string) error {
	p, err := envprofile.Read(dir, name)
	if err != nil {
		return err
	}
	if p == nil {
		return fmt.Errorf("no env set %q: create it with CREATE ENV %s", name, name)
	}
	return nil
}

// applyVarClauses applies SET / BLOCK / UNSET (and DESCRIBE, when desc is
// not nil) to one layer and returns a report line per change. A key lives
// in exactly one of set and unset. Values are never reported.
func applyVarClauses(set *map[string]string, unset *[]string, desc *string, clauses []grammar.Clause) []string {
	var lines []string
	for _, c := range clauses {
		switch c.Kind {
		case grammar.SetVar:
			for _, v := range c.Vars {
				*unset = dropString(*unset, v.Key)
				(*set)[v.Key] = v.Value
				line := "set       " + v.Key
				if c.Plaintext && manifest.LooksLikeSecretKey(v.Key) {
					line += " (plaintext)"
				}
				lines = append(lines, line)
			}
		case grammar.BlockVar:
			for _, k := range c.Keys {
				delete(*set, k)
				if !slices.Contains(*unset, k) {
					*unset = append(*unset, k)
				}
				lines = append(lines, "blocked   "+k)
			}
		case grammar.UnsetVar:
			for _, k := range c.Keys {
				delete(*set, k)
				*unset = dropString(*unset, k)
				lines = append(lines, "unset     "+k)
			}
		case grammar.Describe:
			if desc != nil {
				*desc = c.Arg
				lines = append(lines, "described")
			}
		}
	}
	return lines
}

func report(head string, lines []string) {
	fmt.Println(head)
	for _, l := range lines {
		fmt.Println("  " + l)
	}
}
