package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/settings"
)

// Plugins and the agent (SPEC.md, "Plugins and the
// agent"). The marketplace and plugin clauses delegate to Claude Code's own
// CLI, `claude plugin …`, run with CLAUDE_CONFIG_DIR set to the playbook, so
// its user scope is that playbook: the format of settings.json and the
// plugin cache stay Claude Code's to own. SET AGENT has no CLI and is the
// one key cpb writes itself. Reads (SHOW, EXPLAIN, SHOW CREATE) read
// settings.json, so showing a playbook runs nothing.

const (
	keyMarketplaces = "extraKnownMarketplaces"
	keyPlugins      = "enabledPlugins"
	keyAgent        = "agent"
	keyModel        = "model"
	keyStatusline   = "statusLine"
	// keyRefreshInterval is statusLine.refreshInterval, in whole seconds.
	keyRefreshInterval = "refreshInterval"
	keyPermissions     = "permissions"
)

type marketplaceJSON struct {
	Name   string          `json:"name"`
	Source json.RawMessage `json:"source"`
}

type pluginJSON struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
}

// pluginClauses reports whether a statement has plugin or agent clauses.
func pluginClauses(clauses []grammar.Clause) bool {
	for _, c := range clauses {
		switch c.Kind {
		case grammar.AddMarketplace, grammar.DropMarketplace, grammar.AddPlugin,
			grammar.DropPlugin, grammar.SetAgent, grammar.UnsetAgent,
			grammar.AllowTool, grammar.DenyTool, grammar.UnsetTool,
			grammar.SetStatusline, grammar.UnsetStatusline, grammar.SetModel, grammar.UnsetModel,
			grammar.SetStatuslineRefresh, grammar.UnsetStatuslineRefresh, grammar.SetStatuslinePrevious,
			grammar.AddModel, grammar.DropModel, grammar.SetModelPicker, grammar.UnsetModelPicker:
			return true
		}
	}
	return false
}

// plansPluginCommands reports whether a statement has clauses that run
// `claude plugin` (all but the agent's).
func plansPluginCommands(clauses []grammar.Clause) bool {
	for _, c := range clauses {
		switch c.Kind {
		case grammar.AddMarketplace, grammar.DropMarketplace, grammar.AddPlugin, grammar.DropPlugin:
			return true
		}
	}
	return false
}

// minClaudePluginJSON is the first Claude Code with `claude plugin install
// --json` and `uninstall --json`, which the plugin clauses run (changelog
// 2.1.268). An older claude fails them with a raw usage error, so a plan
// that installs or uninstalls is refused first, in one line.
const minClaudePluginJSON = "2.1.268"

var claudeVersionRe = regexp.MustCompile(`(\d+\.\d+\.\d+)`)

// parseClaudeVersion finds the version in `claude --version`'s answer
// ("2.1.283 (Claude Code)", or a "Claude Code 2.1.283" form), "" if none.
func parseClaudeVersion(out string) string {
	if m := claudeVersionRe.FindStringSubmatch(out); m != nil {
		return m[1]
	}
	return ""
}

// claudeVersion is the version `claude --version` reports, "" when it
// cannot be told (no claude, a stub, an unexpected answer).
var claudeVersion = func() string {
	path, err := exec.LookPath("claude")
	if err != nil {
		return ""
	}
	if v, ok := claudeVersions[path]; ok {
		return v
	}
	c := exec.Command(path, "--version")
	c.Dir = os.TempDir()
	out, err := c.Output()
	v := ""
	if err == nil {
		v = parseClaudeVersion(string(out))
	}
	claudeVersions[path] = v
	return v
}

var claudeVersions = map[string]string{}

// checkClaudeForPlugins refuses a plan that installs or uninstalls a plugin
// with a claude too old to run it. An unknown version is let through: the
// command itself then says what is wrong.
func checkClaudeForPlugins(steps []pluginStep) error {
	needs := false
	for _, s := range steps {
		if !s.mcp && s.skill == nil && len(s.args) > 0 && (s.args[0] == "install" || s.args[0] == "uninstall") {
			needs = true
		}
	}
	if !needs {
		return nil
	}
	v := claudeVersion()
	if v == "" {
		return nil
	}
	if slices.Compare(versionTuple(v), versionTuple(minClaudePluginJSON)) < 0 {
		return fmt.Errorf("the plugin clauses need Claude Code %s or newer (they run `claude plugin install --json`); this claude is %s: update Claude Code", minClaudePluginJSON, v)
	}
	return nil
}

