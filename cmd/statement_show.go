package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envprofile"
	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
	"github.com/ramazanpolat/claude-playbooks/internal/settings"
)

// The read statements. Their --json form is a contract (docs/cli-grammar.md,
// "Output"): fields may be added, and a field never changes meaning within
// a major version. The human form may change; nothing should grep it.
// No value of a credential-looking key is ever printed, in either form.

// varJSON is one variable, with exactly one of value, ref, redacted or
// blocked.
type varJSON struct {
	Key       string     `json:"key"`
	Value     *string    `json:"value,omitempty"`
	Ref       *string    `json:"ref,omitempty"`
	Redacted  bool       `json:"redacted,omitempty"`
	Plaintext bool       `json:"plaintext,omitempty"`
	Blocked   bool       `json:"blocked,omitempty"`
	Layer     *layerJSON `json:"layer,omitempty"`
}

type layerJSON struct {
	Kind string `json:"kind"`
	Name string `json:"name,omitempty"`
}

type sourceJSON struct {
	URL    string  `json:"url"`
	Branch *string `json:"branch"`
	Subdir *string `json:"subdir"`
}

type playbookJSON struct {
	Name     string      `json:"name"`
	Version  *string     `json:"version"`
	Path     string      `json:"path"`
	Source   *sourceJSON `json:"source"`
	Linked   *string     `json:"linked"`
	Launcher *string     `json:"launcher"`
	Envs     []string    `json:"envs"`
	Vars     []varJSON   `json:"vars"`
	Sandbox  bool        `json:"sandbox"`

	Marketplaces []marketplaceJSON `json:"marketplaces"`
	Plugins      []pluginJSON      `json:"plugins"`
	Agent        *string           `json:"agent"`
}

type envJSON struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Vars        []varJSON `json:"vars"`
	UsedBy      []string  `json:"used_by"`
	Default     bool      `json:"default"`
}

type helperJSON struct {
	Command string `json:"command"`
	From    string `json:"from"`
}

type defaultsJSON struct {
	Envs         []string    `json:"envs"`
	SecretHelper *helperJSON `json:"secret_helper"`
}

type explainJSON struct {
	Playbook     string      `json:"playbook"`
	Vars         []varJSON   `json:"vars"`
	SecretHelper *helperJSON `json:"secret_helper"`
	Plugins      []string    `json:"plugins"` // enabled in the playbook's settings.json
	Agent        *agentJSON  `json:"agent"`
}

// agentJSON is the agent a launch starts as, when the playbook names one.
type agentJSON struct {
	Name string `json:"name"`
	From string `json:"from"` // "playbook settings"
}

// launchPlugins is what EXPLAIN says of plugins and the agent: the enabled
// plugins, and the agent the playbook's settings.json pins. An agent that
// only a plugin names is not resolved here (that needs the plugin's own
// files); the human form says it may exist.
func launchPlugins(pb *playbook.Playbook) ([]string, *agentJSON) {
	v := describePlaybook(pb)
	enabled := []string{}
	for _, p := range v.Plugins {
		if p.Enabled {
			enabled = append(enabled, p.ID)
		}
	}
	if v.Agent == nil {
		return enabled, nil
	}
	return enabled, &agentJSON{Name: *v.Agent, From: "playbook settings"}
}

func printLaunchPlugins(plugins []string, agent *agentJSON) {
	fmt.Printf("Plugins: %s\n", listOrNone(plugins))
	switch {
	case agent != nil:
		fmt.Printf("Agent: %s (%s)\n", agent.Name, agent.From)
	case len(plugins) > 0:
		fmt.Println("Agent: not set by the playbook (an enabled plugin may name one)")
	default:
		fmt.Println("Agent: (none)")
	}
}

