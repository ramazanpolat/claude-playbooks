package tui

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Styles are SGR attributes only (no colours), and none at all when
// NoColor is set (NO_COLOR, or the golden tests).
func (m Model) sgr(code, s string) string {
	if m.o.NoColor {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func (m Model) selected(s string) string { return m.sgr("7", s) }
func (m Model) dim(s string) string      { return m.sgr("2", s) }
func (m Model) warn(s string) string     { return m.sgr("1", s) }

var sgrSeq = regexp.MustCompile("\x1b\\[[0-9;]*m")

// width is a line's width on screen, escape sequences not counted.
func width(s string) int { return lipgloss.Width(s) }

// truncate cuts plain text to w columns, ending in … when it cuts.
func truncate(s string, w int) string {
	if utf8.RuneCountInString(s) <= w {
		return s
	}
	if w <= 0 {
		return ""
	}
	r := []rune(s)
	return string(r[:w-1]) + "…"
}

// The screen: a tab line, a rule, the body, a rule, and three lines of
// footer (status, keys, the statement behind the screen).
const chromeLines = 6

func (m Model) bodyHeight() int { return max(1, m.h-chromeLines) }

// View is the program's view: render on the alternate screen.
func (m Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

// render is the screen as text, a pure function of the model.
func (m Model) render() string {
	var body []string
	switch {
	case m.text != nil:
		body = strings.Split(m.text.vp.View(), "\n")
	case m.help:
		body = helpLines
	case !m.loaded && m.err != "":
		body = []string{"", "  cpb could not be read:", "  " + m.err}
	case !m.loaded:
		body = []string{"", "  reading cpb…"}
	case m.detail != nil:
		body = m.detailBody()
	default:
		body = m.listBody()
	}
	h := m.bodyHeight()
	if len(body) > h {
		body = body[:h]
	}
	for len(body) < h {
		body = append(body, "")
	}
	lines := []string{m.tabLine(), m.rule()}
	for _, l := range body {
		lines = append(lines, m.fit(l))
	}
	lines = append(lines, m.rule(), m.fit(m.status()), m.dim(m.fit(m.keys())), m.dim(m.fit("reads: cpb "+m.reads)))
	return strings.Join(lines, "\n")
}

func (m Model) rule() string { return strings.Repeat("─", max(1, m.w)) }

// fit clips a line to the width. A styled line that is too wide loses its
// style: the table fits its rows before styling them.
func (m Model) fit(s string) string {
	if width(s) <= m.w {
		return s
	}
	return truncate(sgrSeq.ReplaceAllString(s, ""), m.w)
}

func (m Model) tabLine() string {
	parts := []string{" cpb "}
	for i, n := range viewNames {
		label := strconv.Itoa(i+1) + " " + n
		if view(i) == m.view && m.detail == nil && m.text == nil {
			parts = append(parts, m.selected("["+label+"]"))
		} else {
			parts = append(parts, " "+label+" ")
		}
	}
	line := strings.Join(parts, " ")
	right := "? help"
	if pad := m.w - width(line) - len(right) - 1; pad > 0 {
		line += strings.Repeat(" ", pad) + right
	}
	return line
}

func (m Model) status() string {
	switch {
	case m.confirm != nil:
		return m.warn(" " + m.short(m.confirm.path) + " exists. Replace it? Type y to replace; any other key keeps it.")
	case m.typing:
		return " filter: " + m.filter + "▏"
	case m.msg != "":
		return " " + m.msg
	case m.err != "":
		return m.warn(" " + m.err)
	case !m.loaded:
		return ""
	}
	s := fmt.Sprintf(" %d playbooks · %d live sessions", len(m.st.Playbooks), len(m.st.Sessions))
	if m.filter != "" {
		s += " · filter " + strconv.Quote(m.filter)
	}
	if !m.lastRead.IsZero() {
		s += " · read " + m.age(m.lastRead.UTC().Format("2006-01-02T15:04:05Z")) + " ago"
	}
	return s
}

func (m Model) keys() string {
	switch {
	case m.text != nil:
		return " ↑↓ scroll  y copy  esc close"
	case m.help:
		return " any key closes help"
	case m.detail != nil:
		return " ←→/1-9 tab  c SHOW CREATE  e export .cpb  y copy  r refresh  esc back  q quit"
	}
	switch m.view {
	case vPlaybooks:
		return " enter open  s sessions  c SHOW CREATE  e export  y copy  / filter  q quit"
	case vSessions:
		if m.recent {
			return " enter RESUME  R live sessions  y copy statement  / filter  r refresh  q quit"
		}
		return " R recent here (to resume)  y copy RESUME  / filter  r refresh  q quit"
	case vEnvs:
		return " enter/c SHOW CREATE  e export .cpb  y copy  / filter  q quit"
	}
	return " 1-5 views · r refresh · q quit"
}

var helpLines = []string{
	"",
	"  cpb tui: a view of what the cpb grammar reads. It changes nothing (v1).",
	"",
	"  1-5          Playbooks, Sessions, Env sets, Defaults, Log",
	"  ↑↓ j k       move          enter     open",
	"  / filter     r refresh     q quit    esc back",
	"  c            SHOW CREATE of the selection (--skip-secrets)",
	"  e            export it as <name>.cpb here (asks before replacing a file)",
	"  y            copy the statement behind the selection",
	"  R            Sessions: recent sessions in this folder; enter resumes one",
	"",
	"  Secret values are never shown: references appear as references,",
	"  plaintext credentials as (redacted). Every screen names the cpb",
	"  statement it reads, so anything here can be scripted.",
}

// table lays out rows under headers in the width, the flex column taking
// what is left. When the columns do not fit, those in drop go first, in
// order, so the ones that answer "which" and "where" stay. The cursor row
// is marked, and the window scrolls to it.
func (m Model) table(headers []string, rows [][]string, flex, cursor, height int, drop ...int) []string {
	keep := make([]bool, len(headers))
	for i := range keep {
		keep[i] = true
	}
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = width(h)
	}
	for _, r := range rows {
		for i, c := range r {
			if w := width(c); w > widths[i] {
				widths[i] = w
			}
		}
	}
	fixed := func() int {
		t := 2
		for i, w := range widths {
			if keep[i] && i != flex {
				t += w + 2
			}
		}
		return t
	}
	minFlex := 0
	if flex >= 0 {
		minFlex = min(widths[flex], 12)
	}
	for _, d := range drop {
		if fixed()+minFlex <= m.w {
			break
		}
		keep[d] = false
	}
	if flex >= 0 {
		widths[flex] = max(minFlex, min(widths[flex], m.w-fixed()))
	}
	line := func(cells []string) string {
		var parts []string
		for i, c := range cells {
			if keep[i] {
				t := truncate(c, widths[i])
				parts = append(parts, t+strings.Repeat(" ", max(0, widths[i]-width(t))))
			}
		}
		return strings.TrimRight(strings.Join(parts, "  "), " ")
	}
	out := []string{"  " + line(headers)}
	start := 0
	visible := height - 1
	if cursor >= visible {
		start = cursor - visible + 1
	}
	for i := start; i < len(rows) && i < start+visible; i++ {
		l := line(rows[i])
		if i == cursor {
			out = append(out, m.selected(truncate("▸ "+l, m.w)))
		} else {
			out = append(out, "  "+l)
		}
	}
	return out
}

// kindShort is a session kind in the table: int for interactive.
func kindShort(k string) string {
	if k == "interactive" {
		return "int"
	}
	return k
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func (m Model) listBody() []string {
	h := m.bodyHeight()
	switch m.view {
	case vPlaybooks:
		rows := [][]string{}
		for _, p := range m.playbookRows() {
			rows = append(rows, []string{p.Name, orDash(deref(p.Launcher)), orDash(deref(p.Version)),
				orDash(strings.Join(p.Envs, ", ")), p.Login(), strconv.Itoa(len(m.sessionsOf(p.Name))), orDash(deref(p.Model))})
		}
		if len(rows) == 0 {
			return []string{"", "  No playbooks. Create one: cpb CREATE PLAYBOOK <name>"}
		}
		return m.table([]string{"NAME", "LAUNCHER", "VERSION", "ENV SETS", "LOGIN", "SESSIONS", "MODEL"}, rows, 3, m.cursor[vPlaybooks], h, 2, 6, 1)
	case vSessions:
		if m.recent {
			rows := [][]string{}
			for _, r := range m.recentRows() {
				live := "-"
				if r.Live && r.PID != nil {
					live = "pid " + strconv.Itoa(*r.PID)
				}
				rows = append(rows, []string{shortID(r.SessionID), r.Playbook, m.age(r.LastActive), orDash(deref(r.Model)), live, orDash(deref(r.Title))})
			}
			if len(rows) == 0 {
				return []string{"", "  No Claude Code session was found in " + m.short(m.o.Cwd) + "."}
			}
			return m.table([]string{"SESSION", "PLAYBOOK", "ACTIVE", "MODEL", "LIVE", "TITLE (recent, in " + m.short(m.o.Cwd) + ")"}, rows, 5, m.cursor[vSessions], h, 3)
		}
		rows := [][]string{}
		for _, s := range m.sessionRows() {
			rows = append(rows, []string{s.Playbook, strconv.Itoa(s.PID), orDash(deref(s.TTY)), kindShort(s.Kind), orDash(deref(s.Status)),
				m.age(s.StartedAt), m.age(deref(s.LastActive)), orDash(deref(s.Model)), m.short(s.Cwd)})
		}
		if len(rows) == 0 {
			return []string{"", "  No live Claude Code sessions. R lists the recent ones in this folder."}
		}
		return m.table([]string{"PLAYBOOK", "PID", "TTY", "KIND", "STATUS", "AGE", "ACTIVE", "MODEL", "CWD"}, rows, 8, m.cursor[vSessions], h, 4, 3, 6, 7)
	case vEnvs:
		rows := [][]string{}
		for _, e := range m.envRows() {
			refs := 0
			for _, v := range e.Vars {
				if v.Ref != nil {
					refs++
				}
			}
			vars := strconv.Itoa(len(e.Vars))
			if refs > 0 {
				vars += fmt.Sprintf(" (%d ref)", refs)
			}
			def := "-"
			if e.Default {
				def = "yes"
			}
			rows = append(rows, []string{e.Name, vars, orDash(strings.Join(e.UsedBy, ", ")), def, e.Description})
		}
		if len(rows) == 0 {
			return []string{"", "  No env sets (env profiles). Create one: cpb CREATE ENV <name> SET …"}
		}
		return m.table([]string{"NAME", "VARS", "USED BY", "DEFAULT", "DESCRIPTION"}, rows, 4, m.cursor[vEnvs], h, 3)
	case vDefaults:
		helper := "(none)"
		if hp := m.st.Defaults.SecretHelper; hp != nil {
			helper = hp.Command + "  (from " + hp.From + ")"
		}
		envs := "(none)"
		if len(m.st.Defaults.Envs) > 0 {
			var n []string
			for i, e := range m.st.Defaults.Envs {
				n = append(n, strconv.Itoa(i+1)+" "+e)
			}
			envs = strings.Join(n, ", ")
		}
		return []string{"", "  Env sets under every playbook:  " + envs, "  Secret helper:                  " + helper}
	case vLog:
		if len(m.log) == 0 {
			return []string{"", "  Nothing yet: copies, exports and resumes of this session appear here."}
		}
		out := []string{""}
		for _, l := range m.log {
			out = append(out, "  "+l)
		}
		return out
	}
	return nil
}

func (m Model) playbook(name string) (Playbook, bool) {
	for _, p := range m.st.Playbooks {
		if p.Name == name {
			return p, true
		}
	}
	return Playbook{}, false
}

func (m Model) detailBody() []string {
	d := m.detail
	p, ok := m.playbook(d.pb)
	if !ok {
		return []string{"", "  playbook " + d.pb + " is gone (r refreshes)"}
	}
	title := " " + p.Name + "  ("
	if p.Launcher != nil {
		title += "launcher " + *p.Launcher + " · "
	}
	title += m.short(p.Path) + ")"
	var tabs []string
	for i, t := range detailTabs {
		if i == d.tab {
			tabs = append(tabs, m.selected("["+t+"]"))
		} else {
			tabs = append(tabs, " "+t+" ")
		}
	}
	out := []string{title, " " + strings.Join(tabs, ""), ""}
	body := m.tabBody(p, detailTabs[d.tab])
	if d.scroll < len(body) {
		body = body[d.scroll:]
	}
	return append(out, body...)
}

func kv(k, v string) string { return fmt.Sprintf("  %-15s %s", k, v) }

func (m Model) tabBody(p Playbook, tab string) []string {
	h := m.bodyHeight() - 3
	switch tab {
	case "Overview":
		src := "(none)"
		if p.Source != nil {
			src = p.Source.URL
			if p.Source.Branch != nil {
				src += " (branch " + *p.Source.Branch + ")"
			}
		} else if p.Linked != nil {
			src = "(linked) " + m.short(*p.Linked)
		}
		pilot := map[string]string{"imported": "imported (CLAUDE.md imports ~/.pilot-profile/)",
			"not_imported": "not imported", "unknown": "unknown (CLAUDE.md cannot be read)"}[p.PilotProfile]
		if pilot == "" {
			pilot = "- (this cpb does not report it)"
		}
		live := m.sessionsOf(p.Name)
		liveS := "none"
		if len(live) > 0 {
			liveS = fmt.Sprintf("%d · newest %s ago in %s", len(live), m.age(live[0].StartedAt), m.short(live[0].Cwd))
		}
		tools := "-"
		if n := len(p.Tools.Allow) + len(p.Tools.Deny); n > 0 {
			tools = fmt.Sprintf("%d allowed, %d denied", len(p.Tools.Allow), len(p.Tools.Deny))
		}
		return []string{
			kv("Version", orDash(deref(p.Version))),
			kv("Source", src),
			kv("Login", p.Login()),
			kv("Pilot profile", pilot),
			kv("Env sets", orDash(strings.Join(p.Envs, ", "))),
			kv("Tools", tools),
			kv("Live sessions", liveS),
		}
	case "Env":
		if len(p.Envs) == 0 {
			return []string{"  No env sets attached. (v2 will attach them; today: cpb ALTER PLAYBOOK " + p.Name + " ADD ENV <env>)"}
		}
		rows := [][]string{}
		for i, e := range p.Envs {
			n := "?"
			for _, s := range m.st.Envs {
				if s.Name == e {
					n = strconv.Itoa(len(s.Vars))
				}
			}
			rows = append(rows, []string{strconv.Itoa(i + 1), e, n})
		}
		return m.table([]string{"#", "ENV SET", "VARS"}, rows, 1, -1, h)
	case "Vars":
		vars := p.Vars
		note := "  the playbook's own layer (EXPLAIN is loading)"
		if m.detail.explain != nil {
			vars, note = m.detail.explain.Vars, "  effective at launch, from EXPLAIN"
		} else if m.detail.err != "" {
			note = "  the playbook's own layer (EXPLAIN failed: " + m.detail.err + ")"
		}
		if len(vars) == 0 {
			return []string{note, "", "  No variables."}
		}
		rows := [][]string{}
		for _, v := range vars {
			rows = append(rows, []string{v.Key, v.Shown(), v.LayerName()})
		}
		return append([]string{note}, m.table([]string{"KEY", "VALUE / REF", "LAYER"}, rows, 1, -1, h-1)...)
	case "Plugins":
		var out []string
		var mk []string
		for _, x := range p.Marketplaces {
			mk = append(mk, x.Name)
		}
		out = append(out, kv("Marketplaces", orDash(strings.Join(mk, ", "))))
		if len(p.Plugins) == 0 {
			out = append(out, kv("Plugins", "-"))
		}
		for i, x := range p.Plugins {
			on := "enabled"
			if !x.Enabled {
				on = "disabled"
			}
			label := ""
			if i == 0 {
				label = "Plugins"
			}
			out = append(out, kv(label, x.ID+"  ("+on+")"))
		}
		return append(out, kv("Agent", orDash(deref(p.Agent))))
	case "MCP":
		if len(p.MCPServers) == 0 {
			return []string{"  No MCP servers."}
		}
		rows := [][]string{}
		for _, s := range p.MCPServers {
			target := deref(s.Command)
			if s.URL != nil {
				target = *s.URL
			}
			var sec []string
			for _, v := range append(append([]Var{}, s.Env...), s.Headers...) {
				sec = append(sec, v.Key+" "+v.Shown())
			}
			rows = append(rows, []string{s.Name, s.Transport, target, orDash(strings.Join(sec, ", "))})
		}
		return m.table([]string{"NAME", "TRANSPORT", "TARGET", "ENV / HEADERS"}, rows, 2, -1, h, 1)
	case "Skills":
		if len(p.Skills) == 0 {
			return []string{"  No skills."}
		}
		rows := [][]string{}
		for _, s := range p.Skills {
			rows = append(rows, []string{s.Name, s.Mode, m.short(s.Source)})
		}
		return m.table([]string{"NAME", "MODE", "FROM"}, rows, 2, -1, h)
	case "Status line":
		refresh := "-"
		if p.StatuslineRefresh != nil {
			refresh = strconv.Itoa(*p.StatuslineRefresh) + " s"
		}
		out := []string{
			kv("Command", orDash(deref(p.Statusline))),
			kv("Refresh", refresh),
			kv("History", fmt.Sprintf("%d (SET STATUSLINE PREVIOUS puts back the newest)", len(p.StatuslineHistory))),
		}
		if len(p.Panels) == 0 {
			return append(out, kv("Panels", "-"))
		}
		for i, x := range p.Panels {
			label := ""
			if i == 0 {
				label = "Panels"
			}
			who := "yours"
			if x.Cpb {
				who = "cpb"
			}
			out = append(out, kv(label, fmt.Sprintf("%s  %s  (%s, %s)", x.Panel, x.Type, x.Source, who)))
		}
		return out
	case "Model":
		out := []string{kv("Default", orDash(deref(p.Model)))}
		if p.ModelPicker == nil {
			return append(out, kv("Picker", "-"))
		}
		out = append(out, kv("Picker", strings.ToUpper(p.ModelPicker.Mode)))
		for _, o := range p.ModelPicker.Options {
			label := o.Model
			if o.Label != nil {
				label += "  \"" + *o.Label + "\""
			}
			out = append(out, kv("", label))
		}
		return out
	case "Sessions":
		live := m.sessionsOf(p.Name)
		if len(live) == 0 {
			return []string{"  No live sessions."}
		}
		rows := [][]string{}
		for _, s := range live {
			rows = append(rows, []string{strconv.Itoa(s.PID), orDash(deref(s.TTY)), kindShort(s.Kind), m.age(s.StartedAt), m.age(deref(s.LastActive)), orDash(deref(s.Model)), m.short(s.Cwd)})
		}
		return m.table([]string{"PID", "TTY", "KIND", "AGE", "ACTIVE", "MODEL", "CWD"}, rows, 6, -1, h, 2, 4, 5)
	}
	return nil
}
