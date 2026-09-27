package grammar

import (
	"regexp"
	"strconv"
	"strings"
)

// Panels (v3.25.0): an ADD PANEL writes one SPC/1 manifest
// (agent-realm/statusmux, docs/spc-1.md) into the config directory's
// statusline.d/<namespace>/. The words of these clauses are not reserved.

var panelWord = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// panelVars are the variables an SPC/1 host expands in a command; SPC/1
// forbids quoting them, since the host quotes each value itself.
var panelVars = []string{"PANEL_DIR", "HOME", "CLAUDE_CONFIG_DIR", "CLAUDE_PLUGIN_ROOT"}

// QuotedPanelVariable reports the first host variable a command puts
// inside quotes of its own ('…${PANEL_DIR}…' or "…${PANEL_DIR}…").
func QuotedPanelVariable(cmd string) string {
	var quote byte
	for i := 0; i < len(cmd); i++ {
		ch := cmd[i]
		switch {
		case quote == 0 && (ch == '\'' || ch == '"'):
			quote = ch
		case quote != 0 && ch == quote:
			quote = 0
		case quote == '"' && ch == '\\':
			i++
		case quote != 0 && ch == '$' && strings.HasPrefix(cmd[i:], "${"):
			for _, v := range panelVars {
				if strings.HasPrefix(cmd[i:], "${"+v+"}") {
					return v
				}
			}
		}
	}
	return ""
}

// panel reads the rest of ADD PANEL <ns>.<id> <type> … or DROP PANEL
// <ns>.<id>.
func (p *parser) panel(c *Clause, w string) *Error {
	c.Kind = AddPanel
	if w == "DROP" {
		c.Kind = DropPanel
	}
	t, err := p.take(w+" PANEL", "<namespace>.<id>")
	if err != nil {
		return err
	}
	ns, id, ok := strings.Cut(t.Text, ".")
	if !ok || !panelWord.MatchString(ns) || !panelWord.MatchString(id) {
		return errAt(t.Pos, "a panel is <namespace>.<id>, each lowercase letters, digits and dashes: local.clock")
	}
	if ns == "project" {
		return errAt(t.Pos, "the project namespace belongs to a repository's own .claude/statusline.d, not to a config directory")
	}
	pn := &Panel{NS: ns, ID: id}
	c.Panel = pn
	if w == "DROP" {
		return nil
	}
	switch p.kw("EXEC", "TEMPLATE", "RECORDS", "OBSERVE", "FROM") {
	case "":
		return p.fail("ADD PANEL needs EXEC '<command>', TEMPLATE '<text>', RECORDS '<path>', OBSERVE '<command>' EVERY <ms>, or FROM STATUSLINE")
	case "FROM":
		if p.kw("STATUSLINE") == "" {
			return p.fail("ADD PANEL … FROM takes STATUSLINE: the current status line's command becomes an exec panel")
		}
		pn.Type, pn.FromStatusline = "exec", true
	case "EXEC", "OBSERVE":
		pn.Type = strings.ToLower(p.toks[p.i-1].Text)
		s, err := p.take(strings.ToUpper(pn.Type), "'<command>'")
		if err != nil {
			return err
		}
		if s.Text == "" {
			return errAt(s.Pos, strings.ToUpper(pn.Type)+" needs a command")
		}
		if v := QuotedPanelVariable(s.Text); v != "" {
			return errAt(s.Pos, "${"+v+"} is inside quotes of its own: SPC/1 hosts quote it themselves, so write it bare (sh ${"+v+"}/x.sh)")
		}
		pn.Source = s.Text
	case "TEMPLATE":
		pn.Type = "template"
		s, err := p.take("TEMPLATE", "'<text>'")
		if err != nil {
			return err
		}
		pn.Source = s.Text
	case "RECORDS":
		pn.Type = "records"
		s, err := p.take("RECORDS", "'<path>'")
		if err != nil {
			return err
		}
		pn.Source = s.Text
	}
	return p.panelOptions(pn)
}

