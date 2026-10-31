package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ramazanpolat/claude-playbooks/internal/account"
	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envset"
)

// fakeAskClaude puts a `claude` on PATH that answers cpb ask's call
// with plan (a JSON plan, or a whole result object when it starts with
// {"is_error"), and records what it received in the returned directory:
// argv (NUL-separated), env, pwd and stdin. Its paths are written into the
// script: cpb ask passes the child no variable the test could add.
func fakeAskClaude(t *testing.T, plan string) string {
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
if [ "$1" = "--version" ]; then env > "` + rec + `/version-env"; echo "2.1.310 (Claude Code)"; exit 0; fi
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

// askRun runs cpb ask with flags, as the command line would.
func askRun(t *testing.T, flags map[string]string, request ...string) (string, error) {
	t.Helper()
	for k, v := range flags {
		if err := askCmd.Flags().Set(k, v); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, k := range []string{"json"} {
			askCmd.Flags().Set(k, "false")
		}
		askCmd.Flags().Set("model", "")
	})
	var err error
	out := captureStdout(t, func() { err = runAsk(askCmd, request) })
	return out, err
}

// askFixture is a registry with a playbook "work" on env set route-a,
// which routes away, and a plain env set "plain".
func askFixture(t *testing.T) string {
	t.Helper()
	resetCommandTestState(t)
	aliasTestHome(t)
	// A claude new enough for the macOS shadow (auth.MinShadowClaude): work
	// holds a login of its own and routes away.
	savedVersion := claudeVersion
	claudeVersion = func() string { return "2.1.302" }
	t.Cleanup(func() { claudeVersion = savedVersion })
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
	// An account full of canaries: only its name, provider and classes may
	// reach the model.
	adir := filepath.Join(root, ".accounts")
	if err := os.MkdirAll(adir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(adir, "edge.toml"), `description = "CANARY-account-description"
provider = "gateway"
base_url = "https://canary-host.example/CANARY-account-path"
identity = "CANARY-identity@example.com"

[auth]
kind = "auth_token"
value = "CANARY-account-token"

[headers]
"X-Access-Id" = "keychain:CANARY-header-ref"
`)
	seedFlatPlaybook(t, "work")
	mustStmt(t, "ALTER PLAYBOOK work USE ENV route-a SET VAR MY_SECRET=CANARY-manifest-value AS PLAINTEXT")
	// route-a routes away with a key of its own: work keeps its own login,
	// or a launch of it is refused (v4.3.0).
	mustStmt(t, "ALTER PLAYBOOK work SET login = 'isolated'")
	pb := filepath.Join(root, "work")
	writeFile(t, filepath.Join(pb, ".credentials.json"), `{"claudeAiOauth":{"accessToken":"CANARY-credentials"}}`)
	writeFile(t, filepath.Join(pb, "settings.json"), `{"env":{"SOME_VAR":"CANARY-settings-env"}}`)
	home, _ := os.UserHomeDir()
	writeFile(t, filepath.Join(home, ".claude", ".credentials.json"), `{"claudeAiOauth":{"accessToken":"CANARY-machine-login"}}`)
	return root
}

const askListPlan = `{"answer":"Your env sets.","steps":[{"args":["SHOW","ENVS"],"why":"Lists the env sets (v3 called them env profiles)."}],"notes":[]}`

