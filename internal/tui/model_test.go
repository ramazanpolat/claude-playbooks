package tui

import (
	"encoding/json"
	"errors"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden")

var now = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

// fakeRunner answers cpb commands from canned outputs and records them.
type fakeRunner struct {
	mu    sync.Mutex
	out   map[string]string
	calls []string
}

func (f *fakeRunner) Run(args ...string) ([]byte, error) {
	k := strings.Join(args, " ")
	f.mu.Lock()
	f.calls = append(f.calls, k)
	f.mu.Unlock()
	if v, ok := f.out[k]; ok {
		if strings.HasPrefix(v, "ERR:") {
			return nil, errors.New(strings.TrimPrefix(v, "ERR:"))
		}
		return []byte(v), nil
	}
	return nil, errors.New("fake: no answer for " + k)
}

// The fixture is what cpb's --json prints: a credential is already
// redacted (no value), a reference is a reference.
const fixturePlaybooks = `[
 {"name":"alpha","version":"v1.2.0","path":"/home/me/.claude-playbooks/alpha","source":{"url":"https://github.com/example/work-playbook","branch":"v1.2.0","subdir":null},"linked":null,"launcher":"al","envs":[],"vars":[],"sandbox":{"always":false},"login":"shared","memory":"isolated","marketplaces":[],"plugins":[],"agent":null,"mcp_servers":[],"tools":{"allow":["Bash(git:*)"],"deny":[]},"skills":[],"statusline":"$HOME/bin/my-status","statusline_refresh":10,"statusline_history":[{"command":"echo old","refresh":null,"replaced_at":"2026-09-27T10:00:00Z"}],"model":"claude-opus-5-5","model_picker":null},
 {"name":"router","version":null,"path":"/home/me/.claude-playbooks/router","source":null,"linked":null,"launcher":"rt","envs":["proxy"],"vars":[{"key":"OPENAI_API_KEY","redacted":true,"plaintext":true},{"key":"MY_FLAG","value":"1"}],"sandbox":{"always":false},"login":"isolated","memory":"isolated","marketplaces":[{"name":"team","source":{"source":"github","repo":"x/y"}}],"plugins":[{"id":"reviewer@team","enabled":true}],"agent":"reviewer:reviewer","mcp_servers":[{"name":"sentry","transport":"http","url":"https://mcp.sentry.dev/mcp","env":[],"headers":[{"key":"Authorization","ref":"keychain:sentry"}]}],"tools":{"allow":[],"deny":[]},"skills":[{"name":"notes","source":"/home/me/notes","branch":null,"subdir":null,"mode":"link"}],"statusline":null,"statusline_refresh":null,"statusline_history":[],"model":"glm-5.3","model_picker":{"mode":"append","options":[{"model":"glm-5.3","label":"GLM","description":null,"behaves_as":null}]}}
]`

const fixtureSessions = `[
 {"playbook":"alpha","config_dir":"/home/me/.claude-playbooks/alpha","pid":47904,"session_id":"08c4811b-3867-4f18-b08f-de6d1e07395f","cwd":"/home/me/DEV/app","kind":"interactive","status":"busy","name":"release","claude_version":"2.1.283","started_at":"2026-09-28T03:00:00.000Z","last_active":"2026-09-28T11:59:06.000Z","model":"claude-opus-5-5","launcher":"al","resume":"cd '/home/me/DEV/app' && al --resume 08c4811b-3867-4f18-b08f-de6d1e07395f","tty":"ttys039"},
 {"playbook":"router","config_dir":"/home/me/.claude-playbooks/router","pid":43627,"session_id":"e7377ba9-434b-4ff2-a592-dd0da81f6e2f","cwd":"/home/me/DEV/app","kind":"bg","status":"idle","name":null,"claude_version":"2.1.283","started_at":"2026-09-26T12:00:00.000Z","last_active":"2026-09-28T11:32:00.000Z","model":"glm-5.3","launcher":"rt","resume":"cd '/home/me/DEV/app' && rt --resume e7377ba9-434b-4ff2-a592-dd0da81f6e2f","tty":null}
]`

const fixtureEnvs = `[
 {"name":"proxy","description":"LLM proxy","vars":[{"key":"ANTHROPIC_BASE_URL","value":"http://localhost:8080/v1"},{"key":"ANTHROPIC_AUTH_TOKEN","ref":"keychain:proxy-token"}],"used_by":["router"],"default":false},
 {"name":"base","description":"","vars":[{"key":"X","value":"1"}],"used_by":[],"default":true}
]`

const fixtureDefaults = `{"envs":["base"],"secret_helper":{"command":"cpb-secret-file","from":"setting"}}`

const fixtureExplain = `{"playbook":"router","vars":[{"key":"X","value":"1","layer":{"kind":"defaults","name":"base"}},{"key":"ANTHROPIC_BASE_URL","value":"http://localhost:8080/v1","layer":{"kind":"env","name":"proxy"}},{"key":"ANTHROPIC_AUTH_TOKEN","ref":"keychain:proxy-token","layer":{"kind":"env","name":"proxy"}},{"key":"OPENAI_API_KEY","redacted":true,"plaintext":true,"layer":{"kind":"playbook"}},{"key":"MY_FLAG","value":"1","layer":{"kind":"playbook"}}],"secret_helper":null}`

const fixtureCreate = "-- playbook.cpb, from: cpb SHOW CREATE PLAYBOOK router --skip-secrets\nCREATE PLAYBOOK IF NOT EXISTS router LAUNCHER rt;\nALTER PLAYBOOK router USE ENV proxy SET VAR MY_FLAG=1;\n-- OPENAI_API_KEY: a credential literal, skipped (--skip-secrets)\n"

func fixture() *fakeRunner {
	f := &fakeRunner{out: map[string]string{
		"SHOW PLAYBOOKS --json":                      fixturePlaybooks,
		"SHOW SESSIONS --json":                       fixtureSessions,
		"SHOW ENVS --json":                           fixtureEnvs,
		"SHOW DEFAULTS --json":                       fixtureDefaults,
		"EXPLAIN PLAYBOOK router --json":             fixtureExplain,
		"SHOW CREATE PLAYBOOK router --skip-secrets": fixtureCreate,
		"SHOW CREATE ENV proxy --skip-secrets":       "CREATE ENV IF NOT EXISTS proxy SET ANTHROPIC_AUTH_TOKEN FROM 'keychain:proxy-token';\n",
		"SHOW CREATE PLAYBOOK alpha --skip-secrets":  "CREATE PLAYBOOK IF NOT EXISTS alpha LAUNCHER al;\n",
	}}
	singular(f, fixturePlaybooks)
	return f
}

// singular answers SHOW PLAYBOOK <name> --json for each playbook of a
// SHOW PLAYBOOKS --json answer, as cpb would.
func singular(f *fakeRunner, all string) {
	var raw []json.RawMessage
	if err := json.Unmarshal([]byte(all), &raw); err != nil {
		panic(err)
	}
	for _, r := range raw {
		var p struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(r, &p)
		f.out["SHOW PLAYBOOK "+p.Name+" --json"] = string(r)
	}
}

type harness struct {
	t    *testing.T
	m    Model
	r    *fakeRunner
	clip []string
}

func newHarness(t *testing.T, w, h int) *harness {
	return newHarnessIn(t, w, h, "/home/me/DEV/app")
}

func newHarnessIn(t *testing.T, w, h int, cwd string) *harness {
	t.Helper()
	hs := &harness{t: t, r: fixture()}
	o := Options{Runner: hs.r, Now: func() time.Time { return now }, Home: "/home/me", Cwd: cwd, Poll: time.Nanosecond, NoColor: true,
		Clipboard: func(s string) error { hs.clip = append(hs.clip, s); return nil }}
	hs.m = New(o)
	hs.send(tea.WindowSizeMsg{Width: w, Height: h})
	st, err := loadState(hs.r)
	if err != nil {
		t.Fatal(err)
	}
	hs.send(stateMsg{st: st})
	return hs
}

// send delivers msg, then runs the command it returns, once, unless it is a
// timer or an external process (the harness never waits or execs).
func (hs *harness) send(msg Msg) {
	var cmd Cmd
	hs.m, cmd = hs.m.update(msg)
	hs.run(cmd)
}

func (hs *harness) run(cmd Cmd) {
	if cmd == nil {
		return
	}
	msg := cmd()
	switch msg := msg.(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			hs.run(c)
		}
		return
	case nil, tickMsg, tea.QuitMsg:
		return
	}
	// tea's own messages (SetClipboard) are the program's business.
	if strings.HasPrefix(reflect.TypeOf(msg).String(), "tea.") {
		return
	}
	hs.send(msg)
}

