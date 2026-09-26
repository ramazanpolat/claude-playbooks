package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/settings"
)

// Plugins and the agent (docs/reference/cli-grammar.md, "Plugins and the
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
			grammar.DropPlugin, grammar.SetAgent, grammar.UnsetAgent:
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
	line   string // the report line once it ran
	market string // ADD MARKETPLACE: the name the source must declare
}

func (s pluginStep) command() string { return "claude plugin " + strings.Join(s.args, " ") }

// planPlugins turns the marketplace and plugin clauses, in the order
// written, into the commands that make them true, checked against the
// state first: what already holds runs nothing. It returns the report
// lines of clauses that need no command.
func planPlugins(w *pluginWorld, clauses []grammar.Clause) ([]pluginStep, []string, error) {
	var steps []pluginStep
	var lines []string
	for _, c := range clauses {
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
			steps = append(steps, pluginStep{args: []string{"marketplace", "add", arg, "--scope", "user"},
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
			steps = append(steps, pluginStep{args: []string{"marketplace", "remove", name, "--scope", "user"},
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
			steps = append(steps, pluginStep{args: []string{"install", id, "--scope", "user", "--json"}, line: "plugin    " + id})
		case grammar.DropPlugin:
			id := c.Names[0]
			if _, ok := w.plugins[id]; !ok {
				lines = append(lines, "plugin "+id+" was not installed")
				continue
			}
			delete(w.plugins, id)
			// --keep-data: dropping a plugin detaches it; its saved data
			// stays, as a detached env set stays.
			steps = append(steps, pluginStep{args: []string{"uninstall", id, "--scope", "user", "--keep-data", "--json"},
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
		out, err := claudePlugin(configDir, s.args...)
		if res := parseResult(out); res != nil && err == nil && res.Outcome != "" && res.Outcome != "ok" {
			err = errors.New(res.Message)
		}
		if err != nil {
			err = stepError(configDir, s, out, err)
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
		repo := strings.TrimPrefix(src, "github:")
		return repo, cliMarketplace{Source: kind, Repo: repo}, nil
	case grammar.SourceGit:
		return src, cliMarketplace{Source: kind, URL: src}, nil
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
	return a.Source == b.Source && a.Repo == b.Repo && a.URL == b.URL &&
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

// applyAgent applies SET AGENT / UNSET AGENT to a loaded settings.json and
// reports whether it changed.
func applyAgent(f *settings.File, clauses []grammar.Clause) ([]string, bool, error) {
	var lines []string
	changed := false
	for _, c := range clauses {
		switch c.Kind {
		case grammar.SetAgent:
			var cur string
			if ok, _ := f.Root.Get(keyAgent, &cur); ok && cur == c.Arg {
				continue
			}
			if err := f.Root.Set(keyAgent, c.Arg); err != nil {
				return nil, false, err
			}
			changed = true
			lines = append(lines, "agent     "+c.Arg)
		case grammar.UnsetAgent:
			if f.Root.Delete(keyAgent) {
				changed = true
				lines = append(lines, "unset     agent")
			}
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
	if field == "" || !slices.Equal(o.Keys(), []string{"source", field}) {
		return "", false
	}
	var v string
	if ok, err := o.Get(field, &v); !ok || err != nil {
		return "", false
	}
	if kind == grammar.SourceGitHub {
		v = "github:" + v
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
		if !ok || manifest.ValidateProfileName(m.Name) != nil {
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
	text := strings.Join(comments, "\n")
	if len(alter.Clauses) > 0 {
		if text != "" {
			text += "\n"
		}
		text += alter.Pretty() + ";"
	}
	return text, nil
}
