package cmd

import (
	"fmt"
	"path/filepath"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
	"github.com/ramazanpolat/claude-playbooks/internal/settings"
)

// Each playbook lifecycle statement fills an options struct from its
// clauses (createOpts, installOpts, linkOpts, deleteOpts, renameOpts,
// aliasOpts) and runs the matching doX. No package state is shared, so
// statements in one APPLY cannot leak options into each other.

// lifecycle reports whether an ALTER PLAYBOOK renames or changes the
// launcher rather than the environment.
func lifecycle(st *grammar.Stmt) bool {
	for _, c := range st.Clauses {
		switch c.Kind {
		case grammar.RenameTo, grammar.Launcher, grammar.NoLauncher, grammar.DefaultLauncher:
			return true
		}
	}
	return false
}

type createOptions struct {
	from, branch, subdir, link, alias string
	noAlias, sandbox, isolatedLogin   bool
	// memory: the memory property, 'isolated' unless SET says otherwise;
	// a LINK has none (its settings.json is the target's).
	memory string
}

func createOptionsOf(st *grammar.Stmt) createOptions {
	var o createOptions
	for _, c := range st.Clauses {
		switch c.Kind {
		case grammar.From:
			o.from = c.Arg
		case grammar.Branch:
			o.branch = c.Arg
		case grammar.Subdir:
			o.subdir = c.Arg
		case grammar.Link:
			o.link = c.Arg
		case grammar.Launcher:
			o.alias = c.Arg
		case grammar.NoLauncher:
			o.noAlias = true
		case grammar.SetSandboxKeys:
			for _, v := range c.Settings {
				if v.Key == "always" && v.Value == "true" {
					o.sandbox = true
				}
			}
		}
	}
	login, _ := grammar.PropertyValue(st.Clauses, "login")
	o.isolatedLogin = login == "isolated"
	o.memory = grammar.PlaybookPropertyDefault("memory")
	if v, ok := grammar.PropertyValue(st.Clauses, "memory"); ok {
		o.memory = v
	}
	if o.link != "" {
		o.memory = ""
	}
	return o
}

// createPlaybookStatement creates a playbook. CREATE … SET is "create, then
// ALTER … SET", in one step: the login, the memory, the launcher and
// sandbox.always are written by the creation itself, before the login is
// linked and the launcher registered; the rest (model, agent, the status
// line, the other sandbox keys) then by an ALTER of the new playbook, within
// the same statement. On an existing playbook, IF NOT EXISTS applies the SET
// list as an ALTER (convergePlaybook): what is fixed at creation is only
// compared.
func createPlaybookStatement(r *stmtRun, st *grammar.Stmt) error {
	if st.IfNotExists {
		pb, exists, err := r.findPlaybook(st.Name)
		if err != nil {
			return err
		}
		if exists {
			return convergePlaybook(r, st, pb)
		}
	}
	var after []grammar.Clause
	for _, c := range st.Clauses {
		switch c.Kind {
		case grammar.From, grammar.Branch, grammar.Subdir, grammar.Link,
			grammar.Launcher, grammar.NoLauncher, grammar.SetProperties:
			// the creation's own: login and memory are SetProperties
		case grammar.SetSandboxKeys:
			// always is the creation's (with the isolated login); the other
			// keys are an ALTER of the new playbook.
			rest := c
			rest.Settings = nil
			for _, v := range c.Settings {
				if v.Key != "always" {
					rest.Settings = append(rest.Settings, v)
				}
			}
			if len(rest.Settings) > 0 {
				after = append(after, rest)
			}
		default:
			after = append(after, c)
		}
	}
	if err := createPlaybookOnly(r, st); err != nil || len(after) == 0 || r.outcome != outCreated {
		return err
	}
	err := playbookStatement(r, &grammar.Stmt{Verb: grammar.Alter, Object: grammar.Playbook, Name: st.Name, Pos: st.Pos, Clauses: after})
	r.outcome = outCreated
	return err
}