// The mechanism behind "no value reaches the model": a canary in every value
// an install can hold, and in the shell environment as a route variable and
// as an unrelated one, and none of it in what claude receives.
func TestAskSendsNoValue(t *testing.T) {
	askFixture(t)
	rec := fakeAskClaude(t, askListPlan)
	t.Setenv("ANTHROPIC_BASE_URL", "http://127.0.0.1:9/CANARY-base-url")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "CANARY-shell-token")
	t.Setenv("ANTHROPIC_API_KEY", "CANARY-shell-key")
	t.Setenv("FOO_TOKEN", "CANARY-unrelated")
	t.Setenv("CLAUDE_CONFIG_DIR", "/CANARY-config-dir")
	// The default login reads no credential: not cpb's machine token either.
	tokenFile := filepath.Join(t.TempDir(), "oauth-token")
	writeFile(t, tokenFile, "CANARY-machine-token\n")
	t.Setenv("CPB_OAUTH_TOKEN_FILE", tokenFile)

	out, err := askRun(t, nil, "list the env profiles")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"argv", "env", "stdin"} {
		if got := readRec(t, rec, f); strings.Contains(got, "CANARY") {
			t.Errorf("claude received a canary in its %s:\n%s", f, got)
		}
	}
	if strings.Contains(readRec(t, rec, "env"), "CLAUDE_CONFIG_DIR=") {
		t.Error("the default login got a config directory: it is the machine's own")
	}
	if data, err := os.ReadFile(filepath.Join(rec, "version-env")); err == nil && strings.Contains(string(data), "CANARY") {
		t.Errorf("claude --version ran with the caller's environment:\n%s", data)
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

func TestAskCallsClaudeWithoutUserConfig(t *testing.T) {
	askFixture(t)
	rec := fakeAskClaude(t, askListPlan)
	if _, err := askRun(t, map[string]string{"model": "model-x"}, "list"); err != nil {
		t.Fatal(err)
	}
	argv := strings.Split(strings.TrimSuffix(readRec(t, rec, "argv"), "\x00"), "\x00")
	joined := strings.Join(argv, " ")
	for _, want := range []string{"-p", "--setting-sources project", "--strict-mcp-config", "--tools  --disable-slash-commands",
		"--no-session-persistence", "--output-format json", "--model model-x", "--json-schema {",
		"--allowedTools mcp__cpb__query", "--max-turns 8", `"args":["-i",`, `"--playbooks-dir",`, `"ask-tool"]`} {
		if !strings.Contains(joined, want) {
			t.Errorf("claude's arguments lack %q: %q", want, argv)
		}
	}
	pwd := strings.TrimSpace(readRec(t, rec, "pwd"))
	if underHome(pwd) || !strings.Contains(filepath.Base(pwd), "cpb-ask-") {
		t.Errorf("claude ran in %s, want a fresh directory outside home", pwd)
	}
	if _, err := os.Stat(pwd); !os.IsNotExist(err) {
		t.Errorf("the directory claude ran in is left behind: %s", pwd)
	}
}

func TestAskEnvIsAnAllowlist(t *testing.T) {
	in := []string{"PATH=/bin", "HOME=/home/user", "LANG=C", "LC_ALL=C", "TERM=xterm",
		"FOO_TOKEN=x", "AWS_SECRET_ACCESS_KEY=x", "ANTHROPIC_BEDROCK_BASE_URL=x", "ANTHROPIC_UNIX_SOCKET=x",
		"CLAUDE_CODE_API_KEY_HELPER_TTL_MS=1", "CLAUDE_CONFIG_DIR=/x", "CPB_CONFIG_DIR=/x", "ANTHROPIC_MODEL=m",
		"ANTHROPIC_AUTH_TOKEN=x", "ANTHROPIC_API_KEY=k", "CLAUDE_CODE_OAUTH_TOKEN=o",
		"HTTPS_PROXY=http://proxy.example.test:3128", "http_proxy=x", "ALL_PROXY=x", "NO_PROXY=x",
		"NODE_EXTRA_CA_CERTS=/x.pem", "SSL_CERT_FILE=/x.pem", "SSL_CERT_DIR=/x"}
	got := strings.Join(askEnv(in), "\n") + "\n"
	for _, keep := range []string{"PATH=/bin", "HOME=/home/user", "LANG=C", "LC_ALL=C", "TERM=xterm",
		"ANTHROPIC_API_KEY=k", "CLAUDE_CODE_OAUTH_TOKEN=o", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1"} {
		if !strings.Contains(got, keep+"\n") {
			t.Errorf("dropped %s:\n%s", keep, got)
		}
	}
	for _, drop := range []string{"FOO_TOKEN", "AWS_SECRET", "BEDROCK", "UNIX_SOCKET", "KEY_HELPER", "CLAUDE_CONFIG_DIR", "CPB_CONFIG_DIR", "ANTHROPIC_MODEL", "ANTHROPIC_AUTH_TOKEN",
		"PROXY", "proxy", "NODE_EXTRA_CA_CERTS", "SSL_CERT"} {
		if strings.Contains(got, drop) {
			t.Errorf("kept %s:\n%s", drop, got)
		}
	}
	// A key that came with another endpoint belongs to it.
	for _, route := range []string{"ANTHROPIC_BASE_URL=http://127.0.0.1:9/v1", "CLAUDE_CODE_USE_BEDROCK=1", "CLAUDE_CODE_USE_VERTEX=true", "CLAUDE_CODE_USE_FOUNDRY=1"} {
		got = strings.Join(askEnv(append(in, route)), "\n")
		if strings.Contains(got, "ANTHROPIC_API_KEY") || strings.Contains(got, "CLAUDE_CODE_OAUTH_TOKEN") || strings.Contains(got, "ANTHROPIC_BASE_URL") {
			t.Errorf("with %s the Anthropic credentials went along:\n%s", route, got)
		}
	}
	if got = strings.Join(askEnv(append(in, "CLAUDE_CODE_USE_FOUNDRY=0")), "\n"); !strings.Contains(got, "ANTHROPIC_API_KEY=k") || strings.Contains(got, "FOUNDRY") {
		t.Errorf("a Foundry switch that is off: want the key kept and the switch dropped:\n%s", got)
	}
	got = strings.Join(askEnv(append(in, "ANTHROPIC_BASE_URL=https://api.anthropic.com")), "\n")
	if !strings.Contains(got, "ANTHROPIC_API_KEY=k") || strings.Contains(got, "ANTHROPIC_BASE_URL") {
		t.Errorf("Anthropic's own endpoint: want the key kept and the variable dropped:\n%s", got)
	}
}

func TestAskNoLogin(t *testing.T) {
	askFixture(t)
	fakeAskClaude(t, `{"is_error":true,"result":"Invalid API key · Please run /login","subtype":"success"}`)
	_, err := askRun(t, nil, "list")
	if err == nil || !strings.Contains(err.Error(), "Invalid API key") || !strings.Contains(err.Error(), "/login") {
		t.Fatalf("got %v, want Claude Code's error and the login hint", err)
	}
	fakeAskClaude(t, `{"is_error":false,"result":"no plan here","subtype":"success"}`)
	if _, err := askRun(t, nil, "list"); err == nil || !strings.Contains(err.Error(), "not a plan") {
		t.Fatalf("an answer that is not a plan: %v", err)
	}
}

func TestAskUsage(t *testing.T) {
	askFixture(t)
	fakeAskClaude(t, askListPlan)
	for _, c := range []struct {
		flags map[string]string
		args  []string
	}{
		{nil, nil},
		{map[string]string{"json": "true"}, []string{"  "}},
	} {
		var err error
		captureStderr(t, func() { _, err = askRun(t, c.flags, c.args...) })
		if code, ok := exitCode(err); !ok || code != 2 {
			t.Errorf("%v %v: %v, want exit 2", c.flags, c.args, err)
		}
	}
}

func step(t *testing.T, root string, args ...string) askStep {
	t.Helper()
	s := askStep{Args: args}
	checkAskStep(&s, root)
	return s
}

func TestAskChecksSteps(t *testing.T) {
	root := askFixture(t)
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
		{[]string{"cat", "/home/user/.claude-playbooks/.env-sets/route-a.toml"}, stepRejected, "not a command cpb ask proposes"},
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

func TestAskGuards(t *testing.T) {
	root := askFixture(t)
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
		{[]string{"start", "--delete", "/tmp/cpb-ask-test-scratch"}, guardDestructive, "deletes /tmp/cpb-ask-test-scratch"},
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

func TestAskRouteAway(t *testing.T) {
	root := askFixture(t)
	// A new start directory shares the machine's login, so a route with a
	// key of its own is refused there: cpb ask says so instead of proposing it.
	if away := step(t, root, "start", "--env-set", "route-a", "--delete", "/tmp/cpb-ask-test-scratch"); away.Status != stepRejected || !strings.Contains(away.Reason, "shares the machine's Claude login") {
		t.Fatalf("a start on route-a: %+v, want rejected", away)
	}
	if run := step(t, root, "run", "work"); !run.RouteAway {
		t.Errorf("run work (on route-a): %+v, want route_away", run)
	}
	home := step(t, root, "start", "--env-set", "plain", "/tmp/cpb-ask-test-scratch")
	if home.RouteAway {
		t.Errorf("a start on plain: %+v, want Anthropic", home)
	}
	if !strings.Contains(strings.Join(home.CPBNotes, " "), "/login") {
		t.Errorf("a start: notes %q, want the shared-login note", home.CPBNotes)
	}
	// The caller's routing does not count: every launch removes it, and
	// the account decides (v4.2.0).
	t.Setenv("ANTHROPIC_BASE_URL", "http://127.0.0.1:9/v1")
	if s := step(t, root, "start", "/tmp/cpb-ask-test-scratch"); s.RouteAway {
		t.Errorf("an exported base URL: %+v, want Anthropic", s)
	}

	// Shown with the directory it runs from, and the files it would have
	// taken from the caller's directory.
	t.Setenv("ANTHROPIC_BASE_URL", "")
	homeDir, _ := os.UserHomeDir()
	writeFile(t, filepath.Join(homeDir, "CLAUDE.md"), "x")
	proj := filepath.Join(homeDir, "proj")
	writeFile(t, filepath.Join(proj, "CLAUDE.md"), "x")
	p := &askPlan{Steps: []askStep{step(t, root, "run", "work")}}
	var b strings.Builder
	printAskPlan(&b, p, proj)
	out := b.String()
	for _, want := range []string{"(cd /tmp && cpb run work)", "~/proj/CLAUDE.md", "~/CLAUDE.md"} {
		if !strings.Contains(out, want) {
			t.Errorf("shown plan lacks %q:\n%s", want, out)
		}
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
func TestAskReferenceMatchesSpec(t *testing.T) {
	spec, err := os.ReadFile(filepath.Join("..", "SPEC.md"))
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile("(?s)### Grammar\n\n```\n(.*?)\n```")
	want := block.FindSubmatch(spec)
	got := regexp.MustCompile("(?s)The grammar, from cpb's specification:\n\n```\n(.*?)\n```").FindStringSubmatch(askReference)
	if want == nil || got == nil {
		t.Fatal("grammar block not found in SPEC.md or the reference")
	}
	if got[1] != string(want[1]) {
		t.Fatal("the reference's grammar block differs from SPEC.md's: copy it again")
	}
	examples := regexp.MustCompile("(?s)Examples:\n\n```\n(.*?)\n```").FindStringSubmatch(askReference)
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
		s := askStep{Args: words[1:]}
		if err := checkAskStatement(&s, words[1:]); err != nil {
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

func TestAskJSONOutput(t *testing.T) {
	askFixture(t)
	fakeAskClaude(t, `{"answer":"a","steps":[{"args":["DROP","ENV","plain"],"why":"w"},{"args":["cat","x"],"why":"w"}],"notes":["n"]}`)
	out, err := askRun(t, map[string]string{"json": "true"}, "drop plain")
	if err != nil {
		t.Fatal(err)
	}
	var p askPlan
	if err := json.Unmarshal([]byte(out), &p); err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	if len(p.Steps) != 2 || p.Steps[0].Status != stepOK || len(p.Steps[0].Guards) != 1 || p.Steps[1].Status != stepRejected || p.Steps[0].Command != "cpb DROP ENV plain" {
		t.Fatalf("%+v", p)
	}
}

func TestQuotedWordsSuggestAsk(t *testing.T) {
	resetCommandTestState(t)
	aliasTestHome(t)
	err := runStatement([]string{"print every env profile"})
	if err == nil || !strings.Contains(err.Error(), "cpb ask 'print every env profile'") {
		t.Fatalf("%v", err)
	}
	if err := runStatement([]string{"SHOW ENVZ"}); err == nil || strings.Contains(err.Error(), "cpb ask") {
		t.Fatalf("a mistyped statement: %v, want no assistant hint", err)
	}
}

// CPB_ASK_LOGIN=token: cpb's machine token signs in, in the child's
// environment only; it replaces any Anthropic key or token exported there.
func TestAskTokenLogin(t *testing.T) {
	askFixture(t)
	rec := fakeAskClaude(t, askListPlan)
	tokenFile := filepath.Join(t.TempDir(), "oauth-token")
	writeFile(t, tokenFile, "CANARY-machine-token\n")
	t.Setenv("CPB_OAUTH_TOKEN_FILE", tokenFile)
	t.Setenv("CPB_ASK_LOGIN", "token")
	t.Setenv("ANTHROPIC_API_KEY", "other-key")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "other-token")
	if _, err := askRun(t, nil, "list"); err != nil {
		t.Fatal(err)
	}
	env := readRec(t, rec, "env")
	if strings.Count(env, "CANARY-machine-token") != 1 || !strings.Contains(env, "CLAUDE_CODE_OAUTH_TOKEN=CANARY-machine-token\n") {
		t.Errorf("the machine token is not the child's one CLAUDE_CODE_OAUTH_TOKEN:\n%s", env)
	}
	if strings.Contains(env, "other-key") || strings.Contains(env, "other-token") {
		t.Errorf("an exported key or token went along with the machine token:\n%s", env)
	}
	// A config directory of its own, fresh and outside home, gone after: no
	// stored login to fall back to.
	cfg := ""
	for _, kv := range strings.Split(env, "\n") {
		if v, ok := strings.CutPrefix(kv, "CLAUDE_CONFIG_DIR="); ok {
			cfg = v
		}
	}
	if cfg == "" || underHome(cfg) || !strings.Contains(cfg, "cpb-ask-") {
		t.Errorf("token mode's config directory: %q, want a fresh one outside home", cfg)
	} else if _, err := os.Stat(cfg); !os.IsNotExist(err) {
		t.Errorf("token mode's config directory is left behind: %s", cfg)
	}
	for _, f := range []string{"argv", "stdin"} {
		if strings.Contains(readRec(t, rec, f), "CANARY") {
			t.Errorf("the machine token reached claude's %s", f)
		}
	}

	// No machine token: refused, nothing sent.
	os.Remove(filepath.Join(rec, "argv"))
	t.Setenv("CPB_OAUTH_TOKEN_FILE", filepath.Join(t.TempDir(), "none"))
	if _, err := askRun(t, nil, "list"); err == nil || !strings.Contains(err.Error(), "no machine token") || !strings.Contains(err.Error(), "claude setup-token") {
		t.Errorf("no token file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rec, "argv")); err == nil {
		t.Error("claude ran without the machine token")
	}

	// An unknown mode is a usage error.
	t.Setenv("CPB_ASK_LOGIN", "keychain")
	var err error
	captureStderr(t, func() { _, err = askRun(t, nil, "list") })
	if exitOf(err) != 2 {
		t.Errorf("CPB_ASK_LOGIN=keychain: %v, want exit 2", err)
	}
}

// One gate, on the final environment of the claude child, whatever built
// it: an endpoint, a provider switch, a proxy or a CA change refuses, named
// by its variable and never its value.
func TestAskGate(t *testing.T) {
	base := []string{"PATH=/bin", "HOME=/home/user", "CLAUDE_CODE_OAUTH_TOKEN=t", "CLAUDE_CONFIG_DIR=/tmp/x"}
	if err := askGate(base); err != nil {
		t.Errorf("a clean environment: %v", err)
	}
	for _, kv := range []string{"ANTHROPIC_BASE_URL=https://api.anthropic.com", "ANTHROPIC_FOUNDRY_BASE_URL=v", "ANTHROPIC_BEDROCK_BASE_URL=v",
		"ANTHROPIC_UNIX_SOCKET=v", "CLAUDE_CODE_USE_BEDROCK=0", "CLAUDE_CODE_USE_VERTEX=1", "CLAUDE_CODE_USE_FOUNDRY=1",
		"HTTPS_PROXY=v", "http_proxy=v", "ALL_PROXY=v", "no_proxy=v", "NODE_EXTRA_CA_CERTS=v", "SSL_CERT_FILE=v", "NODE_TLS_REJECT_UNAUTHORIZED=0"} {
		k, _, _ := strings.Cut(kv, "=")
		err := askGate(append(base, kv))
		if err == nil || !strings.Contains(err.Error(), k) || !strings.Contains(err.Error(), "nothing was sent") {
			t.Errorf("%s: %v, want a refusal naming it", k, err)
		}
	}
}

// Behind a proxy cpb ask refuses before anything is sent: started without
// it, Claude Code would only retry. NO_PROXY alone is no proxy.
func TestAskProxyRefused(t *testing.T) {
	askFixture(t)
	rec := fakeAskClaude(t, askListPlan)
	t.Setenv("HTTPS_PROXY", "http://proxy.example.test:3128")
	_, err := askRun(t, nil, "list")
	if err == nil || !strings.Contains(err.Error(), "does not run through a proxy") || !strings.Contains(err.Error(), "env -u HTTPS_PROXY") || strings.Contains(err.Error(), "proxy.example.test") {
		t.Fatalf("behind a proxy: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rec, "argv")); err == nil {
		t.Fatal("claude ran behind a proxy")
	}
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("NO_PROXY", "localhost")
	if _, err := askRun(t, nil, "list"); err != nil {
		t.Fatalf("NO_PROXY alone: %v", err)
	}
}

// A claude that does not answer is given up on, with a message.
func TestAskTimeout(t *testing.T) {
	askFixture(t)
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "claude"), "#!/bin/sh\n[ \"$1\" = --version ] && exit 0\nexec sleep 5\n")
	os.Chmod(filepath.Join(dir, "claude"), 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	old := askTimeout
	askTimeout = 200 * time.Millisecond
	t.Cleanup(func() { askTimeout = old })
	if _, err := askRun(t, nil, "list"); err == nil || !strings.Contains(err.Error(), "did not answer within") {
		t.Fatalf("a silent claude: %v", err)
	}
}

// childEnv is the recorded child environment as a map.
func childEnv(t *testing.T, rec string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, kv := range strings.Split(readRec(t, rec, "env"), "\n") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			out[k] = v
		}
	}
	return out
}

// cpb ask <playbook>: it signs in as a launch of that playbook would, "you"
// is it, and one routed away from Anthropic is refused before claude runs.
func TestAskAddressedPlaybook(t *testing.T) {
	root := askFixture(t)
	rec := fakeAskClaude(t, askListPlan)

	// work routes away (env set route-a): refused, nothing sent.
	_, err := askRun(t, nil, "work", "how many of your sessions are live?")
	if err == nil || !strings.Contains(err.Error(), "sends its requests away from Anthropic") {
		t.Fatalf("a routed playbook: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rec, "argv")); err == nil {
		t.Fatal("claude ran for a routed playbook")
	}

	// A shared-login playbook, addressed by its launcher: the machine's
	// login, no config directory, and "addressed" in the state.
	mustStmt(t, "CREATE PLAYBOOK home SET launcher = 'hm'")
	if _, err := askRun(t, nil, "hm", "how many of your sessions are live?"); err != nil {
		t.Fatal(err)
	}
	stdin := readRec(t, rec, "stdin")
	if !strings.Contains(stdin, `"addressed":"home"`) || !strings.Contains(stdin, "Request: how many of your sessions are live?") {
		t.Errorf("the state or the request:\n%s", stdin)
	}
	if _, ok := childEnv(t, rec)["CLAUDE_CONFIG_DIR"]; ok {
		t.Error("a shared-login playbook got a config directory")
	}

	// The machine token: a launch of home would inject it, so its question
	// does too, with a fresh config directory.
	tokenFile := filepath.Join(t.TempDir(), "oauth-token")
	writeFile(t, tokenFile, "machine-token-1\n")
	t.Setenv("CPB_OAUTH_TOKEN_FILE", tokenFile)
	if _, err := askRun(t, nil, "home", "list"); err != nil {
		t.Fatal(err)
	}
	env := childEnv(t, rec)
	if env["CLAUDE_CODE_OAUTH_TOKEN"] != "machine-token-1" || !strings.Contains(env["CLAUDE_CONFIG_DIR"], "cpb-ask-") {
		t.Errorf("a token playbook: token %q, config dir %q", env["CLAUDE_CODE_OAUTH_TOKEN"], env["CLAUDE_CONFIG_DIR"])
	}
	t.Setenv("CPB_OAUTH_TOKEN_FILE", filepath.Join(t.TempDir(), "none"))

	// An isolated playbook with a login of its own: its config directory,
	// and no other credential beside it.
	mustStmt(t, "CREATE PLAYBOOK iso SET launcher = '', login = 'isolated'")
	iso := filepath.Join(root, "iso")
	os.Remove(filepath.Join(iso, ".credentials.json"))
	writeFile(t, filepath.Join(iso, ".credentials.json"), `{"claudeAiOauth":{"accessToken":"a","refreshToken":"r","expiresAt":9999999999999}}`)
	t.Setenv("ANTHROPIC_API_KEY", "other-key")
	if _, err := askRun(t, nil, "iso", "list"); err != nil {
		t.Fatal(err)
	}
	env = childEnv(t, rec)
	if env["CLAUDE_CONFIG_DIR"] != iso || env["ANTHROPIC_API_KEY"] != "" || env["CLAUDE_CODE_OAUTH_TOKEN"] != "" {
		t.Errorf("an isolated playbook: config dir %q, key %q, token %q", env["CLAUDE_CONFIG_DIR"], env["ANTHROPIC_API_KEY"], env["CLAUDE_CODE_OAUTH_TOKEN"])
	}
	t.Setenv("ANTHROPIC_API_KEY", "")

	// Isolated with no login of its own: refused, no fallback.
	mustStmt(t, "CREATE PLAYBOOK iso2 SET launcher = '', login = 'isolated'")
	os.Remove(filepath.Join(root, "iso2", ".credentials.json"))
	os.Remove(filepath.Join(rec, "argv"))
	if _, err := askRun(t, nil, "iso2", "list"); err == nil || !strings.Contains(err.Error(), "no login of its own") {
		t.Errorf("an isolated playbook without a login: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rec, "argv")); err == nil {
		t.Error("claude ran without the playbook's login")
	}

	// A first word that is no playbook is part of the request; a playbook
	// name alone is the whole request.
	if _, err := askRun(t, nil, "list", "my", "sets"); err != nil {
		t.Fatal(err)
	}
	if stdin := readRec(t, rec, "stdin"); strings.Contains(stdin, "addressed") || !strings.Contains(stdin, "Request: list my sets") {
		t.Errorf("no playbook named:\n%s", stdin)
	}
	if _, err := askRun(t, nil, "home"); err != nil {
		t.Fatal(err)
	}
	if stdin := readRec(t, rec, "stdin"); strings.Contains(stdin, "addressed") || !strings.Contains(stdin, "Request: home") {
		t.Errorf("one word:\n%s", stdin)
	}
}

// The tool server starts with a clean environment: Claude Code hands an MCP
// server its whole environment, the token included, so cpb starts it
// through env -i. The fake claude starts the configured command as Claude
// Code does, with its own environment; the server's stand-in records the
// environment it began with.
func TestAskToolServerCleanEnv(t *testing.T) {
	askFixture(t)
	dir := t.TempDir()
	rec := filepath.Join(dir, "tool-env")
	recorder := filepath.Join(dir, "cpb-recorder")
	writeFile(t, recorder, "#!/bin/sh\nenv > "+rec+"\n")
	os.Chmod(recorder, 0o755)
	old := askSelf
	askSelf = func() (string, error) { return recorder, nil }
	t.Cleanup(func() { askSelf = old })
	answer := `{"type":"result","subtype":"success","is_error":false,"result":"","structured_output":` + askListPlan + `}`
	writeFile(t, filepath.Join(dir, "answer"), answer)
	script := `#!/bin/sh
[ "$1" = "--version" ] && exit 0
cat > /dev/null
python3 -c 'import json,sys,subprocess,os
a=sys.argv[1:]; c=json.loads(a[a.index("--mcp-config")+1])["mcpServers"]["cpb"]
subprocess.run([c["command"]]+c["args"], input=b"", env=os.environ)' "$@"
cat "` + filepath.Join(dir, "answer") + `"
`
	writeFile(t, filepath.Join(dir, "claude"), script)
	os.Chmod(filepath.Join(dir, "claude"), 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	tokenFile := filepath.Join(t.TempDir(), "oauth-token")
	writeFile(t, tokenFile, "CANARY-tool-token\n")
	t.Setenv("CPB_OAUTH_TOKEN_FILE", tokenFile)
	t.Setenv("CPB_ASK_LOGIN", "token")
	t.Setenv("CPB_ISOLATED_LOGIN", "false")
	if _, err := askRun(t, nil, "list"); err != nil {
		t.Fatal(err)
	}
	env := readRec(t, filepath.Dir(rec), "tool-env")
	// The tool judges no login, so the settings that change how one is
	// judged stay behind too.
	for _, k := range []string{"CPB_OAUTH_TOKEN_FILE=", "CPB_ISOLATED_LOGIN="} {
		if strings.Contains(env, k) {
			t.Errorf("the tool server began with %s", k)
		}
	}
	if strings.Contains(env, "CANARY") {
		t.Errorf("the tool server began with the token:\n%s", env)
	}
	keep := map[string]bool{}
	for _, k := range askToolEnvKeep {
		keep[k] = true
	}
	for _, kv := range strings.Split(strings.TrimSpace(env), "\n") {
		k, _, _ := strings.Cut(kv, "=")
		if !keep[k] && k != "PWD" && k != "SHLVL" && k != "_" && k != "OLDPWD" {
			t.Errorf("the tool server began with %s", k)
		}
	}
	if !strings.Contains(env, "HOME=") || !strings.Contains(env, "PATH=") {
		t.Errorf("the tool server lacks HOME or PATH:\n%s", env)
	}
}

// The neutral directory is outside home and outside every instruction tree:
// a base with CLAUDE.md, CLAUDE.local.md or .claude/ above it is passed
// over, and with no good base the call is refused.
func TestAskNeutralDirOutsideInstructionTrees(t *testing.T) {
	askFixture(t)
	if tree := askInstructionTree(t.TempDir()); tree != "" {
		t.Skipf("this machine's temp directory is inside an instruction tree (%s): the test needs one outside", tree)
	}
	withMD := t.TempDir()
	writeFile(t, filepath.Join(withMD, "CLAUDE.md"), "x")
	badMD := filepath.Join(withMD, "tmp")
	withLocal := t.TempDir()
	writeFile(t, filepath.Join(withLocal, "CLAUDE.local.md"), "x")
	withDir := t.TempDir()
	os.MkdirAll(filepath.Join(withDir, ".claude"), 0o755)
	badDir := filepath.Join(withDir, "a", "b")
	good := t.TempDir()
	for _, d := range []string{badMD, badDir} {
		os.MkdirAll(d, 0o755)
	}
	old := askNeutralBases
	t.Cleanup(func() { askNeutralBases = old })
	askNeutralBases = func() []string { return []string{badMD, withLocal, badDir, good} }
	dir, err := askNeutralDir()
	if err != nil || filepath.Dir(dir) != good {
		t.Fatalf("got %q %v, want a directory in %s", dir, err, good)
	}
	os.RemoveAll(dir)
	askNeutralBases = func() []string { return []string{badMD, badDir} }
	if _, err := askNeutralDir(); err == nil || !strings.Contains(err.Error(), "CLAUDE.md tree") {
		t.Fatalf("no good base: %v", err)
	}
}

// A request that holds a credential is refused before anything is sent,
// naming the key or the kind and never the value. Synthetic values only.
func TestAskRequestCredential(t *testing.T) {
	for req, want := range map[string]string{
		"create env set x with ANTHROPIC_AUTH_TOKEN=FAKE0token0value0for0tests": "ANTHROPIC_AUTH_TOKEN=...",
		"set MY_API_KEY: abcdefghijklmnop":                                      "MY_API_KEY=...",
		"use sk-ant-api03-FAKEFAKEFAKEFAKE0000 for router":                      "an Anthropic key",
		"my key is sk-proj-FAKE0000FAKE0000FAKE0000":                            "an API key",
		"token ghp_FAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKE1234":                        "a GitHub token",
		"aws AKIAFAKEFAKEFAKEFAKE":                                              "an AWS access key",
		"jwt eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJmYWtlIn0.ZmFrZXNpZ25hdHVyZQ":       "a JSON web token",
		"proxy https://bob:FAKEpass@proxy.example.test/":                        "a URL with a password",
		"the key 0123456789abcdef0123456789abcdef0123":                          "a long key-shaped word",
	} {
		if got := askRequestCredential(req); !strings.Contains(got, want) {
			t.Errorf("%q: %q, want %q", req, got, want)
		}
	}
	for _, req := range []string{
		"list my env sets", "SET VAR MAX_THINKING_TOKENS=8000", "ANTHROPIC_AUTH_TOKEN FROM keychain:router-token",
		"set ANTHROPIC_AUTH_TOKEN=<token> on router", "ANTHROPIC_AUTH_TOKEN=... on router", "ANTHROPIC_BASE_URL=http://127.0.0.1:9/v1",
		"start in /home/user/projects/my-long-project-name-for-the-team", "resume 11111111-1111-4111-8111-111111111111",
		"play x --sha256 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"update to commit 0123456789abcdef0123456789abcdef01234567", "how many of your sessions are live?",
	} {
		if got := askRequestCredential(req); got != "" {
			t.Errorf("%q: refused as %q", req, got)
		}
	}
	// End to end: refused, the value never echoed, claude never runs.
	askFixture(t)
	rec := fakeAskClaude(t, askListPlan)
	_, err := askRun(t, nil, "create env set x with ANTHROPIC_AUTH_TOKEN=FAKE0token0value0for0tests")
	if err == nil || !strings.Contains(err.Error(), "ANTHROPIC_AUTH_TOKEN=...") || strings.Contains(err.Error(), "FAKE0token") {
		t.Fatalf("%v", err)
	}
	if _, err := os.Stat(filepath.Join(rec, "argv")); err == nil {
		t.Fatal("claude ran with a credential in the request")
	}
}

// Launch steps are judged as the launch resolves them.
func TestAskLaunchRoutePrecedence(t *testing.T) {
	root := askFixture(t)
	// A step that names its own playbooks root is not proposed: the launch
	// would take it out before reading --delete.
	if s := step(t, root, "start", "/tmp/cpb-ask-test-x", "--playbooks-dir="+root, "--delete"); s.Status != stepRejected {
		t.Errorf("--playbooks-dir in a step: %+v", s)
	}
	// The account decides: the playbook's first, or --account.
	putAccount(t, account.Account{Name: "anth", Provider: account.Anthropic, Auth: account.Auth{Kind: account.ClaudeLogin}})
	putAccount(t, account.Account{Name: "glm", Provider: account.Gateway, BaseURL: "http://127.0.0.1:9/v1", Auth: account.Auth{Kind: account.None}})
	mustStmt(t, "CREATE PLAYBOOK direct SET launcher = '', login = 'isolated' USE ACCOUNT anth, glm")
	if s := step(t, root, "run", "direct"); s.RouteAway {
		t.Errorf("direct on its first account: %+v", s)
	}
	if s := step(t, root, "run", "--account", "glm", "direct"); !s.RouteAway {
		t.Errorf("--account glm: %+v", s)
	}
	// A launch cpb would refuse is rejected, with cpb's reason: a layer
	// route beside an account the playbook names, a start directory's own
	// route, and run under a CPB_CONFIG_DIR that holds one.
	refused := func(what string, s askStep) {
		t.Helper()
		if s.Status != stepRejected || !strings.Contains(s.Reason, "cpb would refuse this launch") {
			t.Errorf("%s: %+v", what, s)
		}
	}
	refused("--env of a base URL beside an account", step(t, root, "run", "--env", "ANTHROPIC_BASE_URL=https://api.anthropic.com", "direct"))
	refused("--env-set route-a beside an account", step(t, root, "run", "--env-set", "route-a", "direct"))
	sdir := filepath.Join(t.TempDir(), "routed")
	writeFile(t, filepath.Join(sdir, ".playbook"), "[env.set]\nANTHROPIC_BASE_URL = \"http://127.0.0.1:9/v1\"\n")
	refused("start on a routed directory", step(t, root, "start", sdir))
	t.Setenv("CPB_CONFIG_DIR", sdir)
	refused("run under CPB_CONFIG_DIR", step(t, root, "run", "direct"))
	t.Setenv("CPB_CONFIG_DIR", "")
}

// A launch on an account its playbook cannot use is rejected with cpb's
// reason (the case cpb ask once proposed: a shared playbook on an account
// with a key of its own), and the context names accounts by class only.
func TestAskRejectsARefusedLaunch(t *testing.T) {
	root := askFixture(t)
	putAccount(t, account.Account{Name: "router", Provider: account.Gateway, BaseURL: "http://127.0.0.1:9/v1", Auth: account.Auth{Kind: account.None}})
	mustStmt(t, "CREATE PLAYBOOK sharedpb SET launcher = ''")
	s := step(t, root, "run", "--account", "router", "sharedpb")
	if s.Status != stepRejected || !strings.Contains(s.Reason, "shares the machine's") {
		t.Fatalf("a shared playbook on a keyed account: %+v", s)
	}
	mustStmt(t, "CREATE PLAYBOOK isopb SET launcher = '', login = 'isolated'")
	if s := step(t, root, "run", "--account", "router", "isopb"); s.Status != stepOK || !s.RouteAway {
		t.Fatalf("an isolated playbook on it: %+v", s)
	}
	ctx, err := buildAskContext(root)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, a := range ctx.Accounts {
		if a.Name == "router" && a.Provider == "gateway" && a.Route == "away" && !a.ClaudeLogin {
			found = true
		}
	}
	if !found {
		t.Fatalf("accounts in the context: %+v", ctx.Accounts)
	}
	for _, p := range ctx.Playbooks {
		if p.Name == "isopb" && (p.Login != "isolated" || p.Account != "machine") {
			t.Fatalf("isopb in the context: %+v", p)
		}
	}
}

// A launch after a step that changes the registry is shown as away.
func TestAskPlanChangesRoute(t *testing.T) {
	root := askFixture(t)
	mustStmt(t, "CREATE PLAYBOOK plainpb SET launcher = ''")
	p := &askPlan{Steps: []askStep{{Args: []string{"run", "plainpb"}}, {Args: []string{"ALTER", "PLAYBOOK", "plainpb", "ADD", "ENV", "route-a"}}, {Args: []string{"run", "plainpb"}}}}
	checkAskPlan(p, root)
	if p.Steps[0].RouteAway || !p.Steps[2].RouteAway || !strings.Contains(strings.Join(p.Steps[2].CPBNotes, " "), "A step before this one") {
		t.Errorf("before %+v, after %+v", p.Steps[0], p.Steps[2])
	}
}

// cpb ask <playbook> signs in as a launch of it would, with a Claude login
// only: a playbook on an account with a key of its own is refused, and so is
// one on an account that is not Anthropic's.
func TestAskTargetAccountRefused(t *testing.T) {
	askFixture(t)
	putAccount(t, account.Account{Name: "console", Provider: account.Anthropic, Auth: account.Auth{Kind: account.APIKey, Value: "sk-ant-x"}})
	putAccount(t, account.Account{Name: "glm", Provider: account.Gateway, BaseURL: "http://127.0.0.1:9/v1", Auth: account.Auth{Kind: account.None}})
	mustStmt(t, "CREATE PLAYBOOK keyed SET launcher = '', login = 'isolated' USE ACCOUNT console")
	mustStmt(t, "CREATE PLAYBOOK routed SET launcher = '', login = 'isolated' USE ACCOUNT glm")
	rec := fakeAskClaude(t, askListPlan)
	for pb, want := range map[string]string{"keyed": "signs in with its api_key", "routed": "away from Anthropic"} {
		_, err := askRun(t, nil, pb, "list my env sets")
		if err == nil || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "sk-ant-x") {
			t.Fatalf("%s: %v", pb, err)
		}
	}
	if _, err := os.Stat(filepath.Join(rec, "argv")); err == nil {
		t.Fatal("claude ran for a refused target")
	}
}

// Arguments passed through to claude count as the launch counts them: a
// route in inline settings is refused, and a settings file is not read but
// judged as away.
func TestAskChecksClaudeArguments(t *testing.T) {
	root := askFixture(t)
	mustStmt(t, "CREATE PLAYBOOK argpb SET launcher = '', login = 'isolated'")
	if s := step(t, root, "run", "argpb"); s.Status != stepOK || s.RouteAway {
		t.Fatalf("argpb with no arguments: %+v", s)
	}
	inline := step(t, root, "run", "argpb", "--settings", `{"env":{"ANTHROPIC_BASE_URL":"https://router.example"}}`)
	if inline.Status != stepRejected || !strings.Contains(inline.Reason, "cpb would refuse") {
		t.Fatalf("a route in inline settings: %+v, want rejected", inline)
	}
	file := step(t, root, "run", "argpb", "--settings=/nonexistent/cpb-ask-settings.json")
	if file.Status != stepOK || !file.RouteAway {
		t.Fatalf("a settings file: %+v, want shown as away", file)
	}
	var guarded bool
	for _, g := range file.Guards {
		guarded = guarded || strings.Contains(g.What, "reads settings from /nonexistent/cpb-ask-settings.json")
	}
	if !guarded {
		t.Fatalf("a settings file: guards %+v", file.Guards)
	}
}