// claudePlugin runs `claude plugin <args>` against one playbook: its config
// directory is the user scope, the working directory is neutral so no
// project's settings join in, and stdin is not a terminal, so Claude Code
// never prompts. It returns stdout; stderr is returned inside the error.
var claudePlugin = func(configDir string, args ...string) ([]byte, error) {
	path, err := exec.LookPath("claude")
	if err != nil {
		return nil, errors.New("claude is not on PATH: the plugin clauses run `claude plugin`")
	}
	c := exec.Command(path, append([]string{"plugin"}, args...)...)
	c.Env = append(withoutKeys(os.Environ(), []string{"CLAUDE_CONFIG_DIR"}), "CLAUDE_CONFIG_DIR="+configDir)
	c.Dir = os.TempDir()
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	if err := c.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return out.Bytes(), errors.New(msg)
	}
	return out.Bytes(), nil
}

// cliMarketplace is one entry of `claude plugin marketplace list --json`.
type cliMarketplace struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Repo   string `json:"repo"`
	URL    string `json:"url"`
	Ref    string `json:"ref"` // a git or github source's #ref, which Claude Code keeps apart from url or repo
	Path   string `json:"path"`
}

// cliPlugin is one entry of `claude plugin list --json`.
type cliPlugin struct {
	ID      string `json:"id"`
	Scope   string `json:"scope"`
	Enabled bool   `json:"enabled"`
}

// pluginWorld is a playbook's marketplaces and user-scope plugins as
// Claude Code reports them.
type pluginWorld struct {
	markets map[string]cliMarketplace
	plugins map[string]bool // id -> enabled
}

func readPluginWorld(configDir string) (*pluginWorld, error) {
	w := &pluginWorld{markets: map[string]cliMarketplace{}, plugins: map[string]bool{}}
	out, err := claudePlugin(configDir, "marketplace", "list", "--json")
	if err != nil {
		return nil, fmt.Errorf("claude plugin marketplace list: %w", err)
	}
	var ms []cliMarketplace
	if err := json.Unmarshal(out, &ms); err != nil {
		return nil, fmt.Errorf("claude plugin marketplace list: unexpected output: %w", err)
	}
	for _, m := range ms {
		w.markets[m.Name] = m
	}
	out, err = claudePlugin(configDir, "list", "--json")
	if err != nil {
		return nil, fmt.Errorf("claude plugin list: %w", err)
	}
	var ps []cliPlugin
	if err := json.Unmarshal(out, &ps); err != nil {
		return nil, fmt.Errorf("claude plugin list: unexpected output: %w", err)
	}
	for _, p := range ps {
		if p.Scope == "user" {
			w.plugins[p.ID] = p.Enabled
		}
	}
	return w, nil
}

// pluginStep is one `claude plugin` command a statement runs.
type pluginStep struct {
	args   []string
	line   string   // the report line once it ran
	market string   // ADD MARKETPLACE: the name the source must declare
	mcp    bool     // a `claude mcp` command, not `claude plugin`
	clause int      // the clause it comes from: steps run in clause order
	skill  *skillOp // a file operation under skills/, not a command
}

func (s pluginStep) command() string {
	if s.skill != nil {
		return s.skill.what
	}
	if s.mcp {
		// The server's config is not shown in full: it may be long, and its
		// placeholders never hold a secret anyway.
		return "claude mcp " + s.args[0] + " " + s.args[1] + " --scope user"
	}
	return "claude plugin " + strings.Join(s.args, " ")
}

