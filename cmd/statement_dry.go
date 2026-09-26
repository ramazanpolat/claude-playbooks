package cmd

import (
	"slices"

	"github.com/ramazanpolat/claude-playbooks/internal/envprofile"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

// dryState is what an APPLY --dry-run's earlier statements would have
// written. A dry run writes nothing, so each statement records its result
// here instead, and every later statement reads this before the files on
// disk: a file is judged against the state its own earlier statements
// produce, as it would run.
type dryState struct {
	profiles    map[string]*envprofile.Profile // env sets written; nil: dropped
	playbooks   map[string]bool                // true: created or renamed to; false: dropped or renamed from
	pbEnvs      map[string]*manifest.Env       // a playbook's env block after earlier statements; nil: empty
	defaults    []string
	hasDefaults bool

	// Plugins and the agent, per playbook name: what earlier statements
	// would have installed and pinned, and, for a playbook renamed earlier,
	// the config directory it still has under its old name.
	worlds     map[string]*pluginWorld
	agents     map[string]*string // nil: unset
	configDirs map[string]string
}

func newDryState() *dryState {
	return &dryState{
		profiles:   map[string]*envprofile.Profile{},
		playbooks:  map[string]bool{},
		pbEnvs:     map[string]*manifest.Env{},
		worlds:     map[string]*pluginWorld{},
		agents:     map[string]*string{},
		configDirs: map[string]string{},
	}
}

// profile reads an env set as the run sees it: nil when it does not exist.
func (r *stmtRun) profile(dir, name string) (*envprofile.Profile, error) {
	if r.dry != nil {
		if p, ok := r.dry.profiles[name]; ok {
			if p == nil {
				return nil, nil
			}
			return cloneProfile(p), nil
		}
	}
	return envprofile.Read(dir, name)
}

// recordProfile keeps a dry run's write of an env set; p nil records a drop.
func (r *stmtRun) recordProfile(name string, p *envprofile.Profile) {
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
		delete(r.dry.agents, name)
		delete(r.dry.configDirs, name)
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
	a, aok := r.dry.agents[from]
	r.recordPlaybook(from, false)
	r.recordPlaybook(to, true)
	if wok {
		r.dry.worlds[to] = w
	}
	if aok {
		r.dry.agents[to] = a
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
	return envprofile.Defaults(dir)
}

func (r *stmtRun) recordDefaults(names []string) {
	if r.dry != nil {
		r.dry.defaults, r.dry.hasDefaults = slices.Clone(names), true
	}
}

// envUsers is profileUsers as the run sees it: a playbook whose env block
// an earlier statement changed counts by that block, and a dropped one not
// at all.
func (r *stmtRun) envUsers(playbooksDir string) (map[string][]string, error) {
	users, err := profileUsers(playbooksDir)
	if err != nil || r.dry == nil {
		return users, err
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
		for _, set := range e.Profiles {
			if !slices.Contains(users[set], pb) {
				users[set] = append(users[set], pb)
			}
		}
	}
	return users, nil
}
