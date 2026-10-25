package cmd

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/envset"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

// askContext is everything cpb ask tells the model about this machine,
// besides the request. It holds names and classes only: no field can carry
// a variable's value, a reference, a description or a credential, and the
// value-bearing types (manifests, env sets) never cross into it. A test
// plants a canary in every value an install can hold and checks that none
// reaches claude.
type askContext struct {
	CPBVersion        string        `json:"cpb_version"`
	ClaudeCodeVersion string        `json:"claude_code_version,omitempty"`
	PlaybooksRoot     string        `json:"playbooks_root"`
	Playbooks         []askPlaybook `json:"playbooks"`
	EnvSets           []askEnvSet   `json:"env_sets"`
	Defaults          []string      `json:"defaults"`
	Unreadable        []string      `json:"unreadable_playbooks,omitempty"`
}

type askPlaybook struct {
	Name            string   `json:"name"`
	Launcher        string   `json:"launcher,omitempty"`
	EnvSets         []string `json:"env_sets,omitempty"`
	AlwaysSandboxed bool     `json:"always_sandboxed,omitempty"`
	Route           string   `json:"route"`
}

type askEnvSet struct {
	Name       string `json:"name"`
	InDefaults bool   `json:"in_defaults,omitempty"`
	Route      string `json:"route"`
}

// buildAskContext reads the registry under root by name.
func buildAskContext(root string) (*askContext, error) {
	actx := &askContext{
		CPBVersion:        Version,
		ClaudeCodeVersion: claudeVersion(),
		PlaybooksRoot:     tildePath(root),
		Playbooks:         []askPlaybook{},
		EnvSets:           []askEnvSet{},
		Defaults:          []string{},
	}
	pbs, bad, err := playbook.Scan(root)
	if err != nil {
		return nil, err
	}
	dir := envset.Dir(root)
	for _, pb := range pbs {
		ap := askPlaybook{Name: pb.Name, Launcher: pb.Alias()}
		var env *manifest.Env
		if m := pb.Manifest; m != nil {
			env = m.Env
			ap.AlwaysSandboxed = m.Sandbox != nil && m.Sandbox.Always
		}
		if env != nil {
			ap.EnvSets = append([]string(nil), env.Sets...)
		}
		ap.Route = askRoute(dir, env)
		actx.Playbooks = append(actx.Playbooks, ap)
	}
	for _, u := range bad {
		actx.Unreadable = append(actx.Unreadable, u.Name)
	}
	defaults, _ := envset.Defaults(dir) // an unreadable marker: every route below reads unknown
	actx.Defaults = append(actx.Defaults, defaults...)
	inDefaults := map[string]bool{}
	for _, n := range defaults {
		inDefaults[n] = true
	}
	// The env sets by file name: their content is read only by askRoute,
	// which returns a class.
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e.Name(), envset.FileExt); ok && !e.IsDir() && manifest.ValidateSetName(name) == nil {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		actx.EnvSets = append(actx.EnvSets, askEnvSet{Name: n, InDefaults: inDefaults[n],
			Route: askRoute(dir, &manifest.Env{Sets: []string{n}})})
	}
	return actx, nil
}

// askRoute is where a launch with env sends its requests, as a class:
// "anthropic", "away" (another endpoint, or one set by a reference), or
// "unknown" (a layer that does not resolve). DEFAULTS count, as at launch.
func askRoute(dir string, env *manifest.Env) string {
	origins, err := envset.Explain(dir, env)
	if err != nil {
		return "unknown"
	}
	if routeEgress(origins) == "anthropic" {
		return "anthropic"
	}
	return "away"
}

// tildePath spells a path below home with ~, so a home directory's name
// stays out of what is sent.
func tildePath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	rel, err := filepath.Rel(home, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return p
	}
	if rel == "." {
		return "~"
	}
	return "~/" + rel
}
