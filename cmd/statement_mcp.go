package cmd

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/settings"
)

// MCP servers (docs/reference/cli-grammar.md, "An agent's configuration"):
// ADD / DROP MCP SERVER run Claude Code's own `claude mcp add-json` and
// `claude mcp remove`, user scope, with CLAUDE_CONFIG_DIR set to the
// playbook. A secret never enters Claude's config: the server gets a
// ${CPB_MCP_…} placeholder, and the reference is stored in the playbook's
// own layer, resolved at launch by the secret helper.

const claudeJSON = ".claude.json"

// claudeMCP runs `claude mcp <args>` against one playbook, as claudePlugin
// does for plugins.
var claudeMCP = func(configDir string, args ...string) ([]byte, error) {
	path, err := exec.LookPath("claude")
	if err != nil {
		return nil, errors.New("claude is not on PATH: the MCP clauses run `claude mcp`")
	}
	c := exec.Command(path, append([]string{"mcp"}, args...)...)
	c.Env = append(withoutKeys(os.Environ(), []string{"CLAUDE_CONFIG_DIR"}), "CLAUDE_CONFIG_DIR="+configDir)
	c.Dir = os.TempDir()
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	if err := c.Run(); err != nil {
		msg := strings.TrimSpace(errb.String() + " " + out.String())
		if msg == "" {
			msg = err.Error()
		}
		return out.Bytes(), errors.New(msg)
	}
	return out.Bytes(), nil
}

func mcpClauses(clauses []grammar.Clause) bool {
	for _, c := range clauses {
		if c.Kind == grammar.AddMCP || c.Kind == grammar.DropMCP {
			return true
		}
	}
	return false
}

// mcpVar derives the variable that carries one secret reference of one
// server: readable (server, E|H, key) and unique (a hash of the exact
// server, kind and key), so an env entry and a header of the same name, or
// two names that normalize alike, never share one.
func mcpVar(server, kind, key string) string {
	norm := func(s string) string {
		b := []byte(strings.ToUpper(s))
		for i, c := range b {
			if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
				b[i] = '_'
			}
		}
		return string(b)
	}
	h := sha256.Sum256([]byte(server + "\x00" + kind + "\x00" + key))
	return fmt.Sprintf("CPB_MCP_%s_%s_%s_%s", norm(server), kind, norm(key), hex.EncodeToString(h[:4]))
}

// mcpConfig is the JSON `claude mcp add-json` takes for a declaration, and
// the references it needs, by derived variable.
func mcpConfig(server string, m *grammar.MCP) (*settings.Object, map[string]string) {
	refs := map[string]string{}
	o := settings.NewObject()
	values := func(vars []grammar.Var, kind string) *settings.Object {
		obj := settings.NewObject()
		for _, v := range vars {
			if v.Ref != "" {
				name := mcpVar(server, kind, v.Key)
				refs[name] = v.Ref
				_ = obj.Set(v.Key, "${"+name+"}")
			} else {
				_ = obj.Set(v.Key, v.Value)
			}
		}
		return obj
	}
	if m.URL != "" {
		kind := "http"
		if m.SSE {
			kind = "sse"
		}
		_ = o.Set("type", kind)
		_ = o.Set("url", m.URL)
		if len(m.Headers) > 0 {
			_ = o.Set("headers", values(m.Headers, "H"))
		}
		return o, refs
	}
	_ = o.Set("type", "stdio")
	_ = o.Set("command", m.Command)
	args := m.Args
	if args == nil {
		args = []string{}
	}
	_ = o.Set("args", args)
	if len(m.Env) > 0 {
		_ = o.Set("env", values(m.Env, "E"))
	}
	return o, refs
}

