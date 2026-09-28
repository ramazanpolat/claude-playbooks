package tui

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
)

// The TUI is a bubbletea program (charm.land/bubbletea/v2): Update gets a
// Msg and returns the new model and a Cmd; a Cmd runs off the loop and its
// Msg comes back. render is a pure function of the model, which is what the
// golden tests compare.
type (
	Msg = tea.Msg
	Cmd = tea.Cmd
)

func batch(cmds ...Cmd) Cmd { return tea.Batch(cmds...) }

func quit() Msg { return tea.Quit() }

// Options are the TUI's connections to the world, all replaceable in tests.
type Options struct {
	Runner Runner
	// Resume builds the command a resume runs (cpb RESUME …); nil disables
	// resuming.
	Resume func(args ...string) *exec.Cmd
	// Clipboard receives copied text; nil sets the terminal's clipboard
	// with OSC 52 (tea.SetClipboard).
	Clipboard func(string) error
	// NoColor draws without attributes (reverse, dim); NO_COLOR sets it.
	NoColor bool
	Now     func() time.Time
	Home    string // for ~ in paths
	Cwd     string // where export writes, and RESUME --list looks
	Poll    time.Duration
}

type view int

const (
	vPlaybooks view = iota
	vSessions
	vEnvs
	vDefaults
	vLog
)

var viewNames = []string{"Playbooks", "Sessions", "Env sets", "Defaults", "Log"}

var detailTabs = []string{"Overview", "Env", "Vars", "Plugins", "MCP", "Skills", "Status line", "Model", "Sessions"}

// Model is the TUI's state. View() is a pure function of it.
type Model struct {
	o        Options
	w, h     int
	st       State
	loaded   bool
	err      string
	msg      string
	view     view
	cursor   map[view]int
	recent   bool
	recents  []Recent
	detail   *detail
	text     *textPane
	confirm  *confirm
	help     bool
	filter   string
	typing   bool
	log      []string
	lastRead time.Time
	reads    string // the statement behind what is on screen
}

type detail struct {
	pb      string
	tab     int
	explain *Explain
	err     string
	scroll  int
}

type textPane struct {
	title, stmt, body string
	vp                viewport.Model
}

type confirm struct {
	path string
	data []byte
}

// Messages.
type (
	stateMsg struct {
		st  State
		err error
	}
	sessionsMsg struct {
		s   []Session
		err error
	}
	recentMsg struct {
		r   []Recent
		err error
	}
	explainMsg struct {
		name string
		e    Explain
		err  error
	}
	textMsg struct {
		title, stmt, body string
		err               error
	}
	exportMsg struct {
		name, body string
		err        error
	}
	resumeDoneMsg struct {
		stmt string
		err  error
	}
	tickMsg time.Time
)

// fromEnv applies the environment to the options: NO_COLOR, set to
// anything, turns attributes off.
func fromEnv(o Options, getenv func(string) string) Options {
	if getenv("NO_COLOR") != "" {
		o.NoColor = true
	}
	return o
}

// New returns the model; Run starts it.
func New(o Options) Model {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Poll == 0 {
		o.Poll = 5 * time.Second
	}
	return Model{o: o, cursor: map[view]int{}, w: 80, h: 24, reads: strings.Join(readPlaybooks, " ")}
}

func (m Model) Init() Cmd { return batch(m.loadAll(), m.tick()) }

func (m Model) tick() Cmd {
	return tea.Tick(m.o.Poll, func(t time.Time) Msg { return tickMsg(t) })
}

func (m Model) loadAll() Cmd {
	r := m.o.Runner
	return func() Msg {
		st, err := loadState(r)
		return stateMsg{st, err}
	}
}

func (m Model) loadSessions() Cmd {
	r := m.o.Runner
	return func() Msg {
		s, err := loadSessions(r)
		return sessionsMsg{s, err}
	}
}

func (m Model) loadRecent() Cmd {
	r := m.o.Runner
	return func() Msg {
		rs, err := loadRecent(r)
		return recentMsg{rs, err}
	}
}

// polling: sessions are re-read only while a screen that shows them is up.
func (m Model) polling() bool {
	if m.text != nil || m.confirm != nil || m.help {
		return false
	}
	if m.detail != nil {
		return m.detail.tab == len(detailTabs)-1 || m.detail.tab == 0
	}
	return (m.view == vPlaybooks || m.view == vSessions) && !m.recent
}

