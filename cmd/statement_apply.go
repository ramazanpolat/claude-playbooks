package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envprofile"
	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

// runApply runs one or more playbook files, in the order given
// (docs/cli-grammar.md, "playbook.cpb"). It parses and validates every
// file and writes nothing if any of it fails, then runs the statements in
// order, each whole-or-nothing, and stops at the first failure. Every
// statement SHOW CREATE writes is safe to repeat, so running the fixed
// files again is the recovery.
func runApply(st *grammar.Stmt) error {
	l := &applyLoader{seen: map[string]bool{}, seenFor: map[string]bool{}, total: map[string]int{}}
	if st.Target != "" {
		name, err := resolveTarget(st.Target)
		if err != nil {
			return fmt.Errorf("TO %s: %w\nnothing was written", st.Target, err)
		}
		l.to = name
	}
	for _, path := range st.Files {
		l.root(path)
	}
	if len(l.errs) > 0 {
		return fmt.Errorf("%w\nnothing was written", errors.Join(l.errs...))
	}
	for _, w := range l.warnings {
		fmt.Fprintln(os.Stderr, "Warning: "+w)
	}
	stmts := l.withTargetsCreated()

	// A file never consents to DROP PLAYBOOK on its own: it deletes an
	// install directory, data and all.
	var drops []string
	for _, x := range stmts {
		if x.s.Verb == grammar.Drop && x.s.Object == grammar.Playbook {
			drops = append(drops, fmt.Sprintf("  %s:%d: DROP PLAYBOOK %s", x.file, x.s.Pos.Line, x.s.Name))
		}
	}
	if len(drops) > 0 && !st.Yes && !st.DryRun {
		return fmt.Errorf("the files drop playbooks, deleting their directories:\n%s\nnothing was written: review them with APPLY … --dry-run, then confirm with --yes",
			strings.Join(drops, "\n"))
	}
	// What can be checked before any write is checked for every file: each
	// secret reference must resolve, against the helper the files will have
	// set by then (a file may set the helper and use it in one run).
	var helper helperState
	envDir := envprofile.Dir(config.ResolvePlaybooksDir())
	// Whether each env set exists at that point of the files: an earlier
	// CREATE makes it, an earlier DROP removes it, and the disk says the rest.
	envExists := map[string]bool{}
	existsNow := func(name string) bool {
		if e, ok := envExists[name]; ok {
			return e
		}
		p, _ := envprofile.Read(envDir, name)
		return p != nil
	}
	for _, x := range stmts {
		s := x.s
		if s.Object == grammar.Env && s.Verb == grammar.Drop {
			envExists[s.Name] = false
			continue
		}
		// CREATE ENV IF NOT EXISTS on a set that exists writes nothing,
		// so its references are not checked (as on the command line).
		if s.Verb == grammar.Create && s.Object == grammar.Env {
			existed := existsNow(s.Name)
			envExists[s.Name] = true
			if s.IfNotExists && existed {
				continue
			}
		}
		for _, c := range s.Clauses {
			helper = helper.after(c)
			// An MCP server's references are checked here too, under the
			// variables they will be stored as.
			if c.Kind == grammar.AddMCP {
				var refs []grammar.Clause
				for _, v := range c.MCP.Env {
					if v.Ref != "" {
						refs = append(refs, grammar.Clause{Kind: grammar.SetRef, Vars: []grammar.Var{{Key: mcpVar(c.Names[0], "E", v.Key), Ref: v.Ref}}})
					}
				}
				for _, h := range c.MCP.Headers {
					if h.Ref != "" {
						refs = append(refs, grammar.Clause{Kind: grammar.SetRef, Vars: []grammar.Var{{Key: mcpVar(c.Names[0], "H", h.Key), Ref: h.Ref}}})
					}
				}
				if err := checkRefsWith(helper, refs); err != nil {
					return fmt.Errorf("%s:%d: %w\nnothing was written", x.file, s.Pos.Line, err)
				}
			}
			if c.Kind == grammar.SetRef {
				if err := checkRefsWith(helper, []grammar.Clause{c}); err != nil {
					return fmt.Errorf("%s:%d: %w\nnothing was written", x.file, s.Pos.Line, err)
				}
			}
		}
	}

	r := &stmtRun{dryRun: st.DryRun, yes: st.Yes}
	if st.DryRun {
		r.dry = newDryState()
	}
	counts := map[string]int{}
	done := map[string]int{} // statements applied per file
	for _, x := range stmts {
		s := x.s
		r.outcome, r.note, r.warning = "", "", ""
		where := fmt.Sprintf("%s:%d", x.file, s.Pos.Line)
		head := stmtHead(s)
		if !st.DryRun {
			fmt.Printf("-- %s: %s\n", where, head)
		}
		if err := execStatement(r, s); err != nil {
			if st.DryRun {
				return fmt.Errorf("%s (%s) would fail: %w\nthe dry run stops here; nothing was written", where, head, err)
			}
			applied := make([]string, 0, len(l.files))
			for _, f := range l.files {
				applied = append(applied, fmt.Sprintf("%s %d of %d", f, done[f], l.total[f]))
			}
			return fmt.Errorf("%s (%s): %w\napplied before it: %s; fix the files and APPLY them again (every statement is safe to repeat)",
				where, head, err, strings.Join(applied, ", "))
		}
		done[x.file]++
		counts[r.outcome]++
		if r.warning != "" {
			counts["warning"]++
			fmt.Fprintf(os.Stderr, "Warning: %s: %s\n", where, r.warning)
		}
		if st.DryRun {
			line := fmt.Sprintf("%-20s %-9s %s", where, r.outcome, head)
			if r.note != "" {
				line += "  (" + r.note + ")"
			}
			if r.warning != "" {
				line += "  WARNING: " + r.warning
			}
			fmt.Println(line)
		}
	}
	verb := "Applied"
	if st.DryRun {
		verb = "Would apply"
	}
	summary := fmt.Sprintf("%s %s: %d created, %d changed, %d unchanged, %d dropped", verb, strings.Join(st.Files, ", "),
		counts[outCreated], counts[outChanged], counts[outUnchanged], counts[outDropped])
	if n := counts["warning"]; n > 0 {
		summary += fmt.Sprintf(", %d warning(s)", n)
	}
	fmt.Println(summary)
	return nil
}

