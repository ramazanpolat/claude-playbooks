package cmd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ramazanpolat/claude-playbooks/internal/auth"
	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envset"
	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

// cpb ask-tool is the one tool cpb ask gives the model: an MCP server over
// stdio that Claude Code starts for that one call (--mcp-config), granted
// alone (--allowedTools mcp__cpb__query). It reads, and only through the
// views below:
//
//   - a statement it accepts is a SHOW or EXPLAIN of the registry, run in
//     this process; nothing writes, launches or reaches a shell;
//   - what it returns is that statement's --json cut to a field allowlist:
//     names, launchers, attached env set names, a route class, versions,
//     and for sessions their kind, status, age and model. Never a
//     variable's value, a reference, a description, a path, a session's
//     folder or title, or a credential;
//   - it touches no credential: no token file, no login store, no Keychain,
//     not even to see whether one is there. EXPLAIN is built here for that
//     reason (askToolExplain), and the tool reports no auth class.
var askToolCmd = &cobra.Command{
	Use:    "ask-tool",
	Short:  "The read-only tool cpb ask gives the model (MCP over stdio)",
	Hidden: true,
	Args:   cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		askToolDropCredentials()
		return serveAskTool(os.Stdin, os.Stdout)
	},
}

func init() {
	rootCmd.AddCommand(askToolCmd)
}

// askToolParent is the process that started the tool: the claude process
// cpb ask runs, which env -i executes into this one.
var askToolParent = os.Getppid

// askToolName is the tool's name; Claude Code calls it mcp__cpb__query.
const askToolName = "query"

const askToolDescription = "Read cpb's state. statement is one of: SHOW PLAYBOOKS; SHOW PLAYBOOK <name>; SHOW ENVS; " +
	"SHOW ENV <name>; SHOW DEFAULTS; SHOW SESSIONS [FOR PLAYBOOK <name>]; EXPLAIN PLAYBOOK <name>. " +
	"Returns JSON with names, launchers, env set names, a route (anthropic or away), versions, and for " +
	"sessions their kind, status, start, last activity and model. It never returns values, paths or credentials."

// askToolDropCredentials removes from this process what Claude Code hands
// every MCP server, its credentials among them: nothing the tool reads
// needs one.
func askToolDropCredentials() {
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k == auth.OAuthTokenEnv || k == "ANTHROPIC_API_KEY" || k == "ANTHROPIC_AUTH_TOKEN" || manifest.LooksLikeSecretKey(k) {
			os.Unsetenv(k)
		}
	}
}

type rpcRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// serveAskTool answers MCP requests, one JSON object per line, until in
// ends. A notification (no id) gets no answer; an unknown method gets
// "method not found", which is how a client learns what is not supported.
func serveAskTool(in io.Reader, out io.Writer) error {
	saved := showAccount
	showAccount = false
	defer func() { showAccount = saved }()
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	enc := json.NewEncoder(out)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			enc.Encode(map[string]any{"jsonrpc": "2.0", "id": nil, "error": rpcError{-32700, "parse error"}})
			continue
		}
		if len(req.ID) == 0 || string(req.ID) == "null" {
			continue
		}
		result, rerr := askToolHandle(req)
		reply := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		if rerr != nil {
			reply["error"] = rerr
		} else {
			reply["result"] = result
		}
		if err := enc.Encode(reply); err != nil {
			return err
		}
	}
	return sc.Err()
}

