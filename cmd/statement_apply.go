package cmd

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
)

// runApply runs one or more playbook files, in the order given
// (docs/cli-grammar.md, "playbook.cpb"). It parses and validates every
// file and writes nothing if any of it fails, then runs the statements in
// order, each whole-or-nothing, and stops at the first failure. Every
// statement SHOW CREATE writes is safe to repeat, so running the fixed
// files again is the recovery.
func runApply(st *grammar.Stmt) error {
	type file struct {
		path  string
		stmts []*grammar.Stmt
	}
	var files []file
	var errs []error
	for _, path := range st.Files {
		data, err := os.ReadFile(path)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		stmts, err := grammar.ParseFile(string(data))
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
			continue
		}
		files = append(files, file{path, stmts})
	}
	if len(errs) > 0 {
		return fmt.Errorf("%w\nnothing was written", errors.Join(errs...))
	}

	// A file never consents to DROP PLAYBOOK on its own: it deletes an
	// install directory, data and all.
	var drops []string
	for _, f := range files {
		for _, s := range f.stmts {
			if s.Verb == grammar.Drop && s.Object == grammar.Playbook {
				drops = append(drops, fmt.Sprintf("  %s:%d: DROP PLAYBOOK %s", f.path, s.Pos.Line, s.Name))
			}
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
	for _, f := range files {
		for _, s := range f.stmts {
			for _, c := range s.Clauses {
				helper = helper.after(c)
				if c.Kind == grammar.SetRef {
					if err := checkRefsWith(helper, []grammar.Clause{c}); err != nil {
						return fmt.Errorf("%s:%d: %w\nnothing was written", f.path, s.Pos.Line, err)
					}
				}
			}
		}
	}

	r := &stmtRun{dryRun: st.DryRun, yes: st.Yes, envs: map[string]bool{}, playbooks: map[string]bool{}}
	counts := map[string]int{}
	applied := make([]string, 0, len(files))
	for fi, f := range files {
		for i, s := range f.stmts {
			r.outcome, r.note, r.warning = "", "", ""
			where := fmt.Sprintf("%s:%d", f.path, s.Pos.Line)
			head := stmtHead(s)
			if !st.DryRun {
				fmt.Printf("-- %s: %s\n", where, head)
			}
			if err := execStatement(r, s); err != nil {
				if st.DryRun {
					return fmt.Errorf("%s (%s) would fail: %w\nthe dry run stops here; nothing was written", where, head, err)
				}
				applied = append(applied, fmt.Sprintf("%s %d of %d", f.path, i, len(f.stmts)))
				for _, rest := range files[fi+1:] {
					applied = append(applied, fmt.Sprintf("%s 0 of %d", rest.path, len(rest.stmts)))
				}
				return fmt.Errorf("%s (%s): %w\napplied before it: %s; fix the files and APPLY them again (every statement is safe to repeat)",
					where, head, err, strings.Join(applied, ", "))
			}
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
		applied = append(applied, fmt.Sprintf("%s %d of %d", f.path, len(f.stmts), len(f.stmts)))
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

// stmtHead names a statement in reports: verb, object and name.
func stmtHead(s *grammar.Stmt) string {
	h := string(s.Verb) + " " + string(s.Object)
	if s.Name != "" {
		h += " " + s.Name
	}
	return h
}