// findPlaybook finds a playbook as the run sees it: on disk, or created or
// dropped by an earlier statement of a dry run (pb nil, exists true: created
// earlier, not on disk yet).
func (r *stmtRun) findPlaybook(name string) (pb *playbook.Playbook, exists bool, err error) {
	pb, err = playbook.Find(config.ResolvePlaybooksDir(), name)
	if err != nil { // discovery failed: whether it exists is unknown
		return nil, false, err
	}
	exists = pb != nil
	if known, alive := r.playbookState(name); known {
		exists = alive
		if !alive {
			pb = nil // dropped earlier in this dry run
		}
	}
	return pb, exists, nil
}

// convergePlaybook is CREATE PLAYBOOK IF NOT EXISTS on a playbook that
// exists: its SET list is applied as ALTER … SET, the properties first and
// then the launcher (which stands alone, as in ALTER), each only where it
// differs, so a statement applied again changes nothing. FROM, BRANCH,
// SUBDIR and LINK are fixed at creation: a source that differs is a warning,
// never a change.
func convergePlaybook(r *stmtRun, st *grammar.Stmt, pb *playbook.Playbook) error {
	o := createOptionsOf(st)
	// Drift: the file names another source than the install records.
	if pb != nil && o.from != "" {
		var have string
		if m := pb.Manifest; m != nil && m.Source != nil {
			have = describeSource(m.Source.Repository, m.Source.Branch, m.Source.Subdir)
		}
		if want := describeSource(o.from, o.branch, o.subdir); have != want {
			if have == "" {
				have = "no recorded source"
			}
			r.warn(warnSourceDrift, fmt.Sprintf("PLAYBOOK %s exists; source differs (installed %s, file says %s)", st.Name, have, want))
		}
	}
	var props, launch []grammar.Clause
	for _, c := range st.Clauses {
		switch c.Kind {
		case grammar.From, grammar.Branch, grammar.Subdir, grammar.Link:
		case grammar.Launcher, grammar.NoLauncher:
			if launcherOpsAllowed() && launcherWanted(st.Name, c) != r.launcherOf(st.Name, pb) {
				launch = append(launch, c)
			}
		default:
			props = append(props, c)
		}
	}
	outcome := outUnchanged
	if len(props) > 0 {
		r.exists = true
		err := playbookStatement(r, &grammar.Stmt{Verb: grammar.Alter, Object: grammar.Playbook, Name: st.Name, Pos: st.Pos, Clauses: props})
		r.exists = false
		if err != nil {
			return err
		}
		outcome = r.outcome
	}
	if len(launch) > 0 {
		if err := alterPlaybookLifecycle(r, &grammar.Stmt{Verb: grammar.Alter, Object: grammar.Playbook, Name: st.Name, Pos: st.Pos, Clauses: launch}); err != nil {
			return err
		}
		outcome = outChanged
	}
	r.outcome = outcome
	if outcome == outUnchanged && len(props) == 0 {
		r.say("PLAYBOOK "+st.Name+" already exists; unchanged", nil)
	}
	return nil
}

// launcherWanted is the launcher a launcher clause gives: a command name,
// or "" for none.
func launcherWanted(name string, c grammar.Clause) string {
	switch c.Kind {
	case grammar.NoLauncher:
		return ""
	case grammar.DefaultLauncher:
		return name
	}
	return c.Arg
}

// launcherOf is the launcher a playbook has as the run sees it: its alias,
// the launcher named after it, or "" for none. A playbook an earlier
// statement of a dry run created has the one that statement gave it.
func (r *stmtRun) launcherOf(name string, pb *playbook.Playbook) string {
	if r.dry != nil {
		if l, ok := r.dry.launchers[name]; ok {
			return l
		}
	}
	switch {
	case pb == nil:
		return name // created earlier in this dry run: the default
	case pb.Alias() != "":
		return pb.Alias()
	case hasNameLauncher(name):
		return name
	}
	return ""
}

