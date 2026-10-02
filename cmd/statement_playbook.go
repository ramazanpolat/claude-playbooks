package cmd

import (
	"fmt"
	"path/filepath"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
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
		case grammar.RenameTo, grammar.Launcher, grammar.NoLauncher:
			return true
		}
	}
	return false
}

type createOptions struct {
	from, branch, subdir, link, alias string
	noAlias, sandbox, isolatedLogin   bool
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
		case grammar.Sandbox:
			o.sandbox = true
		case grammar.IsolatedLogin:
			o.isolatedLogin = true
		}
	}
	return o
}

func createPlaybookStatement(r *stmtRun, st *grammar.Stmt) error {
	pb, err := playbook.Find(config.ResolvePlaybooksDir(), st.Name)
	if err != nil { // discovery failed: whether it exists is unknown
		return err
	}
	exists := pb != nil
	if known, alive := r.playbookState(st.Name); known {
		exists = alive
		if !alive {
			pb = nil // dropped earlier in this dry run
		}
	}
	o := createOptionsOf(st)
	if exists && st.IfNotExists {
		r.outcome = outUnchanged
		r.say("PLAYBOOK "+st.Name+" already exists; unchanged", nil)
		// Drift: the file names another source than the install records.
		// A warning, never an error, and nothing changes.
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
		return nil
	}
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
			launcher: o.alias, noLauncher: o.noAlias, sandbox: o.sandbox, isolatedLogin: o.isolatedLogin}, []string{o.from})

	case o.link != "":
		if o.sandbox {
			return fmt.Errorf("SANDBOX does not apply to LINK: a linked playbook's manifest belongs to the target; set [sandbox] there")
		}
		return doLink(linkOpts{name: st.Name, launcher: o.alias, noLauncher: o.noAlias}, []string{o.link})
	}
	return doCreate(createOpts{launcher: o.alias, noLauncher: o.noAlias, sandbox: o.sandbox, isolatedLogin: o.isolatedLogin}, []string{st.Name})
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

// alterPlaybookLifecycle carries out RENAME TO, LAUNCHER and NO LAUNCHER. They
// are not combined with environment clauses: a rename after an environment
// write could not be undone as one step, so the statement would not apply
// whole or not at all.
func alterPlaybookLifecycle(r *stmtRun, st *grammar.Stmt) error {
	var rename, alias string
	noAlias := false
	for _, c := range st.Clauses {
		switch c.Kind {
		case grammar.RenameTo:
			rename = c.Arg
		case grammar.Launcher:
			alias = c.Arg
		case grammar.NoLauncher:
			noAlias = true
		default:
			return fmt.Errorf("RENAME TO, LAUNCHER and NO LAUNCHER cannot be combined with %s in one statement: use two statements", c.Kind)
		}
	}
	r.outcome = outChanged
	if r.dryRun {
		known, alive := r.playbookState(st.Name)
		var disk *manifest.Env
		cfg := ""
		if !known {
			pb, err := playbook.Require(config.ResolvePlaybooksDir(), st.Name)
			if err != nil {
				return err
			}
			if pb.Manifest != nil {
				disk = pb.Manifest.Env
			}
			cfg = pb.Path
		} else if !alive {
			return fmt.Errorf("unknown playbook %q (dropped earlier in the file)", st.Name)
		}
		if rename != "" { // later statements of the file address the new name, with its environment and plugins
			env := r.playbookEnv(st.Name, disk)
			r.renamePlaybook(st.Name, rename, cfg)
			r.recordPlaybookEnv(rename, env)
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
		// A playbook has one launcher: its alias, or its name. NO LAUNCHER
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
			return fmt.Errorf("cannot verify launcher name %q: %w", st.Name, err)
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
