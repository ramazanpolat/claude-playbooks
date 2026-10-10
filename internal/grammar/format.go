package grammar

import (
	"strconv"
	"strings"
)

// String renders the statement in canonical form: keywords in capitals, one
// line, words quoted playbook-file style where they need it. Parsing the result
// yields the same statement, which is what lets SHOW CREATE emit statements
// and hints print the grammar form of a short-form command.
func (s *Stmt) String() string {
	w := s.headWords()
	// VAR is required inside ALTER PLAYBOOK and optional in an env set,
	// where the canonical form leaves it out.
	varWord := s.Object == Playbook
	for _, c := range mergedClauses(s.Clauses) {
		w = append(w, c.words(varWord)...)
	}
	return strings.Join(w, " ")
}

// Pretty renders the statement for a playbook file: the head on one line and
// each clause on its own, indented. It parses back to the same statement.
func (s *Stmt) Pretty() string {
	var b strings.Builder
	b.WriteString(strings.Join(s.headWords(), " "))
	varWord := s.Object == Playbook
	for _, c := range mergedClauses(s.Clauses) {
		b.WriteString("\n  ")
		b.WriteString(strings.Join(c.words(varWord), " "))
	}
	return b.String()
}

// mergedClauses renders side by side property clauses as one list: the SET
// pairs of clauses in a row as one SET, their DELETE keys as one DELETE,
// as written. Each clause stays what it is: the merged clause only renders.
func mergedClauses(in []Clause) []Clause {
	var out []Clause
	for _, c := range in {
		if n := len(out); n > 0 {
			last := &out[n-1]
			switch {
			case c.pairWords() != nil && last.Kind == mergedSet:
				last.Group = append(last.Group, c)
				continue
			case c.pairWords() != nil && last.pairWords() != nil:
				*last = Clause{Kind: mergedSet, Group: []Clause{*last, c}}
				continue
			case c.deleteKeys() != nil && last.Kind == mergedDelete:
				last.Group = append(last.Group, c)
				continue
			case c.deleteKeys() != nil && last.deleteKeys() != nil:
				*last = Clause{Kind: mergedDelete, Group: []Clause{*last, c}}
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

// mergedSet and mergedDelete are the lists mergedClauses renders; never
// parsed, never run.
const (
	mergedSet    Kind = "SET <pairs>"
	mergedDelete Kind = "DELETE <keys>"
)

// deleteKeys lists the keys a DELETE clause names, without DELETE: nil for
// any other clause.
func (c *Clause) deleteKeys() []string {
	switch c.Kind {
	case DeleteProperties:
		var keys []string
		for _, v := range c.Settings {
			keys = append(keys, v.Key)
		}
		return keys
	case UnsetSandboxKeys:
		var keys []string
		for _, v := range c.Settings {
			keys = append(keys, "sandbox."+v.Key)
		}
		return keys
	case DefaultLauncher, UnsetModel, UnsetAgent, UnsetStatusline, UnsetStatuslineRefresh, UnsetModelPickerMode, UnsetHelper:
		return strings.Fields(string(c.Kind))[1:]
	}
	return nil
}

func (s *Stmt) headWords() []string {
	w := []string{string(s.Verb)}
	switch s.Verb {
	case Create:
		if s.OrReplace {
			w = append(w, "OR", "REPLACE")
		}
		w = append(w, string(s.Object))
		if s.IfNotExists {
			w = append(w, "IF", "NOT", "EXISTS")
		}
		w = append(w, quoteWord(s.Name))
	case Alter:
		w = append(w, string(s.Object))
		if s.Object != Defaults && !s.Recipe {
			w = append(w, quoteWord(s.Name))
		}
	case Use:
		w = append(w, string(s.Object), quoteWord(s.Name))
	case Drop:
		w = append(w, string(s.Object))
		if s.IfExists {
			w = append(w, "IF", "EXISTS")
		}
		w = append(w, quoteWord(s.Name))
		if s.Yes {
			w = append(w, "--yes")
		}
	case Show:
		if s.ShowCreate {
			w = append(w, "CREATE")
		}
		w = append(w, string(s.Object))
		if s.Name != "" {
			w = append(w, quoteWord(s.Name))
		}
		if s.SkipSecrets {
			w = append(w, "--skip-secrets")
		}
		if s.JSON {
			w = append(w, "--json")
		}
	case Explain:
		w = append(w, string(s.Object), quoteWord(s.Name))
		if s.JSON {
			w = append(w, "--json")
		}
	case Include:
		w = append(w, quote(s.Files[0]))
	case Apply:
		for _, f := range s.Files {
			w = append(w, quoteWord(f))
		}
		if s.Target != "" {
			w = append(w, "TO", quoteWord(s.Target))
		}
		if s.DryRun {
			w = append(w, "--dry-run")
		}
		if s.Yes {
			w = append(w, "--yes")
		}
	}
	return w
}

func (c *Clause) words(varWord bool) []string {
	kw := func(k string) []string {
		if varWord {
			return []string{k, "VAR"}
		}
		return []string{k}
	}
	switch c.Kind {
	case SetVar:
		w := kw("SET")
		for _, v := range c.Vars {
			w = append(w, v.Key+"="+quoteValue(v.Value))
		}
		if c.Plaintext {
			w = append(w, "AS", "PLAINTEXT")
		}
		return w
	case SetRef:
		return append(kw("SET"), quoteWord(c.Vars[0].Key), "FROM", quote(c.Vars[0].Ref))
	case BlockVar, UnsetVar:
		w := kw(string(c.Kind))
		for _, k := range c.Keys {
			w = append(w, quoteWord(k))
		}
		return w
	case Description:
		return []string{"DESCRIPTION", quote(c.Arg)}
	case UseEnv, DropEnv:
		w := strings.Fields(string(c.Kind))
		for _, n := range c.Names {
			w = append(w, quoteWord(n))
		}
		return w
	case AddEnv:
		w := []string{"ADD", "ENV", quoteWord(c.Names[0])}
		switch c.Where {
		case First:
			w = append(w, "FIRST")
		case Before, After:
			w = append(w, string(c.Where), quoteWord(c.Anchor))
		}
		return w
	case AddMarketplace:
		return []string{"ADD", "MARKETPLACE", quoteWord(c.Names[0]), "FROM", quote(c.Arg)}
	case DropMarketplace, AddPlugin, DropPlugin:
		return append(strings.Fields(string(c.Kind)), quoteWord(c.Names[0]))
	case AllowTool, DenyTool, UnsetTool:
		w := strings.Fields(string(c.Kind))
		for _, r := range c.Names {
			w = append(w, quote(r))
		}
		return w
	case SetStatusline, SetStatuslineRefresh, SetModel, Launcher, NoLauncher, SetAgent, SetHelper,
		SetModelPicker, SetSandboxKeys, SetProperties:
		return append([]string{"SET"}, c.pairWords()...)
	case mergedSet, SetIfUnset:
		// One SET IF UNSET, whatever clauses its pairs became: it applies
		// whole or not at all.
		w := []string{"SET"}
		if c.Kind == SetIfUnset {
			w = append(w, "IF", "UNSET")
		}
		for i, g := range c.Group {
			pw := g.pairWords()
			if i < len(c.Group)-1 {
				pw[len(pw)-1] += ","
			}
			w = append(w, pw...)
		}
		return w
	case mergedDelete:
		var keys []string
		for _, g := range c.Group {
			keys = append(keys, g.deleteKeys()...)
		}
		w := []string{"DELETE"}
		for i, k := range keys {
			if i < len(keys)-1 {
				k += ","
			}
			w = append(w, k)
		}
		return w
	case DropSkill:
		return []string{"DROP", "SKILL", quoteWord(c.Names[0])}
	case DropModel:
		return []string{"DROP", "MODEL", quote(c.Names[0])}
	case AddModel:
		w := []string{"ADD", "MODEL", quote(c.Row.Model)}
		if c.Row.Label != nil {
			w = append(w, "LABEL", quote(*c.Row.Label))
		}
		if c.Row.Description != nil {
			w = append(w, "DESCRIPTION", quote(*c.Row.Description))
		}
		if c.Row.BehavesAs != nil {
			w = append(w, "BEHAVES", "AS", quote(*c.Row.BehavesAs))
		}
		return w
	case AddSkill:
		w := []string{"ADD", "SKILL", quoteWord(c.Names[0]), "FROM", quote(c.Skill.From)}
		if c.Skill.Branch != "" {
			w = append(w, "BRANCH", quote(c.Skill.Branch))
		}
		if c.Skill.Subdir != "" {
			w = append(w, "SUBDIR", quote(c.Skill.Subdir))
		}
		return w
	case DropMCP:
		return []string{"DROP", "MCP", "SERVER", quoteWord(c.Names[0])}
	case AddMCP:
		w := []string{"ADD", "MCP", "SERVER", quoteWord(c.Names[0])}
		m := c.MCP
		if m.URL != "" {
			w = append(w, "URL", quote(m.URL))
			if m.SSE {
				w = append(w, "TRANSPORT", "SSE")
			}
		} else {
			w = append(w, "COMMAND", quote(m.Command))
			if len(m.Args) > 0 {
				w = append(w, "ARGS")
				for _, a := range m.Args {
					w = append(w, quote(a))
				}
			}
		}
		for _, v := range m.Env {
			if v.Ref != "" {
				w = append(w, "VAR", quoteWord(v.Key), "FROM", quote(v.Ref))
			} else {
				w = append(w, "VAR", v.Key+"="+quoteValue(v.Value))
			}
		}
		for _, h := range m.Headers {
			if h.Ref != "" {
				w = append(w, "HEADER", quote(h.Key), "FROM", quote(h.Ref))
			} else {
				w = append(w, "HEADER", quote(h.Key), quote(h.Value))
			}
		}
		return w
	case RenameTo, From, Branch, Subdir, Link:
		return append(strings.Fields(string(c.Kind)), quoteWord(c.Arg))
	case DeleteProperties:
		return append([]string{"DELETE"}, propertyWords(c, false)...)
	case UnsetSandboxKeys:
		w := []string{"DELETE"}
		for i, v := range c.Settings {
			k := "sandbox." + v.Key
			if i < len(c.Settings)-1 {
				k += ","
			}
			w = append(w, k)
		}
		return w
	default: // SetStatuslinePrevious, DefaultLauncher, UnsetHelper, UnsetAgent, UnsetStatusline, UnsetModel, UnsetModelPickerMode: no argument
		return strings.Fields(string(c.Kind))
	}
}

// pairWords renders the k = v pairs a property clause stands for, without
// SET: what SET and SET IF UNSET write.
func (c *Clause) pairWords() []string {
	pair := func(k, v string) []string { return []string{k, "=", v} }
	switch c.Kind {
	case SetStatusline:
		if c.Refresh > 0 {
			return append(pair("statusline.command", quote(c.Arg)+","), pair("statusline.refresh", strconv.Itoa(c.Refresh))...)
		}
		return pair("statusline.command", quote(c.Arg))
	case SetStatuslineRefresh:
		return pair("statusline.refresh", strconv.Itoa(c.Refresh))
	case SetModel:
		return pair("model", quote(c.Arg))
	case Launcher:
		return pair("launcher", quote(c.Arg))
	case NoLauncher:
		return pair("launcher", "''")
	case SetAgent:
		return pair("agent", quote(c.Arg))
	case SetHelper:
		return pair("secret_helper", quote(c.Arg))
	case SetModelPicker:
		return pair("model_picker.mode", quote(strings.ToLower(c.Arg)))
	case SetSandboxKeys:
		return sandboxWords(c)
	case SetProperties:
		return propertyWords(c, true)
	}
	return nil
}

// quote wraps s in single quotes, doubling any inside.
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// quoteWord quotes a name or argument when it would not read back as the
// same single word: empty, a keyword, a comment start, a trailing comma, or
// a character the lexer treats specially.
func quoteWord(s string) string {
	if s == "" || IsKeyword(s) || strings.HasPrefix(s, "--") || needsQuote(s) {
		return quote(s)
	}
	return s
}

// quoteValue quotes the value part of K=V. A keyword needs no quotes there,
// since K=V is never read as one.
func quoteValue(s string) string {
	if needsQuote(s) {
		return quote(s)
	}
	return s
}

func needsQuote(s string) bool {
	return strings.ContainsAny(s, " \t\r\n'\";") || strings.HasSuffix(s, ",")
}
