package cmd

import (
	"fmt"
	"path/filepath"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

// The playbook lifecycle statements run the code the pre-grammar commands
// run (install, create, link, delete, rename, alias) through each command's
// options struct: the cobra handler fills it from flags, a statement from
// its clauses. No package state is shared, so statements in one APPLY
// cannot leak options into each other.

// lifecycle reports whether an ALTER PLAYBOOK renames or changes the
// launcher rather than the environment.
func lifecycle(st *grammar.Stmt) bool {
	for _, c := range st.Clauses {
		switch c.Kind {
		case grammar.RenameTo, grammar.Alias, grammar.NoAlias:
			return true
		}
	}
	return false
}

type createOptions struct {
	from, branch, subdir, link, alias string
	noAlias, sandbox                  bool
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
		case grammar.Alias:
			o.alias = c.Arg
		case grammar.NoAlias:
			o.noAlias = true
		case grammar.Sandbox:
			o.sandbox = true
		}
	}
	return o
}

func createPlaybookStatement(st *grammar.Stmt) error {
	if st.IfNotExists {
		pb, err := playbook.Find(config.ResolvePlaybooksDir(), st.Name)
		if err != nil { // discovery failed: whether it exists is unknown
			return err
		}
		if pb != nil {
			fmt.Printf("PLAYBOOK %s already exists; unchanged\n", st.Name)
			return nil
		}
	}
	o := createOptionsOf(st)
	switch {
	case o.from != "":
		return doInstall(installOpts{name: st.Name, branch: o.branch, subdir: o.subdir,
			alias: o.alias, noAlias: o.noAlias, sandbox: o.sandbox}, []string{o.from})

	case o.link != "":
		if o.sandbox {
			return fmt.Errorf("SANDBOX does not apply to LINK: a linked playbook's manifest belongs to the target; set [sandbox] there")
		}
		// `link` asks for metadata when the target has no manifest; a
		// statement never prompts, so the manifest must already be there.
		abs, err := filepath.Abs(o.link)
		if err != nil {
			return err
		}
		if !manifest.Exists(abs) {
			return fmt.Errorf("LINK %s: the directory has no %s, and a statement does not prompt for one: add a %s to the target, or run `claude-playbook link %s` interactively", o.link, manifest.FileName, manifest.FileName, o.link)
		}
		return doLink(linkOpts{name: st.Name, alias: o.alias, noAlias: o.noAlias}, []string{o.link})
	}
	return doCreate(createOpts{alias: o.alias, noAlias: o.noAlias, sandbox: o.sandbox}, []string{st.Name})
}

func dropPlaybookStatement(st *grammar.Stmt) error {
	if st.IfExists {
		// Only a playbook that is not there is a no-op; a registry that
		// cannot be read is an error, never "nothing to drop".
		pb, err := playbook.Find(config.ResolvePlaybooksDir(), st.Name)
		if err != nil {
			return err
		}
		if pb == nil {
			fmt.Printf("No playbook %s; nothing to drop\n", st.Name)
			return nil
		}
	}
	return doDelete(deleteOpts{yes: st.Yes}, []string{st.Name})
}

// alterPlaybookLifecycle carries out RENAME TO, ALIAS and NO ALIAS. They
// are not combined with environment clauses: a rename after an environment
// write could not be undone as one step, so the statement would not apply
// whole or not at all.
func alterPlaybookLifecycle(st *grammar.Stmt) error {
	var rename, alias string
	noAlias := false
	for _, c := range st.Clauses {
		switch c.Kind {
		case grammar.RenameTo:
			rename = c.Arg
		case grammar.Alias:
			alias = c.Arg
		case grammar.NoAlias:
			noAlias = true
		default:
			return fmt.Errorf("RENAME TO, ALIAS and NO ALIAS cannot be combined with %s in one statement: use two statements", c.Kind)
		}
	}
	if rename != "" {
		return doRename(renameOpts{alias: alias, noAlias: noAlias}, []string{st.Name, rename})
	}
	pb, err := playbook.Require(config.ResolvePlaybooksDir(), st.Name)
	if err != nil {
		return err
	}
	if noAlias {
		// A playbook has one launcher: its alias, or its name. NO ALIAS
		// removes whichever it is (the hidden dealias clears an alias only).
		if pb.Alias() != "" {
			if err := doAlias(aliasOpts{remove: true}, []string{st.Name}); err != nil {
				return err
			}
		}
		return retireNameLauncher(st.Name)
	}
	// ALIAS <its own name> makes the name the launcher: the alias it
	// replaces is cleared and its launcher retired first (the hidden
	// command's same spelling only repairs the name launcher).
	if alias == st.Name && pb.Alias() != "" {
		// Checked before the alias is cleared, so a name that cannot be the
		// launcher leaves the playbook with the launcher it has.
		owner, err := commandNameOwner(st.Name, st.Name)
		if err != nil {
			return fmt.Errorf("cannot verify command name %q: %w", st.Name, err)
		}
		if owner != nil {
			return fmt.Errorf("command name %q already addresses playbook %q; alias %q kept", st.Name, owner.Name, pb.Alias())
		}
		if err := doAlias(aliasOpts{remove: true}, []string{st.Name}); err != nil {
			return err
		}
	}
	return doAlias(aliasOpts{}, []string{st.Name, alias})
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
