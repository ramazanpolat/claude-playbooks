package cmd

import (
	"strconv"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
)

// Folding (SPEC.md, "A setting set more than once"). A stack of files can set
// one key of one target several times: a base recipe sets the model, the
// child sets it again. Run in order, APPLY wrote the base's value and then
// the child's on every run, so a stack never converged (each re-apply
// reported changes) and the live config held the base's value in between.
// Before anything runs, APPLY therefore drops a clause, or one entry of a
// list clause, whose key the same target writes again later
// unconditionally, and the plan names the statement that wins.

// foldEffect is one key a clause part acts on, and whether it replaces the
// key whatever it held (write) or depends on what it holds.
type foldEffect struct {
	key   string
	write bool
}

// foldPart is one part of a clause: an entry of a list clause (a variable, a
// tool rule, a sandbox setting), or the whole clause (index -1).
type foldPart struct {
	index   int
	what    string // the clause and its key, never a value: "SET VAR LOG_LEVEL"
	effects []foldEffect
	// withFirst: dropped only together with the clause's part 0 (UNSET
	// STATUSLINE removes the whole status line, so its refresh part alone
	// cannot be left out while its command part runs).
	withFirst bool
}

// foldParts lists a clause's parts. A clause with no parts acts on nothing
// folding tracks: it always runs, and never drops another.
func foldParts(c grammar.Clause) []foldPart {
	w := func(k string) foldEffect { return foldEffect{key: k, write: true} }
	m := func(k string) foldEffect { return foldEffect{key: k} }
	var out []foldPart
	switch c.Kind {
	case grammar.SetVar, grammar.SetRef:
		for i, v := range c.Vars {
			out = append(out, foldPart{index: i, what: "SET VAR " + v.Key, effects: []foldEffect{w("var:" + v.Key)}})
		}
	case grammar.BlockVar, grammar.UnsetVar:
		for i, k := range c.Keys {
			out = append(out, foldPart{index: i, what: string(c.Kind) + " VAR " + k, effects: []foldEffect{w("var:" + k)}})
		}
	case grammar.AllowTool, grammar.DenyTool, grammar.UnsetTool:
		for i, r := range c.Names {
			out = append(out, foldPart{index: i, what: string(c.Kind) + " '" + r + "'", effects: []foldEffect{w("tool:" + r)}})
		}
	case grammar.SetSandboxKeys, grammar.UnsetSandboxKeys:
		verb := "SET SANDBOX "
		if c.Kind == grammar.UnsetSandboxKeys {
			verb = "UNSET SANDBOX "
		}
		for i, v := range c.Settings {
			out = append(out, foldPart{index: i, what: verb + v.Key, effects: []foldEffect{w("sandbox:" + v.Key)}})
		}
	case grammar.ModifySetting, grammar.ResetSetting:
		for i, v := range c.Settings {
			out = append(out, foldPart{index: i, what: string(c.Kind) + " " + v.Key, effects: []foldEffect{w("setting:" + v.Key)}})
		}
	case grammar.SetModel, grammar.UnsetModel:
		out = append(out, foldPart{index: -1, what: string(c.Kind), effects: []foldEffect{w("model")}})
	case grammar.SetAgent, grammar.UnsetAgent:
		out = append(out, foldPart{index: -1, what: string(c.Kind), effects: []foldEffect{w("agent")}})
	case grammar.SetStatusline:
		switch {
		case c.IfUnset: // depends on whether a status line is set
			effects := []foldEffect{m("statusline")}
			if c.Refresh > 0 {
				effects = append(effects, m("statusline-refresh"))
			}
			out = append(out, foldPart{index: -1, what: string(c.Kind), effects: effects})
		case c.Refresh > 0: // the command and the refresh, apart
			out = append(out, foldPart{index: 0, what: "SET STATUSLINE", effects: []foldEffect{w("statusline")}},
				foldPart{index: 1, what: "SET STATUSLINE REFRESH", effects: []foldEffect{w("statusline-refresh")}})
		default:
			out = append(out, foldPart{index: -1, what: string(c.Kind), effects: []foldEffect{w("statusline")}})
		}
	case grammar.UnsetStatusline:
		out = append(out, foldPart{index: 0, what: "UNSET STATUSLINE", effects: []foldEffect{w("statusline")}},
			foldPart{index: 1, what: "UNSET STATUSLINE REFRESH", effects: []foldEffect{w("statusline-refresh")}, withFirst: true})
	case grammar.SetStatuslineRefresh, grammar.UnsetStatuslineRefresh:
		out = append(out, foldPart{index: -1, what: string(c.Kind), effects: []foldEffect{w("statusline-refresh")}})
	case grammar.SetStatuslinePrevious: // reads the history every earlier one wrote (see foldStatements)
		out = append(out, foldPart{index: -1, what: string(c.Kind), effects: []foldEffect{m("statusline"), m("statusline-refresh")}})
	case grammar.UseEnv:
		out = append(out, foldPart{index: -1, what: "USE ENV", effects: []foldEffect{w("envs")}})
	case grammar.AddEnv, grammar.DropEnv: // relative to the list as it stands
		out = append(out, foldPart{index: -1, what: string(c.Kind), effects: []foldEffect{m("envs")}})
	}
	return out
}

