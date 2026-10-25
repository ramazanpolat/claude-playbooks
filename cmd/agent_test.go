package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envset"
)

// fakeAgentClaude puts a `claude` on PATH that answers cpb agent's call
// with plan (a JSON plan, or a whole result object when it starts with
// {"is_error"), and records what it received in the returned directory:
// argv (NUL-separated), env, pwd and stdin. Its paths are written into the
// script: cpb agent passes the child no variable the test could add.
func fakeAgentClaude(t *testing.T, plan string) string {
	t.Helper()
	dir := t.TempDir()
	rec := filepath.Join(dir, "rec")
	if err := os.MkdirAll(rec, 0o755); err != nil {
		t.Fatal(err)
	}
	answer := plan
	if !strings.HasPrefix(plan, `{"is_error"`) {
		answer = `{"type":"result","subtype":"success","is_error":false,"result":"","structured_output":` + plan + `}`
	}
	if err := os.WriteFile(filepath.Join(dir, "answer"), []byte(answer), 0o644); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then echo "2.1.310 (Claude Code)"; exit 0; fi
printf '%s\0' "$@" > "` + rec + `/argv"
env > "` + rec + `/env"
pwd > "` + rec + `/pwd"
cat > "` + rec + `/stdin"
cat "` + dir + `/answer"
`
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return rec
}

func readRec(t *testing.T, rec, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(rec, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// agentRun runs cpb agent with flags, as the command line would.
func agentRun(t *testing.T, flags map[string]string, request ...string) (string, error) {
	t.Helper()
	for k, v := range flags {
		if err := agentCmd.Flags().Set(k, v); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, k := range []string{"run", "yes", "json"} {
			agentCmd.Flags().Set(k, "false")
		}
		agentCmd.Flags().Set("model", "")
	})
	var err error
	out := captureStdout(t, func() { err = runAgent(agentCmd, request) })
	return out, err
}

// agentFixture is a registry with a playbook "work" on env set route-a,
// which routes away, and a plain env set "plain".
func agentFixture(t *testing.T) string {
	t.Helper()
	resetCommandTestState(t)
	aliasTestHome(t)
	t.Setenv("ANTHROPIC_BASE_URL", "")
	root := config.ResolvePlaybooksDir()
	sets := envset.Dir(root)
	if err := os.MkdirAll(sets, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(sets, "route-a.toml"), `description = "CANARY-description"
[set]
ANTHROPIC_BASE_URL = "http://127.0.0.1:9/v1"
ANTHROPIC_AUTH_TOKEN = "CANARY-env-set-value"
[refs]
REF_KEY = "keychain:CANARY-env-set-ref"
`)
	writeFile(t, filepath.Join(sets, "plain.toml"), "[set]\nMAX_THINKING_TOKENS = \"8000\"\n")
	seedFlatPlaybook(t, "work")
	mustStmt(t, "ALTER PLAYBOOK work USE ENV route-a SET VAR MY_SECRET=CANARY-manifest-value AS PLAINTEXT")
	pb := filepath.Join(root, "work")
	writeFile(t, filepath.Join(pb, ".credentials.json"), `{"claudeAiOauth":{"accessToken":"CANARY-credentials"}}`)
	writeFile(t, filepath.Join(pb, "settings.json"), `{"env":{"SOME_VAR":"CANARY-settings-env"}}`)
	home, _ := os.UserHomeDir()
	writeFile(t, filepath.Join(home, ".claude", ".credentials.json"), `{"claudeAiOauth":{"accessToken":"CANARY-machine-login"}}`)
	return root
}

const agentListPlan = `{"answer":"Your env sets.","steps":[{"args":["SHOW","ENVS"],"why":"Lists the env sets (v3 called them env profiles)."}],"notes":[]}`

// The mechanism behind "no value reaches the model": a canary in every value
// an install can hold, and in the shell environment as a route variable and
// as an unrelated one, and none of it in what claude receives.
func TestAgentSendsNoValue(t *testing.T) {
	agentFixture(t)
	rec := fakeAgentClaude(t, agentListPlan)
	t.Setenv("ANTHROPIC_BASE_URL", "http://127.0.0.1:9/CANARY-base-url")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "CANARY-shell-token")
	t.Setenv("ANTHROPIC_API_KEY", "CANARY-shell-key")
	t.Setenv("FOO_TOKEN", "CANARY-unrelated")
	t.Setenv("CLAUDE_CONFIG_DIR", "/CANARY-config-dir")
	// The default login reads no credential: not cpb's machine token either.
	tokenFile := filepath.Join(t.TempDir(), "oauth-token")
	writeFile(t, tokenFile, "CANARY-machine-token\n")
	t.Setenv("CPB_OAUTH_TOKEN_FILE", tokenFile)

	out, err := agentRun(t, nil, "list the env profiles")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"argv", "env", "stdin"} {
		if got := readRec(t, rec, f); strings.Contains(got, "CANARY") {
			t.Errorf("claude received a canary in its %s:\n%s", f, got)
		}
	}
	stdin := readRec(t, rec, "stdin")
	for _, want := range []string{`"name":"work"`, `"name":"route-a"`, `"route":"away"`, `"name":"plain"`, `"route":"anthropic"`, "Request: list the env profiles"} {
		if !strings.Contains(stdin, want) {
			t.Errorf("the request lacks %s:\n%s", want, stdin)
		}
	}
	if !strings.Contains(out, "cpb SHOW ENVS") {
		t.Errorf("the plan was not shown:\n%s", out)
	}
}

func TestAgentCallsClaudeWithoutUserConfig(t *testing.T) {
	agentFixture(t)
	rec := fakeAgentClaude(t, agentListPlan)
	if _, err := agentRun(t, map[string]string{"model": "model-x"}, "list"); err != nil {
		t.Fatal(err)
	}
	argv := strings.Split(strings.TrimSuffix(readRec(t, rec, "argv"), "\x00"), "\x00")
	joined := strings.Join(argv, " ")
	for _, want := range []string{"-p", "--setting-sources project", "--strict-mcp-config", "--tools  --disable-slash-commands",
		"--no-session-persistence", "--output-format json", "--model model-x", "--json-schema {"} {
		if !strings.Contains(joined, want) {
			t.Errorf("claude's arguments lack %q: %q", want, argv)
		}
	}
	pwd := strings.TrimSpace(readRec(t, rec, "pwd"))
	if underHome(pwd) || !strings.Contains(filepath.Base(pwd), "cpb-agent-") {
		t.Errorf("claude ran in %s, want a fresh directory outside home", pwd)
	}
	if _, err := os.Stat(pwd); !os.IsNotExist(err) {
		t.Errorf("the directory claude ran in is left behind: %s", pwd)
	}
}

func TestAgentEnvIsAnAllowlist(t *testing.T) {
	in := []string{"PATH=/bin", "HOME=/home/user", "LANG=C", "LC_ALL=C", "TERM=xterm",
		"FOO_TOKEN=x", "AWS_SECRET_ACCESS_KEY=x", "ANTHROPIC_BEDROCK_BASE_URL=x", "ANTHROPIC_UNIX_SOCKET=x",
		"CLAUDE_CODE_API_KEY_HELPER_TTL_MS=1", "CLAUDE_CONFIG_DIR=/x", "CPB_CONFIG_DIR=/x", "ANTHROPIC_MODEL=m",
		"ANTHROPIC_AUTH_TOKEN=x", "ANTHROPIC_API_KEY=k", "CLAUDE_CODE_OAUTH_TOKEN=o"}
	got := strings.Join(agentEnv(in), "\n") + "\n"
	for _, keep := range []string{"PATH=/bin", "HOME=/home/user", "LANG=C", "LC_ALL=C", "TERM=xterm",
		"ANTHROPIC_API_KEY=k", "CLAUDE_CODE_OAUTH_TOKEN=o", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1"} {
		if !strings.Contains(got, keep+"\n") {
			t.Errorf("dropped %s:\n%s", keep, got)
		}
	}
	for _, drop := range []string{"FOO_TOKEN", "AWS_SECRET", "BEDROCK", "UNIX_SOCKET", "KEY_HELPER", "CLAUDE_CONFIG_DIR", "CPB_CONFIG_DIR", "ANTHROPIC_MODEL", "ANTHROPIC_AUTH_TOKEN"} {
		if strings.Contains(got, drop) {
			t.Errorf("kept %s:\n%s", drop, got)
		}
	}
	// A key that came with another endpoint belongs to it.
	for _, route := range []string{"ANTHROPIC_BASE_URL=http://127.0.0.1:9/v1", "CLAUDE_CODE_USE_BEDROCK=1", "CLAUDE_CODE_USE_VERTEX=true", "CLAUDE_CODE_USE_FOUNDRY=1"} {
		got = strings.Join(agentEnv(append(in, route)), "\n")
		if strings.Contains(got, "ANTHROPIC_API_KEY") || strings.Contains(got, "CLAUDE_CODE_OAUTH_TOKEN") || strings.Contains(got, "ANTHROPIC_BASE_URL") {
			t.Errorf("with %s the Anthropic credentials went along:\n%s", route, got)
		}
	}
	if got = strings.Join(agentEnv(append(in, "CLAUDE_CODE_USE_FOUNDRY=0")), "\n"); !strings.Contains(got, "ANTHROPIC_API_KEY=k") || strings.Contains(got, "FOUNDRY") {
		t.Errorf("a Foundry switch that is off: want the key kept and the switch dropped:\n%s", got)
	}
	got = strings.Join(agentEnv(append(in, "ANTHROPIC_BASE_URL=https://api.anthropic.com")), "\n")
	if !strings.Contains(got, "ANTHROPIC_API_KEY=k") || strings.Contains(got, "ANTHROPIC_BASE_URL") {
		t.Errorf("Anthropic's own endpoint: want the key kept and the variable dropped:\n%s", got)
	}
}

func TestAgentNoLogin(t *testing.T) {
	agentFixture(t)
	fakeAgentClaude(t, `{"is_error":true,"result":"Invalid API key · Please run /login","subtype":"success"}`)
	_, err := agentRun(t, nil, "list")
	if err == nil || !strings.Contains(err.Error(), "Invalid API key") || !strings.Contains(err.Error(), "/login") {
		t.Fatalf("got %v, want Claude Code's error and the login hint", err)
	}
	fakeAgentClaude(t, `{"is_error":false,"result":"no plan here","subtype":"success"}`)
	if _, err := agentRun(t, nil, "list"); err == nil || !strings.Contains(err.Error(), "not a plan") {
		t.Fatalf("an answer that is not a plan: %v", err)
	}
}

func TestAgentUsage(t *testing.T) {
	agentFixture(t)
	fakeAgentClaude(t, agentListPlan)
	for _, c := range []struct {
		flags map[string]string
		args  []string
	}{
		{nil, nil},
		{map[string]string{"yes": "true"}, []string{"list"}},
		{map[string]string{"json": "true", "run": "true"}, []string{"list"}},
	} {
		var err error
		captureStderr(t, func() { _, err = agentRun(t, c.flags, c.args...) })
		if code, ok := exitCode(err); !ok || code != 2 {
			t.Errorf("%v %v: %v, want exit 2", c.flags, c.args, err)
		}
	}
}

func step(t *testing.T, root string, args ...string) agentStep {
	t.Helper()
	s := agentStep{Args: args}
	checkAgentStep(&s, root)
	return s
}

func TestAgentChecksSteps(t *testing.T) {
	root := agentFixture(t)
	for _, c := range []struct {
		args   []string
		status string
		reason string
	}{
		{[]string{"cpb", "SHOW", "ENVS"}, stepOK, ""},
		{[]string{"SELECT name, envs FROM PLAYBOOKS"}, stepOK, ""},
		{[]string{"ALTER", "PLAYBOOK", "work", "ALLOW", "TOOL", "Bash(git diff *)"}, stepOK, ""},
		{[]string{"ALTER", "PLAYBOOK", "work", "SET", "model", "=", "claude-sonnet-5-5"}, stepOK, ""},
		{[]string{"SHOW", "ENVZ"}, stepRejected, ""},
		{[]string{"cat", "/home/user/.claude-playbooks/.env-sets/route-a.toml"}, stepRejected, "not a command cpb agent proposes"},
		{[]string{"self-update"}, stepRejected, "not a command"},
		{[]string{"--playbooks-dir", "/x", "SHOW", "ENVS"}, stepRejected, "not a flag"},
		{[]string{"run", "nosuch"}, stepRejected, `unknown playbook "nosuch"`},
		{[]string{"start", "--env-set", "nosuch", "/tmp/cpb-x"}, stepRejected, "nosuch"},
		{[]string{"auth", "logout"}, stepRejected, "auth status"},
		{[]string{"ALTER", "ENV", "route-a", "SET", "ANTHROPIC_AUTH_TOKEN", "FROM", "<secret-ref>"}, stepPlaceholder, "<secret-ref>"},
		{[]string{}, stepRejected, "empty"},
	} {
		s := step(t, root, c.args...)
		if s.Status != c.status || !strings.Contains(s.Reason, c.reason) {
			t.Errorf("%q: %s (%s), want %s (%q)", c.args, s.Status, s.Reason, c.status, c.reason)
		}
	}
}

func TestAgentGuards(t *testing.T) {
	root := agentFixture(t)
	for _, c := range []struct {
		args []string
		kind string
		what string
	}{
		{[]string{"DROP", "PLAYBOOK", "work"}, guardDestructive, "removes playbook work"},
		{[]string{"DROP", "ENV", "plain"}, guardDestructive, "removes env set plain"},
		{[]string{"CREATE", "OR", "REPLACE", "ENV", "plain", "SET", "A=1"}, guardDestructive, "replaces env set plain"},
		{[]string{"ALTER", "PLAYBOOK", "work", "UNSET", "VAR", "MY_SECRET"}, guardCredential, "forgets MY_SECRET"},
		{[]string{"ALTER", "PLAYBOOK", "work", "RESET", "model"}, guardDestructive, "model"},
		{[]string{"ALTER", "PLAYBOOK", "work", "SET", "login", "=", "isolated"}, guardCredential, "login"},
		{[]string{"ALTER", "ENV", "plain", "SET", "ANTHROPIC_AUTH_TOKEN", "FROM", "keychain:x"}, guardCredential, "from the reference keychain:x"},
		{[]string{"ALTER", "ENV", "plain", "BLOCK", "CLAUDE_CODE_OAUTH_TOKEN"}, guardCredential, "CLAUDE_CODE_OAUTH_TOKEN"},
		{[]string{"CREATE", "PLAYBOOK", "rev", "FROM", "https://github.com/example/reviewer", "BRANCH", "main"}, guardFetch, "from https://github.com/example/reviewer BRANCH main"},
		{[]string{"ALTER", "PLAYBOOK", "work", "ADD", "MARKETPLACE", "team", "FROM", "github:example/team-plugins"}, guardFetch, "github:example/team-plugins"},
		{[]string{"ALTER", "PLAYBOOK", "work", "ADD", "PLUGIN", "reviewer@team"}, guardFetch, "reviewer@team"},
		{[]string{"play", "github:example/recipes/rev.cpb@v1"}, guardFetch, "github:example/recipes/rev.cpb@v1"},
		{[]string{"update", "work"}, guardFetch, "work"},
		{[]string{"start", "--delete", "/tmp/cpb-agent-test-scratch"}, guardDestructive, "deletes /tmp/cpb-agent-test-scratch"},
		{[]string{"run", "--block", "CLAUDE_CODE_OAUTH_TOKEN", "work"}, guardCredential, "CLAUDE_CODE_OAUTH_TOKEN"},
		{[]string{"APPLY", "team.cpb"}, guardDestructive, "team.cpb"},
	} {
		s := step(t, root, c.args...)
		found := false
		for _, g := range s.Guards {
			if g.Kind == c.kind && strings.Contains(g.What, c.what) {
				found = true
			}
		}
		if s.Status != stepOK || !found {
			t.Errorf("%q: %s %s %+v, want a %s guard naming %q", c.args, s.Status, s.Reason, s.Guards, c.kind, c.what)
		}
	}
	for _, plain := range [][]string{
		{"SHOW", "PLAYBOOKS"}, {"EXPLAIN", "PLAYBOOK", "work"}, {"ALTER", "PLAYBOOK", "work", "ADD", "ENV", "plain"},
		{"APPLY", "team.cpb", "--dry-run"}, {"auth", "status"}, {"run", "work", "-p", "hi"},
	} {
		if s := step(t, root, plain...); s.Status != stepOK || len(s.Guards) > 0 {
			t.Errorf("%q: %s %s %+v, want a plain step", plain, s.Status, s.Reason, s.Guards)
		}
	}
}

func TestAgentRouteAway(t *testing.T) {
	root := agentFixture(t)
	away := step(t, root, "start", "--env-set", "route-a", "--delete", "/tmp/cpb-agent-test-scratch")
	if !away.RouteAway || away.Status != stepOK {
		t.Fatalf("a start on route-a: %+v, want route_away", away)
	}
	if run := step(t, root, "run", "work"); !run.RouteAway {
		t.Errorf("run work (on route-a): %+v, want route_away", run)
	}
	home := step(t, root, "start", "--env-set", "plain", "/tmp/cpb-agent-test-scratch")
	if home.RouteAway {
		t.Errorf("a start on plain: %+v, want Anthropic", home)
	}
	if !strings.Contains(strings.Join(home.CPBNotes, " "), "/login") {
		t.Errorf("a start: notes %q, want the shared-login note", home.CPBNotes)
	}
	// The caller's environment counts, as at launch.
	t.Setenv("ANTHROPIC_BASE_URL", "http://127.0.0.1:9/v1")
	if s := step(t, root, "start", "/tmp/cpb-agent-test-scratch"); !s.RouteAway {
		t.Errorf("an exported base URL: %+v, want route_away", s)
	}

	// Shown with the directory it runs from, and the files it would have
	// taken from the caller's directory.
	t.Setenv("ANTHROPIC_BASE_URL", "")
	homeDir, _ := os.UserHomeDir()
	writeFile(t, filepath.Join(homeDir, "CLAUDE.md"), "x")
	proj := filepath.Join(homeDir, "proj")
	writeFile(t, filepath.Join(proj, "CLAUDE.md"), "x")
	p := &agentPlan{Steps: []agentStep{away}}
	var b strings.Builder
	printAgentPlan(&b, p, proj, "/tmp")
	out := b.String()
	for _, want := range []string{"(cd /tmp && cpb start --env-set route-a --delete /tmp/cpb-agent-test-scratch)", "~/proj/CLAUDE.md", "~/CLAUDE.md", "! deletes"} {
		if !strings.Contains(out, want) {
			t.Errorf("shown plan lacks %q:\n%s", want, out)
		}
	}
}

// fakeAgentSelf replaces the cpb that runs a step with a recorder; code is
// the exit code it returns for steps whose first word is fail.
func fakeAgentSelf(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	script := `#!/bin/sh
printf '%s|%s\n' "$(pwd)" "$*" >> "` + log + `"
[ "$1" = "fail" ] && exit 7
exit 0
`
	self := filepath.Join(dir, "cpb")
	if err := os.WriteFile(self, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	oldSelf, oldTTY, oldConfirm := agentSelf, agentTTY, agentConfirm
	agentSelf = func() (string, error) { return self, nil }
	t.Cleanup(func() { agentSelf, agentTTY, agentConfirm = oldSelf, oldTTY, oldConfirm })
	return log
}

func plan(t *testing.T, root string, steps ...[]string) *agentPlan {
	t.Helper()
	p := &agentPlan{}
	for _, a := range steps {
		p.Steps = append(p.Steps, agentStep{Args: a, Why: "w"})
	}
	checkAgentPlan(p, root)
	return p
}

func runPlanQuiet(t *testing.T, p *agentPlan, yes bool) error {
	t.Helper()
	var err error
	captureStderr(t, func() { captureStdout(t, func() { err = runAgentPlan(p, yes, "/") }) })
	return err
}

func TestAgentRunConfirms(t *testing.T) {
	root := agentFixture(t)
	log := fakeAgentSelf(t)
	ran := func() string { data, _ := os.ReadFile(log); return string(data) }
	asked := 0
	answer := false
	agentConfirm = func(string) bool { asked++; return answer }

	// No terminal: nothing runs without --yes, and a guarded plan never.
	agentTTY = func() bool { return false }
	if err := runPlanQuiet(t, plan(t, root, []string{"SHOW", "ENVS"}), false); exitOf(err) != 1 {
		t.Errorf("no terminal, no --yes: %v, want exit 1", err)
	}
	if err := runPlanQuiet(t, plan(t, root, []string{"DROP", "ENV", "plain"}), true); exitOf(err) != 1 {
		t.Errorf("no terminal, a guarded step, --yes: %v, want exit 1", err)
	}
	if ran() != "" || asked != 0 {
		t.Fatalf("ran %q, asked %d times, without a terminal", ran(), asked)
	}
	if err := runPlanQuiet(t, plan(t, root, []string{"SHOW", "ENVS"}), true); err != nil || !strings.Contains(ran(), "|SHOW ENVS") {
		t.Fatalf("no terminal, --yes, nothing guarded: %v, ran %q", err, ran())
	}

	// A terminal: --yes skips the question only when nothing is guarded.
	agentTTY = func() bool { return true }
	asked = 0
	if err := runPlanQuiet(t, plan(t, root, []string{"DROP", "ENV", "plain"}), true); exitOf(err) != 1 || asked != 1 {
		t.Errorf("a declined guarded step under --yes: %v, asked %d, want exit 1 after one question", err, asked)
	}
	if strings.Contains(ran(), "DROP") {
		t.Fatalf("a declined step ran: %q", ran())
	}
	answer = true
	if err := runPlanQuiet(t, plan(t, root, []string{"DROP", "ENV", "plain"}), false); err != nil || !strings.Contains(ran(), "|DROP ENV plain") {
		t.Fatalf("a confirmed step: %v, ran %q", err, ran())
	}

	// A step that cannot run stops the plan before anything runs.
	before := ran()
	if err := runPlanQuiet(t, plan(t, root, []string{"SHOW", "ENVS"}, []string{"cat", "x"}), true); exitOf(err) != 1 || ran() != before {
		t.Errorf("a rejected step: %v, ran %q", err, ran())
	}
}

func TestAgentRunStepsInOrder(t *testing.T) {
	root := agentFixture(t)
	log := fakeAgentSelf(t)
	agentTTY = func() bool { return true }
	agentConfirm = func(string) bool { return true }

	p := plan(t, root, []string{"SHOW", "ENVS"}, []string{"start", "--env-set", "route-a", "/tmp/cpb-agent-test-scratch"}, []string{"SHOW", "PLAYBOOKS"})
	if err := runPlanQuiet(t, p, false); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(func() string { d, _ := os.ReadFile(log); return string(d) }()), "\n")
	if len(lines) != 3 {
		t.Fatalf("ran %q", lines)
	}
	cwd, _ := os.Getwd()
	if !strings.HasPrefix(lines[0], cwd+"|") {
		t.Errorf("a plain step ran in %q, want the caller's directory %s", lines[0], cwd)
	}
	dir, _, _ := strings.Cut(lines[1], "|")
	if underHome(dir) || !strings.Contains(dir, "cpb-agent-") {
		t.Errorf("a route-away step ran in %s, want a fresh directory outside home", dir)
	}

	// A failing step stops the plan and passes its code.
	os.Remove(log)
	p = plan(t, root, []string{"SHOW", "ENVS"}, []string{"SHOW", "PLAYBOOKS"})
	p.Steps[0].Args = []string{"fail"}
	if err := runPlanQuiet(t, p, false); exitOf(err) != 7 {
		t.Errorf("a failing step: %v, want its exit code 7", err)
	}
	if d, _ := os.ReadFile(log); strings.Contains(string(d), "PLAYBOOKS") {
		t.Errorf("the step after a failure ran: %s", d)
	}
}

func exitOf(err error) int {
	if err == nil {
		return 0
	}
	if code, ok := exitCode(err); ok {
		return code
	}
	return -1
}

// The reference the model gets must say what this version does: its grammar
// is SPEC.md's, and every example in it parses.
func TestAgentReferenceMatchesSpec(t *testing.T) {
	spec, err := os.ReadFile(filepath.Join("..", "SPEC.md"))
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile("(?s)### Grammar\n\n```\n(.*?)\n```")
	want := block.FindSubmatch(spec)
	got := regexp.MustCompile("(?s)The grammar, from cpb's specification:\n\n```\n(.*?)\n```").FindStringSubmatch(agentReference)
	if want == nil || got == nil {
		t.Fatal("grammar block not found in SPEC.md or the reference")
	}
	if got[1] != string(want[1]) {
		t.Fatal("the reference's grammar block differs from SPEC.md's: copy it again")
	}
	examples := regexp.MustCompile("(?s)Examples:\n\n```\n(.*?)\n```").FindStringSubmatch(agentReference)
	if examples == nil {
		t.Fatal("no examples block")
	}
	n := 0
	for _, line := range strings.Split(examples[1], "\n") {
		words := shellWords(t, line)
		if len(words) < 2 || words[0] != "cpb" {
			t.Errorf("not a cpb line: %q", line)
			continue
		}
		n++
		s := agentStep{Args: words[1:]}
		if err := checkAgentStatement(&s, words[1:]); err != nil {
			t.Errorf("%s: %v", line, err)
		}
	}
	if n < 10 {
		t.Errorf("only %d examples", n)
	}
}

// shellWords splits a line as sh would, for the reference's examples: words,
// single and double quotes.
func shellWords(t *testing.T, line string) []string {
	t.Helper()
	var words []string
	var cur strings.Builder
	in, quote := false, rune(0)
	for _, r := range line {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote != 0:
			cur.WriteRune(r)
		case r == '\'' || r == '"':
			quote, in = r, true
		case r == ' ' || r == '\t':
			if in {
				words, in = append(words, cur.String()), false
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
			in = true
		}
	}
	if quote != 0 {
		t.Fatalf("unbalanced quote: %q", line)
	}
	if in {
		words = append(words, cur.String())
	}
	return words
}

func TestAgentJSONOutput(t *testing.T) {
	agentFixture(t)
	fakeAgentClaude(t, `{"answer":"a","steps":[{"args":["DROP","ENV","plain"],"why":"w"},{"args":["cat","x"],"why":"w"}],"notes":["n"]}`)
	out, err := agentRun(t, map[string]string{"json": "true"}, "drop plain")
	if err != nil {
		t.Fatal(err)
	}
	var p agentPlan
	if err := json.Unmarshal([]byte(out), &p); err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	if len(p.Steps) != 2 || p.Steps[0].Status != stepOK || len(p.Steps[0].Guards) != 1 || p.Steps[1].Status != stepRejected || p.Steps[0].Command != "cpb DROP ENV plain" {
		t.Fatalf("%+v", p)
	}
}

func TestQuotedWordsSuggestAgent(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	err := runStatement([]string{"print every env profile"})
	if err == nil || !strings.Contains(err.Error(), "cpb agent 'print every env profile'") {
		t.Fatalf("%v", err)
	}
	if err := runStatement([]string{"SHOW ENVZ"}); err == nil || strings.Contains(err.Error(), "cpb agent") {
		t.Fatalf("a mistyped statement: %v, want no assistant hint", err)
	}
}

// CPB_AGENT_LOGIN=token: cpb's machine token signs in, in the child's
// environment only; it replaces any Anthropic key or token exported there.
func TestAgentTokenLogin(t *testing.T) {
	agentFixture(t)
	rec := fakeAgentClaude(t, agentListPlan)
	tokenFile := filepath.Join(t.TempDir(), "oauth-token")
	writeFile(t, tokenFile, "CANARY-machine-token\n")
	t.Setenv("CPB_OAUTH_TOKEN_FILE", tokenFile)
	t.Setenv("CPB_AGENT_LOGIN", "token")
	t.Setenv("ANTHROPIC_API_KEY", "other-key")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "other-token")
	if _, err := agentRun(t, nil, "list"); err != nil {
		t.Fatal(err)
	}
	env := readRec(t, rec, "env")
	if strings.Count(env, "CANARY-machine-token") != 1 || !strings.Contains(env, "CLAUDE_CODE_OAUTH_TOKEN=CANARY-machine-token\n") {
		t.Errorf("the machine token is not the child's one CLAUDE_CODE_OAUTH_TOKEN:\n%s", env)
	}
	if strings.Contains(env, "other-key") || strings.Contains(env, "other-token") {
		t.Errorf("an exported key or token went along with the machine token:\n%s", env)
	}
	for _, f := range []string{"argv", "stdin"} {
		if strings.Contains(readRec(t, rec, f), "CANARY") {
			t.Errorf("the machine token reached claude's %s", f)
		}
	}

	// No machine token: refused, nothing sent.
	os.Remove(filepath.Join(rec, "argv"))
	t.Setenv("CPB_OAUTH_TOKEN_FILE", filepath.Join(t.TempDir(), "none"))
	if _, err := agentRun(t, nil, "list"); err == nil || !strings.Contains(err.Error(), "no machine token") || !strings.Contains(err.Error(), "claude setup-token") {
		t.Errorf("no token file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rec, "argv")); err == nil {
		t.Error("claude ran without the machine token")
	}

	// An unknown mode is a usage error.
	t.Setenv("CPB_AGENT_LOGIN", "keychain")
	var err error
	captureStderr(t, func() { _, err = agentRun(t, nil, "list") })
	if exitOf(err) != 2 {
		t.Errorf("CPB_AGENT_LOGIN=keychain: %v, want exit 2", err)
	}
}

// The guard holds whatever builds the child's environment: anything that
// would not reach Anthropic is refused.
func TestAgentEgressGuard(t *testing.T) {
	base := []string{"PATH=/bin", "HOME=/home/user"}
	if err := agentEgressGuard(base); err != nil {
		t.Errorf("a clean environment: %v", err)
	}
	if err := agentEgressGuard(append(base, "ANTHROPIC_BASE_URL=https://api.anthropic.com")); err != nil {
		t.Errorf("Anthropic's own endpoint: %v", err)
	}
	for _, away := range []string{"ANTHROPIC_BASE_URL=http://127.0.0.1:9/v1", "CLAUDE_CODE_USE_BEDROCK=1", "CLAUDE_CODE_USE_VERTEX=1", "CLAUDE_CODE_USE_FOUNDRY=true"} {
		if err := agentEgressGuard(append(base, away)); err == nil || !strings.Contains(err.Error(), "Anthropic only") {
			t.Errorf("%s: %v, want a refusal", away, err)
		}
	}
}
