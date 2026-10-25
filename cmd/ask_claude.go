package cmd

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ramazanpolat/claude-playbooks/internal/auth"
)

// How cpb ask asks (SPEC.md, "cpb ask"): Claude Code's own `claude -p`,
// on the machine's login, always on Anthropic's API. The guarantees are
// built into the child process, not asked of the model:
//
//   - its environment is an allowlist (askEnv), so no route variable,
//     today's or a future one, reaches it, and no env set, DEFAULTS or
//     playbook layer applies;
//   - user settings and user memory stay out (--setting-sources project), and
//     it runs from a fresh directory outside home, so no CLAUDE.md is an
//     ancestor of it;
//   - it gets no tools, no MCP server and no slash command, so the model can
//     read nothing beyond what cpb hands it.

//go:embed ask_reference.md
var askReference string

const defaultAskModel = "sonnet"

// askPlanSchema is the plan the model must return (--json-schema).
const askPlanSchema = `{"type":"object","additionalProperties":false,` +
	`"properties":{"answer":{"type":"string"},` +
	`"steps":{"type":"array","items":{"type":"object","additionalProperties":false,` +
	`"properties":{"args":{"type":"array","items":{"type":"string"}},"why":{"type":"string"}},` +
	`"required":["args","why"]}},` +
	`"notes":{"type":"array","items":{"type":"string"}}},` +
	`"required":["answer","steps","notes"]}`

// askMaxTurns bounds the model's turns: its tool calls and the answer.
const askMaxTurns = "8"

// askClaudeArgs are the flags of the one claude -p call. Its one MCP server
// is this cpb's read-only tool (ask-tool), granted alone; Claude Code's own
// tools stay off.
func askClaudeArgs(model, self, root string) []string {
	mcp, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"cpb": map[string]any{
		"type": "stdio", "command": self, "args": []string{"--playbooks-dir", root, "ask-tool"},
	}}})
	return []string{"-p",
		"--setting-sources", "project",
		"--strict-mcp-config",
		"--mcp-config", string(mcp),
		"--allowedTools", "mcp__cpb__" + askToolName,
		"--tools", "",
		"--disable-slash-commands",
		"--no-session-persistence",
		"--max-turns", askMaxTurns,
		"--system-prompt", askReference,
		"--json-schema", askPlanSchema,
		"--output-format", "json",
		"--model", model,
	}
}

// askSelf is this cpb binary, the tool's command; a variable for tests.
var askSelf = func() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return p, nil
}

// askEnvKeep is what the child keeps of the environment: what Claude Code
// needs to run and to reach its login (the Linux keyring session included).
// Everything else is dropped, a proxy and an added CA among them: together
// they could read the question and the credential (askGate).
var askEnvKeep = map[string]bool{
	"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "SHELL": true,
	"TERM": true, "LANG": true, "TMPDIR": true,
	"XDG_RUNTIME_DIR": true, "DBUS_SESSION_BUS_ADDRESS": true,
}

// askEnvLogin are Anthropic credentials the child keeps only when the
// environment routes to Anthropic: a key that came with another endpoint
// belongs to that endpoint.
var askEnvLogin = map[string]bool{"ANTHROPIC_API_KEY": true, auth.OAuthTokenEnv: true}

// askRoutesToAnthropic reports whether a claude started with environ
// sends its requests to Anthropic, by EXPLAIN's egress rule.
func askRoutesToAnthropic(environ []string) bool {
	return routeEgress(routeInherited(nil, environ)) == "anthropic"
}

// askEnv builds the child's environment from environ by allowlist.
func askEnv(environ []string) []string {
	anthropic := askRoutesToAnthropic(environ)
	out := []string{"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1"}
	for _, kv := range environ {
		k, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if askEnvKeep[k] || strings.HasPrefix(k, "LC_") || (anthropic && askEnvLogin[k]) {
			out = append(out, kv)
		}
	}
	return out
}