// foldTarget is the target whose settings a statement alters ("" for a
// statement folding leaves alone), and the targets it starts afresh: a
// playbook created, dropped or renamed is not the one earlier statements
// configured.
func foldTarget(s *grammar.Stmt) (target string, barriers []string) {
	switch {
	case s.Dir != "":
		return "dir:" + s.Dir, nil
	case s.Object != grammar.Playbook:
		return "", nil
	case s.Verb == grammar.Create || s.Verb == grammar.Drop:
		return "", []string{"pb:" + s.Name}
	case s.Verb == grammar.Alter:
		for _, c := range s.Clauses {
			if c.Kind == grammar.RenameTo {
				return "", []string{"pb:" + s.Name, "pb:" + c.Arg}
			}
		}
		return "pb:" + s.Name, nil
	}
	return "", nil
}

// overriddenPart is a part folding dropped, and the statement whose part
// replaces it.
type overriddenPart struct {
	what string
	by   int // index of that statement
}

type foldResult struct {
	stmts []located
	over  [][]overriddenPart // per statement, the parts dropped from it
	skip  []bool             // every clause dropped: nothing to run
}

// foldStatements drops every part whose key is written again later,
// unconditionally, for the same target. It repeats until nothing changes:
// a write a dropped dependant needed is then dropped too.
func foldStatements(in []located) foldResult {
	type partKey struct{ i, j, p int }
	type occ struct {
		part  partKey
		key   string
		write bool
		sl    bool // a status line key: a later live PREVIOUS keeps it
	}
	parts := make([][][]foldPart, len(in))
	keyOf := make([]string, len(in))
	epoch := map[string]int{}
	for i, x := range in {
		t, barriers := foldTarget(x.s)
		for _, b := range barriers {
			epoch[b]++
		}
		if t == "" {
			continue
		}
		keyOf[i] = t + "#" + strconv.Itoa(epoch[t]) + "|"
		parts[i] = make([][]foldPart, len(x.s.Clauses))
		for j, c := range x.s.Clauses {
			parts[i][j] = foldParts(c)
		}
	}
	dead := map[partKey]bool{}
	previous := map[partKey]bool{}  // a SET STATUSLINE PREVIOUS
	withFirst := map[partKey]bool{} // dropped only with the clause's part 0
	var all []occ                   // every effect, in run order
	for i, x := range in {
		for j := range parts[i] {
			for _, fp := range parts[i][j] {
				pk := partKey{i, j, fp.index}
				previous[pk] = x.s.Clauses[j].Kind == grammar.SetStatuslinePrevious
				withFirst[pk] = fp.withFirst
				for _, e := range fp.effects {
					all = append(all, occ{pk, keyOf[i] + e.key, e.write, strings.HasPrefix(e.key, "statusline")})
				}
			}
		}
	}
	for changed := true; changed; {
		changed = false
		live := all[:0:0]
		for _, o := range all {
			if !dead[o.part] {
				live = append(live, o)
			}
		}
		// SET STATUSLINE PREVIOUS restores from the history, which every
		// earlier replacement of the target's status line adds to, not only
		// the last: while a PREVIOUS runs, no status line clause before it is
		// dropped. Taken from the live clauses on every pass, so a PREVIOUS
		// that is itself dropped keeps nothing.
		lastPrevious := map[string]int{}
		for _, o := range live {
			if previous[o.part] {
				lastPrevious[keyOf[o.part.i]] = o.part.i
			}
		}
		// A part is dead when the next live occurrence of each of its keys
		// is a write.
		nextWrite := make([]bool, len(live))
		seen := map[string]int{}
		for k := len(live) - 1; k >= 0; k-- {
			if n, ok := seen[live[k].key]; ok {
				nextWrite[k] = live[n].write
			}
			seen[live[k].key] = k
		}
		verdict := map[partKey]bool{}
		for k, o := range live {
			last, ok := lastPrevious[keyOf[o.part.i]]
			pinned := o.sl && ok && o.part.i < last
			d, seenPart := verdict[o.part]
			verdict[o.part] = (d || !seenPart) && nextWrite[k] && !pinned
		}
		for p := range verdict {
			if withFirst[p] {
				verdict[p] = verdict[p] && (verdict[partKey{p.i, p.j, 0}] || dead[partKey{p.i, p.j, 0}])
			}
		}
		for p, d := range verdict {
			if d && !dead[p] {
				dead[p], changed = true, true
			}
		}
	}

	// The statement that replaced a dropped part: the next live occurrence of
	// its first key.
	winner := func(p partKey) int {
		var key string
		pos := -1
		for k, o := range all {
			if o.part == p && pos < 0 {
				key, pos = o.key, k
			}
		}
		for _, o := range all[pos+1:] {
			if o.key == key && !dead[o.part] {
				return o.part.i
			}
		}
		return p.i
	}

	res := foldResult{stmts: make([]located, len(in)), over: make([][]overriddenPart, len(in)), skip: make([]bool, len(in))}
	copy(res.stmts, in)
	for i, x := range in {
		if keyOf[i] == "" {
			continue
		}
		var kept []grammar.Clause
		changedStmt := false
		for j, c := range x.s.Clauses {
			fps := parts[i][j]
			if len(fps) == 0 {
				kept = append(kept, c)
				continue
			}
			var keep []int
			for _, fp := range fps {
				pk := partKey{i, j, fp.index}
				if dead[pk] {
					res.over[i] = append(res.over[i], overriddenPart{fp.what, winner(pk)})
					changedStmt = true
				} else {
					keep = append(keep, fp.index)
				}
			}
			switch {
			case len(keep) == len(fps):
				kept = append(kept, c)
			case len(keep) == 0:
			default: // a list clause keeps its live entries
				kept = append(kept, keepEntries(c, keep))
			}
		}
		if !changedStmt {
			continue
		}
		s := *x.s
		s.Clauses = kept
		res.stmts[i].s = &s
		res.skip[i] = len(kept) == 0
	}
	return res
}