// keyMsg is the key bubbletea delivers for a name or a character.
func keyMsg(k string) tea.KeyPressMsg {
	switch k {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	}
	r := []rune(k)[0]
	return tea.KeyPressMsg{Code: r, Text: k}
}

func (hs *harness) keys(ks ...string) {
	for _, k := range ks {
		hs.send(keyMsg(k))
	}
}

func (hs *harness) view() string { return hs.m.render() }

func golden(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v (run go test -update)", err)
	}
	if string(want) != got {
		t.Errorf("%s differs:\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

func TestGoldenScreens(t *testing.T) {
	for _, size := range []struct {
		name string
		w, h int
	}{{"80x24", 80, 24}, {"120x40", 120, 40}} {
		screens := []struct {
			name string
			keys []string
		}{
			{"playbooks", nil},
			{"sessions", []string{"2"}},
			{"envs", []string{"3"}},
			{"defaults", []string{"4"}},
			{"detail-overview", []string{"down", "enter"}},
			{"detail-vars", []string{"down", "enter", "3"}},
			{"detail-mcp", []string{"down", "enter", "5"}},
			{"detail-statusline", []string{"enter", "7"}},
			{"showcreate", []string{"down", "c"}},
			{"help", []string{"?"}},
		}
		for _, s := range screens {
			hs := newHarness(t, size.w, size.h)
			hs.keys(s.keys...)
			golden(t, s.name+"-"+size.name, hs.view())
		}
	}
}

// Nothing secret reaches a screen. cpb's --json withholds values; if a
// value ever came with "redacted": true anyway, the TUI would still show
// it redacted. A canary in every place a value could sit must appear on
// no screen and no tab, at either size.
func TestNoSecretOnAnyScreen(t *testing.T) {
	const canary = "sk-tui-canary-000000000000"
	leak := strings.Replace(fixturePlaybooks, `{"key":"OPENAI_API_KEY","redacted":true,"plaintext":true}`,
		`{"key":"OPENAI_API_KEY","value":"`+canary+`","redacted":true,"plaintext":true}`, 1)
	leak = strings.Replace(leak, `{"key":"Authorization","ref":"keychain:sentry"}`,
		`{"key":"Authorization","value":"`+canary+`","redacted":true}`, 1)
	if strings.Count(leak, canary) != 2 {
		t.Fatal("the canary was not planted")
	}
	explain := strings.Replace(fixtureExplain, `{"key":"OPENAI_API_KEY","redacted":true,"plaintext":true,`,
		`{"key":"OPENAI_API_KEY","value":"`+canary+`","redacted":true,"plaintext":true,`, 1)
	for _, size := range [][2]int{{80, 24}, {120, 40}} {
		screens := [][]string{nil, {"2"}, {"2", "R"}, {"3"}, {"4"}, {"5"}, {"?"}, {"down", "c"}}
		for tab := 1; tab <= len(detailTabs); tab++ {
			screens = append(screens, []string{"down", "enter", strconv.Itoa(tab)})
		}
		for _, keys := range screens {
			hs := newHarness(t, size[0], size[1])
			hs.r.out["SHOW PLAYBOOKS --json"] = leak
			singular(hs.r, leak)
			hs.r.out["EXPLAIN PLAYBOOK router --json"] = explain
			st, _ := loadState(hs.r)
			hs.send(stateMsg{st: st})
			hs.keys(keys...)
			if v := hs.view(); strings.Contains(v, canary) {
				t.Fatalf("%dx%d %v shows the canary:\n%s", size[0], size[1], keys, v)
			}
		}
	}
	hs := newHarness(t, 120, 40)
	hs.keys("down", "enter", "3")
	if v := hs.view(); !strings.Contains(v, "FROM 'keychain:proxy-token'") || !strings.Contains(v, "(redacted, plaintext)") {
		t.Fatalf("vars:\n%s", v)
	}
}

// A terminal a few lines high, and a scroll left from a longer tab, draw
// without a panic (agy, round 1 of the v2 PR).
func TestTinyTerminal(t *testing.T) {
	for h := 1; h <= 8; h++ {
		hs := newHarness(t, 30, h)
		hs.keys("2", "R", "3", "1", "down", "enter", "3")
		for i := 0; i < 20; i++ {
			hs.keys("j")
		}
		hs.keys("1")
		_ = hs.view()
	}
}

func TestReadsNameTheirStatement(t *testing.T) {
	hs := newHarness(t, 120, 40)
	if !strings.Contains(hs.view(), "reads: cpb SHOW PLAYBOOKS --json") {
		t.Fatal(hs.view())
	}
	hs.keys("2")
	if !strings.Contains(hs.view(), "reads: cpb SHOW SESSIONS --json") {
		t.Fatal(hs.view())
	}
	for _, c := range hs.r.calls {
		if !strings.HasPrefix(c, "SHOW ") && !strings.HasPrefix(c, "EXPLAIN ") {
			t.Fatalf("a read that is not a read: %s", c)
		}
	}
}

func TestExportNeverOverwritesWithoutY(t *testing.T) {
	hs := newHarnessIn(t, 120, 40, t.TempDir())
	cwd := hs.m.o.Cwd
	hs.keys("down", "e")
	p := filepath.Join(cwd, "router.cpb")
	b, err := os.ReadFile(p)
	if err != nil || string(b) != fixtureCreate {
		t.Fatalf("export: %v %q", err, b)
	}
	if err := os.WriteFile(p, []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hs.keys("e")
	if !strings.Contains(hs.view(), "Replace router.cpb? Type y to replace; any other key keeps it.") {
		t.Fatalf("no prompt:\n%s", hs.view())
	}
	hs.keys("n")
	if b, _ := os.ReadFile(p); string(b) != "mine\n" {
		t.Fatalf("replaced without y: %q", b)
	}
	hs.keys("e", "Y")
	if b, _ := os.ReadFile(p); string(b) != "mine\n" {
		t.Fatalf("replaced on Y, not a typed y: %q", b)
	}
	hs.keys("e", "y")
	if b, _ := os.ReadFile(p); string(b) != fixtureCreate {
		t.Fatalf("y did not replace: %q", b)
	}
	ents, _ := os.ReadDir(cwd)
	if len(ents) != 1 {
		t.Fatalf("export wrote more than one file: %v", ents)
	}
}

func TestCopyStatements(t *testing.T) {
	hs := newHarness(t, 120, 40)
	hs.keys("down", "y")
	hs.keys("2", "y")
	hs.keys("down", "y")
	want := []string{"SHOW PLAYBOOK router",
		"cd '/home/me/DEV/app' && al --resume 08c4811b-3867-4f18-b08f-de6d1e07395f",
		"cd '/home/me/DEV/app' && rt --resume e7377ba9-434b-4ff2-a592-dd0da81f6e2f"}
	if !reflect.DeepEqual(hs.clip, want) {
		t.Fatalf("copied %q", hs.clip)
	}
	hs.keys("1", "down", "c", "y")
	if len(hs.clip) != 4 || hs.clip[3] != fixtureCreate {
		t.Fatalf("SHOW CREATE copy: %q", hs.clip)
	}
}

// The TUI resumes nothing: Sessions shows the live ones and says where the
// past ones are, and enter and R start no process and change no view.
func TestSessionsPointAtPastSessions(t *testing.T) {
	hs := newHarness(t, 80, 24)
	hs.keys("2")
	before := hs.view()
	if !strings.Contains(before, "past sessions: <launcher> --resume") {
		t.Fatalf("Sessions does not say where past sessions are:\n%s", before)
	}
	hs.keys("enter", "R")
	if hs.view() != before || len(hs.clip) != 0 {
		t.Fatalf("enter or R did something:\n%s", hs.view())
	}
}

func TestPollsOnlyWhileSessionsShow(t *testing.T) {
	hs := newHarness(t, 120, 40)
	m := hs.m
	if !m.polling() {
		t.Fatal("the Playbooks list shows session counts")
	}
	hs.keys("3")
	if hs.m.polling() {
		t.Fatal("Env sets shows no sessions")
	}
	hs.keys("2")
	if !hs.m.polling() {
		t.Fatal("Sessions shows the live sessions")
	}
}

func TestFilter(t *testing.T) {
	hs := newHarness(t, 120, 40)
	hs.keys("/", "r", "o", "u", "enter")
	v := hs.view()
	if strings.Contains(v, "alpha") && !strings.Contains(v, "router") {
		t.Fatal(v)
	}
	if rows := hs.m.playbookRows(); len(rows) != 1 || rows[0].Name != "router" {
		t.Fatalf("filtered: %v", rows)
	}
}

func TestReadError(t *testing.T) {
	r := &fakeRunner{out: map[string]string{"SHOW PLAYBOOKS --json": "ERR:DEFAULTS cannot be read"}}
	m := New(Options{Runner: r, Now: func() time.Time { return now }})
	_, err := loadState(r)
	mm, _ := m.update(stateMsg{err: err})
	if v := mm.render(); !strings.Contains(v, "cpb could not be read") || !strings.Contains(v, "DEFAULTS cannot be read") {
		t.Fatal(v)
	}
}

// NO_COLOR draws no attributes; without it the selection is reverse video.
func TestNoColor(t *testing.T) {
	hs := newHarness(t, 100, 30)
	if strings.Contains(hs.view(), "\x1b[") {
		t.Fatal("NoColor drew attributes")
	}
	m := hs.m
	m.o.NoColor = false
	if !strings.Contains(m.render(), "\x1b[7m▸ ") {
		t.Fatal("no reverse video for the selection")
	}
	env := map[string]string{"NO_COLOR": "1"}
	if !fromEnv(Options{}, func(k string) string { return env[k] }).NoColor {
		t.Fatal("Run ignores NO_COLOR")
	}
	if fromEnv(Options{}, func(string) string { return "" }).NoColor {
		t.Fatal("no NO_COLOR, no NoColor")
	}
}

// The program view is render on the alternate screen.
func TestViewIsAltScreen(t *testing.T) {
	hs := newHarness(t, 100, 30)
	v := hs.m.View()
	if !v.AltScreen || v.Content != hs.m.render() {
		t.Fatal("View is render on the alternate screen")
	}
}

// Pasted text goes into the filter as text.
func TestPasteIntoFilter(t *testing.T) {
	hs := newHarness(t, 120, 40)
	hs.keys("/")
	hs.send(tea.PasteMsg{Content: "rou"})
	hs.keys("enter")
	if rows := hs.m.playbookRows(); len(rows) != 1 || rows[0].Name != "router" {
		t.Fatalf("filtered: %v", rows)
	}
}

// The replace question is whole on an 80-column screen even in a folder
// deeper than the screen is wide: the question first, the folder shortened
// after it (root's review of #138; macOS's long TempDir hid it).
func TestExportPromptVisibleInDeepFolder(t *testing.T) {
	deep := filepath.Join(t.TempDir(), strings.Repeat("a-very-deep-folder/", 8))
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deep, "router.cpb"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hs := newHarnessIn(t, 80, 24, deep)
	hs.keys("down", "e")
	v := hs.view()
	if !strings.Contains(v, "Replace router.cpb? Type y to replace; any other key keeps it.") {
		t.Fatalf("the question is not whole:\n%s", v)
	}
	for _, l := range strings.Split(v, "\n") {
		if width(l) > 80 {
			t.Fatalf("a line is wider than the screen: %q", l)
		}
	}
	hs.keys("y")
	if b, _ := os.ReadFile(filepath.Join(deep, "router.cpb")); string(b) != fixtureCreate {
		t.Fatalf("y did not replace: %q", b)
	}
}

// Replacing is atomic and replaces the name: a symlink named router.cpb
// becomes a regular file, and the file it pointed to is left as it was.
func TestExportReplacesASymlinkNotItsTarget(t *testing.T) {
	cwd := t.TempDir()
	target := filepath.Join(t.TempDir(), "elsewhere.cpb")
	if err := os.WriteFile(target, []byte("theirs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(cwd, "router.cpb")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	hs := newHarnessIn(t, 120, 40, cwd)
	hs.keys("down", "e", "y")
	fi, err := os.Lstat(link)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o644 {
		t.Fatalf("router.cpb: %v %v", fi.Mode(), err)
	}
	if b, _ := os.ReadFile(target); string(b) != "theirs\n" {
		t.Fatalf("written through the symlink: %q", b)
	}
	ents, _ := os.ReadDir(cwd)
	if len(ents) != 1 {
		t.Fatalf("a temporary file was left: %v", ents)
	}
}

// createFile never replaces: an existing name is fs.ErrExist, byte-identical.
func TestCreateFileIsExclusive(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.cpb")
	if err := createFile(p, []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := createFile(p, []byte("two")); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("got %v", err)
	}
	if b, _ := os.ReadFile(p); string(b) != "one" {
		t.Fatalf("replaced: %q", b)
	}
	if ents, _ := os.ReadDir(filepath.Dir(p)); len(ents) != 1 {
		t.Fatalf("a temporary file was left: %v", ents)
	}
}

// Every footer names only reads that ran, so the screen can be reproduced
// from what it says (Codex on #138: the detail named SHOW PLAYBOOK <n> but
// never ran it).
func TestFootersNameReadsThatRan(t *testing.T) {
	screens := [][]string{nil, {"2"}, {"2", "R"}, {"3"}, {"4"}, {"down", "c"}}
	for tab := 1; tab <= len(detailTabs); tab++ {
		screens = append(screens, []string{"down", "enter", strconv.Itoa(tab)})
	}
	for _, keys := range screens {
		hs := newHarness(t, 120, 40)
		hs.keys(keys...)
		v := hs.view()
		lines := strings.Split(v, "\n")
		foot := lines[len(lines)-1]
		if !strings.HasPrefix(foot, "reads: cpb ") {
			t.Fatalf("%v: footer %q", keys, foot)
		}
		ran := map[string]bool{}
		hs.r.mu.Lock()
		for _, c := range hs.r.calls {
			ran[c] = true
		}
		hs.r.mu.Unlock()
		for _, stmt := range strings.Split(strings.TrimPrefix(foot, "reads: cpb "), " · cpb ") {
			if !ran[stmt] {
				t.Errorf("%v: the footer names %q, which never ran (ran: %v)", keys, stmt, hs.r.calls)
			}
		}
	}
}

// Backspace removes a whole character: a pasted "ğü" leaves valid UTF-8
// after one backspace and nothing after two (Codex on #138).
func TestBackspaceDeletesACharacter(t *testing.T) {
	hs := newHarness(t, 120, 40)
	hs.keys("/")
	hs.send(tea.PasteMsg{Content: "ğü"})
	hs.send(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if hs.m.filter != "ğ" || !utf8.ValidString(hs.m.filter) {
		t.Fatalf("after one backspace: %q", hs.m.filter)
	}
	hs.send(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if hs.m.filter != "" {
		t.Fatalf("after two: %q", hs.m.filter)
	}
}

// CJK and emoji are two cells wide: truncation counts cells, so no line of
// an 80x24 screen is wider than 80, and the replace prompt's folder is cut
// to fit (Codex on #138).
func TestWideCharactersFitTheScreen(t *testing.T) {
	cjk := strings.NewReplacer("alpha", "中文プレイブック名前がとても長い", "claude-opus-5-5", "模型名字🙂很长很长很长").Replace(fixturePlaybooks)
	deep := filepath.Join(t.TempDir(), "项目", strings.Repeat("很长的文件夹名字", 6))
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deep, "router.cpb"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, keys := range [][]string{nil, {"2"}, {"2", "R"}, {"3"}, {"e"}} {
		hs := newHarnessIn(t, 80, 24, deep)
		hs.r.out["SHOW PLAYBOOKS --json"] = cjk
		singular(hs.r, cjk)
		st, _ := loadState(hs.r)
		hs.send(stateMsg{st: st})
		hs.keys(keys...)
		v := hs.view()
		for _, l := range strings.Split(v, "\n") {
			if w := ansi.StringWidth(l); w > 80 {
				t.Fatalf("%v: a line %d cells wide: %q", keys, w, l)
			}
		}
		if len(keys) == 0 {
			golden(t, "cjk-80x24", v)
			if !strings.Contains(v, "中文") {
				t.Fatalf("the CJK name is not shown:\n%s", v)
			}
		}
		if len(keys) == 1 && keys[0] == "e" && !strings.Contains(v, "Replace router.cpb? Type y to replace; any other key keeps it.") {
			t.Fatalf("the question is not whole:\n%s", v)
		}
	}
}

// partialRunner answers key with its fixture output and an error, as cpb
// does for a list that left out a playbook whose manifest cannot be read.
type partialRunner struct {
	*fakeRunner
	key, msg string
}

func (p partialRunner) Run(args ...string) ([]byte, error) {
	out, err := p.fakeRunner.Run(args...)
	if strings.Join(args, " ") == p.key {
		return out, errors.New(p.msg)
	}
	return out, err
}

// TestPartialRead: a read that left a playbook out keeps its rows, and the
// status bar says which playbook was left out and why.
func TestPartialRead(t *testing.T) {
	r := partialRunner{fixture(), "SHOW PLAYBOOKS --json", `playbook "bad" is left out: unknown key "bogus_key" in /home/me/.claude-playbooks/bad/.playbook:2`}
	st, err := loadState(r)
	var p *PartialRead
	if !errors.As(err, &p) || len(st.Playbooks) != 2 || len(st.Envs) != 2 || len(st.Sessions) != 2 {
		t.Fatalf("%v: %d playbooks, %d envs, %d sessions", err, len(st.Playbooks), len(st.Envs), len(st.Sessions))
	}
	m := New(Options{Runner: r, Now: func() time.Time { return now }})
	mm, _ := m.update(stateMsg{st, err})
	if v := mm.render(); !strings.Contains(v, "alpha") || !strings.Contains(v, `playbook "bad" is left out`) || strings.Contains(v, "cpb could not be read") {
		t.Fatal(v)
	}
	mm, _ = mm.update(sessionsMsg{st.Sessions[:1], &PartialRead{Msg: `playbook "late" is left out`}})
	if len(mm.st.Sessions) != 1 {
		t.Fatalf("a partial sessions read was dropped: %d", len(mm.st.Sessions))
	}
	if v := mm.render(); !strings.Contains(v, `playbook "late" is left out`) {
		t.Fatalf("a poll that left a playbook out does not say so:\n%s", v)
	}
	mm, _ = mm.update(sessionsMsg{st.Sessions, nil})
	if v := mm.render(); strings.Contains(v, "is left out") {
		t.Fatalf("a clean poll kept the warning:\n%s", v)
	}
	mm.err = "an action failed"
	mm, _ = mm.update(sessionsMsg{st.Sessions, nil})
	if mm.err != "an action failed" {
		t.Fatalf("a clean poll cleared another error: %q", mm.err)
	}
}