// readMCPServers reads the user-scope servers from a config directory's
// .claude.json: name -> config. A missing file holds none.
func readMCPServers(configDir string) (map[string]json.RawMessage, []string, error) {
	out := map[string]json.RawMessage{}
	if configDir == "" {
		return out, nil, nil
	}
	data, err := os.ReadFile(filepath.Join(configDir, claudeJSON))
	if errors.Is(err, os.ErrNotExist) {
		return out, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	root, err := settings.ParseObject(data)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", claudeJSON, err)
	}
	servers, err := root.Object("mcpServers")
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", claudeJSON, err)
	}
	for _, k := range servers.Keys() {
		out[k] = servers.Raw(k)
	}
	return out, servers.Keys(), nil
}

// sameMCP compares a stored config with a declared one, ignoring the empty
// fields Claude Code fills in (args, env, headers).
func sameMCP(have json.RawMessage, want *settings.Object) bool {
	norm := func(raw []byte) map[string]any {
		var m map[string]any
		if json.Unmarshal(raw, &m) != nil {
			return nil
		}
		for _, k := range []string{"args", "env", "headers"} {
			switch v := m[k].(type) {
			case []any:
				if len(v) == 0 {
					delete(m, k)
				}
			case map[string]any:
				if len(v) == 0 {
					delete(m, k)
				}
			}
		}
		return m
	}
	w, _ := want.MarshalJSON()
	a, b := norm(have), norm(w)
	return a != nil && reflect.DeepEqual(a, b)
}

// mcpPlan is what a statement's MCP clauses change: the commands to run,
// the references to set and forget in the playbook's layer, and the new
// record of derived variables.
type mcpPlan struct {
	steps     []pluginStep
	lines     []string
	setRefs   map[string]string
	dropRefs  []string
	records   map[string]*manifest.MCPRecord // nil value: forget the record
	checkRefs []grammar.Clause               // the references, for the helper's check
}

func planMCP(state map[string]json.RawMessage, rec map[string]*manifest.MCPRecord, clauses []grammar.Clause) (*mcpPlan, error) {
	p := &mcpPlan{setRefs: map[string]string{}, records: map[string]*manifest.MCPRecord{}}
	recordOf := func(name string) []string {
		if r, ok := p.records[name]; ok {
			if r == nil {
				return nil
			}
			return r.Vars
		}
		if r := rec[name]; r != nil {
			return r.Vars
		}
		return nil
	}
	for ci, c := range clauses {
		switch c.Kind {
		case grammar.AddMCP:
			name := c.Names[0]
			if c.MCP.URL == "" && len(c.MCP.Headers) > 0 {
				return nil, fmt.Errorf("ADD MCP SERVER %s: HEADER applies to a remote server (URL)", name)
			}
			if c.MCP.URL != "" && len(c.MCP.Env) > 0 {
				return nil, fmt.Errorf("ADD MCP SERVER %s: VAR applies to a COMMAND server; a remote server takes HEADER", name)
			}
			cfg, refs := mcpConfig(name, c.MCP)
			vars := make([]string, 0, len(refs))
			for v, ref := range refs {
				vars = append(vars, v)
				p.setRefs[v] = ref
				p.checkRefs = append(p.checkRefs, grammar.Clause{Kind: grammar.SetRef, Vars: []grammar.Var{{Key: v, Ref: ref}}})
			}
			sort.Strings(vars)
			for _, old := range recordOf(name) {
				if !slices.Contains(vars, old) {
					p.dropRefs = append(p.dropRefs, old)
				}
			}
			if len(vars) > 0 {
				p.records[name] = &manifest.MCPRecord{Vars: vars}
			} else {
				p.records[name] = nil
			}
			have, exists := state[name]
			if exists && sameMCP(have, cfg) {
				continue
			}
			js, _ := cfg.MarshalJSON()
			if exists {
				p.steps = append(p.steps, pluginStep{mcp: true, clause: ci, args: []string{"remove", name, "--scope", "user"}, line: "replaced  MCP server " + name})
			}
			p.steps = append(p.steps, pluginStep{mcp: true, clause: ci, args: []string{"add-json", name, string(js), "--scope", "user"}, line: "MCP server " + name})
			state[name] = js
		case grammar.DropMCP:
			name := c.Names[0]
			p.dropRefs = append(p.dropRefs, recordOf(name)...)
			p.records[name] = nil
			if _, exists := state[name]; !exists {
				p.lines = append(p.lines, "MCP server "+name+" was not declared")
				continue
			}
			delete(state, name)
			p.steps = append(p.steps, pluginStep{mcp: true, clause: ci, args: []string{"remove", name, "--scope", "user"}, line: "dropped   MCP server " + name})
		}
	}
	return p, nil
}

