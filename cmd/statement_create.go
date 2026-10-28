package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/account"
	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envset"
	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
	"github.com/ramazanpolat/claude-playbooks/internal/launcher"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
	"github.com/ramazanpolat/claude-playbooks/internal/settings"
)

// SHOW CREATE writes the statements that rebuild the state (SPEC.md,
// "playbook.cpb"). Every form it writes is safe to repeat, so APPLY of its own
// output changes nothing. Plain text never travels in the output: a
// credential-looking literal becomes a comment with the value withheld and
// the statement that would store it by reference, and SHOW CREATE then
// exits non-zero unless --skip-secrets.

// createBlock is the text for one object, and how many literals it withheld.
type createBlock struct {
	text     string
	withheld int
}

func showCreate(st *grammar.Stmt) error {
	playbooksDir := config.ResolvePlaybooksDir()
	dir := envset.Dir(playbooksDir)
	var blocks []createBlock
	switch st.Object {
	case grammar.Account:
		a, err := account.Read(account.Dir(playbooksDir), st.Name)
		if err != nil {
			return err
		}
		if a == nil {
			return fmt.Errorf("no account %q", st.Name)
		}
		blocks = append(blocks, createAccountBlock(a))
	case grammar.Env:
		p, err := envset.Read(dir, st.Name)
		if err != nil {
			return err
		}
		if p == nil {
			return fmt.Errorf("no env set %q", st.Name)
		}
		blocks = append(blocks, createEnvBlock(p))
	case grammar.Playbook:
		pb, err := playbook.Require(playbooksDir, st.Name)
		if err != nil {
			return err
		}
		b, err := createPlaybookBlock(pb)
		if err != nil {
			return err
		}
		blocks = append(blocks, b)
	default: // ALL: the secret helper, env sets, DEFAULTS, playbooks, so each statement finds what it names
		helper, defaults, err := createDefaultsBlocks(dir)
		if err != nil {
			return err
		}
		// The helper comes first: APPLY checks each reference against the
		// helper an earlier statement sets, and env sets carry references.
		if helper.text != "" {
			blocks = append(blocks, helper)
		}
		profiles, err := envset.List(dir)
		if err != nil {
			return err
		}
		for _, p := range profiles {
			blocks = append(blocks, createEnvBlock(p))
		}
		// Accounts before DEFAULTS and the playbooks, which name them.
		accounts, err := account.List(account.Dir(playbooksDir))
		if err != nil {
			return err
		}
		for _, a := range accounts {
			blocks = append(blocks, createAccountBlock(a))
		}
		if defaults.text != "" {
			blocks = append(blocks, defaults)
		}
		// ALL is every playbook, for APPLY to replay: a playbook that cannot
		// be read refuses it, rather than a dump that leaves one out.
		pbs, err := playbook.Discover(playbooksDir)
		if err != nil {
			return fmt.Errorf("SHOW CREATE ALL writes every playbook, and one cannot be read: %w", err)
		}
		for _, pb := range pbs {
			b, err := createPlaybookBlock(pb)
			if err != nil {
				return err
			}
			blocks = append(blocks, b)
		}
	}
	withheld := 0
	texts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		texts = append(texts, b.text)
		withheld += b.withheld
	}
	fmt.Println(strings.Join(texts, "\n\n"))
	if withheld > 0 && !st.SkipSecrets {
		return fmt.Errorf("%d credential-looking literal(s) were withheld (the commented lines): store each with SET … FROM '<ref>', or pass --skip-secrets to accept the output without them", withheld)
	}
	return nil
}

// withheldLiteral reports whether a literal must not go into a playbook file:
// the same test SHOW uses to redact it.
func withheldLiteral(key, value string) bool {
	return (manifest.LooksLikeSecretKey(key) && !manifest.PlainSetting(value)) || redactURLCredentials(value) != value
}