// Update is tea.Model's; update is the same with the concrete model, which
// the tests drive.
func (m Model) Update(msg Msg) (tea.Model, Cmd) { return m.update(msg) }

func (m Model) update(msg Msg) (Model, Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		// A pty with no size reports 0x0: keep 80x24 rather than draw
		// nothing.
		if msg.Width > 0 && msg.Height > 0 {
			m.w, m.h = msg.Width, msg.Height
		}
		if m.text != nil {
			m.text.vp.SetWidth(m.w)
			m.text.vp.SetHeight(m.bodyHeight())
		}
		return m, nil
	case stateMsg:
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.st, m.loaded, m.err, m.lastRead = msg.st, true, "", m.o.Now()
		m.clampCursors()
		return m, nil
	case sessionsMsg:
		if msg.err == nil {
			m.st.Sessions, m.lastRead = msg.s, m.o.Now()
			m.clampCursors()
		}
		return m, nil
	case recentMsg:
		if msg.err != nil {
			m.msg = msg.err.Error()
			return m, nil
		}
		m.recents = msg.r
		m.clampCursors()
		return m, nil
	case explainMsg:
		if m.detail != nil && m.detail.pb == msg.name {
			if msg.err != nil {
				m.detail.err = msg.err.Error()
			} else {
				e := msg.e
				m.detail.explain = &e
			}
		}
		return m, nil
	case textMsg:
		if msg.err != nil {
			m.msg = msg.err.Error()
			return m, nil
		}
		vp := viewport.New(viewport.WithWidth(m.w), viewport.WithHeight(m.bodyHeight()))
		vp.SetContent(strings.TrimRight(msg.body, "\n"))
		m.text = &textPane{title: msg.title, stmt: msg.stmt, body: msg.body, vp: vp}
		return m, nil
	case exportMsg:
		return m.export(msg)
	case resumeDoneMsg:
		if msg.err != nil {
			m.logf("%s: %v", msg.stmt, msg.err)
			m.msg = msg.stmt + ": " + msg.err.Error()
		} else {
			m.logf("%s: exited 0", msg.stmt)
			m.msg = msg.stmt + ": done"
		}
		return m, batch(m.loadAll(), m.loadRecent())
	case tickMsg:
		if m.polling() && m.loaded {
			return m, batch(m.tick(), m.loadSessions())
		}
		return m, m.tick()
	case tea.PasteMsg:
		if m.typing {
			m.filter += msg.Content
			m.cursor[m.view] = 0
		}
		return m, nil
	case tea.KeyPressMsg:
		return m.key(msg)
	}
	return m, nil
}

func (m *Model) logf(format string, args ...any) {
	m.log = append(m.log, m.o.Now().Format("15:04:05")+"  "+fmt.Sprintf(format, args...))
}

