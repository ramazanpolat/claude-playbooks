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
	Name        string  `json:"name"`
	Version     *string `json:"version"`
	Description *string `json:"description"`
	Homepage    *string `json:"homepage"`
	Author      *string `json:"author"`
	Path        string  `json:"path"`
	// LastUsed is when the playbook's config directory last changed
	// (RFC3339, UTC).
	LastUsed *string     `json:"last_used"`
	Source   *sourceJSON `json:"source"`
	// Migrate is the declared migrate step ([update] migrate) that
	// cpb update runs after the new files are in place.
	Migrate  *string   `json:"migrate"`
	Linked   *string   `json:"linked"`
	Launcher *string   `json:"launcher"`
	Envs     []string  `json:"envs"`
	Vars     []varJSON `json:"vars"`
	Sandbox  bool      `json:"sandbox"`
	// IsolatedLogin is isolate_auth: no login shared with ~/.claude (a
	// sandboxed playbook is always isolated).
	IsolatedLogin bool `json:"isolated_login"`

	Marketplaces []marketplaceJSON `json:"marketplaces"`
	Plugins      []pluginJSON      `json:"plugins"`
	Agent        *string           `json:"agent"`
	MCPServers   []mcpServerJSON   `json:"mcp_servers"`
	Tools        toolsJSON         `json:"tools"`
	Skills       []skillJSON       `json:"skills"`
	Statusline   *string           `json:"statusline"`
	// StatuslineRefresh is statusLine.refreshInterval, whole seconds.
	StatuslineRefresh *int `json:"statusline_refresh"`
	// StatuslineHistory is what SET STATUSLINE PREVIOUS can go back to,
	// newest first (v3.25.0).
	StatuslineHistory []slHistoryJSON `json:"statusline_history"`
	Model             *string         `json:"model"`
	ModelPicker       *pickerJSON     `json:"model_picker"`
	// Play is the [play] record of a playbook `cpb play --keep` built, null
	// for every other. Last.
	Play *playRecordJSON `json:"play"`
}