// planPlugins turns the marketplace and plugin clauses, in the order
// written, into the commands that make them true, checked against the
// state first: what already holds runs nothing. It returns the report
// lines of clauses that need no command.
func planPlugins(w *pluginWorld, clauses []grammar.Clause) ([]pluginStep, []string, error) {
	var steps []pluginStep
	var lines []string
	for ci, c := range clauses {
		switch c.Kind {
		case grammar.AddMarketplace:
			name := c.Names[0]
			arg, want, err := marketplaceArg(c.Arg)
			if err != nil {
				return nil, nil, fmt.Errorf("ADD MARKETPLACE %s: %w", name, err)
			}
			if want.Source == grammar.SourceDirectory {
				declared, err := directoryMarketplaceName(want.Path)
				if err != nil {
					return nil, nil, fmt.Errorf("ADD MARKETPLACE %s: %w", name, err)
				}
				if declared != name {
					return nil, nil, fmt.Errorf("ADD MARKETPLACE %s: the directory declares marketplace %q, not %q", name, declared, name)
				}
			}
			if have, ok := w.markets[name]; ok {
				if sameSource(have, want) {
					continue
				}
				return nil, nil, fmt.Errorf("ADD MARKETPLACE %s: already declared from another source; DROP MARKETPLACE %s first", name, name)
			}
			w.markets[name] = want
			steps = append(steps, pluginStep{clause: ci, args: []string{"marketplace", "add", arg, "--scope", "user"},
				line: "marketplace " + name + " from " + c.Arg, market: name})
		case grammar.DropMarketplace:
			name := c.Names[0]
			if _, ok := w.markets[name]; !ok {
				lines = append(lines, "marketplace "+name+" was not declared")
				continue
			}
			// Claude Code would uninstall them silently; a playbook file says so.
			var users []string
			for id := range w.plugins {
				if _, m, _ := grammar.PluginID(id); m == name {
					users = append(users, id)
				}
			}
			if len(users) > 0 {
				slices.Sort(users)
				return nil, nil, fmt.Errorf("DROP MARKETPLACE %s: plugins still use it: %s; drop them first with DROP PLUGIN", name, strings.Join(users, ", "))
			}
			delete(w.markets, name)
			steps = append(steps, pluginStep{clause: ci, args: []string{"marketplace", "remove", name, "--scope", "user"},
				line: "dropped   marketplace " + name})
		case grammar.AddPlugin:
			id := c.Names[0]
			_, m, _ := grammar.PluginID(id)
			if _, ok := w.markets[m]; !ok {
				return nil, nil, fmt.Errorf("ADD PLUGIN %s: marketplace %s is not declared in this playbook: ADD MARKETPLACE %s FROM '<source>' first", id, m, m)
			}
			if w.plugins[id] {
				continue
			}
			w.plugins[id] = true
			steps = append(steps, pluginStep{clause: ci, args: []string{"install", id, "--scope", "user", "--json"}, line: "plugin    " + id})
		case grammar.DropPlugin:
			id := c.Names[0]
			if _, ok := w.plugins[id]; !ok {
				lines = append(lines, "plugin "+id+" was not installed")
				continue
			}
			delete(w.plugins, id)
			// --keep-data: dropping a plugin detaches it; its saved data
			// stays, as a detached env set stays.
			steps = append(steps, pluginStep{clause: ci, args: []string{"uninstall", id, "--scope", "user", "--keep-data", "--json"},
				line: "dropped   plugin " + id})
		}
	}
	return steps, lines, nil
}

