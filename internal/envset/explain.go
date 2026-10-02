package envset

import (
	"sort"

	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// Layer kinds, bottom to top.
const (
	LayerDefaults = "DEFAULTS" // an env set listed in DEFAULTS
	LayerEnv      = "ENV"      // an env set the playbook uses
	LayerPlaybook = "PLAYBOOK" // the playbook's own block
)

// Origin is one variable a launch changes, and the layer that decided it.
type Origin struct {
	Key     string
	Value   string // when set
	Ref     string // when set by a secret reference
	Blocked bool   // removed at launch
	Kind    string // LayerDefaults, LayerEnv or LayerPlaybook
	Set     string // the env set's name, for LayerDefaults and LayerEnv
}

// Explain is ExpandWithDefault keeping provenance: for each variable the
// launch would set or remove, the layer that decided it. It merges exactly
// as ExpandWithDefault does (defaults in order, then e's profiles in order,
// then e's own block; within a layer, refs, then sets, then unsets), so the two cannot
// disagree about a value. Errors are ExpandWithDefault's: a missing or
// broken layer refuses (errors.Is(err, ErrProfile)). Sorted by key.
func Explain(dir string, e *manifest.Env) ([]Origin, error) {
	defaults, err := Defaults(dir)
	if err != nil {
		return nil, &ResolveError{Name: DefaultMarker, Err: err}
	}
	type layer struct {
		kind, set string
		env       *manifest.Env
	}
	var layers []layer
	add := func(kind, name string) error {
		p, err := Read(dir, name)
		if err != nil {
			return &ResolveError{Name: name, Err: err}
		}
		if p == nil {
			return &MissingError{Name: name, Dir: dir}
		}
		layers = append(layers, layer{kind, name, p.Env()})
		return nil
	}
	for _, name := range defaults {
		if err := add(LayerDefaults, name); err != nil {
			return nil, err
		}
	}
	if e != nil {
		for _, name := range e.Sets {
			if err := add(LayerEnv, name); err != nil {
				return nil, err
			}
		}
		layers = append(layers, layer{LayerPlaybook, "", &manifest.Env{Set: e.Set, Refs: e.Refs, Block: e.Block}})
	}

	decided := map[string]Origin{}
	for _, l := range layers {
		for key, ref := range l.env.Refs {
			decided[key] = Origin{Key: key, Ref: ref, Kind: l.kind, Set: l.set}
		}
		for key, value := range l.env.Set {
			decided[key] = Origin{Key: key, Value: value, Kind: l.kind, Set: l.set}
		}
		for _, key := range l.env.Block {
			decided[key] = Origin{Key: key, Blocked: true, Kind: l.kind, Set: l.set}
		}
	}
	out := make([]Origin, 0, len(decided))
	for _, o := range decided {
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// LayerOrigin is one entry of one layer, and whether it is the one a launch
// uses (a later layer overrides an earlier one).
type LayerOrigin struct {
	Origin
	Effective bool
}

// ExplainAll is Explain keeping every layer's entries, not only the one
// that decides, in layer order (DEFAULTS sets, the playbook's sets, its own
// block), each marked Effective when it decides its key.
func ExplainAll(dir string, e *manifest.Env) ([]LayerOrigin, error) {
	decided, err := Explain(dir, e)
	if err != nil {
		return nil, err
	}
	wins := map[string]Origin{}
	for _, o := range decided {
		wins[o.Key] = o
	}
	defaults, err := Defaults(dir)
	if err != nil {
		return nil, &ResolveError{Name: DefaultMarker, Err: err}
	}
	var out []LayerOrigin
	add := func(kind, set string, env *manifest.Env) {
		var entries []Origin
		for key, ref := range env.Refs {
			entries = append(entries, Origin{Key: key, Ref: ref, Kind: kind, Set: set})
		}
		for key, value := range env.Set {
			entries = append(entries, Origin{Key: key, Value: value, Kind: kind, Set: set})
		}
		for _, key := range env.Block {
			entries = append(entries, Origin{Key: key, Blocked: true, Kind: kind, Set: set})
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
		for _, o := range entries {
			w := wins[o.Key]
			out = append(out, LayerOrigin{Origin: o, Effective: w.Kind == o.Kind && w.Set == o.Set})
		}
	}
	for _, name := range defaults {
		p, err := Read(dir, name)
		if err != nil || p == nil {
			continue // Explain above already refused a broken layer
		}
		add(LayerDefaults, name, p.Env())
	}
	if e != nil {
		for _, name := range e.Sets {
			p, err := Read(dir, name)
			if err != nil || p == nil {
				continue
			}
			add(LayerEnv, name, p.Env())
		}
		add(LayerPlaybook, "", &manifest.Env{Set: e.Set, Refs: e.Refs, Block: e.Block})
	}
	return out, nil
}