func createPlaybookOnly(r *stmtRun, st *grammar.Stmt) error {
	_, exists, err := r.findPlaybook(st.Name)
	if err != nil {
		return err
	}
	o := createOptionsOf(st)
	if r.dryRun {
		if exists {
			return fmt.Errorf("playbook %q already exists (write CREATE PLAYBOOK IF NOT EXISTS to keep it)", st.Name)
		}
		if o.link != "" {
			if abs, err := filepath.Abs(o.link); err != nil || !manifest.Exists(abs) {
				return fmt.Errorf("LINK %s: the directory has no %s", o.link, manifest.FileName)
			}
		}
		r.recordPlaybook(st.Name, true)
		r.recordPlaybookEnv(st.Name, nil) // a new playbook's env block is empty
		if r.dry != nil {
			switch {
			case o.noAlias:
				r.dry.launchers[st.Name] = ""
			case o.alias != "":
				r.dry.launchers[st.Name] = o.alias
			default:
				r.dry.launchers[st.Name] = st.Name
			}
		}
		if o.memory == "isolated" && r.dry != nil {
			// The settings.json CREATE writes, so a later SET memory in the
			// same file plans against it.
			sf := &settings.File{Root: settings.NewObject()}
			if _, _, err := applyMemory(sf, o.memory); err == nil {
				r.dry.settings[st.Name], _ = sf.Root.MarshalJSON()
			}
		}
		if r.dry != nil {
			r.dry.isolated[st.Name] = o.sandbox || o.isolatedLogin
			r.dry.sandboxed[st.Name] = o.sandbox
		}
		r.outcome = outCreated
		if o.from != "" {
			r.actions = append(r.actions, fetchAction(o.from, o.branch, o.subdir,
				filepath.Join(config.ResolvePlaybooksDir(), st.Name)))
		}
		return nil
	}
	r.outcome = outCreated
	switch {
	case o.from != "":
		return doInstall(installOpts{name: st.Name, branch: o.branch, subdir: o.subdir,
			launcher: o.alias, noLauncher: o.noAlias, sandbox: o.sandbox, isolatedLogin: o.isolatedLogin, memory: o.memory}, []string{o.from})

	case o.link != "":
		if o.sandbox {
			return fmt.Errorf("SANDBOX does not apply to LINK: a linked playbook's manifest belongs to the target; set [sandbox] there")
		}
		return doLink(linkOpts{name: st.Name, launcher: o.alias, noLauncher: o.noAlias}, []string{o.link})
	}
	return doCreate(createOpts{launcher: o.alias, noLauncher: o.noAlias, sandbox: o.sandbox, isolatedLogin: o.isolatedLogin, memory: o.memory}, []string{st.Name})
}

func dropPlaybookStatement(r *stmtRun, st *grammar.Stmt) error {
	// Only a playbook that is not there is "nothing to drop"; a registry
	// that cannot be read is an error.
	pb, err := playbook.Find(config.ResolvePlaybooksDir(), st.Name)
	if err != nil {
		return err
	}
	known, alive := r.playbookState(st.Name)
	if known && !alive {
		pb = nil // dropped earlier in this dry run
	}
	if pb == nil {
		if known && alive { // created earlier in this dry run
			r.recordPlaybook(st.Name, false)
			r.outcome = outDropped
			return nil
		}
		if st.IfExists {
			r.outcome = outUnchanged
			r.say("No playbook "+st.Name+"; nothing to drop", nil)
			return nil
		}
		return fmt.Errorf("unknown playbook %q. `cpb SHOW PLAYBOOKS` lists them", st.Name)
	}
	r.outcome = outDropped
	if r.dryRun {
		// What a drop deletes is shown before anything is confirmed.
		r.note = "deletes " + pb.RootPath
		r.actions = append(r.actions, deleteAction("playbook", pb.RootPath))
		r.recordPlaybook(st.Name, false)
		return nil
	}
	return doDelete(deleteOpts{yes: st.Yes || r.yes}, []string{st.Name})
}

