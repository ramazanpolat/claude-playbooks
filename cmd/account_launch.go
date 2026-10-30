package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/account"
	"github.com/ramazanpolat/claude-playbooks/internal/auth"
	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envset"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// anthropicConfigDirName is the empty directory, in each config directory,
// that ANTHROPIC_CONFIG_DIR names at launch: no Anthropic profile there, so
// none is ever the credential.
const anthropicConfigDirName = ".cpb-anthropic-config"

// accountLaunch is the account part of one launch (SPEC.md, "CREATE /
// ALTER / DROP ACCOUNT").
type accountLaunch struct {
	sel      account.Selection
	rendered account.Rendered
	// layers are the launch's layers with the account's appended last.
	layers    []*manifest.Env
	configDir string
}

// accountFlag is the --account a launch names: the last one wins.
func accountFlag(layers []*manifest.Env) string {
	name := ""
	for _, l := range layers {
		if l != nil && len(l.Accounts) > 0 {
			name = l.Accounts[len(l.Accounts)-1]
		}
	}
	return name
}

// planAccount picks the account a launch of configDir runs on and refuses,
// before anything is changed, a launch that something other than the
// account would route or authenticate: a reserved variable set by a layer,
// or a settings file, or a --settings passed to claude, that sets one or
// names an apiKeyHelper. workdir is the directory Claude Code starts in,
// whose .claude settings apply too; claudeArgs are the arguments passed
// through to claude (nil when there are none, as for EXPLAIN).
func planAccount(configDir, name string, layers []*manifest.Env, workdir string, claudeArgs []string) (*accountLaunch, error) {
	root := config.ResolvePlaybooksDir()
	var list []string
	m, _ := manifest.Nearest(configDir)
	if m != nil && m.Env != nil {
		list = m.Env.Accounts
	}
	sel, err := account.Select(account.Dir(root), name, list, accountFlag(layers))
	if err != nil {
		return nil, err
	}
	eff, err := auth.EffectiveBlock(configDir, layers)
	if err != nil {
		return nil, err
	}
	// A playbook that names no account may state its route in one layer:
	// it runs as an account derived for the launch (internal/auth).
	var route *auth.LayerRoute
	if sel.Account == nil && sel.Why == account.WhyInherited {
		if route, err = auth.LayerRouteFor(configDir, layers, eff); err != nil {
			return nil, err
		}
		if route != nil {
			sel = account.Selection{Name: route.Name, Account: route.Account, Why: route.Why}
		}
	}
	if route == nil {
		if err := reservedInLayers(root, name, m, eff, layers, sel); err != nil {
			return nil, err
		}
	}
	if err := reservedInSettings(configDir, workdir); err != nil {
		return nil, err
	}
	if err := reservedInSettingsArgs(claudeArgs, workdir, false); err != nil {
		return nil, err
	}
	rendered := sel.Render(filepath.Join(configDir, anthropicConfigDirName))
	if route != nil && (route.Headers != "" || route.HeadersRef != "") {
		// Custom headers stated with the route go along as they are.
		l := rendered.Layer
		l.Block = slices.DeleteFunc(slices.Clone(l.Block), func(k string) bool { return k == "ANTHROPIC_CUSTOM_HEADERS" })
		if route.HeadersRef != "" {
			if l.Refs == nil {
				l.Refs = map[string]string{}
			}
			l.Refs["ANTHROPIC_CUSTOM_HEADERS"] = route.HeadersRef
		} else {
			l.Set["ANTHROPIC_CUSTOM_HEADERS"] = route.Headers
		}
	}
	if err := helperWithSettings(sel, rendered, claudeArgs); err != nil {
		return nil, err
	}
	if rendered.NoClaudeLogin {
		if err := auth.AccountLoginRefusal(configDir, name, sel.Name); err != nil {
			return nil, err
		}
	}
	all := append(slices.Clone(layers), rendered.Layer)
	return &accountLaunch{sel: sel, rendered: rendered, layers: all, configDir: configDir}, nil
}

