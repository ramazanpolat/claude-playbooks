package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ramazanpolat/claude-playbooks/internal/auth"
	"github.com/ramazanpolat/claude-playbooks/internal/config"
)

// toolExchange feeds lines to the tool server and returns its replies by id.
func toolExchange(t *testing.T, lines ...string) map[string]map[string]any {
	t.Helper()
	var out bytes.Buffer
	if err := serveAskTool(strings.NewReader(strings.Join(lines, "\n")+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	replies := map[string]map[string]any{}
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("not JSON: %s", l)
		}
		id, _ := json.Marshal(m["id"])
		replies[string(id)] = m
	}
	return replies
}

func toolCall(id int, statement string) string {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call",
		"params": map[string]any{"name": "query", "arguments": map[string]any{"statement": statement}}})
	return string(b)
}

// toolText is a tools/call reply's text and whether it is an error.
func toolText(t *testing.T, reply map[string]any) (string, bool) {
	t.Helper()
	res, ok := reply["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %v", reply)
	}
	content := res["content"].([]any)
	isErr, _ := res["isError"].(bool)
	return content[0].(map[string]any)["text"].(string), isErr
}

// The protocol: an unknown method (the newer discovery probe among them) is
// "method not found", initialize echoes the client's version, a
// notification gets no reply, and there is exactly one tool.
func TestAskToolProtocol(t *testing.T) {
	r := toolExchange(t,
		`{"jsonrpc":"2.0","id":"d","method":"server/discover","params":{}}`,
		`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"shell","arguments":{"statement":"ls"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"ping"}`,
		`not json`,
	)
	if e, ok := r[`"d"`]["error"].(map[string]any); !ok || e["code"] != float64(-32601) {
		t.Errorf("server/discover: %v", r[`"d"`])
	}
	if res := r["0"]["result"].(map[string]any); res["protocolVersion"] != "2025-11-25" {
		t.Errorf("initialize: %v", res)
	}
	tools := r["1"]["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != "query" {
		t.Errorf("tools/list: %v", tools)
	}
	if e, ok := r["2"]["error"].(map[string]any); !ok || e["code"] != float64(-32602) {
		t.Errorf("another tool: %v", r["2"])
	}
	if _, ok := r["3"]["result"]; !ok {
		t.Errorf("ping: %v", r["3"])
	}
	if e, ok := r["null"]["error"].(map[string]any); !ok || e["code"] != float64(-32700) {
		t.Errorf("a line that is not JSON: %v", r["null"])
	}
	if len(r) != 6 {
		t.Errorf("replies %d, want 6 (the notification gets none)", len(r))
	}
}

// Only the reads: anything that writes, exports or launches is refused,
// and the state is as it was.
func TestAskToolReadsOnly(t *testing.T) {
	root := askFixture(t)
	before, _ := os.ReadFile(filepath.Join(root, "work", ".playbook"))
	var lines []string
	bad := []string{"DROP PLAYBOOK work --yes", "ALTER PLAYBOOK work SET VAR X=1", "CREATE ENV x SET A=1", "DROP ENV plain",
		"SHOW CREATE ALL", "SHOW CREATE ENV route-a", "APPLY team.cpb", "SELECT key, value FROM VARS", "DESCRIBE PLAYBOOKS",
		"ALTER DEFAULTS USE ENV plain", "run work", "", "SHOW ENVS; DROP ENV plain"}
	for i, b := range bad {
		lines = append(lines, toolCall(i+1, b))
	}
	r := toolExchange(t, lines...)
	for i, b := range bad {
		// Refused by the tool's own rule, not by whatever the statement
		// would have run into.
		text, isErr := toolText(t, r[jsonID(i+1)])
		if !isErr || !strings.Contains(text, "the tool reads only") {
			t.Errorf("%q: %v %s, want the tool's refusal", b, isErr, text)
		}
	}
	after, _ := os.ReadFile(filepath.Join(root, "work", ".playbook"))
	if string(before) != string(after) {
		t.Error("a refused statement changed the playbook")
	}
	for _, p := range []string{filepath.Join(root, "work"), filepath.Join(root, ".env-sets", "plain.toml")} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s is gone", p)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".env-sets", "x.toml")); err == nil {
		t.Error("CREATE ENV ran")
	}
}

func jsonID(i int) string { b, _ := json.Marshal(i); return string(b) }

// Every view, over an install with a canary in every value, a description,
// a session folder and a transcript title: names and classes come back,
// none of the canaries and no home path.
func TestAskToolNoValues(t *testing.T) {
	root := askFixture(t)
	t.Setenv("CPB_PLAYBOOKS_DIR", root)
	config.PlaybooksDir = root
	home, _ := os.UserHomeDir()
	now := time.Now()
	work := filepath.Join(root, "work")
	cwd := filepath.Join(home, "CANARY-project")
	writeSessionFile(t, work, 101, sidLive, cwd, "interactive", liveStart, now.Add(-time.Hour))
	writeTranscript(t, work, cwd, sidLive, "claude-sonnet-5-5", "CANARY-title", now)
	fakeProcs(t, map[int]string{101: liveStart})

	views := []string{"SHOW PLAYBOOKS", "SHOW PLAYBOOK work", "SHOW ENVS", "SHOW ENV route-a --json", "SHOW DEFAULTS",
		"SHOW SESSIONS", "SHOW SESSIONS FOR PLAYBOOK work", "EXPLAIN PLAYBOOK work", "show envs"}
	var lines []string
	for i, v := range views {
		lines = append(lines, toolCall(i+1, v))
	}
	r := toolExchange(t, lines...)
	all := ""
	for i, v := range views {
		text, isErr := toolText(t, r[jsonID(i+1)])
		if isErr {
			t.Errorf("%s: refused: %s", v, text)
		}
		all += text + "\n"
		if strings.Contains(text, "CANARY") || strings.Contains(text, home) {
			t.Errorf("%s gave a value, a folder, a title or a home path:\n%s", v, text)
		}
	}
	for _, want := range []string{`"name":"work"`, `"name":"route-a"`, `"route":"away"`, `"count":1`, `"kind":"interactive"`, `"model":"claude-sonnet-5-5"`, `"envs":["route-a"]`} {
		if !strings.Contains(all, want) {
			t.Errorf("the views lack %s:\n%s", want, all)
		}
	}
	for _, never := range []string{`"vars"`, `"cwd"`, `"session_id"`, `"resume"`, `"path"`, `"description"`, `"source"`, `"config_dir"`, `"auth"`} {
		if strings.Contains(all, never) {
			t.Errorf("the views carry %s:\n%s", never, all)
		}
	}
}

