package cmd

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/auth"
	"github.com/ramazanpolat/claude-playbooks/internal/envset"
)

// How cpb agent asks (SPEC.md, "cpb agent"): Claude Code's own `claude -p`,
// on the machine's login, always on Anthropic's API. The guarantees are
// built into the child process, not asked of the model:
//
//   - its environment is an allowlist (agentEnv), so no route variable,
//     today's or a future one, reaches it, and no env set, DEFAULTS or
//     playbook layer applies;
//   - user settings and user memory stay out (--setting-sources project), and
//     it runs from a fresh directory outside home, so no CLAUDE.md is an
//     ancestor of it;
//   - it gets no tools, no MCP server and no slash command, so the model can
//     read nothing beyond what cpb hands it.

//go:embed agent_reference.md
var agentReference string

const defaultAgentModel = "sonnet"

// agentPlanSchema is the plan the model must return (--json-schema).
const agentPlanSchema = `{"type":"object","additionalProperties":false,` +
	`"properties":{"answer":{"type":"string"},` +
	`"steps":{"type":"array","items":{"type":"object","additionalProperties":false,` +
	`"properties":{"args":{"type":"array","items":{"type":"string"}},"why":{"type":"string"}},` +
	`"required":["args","why"]}},` +
	`"notes":{"type":"array","items":{"type":"string"}}},` +
	`"required":["answer","steps","notes"]}`

// agentClaudeArgs are the flags of the one claude -p call.
func agentClaudeArgs(model string) []string {
	return []string{"-p",
		"--setting-sources", "project",
		"--strict-mcp-config",
		"--tools", "",
		"--disable-slash-commands",
		"--no-session-persistence",
		"--system-prompt", agentReference,
		"--json-schema", agentPlanSchema,
		"--output-format", "json",
		"--model", model,
	}
}

// agentEnvKeep is what the child keeps of the environment: what Claude Code
// needs to run and to reach its login (the Linux keyring session included),
// and the network's own settings. Everything else is dropped.
var agentEnvKeep = map[string]bool{
	"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "SHELL": true,
	"TERM": true, "LANG": true, "TMPDIR": true,
	"XDG_RUNTIME_DIR": true, "DBUS_SESSION_BUS_ADDRESS": true,
	"HTTPS_PROXY": true, "https_proxy": true, "HTTP_PROXY": true, "http_proxy": true,
	"NO_PROXY": true, "no_proxy": true, "ALL_PROXY": true, "all_proxy": true,
	"NODE_EXTRA_CA_CERTS": true, "SSL_CERT_FILE": true, "SSL_CERT_DIR": true,
}

// agentEnvLogin are Anthropic credentials the child keeps only when the
// environment routes to Anthropic: a key that came with another endpoint
// belongs to that endpoint.
var agentEnvLogin = map[string]bool{"ANTHROPIC_API_KEY": true, auth.OAuthTokenEnv: true}

// agentEnv builds the child's environment from environ by allowlist.
func agentEnv(environ []string) []string {
	var route []envset.Origin
	for _, kv := range environ {
		k, v, _ := strings.Cut(kv, "=")
		switch k {
		case "ANTHROPIC_BASE_URL", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX":
			route = append(route, envset.Origin{Key: k, Value: v})
		}
	}
	anthropic := routeEgress(route) == "anthropic"
	out := []string{"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1"}
	for _, kv := range environ {
		k, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if agentEnvKeep[k] || strings.HasPrefix(k, "LC_") || (anthropic && agentEnvLogin[k]) {
			out = append(out, kv)
		}
	}
	return out
}

// agentNeutralDir makes a fresh directory outside home: the system temp
// dir, or /tmp when that one is under home. Claude Code loads the CLAUDE.md
// files of a session's working directory and its ancestors, home's among
// them when it starts below home.
func agentNeutralDir() (string, error) {
	for _, base := range []string{os.TempDir(), "/tmp"} {
		if underHome(base) {
			continue
		}
		dir, err := os.MkdirTemp(base, "cpb-agent-")
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

// agentLoginHint ends every failure of the call.
const agentLoginHint = "cpb agent asks Claude through your Claude Code login: run claude and /login, or set ANTHROPIC_API_KEY"

// askAgent sends the installed state and the request, and returns the plan.
func askAgent(model string, state *agentContext, request string) (*agentPlan, error) {
	path, err := exec.LookPath("claude")
	if err != nil {
		return nil, errors.New("'claude' command not found. Install Claude Code first: https://claude.ai/download")
	}
	dir, err := agentNeutralDir()
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	stateJSON, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	c := exec.Command(path, agentClaudeArgs(model)...)
	c.Dir = dir
	c.Env = agentEnv(os.Environ())
	c.Stdin = strings.NewReader("Installed state: " + string(stateJSON) + "\n\nRequest: " + request + "\n")
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	runErr := c.Run()
	return parseAgentAnswer(stdout.Bytes(), stderr.String(), runErr)
}

// parseAgentAnswer reads claude -p --output-format json's result object.
func parseAgentAnswer(stdout []byte, stderr string, runErr error) (*agentPlan, error) {
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
		return nil, fmt.Errorf("Claude Code did not answer (%s). %s", why, agentLoginHint)
	}
	if res.IsError || runErr != nil {
		why := firstLine(res.Result)
		if why == "" {
			why = firstLine(stderr)
		}
		return nil, fmt.Errorf("Claude Code answered with an error: %s. %s", why, agentLoginHint)
	}
	raw := res.StructuredOutput
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage(res.Result)
	}
	var p agentPlan
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
