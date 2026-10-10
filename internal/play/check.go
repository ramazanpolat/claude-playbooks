package play

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// Risk codes.
const (
	RiskRunsProgram       = "runs_program"
	RiskThirdPartyCode    = "third_party_code"
	RiskSendsData         = "sends_data"
	RiskActsWithoutAsking = "acts_without_asking"
	RiskFetchesCode       = "fetches_code"
	RiskEndpointChange    = "endpoint_change"
	RiskTLSOrProxy        = "tls_or_proxy"
	RiskTelemetryExport   = "telemetry_export"
	RiskUsesSecret        = "uses_secret"
	RiskNoSandbox         = "no_sandbox"
)

// Refusal is a statement or clause a played recipe may not hold.
type Refusal struct {
	Line   int    `json:"line"`
	What   string `json:"what"`
	Reason string `json:"reason"`
}

// Risk is a clause the preview highlights. Confirm, when set, must be typed
// before the recipe runs: the host a model endpoint or proxy moves to,
// "TLS" for a trust change, or a secret reference.
type Risk struct {
	Code    string `json:"code"`
	Line    int    `json:"line"`
	Clause  string `json:"clause"`
	Detail  string `json:"detail"`
	Confirm string `json:"confirm,omitempty"`
}

// Result is a recipe checked: its header, what is refused, and the risks.
type Result struct {
	Header  Header    `json:"-"`
	Refused []Refusal `json:"refused"`
	Risks   []Risk    `json:"risks"`
	// Endpoint is the model endpoint's host when the recipe moves it away
	// from Anthropic, "" otherwise: play then isolates the login and blocks
	// the credentials a launch would inherit.
	Endpoint string `json:"endpoint"`
}

// Check reads a recipe and says what a play would refuse and what it would
// highlight. A parse error is a refusal at its line.
func Check(src []byte) *Result {
	r := &Result{Header: ParseHeader(src), Refused: []Refusal{}, Risks: []Risk{}}
	stmts, err := grammar.ParseFile(string(src))
	if err != nil {
		r.refuse(0, "the file", err.Error())
		return r
	}
	if len(stmts) == 0 {
		r.refuse(0, "the file", "no statements: a played recipe is ALTER PLAYBOOK statements with no name")
	}
	for _, s := range stmts {
		r.statement(s)
	}
	return r
}

func (r *Result) refuse(line int, what, reason string) {
	r.Refused = append(r.Refused, Refusal{Line: line, What: what, Reason: reason})
}

func (r *Result) risk(line int, code, clause, detail, confirm string) {
	r.Risks = append(r.Risks, Risk{Code: code, Line: line, Clause: clause, Detail: detail, Confirm: confirm})
}

const onlyThisPlaybook = "a played recipe changes nothing on your machine but the one playbook play makes"

func (r *Result) statement(s *grammar.Stmt) {
	line := s.Pos.Line
	switch {
	case s.Verb == grammar.Include:
		r.refuse(line, "INCLUDE", "a play runs one file: flatten the layers (SHOW CREATE of the built playbook)")
		return
	case s.Verb == grammar.Use:
		r.refuse(line, "USE PLAYBOOK", onlyThisPlaybook)
		return
	case s.Object == grammar.Env || s.Object == grammar.Defaults:
		r.refuse(line, fmt.Sprintf("%s %s", s.Verb, s.Object), "it would change your env sets or DEFAULTS: "+onlyThisPlaybook)
		return
	case s.Verb != grammar.Alter || s.Object != grammar.Playbook || !s.Recipe:
		r.refuse(line, strings.TrimSpace(fmt.Sprintf("%s %s %s", s.Verb, s.Object, s.Name)),
			onlyThisPlaybook+": write it as a recipe, ALTER PLAYBOOK with no name")
		return
	}
	for _, c := range s.Clauses {
		r.clause(c)
	}
}

// Clauses a played recipe may not hold, with the reason.
var refusedClauses = map[grammar.Kind]string{
	grammar.UseEnv:          "it would attach your env sets, and your keys, to someone else's playbook",
	grammar.AddEnv:          "it would attach your env sets, and your keys, to someone else's playbook",
	grammar.DropEnv:         "it would detach your env sets: " + onlyThisPlaybook,
	grammar.RenameTo:        "the name is play's decision, not the recipe's",
	grammar.Launcher:        "the launcher is play's decision, not the recipe's",
	grammar.DefaultLauncher: "the launcher is play's decision, not the recipe's",
}