// varClauses turns one layer into SET, SET … FROM and BLOCK clauses, and
// comments for the literals it withholds. fix names the statement that
// would store a withheld key by reference.
func varClauses(set, refs map[string]string, unset []string, fix func(key string) string) ([]grammar.Clause, []string) {
	var clauses []grammar.Clause
	var comments []string
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lit := grammar.Clause{Kind: grammar.SetVar}
	for _, k := range keys {
		if withheldLiteral(k, set[k]) {
			comments = append(comments,
				fmt.Sprintf("-- SET %s=<withheld> AS PLAINTEXT  (a credential-looking literal never goes into a file)", k),
				"-- to keep it here, store it by reference: "+fix(k))
			continue
		}
		lit.Vars = append(lit.Vars, grammar.Var{Key: k, Value: set[k]})
	}
	if len(lit.Vars) > 0 {
		clauses = append(clauses, lit)
	}
	refKeys := make([]string, 0, len(refs))
	for k := range refs {
		refKeys = append(refKeys, k)
	}
	sort.Strings(refKeys)
	for _, k := range refKeys {
		clauses = append(clauses, grammar.Clause{Kind: grammar.SetRef, Vars: []grammar.Var{{Key: k, Ref: refs[k]}}})
	}
	if len(unset) > 0 {
		blocked := append([]string(nil), unset...)
		sort.Strings(blocked)
		clauses = append(clauses, grammar.Clause{Kind: grammar.BlockVar, Keys: blocked})
	}
	return clauses, comments
}

func createEnvBlock(p *envset.Set) createBlock {
	var clauses []grammar.Clause
	if p.Description != "" {
		clauses = append(clauses, grammar.Clause{Kind: grammar.Description, Arg: p.Description})
	}
	vars, comments := varClauses(p.Set, p.Refs, p.Block, func(k string) string {
		return fmt.Sprintf("ALTER ENV %s SET %s FROM '<ref>'", p.Name, k)
	})
	clauses = append(clauses, vars...)
	if len(comments) == 0 {
		st := &grammar.Stmt{Verb: grammar.Create, Object: grammar.Env, Name: p.Name, OrReplace: true, Clauses: clauses}
		return createBlock{text: st.Pretty() + ";"}
	}
	// A withheld literal is not in the file, so CREATE OR REPLACE would
	// rebuild the set without it and delete the stored value on APPLY.
	// Written instead as a create that leaves an existing set alone and an
	// ALTER that only adds: the stored value survives, at the price of
	// exact convergence for this set.
	withheld := len(comments) / 2
	comments = append(comments,
		"-- ENV "+p.Name+" withholds a literal, so it is written to keep what is stored: APPLY creates the set",
		"-- only if it is missing and adds the clauses below; keys added to it since are not removed")
	text := (&grammar.Stmt{Verb: grammar.Create, Object: grammar.Env, Name: p.Name, IfNotExists: true}).Pretty() + ";"
	if len(clauses) > 0 {
		text += "\n" + (&grammar.Stmt{Verb: grammar.Alter, Object: grammar.Env, Name: p.Name, Clauses: clauses}).Pretty() + ";"
	}
	return createBlock{text: joinComments(comments, text), withheld: withheld}
}

// createDefaultsBlocks is DEFAULTS as two statements: the secret helper,
// which goes before the env sets whose references it resolves, and the env
// sets DEFAULTS uses, which go after them. Either is empty when unset.
func createDefaultsBlocks(dir string) (helper, defaults createBlock, err error) {
	names, err := envset.Defaults(dir)
	if err != nil {
		return createBlock{}, createBlock{}, fmt.Errorf("DEFAULTS cannot be read: %w", err)
	}
	// The env sets dir sits in the playbooks root, beside the accounts.
	def, err := account.Default(account.Dir(filepath.Dir(dir)))
	if err != nil {
		return createBlock{}, createBlock{}, fmt.Errorf("the DEFAULTS account cannot be read: %w", err)
	}
	var clauses []grammar.Clause
	if len(names) > 0 {
		clauses = append(clauses, grammar.Clause{Kind: grammar.UseEnv, Names: names})
	}
	if def != "" {
		clauses = append(clauses, grammar.Clause{Kind: grammar.UseAccount, Names: []string{def}})
	}
	if len(clauses) > 0 {
		st := &grammar.Stmt{Verb: grammar.Alter, Object: grammar.Defaults, Clauses: clauses}
		defaults = createBlock{text: st.Pretty() + ";"}
	}
	// Only the stored setting: CPB_SECRET_HELPER belongs to one process.
	if data, err := os.ReadFile(filepath.Join(dir, envset.SecretHelperFile)); err == nil {
		if cmd := strings.TrimSpace(string(data)); cmd != "" {
			st := &grammar.Stmt{Verb: grammar.Alter, Object: grammar.Defaults, Clauses: []grammar.Clause{{Kind: grammar.SetHelper, Arg: cmd}}}
			helper = createBlock{text: st.Pretty() + ";"}
		}
	}
	return helper, defaults, nil
}