// alterPlaybookLifecycle carries out RENAME TO and the launcher (SET
// launcher = '<name>', the empty launcher, DELETE launcher). They are not
// combined with other clauses: a rename after an environment write could not
// be undone as one step, so the statement would not apply whole or not at
// all.
func alterPlaybookLifecycle(r *stmtRun, st *grammar.Stmt) error {
	var rename, alias string
	noAlias := false
	for _, c := range st.Clauses {
		switch c.Kind {
		case grammar.RenameTo:
			rename = c.Arg
		case grammar.Launcher:
			alias = c.Arg
		case grammar.DefaultLauncher:
			// Back to the default: the launcher named after the playbook
			// (after a rename, the new name).
			alias = st.Name
			for _, rc := range st.Clauses {
				if rc.Kind == grammar.RenameTo {
					alias = rc.Arg
				}
			}
		case grammar.NoLauncher:
			noAlias = true
		default:
			return fmt.Errorf("RENAME TO and the launcher cannot be combined with %s in one statement: use two statements", c.Kind)
		}
	}
	r.outcome = outChanged
	if r.dryRun {
		known, alive := r.playbookState(st.Name)
		var disk *manifest.Env
		var onDisk *playbook.Playbook
		cfg := ""
		if !known {
			pb, err := playbook.Require(config.ResolvePlaybooksDir(), st.Name)
			if err != nil {
				return err
			}
			if pb.Manifest != nil {
				disk = pb.Manifest.Env
			}
			cfg, onDisk = pb.Path, pb
		} else if !alive {
			return fmt.Errorf("unknown playbook %q (dropped earlier in the file)", st.Name)
		}
		// The launcher later statements see: the one given, else the one
		// it had, renamed with the playbook when it was the default.
		was := r.launcherOf(st.Name, onDisk)
		target := st.Name
		if rename != "" { // later statements of the file address the new name, with its environment and plugins
			env := r.playbookEnv(st.Name, disk)
			r.renamePlaybook(st.Name, rename, cfg)
			r.recordPlaybookEnv(rename, env)
			target = rename
		}
		if r.dry != nil {
			switch {
			case noAlias:
				r.dry.launchers[target] = ""
			case alias != "":
				r.dry.launchers[target] = alias
			case was == st.Name:
				r.dry.launchers[target] = target
			default:
				r.dry.launchers[target] = was
			}
		}
		return nil
	}
	if rename != "" {
		return doRename(renameOpts{launcher: alias, noLauncher: noAlias}, []string{st.Name, rename})
	}
	pb, err := playbook.Require(config.ResolvePlaybooksDir(), st.Name)
	if err != nil {
		return err
	}
	if noAlias {
		// A playbook has one launcher: its alias, or its name. launcher = ''
		// removes whichever it is (the hidden dealias clears an alias only).
		if pb.Alias() != "" {
			if err := doLauncher(launcherOpts{remove: true}, []string{st.Name}); err != nil {
				return err
			}
		}
		return retireNameLauncher(st.Name)
	}
	// LAUNCHER <its own name> makes the name the launcher: the alias it
	// replaces is cleared and its launcher retired first (the hidden
	// command's same spelling only repairs the name launcher).
	if alias == st.Name && pb.Alias() != "" {
		// Checked before the alias is cleared, so a name that cannot be the
		// launcher leaves the playbook with the launcher it has.
		owner, err := commandNameOwner(st.Name, st.Name)
		if err != nil {
			return fmt.Errorf("cannot check launcher name %q against every playbook: %w", st.Name, err)
		}
		if owner != nil {
			return fmt.Errorf("launcher name %q already addresses playbook %q; launcher %q kept", st.Name, owner.Name, pb.Alias())
		}
		if err := doLauncher(launcherOpts{remove: true}, []string{st.Name}); err != nil {
			return err
		}
	}
	return doLauncher(launcherOpts{}, []string{st.Name, alias})
}

// retireNameLauncher removes the launcher named after a playbook, when cpb
// wrote it and no other playbook answers to that command.
func retireNameLauncher(name string) error {
	unlock, err := lockRegistry()
	if err != nil {
		return err
	}
	defer unlock()
	return retireAliasLauncher(name, name)
}

// describeSource spells a source for the drift warning.
func describeSource(url, branch, subdir string) string {
	s := url
	if branch != "" {
		s += " branch " + branch
	}
	if subdir != "" {
		s += " subdir " + subdir
	}
	return s
}