// applyToManifest applies the plan to a manifest in two phases. The first
// (removals false) adds: new references and records. The second (removals
// true) forgets the references and records a statement no longer needs; it
// runs only after the commands succeeded, so a failed command never leaves a
// server that is still declared without its references.
func (p *mcpPlan) applyToManifest(m *manifest.Manifest, removals bool) {
	if m.Env == nil {
		m.Env = &manifest.Env{}
	}
	if removals {
		for _, v := range p.dropRefs {
			delete(m.Env.Refs, v)
		}
		for name, r := range p.records {
			if r == nil {
				delete(m.MCP, name)
			} else {
				m.MCP[name] = r // exactly the new variables
			}
		}
	} else {
		for v, ref := range p.setRefs {
			if m.Env.Refs == nil {
				m.Env.Refs = map[string]string{}
			}
			m.Env.Refs[v] = ref
		}
		for name, r := range p.records {
			if r == nil {
				continue
			}
			if m.MCP == nil {
				m.MCP = map[string]*manifest.MCPRecord{}
			}
			// Until the commands succeed, the record keeps the old variables
			// too, so a later DROP still forgets them all.
			vars := slices.Clone(r.Vars)
			if old := m.MCP[name]; old != nil {
				for _, v := range old.Vars {
					if !slices.Contains(vars, v) {
						vars = append(vars, v)
					}
				}
				sort.Strings(vars)
			}
			m.MCP[name] = &manifest.MCPRecord{Vars: vars}
		}
	}
	if len(m.MCP) == 0 {
		m.MCP = nil
	}
}

// hasRemovals reports whether the second phase changes anything.
func (p *mcpPlan) hasRemovals(before map[string]*manifest.MCPRecord) bool {
	if len(p.dropRefs) > 0 {
		return true
	}
	for name, r := range p.records {
		if r == nil && before[name] != nil {
			return true
		}
	}
	return false
}

// mcpServerJSON is one server as SHOW prints it.
type mcpServerJSON struct {
	Name      string    `json:"name"`
	Transport string    `json:"transport"`
	Command   *string   `json:"command,omitempty"`
	Args      []string  `json:"args,omitempty"`
	URL       *string   `json:"url,omitempty"`
	Env       []varJSON `json:"env"`
	Headers   []varJSON `json:"headers"`
}

// describeMCP reads a playbook's servers for SHOW: a placeholder cpb
// derived is shown as its reference, a credential-looking literal redacted.
func describeMCP(configDir string, m *manifest.Manifest) []mcpServerJSON {
	state, order, err := readMCPServers(configDir)
	if err != nil {
		return []mcpServerJSON{}
	}
	refs := map[string]string{}
	if m != nil && m.Env != nil {
		refs = m.Env.Refs
	}
	out := []mcpServerJSON{}
	for _, name := range order {
		var cfg struct {
			Type    string            `json:"type"`
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			URL     string            `json:"url"`
			Env     map[string]string `json:"env"`
			Headers map[string]string `json:"headers"`
		}
		if json.Unmarshal(state[name], &cfg) != nil {
			continue
		}
		s := mcpServerJSON{Name: name, Transport: cfg.Type, Env: mcpValues(cfg.Env, refs), Headers: mcpValues(cfg.Headers, refs)}
		if s.Transport == "" {
			s.Transport = "stdio"
		}
		if cfg.URL != "" {
			s.URL = strPtr(cfg.URL)
		} else {
			s.Command = strPtr(cfg.Command)
			s.Args = cfg.Args
		}
		out = append(out, s)
	}
	return out
}