func askToolHandle(req rpcRequest) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(req.Params, &p)
		if p.ProtocolVersion == "" {
			p.ProtocolVersion = "2025-06-18"
		}
		return map[string]any{
			"protocolVersion": p.ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "cpb", "version": Version},
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": []any{map[string]any{
			"name":        askToolName,
			"description": askToolDescription,
			"inputSchema": map[string]any{
				"type":                 "object",
				"properties":           map[string]any{"statement": map[string]any{"type": "string"}},
				"required":             []string{"statement"},
				"additionalProperties": false,
			},
		}}}, nil
	case "tools/call":
		var p struct {
			Name      string `json:"name"`
			Arguments struct {
				Statement string `json:"statement"`
			} `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil || p.Name != askToolName {
			return nil, &rpcError{-32602, "unknown tool: the one tool is " + askToolName}
		}
		text, err := askQuery(p.Arguments.Statement)
		if err != nil {
			return map[string]any{"content": []any{map[string]any{"type": "text", "text": err.Error()}}, "isError": true}, nil
		}
		return map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}}, nil
	}
	return nil, &rpcError{-32601, "method not found: " + req.Method}
}

// askToolViews are the statements the tool runs, and how their output is
// cut down.
const askToolRefusal = "the tool reads only: SHOW PLAYBOOKS, SHOW PLAYBOOK <name>, SHOW ENVS, SHOW ENV <name>, SHOW DEFAULTS, SHOW SESSIONS [FOR PLAYBOOK <name>], EXPLAIN PLAYBOOK <name>"

// askQuery runs one read statement and returns its cut-down JSON.
func askQuery(statement string) (string, error) {
	text := strings.TrimSpace(statement)
	text = strings.TrimSpace(strings.TrimSuffix(text, "--json"))
	if text == "" {
		return "", errors.New(askToolRefusal)
	}
	st, err := grammar.ParseLine(text)
	if err != nil {
		return "", fmt.Errorf("not a statement (%v); %s", err, askToolRefusal)
	}
	var args []string
	view := ""
	switch {
	case st.Verb == grammar.Show && !st.ShowCreate:
		switch st.Object {
		case grammar.Playbooks, "":
			args, view = []string{"SHOW", "PLAYBOOKS"}, "playbooks"
		case grammar.Playbook:
			args, view = []string{"SHOW", "PLAYBOOK", st.Name}, "playbook"
		case grammar.Envs:
			args, view = []string{"SHOW", "ENVS"}, "envs"
		case grammar.Env:
			args, view = []string{"SHOW", "ENV", st.Name}, "env"
		case grammar.Defaults:
			args, view = []string{"SHOW", "DEFAULTS"}, "defaults"
		case grammar.Sessions:
			args, view = []string{"SHOW", "SESSIONS"}, "sessions"
			if st.For != "" {
				args = append(args, "FOR", "PLAYBOOK", st.For)
			}
		}
	case st.Verb == grammar.Explain && st.Object == grammar.Playbook:
		v, err := askToolExplain(st.Name)
		if err != nil {
			return "", err
		}
		out, err := json.Marshal(v)
		return string(out), err
	}
	if view == "" {
		return "", errors.New(askToolRefusal)
	}
	raw, runErr := askCapture(append(args, "--json"))
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		// A list that leaves out an unreadable manifest still prints its
		// JSON and fails; anything else that fails has nothing to show.
		if runErr != nil {
			return "", errors.New(askToolScrub(runErr.Error()))
		}
		return "", errors.New("the statement gave no JSON")
	}
	out, err := json.Marshal(askToolCut(view, v))
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// askCapture runs a read statement in this process and returns what it
// printed. The tool's own replies go to the writer serveAskTool holds,
// never to os.Stdout, so swapping it here cannot mix the two.
func askCapture(args []string) ([]byte, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	saved := os.Stdout
	os.Stdout = w
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() { io.Copy(&buf, r); close(done) }()
	runErr := runStatement(args)
	w.Close()
	os.Stdout = saved
	<-done
	r.Close()
	return buf.Bytes(), runErr
}

// askToolScrub keeps an error to its first line, home spelled ~.
func askToolScrub(msg string) string {
	msg = firstLine(msg)
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		msg = strings.ReplaceAll(msg, home, "~")
	}
	return msg
}

// askToolCut keeps, of a view's JSON, only what the allowlist names.
func askToolCut(view string, v any) any {
	switch view {
	case "playbooks":
		var out []any
		for _, pb := range askList(v) {
			out = append(out, askCutPlaybook(askMap(pb)))
		}
		return askEmpty(out)
	case "playbook":
		return askCutPlaybook(askMap(v))
	case "envs":
		var out []any
		for _, e := range askList(v) {
			out = append(out, askCutEnv(askMap(e)))
		}
		return askEmpty(out)
	case "env":
		return askCutEnv(askMap(v))
	case "defaults":
		return askPick(askMap(v), "envs")
	case "sessions":
		var out []any
		self := askToolParent()
		for _, s := range askList(v) {
			// The claude process cpb ask started is a live session too, of
			// the playbook it signs in as; it is not one of the user's.
			if pid, ok := askMap(s)["pid"].(float64); ok && int(pid) == self {
				continue
			}
			out = append(out, askPick(askMap(s), "playbook", "launcher", "kind", "status", "started_at", "last_active", "model", "claude_version"))
		}
		return map[string]any{"count": len(out), "sessions": askEmpty(out)}
	}
	return nil
}

// askToolExplain is the tool's EXPLAIN PLAYBOOK: where a launch of the
// playbook sends its requests, as cpb ask itself judges it, and its login
// and global instructions as its manifest sets them. It does not run
// EXPLAIN, which judges the login from the token file, the login store and
// the Keychain: the tool process touches no credential.
func askToolExplain(name string) (map[string]any, error) {
	pb, err := playbook.Require(config.ResolvePlaybooksDir(), name)
	if err != nil {
		return nil, errors.New(askToolScrub(err.Error()))
	}
	route := "away"
	if askPlaybookRoute(pb.Path) == "anthropic" {
		route = "anthropic"
	}
	v := describePlaybook(pb)
	return map[string]any{"playbook": pb.Name, "route": route, "login": v.Login, "global_instructions": v.GlobalInstructions}, nil
}

func askCutPlaybook(m map[string]any) map[string]any {
	out := askPick(m, "name", "version", "launcher", "envs", "login", "global_instructions", "agent", "model")
	out["always_sandboxed"] = askMap(m["sandbox"])["always"] == true
	out["plugins"] = askPluck(m["plugins"], "id", "enabled")
	out["marketplaces"] = askNames(m["marketplaces"], "name")
	out["mcp_servers"] = askNames(m["mcp_servers"], "name")
	out["skills"] = askNames(m["skills"], "name")
	return out
}

func askCutEnv(m map[string]any) map[string]any {
	out := askPick(m, "name", "used_by", "default")
	if name, ok := m["name"].(string); ok {
		out["route"] = askRoute(envset.Dir(config.ResolvePlaybooksDir()), &manifest.Env{Sets: []string{name}})
	}
	return out
}

func askMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func askList(v any) []any {
	if l, ok := v.([]any); ok {
		return l
	}
	return nil
}

func askEmpty(l []any) []any {
	if l == nil {
		return []any{}
	}
	return l
}

func askPick(m map[string]any, keys ...string) map[string]any {
	out := map[string]any{}
	for _, k := range keys {
		if v, ok := m[k]; ok {
			out[k] = v
		}
	}
	return out
}

func askPluck(v any, keys ...string) []any {
	var out []any
	for _, e := range askList(v) {
		out = append(out, askPick(askMap(e), keys...))
	}
	return askEmpty(out)
}

func askNames(v any, key string) []any {
	var out []any
	for _, e := range askList(v) {
		if s, ok := askMap(e)[key].(string); ok {
			out = append(out, s)
		}
	}
	return askEmpty(out)
}