// panelOptions reads the options that follow a panel's type, each once and
// only where SPC/1 gives it a meaning.
func (p *parser) panelOptions(pn *Panel) *Error {
	seen := map[string]bool{}
	allowed := map[string][]string{
		"exec":     {"FORMAT", "ROW", "PRIORITY", "ALIGN", "TIMEOUT", "MAX", "TTL", "STALE", "WIDTH"},
		"template": {"WHEN", "ROW", "PRIORITY", "ALIGN"},
		"records":  {"STALE", "ROW", "PRIORITY", "ALIGN"},
		"observe":  {"EVERY", "MAX"},
	}[pn.Type]
	all := []string{"FORMAT", "ROW", "PRIORITY", "ALIGN", "TIMEOUT", "MAX", "TTL", "STALE", "WIDTH", "WHEN", "EVERY"}
	for {
		w := p.kw(all...)
		if w == "" {
			break
		}
		at := p.toks[p.i-1].Pos
		if seen[w] {
			return errAt(at, w+" appears twice in one ADD PANEL")
		}
		seen[w] = true
		ok := false
		for _, a := range allowed {
			ok = ok || a == w
		}
		if !ok {
			return errAt(at, w+" does not apply to a "+pn.Type+" panel")
		}
		var err *Error
		switch w {
		case "FORMAT":
			switch p.kw("TEXT", "RECORDS") {
			case "TEXT":
				pn.Format = "text"
			case "RECORDS":
				pn.Format = "records"
			default:
				return p.fail("FORMAT takes TEXT or RECORDS")
			}
		case "ALIGN":
			switch p.kw("LEFT", "RIGHT") {
			case "LEFT":
				pn.Align = "left"
			case "RIGHT":
				pn.Align = "right"
			default:
				return p.fail("ALIGN takes LEFT or RIGHT")
			}
		case "WHEN":
			var s Token
			if s, err = p.take("WHEN", "'<field>'"); err == nil {
				pn.When = s.Text
			}
		case "ROW":
			pn.Row, err = p.panelNumber("ROW", 1, 1<<20, "a row, from 1")
		case "PRIORITY":
			pn.Priority, err = p.panelNumber("PRIORITY", 0, 100, "0 to 100")
		case "TIMEOUT":
			pn.Timeout, err = p.panelNumber("TIMEOUT", 1, 1<<30, "milliseconds, at least 1")
		case "MAX":
			if p.kw("RUN") == "" {
				return p.fail("MAX takes RUN: MAX RUN <ms>")
			}
			pn.MaxRun, err = p.panelNumber("MAX RUN", 1, 1<<30, "milliseconds, at least 1")
		case "TTL":
			pn.TTL, err = p.panelNumber("TTL", 0, 1<<30, "milliseconds")
		case "STALE":
			pn.Stale, err = p.panelNumber("STALE", 0, 1<<30, "milliseconds")
		case "WIDTH":
			pn.Width, err = p.panelNumber("WIDTH", 0, 1<<16, "columns (0: the host decides)")
		case "EVERY":
			pn.Every, err = p.panelNumber("EVERY", 1000, 1<<30, "milliseconds, at least 1000")
		}
		if err != nil {
			return err
		}
	}
	if pn.Type == "observe" && pn.Every == nil {
		return p.fail("OBSERVE needs EVERY <ms>: how often the host may start it")
	}
	return nil
}

func (p *parser) panelNumber(what string, lo, hi int, unit string) (*int, *Error) {
	t, err := p.take(what, "<n>")
	if err != nil {
		return nil, err
	}
	n, cerr := strconv.Atoi(t.Text)
	if cerr != nil || n < lo || n > hi || strings.TrimLeft(t.Text, "0123456789") != "" {
		return nil, errAt(t.Pos, what+" takes a whole number: "+unit)
	}
	return &n, nil
}

// panelWords writes an ADD or DROP PANEL clause as it parses back.
func panelWords(c *Clause) []string {
	pn := c.Panel
	w := append(strings.Fields(string(c.Kind)), pn.NS+"."+pn.ID)
	if c.Kind == DropPanel {
		return w
	}
	if pn.FromStatusline {
		// SHOW CREATE writes the command itself: FROM STATUSLINE is how a
		// panel was made, not what it is.
		return append(w, "FROM", "STATUSLINE")
	}
	w = append(w, strings.ToUpper(pn.Type), quote(pn.Source))
	num := func(word string, v *int) {
		if v != nil {
			w = append(w, strings.Fields(word)...)
			w = append(w, strconv.Itoa(*v))
		}
	}
	if pn.Format != "" {
		w = append(w, "FORMAT", strings.ToUpper(pn.Format))
	}
	if pn.When != "" {
		w = append(w, "WHEN", quote(pn.When))
	}
	num("ROW", pn.Row)
	num("PRIORITY", pn.Priority)
	if pn.Align != "" {
		w = append(w, "ALIGN", strings.ToUpper(pn.Align))
	}
	num("TIMEOUT", pn.Timeout)
	num("MAX RUN", pn.MaxRun)
	num("TTL", pn.TTL)
	num("STALE", pn.Stale)
	num("WIDTH", pn.Width)
	num("EVERY", pn.Every)
	return w
}
