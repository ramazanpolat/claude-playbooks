package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/settings"
)

// Panels (v3.25.0): ADD PANEL writes one SPC/1 manifest (agent-realm/
// statusmux, docs/spc-1.md) at <config dir>/statusline.d/<ns>/<id>.toml, and
// DROP PANEL removes it. cpb writes manifests only: never the pilot's layout
// file (statusline.toml, which SPC/1 section 6 reserves for the pilot), and
// never a plugin's or a project's panels. A manifest cpb wrote starts with
// panelMarker; one without it is the pilot's, and cpb neither overwrites nor
// removes it.

const (
	panelsDirName = "statusline.d"
	panelMarker   = "# Written by cpb (ADD PANEL). DROP PANEL removes it; without this line cpb leaves the file to you."
)

// panelManifest is an SPC/1 manifest as cpb writes it, fields in SPC/1's
// order.
type panelManifest struct {
	Contract int    `toml:"contract"`
	ID       string `toml:"id"`
	Type     string `toml:"type"`
	Row      *int   `toml:"row,omitempty"`
	Priority *int   `toml:"priority,omitempty"`
	Align    string `toml:"align,omitempty"`
	Command  string `toml:"command,omitempty"`
	Format   string `toml:"format,omitempty"`
	Timeout  *int   `toml:"timeout_ms,omitempty"`
	MaxRun   *int   `toml:"max_run_ms,omitempty"`
	TTL      *int   `toml:"ttl_ms,omitempty"`
	Stale    *int   `toml:"stale_ms,omitempty"`
	Width    *int   `toml:"max_width,omitempty"`
	Every    *int   `toml:"every_ms,omitempty"`
	Text     string `toml:"text,omitempty"`
	When     string `toml:"when,omitempty"`
	Path     string `toml:"path,omitempty"`
}

func panelPath(cfg string, pn *grammar.Panel) string {
	return filepath.Join(cfg, panelsDirName, pn.NS, pn.ID+".toml")
}