// located is one statement to run and the file it came from. recipe marks
// a name-less ALTER PLAYBOOK, named here with its target.
type located struct {
	file   string
	s      *grammar.Stmt
	recipe bool
}

// applyLoader reads the files APPLY runs and expands their INCLUDEs
// (docs/reference/cli-grammar.md, "INCLUDE"): included files run in place
// of the directive, a file reached twice runs once, at its first
// occurrence, and a cycle is refused. Every error is collected, so one run
// reports every problem and nothing is written.
type applyLoader struct {
	out   []located
	files []string       // in load order, as reports name them
	total map[string]int // statements per file, INCLUDEs not counted
	seen  map[string]bool
	// seenFor marks a file reached for a target: its name-less statements
	// run once per target, everything else once (docs: "Targets").
	seenFor  map[string]bool
	to       string // APPLY … TO: the target of every name-less statement
	warnings []string
	errs     []error
}

// root loads a file named on the command line. It may be a pipe (a file
// that is not regular), which then may not INCLUDE a relative path.
func (l *applyLoader) root(path string) {
	info, err := os.Stat(path)
	if err != nil {
		l.errs = append(l.errs, err)
		return
	}
	if info.IsDir() {
		l.errs = append(l.errs, fmt.Errorf("%s is a directory, not a playbook file", path))
		return
	}
	id, base := path, ""
	if info.Mode().IsRegular() {
		if id, err = fileIdentity(path); err != nil {
			l.errs = append(l.errs, err)
			return
		}
		base = filepath.Dir(id)
	}
	l.load(path, id, base, nil, l.to)
}

