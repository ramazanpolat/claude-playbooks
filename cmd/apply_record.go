package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

// The [apply] record (SPEC.md, "The [apply] record"): for each playbook an
// APPLY gives name-less statements, the files it applied and what those
// statements wrote, so `cpb update <name>` can apply the files again and
// remove what they no longer set.

// applyRecordFile holds what the statements wrote, references only.
const applyRecordFile = ".apply/recipe.cpb"

// applyScope is what an APPLY does beyond running its files. APPLY's
// internal callers (cpb play) pass none and record nothing.
type applyScope struct {
	record bool // write the [apply] records when the run succeeds
	// only keeps the name-less statements for this playbook (cpb update);
	// old is what its record says they wrote: the record's file, its text,
	// statements and sha256.
	only     string
	oldPath  string
	oldText  []byte
	old      []*grammar.Stmt
	oldSHA   string
	showDiff bool // print how the record changes before the plan
}

// targetStatements are the name-less statements the files give one
// playbook, in run order.
func targetStatements(stmts []located, name string) []located {
	var out []located
	for _, x := range stmts {
		if x.recipe && x.s.Dir == "" && x.s.Name == name {
			out = append(out, x)
		}
	}
	return out
}

// applyRecordText is what the record keeps for one playbook: its name-less
// statements after the fold, name-less again, with every credential-looking
// literal withheld as SHOW CREATE withholds it.
func applyRecordText(stmts []located) []byte {
	fold := foldStatements(stmts, nil) // name-less ALTERs only: no CREATE
	var b strings.Builder
	b.WriteString("-- What the last APPLY wrote to this playbook, for cpb update. Credential-looking literals are withheld.\n")
	for i, x := range fold.stmts {
		if fold.skip[i] || len(x.s.Clauses) == 0 {
			continue
		}
		r := grammar.Stmt{Verb: grammar.Alter, Object: grammar.Playbook, Recipe: true}
		for _, c := range x.s.Clauses {
			r.Clauses = append(r.Clauses, withheldClause(c))
		}
		b.WriteString(r.Pretty() + ";\n")
	}
	return []byte(b.String())
}

// withheldMark stands in for a value the record does not keep. The key
// stays, so cpb update still knows the statement set it.
const withheldMark = "<withheld>"

// withheldClause is a clause with its credential-looking literals and the
// credentials of its URLs withheld. The clause itself is not changed.
func withheldClause(c grammar.Clause) grammar.Clause {
	vars := func(in []grammar.Var) []grammar.Var {
		if in == nil {
			return nil
		}
		out := make([]grammar.Var, len(in))
		for i, v := range in {
			if v.Ref == "" && withheldLiteral(v.Key, v.Value) {
				v.Value = withheldMark
			}
			out[i] = v
		}
		return out
	}
	c.Vars = vars(c.Vars)
	c.Settings = vars(c.Settings)
	c.Arg = urlWithheld(c.Arg)
	if c.MCP != nil {
		m := *c.MCP
		m.Command, m.URL = urlWithheld(m.Command), urlWithheld(m.URL)
		m.Args = slices.Clone(m.Args)
		for i := range m.Args {
			m.Args[i] = urlWithheld(m.Args[i])
		}
		m.Env, m.Headers = vars(m.Env), vars(m.Headers)
		c.MCP = &m
	}
	if c.Skill != nil {
		sk := *c.Skill
		sk.From = urlWithheld(sk.From)
		c.Skill = &sk
	}
	return c
}

// urlWithheld replaces the credential a URL carries before its host.
func urlWithheld(v string) string {
	return urlUserinfo.ReplaceAllStringFunc(v, func(m string) string {
		return urlUserinfo.FindStringSubmatch(m)[1] + "withheld@"
	})
}