// panelBytes is the manifest cpb writes for a panel.
func panelBytes(pn *grammar.Panel) ([]byte, error) {
	m := panelManifest{Contract: 1, ID: pn.ID, Type: pn.Type, Row: pn.Row, Priority: pn.Priority, Align: pn.Align,
		Format: pn.Format, Timeout: pn.Timeout, MaxRun: pn.MaxRun, TTL: pn.TTL, Stale: pn.Stale, Width: pn.Width,
		Every: pn.Every, When: pn.When}
	switch pn.Type {
	case "exec", "observe":
		m.Command = pn.Source
	case "template":
		m.Text = pn.Source
	case "records":
		m.Path = pn.Source
	}
	var b bytes.Buffer
	b.WriteString(panelMarker + "\n")
	if err := toml.NewEncoder(&b).Encode(m); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// panelFromManifest reads a cpb-written manifest back into the clause that
// writes it (SHOW CREATE).
func panelFromManifest(ns string, data []byte) (*grammar.Panel, error) {
	var m panelManifest
	if _, err := toml.Decode(string(data), &m); err != nil {
		return nil, err
	}
	pn := &grammar.Panel{NS: ns, ID: m.ID, Type: m.Type, Row: m.Row, Priority: m.Priority, Align: m.Align,
		Format: m.Format, Timeout: m.Timeout, MaxRun: m.MaxRun, TTL: m.TTL, Stale: m.Stale, Width: m.Width,
		Every: m.Every, When: m.When}
	switch m.Type {
	case "exec", "observe":
		pn.Source = m.Command
	case "template":
		pn.Source = m.Text
	case "records":
		pn.Source = m.Path
	}
	return pn, nil
}

// panelOp is one planned manifest write (data) or removal (nil).
type panelOp struct {
	path string
	data []byte
}

// panelFile is a manifest as the run sees it: its bytes, or nil when absent.
// onDisk false: the directory does not exist yet (a playbook created earlier
// in a dry run), so only what earlier statements would write is there.
func (r *stmtRun) panelFile(key, path string, onDisk bool) ([]byte, error) {
	if r.dry != nil {
		if files, ok := r.dry.panels[key]; ok {
			if data, ok := files[path]; ok {
				return data, nil
			}
		}
	}
	if !onDisk {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return data, err
}

// planPanels decides a statement's ADD and DROP PANEL clauses against the
// config directory cfg ("" for a playbook created earlier in a dry run):
// what it would write and remove, and the lines that report it. root is
// the directory's settings.json, for ADD PANEL … FROM STATUSLINE.
func (r *stmtRun) planPanels(key, cfg string, root *settings.Object, clauses []grammar.Clause) ([]panelOp, []string, error) {
	var ops []panelOp
	var lines []string
	base := cfg
	if base == "" {
		base = "/dev/null/" + key // a playbook not on disk yet: nothing to read
	}
	for _, c := range clauses {
		if c.Kind != grammar.AddPanel && c.Kind != grammar.DropPanel {
			continue
		}
		pn := *c.Panel
		name := pn.NS + "." + pn.ID
		path := panelPath(base, &pn)
		cur, err := r.panelFile(key, path, cfg != "")
		if err != nil {
			return nil, nil, err
		}
		ours := cur == nil || strings.HasPrefix(string(cur), panelMarker)
		if c.Kind == grammar.DropPanel {
			if cur == nil {
				continue
			}
			if !ours {
				return nil, nil, fmt.Errorf("DROP PANEL %s: %s was not written by cpb; remove it by hand if you mean to", name, filepath.Join(panelsDirName, pn.NS, pn.ID+".toml"))
			}
			ops = append(ops, panelOp{path: path})
			lines = append(lines, "panel     - "+name)
			continue
		}
		if pn.FromStatusline {
			var typ, cmd string
			if sl, err := root.Object(keyStatusline); err == nil {
				_, _ = sl.Get("type", &typ)
				_, _ = sl.Get("command", &cmd)
			}
			switch {
			case typ != "command" || cmd == "":
				return nil, nil, fmt.Errorf("ADD PANEL %s FROM STATUSLINE: there is no status line command to turn into a panel", name)
			case isHostCommand(cmd):
				return nil, nil, fmt.Errorf("ADD PANEL %s FROM STATUSLINE: the status line is the host itself (%s), not a bar to adopt", name, cmd)
			}
			pn.Source, pn.FromStatusline = cmd, false
		}
		if !ours {
			return nil, nil, fmt.Errorf("ADD PANEL %s: %s exists and was not written by cpb; cpb leaves it to you", name, filepath.Join(panelsDirName, pn.NS, pn.ID+".toml"))
		}
		data, err := panelBytes(&pn)
		if err != nil {
			return nil, nil, err
		}
		if bytes.Equal(cur, data) {
			continue
		}
		ops = append(ops, panelOp{path: path, data: data})
		lines = append(lines, "panel     + "+name+" ("+pn.Type+")")
	}
	return ops, lines, nil
}

// applyPanels carries the planned writes and removals out: kept in a dry
// run, written (0644, atomically) otherwise.
func (r *stmtRun) applyPanels(key string, ops []panelOp) error {
	if r.dry != nil {
		if r.dry.panels[key] == nil {
			r.dry.panels[key] = map[string][]byte{}
		}
		for _, op := range ops {
			r.dry.panels[key][op.path] = op.data
		}
		if r.dryRun {
			return nil
		}
	}
	for _, op := range ops {
		if op.data == nil {
			if err := os.Remove(op.path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			_ = os.Remove(filepath.Dir(op.path)) // the namespace directory, once empty
			continue
		}
		if err := os.MkdirAll(filepath.Dir(op.path), 0o755); err != nil {
			return err
		}
		if err := settings.WriteAtomic(op.path, op.data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// panelJSON is one panel as SHOW --json and SELECT list it.
type panelJSON struct {
	Panel    string `json:"panel"`  // <namespace>.<id>
	Type     string `json:"type"`   // exec, template, records, observe
	Source   string `json:"source"` // "config" or "plugin <name@marketplace>"
	Cpb      bool   `json:"cpb"`    // written by cpb (ADD PANEL)
	Row      *int   `json:"row"`
	Priority *int   `json:"priority"`
	Align    string `json:"align"`
}

// describePanels lists a config directory's panels: its own
// statusline.d/<ns>/*.toml, then enabled plugins' statusline/*.toml (read
// only, as SPC/1 section 2 discovers them). A manifest that does not parse
// is left out; the host reports those.
func describePanels(cfg string) []panelJSON {
	out := []panelJSON{}
	files, _ := filepath.Glob(filepath.Join(cfg, panelsDirName, "*", "*.toml"))
	sort.Strings(files)
	taken := map[string]bool{}
	for _, f := range files {
		ns := filepath.Base(filepath.Dir(f))
		taken[ns] = true
		if p, ok := readPanel(f, ns, "config"); ok {
			out = append(out, p)
		}
	}
	for _, pl := range enabledPluginPanelDirs(cfg) {
		ns := pl.ns
		if taken[ns] || ns == "local" || ns == "project" {
			ns = panelNamespace(strings.Replace(pl.key, "@", "--", 1))
		}
		taken[ns] = true
		pfiles, _ := filepath.Glob(filepath.Join(pl.dir, "*.toml"))
		sort.Strings(pfiles)
		for _, f := range pfiles {
			if p, ok := readPanel(f, ns, "plugin "+pl.key); ok {
				out = append(out, p)
			}
		}
	}
	return out
}

func readPanel(file, ns, source string) (panelJSON, bool) {
	data, err := os.ReadFile(file)
	if err != nil {
		return panelJSON{}, false
	}
	var m panelManifest
	if _, err := toml.Decode(string(data), &m); err != nil || m.ID == "" || m.Type == "" {
		return panelJSON{}, false
	}
	return panelJSON{Panel: ns + "." + m.ID, Type: m.Type, Source: source,
		Cpb: strings.HasPrefix(string(data), panelMarker), Row: m.Row, Priority: m.Priority, Align: m.Align}, true
}

type pluginPanelDir struct{ key, ns, dir string }

// enabledPluginPanelDirs finds the statusline/ directory of every plugin
// enabled in this config directory and installed in user scope, from Claude
// Code's own records (plugins/installed_plugins.json, enabledPlugins), never
// by globbing the plugin cache.
func enabledPluginPanelDirs(cfg string) []pluginPanelDir {
	enabled := map[string]bool{}
	for _, name := range []string{settings.FileName, "settings.local.json"} {
		data, err := os.ReadFile(filepath.Join(cfg, name))
		if err != nil {
			continue
		}
		var s struct {
			EnabledPlugins map[string]bool `json:"enabledPlugins"`
		}
		if json.Unmarshal(data, &s) == nil {
			for k, v := range s.EnabledPlugins {
				enabled[k] = v
			}
		}
	}
	data, err := os.ReadFile(filepath.Join(cfg, "plugins", "installed_plugins.json"))
	if err != nil {
		return nil
	}
	var inst struct {
		Plugins map[string][]struct {
			Scope       string `json:"scope"`
			InstallPath string `json:"installPath"`
		} `json:"plugins"`
	}
	if json.Unmarshal(data, &inst) != nil {
		return nil
	}
	var out []pluginPanelDir
	for key, entries := range inst.Plugins {
		if !enabled[key] {
			continue
		}
		for _, e := range entries {
			if e.Scope == "user" && e.InstallPath != "" {
				name, _, _ := strings.Cut(key, "@")
				out = append(out, pluginPanelDir{key: key, ns: panelNamespace(name), dir: filepath.Join(e.InstallPath, "statusline")})
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out
}

// panelNamespace is SPC/1's plugin namespace: lowercased, anything outside
// [a-z0-9-] replaced by "-".
func panelNamespace(s string) string {
	b := []byte(strings.ToLower(s))
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			b[i] = '-'
		}
	}
	if out := strings.TrimLeft(string(b), "-"); out != "" {
		return out
	}
	return "plugin"
}

// panelCreateClauses are the ADD PANEL clauses that rebuild the panels cpb
// wrote in a config directory (SHOW CREATE); the pilot's own and plugins'
// are not cpb's to write.
func panelCreateClauses(cfg string) []grammar.Clause {
	var out []grammar.Clause
	files, _ := filepath.Glob(filepath.Join(cfg, panelsDirName, "*", "*.toml"))
	sort.Strings(files)
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil || !strings.HasPrefix(string(data), panelMarker) {
			continue
		}
		if pn, err := panelFromManifest(filepath.Base(filepath.Dir(f)), data); err == nil && pn.ID != "" {
			out = append(out, grammar.Clause{Kind: grammar.AddPanel, Panel: pn})
		}
	}
	return out
}

// hasPanelClauses reports whether a statement adds or drops a panel.
func hasPanelClauses(clauses []grammar.Clause) bool {
	for _, c := range clauses {
		if c.Kind == grammar.AddPanel || c.Kind == grammar.DropPanel {
			return true
		}
	}
	return false
}
