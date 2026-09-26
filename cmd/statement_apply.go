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
)

// runApply runs one or more playbook files, in the order given
// (docs/cli-grammar.md, "playbook.cpb"). It parses and validates every
// file and writes nothing if any of it fails, then runs the statements in
// order, each whole-or-nothing, and stops at the first failure. Every
// statement SHOW CREATE writes is safe to repeat, so running the fixed
// files again is the recovery.
func runApply(st *grammar.Stmt) error {
	l := &applyLoader{seen: map[string]bool{}, total: map[string]int{}}
	for _, path := range st.Files {
		l.root(path)
	}
	if len(l.errs) > 0 {
		return fmt.Errorf("%w\nnothing was written", errors.Join(l.errs...))
	}
	stmts := l.out

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

// located is one statement to run and the file it came from.
type located struct {
	file string
	s    *grammar.Stmt
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
	errs  []error
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
	l.load(path, id, base, nil)
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

func (l *applyLoader) load(name, id, base string, chain []chainLink) {
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
	if l.seen[id] {
		return
	}
	l.seen[id] = true
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
	l.files = append(l.files, name)
	chain = append(chain, chainLink{id, name})
	for _, s := range stmts {
		if s.Verb != grammar.Include {
			// A relative directory source resolves against this file's
			// directory, as INCLUDE does.
			for i, c := range s.Clauses {
				if c.Kind != grammar.AddMarketplace || !grammar.RelativeSource(c.Arg) {
					continue
				}
				if base == "" {
					l.errs = append(l.errs, fmt.Errorf("%s:%d: a relative directory source in a file that is not a regular file (a pipe) has nothing to resolve against", name, s.Pos.Line))
					continue
				}
				s.Clauses[i].Arg = filepath.Join(base, c.Arg)
			}
			l.out = append(l.out, located{name, s})
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
		l.load(p, cid, filepath.Dir(cid), chain)
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