// runPluginSteps runs the planned commands in order and stops at the first
// failure, saying what ran before it. There is no rollback: every step is
// safe to repeat, so running the statement again finishes it.
func runPluginSteps(configDir string, steps []pluginStep) ([]string, error) {
	var lines []string
	for i, s := range steps {
		var before map[string]bool
		if s.market != "" {
			before = marketNames(configDir)
		}
		var out []byte
		var err error
		if s.skill != nil {
			if err = s.skill.do(); err == nil && s.skill.record != nil {
				err = s.skill.record()
			}
		} else if s.mcp {
			if _, err = claudeMCP(configDir, s.args...); err != nil {
				err = fmt.Errorf("%s: %w", s.command(), err)
			}
		} else {
			out, err = claudePlugin(configDir, s.args...)
			if res := parseResult(out); res != nil && err == nil && res.Outcome != "" && res.Outcome != "ok" {
				err = errors.New(res.Message)
			}
			if err != nil {
				err = stepError(configDir, s, out, err)
			}
		}
		if err != nil {
			if i > 0 {
				ran := make([]string, i)
				for j := range ran {
					ran[j] = steps[j].command()
				}
				err = fmt.Errorf("%w\nalready run: %s; run the statement again to finish (it is safe to repeat)", err, strings.Join(ran, "; "))
			}
			return lines, err
		}
		if s.market != "" {
			// The marketplace's name is the source's to declare; the
			// statement named one, and a different one is not kept.
			after := marketNames(configDir)
			if !after[s.market] {
				var added []string
				for n := range after {
					if !before[n] {
						added = append(added, n)
						_, _ = claudePlugin(configDir, "marketplace", "remove", n, "--scope", "user")
					}
				}
				return lines, fmt.Errorf("ADD MARKETPLACE %s: the source declares %s, not %s; nothing was kept", s.market, strings.Join(added, ", "), s.market)
			}
		}
		lines = append(lines, s.line)
	}
	return lines, nil
}

// cliResult is the one line `claude plugin … --json` prints.
type cliResult struct {
	Outcome      string          `json:"outcome"`
	Message      string          `json:"message"`
	ShownCommand json.RawMessage `json:"shownCommand"`
}

func parseResult(out []byte) *cliResult {
	line := bytes.TrimSpace(out)
	if i := bytes.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	var r cliResult
	if json.Unmarshal(line, &r) != nil {
		return nil
	}
	return &r
}

// stepError explains a failed command. A marketplace-declared command (a
// plugin installed by running one) is never accepted for the pilot: it is
// shown, and confirming it is the pilot's own step.
func stepError(configDir string, s pluginStep, out []byte, err error) error {
	if res := parseResult(out); res != nil {
		if len(res.ShownCommand) > 0 && string(res.ShownCommand) != "null" {
			var sha struct {
				SHA256 string `json:"sha256"`
			}
			_ = json.Unmarshal(res.ShownCommand, &sha)
			return fmt.Errorf("%s: the plugin runs a command its marketplace declares, which cpb never accepts for you:\n  %s\nreview it, then confirm by hand:\n  CLAUDE_CONFIG_DIR=%s claude plugin install %s --accept-command %s",
				s.command(), string(res.ShownCommand), configDir, s.args[1], sha.SHA256)
		}
		if res.Message != "" {
			return fmt.Errorf("%s: %s", s.command(), res.Message)
		}
	}
	return fmt.Errorf("%s: %w", s.command(), err)
}

func marketNames(configDir string) map[string]bool {
	names := map[string]bool{}
	out, err := claudePlugin(configDir, "marketplace", "list", "--json")
	if err != nil {
		return names
	}
	var ms []cliMarketplace
	_ = json.Unmarshal(out, &ms)
	for _, m := range ms {
		names[m.Name] = true
	}
	return names
}

// marketplaceArg is the source as `claude plugin marketplace add` takes it,
// and the entry it should produce. A '~/' directory is expanded here.
func marketplaceArg(src string) (string, cliMarketplace, error) {
	kind, err := grammar.MarketplaceSource(src)
	if err != nil {
		return "", cliMarketplace{}, err
	}
	switch kind {
	case grammar.SourceGitHub:
		// Claude Code takes owner/repo#ref and records repo and ref apart,
		// as for a git URL; '@' is spelled '#' for it.
		repo, ref, err := grammar.GitHubSource(src)
		if err != nil {
			return "", cliMarketplace{}, err
		}
		arg := repo
		if ref != "" {
			arg += "#" + ref
		}
		return arg, cliMarketplace{Source: kind, Repo: repo, Ref: ref}, nil
	case grammar.SourceGit:
		// Claude Code takes url#ref and records url and ref apart; compare
		// the same way, or an unchanged source reads as another one.
		u, ref, _ := strings.Cut(src, "#")
		return src, cliMarketplace{Source: kind, URL: u, Ref: ref}, nil
	}
	path := src
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", cliMarketplace{}, err
		}
		path = filepath.Join(home, path[2:])
	}
	path = filepath.Clean(path)
	return path, cliMarketplace{Source: kind, Path: path}, nil
}