func (r *Result) clause(c grammar.Clause) {
	line, kind := c.Pos.Line, string(c.Kind)
	if why, ok := refusedClauses[c.Kind]; ok {
		r.refuse(line, kind, why)
		return
	}
	switch c.Kind {
	case grammar.AddMarketplace:
		src, err := grammar.MarketplaceSource(c.Arg)
		switch {
		case err != nil:
			r.refuse(line, kind+" "+c.Names[0], err.Error())
			return
		case src == grammar.SourceDirectory:
			r.refuse(line, kind+" "+c.Names[0], "a local directory source points into your filesystem")
			return
		}
		r.risk(line, RiskThirdPartyCode, kind+" "+c.Names[0], "a marketplace from "+c.Arg+": its plugins can carry hooks and commands that run as you", "")
	case grammar.AddPlugin:
		r.risk(line, RiskThirdPartyCode, kind+" "+c.Names[0], "a plugin can carry hooks (shell commands run on events) and commands", "")
	case grammar.SetAgent, grammar.SetModel, grammar.AddModel, grammar.SetModelPicker,
		grammar.DenyTool, grammar.SetStatuslineRefresh, grammar.BlockVar, grammar.NoLauncher:
		// configuration only
	case grammar.SetProperties:
		// A recipe may isolate more, never less: the login and the memory
		// of ~/.claude stay apart unless play itself shares them.
		for _, v := range c.Settings {
			if v.Value == "shared" {
				r.refuse(line, "SET "+v.Key+" = 'shared'", "sharing the "+v.Key+" is play's decision, not the recipe's")
				return
			}
		}
	case grammar.DeleteProperties:
		// A new playbook already has every default; DELETE only undoes.
		r.refuse(line, "DELETE "+c.Settings[0].Key, "nothing to undo on a new playbook")
		return
	case grammar.AllowTool:
		for _, rule := range c.Names {
			if why := wideAllow(rule); why != "" {
				r.risk(line, RiskActsWithoutAsking, "ALLOW TOOL '"+rule+"'", why+", without asking", "")
			}
		}
	case grammar.SetStatusline:
		r.risk(line, RiskRunsProgram, kind, "runs '"+c.Arg+"' every few seconds, as you", "")
	case grammar.AddSkill:
		r.skill(c)
	case grammar.AddMCP:
		r.mcp(c)
	case grammar.SetVar:
		for _, v := range c.Vars {
			r.setVar(line, v)
		}
	case grammar.SetRef:
		for _, v := range c.Vars {
			r.risk(line, RiskUsesSecret, "SET VAR "+v.Key+" FROM '"+v.Ref+"'",
				"your secret "+v.Ref+" → the playbook's environment as "+v.Key+", which every tool and MCP server can read", v.Ref)
		}
	default:
		if strings.HasPrefix(kind, "DROP ") || strings.HasPrefix(kind, "UNSET ") || strings.HasPrefix(kind, "DELETE ") {
			r.refuse(line, kind, "nothing to undo on a new playbook")
			return
		}
		r.refuse(line, kind, "not in a played recipe")
	}
}

func (r *Result) skill(c grammar.Clause) {
	line, name := c.Pos.Line, "ADD SKILL "+c.Names[0]
	if c.Skill == nil {
		return
	}
	src := c.Skill.From
	kind, err := grammar.SkillSource(src)
	switch {
	case err != nil:
		r.refuse(line, name, err.Error())
	case kind == grammar.SkillDirectory:
		r.refuse(line, name, "a local directory source points into your filesystem")
	case strings.HasPrefix(src, "http://"), strings.HasPrefix(src, "file://"):
		r.refuse(line, name, "a played recipe fetches skills over https, git@ or github: only")
	default:
		r.risk(line, RiskFetchesCode, name, "a skill from "+src+": it can tell the agent to run its scripts", "")
	}
}

