package envprofile

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
	Blocked bool   // removed at launch
	Kind    string // LayerDefaults, LayerEnv or LayerPlaybook
	Set     string // the env set's name, for LayerDefaults and LayerEnv
}

// Explain is ExpandWithDefault keeping provenance: for each variable the
// launch would set or remove, the layer that decided it. It merges exactly
// as ExpandWithDefault does (defaults in order, then e's profiles in order,
// then e's own block; within a layer, sets then unsets), so the two cannot
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
		for _, name := range e.Profiles {
			if err := add(LayerEnv, name); err != nil {
				return nil, err
			}
		}
		layers = append(layers, layer{LayerPlaybook, "", &manifest.Env{Set: e.Set, Unset: e.Unset}})
	}

	decided := map[string]Origin{}
	for _, l := range layers {
		for key, value := range l.env.Set {
			decided[key] = Origin{Key: key, Value: value, Kind: l.kind, Set: l.set}
		}
		for _, key := range l.env.Unset {
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