func sameSource(a, b cliMarketplace) bool {
	return a.Source == b.Source && a.Repo == b.Repo && a.URL == b.URL && a.Ref == b.Ref &&
		filepath.Clean(a.Path) == filepath.Clean(b.Path)
}

// directoryMarketplaceName reads the name a local marketplace declares, so
// a mistyped path or name fails before anything runs.
func directoryMarketplaceName(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, ".claude-plugin", "marketplace.json"))
	if err != nil {
		return "", fmt.Errorf("%s is not a marketplace: it has no readable .claude-plugin/marketplace.json", dir)
	}
	var m struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &m); err != nil || m.Name == "" {
		return "", fmt.Errorf("%s/.claude-plugin/marketplace.json names no marketplace", dir)
	}
	return m.Name, nil
}

// applySettings applies the clauses that are one key of the playbook's
// settings.json (Claude Code has no CLI for them): the agent, tool
// permissions, the status line and the model. It reports whether anything
// changed; every other key, and the order of keys, is kept.
func applySettings(f *settings.File, clauses []grammar.Clause) ([]string, bool, error) {
	var lines []string
	changed := false
	setString := func(key, value, label string) error {
		var cur string
		if ok, _ := f.Root.Get(key, &cur); ok && cur == value {
			return nil
		}
		if err := f.Root.Set(key, value); err != nil {
			return err
		}
		changed = true
		lines = append(lines, label+value)
		return nil
	}
	unset := func(key, label string) {
		if f.Root.Delete(key) {
			changed = true
			lines = append(lines, "unset     "+label)
		}
	}
	for _, c := range clauses {
		switch c.Kind {
		case grammar.SetAgent:
			if err := setString(keyAgent, c.Arg, "agent     "); err != nil {
				return nil, false, err
			}
		case grammar.UnsetAgent:
			unset(keyAgent, "agent")
		case grammar.SetModel:
			if err := setString(keyModel, c.Arg, "model     "); err != nil {
				return nil, false, err
			}
		case grammar.UnsetModel:
			unset(keyModel, "model")
		case grammar.SetStatusline:
			// IF UNSET applies only where no status line is set yet: a
			// recipe that offers a bar leaves the one you chose alone.
			if c.IfUnset && f.Root.Has(keyStatusline) {
				continue
			}
			sl, err := f.Root.Object(keyStatusline)
			if err != nil {
				return nil, false, err
			}
			var typ, cmd string
			var refresh int
			_, _ = sl.Get("type", &typ)
			_, _ = sl.Get("command", &cmd)
			_, _ = sl.Get(keyRefreshInterval, &refresh)
			// Without REFRESH, an existing refreshInterval is kept, as
			// padding and every other field are.
			if typ == "command" && cmd == c.Arg && (c.Refresh == 0 || refresh == c.Refresh) {
				continue
			}
			_ = sl.Set("type", "command")
			_ = sl.Set("command", c.Arg)
			line := "statusline " + c.Arg
			if c.Refresh > 0 {
				_ = sl.Set(keyRefreshInterval, c.Refresh)
				line += fmt.Sprintf(" (refreshes every %d s)", c.Refresh)
			}
			f.Root.SetObject(keyStatusline, sl)
			changed = true
			lines = append(lines, line)
		case grammar.SetStatuslineRefresh:
			sl, err := f.Root.Object(keyStatusline)
			if err != nil {
				return nil, false, err
			}
			var typ, cmd string
			var refresh int
			_, _ = sl.Get("type", &typ)
			_, _ = sl.Get("command", &cmd)
			_, _ = sl.Get(keyRefreshInterval, &refresh)
			if typ != "command" || cmd == "" {
				return nil, false, fmt.Errorf("SET STATUSLINE REFRESH needs a status line: SET STATUSLINE '<cmd>' REFRESH %d", c.Refresh)
			}
			if refresh == c.Refresh {
				continue
			}
			_ = sl.Set(keyRefreshInterval, c.Refresh)
			f.Root.SetObject(keyStatusline, sl)
			changed = true
			lines = append(lines, fmt.Sprintf("statusline refreshes every %d s", c.Refresh))
		case grammar.UnsetStatuslineRefresh:
			sl, err := f.Root.Object(keyStatusline)
			if err != nil {
				return nil, false, err
			}
			if sl.Delete(keyRefreshInterval) {
				f.Root.SetObject(keyStatusline, sl)
				changed = true
				lines = append(lines, "unset     statusline refresh")
			}
		case grammar.UnsetStatusline:
			unset(keyStatusline, "statusline")
		case grammar.AddModel, grammar.DropModel, grammar.SetModelPicker, grammar.UnsetModelPicker:
			l, ch, err := applyPicker(f.Root, c)
			if err != nil {
				return nil, false, err
			}
			lines = append(lines, l...)
			changed = changed || ch
		case grammar.AllowTool, grammar.DenyTool, grammar.UnsetTool:
			perms, err := f.Root.Object(keyPermissions)
			if err != nil {
				return nil, false, err
			}
			var allow, deny []string
			if _, err := perms.Get("allow", &allow); err != nil {
				return nil, false, fmt.Errorf("%s.allow: %w", keyPermissions, err)
			}
			if _, err := perms.Get("deny", &deny); err != nil {
				return nil, false, fmt.Errorf("%s.deny: %w", keyPermissions, err)
			}
			a0, d0 := slices.Clone(allow), slices.Clone(deny)
			for _, r := range c.Names {
				allow = slices.DeleteFunc(allow, func(x string) bool { return x == r && c.Kind != grammar.AllowTool })
				deny = slices.DeleteFunc(deny, func(x string) bool { return x == r && c.Kind != grammar.DenyTool })
				switch c.Kind {
				case grammar.AllowTool:
					if !slices.Contains(allow, r) {
						allow = append(allow, r)
						lines = append(lines, "allow     "+r)
					}
				case grammar.DenyTool:
					if !slices.Contains(deny, r) {
						deny = append(deny, r)
						lines = append(lines, "deny      "+r)
					}
				default:
					if slices.Contains(a0, r) || slices.Contains(d0, r) {
						lines = append(lines, "unset     tool "+r)
					}
				}
			}
			if slices.Equal(a0, allow) && slices.Equal(d0, deny) {
				continue
			}
			setList := func(key string, v []string) {
				if len(v) == 0 {
					perms.Delete(key)
				} else {
					_ = perms.Set(key, v)
				}
			}
			setList("allow", allow)
			setList("deny", deny)
			f.Root.SetObject(keyPermissions, perms)
			changed = true
		}
	}
	return lines, changed, nil
}