type playRecordJSON struct {
	Ref    string `json:"ref"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Played string `json:"played"`
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
	MCPServers   []string    `json:"mcp_servers"`
	Tools        toolsJSON   `json:"tools"`
	Model        *modelJSON  `json:"model"`
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
	case st.Object == grammar.Sessions:
		return showSessions(st)
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
		Marketplaces: []marketplaceJSON{}, Plugins: []pluginJSON{}, MCPServers: describeMCP(pb.Path, pb.Manifest),
		Skills: describeSkills(pb.Manifest)}
	if pb.Manifest != nil && pb.Manifest.Play != nil {
		p := pb.Manifest.Play
		v.Play = &playRecordJSON{Ref: p.Ref, URL: p.URL, SHA256: p.SHA256, Played: p.Played}
	}
	// What the playbook's settings.json declares; an unreadable file shows
	// none rather than failing the whole SHOW.
	if sf, err := settings.Load(pb.Path); err == nil {
		if mps, plugins, agent, err := pluginState(sf.Root); err == nil {
			v.Marketplaces, v.Plugins, v.Agent = mps, plugins, agent
		}
		v.Tools, v.Statusline, v.Model = settingsExtras(sf.Root)
		v.ModelPicker = readPicker(sf.Root)
		v.StatuslineRefresh = statuslineRefresh(sf.Root)
	}
	v.StatuslineHistory = describeSLHistory(pb.Path)
	if !pb.LastUsed.IsZero() {
		v.LastUsed = strPtr(rfc3339(pb.LastUsed))
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
	v.Version, v.Description, v.Homepage, v.Author = optStr(m.Version), optStr(m.Description), optStr(m.Homepage), optStr(m.Author)
	if m.Alias != "" {
		v.Launcher = strPtr(m.Alias)
	}
	if m.Source != nil && m.Source.Repository != "" {
		v.Source = &sourceJSON{URL: m.Source.Repository, Branch: optStr(m.Source.Branch), Subdir: optStr(m.Source.Subdir)}
	}
	if m.Update != nil {
		v.Migrate = optStr(m.Update.Migrate)
	}
	v.Sandbox = m.Sandbox != nil && m.Sandbox.Always
	// A sandbox never shares the machine's login, whatever the manifest says.
	v.IsolatedLogin = m.IsolateAuth || v.Sandbox
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
		shown := displayEnvValue(v.Key, value)
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
	}
	// The manifest's own words, when it has them.
	for _, r := range []struct {
		label string
		value *string
	}{{"Description", v.Description}, {"Homepage", v.Homepage}, {"Author", v.Author}} {
		if r.value != nil {
			rows = append(rows, [2]string{r.label, *r.value})
		}
	}
	rows = append(rows, [][2]string{
		{"Path", v.Path},
		{"Source", source},
	}...)
	if v.Migrate != nil {
		rows = append(rows, [2]string{"Migrate", *v.Migrate + " (run by cpb update)"})
	}
	rows = append(rows, [][2]string{
		{"Launcher", deref(v.Launcher, "(none)")},
		{"Env sets", listOrNone(v.Envs)},
		{"Variables", strings.Join(humanVars(v.Vars, values), "\n")},
		{"Sandbox", sandbox},
	}...)
	if v.IsolatedLogin {
		rows = append(rows, [2]string{"Login", "isolated (shares nothing with ~/.claude)"})
	}
	if v.Play != nil {
		rows = append(rows, [2]string{"Played from", fmt.Sprintf("%s (sha256 %s, %s; cpb update %s)", v.Play.Ref, shortSHA(v.Play.SHA256), v.Play.Played, v.Name)})
	}
	// Shown when the playbook has any, so the rest of the layout stays as
	// it was for the playbooks that have none.
	if len(v.Tools.Allow)+len(v.Tools.Deny) > 0 {
		rows = append(rows, [2]string{"Tools", toolsLine(v.Tools)})
	}
	if v.Statusline != nil {
		rows = append(rows, [2]string{"Status line", statuslineLine(v)})
	}
	if v.Model != nil {
		rows = append(rows, [2]string{"Model", *v.Model})
	}
	if v.ModelPicker != nil {
		rows = append(rows, [2]string{"Model picker", pickerLine(v.ModelPicker)})
	}
	if len(v.Skills) > 0 {
		names := make([]string, len(v.Skills))
		for i, s := range v.Skills {
			names[i] = s.Name + " (" + s.Mode + ")"
		}
		rows = append(rows, [2]string{"Skills", strings.Join(names, ", ")})
	}
	if len(v.MCPServers) > 0 {
		names := make([]string, len(v.MCPServers))
		for i, m := range v.MCPServers {
			names[i] = m.Name + " (" + m.Transport + ")"
		}
		rows = append(rows, [2]string{"MCP servers", strings.Join(names, ", ")})
	}
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
		return printJSON(explainJSON{Playbook: pb.Name, Vars: vars, SecretHelper: helper, Plugins: plugins, Agent: agent, MCPServers: mcpNames(pb),
			Tools: describePlaybook(pb).Tools, Model: launchModel(pb, vars)})
	}
	if len(vars) == 0 {
		fmt.Printf("A launch of %s changes no environment variables.\n", pb.Name)
		fmt.Printf("\nSecret helper: %s\n", humanHelper(helper))
		printLaunchPlugins(plugins, agent)
		printMCPNames(mcpNames(pb))
		printToolsAndModel(pb, vars)
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
	printMCPNames(mcpNames(pb))
	printToolsAndModel(pb, vars)
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

func mcpNames(pb *playbook.Playbook) []string {
	names := []string{}
	for _, s := range describeMCP(pb.Path, pb.Manifest) {
		names = append(names, s.Name)
	}
	return names
}

func printMCPNames(names []string) {
	if len(names) > 0 {
		fmt.Printf("MCP servers: %s\n", strings.Join(names, ", "))
	}
}

func toolsLine(t toolsJSON) string {
	var parts []string
	if len(t.Allow) > 0 {
		parts = append(parts, "allow "+strings.Join(t.Allow, ", "))
	}
	if len(t.Deny) > 0 {
		parts = append(parts, "deny "+strings.Join(t.Deny, ", "))
	}
	return strings.Join(parts, "; ")
}

// modelJSON is the model a launch starts with and what decides it: an
// ANTHROPIC_MODEL a layer sets wins over the settings model.
type modelJSON struct {
	Name string `json:"name"`
	From string `json:"from"` // "ANTHROPIC_MODEL" or "playbook settings"
}

func launchModel(pb *playbook.Playbook, vars []varJSON) *modelJSON {
	for _, v := range vars {
		if v.Key != "ANTHROPIC_MODEL" || v.Blocked {
			continue
		}
		switch {
		case v.Value != nil:
			return &modelJSON{Name: *v.Value, From: "ANTHROPIC_MODEL"}
		case v.Ref != nil:
			// Set by reference: it still decides; its value is not shown.
			return &modelJSON{Name: "(by reference)", From: "ANTHROPIC_MODEL"}
		case v.Redacted:
			// A literal withheld from output (never with its Value set).
			return &modelJSON{Name: "(withheld)", From: "ANTHROPIC_MODEL"}
		}
	}
	if m := describePlaybook(pb).Model; m != nil {
		return &modelJSON{Name: *m, From: "playbook settings"}
	}
	return nil
}

func printToolsAndModel(pb *playbook.Playbook, vars []varJSON) {
	v := describePlaybook(pb)
	if len(v.Skills) > 0 {
		names := make([]string, len(v.Skills))
		for i, s := range v.Skills {
			names[i] = s.Name
		}
		fmt.Printf("Skills: %s\n", strings.Join(names, ", "))
	}
	if len(v.Tools.Allow)+len(v.Tools.Deny) > 0 {
		fmt.Printf("Tools: %s\n", toolsLine(v.Tools))
	}
	if m := launchModel(pb, vars); m != nil {
		line := fmt.Sprintf("Model: %s (%s)", m.Name, m.From)
		if m.From == "ANTHROPIC_MODEL" && v.Model != nil {
			line += "; the settings model " + *v.Model + " is overridden"
		}
		fmt.Println(line + "; a launch's --model and /model still win")
	}
	if v.ModelPicker != nil {
		fmt.Printf("Model picker: %s\n", pickerLine(v.ModelPicker))
	}
	if v.Statusline != nil {
		fmt.Printf("Status line: %s\n", statuslineLine(v))
	}
	if n := len(v.StatuslineHistory); n > 0 {
		fmt.Printf("Status line history: %d earlier (SET STATUSLINE PREVIOUS restores %s)\n", n, v.StatuslineHistory[0].Command)
	}
	if v.IsolatedLogin {
		fmt.Println("Login: isolated: no link to ~/.claude's login and no machine token; /login once in it")
	}
}

// statuslineLine is the status line command, and its refresh when set.
func statuslineLine(v playbookJSON) string {
	if v.Statusline == nil {
		return ""
	}
	if v.StatuslineRefresh != nil {
		return fmt.Sprintf("%s (refreshes every %d s)", *v.Statusline, *v.StatuslineRefresh)
	}
	return *v.Statusline
}

// shortSHA is the first 12 hex digits of a sha256, as the preview names it.
func shortSHA(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// governingManifest returns the manifest a launch of pb consults, resolved
// exactly as the launch resolves it: the nearest valid manifest walking up
// from the config directory (manifest.NearestPath, the lookup behind
// PrepareLaunchEnv and auth status). Usually that is the playbook's own root
// manifest, and the returned directory is "". It is the directory of the
// governing manifest when that is some other file: a legacy `subdir` layout
// whose subdirectory carries a manifest of its own, or a manifest-free
// playbook under an ancestor directory that has one.
func governingManifest(pb *playbook.Playbook) (*manifest.Manifest, string) {
	m, dir, _ := manifest.NearestPath(pb.Path)
	if m == nil || dir == pb.RootPath {
		return m, ""
	}
	return m, dir
}

// profileUsers maps each env set to the sorted playbooks that use it.
func profileUsers(playbooksDir string) (map[string][]string, error) {
	pbs, err := playbook.Discover(playbooksDir)
	if err != nil {
		return nil, err
	}
	users := map[string][]string{}
	for _, pb := range pbs {
		// A launch reads the governing manifest, which in a subdir layout
		// can be a nested one; the root manifest is what ALTER PLAYBOOK edits. Both
		// count, so a set either one names is never deleted from under it.
		named := map[string]bool{}
		governing, _ := governingManifest(pb)
		for _, m := range []*manifest.Manifest{pb.Manifest, governing} {
			if m == nil || m.Env == nil {
				continue
			}
			for _, name := range m.Env.Profiles {
				if !named[name] {
					named[name] = true
					users[name] = append(users[name], pb.Name)
				}
			}
		}
	}
	for name := range users {
		sort.Strings(users[name])
	}
	return users, nil
}

// isRegistryDefault reports whether name is one of the registry defaults,
// by spelling or by file identity (one file, two spellings, on a
// case-insensitive filesystem). No defaults match nothing.
func isRegistryDefault(dir string, defaults []string, name string) bool {
	for _, d := range defaults {
		if d == name || envprofile.SameProfile(dir, d, name) {
			return true
		}
	}
	return false
}
