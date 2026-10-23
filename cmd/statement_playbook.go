package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envset"
	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/launcher"
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
	// login is the statement's SET login, "" when it does not say: an
	// install keeps the source's then.
	login string
	// globalInstructions: the global_instructions property, 'excluded'
	// unless SET says otherwise; a LINK has none (its settings.json is the
	// target's).
	globalInstructions string
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
	o.login, _ = grammar.PropertyValue(st.Clauses, "login")
	o.isolatedLogin = o.login == "isolated"
	o.globalInstructions = grammar.PlaybookPropertyDefault("global_instructions")
	if v, ok := grammar.PropertyValue(st.Clauses, "global_instructions"); ok {
		o.globalInstructions = v
	}
	if o.link != "" {
		o.globalInstructions = ""
	}
	// RESET launcher is the playbook's own name: the default, nothing to
	// record (a source never names the launcher).
	return o
}

// createPlaybookStatement creates a playbook. CREATE … SET is "create, then
// ALTER … SET", in one step: the login, the global instructions, the launcher and
// sandbox.always are written by the creation itself, before the login is
// linked and the launcher registered; the rest (model, agent, the status
// line, the other sandbox keys) then by an ALTER of the new playbook, within
// the same statement. On an existing playbook, CREATE OR ALTER applies the
// SET list as an ALTER (convergePlaybook), and IF NOT EXISTS changes
// nothing; what is fixed at creation is only compared.
func createPlaybookStatement(r *stmtRun, st *grammar.Stmt) error {
	if st.OrAlter || st.IfNotExists {
		pb, exists, err := r.findPlaybook(st.Name)
		if err != nil {
			return err
		}
		switch {
		case exists && st.OrAlter:
			return convergePlaybook(r, st, pb)
		case exists:
			warnDrift(r, st, pb)
			r.outcome = outUnchanged
			r.say("PLAYBOOK "+st.Name+" already exists; unchanged", nil)
			return nil
		}
	}
	o := createOptionsOf(st)
	var after []grammar.Clause
	for _, c := range st.Clauses {
		if isReset(c) {
			// RESET on a new playbook: an empty one starts at every default,
			// but one installed FROM a source carries the source's
			// settings.json, so the RESET applies after the install. The
			// launcher is the creation's own.
			if o.from != "" && c.Kind != grammar.DefaultLauncher {
				after = append(after, c)
			}
			continue
		}
		if c.Kind == grammar.SetIfUnset {
			// Judged after the creation, against what it installed: a
			// source's settings.json may already set the key.
			after = append(after, c)
			continue
		}
		switch c.Kind {
		case grammar.From, grammar.Branch, grammar.Subdir, grammar.Link,
			grammar.Launcher, grammar.NoLauncher, grammar.SetProperties:
			// the creation's own: login and global_instructions are SetProperties
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
	// The env sets USE ENV attaches must exist before anything is created:
	// the statement applies whole or not at all.
	for _, c := range st.Clauses {
		if c.Kind == grammar.UseEnv {
			for _, n := range c.Names {
				if err := r.requireEnv(envset.Dir(config.ResolvePlaybooksDir()), n); err != nil {
					return err
				}
			}
		}
	}
	// The keys the ALTER after the creation writes are checked before
	// anything is created: a value that ALTER would refuse (sandbox.backend
	// = bogus, a relative sandbox.workdir) refuses the CREATE whole.
	if _, _, _, err := planSandbox(nil, grammar.Flatten(after)); err != nil {
		return err
	}
	if err := createPlaybookOnly(r, st); err != nil || len(after) == 0 || r.outcome != outCreated {
		return err
	}
	alter := &grammar.Stmt{Verb: grammar.Alter, Object: grammar.Playbook, Name: st.Name, Pos: st.Pos, Clauses: after}
	alter, err := r.resolveIfUnset(alter)
	if err == nil && len(alter.Clauses) > 0 {
		err = playbookStatement(r, alter)
	}
	if err != nil && !r.dryRun {
		// The statement applies whole or not at all: the playbook this
		// statement created is removed again.
		if derr := doDelete(deleteOpts{yes: true}, []string{st.Name}); derr != nil {
			return fmt.Errorf("%w (the new playbook %q could not be removed again: %v)", err, st.Name, derr)
		}
		return fmt.Errorf("%w; nothing was created", err)
	}
	r.outcome = outCreated
	return err
}

// isReset reports a clause that puts a property back to its default: what
// RESET k parses to.
func isReset(c grammar.Clause) bool {
	switch c.Kind {
	case grammar.ResetProperties, grammar.DefaultLauncher, grammar.UnsetModel, grammar.UnsetAgent,
		grammar.UnsetStatusline, grammar.UnsetStatuslineRefresh, grammar.UnsetModelPickerMode, grammar.UnsetSandboxKeys:
		return true
	}
	return false
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

// convergePlaybook is CREATE OR ALTER PLAYBOOK on a playbook that exists:
// its SET list is applied as ALTER … SET, the properties first and then the
// launcher (which stands alone, as in ALTER), each only where it differs,
// so a statement applied again changes nothing. FROM, BRANCH, SUBDIR and
// LINK are fixed at creation: a source that differs is a warning, never a
// change.
func convergePlaybook(r *stmtRun, st *grammar.Stmt, pb *playbook.Playbook) error {
	warnDrift(r, st, pb)
	var props, launch []grammar.Clause
	for _, c := range st.Clauses {
		switch c.Kind {
		case grammar.From, grammar.Branch, grammar.Subdir, grammar.Link:
		case grammar.Launcher, grammar.NoLauncher, grammar.DefaultLauncher:
			if r.launcherDiffers(st.Name, pb, c) {
				launch = append(launch, c)
			}
		default:
			props = append(props, c)
		}
	}
	// Whole or nothing: every check that can refuse the launcher runs
	// before the properties are written.
	if len(launch) > 0 {
		if err := r.preflightLauncher(st.Name, pb, launch[0]); err != nil {
			return err
		}
	}
	outcome := outUnchanged
	if len(props) > 0 {
		alter, err := r.resolveIfUnset(&grammar.Stmt{Verb: grammar.Alter, Object: grammar.Playbook, Name: st.Name, Pos: st.Pos, Clauses: props})
		if err != nil {
			return err
		}
		if len(alter.Clauses) > 0 {
			r.exists = true
			err = playbookStatement(r, alter)
			r.exists = false
			if err != nil {
				return err
			}
			outcome = r.outcome
		}
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

// warnDrift warns when a CREATE names another source than the install
// records: a warning, never an error, and the source is never changed.
func warnDrift(r *stmtRun, st *grammar.Stmt, pb *playbook.Playbook) {
	o := createOptionsOf(st)
	if pb == nil || o.from == "" {
		return
	}
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
			return fmt.Errorf("playbook %q already exists (write CREATE PLAYBOOK IF NOT EXISTS to keep it, or CREATE OR ALTER PLAYBOOK to apply the SET list to it)", st.Name)
		}
		alias := o.alias
		if o.link != "" {
			abs, err := filepath.Abs(o.link)
			if err != nil || !manifest.Exists(abs) {
				return fmt.Errorf("LINK %s: the directory has no %s", o.link, manifest.FileName)
			}
			// A linked target's manifest is shared: its launcher registers.
			if m, _ := manifest.Read(abs); alias == "" && m != nil {
				alias = m.Launcher
			}
		}
		// The checks the creation runs before it registers anything: the
		// launcher it writes, and every name that will address it.
		if alias == st.Name {
			alias = ""
		}
		if err := validateTopLevelName("playbook name", st.Name); err != nil {
			return err
		}
		if _, err := resolveLauncherName(o.noAlias, alias, st.Name, "create the playbook"); err != nil {
			return err
		}
		if err := r.preflightNames("", st.Name, alias); err != nil {
			return err
		}
		r.recordPlaybook(st.Name, true)
		r.recordPlaybookEnv(st.Name, nil) // a new playbook's env block is empty
		if r.dry != nil {
			switch {
			case o.noAlias:
				r.dry.launchers[st.Name] = ""
			case alias != "":
				r.dry.launchers[st.Name] = alias
			default:
				r.dry.launchers[st.Name] = st.Name
			}
		}
		if o.globalInstructions == "excluded" && r.dry != nil {
			// The settings.json CREATE writes, so a later SET global_instructions in the
			// same file plans against it.
			sf := &settings.File{Root: settings.NewObject()}
			if _, _, err := applyGlobalInstructions(sf, o.globalInstructions); err == nil {
				r.dry.settings[st.Name], _ = sf.Root.MarshalJSON()
			}
		}
		if r.dry != nil {
			r.dry.isolated[st.Name] = o.sandbox || o.isolatedLogin
			r.dry.sandboxed[st.Name] = o.sandbox
			if o.sandbox { // a new playbook's [sandbox] block holds always alone
				r.dry.sandboxes[st.Name] = &manifest.Sandbox{Always: true}
			} else {
				r.dry.sandboxes[st.Name] = nil
			}
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
			launcher: o.alias, noLauncher: o.noAlias, sandbox: o.sandbox, login: o.login, globalInstructions: o.globalInstructions}, []string{o.from})

	case o.link != "":
		if o.sandbox {
			return fmt.Errorf("SANDBOX does not apply to LINK: a linked playbook's manifest belongs to the target; set [sandbox] there")
		}
		return doLink(linkOpts{name: st.Name, launcher: o.alias, noLauncher: o.noAlias}, []string{o.link})
	}
	return doCreate(createOpts{launcher: o.alias, noLauncher: o.noAlias, sandbox: o.sandbox, isolatedLogin: o.isolatedLogin, globalInstructions: o.globalInstructions}, []string{st.Name})
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

// launcherWithOthers reports an ALTER PLAYBOOK that changes the launcher
// together with other clauses, and renames nothing: the launcher is an
// ordinary key (D1). RENAME TO stays a statement of its own.
func launcherWithOthers(st *grammar.Stmt) bool {
	launch, other := false, false
	for _, c := range st.Clauses {
		switch c.Kind {
		case grammar.RenameTo:
			return false
		case grammar.Launcher, grammar.NoLauncher, grammar.DefaultLauncher:
			launch = true
		default:
			other = true
		}
	}
	return launch && other
}

// alterPlaybookWithLauncher is an ALTER PLAYBOOK whose SET list holds the
// launcher beside other keys. The launcher name is checked before anything
// is written; then the other clauses apply as one ALTER, and the launcher
// changes last, only where it differs, as CREATE OR ALTER does.
func alterPlaybookWithLauncher(r *stmtRun, st *grammar.Stmt) error {
	var props, launch []grammar.Clause
	for _, c := range st.Clauses {
		switch c.Kind {
		case grammar.Launcher, grammar.NoLauncher, grammar.DefaultLauncher:
			launch = append(launch, c)
		default:
			props = append(props, c)
		}
	}
	pb, exists, err := r.findPlaybook(st.Name)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("unknown playbook %q. `cpb SHOW PLAYBOOKS` lists them", st.Name)
	}
	differs := r.launcherDiffers(st.Name, pb, launch[0])
	if differs {
		// Whole or nothing: every check that can refuse the launcher runs
		// before the other clauses write anything.
		if err := r.preflightLauncher(st.Name, pb, launch[0]); err != nil {
			return err
		}
	}
	if err := playbookStatement(r, &grammar.Stmt{Verb: grammar.Alter, Object: grammar.Playbook, Name: st.Name, Pos: st.Pos, Clauses: props}); err != nil {
		return err
	}
	if !differs {
		return nil
	}
	if err := alterPlaybookLifecycle(r, &grammar.Stmt{Verb: grammar.Alter, Object: grammar.Playbook, Name: st.Name, Pos: st.Pos, Clauses: launch}); err != nil {
		return err
	}
	r.outcome = outChanged
	return nil
}

// launcherDiffers reports a launcher clause that would change the playbook.
// On the default root that is the launcher command itself. Under another
// root (--playbooks-dir) cpb manages no launchers (SPEC, "Launchers":
// default root only), so only the alias the manifest records counts: the
// default and the empty launcher both mean no alias there.
func (r *stmtRun) launcherDiffers(name string, pb *playbook.Playbook, c grammar.Clause) bool {
	if launcherOpsAllowed() {
		return launcherWanted(name, c) != r.launcherOf(name, pb)
	}
	want := launcherWanted(name, c)
	if want == name {
		want = ""
	}
	return want != r.recordedAlias(name, pb)
}

// recordedAlias is the alias a playbook's manifest records as the run sees
// it, "" for none (the launcher is its name, or it has none).
func (r *stmtRun) recordedAlias(name string, pb *playbook.Playbook) string {
	if r.dry != nil {
		if l, ok := r.dry.launchers[name]; ok {
			if l == name {
				return ""
			}
			return l
		}
	}
	if pb == nil {
		return ""
	}
	return pb.Alias()
}

// preflightLauncher runs every check that can refuse a launcher change,
// before anything is written: the name is valid, no other playbook answers
// to it, no file cpb did not write sits on it, and a linked target's shared
// manifest is not touched. These are the refusals doLauncher and
// alterPlaybookLifecycle would give later, read-only and in advance.
func (r *stmtRun) preflightLauncher(name string, pb *playbook.Playbook, c grammar.Clause) error {
	want := launcherWanted(name, c)
	if pb == nil { // created earlier in this dry run: the registry and the name can refuse
		if want == "" || want == r.launcherOf(name, nil) {
			return nil
		}
		if err := checkNewLauncher(want); err != nil {
			return err
		}
		return r.preflightNames(name, want)
	}
	linked := false
	if info, err := os.Lstat(pb.RootPath); err == nil && info.Mode()&os.ModeSymlink != 0 {
		linked = true
	}
	foreign := func(n string) error {
		if !launcherOpsAllowed() {
			return nil
		}
		if ldir, err := config.ResolveLauncherDir(); err == nil {
			if _, _, f := launcher.Lookup(ldir, n); f {
				return fmt.Errorf("launcher name %q is taken by a file cpb did not generate", n)
			}
		}
		return nil
	}
	old := pb.Alias()
	switch {
	case want == "":
		if linked && old != "" {
			return fmt.Errorf("cannot clear launcher %q: the linked target's manifest is shared with other registrations. Edit the target's %s directly if you really mean it", old, manifest.FileName)
		}
		return nil
	case want == name:
		owner, err := r.nameOwner(name, name)
		if err != nil {
			return fmt.Errorf("cannot check launcher name %q against every playbook: %w", name, err)
		}
		if owner != "" {
			return fmt.Errorf("launcher name %q already addresses playbook %q", name, owner)
		}
		if linked && old != "" {
			return fmt.Errorf("cannot clear launcher %q: the linked target's manifest is shared with other registrations. Edit the target's %s directly if you really mean it", old, manifest.FileName)
		}
		if err := checkLauncherKept(name); err != nil {
			return err
		}
		return foreign(name)
	}
	if linked && want != old {
		return fmt.Errorf("cannot set launcher %q on a linked target's shared %s (current launcher %q). Edit the target's manifest directly if you really mean it", want, manifest.FileName, old)
	}
	if err := launcher.ValidateName(want); err != nil {
		return err
	}
	if want == old {
		return nil
	}
	if err := checkNewLauncher(want); err != nil {
		return err
	}
	if err := r.preflightNames(pb.Name, want); err != nil {
		return err
	}
	return foreign(want)
}

// alterPlaybookLifecycle carries out RENAME TO and the launcher (SET
// launcher = '<name>', the empty launcher, RESET launcher). RENAME TO is
// not combined with other clauses (only with the launcher): a rename after
// an environment write could not be undone as one step, so the statement
// would not apply whole or not at all.
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
			return fmt.Errorf("RENAME TO is a statement of its own (with SET launcher at most), not combined with %s: use two statements", c.Kind)
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
		if rename == "" {
			// The launcher alone: the checks ALTER … SET launcher runs.
			for _, c := range st.Clauses {
				if err := r.preflightLauncher(st.Name, onDisk, c); err != nil {
					return err
				}
			}
		}
		if rename != "" {
			// The refusals doRename gives before it moves anything.
			if err := validateTopLevelName("new name", rename); err != nil {
				return err
			}
			if _, exists, err := r.findPlaybook(rename); err != nil {
				return err
			} else if exists {
				return fmt.Errorf("playbook %q already exists", rename)
			}
			if err := r.preflightNames(st.Name, alias, rename); err != nil {
				return err
			}
			// As doRename: SET launcher replaces the launcher; else the
			// default launcher follows the new name, a custom one is kept,
			// and none stays none.
			var err error
			switch {
			case alias != "" || noAlias:
				_, err = resolveLauncherNameKeeping(noAlias, alias, rename, "rename", was)
			case was == st.Name:
				_, err = resolveLauncherName(false, "", rename, "rename")
			case was != "":
				err = checkLauncherKept(was)
			}
			if err != nil {
				return err
			}
		}
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
		if err := checkLauncherKept(st.Name); err != nil {
			return fmt.Errorf("%w; launcher %q kept", err, pb.Alias())
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