func createPlaybookBlock(pb *playbook.Playbook) (createBlock, error) {
	st := &grammar.Stmt{Verb: grammar.Create, Object: grammar.Playbook, Name: pb.Name, OrAlter: true}
	v := describePlaybook(pb)
	m := pb.Manifest
	switch {
	case v.Linked != nil:
		st.Clauses = append(st.Clauses, grammar.Clause{Kind: grammar.Link, Arg: *v.Linked})
	case v.Source != nil:
		// The recorded URL, not the view's masked one: a credential in it
		// is caught below and the whole statement withheld.
		st.Clauses = append(st.Clauses, grammar.Clause{Kind: grammar.From, Arg: m.Source.Repository})
		if v.Source.Branch != nil {
			st.Clauses = append(st.Clauses, grammar.Clause{Kind: grammar.Branch, Arg: *v.Source.Branch})
		}
		if v.Source.Subdir != nil {
			st.Clauses = append(st.Clauses, grammar.Clause{Kind: grammar.Subdir, Arg: *v.Source.Subdir})
		}
	}
	switch {
	case pb.Alias() != "" && pb.Alias() != pb.Name:
		st.Clauses = append(st.Clauses, grammar.Clause{Kind: grammar.Launcher, Arg: pb.Alias()})
	case hasNameLauncher(pb.Name) || !launcherOpsAllowed():
		// The default launcher, named after the playbook, as RESET launcher
		// (D2: a key at its default is RESET), never as SET launcher =
		// '<name>': a playbook's name may be a keyword ('server', or one
		// named with before WITH was reserved), which is no launcher value.
		// Under a --playbooks-dir root cpb manages no launchers, so no alias
		// is the default there.
		st.Clauses = append(st.Clauses, grammar.Clause{Kind: grammar.DefaultLauncher})
	default:
		st.Clauses = append(st.Clauses, grammar.Clause{Kind: grammar.NoLauncher})
	}
	// The properties SHOW CREATE always writes, so a recipe never leans on
	// a default: login and global_instructions, and sandbox.always with the table's
	// other set keys (a sandboxed playbook's login is isolated). On the
	// CREATE, which applies them to a playbook that exists too (CREATE OR
	// ALTER converges). A linked playbook's manifest and settings.json
	// are the target's.
	sandboxed := v.Sandbox.Always && v.Linked == nil
	if v.Linked == nil {
		login := "shared"
		if v.IsolatedLogin || sandboxed {
			login = "isolated"
		}
		global, _ := globalInstructionsStateOf(pb.Path)
		props := []grammar.Var{{Key: "login", Value: login}, {Key: "global_instructions", Value: global}}
		st.Clauses = append(st.Clauses, grammar.Clause{Kind: grammar.SetProperties, Settings: props})
		// Exact convergence (D2): every key that is not set is RESET, so
		// the statement brings a playbook back to exactly this, and the
		// whole env list is stated, '' for none. Collections (plugins,
		// tools, MCP servers, models, skills) stay additive.
		st.Clauses = append(st.Clauses, sandboxCreateClauses(m, sandboxed)...)
		sf, err := settings.Load(pb.Path)
		if err != nil {
			return createBlock{}, fmt.Errorf("PLAYBOOK %s: %w", pb.Name, err)
		}
		st.Clauses = append(st.Clauses, settingsResetClauses(sf.Root)...)
		var sets, accounts []string
		if m != nil && m.Env != nil {
			sets, accounts = m.Env.Sets, m.Env.Accounts
		}
		st.Clauses = append(st.Clauses, grammar.Clause{Kind: grammar.UseEnv, Names: sets})
		st.Clauses = append(st.Clauses, grammar.Clause{Kind: grammar.UseAccount, Names: accounts})
	}
	text := st.Pretty() + ";"
	// A source URL that carries credentials is never printed: the whole
	// statement is withheld, shown with the URL masked.
	withheldSource := 0
	if v.Source != nil && redactSourceURL(m.Source.Repository) != m.Source.Repository {
		masked := *st
		masked.Clauses = slices.Clone(st.Clauses)
		for i, c := range masked.Clauses {
			if c.Kind == grammar.From {
				masked.Clauses[i].Arg = redactSourceURL(c.Arg)
			}
		}
		lines := []string{"-- withheld: the source URL of PLAYBOOK " + pb.Name + " carries credentials; install it from a URL without them"}
		for _, l := range strings.Split(masked.Pretty()+";", "\n") {
			lines = append(lines, "-- "+l)
		}
		text, withheldSource = strings.Join(lines, "\n"), 1
	}
	// Plugins and the agent: what settings.json declares, as the clauses
	// that declare it. A linked playbook's settings belong to its target.
	// A settings.json that cannot be read fails SHOW CREATE: an export
	// that silently left out the plugins would rebuild a different playbook.
	plugins := ""
	if v.Linked == nil {
		sf, err := settings.Load(pb.Path)
		if err != nil {
			return createBlock{}, fmt.Errorf("PLAYBOOK %s: %w", pb.Name, err)
		}
		if plugins, err = pluginCreateBlock(pb.Name, sf.Root); err != nil {
			return createBlock{}, fmt.Errorf("PLAYBOOK %s: %s: %w", pb.Name, settings.FileName, err)
		}
		// MCP servers and skills, as clauses of their own statement.
		mcps, mcpComments := mcpCreateClauses(pb.Path, m)
		skills, skillComments := skillCreateClauses(m)
		mcps = append(mcps, skills...)
		mcpComments = append(mcpComments, skillComments...)
		if len(mcps)+len(mcpComments) > 0 {
			t := strings.Join(mcpComments, "\n")
			if len(mcps) > 0 {
				if t != "" {
					t += "\n"
				}
				t += (&grammar.Stmt{Verb: grammar.Alter, Object: grammar.Playbook, Name: pb.Name, Clauses: mcps}).Pretty() + ";"
			}
			if plugins != "" {
				plugins += "\n\n"
			}
			plugins += t
		}
	}
	withPlugins := func(b createBlock) createBlock {
		if plugins != "" {
			b.text += "\n\n" + plugins
		}
		return b
	}
	// The variables cpb derived for MCP servers travel in their ADD MCP
	// SERVER clauses, not as variables of their own.
	var env *manifest.Env
	if m != nil {
		env = cloneEnv(m.Env)
		for v := range mcpVarsOf(m) {
			if env != nil {
				delete(env.Refs, v)
			}
		}
	}
	if v.Linked != nil {
		if env.Empty() {
			return withPlugins(createBlock{text: text, withheld: withheldSource}), nil
		}
		return createBlock{text: text + "\n-- the environment of a linked playbook lives in the target's " + manifest.FileName, withheld: withheldSource}, nil
	}
	alter := &grammar.Stmt{Verb: grammar.Alter, Object: grammar.Playbook, Name: pb.Name}
	var comments []string
	if !env.Empty() {
		var clauses []grammar.Clause
		clauses, comments = varClauses(env.Set, env.Refs, env.Block, func(k string) string {
			return fmt.Sprintf("ALTER PLAYBOOK %s SET VAR %s FROM '<ref>'", pb.Name, k)
		})
		alter.Clauses = append(alter.Clauses, clauses...)
	}
	if len(alter.Clauses) > 0 {
		text += "\n\n" + alter.Pretty() + ";"
	}
	return withPlugins(createBlock{text: joinComments(comments, text), withheld: len(comments)/2 + withheldSource}), nil
}