// narrow keeps what cpb update runs: the name-less statements the recorded
// files give its playbook, after one ALTER PLAYBOOK that removes what the
// record says they wrote and they no longer write the same way (play's
// undo, undoFor).
func (sc *applyScope) narrow(stmts []located) ([]located, error) {
	keep := targetStatements(stmts, sc.only)
	if len(keep) == 0 {
		return nil, fmt.Errorf("the recorded files give %s no name-less statements now (a USE PLAYBOOK line that names it is gone, or it was renamed): APPLY its files again\nnothing was written", sc.only)
	}
	text := applyRecordText(keep)
	newStmts, err := grammar.ParseFile(string(text))
	if err != nil {
		return nil, fmt.Errorf("%s: what the files would write cannot be recorded: %v\nnothing was written", sc.only, err)
	}
	if sc.showDiff {
		sum := sha256.Sum256(text)
		if h := hex.EncodeToString(sum[:]); h == sc.oldSHA {
			fmt.Printf("The files give %s the statements they gave it at the last APPLY (sha256 %s).\n", sc.only, shortSHA(h))
		} else {
			fmt.Printf("Changes since the last APPLY (sha256 %s to %s):\n", shortSHA(sc.oldSHA), shortSHA(h))
			for _, l := range lineDiff(recipeLines(sc.oldText), recipeLines(text)) {
				fmt.Println("  " + l)
			}
		}
		fmt.Println()
	}
	// The deferred SET IF UNSET lists first: with a DELETE statusline gone,
	// a DELETE statusline.refresh beside it stays (Codex, #213).
	if u := undoStatement(sc.only, sc.deferredIfUnset(undoClauses(sc.old, newStmts))); u != nil {
		keep = append([]located{{file: "undo", path: sc.oldPath, s: u}}, keep...)
	}
	return keep, nil
}

// deferredIfUnset drops the undo of a SET IF UNSET list in the record that
// the playbook does not hold: one of its keys has another value, so the
// list deferred to values the playbook had, which the files never wrote,
// and their dropping it takes nothing away.
func (sc *applyScope) deferredIfUnset(undo []grammar.Clause) []grammar.Clause {
	var drop []grammar.Clause
	for _, s := range sc.old {
		for _, c := range s.Clauses {
			if c.Kind != grammar.SetIfUnset {
				continue
			}
			want := ifUnsetValues(c.Group)
			keys := make([]string, 0, len(want))
			for k := range want {
				keys = append(keys, k)
			}
			live := livePropertyValues(sc.only, keys)
			held := true
			for k, v := range want {
				if live[k] != v {
					held = false
				}
			}
			if held {
				continue
			}
			for _, x := range clauseUndo(c) {
				drop = append(drop, x.undo)
			}
		}
	}
	return slices.DeleteFunc(undo, func(u grammar.Clause) bool {
		return slices.ContainsFunc(drop, func(d grammar.Clause) bool { return reflect.DeepEqual(d, u) })
	})
}