// planSandboxAccount is planAccount for a sandboxed launch, whose working
// directory is the sandbox's: --workdir, else the manifest's, else here.
func planSandboxAccount(configDir, name string, layers []*manifest.Env, sb *manifest.Sandbox, opts sandboxOpts, claudeArgs []string) (*accountLaunch, error) {
	workdir := opts.workdir
	if workdir == "" && sb != nil {
		workdir = sb.Workdir
	}
	if workdir == "" {
		workdir, _ = os.Getwd()
	}
	if err := reservedInSettingsArgs(claudeArgs, "", true); err != nil {
		return nil, err
	}
	acct, err := planAccount(configDir, name, layers, expandHome(workdir), nil)
	if err != nil {
		return nil, err
	}
	if err := helperWithSettings(acct.sel, acct.rendered, claudeArgs); err != nil {
		return nil, err
	}
	return acct, nil
}

// liveLoginRefusal refuses a launch on an account that uses no Claude login
// while the playbook holds its own login and another session of it is
// live: setting the login aside would pull it from under that session, which
// may write it back (a /login, a refresh) within reach of this run.
func liveLoginRefusal(name, configDir string, a *accountLaunch) error {
	if a.noLoginAccount() == "" || !auth.IsAuthIsolated(configDir) || !auth.StoreHoldsLogin(configDir) {
		return nil
	}
	live, _, err := liveSessions(name)
	if err != nil {
		return fmt.Errorf("cannot tell whether a session of %s is live (%v): a launch on account %s, which uses no Claude login, waits until it can", name, err, a.sel.Name)
	}
	if len(live) > 0 {
		return fmt.Errorf("%s holds a login of its own and a session of it is live: a launch on account %s, which uses no Claude login, would set that login aside from under it. Launch once that session ends", name, a.sel.Name)
	}
	return nil
}

// noLoginAccount is what PrepareLaunchEnvFor takes: the account's name when
// it uses no Claude login.
func (a *accountLaunch) noLoginAccount() string {
	if a.rendered.NoClaudeLogin {
		return a.sel.Name
	}
	return ""
}

// prepare makes the empty directory ANTHROPIC_CONFIG_DIR names, afresh at
// every launch: it sits in the playbook's directory, which its sessions can
// write (and a sandbox mounts), so whatever is there now (a profile, a link
// in its place) is removed, and a new directory is made without following
// anything that appears meanwhile. It runs after every refusal, with the
// other changes a launch makes.
func (a *accountLaunch) prepare() error {
	dir := filepath.Join(a.configDir, anthropicConfigDirName)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("cannot prepare %s: %w", dir, err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return fmt.Errorf("cannot prepare %s: %w", dir, err)
	}
	return nil
}

// claudeArgs are the arguments the account adds before claude's own: its
// apiKeyHelper as a launch-only setting, never written into a file.
func (a *accountLaunch) claudeArgs(args []string) []string {
	if a.rendered.KeyHelper == "" {
		return args
	}
	s, _ := json.Marshal(map[string]string{"apiKeyHelper": a.rendered.KeyHelper})
	return append([]string{"--settings", string(s)}, args...)
}