// sandboxCreateClauses is the [sandbox] table as SHOW CREATE states it:
// RESET sandbox when nothing in it is set, else sandbox.always and the keys
// that are set, and RESET for the keys that are not.
func sandboxCreateClauses(m *manifest.Manifest, sandboxed bool) []grammar.Clause {
	set := map[string]string{}
	if m != nil && m.Sandbox != nil {
		for _, kv := range m.Sandbox.Settings() {
			set[kv[0]] = kv[1]
		}
	}
	all := grammar.Clause{Kind: grammar.UnsetSandboxKeys}
	for _, k := range manifest.SandboxKeys {
		all.Settings = append(all.Settings, grammar.Var{Key: k})
	}
	if !sandboxed && len(set) == 0 {
		return []grammar.Clause{all}
	}
	on := grammar.Clause{Kind: grammar.SetSandboxKeys, Settings: []grammar.Var{{Key: "always", Value: strconv.FormatBool(sandboxed)}}}
	off := grammar.Clause{Kind: grammar.UnsetSandboxKeys}
	for _, k := range manifest.SandboxKeys {
		if k == "always" {
			continue
		}
		if v, ok := set[k]; ok {
			on.Settings = append(on.Settings, grammar.Var{Key: k, Value: v})
		} else {
			off.Settings = append(off.Settings, grammar.Var{Key: k})
		}
	}
	if len(off.Settings) == 0 {
		return []grammar.Clause{on}
	}
	return []grammar.Clause{on, off}
}

