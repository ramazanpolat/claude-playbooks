package cmd

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
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
	if st.Verb == grammar.Apply {
		return runApply(st)
	}
	r := &stmtRun{}
	err = execStatement(r, st)
	if r.warning != "" {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", r.warning)
	}
	return err
}

// Outcomes a write statement reports to APPLY.
const (
	outCreated   = "created"
	outChanged   = "changed"
	outUnchanged = "unchanged"
	outDropped   = "dropped"
)

// stmtRun is one statement's execution: whether it may write, and, in an
// APPLY --dry-run, what the file's earlier statements would have created,
// so a later statement that depends on them is judged as it would run.
type stmtRun struct {
	dryRun    bool
	yes       bool            // APPLY --yes: confirms the file's DROP PLAYBOOKs
	envs      map[string]bool // env sets created earlier in a dry run
	playbooks map[string]bool // playbooks created earlier in a dry run
	outcome   string
	note      string      // a dry run's detail, e.g. what a drop would delete
	warning   string      // reported, never an error: e.g. a source that drifted
	helper    helperState // in a dry run: the helper earlier statements would set
}

// checkRefs checks a statement's references against the helper in effect
// at this point of the run.
func (r *stmtRun) checkRefs(clauses []grammar.Clause) error {
	return checkRefsWith(r.helper, clauses)
}

// say prints a statement's report; a dry run prints only APPLY's summary.
func (r *stmtRun) say(head string, lines []string) {
	if !r.dryRun {
		report(head, lines)
	}
}

func execStatement(r *stmtRun, st *grammar.Stmt) error {
	switch {
	case st.Object == grammar.Env && st.Write():
		return envStatement(r, st)
	case st.Verb == grammar.Alter && st.Object == grammar.Defaults:
		return defaultsStatement(r, st)
	case st.Verb == grammar.Create && st.Object == grammar.Playbook:
		return createPlaybookStatement(r, st)
	case st.Verb == grammar.Drop && st.Object == grammar.Playbook:
		return dropPlaybookStatement(r, st)
	case st.Verb == grammar.Alter && st.Object == grammar.Playbook:
		if lifecycle(st) {
			return alterPlaybookLifecycle(r, st)
		}
		return playbookStatement(r, st)
	}
	return readStatement(st)
}