func readStatement(st *grammar.Stmt) error {
	if st.Verb == grammar.Show && st.ShowCreate {
		return showCreate(st)
	}
	playbooksDir := config.ResolvePlaybooksDir()
	dir := envprofile.Dir(playbooksDir)
	switch {
	case st.Verb == grammar.Explain:
		return explainPlaybook(playbooksDir, dir, st)
	case st.Object == grammar.Playbook:
		pb, err := playbook.Require(playbooksDir, st.Name)
		if err != nil {
			return err
		}
		v := describePlaybook(pb)
		if st.JSON {
			return printJSON(v)
		}
		var values map[string]string
		if pb.Manifest != nil && pb.Manifest.Env != nil {
			values = pb.Manifest.Env.Set
		}
		printPlaybook(v, values)
		return nil
	case st.Object == grammar.Playbooks:
		pbs, err := playbook.Discover(playbooksDir)
		if err != nil {
			return err
		}
		all := make([]playbookJSON, 0, len(pbs))
		for _, pb := range pbs {
			all = append(all, describePlaybook(pb))
		}
		if st.JSON {
			return printJSON(all)
		}
		printPlaybooks(all)
		return nil
	case st.Object == grammar.Env, st.Object == grammar.Envs:
		return showEnvs(playbooksDir, dir, st)
	case st.Object == grammar.Defaults:
		names, err := envprofile.Defaults(dir)
		if err != nil {
			return fmt.Errorf("DEFAULTS cannot be read: %w", err)
		}
		helper, err := helperInEffect(dir)
		if err != nil {
			return err
		}
		v := defaultsJSON{Envs: nonNil(names), SecretHelper: helper}
		if st.JSON {
			return printJSON(v)
		}
		printLabels([][2]string{{"Env sets", listOrNone(v.Envs)}, {"Secret helper", humanHelper(helper)}})
		return nil
	}
	return fmt.Errorf("cannot run %s", st.String())
}

func describePlaybook(pb *playbook.Playbook) playbookJSON {
	v := playbookJSON{Name: pb.Name, Path: pb.Path, Envs: []string{}, Vars: []varJSON{},
		Marketplaces: []marketplaceJSON{}, Plugins: []pluginJSON{}}
	// What the playbook's settings.json declares; an unreadable file shows
	// none rather than failing the whole SHOW.
	if sf, err := settings.Load(pb.Path); err == nil {
		if mps, plugins, agent, err := pluginState(sf.Root); err == nil {
			v.Marketplaces, v.Plugins, v.Agent = mps, plugins, agent
		}
	}
	root := pb.RootPath
	if root == "" {
		root = pb.Path
	}
	if info, err := os.Lstat(root); err == nil && info.Mode()&os.ModeSymlink != 0 {
		if target, err := os.Readlink(root); err == nil {
			v.Linked = &target
		}
	}
	m := pb.Manifest
	if m == nil {
		return v
	}
	if m.Version != "" {
		v.Version = strPtr(m.Version)
	}
	if m.Alias != "" {
		v.Launcher = strPtr(m.Alias)
	}
	if m.Source != nil && m.Source.Repository != "" {
		v.Source = &sourceJSON{URL: m.Source.Repository, Branch: optStr(m.Source.Branch), Subdir: optStr(m.Source.Subdir)}
	}
	v.Sandbox = m.Sandbox != nil && m.Sandbox.Always
	if m.Env != nil {
		v.Envs = nonNil(m.Env.Profiles)
		v.Vars = layerVars(m.Env.Set, m.Env.Refs, m.Env.Unset)
	}
	return v
}

// layerVars lists one layer's own entries, sorted: literals and
// references by key, then blocked.
func layerVars(set, refs map[string]string, unset []string) []varJSON {
	vars := []varJSON{}
	keys := make([]string, 0, len(set)+len(refs))
	for k := range set {
		keys = append(keys, k)
	}
	for k := range refs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if ref, ok := refs[k]; ok {
			vars = append(vars, varJSON{Key: k, Ref: strPtr(ref)})
			continue
		}
		vars = append(vars, literalVar(k, set[k]))
	}
	blocked := append([]string(nil), unset...)
	sort.Strings(blocked)
	for _, k := range blocked {
		vars = append(vars, varJSON{Key: k, Blocked: true})
	}
	return vars
}

// literalVar is a literal value, or redacted when it may be a credential:
// a credential-looking key holding more than a plain setting, or a URL
// carrying a password. MAX_THINKING_TOKENS=8000 is shown, as the grammar
// accepts it without AS PLAINTEXT.
func literalVar(key, value string) varJSON {
	secret := manifest.LooksLikeSecretKey(key) && !manifest.PlainSetting(value)
	if secret || redactURLCredentials(value) != value {
		return varJSON{Key: key, Redacted: true, Plaintext: true}
	}
	return varJSON{Key: key, Value: strPtr(value)}
}

// humanVar renders a variable for the human form; its layer is not part
// of it.
func humanVar(v varJSON, value string) string {
	switch {
	case v.Blocked:
		return v.Key + " (blocked)"
	case v.Ref != nil:
		return v.Key + " <from " + *v.Ref + ">"
	case v.Redacted:
		shown := displayEnvValue(v.Key, value, false)
		if strings.HasSuffix(shown, " chars)") || strings.HasSuffix(shown, " chars>") {
			return v.Key + "=" + shown[:len(shown)-1] + ", plaintext" + shown[len(shown)-1:]
		}
		return v.Key + "=" + shown + " (plaintext)"
	}
	return v.Key + "=" + *v.Value
}

