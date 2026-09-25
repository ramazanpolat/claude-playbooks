package grammar

import "strings"

// String renders the statement in canonical form: keywords in capitals, one
// line, words quoted setup-file style where they need it. Parsing the result
// yields the same statement, which is what lets SHOW CREATE emit statements
// and hints print the grammar form of a short-form command.
func (s *Stmt) String() string {
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
		if s.Object != Defaults {
			w = append(w, quoteWord(s.Name))
		}
	case Drop:
		w = append(w, string(s.Object))
		if s.IfExists {
			w = append(w, "IF", "EXISTS")
		}
		w = append(w, quoteWord(s.Name))
	case Show:
		if s.ShowCreate {
			w = append(w, "CREATE")
		}
		w = append(w, string(s.Object))
		if s.Name != "" {
			w = append(w, quoteWord(s.Name))
		}
	case Explain:
		w = append(w, string(s.Object), quoteWord(s.Name))
	case Apply:
		w = append(w, quoteWord(s.File))
		if s.DryRun {
			w = append(w, "--dry-run")
		}
	}
	// VAR is required inside ALTER PLAYBOOK and optional in an env set,
	// where the canonical form leaves it out.
	varWord := s.Object == Playbook
	for _, c := range s.Clauses {
		w = append(w, c.words(varWord)...)
	}
	return strings.Join(w, " ")
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
		return w
	case SetRef:
		return append(kw("SET"), quoteWord(c.Vars[0].Key), "FROM", quote(c.Vars[0].Ref))
	case BlockVar, UnsetVar:
		w := kw(string(c.Kind))
		for _, k := range c.Keys {
			w = append(w, quoteWord(k))
		}
		return w
	case Describe:
		return []string{"DESCRIBE", quote(c.Arg)}
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
	case UsePilot, RenameTo, Alias, From, Branch, Subdir, Link:
		return append(strings.Fields(string(c.Kind)), quoteWord(c.Arg))
	default: // DropPilot, NoAlias, Sandbox: no argument
		return strings.Fields(string(c.Kind))
	}
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