func (m Model) key(k tea.KeyPressMsg) (Model, Cmd) {
	s := k.String()
	if s == "ctrl+c" {
		return m, quit
	}
	switch {
	case m.confirm != nil:
		// Only a typed y replaces the file; anything else leaves it.
		c := m.confirm
		m.confirm = nil
		if s == "y" {
			if err := replaceFile(c.path, c.data); err != nil {
				m.msg = "export failed: " + err.Error()
			} else {
				m.msg = "exported " + m.short(c.path) + " (replaced)"
				m.logf("export %s (replaced)", m.short(c.path))
			}
		} else {
			m.msg = "not exported: " + m.short(c.path) + " left as it was"
		}
		return m, nil
	case m.typing:
		switch s {
		case "enter", "esc":
			m.typing = false
			if s == "esc" {
				m.filter = ""
			}
		case "backspace":
			if len(m.filter) > 0 {
				m.filter = m.filter[:len(m.filter)-1]
			}
		case "space":
			m.filter += " "
		default:
			if k.Text != "" {
				m.filter += k.Text
			}
		}
		m.cursor[m.view] = 0
		return m, nil
	case m.help:
		m.help = false
		return m, nil
	case m.text != nil:
		switch s {
		case "esc", "q", "c":
			m.text = nil
			return m, nil
		case "y":
			return m.copy(m.text.body, m.text.stmt)
		}
		t := *m.text
		var cmd Cmd
		t.vp, cmd = t.vp.Update(k)
		m.text = &t
		return m, cmd
	case m.detail != nil:
		return m.detailKey(s)
	}
	switch s {
	case "q":
		return m, quit
	case "?":
		m.help = true
		return m, nil
	case "1", "2", "3", "4", "5":
		m.view = view(s[0] - '1')
		m.filter, m.recent = "", false
		m.reads = m.viewReads()
		return m, nil
	case "r":
		m.msg = "refreshing…"
		if m.recent {
			return m, batch(m.loadAll(), m.loadRecent())
		}
		return m, m.loadAll()
	case "/":
		m.typing, m.filter = true, ""
		return m, nil
	case "up", "k":
		m.move(-1)
		return m, nil
	case "down", "j":
		m.move(1)
		return m, nil
	case "pgup":
		m.move(-m.bodyHeight())
		return m, nil
	case "pgdown":
		m.move(m.bodyHeight())
		return m, nil
	}
	switch m.view {
	case vPlaybooks:
		pb, ok := m.selectedPlaybook()
		if !ok {
			return m, nil
		}
		switch s {
		case "enter":
			m.detail = &detail{pb: pb.Name}
			m.reads = "SHOW PLAYBOOK " + pb.Name + " --json"
			return m, m.loadExplain(pb.Name)
		case "s":
			m.view, m.filter, m.recent = vSessions, pb.Name, false
			m.reads = m.viewReads()
			return m, nil
		case "c":
			return m, m.showCreate("PLAYBOOK", pb.Name)
		case "e":
			return m, m.exportCmd("PLAYBOOK", pb.Name)
		case "y":
			return m.copy("SHOW PLAYBOOK "+pb.Name, "")
		}
	case vSessions:
		switch s {
		case "R":
			m.recent = !m.recent
			m.cursor[vSessions] = 0
			m.reads = m.viewReads()
			if m.recent {
				return m, m.loadRecent()
			}
			return m, nil
		case "enter":
			return m.resumeSelected()
		case "y":
			if stmt, ok := m.selectedResumeStatement(); ok {
				return m.copy(stmt, "")
			}
		}
	case vEnvs:
		rows := m.envRows()
		i := m.cursor[vEnvs]
		if i >= len(rows) {
			return m, nil
		}
		name := rows[i].Name
		switch s {
		case "c", "enter":
			return m, m.showCreate("ENV", name)
		case "e":
			return m, m.exportCmd("ENV", name)
		case "y":
			return m.copy("SHOW ENV "+name, "")
		}
	case vDefaults:
		if s == "y" {
			return m.copy("SHOW DEFAULTS", "")
		}
	}
	return m, nil
}

func (m Model) detailKey(s string) (Model, Cmd) {
	d := *m.detail
	switch s {
	case "esc", "backspace":
		m.detail = nil
		m.reads = m.viewReads()
		return m, nil
	case "q":
		return m, quit
	case "?":
		m.help = true
		return m, nil
	case "right", "l", "tab":
		d.tab, d.scroll = (d.tab+1)%len(detailTabs), 0
	case "left", "h", "shift+tab":
		d.tab, d.scroll = (d.tab+len(detailTabs)-1)%len(detailTabs), 0
	case "down", "j":
		d.scroll++
	case "up", "k":
		if d.scroll > 0 {
			d.scroll--
		}
	case "c":
		return m, m.showCreate("PLAYBOOK", d.pb)
	case "e":
		return m, m.exportCmd("PLAYBOOK", d.pb)
	case "y":
		stmt := "SHOW PLAYBOOK " + d.pb
		if detailTabs[d.tab] == "Vars" {
			stmt = "EXPLAIN PLAYBOOK " + d.pb
		}
		return m.copy(stmt, "")
	case "r":
		m.detail = &d
		return m, batch(m.loadAll(), m.loadExplain(d.pb))
	default:
		if n, err := strconv.Atoi(s); err == nil && n >= 1 && n <= len(detailTabs) {
			d.tab, d.scroll = n-1, 0
		}
	}
	m.detail = &d
	if detailTabs[d.tab] == "Vars" {
		m.reads = "EXPLAIN PLAYBOOK " + d.pb + " --json"
	} else {
		m.reads = "SHOW PLAYBOOK " + d.pb + " --json"
	}
	return m, nil
}

func (m Model) viewReads() string {
	switch m.view {
	case vPlaybooks:
		return strings.Join(readPlaybooks, " ")
	case vSessions:
		if m.recent {
			return strings.Join(readRecent, " ")
		}
		return strings.Join(readSessions, " ")
	case vEnvs:
		return strings.Join(readEnvs, " ")
	case vDefaults:
		return strings.Join(readDefaults, " ")
	}
	return "(what the TUI ran this session)"
}