func mcpValues(values map[string]string, refs map[string]string) []varJSON {
	out := []varJSON{}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := values[k]
		if ref, ok := placeholderRef(v, refs); ok {
			out = append(out, varJSON{Key: k, Ref: strPtr(ref)})
			continue
		}
		out = append(out, literalMCPValue(k, v))
	}
	return out
}

// placeholderRef reports whether a value is exactly ${VAR} for a variable
// the playbook holds a reference for, and returns that reference.
func placeholderRef(v string, refs map[string]string) (string, bool) {
	if !strings.HasPrefix(v, "${") || !strings.HasSuffix(v, "}") {
		return "", false
	}
	ref, ok := refs[v[2:len(v)-1]]
	return ref, ok
}

// literalMCPValue is a literal as SHOW prints it: a credential-looking key
// (or header) is redacted, as a variable's would be.
func literalMCPValue(k, v string) varJSON {
	if grammar.CredentialHeader(k) && !manifest.PlainSetting(v) {
		return varJSON{Key: k, Redacted: true}
	}
	return literalVar(k, v)
}

// mcpCreateClauses turns a playbook's servers back into ADD MCP SERVER
// clauses, with comments for what the grammar cannot write (a
// credential-looking literal added by hand, a transport it does not know).
func mcpCreateClauses(configDir string, m *manifest.Manifest) ([]grammar.Clause, []string) {
	state, order, err := readMCPServers(configDir)
	if err != nil {
		return nil, []string{"-- MCP servers: " + claudeJSON + " cannot be read; not written"}
	}
	refs := map[string]string{}
	if m != nil && m.Env != nil {
		refs = m.Env.Refs
	}
	var clauses []grammar.Clause
	var comments []string
	for _, name := range order {
		var cfg struct {
			Type    string            `json:"type"`
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			URL     string            `json:"url"`
			Env     map[string]string `json:"env"`
			Headers map[string]string `json:"headers"`
		}
		if json.Unmarshal(state[name], &cfg) != nil || manifest.ValidateSetName(name) != nil {
			comments = append(comments, "-- MCP SERVER "+name+" is not one the grammar writes; not written")
			continue
		}
		mc := &grammar.MCP{Command: cfg.Command, Args: cfg.Args, URL: cfg.URL}
		switch cfg.Type {
		case "", "stdio":
		case "http":
		case "sse":
			mc.SSE = true
		default:
			comments = append(comments, "-- MCP SERVER "+name+" uses transport "+cfg.Type+", which the grammar does not write; not written")
			continue
		}
		ok := true
		conv := func(values map[string]string, kind string) []grammar.Var {
			keys := make([]string, 0, len(values))
			for k := range values {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			var out []grammar.Var
			for _, k := range keys {
				v := values[k]
				if ref, isRef := placeholderRef(v, refs); isRef {
					out = append(out, grammar.Var{Key: k, Ref: ref})
					continue
				}
				credential := manifest.LooksLikeSecretKey(k)
				if kind == "H" {
					credential = grammar.CredentialHeader(k)
				}
				if credential && !manifest.PlainSetting(v) {
					ok = false
				}
				out = append(out, grammar.Var{Key: k, Value: v})
			}
			return out
		}
		mc.Env, mc.Headers = conv(cfg.Env, "E"), conv(cfg.Headers, "H")
		if !ok {
			comments = append(comments, "-- MCP SERVER "+name+" holds a credential as a literal in "+claudeJSON+"; not written: re-declare it with … FROM '<ref>'")
			continue
		}
		clauses = append(clauses, grammar.Clause{Kind: grammar.AddMCP, Names: []string{name}, MCP: mc})
	}
	return clauses, comments
}

// mcpVarsOf lists the variables recorded for a playbook's servers.
func mcpVarsOf(m *manifest.Manifest) map[string]bool {
	out := map[string]bool{}
	if m == nil {
		return out
	}
	for _, r := range m.MCP {
		if r == nil {
			continue
		}
		for _, v := range r.Vars {
			out[v] = true
		}
	}
	return out
}
