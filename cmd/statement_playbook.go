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
// run (install, create, link, delete, rename, alias), with the statement's
// options in place of flags. That code is not changed by them: the hidden
// commands keep their own paths, and these statements add none.

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
		if _, err := playbook.Require(config.ResolvePlaybooksDir(), st.Name); err == nil {
			fmt.Printf("PLAYBOOK %s already exists; unchanged\n", st.Name)
			return nil
		}
	}
	o := createOptionsOf(st)
	switch {
	case o.from != "":
		defer restoreInstallFlags(installName, installBranch, installSubdir, installAlias, installNoAlias, installSandbox)
		installName, installBranch, installSubdir = st.Name, o.branch, o.subdir
		installAlias, installNoAlias, installSandbox = o.alias, o.noAlias, o.sandbox
		return runInstall(nil, []string{o.from})

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
		defer restoreLinkFlags(linkName, linkAlias, linkNoAlias)
		linkName, linkAlias, linkNoAlias = st.Name, o.alias, o.noAlias
		return runLink(nil, []string{o.link})
	}
	defer restoreCreateFlags(createAlias, createNoAlias, createSandbox)
	createAlias, createNoAlias, createSandbox = o.alias, o.noAlias, o.sandbox
	return runCreate(nil, []string{st.Name})
}

func dropPlaybookStatement(st *grammar.Stmt) error {
	if st.IfExists {
		if _, err := playbook.Require(config.ResolvePlaybooksDir(), st.Name); err != nil {
			fmt.Printf("No playbook %s; nothing to drop\n", st.Name)
			return nil
		}
	}
	defer func(v bool) { deleteYes = v }(deleteYes)
	deleteYes = st.Yes
	return runDelete(nil, []string{st.Name})
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
		defer restoreRenameFlags(renameAlias, renameNoAlias)
		renameAlias, renameNoAlias = alias, noAlias
		return runRename(nil, []string{st.Name, rename})
	}
	defer func(v bool) { aliasRemove = v }(aliasRemove)
	if noAlias {
		aliasRemove = true
		return runAlias(nil, []string{st.Name})
	}
	aliasRemove = false
	return runAlias(nil, []string{st.Name, alias})
}

func restoreInstallFlags(name, branch, subdir, alias string, noAlias, sandbox bool) {
	installName, installBranch, installSubdir, installAlias, installNoAlias, installSandbox = name, branch, subdir, alias, noAlias, sandbox
}

func restoreLinkFlags(name, alias string, noAlias bool) {
	linkName, linkAlias, linkNoAlias = name, alias, noAlias
}

func restoreCreateFlags(alias string, noAlias, sandbox bool) {
	createAlias, createNoAlias, createSandbox = alias, noAlias, sandbox
}

func restoreRenameFlags(alias string, noAlias bool) {
	renameAlias, renameNoAlias = alias, noAlias
}
