package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
)

// runApply runs a setup file (docs/cli-grammar.md, "setup.cpb"): it parses
// and validates the whole file and writes nothing if any of it fails, then
// runs the statements in order, each whole-or-nothing, and stops at the
// first failure. Every statement SHOW CREATE writes is safe to repeat, so
// running the fixed file again is the recovery.
func runApply(st *grammar.Stmt) error {
	data, err := os.ReadFile(st.File)
	if err != nil {
		return err
	}
	stmts, err := grammar.ParseFile(string(data))
	if err != nil {
		return fmt.Errorf("%s: %w\nnothing was written", st.File, err)
	}

	// A file never consents to DROP PLAYBOOK on its own: it deletes an
	// install directory, data and all.
	var drops []string
	for _, s := range stmts {
		if s.Verb == grammar.Drop && s.Object == grammar.Playbook {
			drops = append(drops, fmt.Sprintf("  line %d: DROP PLAYBOOK %s", s.Pos.Line, s.Name))
		}
	}
	if len(drops) > 0 && !st.Yes && !st.DryRun {
		return fmt.Errorf("%s drops playbooks, deleting their directories:\n%s\nnothing was written: review it with APPLY %s --dry-run, then confirm with --yes",
			st.File, strings.Join(drops, "\n"), st.File)
	}

	// What can be checked before any write is checked for the whole file:
	// every secret reference must resolve.
	var refs []grammar.Clause
	for _, s := range stmts {
		for _, c := range s.Clauses {
			if c.Kind == grammar.SetRef {
				refs = append(refs, c)
			}
		}
	}
	if err := checkRefs(refs); err != nil {
		return fmt.Errorf("%s: %w\nnothing was written", st.File, err)
	}

	r := &stmtRun{dryRun: st.DryRun, yes: st.Yes, envs: map[string]bool{}, playbooks: map[string]bool{}}
	counts := map[string]int{}
	for i, s := range stmts {
		r.outcome, r.note = "", ""
		head := stmtHead(s)
		if !st.DryRun {
			fmt.Printf("-- line %d: %s\n", s.Pos.Line, head)
		}
		if err := execStatement(r, s); err != nil {
			if st.DryRun {
				return fmt.Errorf("%s line %d (%s) would fail: %w\nthe dry run stops here; nothing was written", st.File, s.Pos.Line, head, err)
			}
			return fmt.Errorf("%s line %d (%s): %w\n%d of %d statements were applied before it; fix the file and APPLY it again (every statement is safe to repeat)",
				st.File, s.Pos.Line, head, err, i, len(stmts))
		}
		counts[r.outcome]++
		if st.DryRun {
			line := fmt.Sprintf("line %-4d %-9s %s", s.Pos.Line, r.outcome, head)
			if r.note != "" {
				line += "  (" + r.note + ")"
			}
			fmt.Println(line)
		}
	}
	verb := "Applied"
	if st.DryRun {
		verb = "Would apply"
	}
	fmt.Printf("%s %s: %d created, %d changed, %d unchanged, %d dropped\n", verb, st.File,
		counts[outCreated], counts[outChanged], counts[outUnchanged], counts[outDropped])
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