func envStatement(r *stmtRun, st *grammar.Stmt) error {
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
	// CREATE ... IF NOT EXISTS on an existing set writes nothing, so its
	// references are not checked: the helper is not even asked.
	if !(st.Verb == grammar.Create && (p != nil || r.envs[st.Name]) && st.IfNotExists) {
		if err := r.checkRefs(st.Clauses); err != nil {
			return err
		}
	}
	switch st.Verb {
	case grammar.Create:
		if (p != nil || r.envs[st.Name]) && st.IfNotExists {
			r.outcome = outUnchanged
			r.say("ENV "+st.Name+" already exists; unchanged", nil)
			return nil
		}
		if p != nil && !st.OrReplace {
			return fmt.Errorf("env set %q already exists: change it with ALTER ENV %s, or use CREATE OR REPLACE ENV / CREATE ENV IF NOT EXISTS", st.Name, st.Name)
		}
		old := p
		p = &envprofile.Profile{Name: st.Name, Set: map[string]string{}}
		lines := applyVarClauses(&p.Set, &p.Refs, &p.Unset, &p.Description, st.Clauses)
		return r.writeProfile(dir, old, p, "Replaced", "ENV "+st.Name, lines)

	case grammar.Alter:
		if p == nil {
			if r.envs[st.Name] { // created earlier in this dry run
				r.outcome = outChanged
				return nil
			}
			return fmt.Errorf("no env set %q: create it with CREATE ENV %s", st.Name, st.Name)
		}
		old := cloneProfile(p)
		if p.Set == nil {
			p.Set = map[string]string{}
		}
		lines := applyVarClauses(&p.Set, &p.Refs, &p.Unset, &p.Description, st.Clauses)
		return r.writeProfile(dir, old, p, "Altered", "ENV "+st.Name, lines)
	}

	// DROP ENV
	if p == nil {
		if r.envs[st.Name] {
			delete(r.envs, st.Name)
			r.outcome = outDropped
			return nil
		}
		if st.IfExists {
			r.outcome = outUnchanged
			r.say("No env set "+st.Name+"; nothing to drop", nil)
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
	r.outcome = outDropped
	if r.dryRun {
		return nil
	}
	if err := envprofile.Delete(dir, st.Name); err != nil {
		return err
	}
	fmt.Printf("Dropped ENV %s\n", st.Name)
	return nil
}

// writeProfile writes an env set unless nothing changed or this is a dry
// run, and records the outcome. old is nil for a new set; changed is the
// report's verb when it existed ("Replaced", "Altered").
func (r *stmtRun) writeProfile(dir string, old, p *envprofile.Profile, changed, what string, lines []string) error {
	switch {
	case old == nil:
		r.outcome = outCreated
	case profileEqual(old, p):
		r.outcome = outUnchanged
		r.say(what+" unchanged", nil)
		return nil
	default:
		r.outcome = outChanged
	}
	if r.dryRun {
		if old == nil {
			r.envs[p.Name] = true
		}
		return nil
	}
	if err := envprofile.Write(dir, p); err != nil {
		return fmt.Errorf("cannot write env set: %w", err)
	}
	head := changed + " " + what
	if r.outcome == outCreated {
		head = "Created " + what
	}
	r.say(head, lines)
	return nil
}

func defaultsStatement(r *stmtRun, st *grammar.Stmt) error {
	dir := envprofile.Dir(config.ResolvePlaybooksDir())

	var listClauses, helperClauses []grammar.Clause
	for _, c := range st.Clauses {
		if c.Kind == grammar.SetHelper || c.Kind == grammar.UnsetHelper {
			helperClauses = append(helperClauses, c)
		} else {
			listClauses = append(listClauses, c)
		}
	}

	unlock, err := lockRegistry()
	if err != nil {
		return err
	}
	defer unlock()

	// Everything is decided before anything is written.
	var names, lines, current []string
	write := envprofile.WriteDefaultsUnchecked
	if len(listClauses) > 0 {
		current, err = envprofile.Defaults(dir)
		if err != nil {
			// A broken marker can still be replaced outright: USE ENV
			// states the whole list and needs nothing from the old one.
			if listClauses[0].Kind != grammar.UseEnv {
				return fmt.Errorf("DEFAULTS cannot be read (%v); replace the list with ALTER DEFAULTS USE ENV …", err)
			}
			current = nil
		}
		if names, lines, err = r.applyEnvList(dir, current, listClauses); err != nil {
			return err
		}
		// Removing needs no profile to be readable, which matters exactly
		// when one is broken; adding checks every name.
		for _, c := range listClauses {
			if c.Kind == grammar.UseEnv || c.Kind == grammar.AddEnv {
				write = envprofile.WriteDefaults
			}
		}
	}

	// Unchanged when the list and the helper setting would stay as they are.
	listSame := len(listClauses) == 0 || slices.Equal(current, names)
	helperSame := true
	for _, c := range helperClauses {
		h, _ := os.ReadFile(filepath.Join(dir, envprofile.SecretHelperFile))
		stored := strings.TrimSpace(string(h))
		helperSame = (c.Kind == grammar.SetHelper && stored == c.Arg) || (c.Kind == grammar.UnsetHelper && stored == "")
	}
	switch {
	case listSame && helperSame:
		r.outcome = outUnchanged
		r.say("DEFAULTS unchanged", nil)
		return nil
	case r.dryRun:
		r.outcome = outChanged
		for _, c := range helperClauses {
			r.helper = r.helper.after(c)
		}
		return nil
	}
	r.outcome = outChanged

	// The helper setting is written first and restored if the list write
	// then fails, so the statement applies whole or not at all.
	restore := func() {}
	for _, c := range helperClauses {
		path := filepath.Join(dir, envprofile.SecretHelperFile)
		old, rerr := os.ReadFile(path)
		existed := rerr == nil
		restore = func() {
			if existed {
				_ = manifest.WritePrivate(path, old, 0o600)
			} else {
				_ = os.Remove(path)
			}
		}
		if c.Kind == grammar.SetHelper {
			if err := envprofile.SetSecretHelper(dir, c.Arg); err != nil {
				return fmt.Errorf("cannot store the secret helper: %w", err)
			}
			lines = append(lines, "secret helper  "+c.Arg)
		} else {
			if err := envprofile.ClearSecretHelper(dir); err != nil {
				return fmt.Errorf("cannot remove the secret helper: %w", err)
			}
			lines = append(lines, "secret helper  (none)")
		}
		if v, ok := os.LookupEnv(envprofile.SecretHelperEnv); ok && v != "" {
			lines = append(lines, "(note: "+envprofile.SecretHelperEnv+" is set in this environment and overrides the setting here)")
		}
	}
	if len(listClauses) > 0 {
		if err := write(dir, names); err != nil {
			restore()
			return fmt.Errorf("cannot write DEFAULTS: %w", err)
		}
	}
	r.say("Altered DEFAULTS", lines)
	return nil
}

func playbookStatement(r *stmtRun, st *grammar.Stmt) error {
	if err := r.checkRefs(st.Clauses); err != nil {
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
		if r.playbooks[st.Name] { // created earlier in this dry run
			r.outcome = outChanged
			return nil
		}
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
	before := cloneEnv(m.Env)
	if m.Env == nil {
		m.Env = &manifest.Env{}
	}
	if m.Env.Set == nil {
		m.Env.Set = map[string]string{}
	}
	profiles, lines, err := r.applyEnvList(dir, m.Env.Profiles, st.Clauses)
	if err != nil {
		return err
	}
	m.Env.Profiles = profiles
	lines = append(lines, applyVarClauses(&m.Env.Set, &m.Env.Refs, &m.Env.Unset, nil, st.Clauses)...)
	if m.Env.Empty() {
		m.Env = nil
	}
	if envEqual(before, m.Env) {
		r.outcome = outUnchanged
		r.say("PLAYBOOK "+st.Name+" unchanged", nil)
		return nil
	}
	r.outcome = outChanged
	if r.dryRun {
		return nil
	}
	if err := manifest.Write(pb.RootPath, m); err != nil {
		return fmt.Errorf("cannot record the environment: %w", err)
	}
	r.say("Altered PLAYBOOK "+st.Name, lines)
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
func (r *stmtRun) applyEnvList(dir string, list []string, clauses []grammar.Clause) ([]string, []string, error) {
	out := slices.Clone(list)
	var lines []string
	changed := false
	for _, c := range clauses {
		switch c.Kind {
		case grammar.UseEnv:
			for _, n := range c.Names {
				if err := r.requireEnv(dir, n); err != nil {
					return nil, nil, err
				}
			}
			out = slices.Clone(c.Names)
			changed = true
		case grammar.AddEnv:
			n := c.Names[0]
			if err := r.requireEnv(dir, n); err != nil {
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

func (r *stmtRun) requireEnv(dir, name string) error {
	if r.envs[name] { // created earlier in this dry run
		return nil
	}
	p, err := envprofile.Read(dir, name)
	if err != nil {
		return err
	}
	if p == nil {
		return fmt.Errorf("no env set %q: create it with CREATE ENV %s", name, name)
	}
	return nil
}

// applyVarClauses applies SET, SET … FROM, BLOCK and UNSET (and DESCRIBE,
// when desc is not nil) to one layer and returns a report line per change.
// A key lives in exactly one of set, refs and unset. Values are never
// reported; references are not secrets and are.
func applyVarClauses(set, refs *map[string]string, unset *[]string, desc *string, clauses []grammar.Clause) []string {
	var lines []string
	for _, c := range clauses {
		switch c.Kind {
		case grammar.SetRef:
			for _, v := range c.Vars {
				*unset = dropString(*unset, v.Key)
				delete(*set, v.Key)
				if *refs == nil {
					*refs = map[string]string{}
				}
				(*refs)[v.Key] = v.Ref
				lines = append(lines, "ref       "+v.Key+" <from "+v.Ref+">")
			}
		case grammar.SetVar:
			for _, v := range c.Vars {
				*unset = dropString(*unset, v.Key)
				delete(*refs, v.Key)
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
				delete(*refs, k)
				if !slices.Contains(*unset, k) {
					*unset = append(*unset, k)
				}
				lines = append(lines, "blocked   "+k)
			}
		case grammar.UnsetVar:
			for _, k := range c.Keys {
				delete(*set, k)
				delete(*refs, k)
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

func cloneProfile(p *envprofile.Profile) *envprofile.Profile {
	c := *p
	c.Set, c.Refs, c.Unset = maps.Clone(p.Set), maps.Clone(p.Refs), slices.Clone(p.Unset)
	return &c
}

func profileEqual(a, b *envprofile.Profile) bool {
	return a.Description == b.Description && envEqual(a.Env(), b.Env())
}

func cloneEnv(e *manifest.Env) *manifest.Env {
	if e == nil {
		return nil
	}
	return &manifest.Env{Profiles: slices.Clone(e.Profiles), Set: maps.Clone(e.Set), Refs: maps.Clone(e.Refs), Unset: slices.Clone(e.Unset)}
}

// envEqual compares two blocks as a launch sees them: attached sets in
// order, and set, refs and unset as sets of entries.
func envEqual(a, b *manifest.Env) bool {
	if a.Empty() || b.Empty() {
		return a.Empty() && b.Empty()
	}
	ua, ub := slices.Clone(a.Unset), slices.Clone(b.Unset)
	slices.Sort(ua)
	slices.Sort(ub)
	return slices.Equal(a.Profiles, b.Profiles) && maps.Equal(a.Set, b.Set) && maps.Equal(a.Refs, b.Refs) && slices.Equal(ua, ub)
}