// pluginState reads what a settings.json declares: marketplaces and plugins
// in file order, and the agent (nil when unset).
func pluginState(root *settings.Object) ([]marketplaceJSON, []pluginJSON, *string, error) {
	mps := []marketplaceJSON{}
	plugins := []pluginJSON{}
	m, err := root.Object(keyMarketplaces)
	if err != nil {
		return nil, nil, nil, err
	}
	for _, name := range m.Keys() {
		entry, err := m.Object(name)
		if err != nil {
			return nil, nil, nil, err
		}
		src := entry.Raw("source")
		if src == nil {
			src = json.RawMessage("null")
		}
		mps = append(mps, marketplaceJSON{Name: name, Source: src})
	}
	p, err := root.Object(keyPlugins)
	if err != nil {
		return nil, nil, nil, err
	}
	for _, id := range p.Keys() {
		var on any
		_, _ = p.Get(id, &on)
		plugins = append(plugins, pluginJSON{ID: id, Enabled: on == true})
	}
	var agent *string
	var a string
	if ok, err := root.Get(keyAgent, &a); err != nil {
		return nil, nil, nil, fmt.Errorf("%s: %w", keyAgent, err)
	} else if ok {
		agent = &a
	}
	return mps, plugins, agent, nil
}

// sourceString turns a stored source object back into the FROM of ADD
// MARKETPLACE; false when the grammar cannot write it (a field it does not
// know, or a source type it does not write).
func sourceString(raw json.RawMessage) (string, bool) {
	o, err := settings.ParseObject(raw)
	if err != nil {
		return "", false
	}
	var kind string
	if ok, err := o.Get("source", &kind); !ok || err != nil {
		return "", false
	}
	field := map[string]string{grammar.SourceGitHub: "repo", grammar.SourceGit: "url", grammar.SourceDirectory: "path"}[kind]
	keys := o.Keys()
	// A git or github source may carry its #ref apart from the url or
	// repo, as Claude Code records it; it is written back as url#ref, or
	// github:repo#ref.
	var ref string
	if (kind == grammar.SourceGit || kind == grammar.SourceGitHub) && slices.Contains(keys, "ref") {
		if ok, err := o.Get("ref", &ref); !ok || err != nil || ref == "" {
			return "", false
		}
		keys = slices.DeleteFunc(slices.Clone(keys), func(k string) bool { return k == "ref" })
	}
	if field == "" || !slices.Equal(keys, []string{"source", field}) {
		return "", false
	}
	var v string
	if ok, err := o.Get(field, &v); !ok || err != nil {
		return "", false
	}
	if kind == grammar.SourceGit {
		// A '#' in the url itself would read back as a ref.
		if strings.Contains(v, "#") {
			return "", false
		}
		if ref != "" {
			v += "#" + ref
		}
	}
	if kind == grammar.SourceGitHub {
		if strings.ContainsAny(v, "#@") {
			return "", false
		}
		v = "github:" + v
		if ref != "" {
			v += "#" + ref
		}
	}
	if _, err := grammar.MarketplaceSource(v); err != nil {
		return "", false
	}
	return v, true
}