// settingsResetClauses RESETs the settings.json properties a playbook does
// not set: the model, the agent, the status line (or only its refresh, when
// a command status line has none) and the picker mode. A key that is set is
// written by the plugins ALTER, or left out when the grammar cannot write
// it, and is never reset.
func settingsResetClauses(root *settings.Object) []grammar.Clause {
	var out []grammar.Clause
	if !root.Has(keyModel) {
		out = append(out, grammar.Clause{Kind: grammar.UnsetModel})
	}
	if !root.Has(keyAgent) {
		out = append(out, grammar.Clause{Kind: grammar.UnsetAgent})
	}
	_, command, _ := settingsExtras(root)
	switch {
	case !root.Has(keyStatusline):
		out = append(out, grammar.Clause{Kind: grammar.UnsetStatusline})
	case command != nil:
		if sl, err := root.Object(keyStatusline); err == nil && !sl.Has(keyRefreshInterval) {
			out = append(out, grammar.Clause{Kind: grammar.UnsetStatuslineRefresh})
		}
	}
	if mp, err := root.Object(keyModelPicker); !root.Has(keyModelPicker) || (err == nil && !mp.Has("replaceBuiltInOptions")) {
		out = append(out, grammar.Clause{Kind: grammar.UnsetModelPickerMode})
	}
	return out
}

// hasNameLauncher reports whether the launcher named after a playbook is
// one cpb manages: the default launcher, which CREATE PLAYBOOK writes when
// no launcher clause says otherwise.
func hasNameLauncher(name string) bool {
	if !launcherOpsAllowed() {
		return false
	}
	dir, err := config.ResolveLauncherDir()
	if err != nil {
		return false
	}
	if _, exists, foreign := launcher.Lookup(dir, name); !exists || foreign {
		return false
	}
	// A launcher answers to its name; if another playbook claims the name
	// as its alias, the launcher is not this playbook's default. A read: a
	// manifest that cannot be read does not hide the launcher.
	owner, err := readableNameOwner(name, name)
	return err == nil && owner == nil
}

func joinComments(comments []string, text string) string {
	if len(comments) == 0 {
		return text
	}
	return strings.Join(comments, "\n") + "\n" + text
}
