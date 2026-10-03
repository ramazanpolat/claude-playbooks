package cmd

import (
	"encoding/json"
	"slices"

	"github.com/ramazanpolat/claude-playbooks/internal/envset"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

// dryState is what an APPLY --dry-run's earlier statements would have
// written. A dry run writes nothing, so each statement records its result
// here instead, and every later statement reads this before the files on
// disk: a file is judged against the state its own earlier statements
// produce, as it would run.
type dryState struct {
	profiles    map[string]*envset.Set   // env sets written; nil: dropped
	playbooks   map[string]bool          // true: created or renamed to; false: dropped or renamed from
	pbEnvs      map[string]*manifest.Env // a playbook's env block after earlier statements; nil: empty
	defaults    []string
	hasDefaults bool

	// Plugins and the agent, per playbook name: what earlier statements
	// would have installed and pinned, and, for a playbook renamed earlier,
	// the config directory it still has under its old name.
	worlds     map[string]*pluginWorld
	settings   map[string][]byte // settings.json as earlier statements left it (agent, tools, status line, model)
	configDirs map[string]string

	// MCP servers, per playbook name: the servers earlier statements would
	// have declared, and the derived variables they would have recorded.
	mcp        map[string]map[string]json.RawMessage
	mcpRecords map[string]map[string]*manifest.MCPRecord

	// Skills, per playbook name: the records earlier statements would have
	// left, and which skill directories they would have put in place (true)
	// or removed (false).
	skillRecords map[string]map[string]*manifest.SkillRecord
	skillKnown   map[string]map[string]bool

	// isolated and sandboxed, per playbook: isolated_login and [sandbox]
	// always as earlier statements would leave them.
	isolated  map[string]bool
	sandboxed map[string]bool

	// slHistory, per config directory key: the status line history as
	// earlier statements would leave it.
	slHistory map[string][]slEntry
}

func newDryState() *dryState {
	return &dryState{
		profiles:     map[string]*envset.Set{},
		playbooks:    map[string]bool{},
		pbEnvs:       map[string]*manifest.Env{},
		worlds:       map[string]*pluginWorld{},
		settings:     map[string][]byte{},
		configDirs:   map[string]string{},
		mcp:          map[string]map[string]json.RawMessage{},
		mcpRecords:   map[string]map[string]*manifest.MCPRecord{},
		skillRecords: map[string]map[string]*manifest.SkillRecord{},
		skillKnown:   map[string]map[string]bool{},
		isolated:     map[string]bool{},
		sandboxed:    map[string]bool{},
		slHistory:    map[string][]slEntry{},
	}
}

// profile reads an env set as the run sees it: nil when it does not exist.
func (r *stmtRun) profile(dir, name string) (*envset.Set, error) {
	if r.dry != nil {
		if p, ok := r.dry.profiles[name]; ok {
			if p == nil {
				return nil, nil
			}
			return cloneProfile(p), nil
		}
	}
	return envset.Read(dir, name)
}

// recordProfile keeps a dry run's write of an env set; p nil records a drop.
func (r *stmtRun) recordProfile(name string, p *envset.Set) {
	if r.dry == nil {
		return
	}
	if p != nil {
		p = cloneProfile(p)
	}
	r.dry.profiles[name] = p
}

// playbookState says what the run knows of a playbook beyond the disk:
// known reports that an earlier statement created, renamed or dropped it,
// and exists whether it is there now.
func (r *stmtRun) playbookState(name string) (known, exists bool) {
	if r.dry == nil {
		return false, false
	}
	exists, known = r.dry.playbooks[name]
	return known, exists
}

func (r *stmtRun) recordPlaybook(name string, exists bool) {
	if r.dry == nil {
		return
	}
	r.dry.playbooks[name] = exists
	if !exists {
		delete(r.dry.pbEnvs, name)
		delete(r.dry.worlds, name)
		delete(r.dry.settings, name)
		delete(r.dry.configDirs, name)
		delete(r.dry.mcp, name)
		delete(r.dry.mcpRecords, name)
		delete(r.dry.skillRecords, name)
		delete(r.dry.skillKnown, name)
		delete(r.dry.isolated, name)
		delete(r.dry.sandboxed, name)
	}
}

// renamePlaybook moves what a dry run knows of a playbook's plugins and
// agent to its new name; cfg is its config directory when it is on disk.
func (r *stmtRun) renamePlaybook(from, to, cfg string) {
	if r.dry == nil {
		return
	}
	if cfg == "" {
		cfg = r.dry.configDirs[from]
	}
	w, wok := r.dry.worlds[from]
	a, aok := r.dry.settings[from]
	ms, msok := r.dry.mcp[from]
	mr, mrok := r.dry.mcpRecords[from]
	sr, srok := r.dry.skillRecords[from]
	sk, skok := r.dry.skillKnown[from]
	iso, isook := r.dry.isolated[from]
	sbx, sbxok := r.dry.sandboxed[from]
	// A playbook on disk carries its login setting under its new name: the
	// directory it keeps says what it is.
	if cfg != "" && !(isook && sbxok) {
		if m, _ := manifest.Nearest(cfg); m != nil {
			if !isook {
				iso, isook = m.IsolatedLogin, true
			}
			if !sbxok {
				sbx, sbxok = m.Sandbox != nil && m.Sandbox.Always, true
			}
		}
	}
	r.recordPlaybook(from, false)
	r.recordPlaybook(to, true)
	if wok {
		r.dry.worlds[to] = w
	}
	if msok {
		r.dry.mcp[to] = ms
	}
	if mrok {
		r.dry.mcpRecords[to] = mr
	}
	if srok {
		r.dry.skillRecords[to] = sr
	}
	if skok {
		r.dry.skillKnown[to] = sk
	}
	if isook {
		r.dry.isolated[to] = iso
	}
	if sbxok {
		r.dry.sandboxed[to] = sbx
	}
	if aok {
		r.dry.settings[to] = a
	}
	if cfg != "" {
		r.dry.configDirs[to] = cfg
	}
}

// configDir is where a playbook's settings live as the run sees it: its
// directory, or, renamed earlier in a dry run, the directory it keeps.
func (r *stmtRun) configDir(name string, pb *playbook.Playbook) string {
	if pb != nil {
		return pb.Path
	}
	if r.dry != nil {
		return r.dry.configDirs[name]
	}
	return ""
}

// playbookEnv is a playbook's env block as the run sees it; disk is the
// block its manifest holds.
func (r *stmtRun) playbookEnv(name string, disk *manifest.Env) *manifest.Env {
	if r.dry != nil {
		if e, ok := r.dry.pbEnvs[name]; ok {
			return cloneEnv(e)
		}
	}
	return cloneEnv(disk)
}

func (r *stmtRun) recordPlaybookEnv(name string, e *manifest.Env) {
	if r.dry != nil {
		r.dry.pbEnvs[name] = cloneEnv(e)
	}
}

// defaultsList reads DEFAULTS as the run sees it.
func (r *stmtRun) defaultsList(dir string) ([]string, error) {
	if r.dry != nil && r.dry.hasDefaults {
		return slices.Clone(r.dry.defaults), nil
	}
	return envset.Defaults(dir)
}

func (r *stmtRun) recordDefaults(names []string) {
	if r.dry != nil {
		r.dry.defaults, r.dry.hasDefaults = slices.Clone(names), true
	}
}

// envUsers is profileUsers as the run sees it: a playbook whose env block
// an earlier statement changed counts by that block, and a dropped one not
// at all.
func (r *stmtRun) envUsers(playbooksDir string) (map[string][]string, []playbook.Unreadable, error) {
	users, bad, err := profileUsers(playbooksDir)
	if err != nil || r.dry == nil {
		return users, bad, err
	}
	drop := func(pb string) {
		for set, list := range users {
			users[set] = slices.DeleteFunc(list, func(s string) bool { return s == pb })
		}
	}
	for pb, alive := range r.dry.playbooks {
		if !alive {
			drop(pb)
		}
	}
	for pb, e := range r.dry.pbEnvs {
		drop(pb)
		if e == nil {
			continue
		}
		for _, set := range e.Sets {
			if !slices.Contains(users[set], pb) {
				users[set] = append(users[set], pb)
			}
		}
	}
	return users, bad, nil
}

// mcpState is a playbook's MCP servers as the run sees them; a dry run
// keeps what earlier statements would have declared.
func (r *stmtRun) mcpState(name, configDir string) (map[string]json.RawMessage, error) {
	if r.dry != nil {
		if s, ok := r.dry.mcp[name]; ok {
			return s, nil
		}
	}
	s, _, err := readMCPServers(configDir)
	if err != nil {
		return nil, err
	}
	if r.dry != nil {
		r.dry.mcp[name] = s
	}
	return s, nil
}

// mcpRecords is a playbook's MCP record as the run sees it.
func (r *stmtRun) mcpRecords(name string, m *manifest.Manifest) map[string]*manifest.MCPRecord {
	if r.dry != nil {
		if rec, ok := r.dry.mcpRecords[name]; ok {
			return rec
		}
	}
	if m == nil {
		return nil
	}
	return m.MCP
}

func cloneMCP(in map[string]*manifest.MCPRecord) map[string]*manifest.MCPRecord {
	if in == nil {
		return nil
	}
	out := make(map[string]*manifest.MCPRecord, len(in))
	for k, v := range in {
		if v == nil {
			continue
		}
		out[k] = &manifest.MCPRecord{Vars: slices.Clone(v.Vars)}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// skillRecords is a playbook's skill record as the run sees it.
func (r *stmtRun) skillRecords(name string, m *manifest.Manifest) map[string]*manifest.SkillRecord {
	if r.dry != nil {
		if rec, ok := r.dry.skillRecords[name]; ok {
			return rec
		}
	}
	if m == nil {
		return nil
	}
	return m.Skills
}

// skillKnown is what a dry run's earlier statements decided about which
// skill directories exist.
func (r *stmtRun) skillKnown(name string) map[string]bool {
	if r.dry == nil {
		return nil
	}
	return r.dry.skillKnown[name]
}

// recordSkills keeps a dry run's skill changes for later statements.
func (r *stmtRun) recordSkills(name string, after map[string]*manifest.SkillRecord, p *skillPlan) {
	if r.dry == nil {
		return
	}
	r.dry.skillRecords[name] = cloneSkills(after)
	known := r.dry.skillKnown[name]
	if known == nil {
		known = map[string]bool{}
	}
	for n, rec := range p.records {
		known[n] = rec != nil
	}
	r.dry.skillKnown[name] = known
}