// askNeutralDir makes a fresh directory outside home: the system temp
// dir, or /tmp when that one is under home. Claude Code loads the CLAUDE.md
// files of a session's working directory and its ancestors, home's among
// them when it starts below home.
func askNeutralDir() (string, error) {
	for _, base := range []string{os.TempDir(), "/tmp"} {
		if underHome(base) {
			continue
		}
		dir, err := os.MkdirTemp(base, "cpb-ask-")
		if err != nil {
			return "", err
		}
		return dir, nil
	}
	return "", errors.New("no temporary directory outside your home directory: set TMPDIR to one")
}

// underHome reports whether path is home or below it, symlinks resolved.
func underHome(path string) bool {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	resolve := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return filepath.Clean(p)
	}
	rel, err := filepath.Rel(resolve(home), resolve(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// How cpb ask signs in, CPB_ASK_LOGIN: "claude", the default, is Claude
// Code's own login on this machine, and cpb reads no credential; "token" is
// cpb's machine token, the long-lived token every launch injects
// (auth.OAuthTokenFile), put into the child's environment and nowhere else.
const (
	askLoginEnv    = "CPB_ASK_LOGIN"
	askLoginClaude = "claude"
	askLoginToken  = "token"
	// askLoginPlaybook signs in with a playbook's own stored login: its
	// config directory is Claude Code's (cpb ask <playbook>, isolated).
	askLoginPlaybook = "playbook"
)

// askSignIn is how one call signs in: a mode, and for askLoginPlaybook the
// playbook's config directory.
type askSignIn struct {
	mode      string
	configDir string
}

// askLogin reads CPB_ASK_LOGIN; anything else is a usage error.
func askLogin() (string, error) {
	switch v := os.Getenv(askLoginEnv); v {
	case "", askLoginClaude:
		return askLoginClaude, nil
	case askLoginToken:
		return askLoginToken, nil
	default:
		return "", fmt.Errorf("%s=%q: use %s (the default) or %s", askLoginEnv, v, askLoginClaude, askLoginToken)
	}
}

// askLoginEnvFor adds what the login mode needs to the child's
// environment. In token mode the machine token replaces any Anthropic key
// or token the environment carried, so it is the one that signs in.
//
// The token mode also gives Claude Code a config directory of its own,
// configDir, empty and fresh: with no stored login in it, a token that is
// refused cannot fall back to the machine's login. Neither mode falls back
// to the other.
func askLoginEnvFor(mode string, env []string, configDir string) ([]string, error) {
	if mode == askLoginPlaybook {
		// The playbook's own login, and no other credential beside it.
		out := make([]string, 0, len(env)+1)
		for _, kv := range env {
			if k, _, _ := strings.Cut(kv, "="); !askEnvLogin[k] {
				out = append(out, kv)
			}
		}
		return append(out, "CLAUDE_CONFIG_DIR="+configDir), nil
	}
	if mode != askLoginToken {
		return env, nil
	}
	f := auth.OAuthTokenFile()
	data, err := os.ReadFile(f)
	tok := strings.TrimSpace(string(data))
	if f == "" || err != nil || tok == "" {
		return nil, fmt.Errorf("%s=%s, but there is no machine token at %s: create it with claude setup-token (see docs/guides/authentication.md), or unset %s", askLoginEnv, askLoginToken, tildePath(f), askLoginEnv)
	}
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); !askEnvLogin[k] {
			out = append(out, kv)
		}
	}
	return append(out, "CLAUDE_CONFIG_DIR="+configDir, auth.OAuthTokenEnv+"="+tok), nil
}

// askGate is the one Anthropic-only gate, on the final environment of the
// claude child, whatever login mode built it: an endpoint, a provider
// switch, a proxy or a trust-store change in it refuses the call, named by
// its variable, never its value. The allowlist already drops all of them;
// the gate is what holds if that ever changes.
func askGate(env []string) error {
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		u := strings.ToUpper(k)
		switch {
		case strings.HasPrefix(u, "ANTHROPIC_") && strings.HasSuffix(u, "_BASE_URL"),
			u == "ANTHROPIC_UNIX_SOCKET",
			u == "CLAUDE_CODE_USE_BEDROCK", u == "CLAUDE_CODE_USE_VERTEX", u == "CLAUDE_CODE_USE_FOUNDRY",
			u == "HTTP_PROXY", u == "HTTPS_PROXY", u == "ALL_PROXY", u == "NO_PROXY",
			u == "NODE_EXTRA_CA_CERTS", u == "SSL_CERT_FILE", u == "SSL_CERT_DIR", u == "NODE_TLS_REJECT_UNAUTHORIZED":
			return fmt.Errorf("cpb ask asks Anthropic only, directly: %s would be in Claude Code's environment, so nothing was sent", k)
		}
	}
	return nil
}