// printPlaybook needs the raw values only to show a credential masked.
func printPlaybook(v playbookJSON, values map[string]string) {
	source := "(none)"
	switch {
	case v.Linked != nil:
		source = "(linked) " + *v.Linked
	case v.Source != nil:
		source = v.Source.URL
		var extra []string
		if v.Source.Branch != nil {
			extra = append(extra, "branch "+*v.Source.Branch)
		}
		if v.Source.Subdir != nil {
			extra = append(extra, "subdir "+*v.Source.Subdir)
		}
		if len(extra) > 0 {
			source += " (" + strings.Join(extra, ", ") + ")"
		}
	}
	sandbox := "no"
	if v.Sandbox {
		sandbox = "yes"
	}
	rows := [][2]string{
		{"Name", v.Name},
		{"Version", deref(v.Version, "(none)")},
		{"Path", v.Path},
		{"Source", source},
		{"Launcher", deref(v.Launcher, "(none)")},
		{"Env sets", listOrNone(v.Envs)},
		{"Variables", strings.Join(humanVars(v.Vars, values), "\n")},
		{"Sandbox", sandbox},
	}
	// Shown when the playbook has any, so the rest of the layout stays as
	// it was for the playbooks that have none.
	if len(v.Marketplaces) > 0 || len(v.Plugins) > 0 || v.Agent != nil {
		rows = append(rows,
			[2]string{"Marketplaces", listOrNone(marketplaceNames(v.Marketplaces))},
			[2]string{"Plugins", listOrNone(pluginNames(v.Plugins))},
			[2]string{"Agent", deref(v.Agent, "(none)")})
	}
	printLabels(rows)
}

func marketplaceNames(mps []marketplaceJSON) []string {
	out := make([]string, 0, len(mps))
	for _, m := range mps {
		out = append(out, m.Name)
	}
	return out
}

// pluginNames lists plugin ids, marking one settings.json holds disabled.
func pluginNames(ps []pluginJSON) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		if p.Enabled {
			out = append(out, p.ID)
		} else {
			out = append(out, p.ID+" (disabled)")
		}
	}
	return out
}

func printPlaybooks(all []playbookJSON) {
	t := newTable("NAME", "VERSION", "LAUNCHER", "ENV SETS", "SOURCE").flexible(4)
	for _, v := range all {
		source := "-"
		switch {
		case v.Linked != nil:
			source = "(linked) " + *v.Linked
		case v.Source != nil:
			source = v.Source.URL
		}
		envs := strings.Join(v.Envs, ", ")
		if envs == "" {
			envs = "-"
		}
		t.add(v.Name, deref(v.Version, "-"), deref(v.Launcher, "-"), envs, source)
	}
	t.render(os.Stdout)
}

func showEnvs(playbooksDir, dir string, st *grammar.Stmt) error {
	var profiles []*envprofile.Profile
	if st.Object == grammar.Env {
		p, err := envprofile.Read(dir, st.Name)
		if err != nil {
			return err
		}
		if p == nil {
			return fmt.Errorf("no env set %q", st.Name)
		}
		profiles = []*envprofile.Profile{p}
	} else {
		var err error
		if profiles, err = envprofile.List(dir); err != nil {
			return err
		}
	}
	users, err := profileUsers(playbooksDir)
	if err != nil {
		return err
	}
	defaults, derr := envprofile.Defaults(dir)
	all := make([]envJSON, 0, len(profiles))
	for _, p := range profiles {
		all = append(all, envJSON{
			Name: p.Name, Description: p.Description,
			Vars: layerVars(p.Set, p.Refs, p.Unset), UsedBy: nonNil(users[p.Name]),
			Default: isRegistryDefault(dir, defaults, p.Name),
		})
	}
	if st.JSON {
		if st.Object == grammar.Env {
			return printJSON(all[0])
		}
		return printJSON(all)
	}
	if derr != nil {
		fmt.Fprintf(os.Stderr, "Warning: DEFAULTS cannot be read (%v); every launch is refused until ALTER DEFAULTS USE ENV … replaces it.\n", derr)
	}
	if st.Object == grammar.Env {
		v, p := all[0], profiles[0]
		yes := "no"
		if v.Default {
			yes = "yes"
		}
		printLabels([][2]string{
			{"Name", v.Name},
			{"Description", deref(optStr(v.Description), "(none)")},
			{"Used by", listOrNone(v.UsedBy)},
			{"Default", yes},
			{"Variables", strings.Join(humanVars(v.Vars, p.Set), "\n")},
		})
		return nil
	}
	t := newTable("NAME", "SET", "BLOCKED", "USED BY", "DESCRIPTION").flexible(4).rightAlign(1, 2)
	for i, v := range all {
		name := v.Name
		if v.Default {
			name += " *"
		}
		used := strings.Join(v.UsedBy, ", ")
		if used == "" {
			used = "-"
		}
		t.add(name, fmt.Sprint(len(profiles[i].Set)), fmt.Sprint(len(profiles[i].Unset)), used, v.Description)
	}
	t.render(os.Stdout)
	return nil
}

