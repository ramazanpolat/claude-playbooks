package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envset"
	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

// runApply runs one or more playbook files, in the order given
// (SPEC.md, "playbook.cpb"). It parses and validates every
// file and writes nothing if any of it fails, then runs the statements in
// order, each whole-or-nothing, and stops at the first failure. Every
// statement SHOW CREATE writes is safe to repeat, so running the fixed
// files again is the recovery.
func runApply(st *grammar.Stmt) error {
	if st.JSON {
		return runApplyJSON(st)
	}
	return applyRunIn(st, nil, &applyScope{record: true})
}

// applyRun runs APPLY for cpb's own callers, which record nothing.
func applyRun(st *grammar.Stmt, rep *applyReport) error {
	return applyRunIn(st, rep, nil)
}

// applyRunIn runs APPLY. rep, when set, collects the --json plan; errors are
// classed for it (applyFailure), their text unchanged. sc, when set, records
// the run for cpb update, or narrows it to what cpb update runs.
func applyRunIn(st *grammar.Stmt, rep *applyReport, sc *applyScope) error {
	l := &applyLoader{seen: map[string]bool{}, seenFor: map[string]bool{}, total: map[string]int{}, pathOf: map[string]string{}}
	if st.Target != "" {
		name, err := resolveTarget(st.Target)
		if err != nil {
			return usage(fmt.Errorf("TO %s: %w\nnothing was written", st.Target, err))
		}
		l.to, l.toShown = name, st.Target
		if rep != nil {
			rep.Target = &targetJSON{Kind: "playbook", Name: name}
			if strings.HasPrefix(name, dirMark) {
				rep.Target = &targetJSON{Kind: "dir", Path: strings.TrimPrefix(name, dirMark)}
			}
		}
	}
	for _, path := range st.Files {
		l.root(path)
	}
	if rep != nil {
		rep.Files = append(rep.Files, l.paths...)
	}
	if len(l.errs) > 0 {
		err := fmt.Errorf("%w\nnothing was written", errors.Join(l.errs...))
		if l.usageErr {
			return usage(err)
		}
		file, line := l.locate(l.errs[0].Error())
		return refused(file, line, err)
	}
	for _, w := range l.warnings {
		fmt.Fprintln(os.Stderr, "Warning: "+w.String())
	}
	if rep != nil {
		rep.Warnings = append(rep.Warnings, l.warnings...)
	}
	stmts, err := l.withTargetsCreated()
	if err != nil {
		return usage(err)
	}
	if sc != nil && sc.only != "" {
		if stmts, err = sc.narrow(stmts); err != nil {
			return usage(err)
		}
	}
	all := stmts // as loaded, for the [apply] records
	// A setting set more than once for one target is written once, with
	// the last value (SPEC.md, "A setting set more than once").
	fold := foldStatements(stmts)
	stmts = fold.stmts
	// A plain config directory is not a playbook: applying to one is
	// confirmed on a terminal, or by --yes, before anything runs.
	if strings.HasPrefix(l.to, dirMark) && !st.DryRun && !st.Yes {
		dir := strings.TrimPrefix(l.to, dirMark)
		if !(isTerminal(os.Stdin) && isTerminal(os.Stdout)) {
			return fmt.Errorf("TO %s is a Claude Code config directory, not a playbook: confirm with --yes (review with --dry-run first)\nnothing was written", dir)
		}
		if !confirm(fmt.Sprintf("Apply to %s, a Claude Code config directory that is not a playbook? Its settings are backed up first. [y/N] ", dir)) {
			return fmt.Errorf("not confirmed; nothing was written")
		}
	}

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
	// A statement that claims a launcher name checks it against every
	// playbook, and a manifest that cannot be read refuses that check: it
	// refuses here, before anything is written. So does DROP ENV, below.
	_, unreadable, serr := playbook.Scan(config.ResolvePlaybooksDir())
	if serr != nil {
		unreadable = nil // the statements report a root they cannot read
	}
	if bad := unreadable; len(bad) > 0 {
		for _, x := range stmts {
			if claimsLauncherName(x.s) {
				return refused(x.path, x.s.Pos.Line, fmt.Errorf("%s:%d (%s): cannot check its launcher name against every playbook: %w\nnothing was written",
					x.file, x.s.Pos.Line, stmtHead(x.s), &playbook.UnreadableError{List: bad}))
			}
		}
	}
	envDir := envset.Dir(config.ResolvePlaybooksDir())
	// Whether each env set exists at that point of the files: an earlier
	// CREATE makes it, an earlier DROP removes it, and the disk says the rest.
	envExists := map[string]bool{}
	existsNow := func(name string) bool {
		if e, ok := envExists[name]; ok {
			return e
		}
		p, _ := envset.Read(envDir, name)
		return p != nil
	}
	for _, x := range stmts {
		s := x.s
		if s.Dir != "" {
			// A plain directory: its refusals are checked here, before any
			// write; it takes no reference to check.
			if err := validateDirClauses(s); err != nil {
				return refused(x.path, s.Pos.Line, fmt.Errorf("%s:%d: %w\nnothing was written", x.file, s.Pos.Line, err))
			}
			continue
		}
		if s.Object == grammar.Env && s.Verb == grammar.Drop {
			// A playbook that cannot be read may use the set: the drop
			// would refuse, so the file does, before anything is written.
			if len(unreadable) > 0 && existsNow(s.Name) {
				return refused(x.path, s.Pos.Line, fmt.Errorf("%s:%d (%s): a playbook that cannot be read may use env set %q: %w\nnothing was written",
					x.file, s.Pos.Line, stmtHead(s), s.Name, &playbook.UnreadableError{List: unreadable}))
			}
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
					return refused(x.path, s.Pos.Line, fmt.Errorf("%s:%d: %w\nnothing was written", x.file, s.Pos.Line, err))
				}
			}
			if c.Kind == grammar.SetRef {
				if err := checkRefsWith(helper, []grammar.Clause{c}); err != nil {
					return refused(x.path, s.Pos.Line, fmt.Errorf("%s:%d: %w\nnothing was written", x.file, s.Pos.Line, err))
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
	for i, x := range stmts {
		s := x.s
		r.outcome, r.note, r.warnings, r.actions = "", "", nil, nil
		where := fmt.Sprintf("%s:%d", x.file, s.Pos.Line)
		if s.Pos.Line == 0 { // cpb update's undo, from no file
			where = x.file
		}
		head := stmtHead(s)
		if !st.DryRun {
			fmt.Printf("-- %s: %s\n", where, head)
		}
		entry := func(verdict string) applyStmtJSON {
			e := applyStmtJSON{File: x.path, Line: s.Pos.Line, Statement: head, Verb: string(s.Verb), Object: string(s.Object),
				Target: stmtTarget(s), Recipe: x.recipe, Implicit: x.implicit, Verdict: verdict, Warnings: []applyWarning{}, Actions: nonNilActions(r.actions)}
			for _, o := range fold.over[i] {
				w := stmts[o.by]
				e.Overridden = append(e.Overridden, applyOverride{Clause: o.what, By: applyLocation{File: w.path, Line: w.s.Pos.Line}})
			}
			return e
		}
		var err error
		if fold.skip[i] {
			r.outcome = outUnchanged // every clause is set again later: nothing to write
		} else {
			err = execStatement(r, s)
		}
		if len(fold.over[i]) > 0 {
			note := overriddenNote(fold.over[i], stmts)
			if r.note != "" {
				note = r.note + "; " + note
			}
			r.note = note
			if !st.DryRun && err == nil {
				fmt.Println("  " + note)
			}
		}
		if err != nil {
			if st.DryRun {
				if rep != nil {
					e := entry(verdictRefused)
					reason := err.Error()
					e.Reason, e.Actions = &reason, []planAction{}
					rep.Statements = append(rep.Statements, e)
					rep.Summary.Refused++
				}
				return refused(x.path, s.Pos.Line, fmt.Errorf("%s (%s) would fail: %w\nthe dry run stops here; nothing was written", where, head, err))
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
		if rep != nil {
			e := entry(r.outcome)
			for _, w := range r.warnings {
				e.Warnings = append(e.Warnings, applyWarning{Code: w.code, File: x.path, Line: s.Pos.Line, Message: w.message, shown: x.file})
			}
			rep.Statements = append(rep.Statements, e)
			switch r.outcome {
			case outCreated:
				rep.Summary.Created++
			case outChanged:
				rep.Summary.Changed++
			case outUnchanged:
				rep.Summary.Unchanged++
			case outDropped:
				rep.Summary.Dropped++
			}
		}
		for _, w := range r.warnings {
			counts["warning"]++
			fmt.Fprintf(os.Stderr, "Warning: %s: %s\n", where, w.message)
		}
		if st.DryRun {
			line := fmt.Sprintf("%-20s %-9s %s", where, r.outcome, head)
			if r.note != "" {
				line += "  (" + r.note + ")"
			}
			for _, w := range r.warnings {
				line += "  WARNING: " + w.message
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
	if sc != nil && sc.record && !st.DryRun {
		return writeApplyRecords(st, l.to, all)
	}
	return nil
}

// located is one statement to run and the file it came from. recipe marks
// a name-less ALTER PLAYBOOK, named here with its target.
type located struct {
	file     string // as the human output names it
	path     string // resolved, for --json
	s        *grammar.Stmt
	recipe   bool
	implicit bool // the bare CREATE a missing target gets
}

// applyLoader reads the files APPLY runs and expands their INCLUDEs
// (SPEC.md, "INCLUDE"): included files run in place
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
	to       string // APPLY … TO: the target of every name-less statement (dirMark+path for a directory)
	toShown  string // TO as written, for messages
	warnings []applyWarning
	errs     []error
	paths    []string          // the files' resolved paths, in load order, for --json
	pathOf   map[string]string // a file as reports name it -> its resolved path
	usageErr bool              // an error in the command line (a missing file), not in a file
}

var errAtLine = regexp.MustCompile(`^(.+?):(?:(\d+):| line (\d+),)`)

// locate finds the file and line an error message starts with, the file
// resolved.
func (l *applyLoader) locate(msg string) (string, int) {
	m := errAtLine.FindStringSubmatch(msg)
	if m == nil {
		return "", 0
	}
	n := m[2] + m[3]
	line, _ := strconv.Atoi(n)
	if p, ok := l.pathOf[m[1]]; ok {
		return p, line
	}
	return m[1], line
}

// root loads a file named on the command line. It may be a pipe (a file
// that is not regular), which then may not INCLUDE a relative path.
func (l *applyLoader) root(path string) {
	info, err := os.Stat(path)
	if err != nil {
		l.errs = append(l.errs, err)
		l.usageErr = true
		return
	}
	if info.IsDir() {
		l.errs = append(l.errs, fmt.Errorf("%s is a directory, not a playbook file", path))
		l.usageErr = true
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
	// Known before the parse, so a parse error names the file resolved.
	resolved := id
	if base == "" { // a pipe has no path but its name
		resolved = name
	}
	l.pathOf[name] = resolved
	stmts, err := grammar.ParseFile(string(data))
	if err != nil {
		l.errs = append(l.errs, fmt.Errorf("%s: %w", name, err))
		return
	}
	if first {
		l.files = append(l.files, name)
		l.paths = append(l.paths, resolved)
	}
	chain = append(chain, chainLink{id, name})
	cur := target // this file's target: inherited, changed by USE PLAYBOOK
	for _, s := range stmts {
		if s.Verb == grammar.Use {
			if l.to != "" {
				l.warnings = append(l.warnings, applyWarning{Code: warnUsePlaybookOverridden, File: resolved, Line: s.Pos.Line,
					Message: fmt.Sprintf("USE PLAYBOOK %s is ignored: TO %s sets the target", s.Name, l.toShown), shown: name})
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
				if strings.HasPrefix(cur, dirMark) {
					named.Name, named.Dir = "", strings.TrimPrefix(cur, dirMark)
				}
				l.out = append(l.out, located{file: name, path: resolved, s: &named, recipe: true})
			} else {
				l.out = append(l.out, located{file: name, path: resolved, s: s})
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

// dirMark marks a target that is a plain config directory, not a playbook
// name (a name never holds a NUL).
const dirMark = "\x00dir:"

// stmtHead names a statement in reports: verb, object and name.
func stmtHead(s *grammar.Stmt) string {
	if s.Dir != "" {
		return string(s.Verb) + " '" + s.Dir + "'"
	}
	h := string(s.Verb) + " " + string(s.Object)
	if s.Name != "" {
		h += " " + s.Name
	}
	return h
}

// withTargetsCreated returns the statements to run, with a bare CREATE
// PLAYBOOK IF NOT EXISTS before the first recipe for a target that neither
// exists nor is created earlier in the files (docs: "Targets"): the dry run
// lists it like any other statement. A registry that cannot be read stops
// the apply before anything is written.
func (l *applyLoader) withTargetsCreated() ([]located, error) {
	root := config.ResolvePlaybooksDir()
	created := map[string]bool{}
	out := make([]located, 0, len(l.out))
	for _, x := range l.out {
		if x.s.Verb == grammar.Create && x.s.Object == grammar.Playbook {
			created[x.s.Name] = true
		}
		if x.recipe && x.s.Dir == "" && !created[x.s.Name] {
			created[x.s.Name] = true
			pb, err := playbook.Find(root, x.s.Name)
			if err != nil {
				return nil, fmt.Errorf("%s: target %s: %w\nnothing was written", x.file, x.s.Name, err)
			}
			if pb == nil {
				out = append(out, located{file: x.file, path: x.path, implicit: true, s: &grammar.Stmt{Verb: grammar.Create, Object: grammar.Playbook,
					Name: x.s.Name, IfNotExists: true, Pos: x.s.Pos}})
				l.total[x.file]++
			}
		}
		out = append(out, x)
	}
	return out, nil
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
	pbs, bad, err := playbook.Scan(config.ResolvePlaybooksDir())
	if err != nil {
		return "", err
	}
	// The directory of a playbook that cannot be read is that playbook:
	// its read error, not a plain directory.
	for _, u := range bad {
		if r, err := filepath.EvalSymlinks(u.Path); err == nil && r == dir {
			return "", u.Err
		}
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
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			return "", fmt.Errorf("%s is not a directory", dir)
		}
		return dirMark + dir, nil
	}
	return "", fmt.Errorf("the directory belongs to several playbooks (%s): name one, TO <playbook>", strings.Join(names, ", "))
}

// claimsLauncherName reports a statement that registers a name a launcher
// answers to: a new playbook (its directory name, and its launcher), a
// rename, or a launcher change. CREATE … IF NOT EXISTS on a playbook that
// exists registers nothing.
func claimsLauncherName(s *grammar.Stmt) bool {
	if s.Dir != "" || s.Object != grammar.Playbook {
		return false
	}
	switch s.Verb {
	case grammar.Create:
		if s.IfNotExists {
			if pb, err := playbook.Find(config.ResolvePlaybooksDir(), s.Name); err == nil && pb != nil {
				return false
			}
		}
		return true
	case grammar.Alter:
		for _, c := range s.Clauses {
			if c.Kind == grammar.RenameTo || c.Kind == grammar.Launcher {
				return true
			}
		}
	}
	return false
}