func (r *Result) mcp(c grammar.Clause) {
	line, name := c.Pos.Line, "ADD MCP SERVER "+c.Names[0]
	m := c.MCP
	if m == nil {
		return
	}
	dest := "the command '" + m.Command + "'"
	if u, err := url.Parse(m.URL); m.URL != "" && (err != nil || u.User != nil) {
		r.refuse(line, name, "an MCP server URL carrying credentials is refused: use a HEADER with a reference, FROM '<ref>'")
		return
	}
	if m.URL != "" {
		dest = hostOf(m.URL)
		r.risk(line, RiskSendsData, name, "tool calls and their content go to "+dest, "")
	} else {
		r.risk(line, RiskRunsProgram, name, "starts '"+strings.TrimSpace(m.Command+" "+strings.Join(m.Args, " "))+"' every session, as you", "")
	}
	for _, v := range m.Env {
		if v.Ref != "" {
			r.risk(line, RiskUsesSecret, name+" ENV "+v.Key, "your secret "+v.Ref+" → MCP server "+c.Names[0]+", env "+v.Key+", to "+dest, v.Ref)
		} else if manifest.LooksLikeSecretKey(v.Key) && !manifest.PlainSetting(v.Value) {
			r.refuse(line, name+" ENV "+v.Key, "a shared recipe carries no secret: use a reference, FROM '<ref>'")
		}
	}
	for _, h := range m.Headers {
		if h.Ref != "" {
			r.risk(line, RiskUsesSecret, name+" HEADER "+h.Key, "your secret "+h.Ref+" → MCP server "+c.Names[0]+", HEADER "+h.Key+", to "+dest, h.Ref)
		} else if grammar.CredentialHeader(h.Key) {
			r.refuse(line, name+" HEADER "+h.Key, "a shared recipe carries no secret: use a reference, FROM '<ref>'")
		}
	}
}

// Model-endpoint variables: a change moves every request, and the
// credential sent with it, to another host. Typed to confirm.
var endpointVars = map[string]bool{
	"ANTHROPIC_BASE_URL": true, "ANTHROPIC_BEDROCK_BASE_URL": true, "ANTHROPIC_VERTEX_BASE_URL": true,
}

var (
	proxyVar     = regexp.MustCompile(`^(?i)(https?_proxy|all_proxy)$`)
	tlsVars      = map[string]bool{"NODE_EXTRA_CA_CERTS": true, "SSL_CERT_FILE": true, "SSL_CERT_DIR": true, "NODE_TLS_REJECT_UNAUTHORIZED": true}
	telemetryVar = regexp.MustCompile(`^(OTEL_EXPORTER_OTLP_[A-Z_]*|CLAUDE_CODE_ENABLE_TELEMETRY)$`)
	urlishVar    = regexp.MustCompile(`_(BASE_URL|URL|HOST|ENDPOINT)$`)
)

func (r *Result) setVar(line int, v grammar.Var) {
	key, clause := v.Key, "SET VAR "+v.Key
	switch {
	case manifest.LooksLikeSecretKey(key) && !manifest.PlainSetting(v.Value):
		// The grammar's own rule: a value that cannot be a secret (empty,
		// an integer, true/false) passes, so MAX_THINKING_TOKENS=8000 does.
		r.refuse(line, clause, "a shared recipe carries no secret, even AS PLAINTEXT: use a reference, FROM '<ref>'")
	case endpointVars[key]:
		host := hostOf(v.Value)
		// Anthropic's own host is no move, over https only: plain http
		// would send the conversation and the credential unencrypted.
		if anthropic(host) && strings.HasPrefix(strings.TrimSpace(v.Value), "https://") {
			return
		}
		r.risk(line, RiskEndpointChange, clause+"="+v.Value,
			"every request, your code and conversation with it, goes to "+host+", with the credential sent to it", host)
		if r.Endpoint == "" {
			r.Endpoint = host
		}
	case key == "CLAUDE_CODE_USE_BEDROCK" || key == "CLAUDE_CODE_USE_VERTEX":
		if v.Value == "" || v.Value == "0" || strings.EqualFold(v.Value, "false") {
			return
		}
		word := strings.ToLower(strings.TrimPrefix(key, "CLAUDE_CODE_USE_"))
		r.risk(line, RiskEndpointChange, clause+"="+v.Value, "requests go to "+word+" instead of Anthropic", word)
		if r.Endpoint == "" {
			r.Endpoint = word
		}
	case proxyVar.MatchString(key):
		host := hostOf(v.Value)
		r.risk(line, RiskTLSOrProxy, clause+"="+v.Value, "every request goes through the proxy "+host+", which can read or change it", host)
	case tlsVars[key]:
		r.risk(line, RiskTLSOrProxy, clause+"="+v.Value, "changes which TLS certificates are trusted: another party can read or change every request", "TLS")
	case telemetryVar.MatchString(key):
		r.risk(line, RiskTelemetryExport, clause+"="+v.Value, "prompts and usage can be exported", "")
	case urlishVar.MatchString(key):
		r.risk(line, RiskSendsData, clause+"="+v.Value, "a tool may send data to "+hostOf(v.Value), "")
	}
}