// pluginCreateBlock is the ALTER PLAYBOOK that SHOW CREATE writes for a
// playbook's plugins and agent, and comments for what the grammar does not
// write. Empty when the settings declare none.
func pluginCreateBlock(name string, root *settings.Object) (string, error) {
	mps, plugins, agent, err := pluginState(root)
	if err != nil {
		return "", err
	}
	alter := &grammar.Stmt{Verb: grammar.Alter, Object: grammar.Playbook, Name: name}
	var comments []string
	written := map[string]bool{}
	for _, m := range mps {
		src, ok := sourceString(m.Source)
		if !ok || manifest.ValidateSetName(m.Name) != nil {
			comments = append(comments, fmt.Sprintf("-- MARKETPLACE %s has a source the grammar does not write; not written", m.Name))
			continue
		}
		written[m.Name] = true
		alter.Clauses = append(alter.Clauses, grammar.Clause{Kind: grammar.AddMarketplace, Names: []string{m.Name}, Arg: src})
	}
	for _, p := range plugins {
		if !p.Enabled {
			comments = append(comments, fmt.Sprintf("-- PLUGIN %s is false in %s; not written", p.ID, settings.FileName))
			continue
		}
		_, m, ok := grammar.PluginID(p.ID)
		if !ok {
			comments = append(comments, fmt.Sprintf("-- PLUGIN %s is not a <plugin>@<marketplace> id; not written", p.ID))
			continue
		}
		// Its ADD PLUGIN would fail without the marketplace it names.
		if !written[m] {
			comments = append(comments, fmt.Sprintf("-- PLUGIN %s: its marketplace %s is not written; not written", p.ID, m))
			continue
		}
		alter.Clauses = append(alter.Clauses, grammar.Clause{Kind: grammar.AddPlugin, Names: []string{p.ID}})
	}
	switch {
	case agent == nil:
	case grammar.ValidAgent(*agent):
		alter.Clauses = append(alter.Clauses, grammar.Clause{Kind: grammar.SetAgent, Arg: *agent})
	default:
		comments = append(comments, "-- the agent in "+settings.FileName+" is not a name the grammar writes; not written")
	}
	tools, statusline, model := settingsExtras(root)
	if len(tools.Allow) > 0 {
		alter.Clauses = append(alter.Clauses, grammar.Clause{Kind: grammar.AllowTool, Names: tools.Allow})
	}
	if len(tools.Deny) > 0 {
		alter.Clauses = append(alter.Clauses, grammar.Clause{Kind: grammar.DenyTool, Names: tools.Deny})
	}
	if statusline != nil {
		c := grammar.Clause{Kind: grammar.SetStatusline, Arg: *statusline}
		if r := statuslineRefresh(root); r != nil {
			c.Refresh = *r
		}
		alter.Clauses = append(alter.Clauses, c)
	}
	if model != nil && *model != "" && !strings.ContainsAny(*model, " \t\r\n") {
		alter.Clauses = append(alter.Clauses, grammar.Clause{Kind: grammar.SetModel, Arg: *model})
	}
	picker, pickerComments := pickerCreateClauses(root)
	alter.Clauses = append(alter.Clauses, picker...)
	comments = append(comments, pickerComments...)
	text := strings.Join(comments, "\n")
	if len(alter.Clauses) > 0 {
		if text != "" {
			text += "\n"
		}
		text += alter.Pretty() + ";"
	}
	return text, nil
}