// fileIdentity is a file's fully resolved path: two spellings of one file
// are the same file.
func fileIdentity(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

type chainLink struct{ id, name string }

func (l *applyLoader) load(name, id, base string, chain []chainLink, target string) {
	for i, c := range chain {
		if c.id == id {
			names := make([]string, 0, len(chain)-i+1)
			for _, d := range chain[i:] {
				names = append(names, d.name)
			}
			names = append(names, name)
			l.errs = append(l.errs, fmt.Errorf("INCLUDE cycle: %s", strings.Join(names, " -> ")))
			return
		}
	}
	first := !l.seen[id]
	l.seen[id] = true
	forKey := id + "\x00" + target
	firstFor := !l.seenFor[forKey]
	l.seenFor[forKey] = true
	if !first && !firstFor {
		return // reached before for this target: nothing new would run
	}
	read := id
	if base == "" { // a pipe: read it by the name given
		read = name
	}
	data, err := os.ReadFile(read)
	if err != nil {
		l.errs = append(l.errs, err)
		return
	}
	stmts, err := grammar.ParseFile(string(data))
	if err != nil {
		l.errs = append(l.errs, fmt.Errorf("%s: %w", name, err))
		return
	}
	if first {
		l.files = append(l.files, name)
	}
	chain = append(chain, chainLink{id, name})
	cur := target // this file's target: inherited, changed by USE PLAYBOOK
	for _, s := range stmts {
		if s.Verb == grammar.Use {
			if l.to != "" {
				l.warnings = append(l.warnings, fmt.Sprintf("%s:%d: USE PLAYBOOK %s is ignored: TO %s sets the target", name, s.Pos.Line, s.Name, l.to))
				continue
			}
			cur = s.Name
			continue
		}
		if s.Verb != grammar.Include {
			switch {
			case s.Recipe && !firstFor:
				continue
			case !s.Recipe && !first:
				continue
			case s.Recipe && cur == "":
				l.errs = append(l.errs, fmt.Errorf("%s:%d: this file has name-less statements and no target: add TO <playbook|dir> to APPLY, or a USE PLAYBOOK line", name, s.Pos.Line))
				continue
			}
			// A relative directory source resolves against this file's
			// directory, as INCLUDE does.
			for i, c := range s.Clauses {
				rel := ""
				switch {
				case c.Kind == grammar.AddMarketplace && grammar.RelativeSource(c.Arg):
					rel = c.Arg
				case c.Kind == grammar.AddSkill && grammar.RelativeSource(c.Skill.From):
					rel = c.Skill.From
				default:
					continue
				}
				if base == "" {
					l.errs = append(l.errs, fmt.Errorf("%s:%d: a relative directory source in a file that is not a regular file (a pipe) has nothing to resolve against", name, s.Pos.Line))
					continue
				}
				if c.Kind == grammar.AddSkill {
					sk := *c.Skill
					sk.From = filepath.Join(base, rel)
					s.Clauses[i].Skill = &sk
				} else {
					s.Clauses[i].Arg = filepath.Join(base, rel)
				}
			}
			if s.Recipe {
				named := *s
				named.Name, named.Recipe = cur, false
				l.out = append(l.out, located{file: name, s: &named, recipe: true})
			} else {
				l.out = append(l.out, located{file: name, s: s})
			}
			l.total[name]++
			continue
		}
		at := fmt.Sprintf("%s:%d", name, s.Pos.Line)
		p := s.Files[0]
		if strings.Contains(p, "://") {
			l.errs = append(l.errs, fmt.Errorf("%s: INCLUDE takes a local file, not a URL", at))
			continue
		}
		if !filepath.IsAbs(p) {
			if base == "" {
				l.errs = append(l.errs, fmt.Errorf("%s: INCLUDE of a relative path from a file that is not a regular file (a pipe) has nothing to resolve against", at))
				continue
			}
			p = filepath.Join(base, p)
		}
		info, err := os.Stat(p)
		if err != nil {
			l.errs = append(l.errs, fmt.Errorf("%s: INCLUDE: %w", at, err))
			continue
		}
		if !info.Mode().IsRegular() {
			l.errs = append(l.errs, fmt.Errorf("%s: INCLUDE takes a regular file; %s is not one", at, p))
			continue
		}
		cid, err := fileIdentity(p)
		if err != nil {
			l.errs = append(l.errs, fmt.Errorf("%s: INCLUDE: %w", at, err))
			continue
		}
		l.load(p, cid, filepath.Dir(cid), chain, cur)
	}
}

// stmtHead names a statement in reports: verb, object and name.
func stmtHead(s *grammar.Stmt) string {
	h := string(s.Verb) + " " + string(s.Object)
	if s.Name != "" {
		h += " " + s.Name
	}
	return h
}

// withTargetsCreated returns the statements to run, with a bare CREATE
// PLAYBOOK IF NOT EXISTS before the first recipe for a target that neither
// exists nor is created earlier in the files (docs: "Targets"): the dry run
// lists it like any other statement.
func (l *applyLoader) withTargetsCreated() []located {
	root := config.ResolvePlaybooksDir()
	created := map[string]bool{}
	out := make([]located, 0, len(l.out))
	for _, x := range l.out {
		if x.s.Verb == grammar.Create && x.s.Object == grammar.Playbook {
			created[x.s.Name] = true
		}
		if x.recipe && !created[x.s.Name] {
			created[x.s.Name] = true
			if pb, err := playbook.Find(root, x.s.Name); err == nil && pb == nil {
				out = append(out, located{file: x.file, s: &grammar.Stmt{Verb: grammar.Create, Object: grammar.Playbook,
					Name: x.s.Name, IfNotExists: true, Pos: x.s.Pos}})
				l.total[x.file]++
			}
		}
		out = append(out, x)
	}
	return out
}

// dirTarget reports whether a TO target is a directory rather than a
// playbook name: a playbook name never has a '/' and never starts with '~'
// or '.'.
func dirTarget(t string) bool {
	return strings.Contains(t, "/") || strings.HasPrefix(t, "~") || strings.HasPrefix(t, ".")
}

// resolveTarget turns APPLY's TO into a playbook name. A directory that is
// a registered playbook (its directory or its config directory, symlinks
// followed) is that playbook; one claimed by several registrations is
// refused. A plain config directory is not a playbook target.
func resolveTarget(t string) (string, error) {
	if !dirTarget(t) {
		return t, nil
	}
	dir, err := expandSkillPath(t)
	if err != nil {
		return "", err
	}
	if dir, err = filepath.Abs(dir); err != nil {
		return "", err
	}
	if dir, err = filepath.EvalSymlinks(dir); err != nil {
		return "", fmt.Errorf("no such directory")
	}
	pbs, err := playbook.Discover(config.ResolvePlaybooksDir())
	if err != nil {
		return "", err
	}
	var names []string
	for _, pb := range pbs {
		for _, p := range []string{pb.Path, pb.RootPath} {
			if r, err := filepath.EvalSymlinks(p); err == nil && r == dir {
				names = append(names, pb.Name)
				break
			}
		}
	}
	switch len(names) {
	case 1:
		return names[0], nil
	case 0:
		return "", fmt.Errorf("%s is not a playbook, and applying to a plain config directory is not built yet", dir)
	}
	return "", fmt.Errorf("the directory belongs to several playbooks (%s): name one, TO <playbook>", strings.Join(names, ", "))
}