// writeApplyRecords records, for each playbook the files gave name-less
// statements, the files and what those statements wrote. A playbook that
// updates from a [play] or [source] record is left to it, a linked
// playbook's manifest belongs to its target, a directory has no manifest,
// and files from a pipe cannot be read again: none is recorded, each but
// the directory with a note.
func writeApplyRecords(st *grammar.Stmt, to string, stmts []located) error {
	var names []string
	for _, x := range stmts {
		if x.recipe && x.s.Dir == "" && !slices.Contains(names, x.s.Name) {
			names = append(names, x.s.Name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	files := make([]string, 0, len(st.Files))
	for _, f := range st.Files {
		info, err := os.Stat(f)
		if err == nil && !info.Mode().IsRegular() {
			fmt.Printf("Note: %s is not a regular file (a pipe): cpb update cannot read it again, so nothing is recorded for it.\n", f)
			return nil
		}
		id, err := fileIdentity(f)
		if err != nil {
			return fmt.Errorf("applied, but the [apply] record for cpb update was not written: %v", err)
		}
		files = append(files, id)
	}
	store := config.ResolvePlaybooksDir()
	for _, name := range names {
		pb, err := playbook.Find(store, name)
		if err != nil {
			return fmt.Errorf("applied, but the [apply] record for cpb update was not written: %v", err)
		}
		if pb == nil {
			continue // dropped later in the files
		}
		if info, err := os.Lstat(pb.RootPath); err == nil && info.Mode()&os.ModeSymlink != 0 {
			fmt.Printf("Note: %s is linked, and its manifest belongs to the target, so this APPLY is not recorded for cpb update.\n", name)
			continue
		}
		if m := pb.Manifest; m != nil && (m.Play != nil || (m.Source != nil && m.Source.Repository != "")) {
			from := "[source]"
			if m.Play != nil {
				from = "[play] record"
			}
			fmt.Printf("Note: %s updates from its %s, so this APPLY is not recorded for cpb update.\n", name, from)
			continue
		}
		// Under TO, every name-less statement is the TO playbook's.
		if err := writeApplyRecord(pb, files, to == name, applyRecordText(targetStatements(stmts, name))); err != nil {
			return fmt.Errorf("applied, but the [apply] record of %s for cpb update was not written: %v", name, err)
		}
	}
	return nil
}

// writeApplyRecord stores one playbook's record and its [apply] entry.
func writeApplyRecord(pb *playbook.Playbook, files []string, to bool, text []byte) error {
	root := pb.RootPath
	if root == "" {
		root = pb.Path
	}
	if err := os.MkdirAll(filepath.Join(root, filepath.Dir(applyRecordFile)), 0o700); err != nil {
		return err
	}
	if err := manifest.WritePrivate(filepath.Join(root, applyRecordFile), text, 0o600); err != nil {
		return err
	}
	m, err := manifest.Read(root)
	if err != nil {
		return err
	}
	if m == nil {
		m = &manifest.Manifest{Name: filepath.Base(root)}
	}
	if m.Apply != nil && !slices.Equal(m.Apply.Files, files) {
		fmt.Printf("Note: cpb update %s now applies %s again (it applied %s).\n", pb.Name, strings.Join(files, ", "), strings.Join(m.Apply.Files, ", "))
	}
	sum := sha256.Sum256(text)
	m.Apply = &manifest.Apply{Files: files, To: to, SHA256: hex.EncodeToString(sum[:]), AppliedAt: time.Now().UTC().Format(time.RFC3339)}
	return manifest.Write(root, m)
}

// applyUpdateRun applies a playbook's recorded files again: only the
// name-less statements they give it, after the undo (narrow). The record is
// rewritten when the run succeeds.
func applyUpdateRun(pb *playbook.Playbook, dryRun, jsonOut bool) error {
	rec := pb.Manifest.Apply
	root := pb.RootPath
	if root == "" {
		root = pb.Path
	}
	oldPath := filepath.Join(root, applyRecordFile)
	old, err := os.ReadFile(oldPath)
	if err != nil {
		return fmt.Errorf("%s: the record of what APPLY wrote is missing (%s): APPLY its files again", pb.Name, applyRecordFile)
	}
	oldStmts, err := grammar.ParseFile(string(old))
	if err != nil {
		return fmt.Errorf("%s: the record of what APPLY wrote (%s) does not parse: %v; APPLY its files again", pb.Name, applyRecordFile, err)
	}
	if len(rec.Files) == 0 {
		return fmt.Errorf("%s: its [apply] record names no files: APPLY its files again", pb.Name)
	}
	sc := &applyScope{record: true, only: pb.Name, oldPath: oldPath, oldText: old, old: oldStmts, oldSHA: rec.SHA256, showDiff: !jsonOut}
	st := &grammar.Stmt{Verb: grammar.Apply, Files: rec.Files, DryRun: dryRun, JSON: jsonOut}
	if rec.To {
		st.Target = pb.Name
	}
	if jsonOut {
		return runApplyJSONIn(st, sc)
	}
	return applyRunIn(st, nil, sc)
}