// hostOf is a URL's host, or the value itself when it is not a URL.
func hostOf(v string) string {
	v = strings.TrimSpace(v)
	if u, err := url.Parse(v); err == nil && u.Hostname() != "" {
		return strings.ToLower(u.Hostname())
	}
	if u, err := url.Parse("//" + v); err == nil && u.Hostname() != "" {
		return strings.ToLower(u.Hostname())
	}
	return v
}

func anthropic(host string) bool {
	return host == "anthropic.com" || strings.HasSuffix(host, ".anthropic.com")
}

// Interpreters and launchers: any rule with a wildcard after them runs
// anything.
var wideInterpreters = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "fish": true, "dash": true, "python": true, "python3": true,
	"node": true, "deno": true, "bun": true, "ruby": true, "perl": true, "php": true, "lua": true,
	"eval": true, "exec": true, "sudo": true, "su": true, "env": true, "xargs": true, "npx": true, "uvx": true,
}

// Tools that are wide on their own or with a bare wildcard ("kubectl *"),
// and narrow with a subcommand ("kubectl get *").
var wideTools = map[string]bool{
	"curl": true, "wget": true, "ssh": true, "scp": true, "rsync": true, "nc": true, "ncat": true,
	"rm": true, "docker": true, "kubectl": true, "pip": true, "npm": true,
}

// wideAllow says what an ALLOW TOOL rule lets the agent do without asking,
// when that is more than a narrow pattern; "" for a narrow one. It errs
// toward flagging (root, 2026-10-01). The list is in the reference.
func wideAllow(rule string) string {
	tool, arg := rule, ""
	if i := strings.Index(rule, "("); i >= 0 && strings.HasSuffix(rule, ")") {
		tool, arg = rule[:i], strings.TrimSpace(rule[i+1:len(rule)-1])
	}
	everything := arg == "" || arg == "*" || arg == "**" || arg == ":*"
	switch tool {
	case "*":
		return "any tool"
	case "Bash":
		if everything {
			return "any shell command"
		}
		// An interpreter is wide with a wildcard, or alone; a tool is wide
		// alone or with a bare wildcard, narrow with a subcommand
		// (kubectl get *); an exact command line (npm test) is narrow.
		words := strings.Fields(strings.TrimSuffix(strings.TrimSuffix(arg, ":*"), "*"))
		if len(words) > 0 {
			// By path too: /bin/sh, /usr/bin/python3.
			words[0] = path.Base(words[0])
			switch {
			case wideInterpreters[words[0]] && (strings.Contains(arg, "*") || len(words) == 1):
				return "any '" + words[0] + "' command"
			case wideTools[words[0]] && len(words) == 1:
				return "any '" + words[0] + "' command"
			}
		}
		if strings.HasPrefix(arg, "git push") {
			return "git push"
		}
	case "Write", "Edit", "MultiEdit", "NotebookEdit", "Read":
		if everything || arg == "/**" || arg == "~/**" || arg == "//**" || arg == "/*" || arg == "~/*" || arg == "~" || arg == "/" {
			verb := strings.ToLower(tool)
			if tool == "Read" {
				return "read any file, your keys and tokens included"
			}
			return verb + " any file"
		}
	case "WebFetch":
		if everything || strings.HasPrefix(arg, "domain:*") {
			return "fetch any URL"
		}
	}
	return ""
}