func explainPlaybook(playbooksDir, dir string, st *grammar.Stmt) error {
	pb, err := playbook.Require(playbooksDir, st.Name)
	if err != nil {
		return err
	}
	// The launch reads the governing manifest, which is the nearest one to
	// the config directory; EXPLAIN must describe that same launch.
	governing, _ := governingManifest(pb)
	var block *manifest.Env
	if governing != nil {
		block = governing.Env
	}
	origins, err := envprofile.Explain(dir, block)
	if err != nil {
		return fmt.Errorf("the launch of %q would be refused: %w", st.Name, err)
	}
	helper, err := helperInEffect(dir)
	if err != nil {
		return err
	}
	vars := make([]varJSON, 0, len(origins))
	values := map[string]string{}
	refs := 0
	for _, o := range origins {
		v := varJSON{Key: o.Key, Blocked: o.Blocked}
		switch {
		case o.Ref != "":
			v = varJSON{Key: o.Key, Ref: strPtr(o.Ref)}
			refs++
		case !o.Blocked:
			v = literalVar(o.Key, o.Value)
			values[o.Key] = o.Value
		}
		v.Layer = &layerJSON{Kind: o.Kind, Name: o.Set}
		vars = append(vars, v)
	}
	plugins, agent := launchPlugins(pb)
	if st.JSON {
		return printJSON(explainJSON{Playbook: pb.Name, Vars: vars, SecretHelper: helper, Plugins: plugins, Agent: agent})
	}
	if len(vars) == 0 {
		fmt.Printf("A launch of %s changes no environment variables.\n", pb.Name)
		fmt.Printf("\nSecret helper: %s\n", humanHelper(helper))
		printLaunchPlugins(plugins, agent)
		return nil
	}
	t := newTable("VARIABLE", "VALUE", "FROM").flexible(1)
	for _, v := range vars {
		shown := strings.TrimPrefix(humanVar(v, values[v.Key]), v.Key)
		shown = strings.TrimPrefix(strings.TrimPrefix(shown, "="), " ")
		from := v.Layer.Kind
		switch v.Layer.Kind {
		case envprofile.LayerEnv:
			from += " " + v.Layer.Name
		case envprofile.LayerDefaults:
			from += " (ENV " + v.Layer.Name + ")"
		case envprofile.LayerPlaybook:
			from += " " + pb.Name
		}
		t.add(v.Key, shown, from)
	}
	t.render(os.Stdout)
	fmt.Printf("\nSecret helper: %s\n", humanHelper(helper))
	if refs > 0 && helper == nil {
		fmt.Println("This launch uses secret references and no helper is configured: it would be refused.")
	}
	printLaunchPlugins(plugins, agent)
	return nil
}

// helperInEffect is the configured secret helper for --json, nil if none.
func helperInEffect(dir string) (*helperJSON, error) {
	h, err := envprofile.SecretHelper(dir)
	if err != nil || h == nil {
		return nil, err
	}
	return &helperJSON{Command: h.Command, From: h.From}, nil
}

func humanHelper(h *helperJSON) string {
	if h == nil {
		return "(none)"
	}
	return h.Command + " (from " + h.From + ")"
}

func humanVars(vars []varJSON, values map[string]string) []string {
	if len(vars) == 0 {
		return []string{"(none)"}
	}
	out := make([]string, 0, len(vars))
	for _, v := range vars {
		out = append(out, humanVar(v, values[v.Key]))
	}
	return out
}

// printLabels prints "Label:  value" lines with the values aligned; a
// multi-line value continues under its first line.
func printLabels(rows [][2]string) {
	width := 0
	for _, r := range rows {
		if len(r[0]) > width {
			width = len(r[0])
		}
	}
	for _, r := range rows {
		lines := strings.Split(r[1], "\n")
		fmt.Printf("%-*s  %s\n", width+1, r[0]+":", lines[0])
		for _, l := range lines[1:] {
			fmt.Printf("%-*s  %s\n", width+1, "", l)
		}
	}
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

func strPtr(s string) *string { return &s }

func optStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string, none string) string {
	if s == nil {
		return none
	}
	return *s
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func listOrNone(s []string) string {
	if len(s) == 0 {
		return "(none)"
	}
	return strings.Join(s, ", ")
}