// reservedInLayers refuses a reserved variable that any layer under the
// account sets (a value or a reference): DEFAULTS, an env set, the
// playbook's own block, a launch flag. Blocking one is fine: the launch
// removes it anyway. The refusal names the variable and the layer, never a
// value, and, for an env set that holds an address and a key, the account
// that replaces it.
func reservedInLayers(root, name string, m *manifest.Manifest, eff *manifest.Env, layers []*manifest.Env, sel account.Selection) error {
	if eff == nil {
		return nil
	}
	var keys []string
	for k := range eff.Set {
		if account.IsReserved(k) {
			keys = append(keys, k)
		}
	}
	for k := range eff.Refs {
		if account.IsReserved(k) {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	sort.Strings(keys)
	var block *manifest.Env
	if m != nil {
		block = m.Env
	}
	where := map[string]string{}
	sets := map[string]bool{}
	if origins, err := envset.Explain(envset.Dir(root), block); err == nil {
		for _, o := range origins {
			if o.Blocked {
				continue
			}
			switch o.Kind {
			case envset.LayerDefaults:
				where[o.Key], sets[o.Set] = "ENV "+o.Set+" (in DEFAULTS)", true
			case envset.LayerEnv:
				where[o.Key], sets[o.Set] = "ENV "+o.Set, true
			default:
				where[o.Key] = "the playbook's own block (SET VAR)"
			}
		}
	}
	target := name
	if target == "" {
		target = "<playbook>"
	}
	var lines []string
	for _, k := range keys {
		w := where[k]
		if w == "" {
			w = "a launch flag (--env, --env-set, --env-file)"
		}
		lines = append(lines, "  - "+k+", set by "+w)
	}
	msg := fmt.Sprintf("since v4.2.0 an account says where a playbook's requests go and how they authenticate, and these are set elsewhere:\n%s\n"+
		"Move them into an account and attach it: cpb CREATE ACCOUNT <name> SET provider = '<provider>', base_url = '<address>', auth_token FROM '<ref>'; "+
		"cpb ALTER PLAYBOOK %s USE ACCOUNT <name>; then remove them where they are set (ALTER ENV <set> UNSET …, ALTER PLAYBOOK %s UNSET VAR …)",
		strings.Join(lines, "\n"), target, target)
	// A route a layer states runs only where the account is inherited.
	if sel.Why != account.WhyInherited {
		msg += fmt.Sprintf("\nA route stated in a layer (an address with its key, in one env set, SET VAR or the launch's flags) runs only for a playbook that names no account; this launch runs on %s (%s)", sel.Name, sel.Why)
	}
	if slices.Contains(keys, "ANTHROPIC_CUSTOM_HEADERS") {
		msg += "\nANTHROPIC_CUSTOM_HEADERS: on an account each header goes by reference, ALTER ACCOUNT <name> SET header.<Name> FROM '<ref>'; in a layer only beside the address and key it belongs to"
	}
	// One env set holding an address and its key is an account already:
	// say which statement makes it one, from references only.
	for set := range sets {
		if s := accountFromSet(root, set); s != "" {
			msg += "\nFor ENV " + set + ": " + s
		}
	}
	return errors.New(msg)
}

// accountFromSet is the CREATE ACCOUNT statement that replaces an env set
// holding an address and its key, built from references only: "" when the
// set is not that shape, or holds its key as a literal.
func accountFromSet(root, name string) string {
	p, err := envset.Read(envset.Dir(root), name)
	if err != nil || p == nil {
		return ""
	}
	base := p.Set["ANTHROPIC_BASE_URL"]
	if base == "" || redactURLCredentials(base) != base {
		return ""
	}
	u, err := account.ParseBaseURL(base)
	if err != nil {
		return ""
	}
	provider := account.Gateway
	if account.IsAnthropicURL(u) {
		provider = account.Anthropic
	}
	cred := ""
	switch {
	case p.Refs["ANTHROPIC_AUTH_TOKEN"] != "":
		cred = "auth_token FROM " + quoteSQL(p.Refs["ANTHROPIC_AUTH_TOKEN"])
	case p.Refs["ANTHROPIC_API_KEY"] != "":
		cred = "api_key FROM " + quoteSQL(p.Refs["ANTHROPIC_API_KEY"])
	case len(p.Set["ANTHROPIC_AUTH_TOKEN"]) > 0 || len(p.Set["ANTHROPIC_API_KEY"]) > 0:
		return ""
	default:
		cred = "auth = 'none'"
	}
	model := ""
	if m := p.Set["ANTHROPIC_MODEL"]; m != "" && !strings.ContainsAny(m, " '") {
		model = ", model = " + quoteSQL(m)
	}
	return fmt.Sprintf("cpb CREATE ACCOUNT %s SET provider = '%s', base_url = %s, %s%s", name, provider, quoteSQL(base), cred, model)
}

func quoteSQL(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// reservedInSettings refuses a settings file that would route or
// authenticate the launch after cpb's account: the playbook's settings.json
// and the working folder's .claude/settings.json and settings.local.json,
// whose env Claude Code applies over the environment it starts with, and
// whose apiKeyHelper outranks the Claude login. One that cannot be read or
// parsed refuses too.
func reservedInSettings(configDir, workdir string) error {
	files := []string{filepath.Join(configDir, "settings.json")}
	if workdir != "" {
		files = append(files, filepath.Join(workdir, ".claude", "settings.json"), filepath.Join(workdir, ".claude", "settings.local.json"))
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("%s cannot be read (%v): it may route or authenticate this launch, so it is refused", f, err)
		}
		if err := settingsReserved(f, data); err != nil {
			return err
		}
	}
	return nil
}

// reservedInSettingsArgs is reservedInSettings for the --settings that the
// launch passes through to claude: inline JSON, or a file, relative to the
// directory claude starts in. Claude Code applies it over every settings
// file, so it is held to the same rule. A sandboxed launch takes inline JSON
// only: a file is read inside the sandbox, which cpb cannot see from here.
// Every argument is scanned, past a "--" too: one may be another option's
// value, so the scan fails closed rather than guess claude's parsing. Never
// echoes the value.
func reservedInSettingsArgs(claudeArgs []string, workdir string, sandboxed bool) error {
	for i := 0; i < len(claudeArgs); i++ {
		a := claudeArgs[i]
		var value string
		switch {
		case a == "--settings" && i+1 < len(claudeArgs):
			i++
			value = claudeArgs[i]
		case strings.HasPrefix(a, "--settings="):
			value = strings.TrimPrefix(a, "--settings=")
		default:
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(value), "{") {
			if err := settingsReserved("--settings", []byte(value)); err != nil {
				return err
			}
			continue
		}
		if sandboxed {
			return errors.New("--settings names a file, which a sandboxed launch reads inside the sandbox where cpb cannot check it for an account's variables: pass the settings inline as JSON")
		}
		f := expandHome(value)
		if !filepath.IsAbs(f) && workdir != "" {
			f = filepath.Join(workdir, f)
		}
		data, err := os.ReadFile(f)
		if err != nil {
			why := "it cannot be read"
			if errors.Is(err, os.ErrNotExist) {
				why = "no such file"
			}
			return fmt.Errorf("--settings names a file that cannot be checked (%s): it may route or authenticate this launch, so it is refused", why)
		}
		if err := settingsReserved("the --settings file", data); err != nil {
			return err
		}
	}
	return nil
}

// helperWithSettings refuses a --settings passed through to claude on an
// account whose credential is a helper: the account's apiKeyHelper travels
// as a --settings of its own, and claude takes one.
func helperWithSettings(sel account.Selection, rendered account.Rendered, claudeArgs []string) error {
	if rendered.KeyHelper == "" {
		return nil
	}
	for _, a := range claudeArgs {
		if a == "--settings" || strings.HasPrefix(a, "--settings=") {
			return fmt.Errorf("account %s authenticates with a helper, which a launch passes to claude as its own --settings, and claude takes one: put these settings in the playbook's settings.json instead of --settings", sel.Name)
		}
	}
	return nil
}

// settingsReserved refuses settings (from the file or flag name) that set a
// reserved variable in their env, or name an apiKeyHelper, or do not parse.
// Keys are matched exactly, as Claude Code reads them: Go's case-insensitive
// struct decoding would let an "ENV" key hide the "env" one.
func settingsReserved(name string, data []byte) error {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return fmt.Errorf("%s does not parse as a JSON object: it may route or authenticate this launch, so it is refused", name)
	}
	var found []string
	if raw, ok := top["env"]; ok && string(raw) != "null" {
		var env map[string]json.RawMessage
		if err := json.Unmarshal(raw, &env); err != nil {
			return fmt.Errorf("%s has an env that is not an object: it may route or authenticate this launch, so it is refused", name)
		}
		for k := range env {
			if account.IsReserved(k) {
				found = append(found, k)
			}
		}
	}
	sort.Strings(found)
	if raw, ok := top["apiKeyHelper"]; ok && string(raw) != "null" {
		found = append(found, "apiKeyHelper")
	}
	if len(found) > 0 {
		return fmt.Errorf("%s sets %s, which would route or authenticate this launch after its account: since v4.2.0 that is the account's (CREATE ACCOUNT … SET …, helper = '<command>' for an apiKeyHelper); remove it", name, strings.Join(found, ", "))
	}
	return nil
}