// The tool touches no credential: every statement it admits runs with a trip
// on the token file, the login stores and the Keychain, over an install
// where each is there to be found, and none fires.
func TestAskToolTouchesNoCredential(t *testing.T) {
	root := askFixture(t)
	t.Setenv("CPB_PLAYBOOKS_DIR", root)
	config.PlaybooksDir = root
	seedFlatPlaybook(t, "solo")
	mustStmt(t, "ALTER PLAYBOOK solo SET login = 'isolated'")
	writeFile(t, filepath.Join(root, "solo", ".credentials.json"), `{"claudeAiOauth":{"accessToken":"CANARY-own-login"}}`)
	tokenFile := filepath.Join(t.TempDir(), "oauth-token")
	writeFile(t, tokenFile, "CANARY-machine-token\n")
	t.Setenv("CPB_OAUTH_TOKEN_FILE", tokenFile)
	home, _ := os.UserHomeDir()
	now := time.Now()
	cwd := filepath.Join(home, "project")
	writeSessionFile(t, filepath.Join(root, "work"), 101, sidLive, cwd, "interactive", liveStart, now.Add(-time.Hour))
	writeTranscript(t, filepath.Join(root, "work"), cwd, sidLive, "claude-sonnet-5-5", "title", now)
	fakeProcs(t, map[int]string{101: liveStart})

	var fired []string
	auth.CredentialTouch = func(what string) { fired = append(fired, what) }
	t.Cleanup(func() { auth.CredentialTouch = nil })
	statements := []string{"SHOW PLAYBOOKS", "SHOW PLAYBOOK work", "SHOW PLAYBOOK solo", "SHOW ENVS", "SHOW ENV route-a",
		"SHOW DEFAULTS", "SHOW SESSIONS", "SHOW SESSIONS FOR PLAYBOOK work", "EXPLAIN PLAYBOOK work", "EXPLAIN PLAYBOOK solo"}
	var lines []string
	for i, st := range statements {
		lines = append(lines, toolCall(i+1, st))
	}
	r := toolExchange(t, lines...)
	for i, st := range statements {
		if text, isErr := toolText(t, r[jsonID(i+1)]); isErr {
			t.Errorf("%s: refused: %s", st, text)
		}
	}
	if len(fired) > 0 {
		t.Errorf("the tool touched a credential: %v", fired)
	}
	// The trip itself works: the same EXPLAIN as a statement judges the login.
	askCapture([]string{"EXPLAIN", "PLAYBOOK", "solo", "--json"})
	if len(fired) == 0 {
		t.Error("EXPLAIN's own login check set off no trip; the guard proves nothing")
	}
}

// The claude process cpb ask started, the tool's parent, is not counted as
// one of the playbook's sessions.
func TestAskToolSessionsLeaveOutTheAsk(t *testing.T) {
	root := askFixture(t)
	t.Setenv("CPB_PLAYBOOKS_DIR", root)
	config.PlaybooksDir = root
	home, _ := os.UserHomeDir()
	now := time.Now()
	work := filepath.Join(root, "work")
	writeSessionFile(t, work, 101, sidLive, filepath.Join(home, "project"), "interactive", liveStart, now.Add(-time.Hour))
	writeSessionFile(t, work, 102, sidSpare, filepath.Join(home, "project"), "interactive", liveStart, now)
	fakeProcs(t, map[int]string{101: liveStart, 102: liveStart})
	old := askToolParent
	t.Cleanup(func() { askToolParent = old })
	askToolParent = func() int { return 102 }
	r := toolExchange(t, toolCall(1, "SHOW SESSIONS FOR PLAYBOOK work"), toolCall(2, "SHOW SESSIONS"))
	for _, id := range []string{"1", "2"} {
		text, isErr := toolText(t, r[id])
		if isErr || !strings.Contains(text, `"count":1`) {
			t.Errorf("sessions with the ask's own claude left out: %s", text)
		}
	}
}

// The tool drops the credentials Claude Code hands it.
func TestAskToolDropsCredentials(t *testing.T) {
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "t")
	t.Setenv("ANTHROPIC_API_KEY", "k")
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "m")
	t.Setenv("PATH_KEEP_ME", "x")
	askToolDropCredentials()
	for _, k := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "CLAUDE_CODE_MESSAGING_TOKEN"} {
		if os.Getenv(k) != "" {
			t.Errorf("%s is still set", k)
		}
	}
	if os.Getenv("PATH_KEEP_ME") != "x" {
		t.Error("an ordinary variable was dropped")
	}
}
