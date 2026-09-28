package tui

import (
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
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
 {"name":"kommander-dev","version":"v3.11.4","path":"/home/p/.claude-playbooks/kommander-dev","source":{"url":"https://github.com/ramazanpolat/kommander-playbook","branch":"v3.11.4","subdir":null},"linked":null,"launcher":"kd","envs":[],"vars":[],"sandbox":false,"isolated_login":false,"marketplaces":[],"plugins":[],"agent":null,"mcp_servers":[],"tools":{"allow":["Bash(git:*)"],"deny":[]},"skills":[],"statusline":"\"$HOME/.local/bin/statusmux\" render","statusline_refresh":10,"statusline_history":[{"command":"echo old","refresh":null,"replaced_at":"2026-09-27T10:00:00Z"}],"panels":[{"panel":"local.clock","type":"exec","source":"config","cpb":true,"row":1,"priority":60,"align":"right"}],"model":"claude-opus-5-5","model_picker":null,"pilot_profile":"imported"},
 {"name":"router","version":null,"path":"/home/p/.claude-playbooks/router","source":null,"linked":null,"launcher":"k9","envs":["9router"],"vars":[{"key":"OPENAI_API_KEY","redacted":true,"plaintext":true},{"key":"MY_FLAG","value":"1"}],"sandbox":false,"isolated_login":true,"marketplaces":[{"name":"agentship","source":{"source":"github","repo":"x/y"}}],"plugins":[{"id":"kommander@agentship","enabled":true}],"agent":"kommander:k","mcp_servers":[{"name":"sentry","transport":"http","url":"https://mcp.sentry.dev/mcp","env":[],"headers":[{"key":"Authorization","ref":"keychain:pilot/sentry"}]}],"tools":{"allow":[],"deny":[]},"skills":[{"name":"notes","source":"/home/p/notes","branch":null,"subdir":null,"mode":"link"}],"statusline":null,"statusline_refresh":null,"statusline_history":[],"panels":[],"model":"glm-5.3","model_picker":{"mode":"append","options":[{"model":"glm-5.3","label":"GLM","description":null,"behaves_as":null}]},"pilot_profile":"not_imported"}
]`

const fixtureSessions = `[
 {"playbook":"kommander-dev","config_dir":"/home/p/.claude-playbooks/kommander-dev","pid":47904,"session_id":"08c4811b-3867-4f18-b08f-de6d1e07395f","cwd":"/home/p/DEV/claude-playbooks","kind":"interactive","status":"busy","name":"cp-3c","claude_version":"2.1.283","started_at":"2026-09-28T03:00:00.000Z","last_active":"2026-09-28T11:59:06.000Z","model":"claude-opus-5-5","launcher":"kd","resume":"kd --resume 08c4811b-3867-4f18-b08f-de6d1e07395f","tty":"ttys039"},
 {"playbook":"router","config_dir":"/home/p/.claude-playbooks/router","pid":43627,"session_id":"e7377ba9-434b-4ff2-a592-dd0da81f6e2f","cwd":"/home/p/DEV/claude-playbooks","kind":"bg","status":"idle","name":null,"claude_version":"2.1.283","started_at":"2026-09-26T12:00:00.000Z","last_active":"2026-09-28T11:32:00.000Z","model":"glm-5.3","launcher":"k9","resume":"k9 --resume e7377ba9-434b-4ff2-a592-dd0da81f6e2f","tty":null}
]`

const fixtureEnvs = `[
 {"name":"9router","description":"LLM proxy on tr0","vars":[{"key":"ANTHROPIC_BASE_URL","value":"http://tr0:20128/v1"},{"key":"ANTHROPIC_AUTH_TOKEN","ref":"keychain:pilot/9r"}],"used_by":["router"],"default":false},
 {"name":"base","description":"","vars":[{"key":"X","value":"1"}],"used_by":[],"default":true}
]`

const fixtureDefaults = `{"envs":["base"],"secret_helper":{"command":"with-secret","from":"setting"}}`

const fixtureExplain = `{"playbook":"router","vars":[{"key":"X","value":"1","layer":{"kind":"DEFAULTS","name":"base"}},{"key":"ANTHROPIC_BASE_URL","value":"http://tr0:20128/v1","layer":{"kind":"ENV","name":"9router"}},{"key":"ANTHROPIC_AUTH_TOKEN","ref":"keychain:pilot/9r","layer":{"kind":"ENV","name":"9router"}},{"key":"OPENAI_API_KEY","redacted":true,"plaintext":true,"layer":{"kind":"PLAYBOOK"}},{"key":"MY_FLAG","value":"1","layer":{"kind":"PLAYBOOK"}}],"secret_helper":null}`

const fixtureRecent = `[
 {"playbook":"kommander-dev","config_dir":"/home/p/.claude-playbooks/kommander-dev","session_id":"08c4811b-3867-4f18-b08f-de6d1e07395f","cwd":"/home/p/DEV/claude-playbooks","last_active":"2026-09-28T11:59:06.000Z","model":"claude-opus-5-5","title":"Open task claude-playbooks-cli","launcher":"kd","live":true,"pid":47904,"resume":"kd --resume 08c4811b"},
 {"playbook":"kommander-dev","config_dir":"/home/p/.claude-playbooks/kommander-dev","session_id":"d0a04774-d6ed-49f7-bb32-3c57962348fa","cwd":"/home/p/DEV/claude-playbooks","last_active":"2026-09-24T09:00:00.000Z","model":"claude-opus-5-5","title":"Cpb-env-redact-secrets","launcher":"kd","live":false,"pid":null,"resume":"kd --resume d0a04774"}
]`

const fixtureCreate = "-- playbook.cpb, from: cpb SHOW CREATE PLAYBOOK router --skip-secrets\nCREATE PLAYBOOK IF NOT EXISTS router ALIAS k9;\nALTER PLAYBOOK router USE ENV 9router SET VAR MY_FLAG=1;\n-- OPENAI_API_KEY: a credential literal, skipped (--skip-secrets)\n"

func fixture() *fakeRunner {
	return &fakeRunner{out: map[string]string{
		"SHOW PLAYBOOKS --json":                             fixturePlaybooks,
		"SHOW SESSIONS --json":                              fixtureSessions,
		"SHOW ENVS --json":                                  fixtureEnvs,
		"SHOW DEFAULTS --json":                              fixtureDefaults,
		"EXPLAIN PLAYBOOK router --json":                    fixtureExplain,
		"RESUME --list --json":                              fixtureRecent,
		"SHOW CREATE PLAYBOOK router --skip-secrets":        fixtureCreate,
		"SHOW CREATE ENV 9router --skip-secrets":            "CREATE ENV IF NOT EXISTS 9router SET ANTHROPIC_AUTH_TOKEN FROM 'keychain:pilot/9r';\n",
		"SHOW CREATE PLAYBOOK kommander-dev --skip-secrets": "CREATE PLAYBOOK IF NOT EXISTS kommander-dev ALIAS kd;\n",
	}}
}

type harness struct {
	t       *testing.T
	m       Model
	r       *fakeRunner
	clip    []string
	resumed [][]string
}

func newHarness(t *testing.T, w, h int) *harness {
	return newHarnessIn(t, w, h, "/home/p/DEV/claude-playbooks")
}

func newHarnessIn(t *testing.T, w, h int, cwd string) *harness {
	t.Helper()
	hs := &harness{t: t, r: fixture()}
	o := Options{Runner: hs.r, Now: func() time.Time { return now }, Home: "/home/p", Cwd: cwd, Poll: time.Nanosecond, NoColor: true,
		Clipboard: func(s string) error { hs.clip = append(hs.clip, s); return nil },
		Resume: func(args ...string) *exec.Cmd {
			hs.resumed = append(hs.resumed, args)
			return exec.Command("true")
		}}
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
	// tea.ExecProcess's message starts a process in a real program (the
	// harness records the resume in Options.Resume), and tea's own
	// messages (SetClipboard) are the program's business.
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
			{"sessions-recent", []string{"2", "R"}},
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
	leak = strings.Replace(leak, `{"key":"Authorization","ref":"keychain:pilot/sentry"}`,
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
	if v := hs.view(); !strings.Contains(v, "FROM 'keychain:pilot/9r'") || !strings.Contains(v, "(redacted, plaintext)") {
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
	hs.keys("R")
	if !strings.Contains(hs.view(), "reads: cpb RESUME --list --json") {
		t.Fatal(hs.view())
	}
	for _, c := range hs.r.calls {
		if !strings.HasPrefix(c, "SHOW ") && !strings.HasPrefix(c, "EXPLAIN ") && c != "RESUME --list --json" {
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
	if !strings.Contains(hs.view(), "exists. Replace it? Type y") {
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
	hs.keys("R", "down", "y")
	want := []string{"SHOW PLAYBOOK router",
		"RESUME SESSION '08c4811b-3867-4f18-b08f-de6d1e07395f' FOR PLAYBOOK kommander-dev",
		"RESUME SESSION 'd0a04774-d6ed-49f7-bb32-3c57962348fa' FOR PLAYBOOK kommander-dev"}
	if !reflect.DeepEqual(hs.clip, want) {
		t.Fatalf("copied %q", hs.clip)
	}
	hs.keys("1", "down", "c", "y")
	if len(hs.clip) != 4 || hs.clip[3] != fixtureCreate {
		t.Fatalf("SHOW CREATE copy: %q", hs.clip)
	}
}

// RESUME runs through cpb, for a session that is not live; a live one is
// refused by the TUI with the pid, and nothing starts.
func TestResume(t *testing.T) {
	hs := newHarness(t, 120, 40)
	hs.keys("2", "enter")
	if len(hs.resumed) != 0 || !strings.Contains(hs.view(), "is running in pid 47904 on ttys039") {
		t.Fatalf("live: %v\n%s", hs.resumed, hs.view())
	}
	hs.keys("R", "enter")
	if len(hs.resumed) != 0 {
		t.Fatalf("resumed a live recent session: %v", hs.resumed)
	}
	hs.keys("down", "enter")
	want := []string{"RESUME", "SESSION", "d0a04774-d6ed-49f7-bb32-3c57962348fa", "FOR", "PLAYBOOK", "kommander-dev"}
	if len(hs.resumed) != 1 || !reflect.DeepEqual(hs.resumed[0], want) {
		t.Fatalf("resume: %v", hs.resumed)
	}
	if got := resumeArgs("/home/p/plain", "abc"); !reflect.DeepEqual(got, []string{"RESUME", "SESSION", "abc"}) {
		t.Fatalf("a plain dir's session: %v", got)
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
	hs.keys("2", "R")
	if hs.m.polling() {
		t.Fatal("recent sessions are read on r, not polled")
	}
}

func TestFilter(t *testing.T) {
	hs := newHarness(t, 120, 40)
	hs.keys("/", "r", "o", "u", "enter")
	v := hs.view()
	if strings.Contains(v, "kommander-dev") && !strings.Contains(v, "router") {
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