func (m Model) loadExplain(name string) Cmd {
	r := m.o.Runner
	return func() Msg {
		e, err := loadExplain(r, name)
		return explainMsg{name, e, err}
	}
}

func (m Model) showCreate(object, name string) Cmd {
	r := m.o.Runner
	stmt := "SHOW CREATE " + object + " " + name + " --skip-secrets"
	return func() Msg {
		body, err := showCreate(r, object, name)
		return textMsg{title: "SHOW CREATE " + object + " " + name, stmt: stmt, body: body, err: err}
	}
}

func (m Model) exportCmd(object, name string) Cmd {
	r := m.o.Runner
	return func() Msg {
		body, err := showCreate(r, object, name)
		return exportMsg{name: name, body: body, err: err}
	}
}

// export writes <name>.cpb in the working folder, never over an existing
// file without a typed y.
func (m Model) export(e exportMsg) (Model, Cmd) {
	if e.err != nil {
		m.msg = e.err.Error()
		return m, nil
	}
	path := filepath.Join(m.o.Cwd, e.name+".cpb")
	err := createFile(path, []byte(e.body))
	if errors.Is(err, fs.ErrExist) {
		m.confirm = &confirm{path: path, data: []byte(e.body)}
		return m, nil
	}
	if err != nil {
		m.msg = "export failed: " + err.Error()
		return m, nil
	}
	m.msg = "exported " + m.short(path)
	m.logf("export %s", m.short(path))
	return m, nil
}

func (m Model) copy(text, stmt string) (Model, Cmd) {
	var cmd Cmd
	if clip := m.o.Clipboard; clip != nil {
		if err := clip(text); err != nil {
			m.msg = "copy failed: " + err.Error()
			return m, nil
		}
	} else {
		cmd = tea.SetClipboard(text)
	}
	what := text
	if stmt != "" {
		what = stmt
	}
	if i := strings.IndexByte(what, '\n'); i >= 0 {
		what = what[:i] + " …"
	}
	m.msg = "copied: " + what
	m.logf("copied %s", what)
	return m, cmd
}

// resumeArgs is the RESUME statement for a session of label (a playbook
// name, or a plain directory's path).
func resumeArgs(label, id string) []string {
	args := []string{"RESUME", "SESSION", id}
	if !strings.HasPrefix(label, "/") {
		args = append(args, "FOR", "PLAYBOOK", label)
	}
	return args
}

func (m Model) selectedResumeStatement() (string, bool) {
	i := m.cursor[vSessions]
	if m.recent {
		rows := m.recentRows()
		if i < len(rows) {
			return statement(resumeArgs(rows[i].Playbook, rows[i].SessionID)), true
		}
		return "", false
	}
	rows := m.sessionRows()
	if i < len(rows) {
		return statement(resumeArgs(rows[i].Playbook, rows[i].SessionID)), true
	}
	return "", false
}

// statement quotes a session id as the grammar writes it.
func statement(args []string) string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = a
		if i > 0 && args[i-1] == "SESSION" {
			out[i] = "'" + a + "'"
		}
	}
	return strings.Join(out, " ")
}

func (m Model) resumeSelected() (Model, Cmd) {
	i := m.cursor[vSessions]
	var label, id string
	var livePID int
	var tty string
	if m.recent {
		rows := m.recentRows()
		if i >= len(rows) {
			return m, nil
		}
		label, id = rows[i].Playbook, rows[i].SessionID
		if rows[i].Live && rows[i].PID != nil {
			livePID = *rows[i].PID
		}
	} else {
		rows := m.sessionRows()
		if i >= len(rows) {
			return m, nil
		}
		label, id, livePID = rows[i].Playbook, rows[i].SessionID, rows[i].PID
		if rows[i].TTY != nil {
			tty = " on " + *rows[i].TTY
		}
	}
	if livePID != 0 {
		// cpb refuses a live session; the TUI says so rather than ask.
		m.msg = fmt.Sprintf("%s is running in pid %d%s: cpb does not resume a live session (R lists the others)", shortID(id), livePID, tty)
		return m, nil
	}
	if m.o.Resume == nil {
		return m, nil
	}
	args := resumeArgs(label, id)
	stmt := statement(args)
	m.logf("%s", stmt)
	// bubbletea hands the terminal to the session and takes it back: its
	// input reader is cancelled first, so no key typed for the session is
	// read here.
	return m, tea.ExecProcess(m.o.Resume(args...), func(err error) Msg { return resumeDoneMsg{stmt, err} })
}