// askProxy names the proxy variable the caller's environment sets, ""
// when none. cpb ask does not run through a proxy: Claude Code started
// without it would only retry until it gave up, so the call is refused
// before anything is sent.
func askProxy(environ []string) string {
	for _, kv := range environ {
		k, v, _ := strings.Cut(kv, "=")
		switch strings.ToUpper(k) {
		case "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY":
			if strings.TrimSpace(v) != "" {
				return k
			}
		}
	}
	return ""
}

// askTimeout bounds the one call: Claude Code retries an unreachable API
// for minutes.
var askTimeout = 3 * time.Minute

// askLoginHint ends every failure of the call.
const askLoginHint = "cpb ask asks Claude through your Claude Code login: run claude and /login, or set ANTHROPIC_API_KEY"

// askAsk sends the installed state and the request, and returns the plan.
func askViaClaude(model string, signIn askSignIn, root string, state *askContext, request string) (*askPlan, error) {
	if p := askProxy(os.Environ()); p != "" {
		return nil, fmt.Errorf("cpb ask does not run through a proxy yet, and %s is set here: nothing was sent. If Anthropic's API is reachable without it, run: env -u %s cpb ask ...", p, p)
	}
	path, err := exec.LookPath("claude")
	if err != nil {
		return nil, errors.New("'claude' command not found. Install Claude Code first: https://claude.ai/download")
	}
	dir, err := askNeutralDir()
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	configDir := signIn.configDir
	if signIn.mode == askLoginToken {
		if configDir, err = askNeutralDir(); err != nil {
			return nil, err
		}
		defer os.RemoveAll(configDir)
	}
	env, err := askLoginEnvFor(signIn.mode, askEnv(os.Environ()), configDir)
	if err != nil {
		return nil, err
	}
	if err := askGate(env); err != nil {
		return nil, err
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), askTimeout)
	defer cancel()
	self, err := askSelf()
	if err != nil {
		return nil, err
	}
	c := exec.CommandContext(ctx, path, askClaudeArgs(model, self, root)...)
	c.Dir = dir
	c.Env = env
	c.Stdin = strings.NewReader("Installed state: " + string(stateJSON) + "\n\nRequest: " + request + "\n")
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	runErr := c.Run()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("Claude Code did not answer within %s: nothing came back. %s", askTimeout, askLoginHint)
	}
	return parseAskAnswer(stdout.Bytes(), stderr.String(), runErr)
}

// parseAskAnswer reads claude -p --output-format json's result object.
func parseAskAnswer(stdout []byte, stderr string, runErr error) (*askPlan, error) {
	var res struct {
		IsError          bool            `json:"is_error"`
		Result           string          `json:"result"`
		StructuredOutput json.RawMessage `json:"structured_output"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(stdout), &res); err != nil {
		why := firstLine(stderr)
		if why == "" {
			why = firstLine(string(stdout))
		}
		if why == "" && runErr != nil {
			why = runErr.Error()
		}
		return nil, fmt.Errorf("Claude Code did not answer (%s). %s", why, askLoginHint)
	}
	if res.IsError || runErr != nil {
		why := firstLine(res.Result)
		if why == "" {
			why = firstLine(stderr)
		}
		return nil, fmt.Errorf("Claude Code answered with an error: %s. %s", why, askLoginHint)
	}
	raw := res.StructuredOutput
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage(res.Result)
	}
	var p askPlan
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, errors.New("the answer was not a plan; ask again, perhaps in other words")
	}
	return &p, nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