// toolsJSON is a playbook's tool permissions as SHOW prints them.
type toolsJSON struct {
	Allow []string `json:"allow"`
	Deny  []string `json:"deny"`
}

// settingsExtras reads the keys cpb writes besides the agent: tool
// permissions, the status line command (nil unless it is a command status
// line) and the model.
func settingsExtras(root *settings.Object) (toolsJSON, *string, *string) {
	t := toolsJSON{Allow: []string{}, Deny: []string{}}
	if perms, err := root.Object(keyPermissions); err == nil {
		_, _ = perms.Get("allow", &t.Allow)
		_, _ = perms.Get("deny", &t.Deny)
		if t.Allow == nil {
			t.Allow = []string{}
		}
		if t.Deny == nil {
			t.Deny = []string{}
		}
	}
	var statusline *string
	if sl, err := root.Object(keyStatusline); err == nil {
		var typ, cmd string
		_, _ = sl.Get("type", &typ)
		_, _ = sl.Get("command", &cmd)
		if typ == "command" && cmd != "" {
			statusline = &cmd
		}
	}
	var model *string
	var m string
	if ok, err := root.Get(keyModel, &m); ok && err == nil {
		model = &m
	}
	return t, statusline, model
}

// statuslineRefresh is statusLine.refreshInterval when it is a whole number
// of seconds, at least 1, as REFRESH writes it; nil otherwise.
func statuslineRefresh(root *settings.Object) *int {
	sl, err := root.Object(keyStatusline)
	if err != nil {
		return nil
	}
	var n int
	if ok, err := sl.Get(keyRefreshInterval, &n); !ok || err != nil || n < 1 {
		return nil
	}
	return &n
}

// warnMarketplaceRef warns about an ADD MARKETPLACE git source (https:// or
// git@) whose #ref looks like a commit (v3.27.0). Claude Code clones a
// marketplace by branch or tag only, so the source is written and then
// fails to clone. The github: form refuses such a ref; a git source took
// one before v3.27.0, so it is warned about rather than refused, which the
// stability rules allow in a minor release. The message names the
// marketplace and the ref, never the URL.
func (r *stmtRun) warnMarketplaceRef(clauses []grammar.Clause) {
	for _, c := range clauses {
		if c.Kind != grammar.AddMarketplace || len(c.Names) == 0 {
			continue
		}
		if kind, err := grammar.MarketplaceSource(c.Arg); err != nil || kind != grammar.SourceGit {
			continue
		}
		_, ref, _ := strings.Cut(c.Arg, "#")
		if !grammar.LooksLikeCommit(ref) {
			continue
		}
		msg := "MARKETPLACE " + c.Names[0] + " #" + ref + ": Claude Code clones marketplaces by branch or tag; this ref looks like a commit and will not clone: use a tag at that commit"
		r.warn(warnMarketplaceRefNotCloneable, msg)
	}
}