func (m Model) selectedPlaybook() (Playbook, bool) {
	rows := m.playbookRows()
	i := m.cursor[vPlaybooks]
	if i < len(rows) {
		return rows[i], true
	}
	return Playbook{}, false
}

func (m *Model) move(d int) {
	n := m.rowCount()
	if n == 0 {
		return
	}
	c := m.cursor[m.view] + d
	if c < 0 {
		c = 0
	}
	if c >= n {
		c = n - 1
	}
	m.cursor[m.view] = c
}

func (m *Model) clampCursors() {
	for v := range m.cursor {
		saved := m.view
		m.view = v
		n := m.rowCount()
		if m.cursor[v] >= n {
			m.cursor[v] = max(0, n-1)
		}
		m.view = saved
	}
}

func (m Model) rowCount() int {
	switch m.view {
	case vPlaybooks:
		return len(m.playbookRows())
	case vSessions:
		if m.recent {
			return len(m.recentRows())
		}
		return len(m.sessionRows())
	case vEnvs:
		return len(m.envRows())
	case vLog:
		return len(m.log)
	}
	return 0
}

func matches(filter string, fields ...string) bool {
	if filter == "" {
		return true
	}
	f := strings.ToLower(filter)
	for _, s := range fields {
		if strings.Contains(strings.ToLower(s), f) {
			return true
		}
	}
	return false
}

func (m Model) playbookRows() []Playbook {
	var out []Playbook
	for _, p := range m.st.Playbooks {
		if matches(m.filter, p.Name, deref(p.Launcher)) {
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (m Model) sessionRows() []Session {
	var out []Session
	for _, s := range m.st.Sessions {
		if matches(m.filter, s.Playbook, s.Cwd, s.SessionID, deref(s.Model)) {
			out = append(out, s)
		}
	}
	return out
}

func (m Model) recentRows() []Recent {
	var out []Recent
	for _, r := range m.recents {
		if matches(m.filter, r.Playbook, r.SessionID, deref(r.Title), deref(r.Model)) {
			out = append(out, r)
		}
	}
	return out
}

func (m Model) envRows() []EnvSet {
	var out []EnvSet
	for _, e := range m.st.Envs {
		if matches(m.filter, e.Name, e.Description) {
			out = append(out, e)
		}
	}
	return out
}

func (m Model) sessionsOf(pb string) []Session {
	var out []Session
	for _, s := range m.st.Sessions {
		if s.Playbook == pb {
			out = append(out, s)
		}
	}
	return out
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// short writes a path under home with ~.
func (m Model) short(p string) string {
	if m.o.Home != "" && (p == m.o.Home || strings.HasPrefix(p, m.o.Home+"/")) {
		return "~" + strings.TrimPrefix(p, m.o.Home)
	}
	return p
}

// age is a compact age: 45s, 12m, 3h, 2d; "-" for none.
func (m Model) age(ts string) string {
	if ts == "" {
		return "-"
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return "-"
	}
	d := m.o.Now().Sub(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", max(0, int(d.Seconds())))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// writeTemp writes data to a new file in path's folder and returns its
// name: the half of an atomic write that can fail without touching path.
func writeTemp(path string, data []byte) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return "", err
	}
	_, werr := f.Write(data)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(f.Name(), 0o644)
	}
	if werr != nil {
		os.Remove(f.Name())
		return "", werr
	}
	return f.Name(), nil
}

// createFile writes path only if nothing has that name, all at once: the
// content goes to a temporary file, which is then linked to the name (a
// link, unlike a rename, fails when the name exists). fs.ErrExist when it
// does.
func createFile(path string, data []byte) error {
	tmp, err := writeTemp(path, data)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	if err := os.Link(tmp, path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fs.ErrExist
		}
		// A filesystem without hard links: create the name exclusively.
		f, ferr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if ferr != nil {
			return ferr
		}
		_, werr := f.Write(data)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		return werr
	}
	return nil
}

// replaceFile replaces path's content all at once: a temporary file in the
// same folder renamed over the name, so a failed write leaves the old file,
// and a symlink named path is replaced, not written through.
func replaceFile(path string, data []byte) error {
	tmp, err := writeTemp(path, data)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