// keepEntries is a list clause with only the entries at keep.
func keepEntries(c grammar.Clause, keep []int) grammar.Clause {
	out := c
	switch c.Kind {
	case grammar.SetStatusline: // one of the command (0) and the refresh (1)
		if keep[0] == 0 {
			out.Refresh = 0 // the command alone keeps the refresh it finds
		} else {
			out.Kind, out.Arg = grammar.SetStatuslineRefresh, ""
		}
	case grammar.UnsetStatusline: // the refresh only (see withFirst)
		out.Kind = grammar.UnsetStatuslineRefresh
	case grammar.SetVar, grammar.SetRef:
		out.Vars = pick(c.Vars, keep)
	case grammar.BlockVar, grammar.UnsetVar:
		out.Keys = pick(c.Keys, keep)
	case grammar.AllowTool, grammar.DenyTool, grammar.UnsetTool:
		out.Names = pick(c.Names, keep)
	case grammar.SetSandboxKeys, grammar.UnsetSandboxKeys, grammar.ModifySetting, grammar.ResetSetting:
		out.Settings = pick(c.Settings, keep)
	}
	return out
}

func pick[T any](list []T, keep []int) []T {
	out := make([]T, 0, len(keep))
	for _, k := range keep {
		out = append(out, list[k])
	}
	return out
}

// overriddenNote is the plan's line for what folding dropped from a
// statement: each part, and where the value that replaces it is set.
func overriddenNote(over []overriddenPart, stmts []located) string {
	parts := make([]string, 0, len(over))
	for _, o := range over {
		w := stmts[o.by]
		parts = append(parts, o.what+" (set again at "+w.file+":"+strconv.Itoa(w.s.Pos.Line)+")")
	}
	return "overridden: " + strings.Join(parts, ", ")
}
