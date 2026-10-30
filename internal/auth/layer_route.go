package auth

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/account"
	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envset"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// A route stated in one place: a playbook that names no account (and runs
// where DEFAULTS names none) may still say where its requests go, as long as
// the address and its key come together from one layer: one env set the
// playbook uses, its own block (SET VAR), or the launch's flags. Such a
// route runs as an account derived for the launch, so every account guard
// applies (the address rules, a Claude login kept out, nothing exported
// standing in). What stays refused: an address from one layer and its key
// from another, a route from DEFAULTS (it would reach every playbook), and
// the variables only an account may say (a Claude token, a cloud provider,
// client certificates and the rest).

// LayerRoute is a route stated in one layer, as an account.
type LayerRoute struct {
	// Account is the derived record: never written, never listed.
	Account *account.Account
	// Name labels it where an account's name goes (env:<set>, own-block,
	// launch-flags); never a stored account's name, which has no colon and
	// is never one of the other two.
	Name string
	// Why says where it was stated, for EXPLAIN and errors.
	Why string
	// Headers is ANTHROPIC_CUSTOM_HEADERS from the same layer, passed along
	// as it is: a value, or a reference (HeadersRef).
	Headers, HeadersRef string
}

// Route-carrying names that a layer route may set: the address, exactly one
// key, and custom headers. Every other reserved variable but the model ones
// stays an account's alone.
var (
	routeAddress = "ANTHROPIC_BASE_URL"
	routeKeys    = []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY"}
	routeHeaders = "ANTHROPIC_CUSTOM_HEADERS"
	// routeModels map a layer's model variables to the account's keys.
	routeModels = map[string]string{
		"ANTHROPIC_MODEL":                "model",
		"ANTHROPIC_DEFAULT_MODEL":        "model",
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   "opus_model",
		"ANTHROPIC_DEFAULT_SONNET_MODEL": "sonnet_model",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":  "haiku_model",
		"ANTHROPIC_DEFAULT_FABLE_MODEL":  "fable_model",
		"ANTHROPIC_SMALL_FAST_MODEL":     "haiku_model", // its older name
	}
)

// ErrLayerRoute marks a route a layer states that cannot run as one.
var ErrLayerRoute = errors.New("this launch's route cannot run")

// LayerRouteFor reads the route the layers of a launch of configDir state,
// when the launch's account is inherited: the playbook names none and
// DEFAULTS names none (sel.Why == account.WhyInherited). It returns nil when
// no layer routes (the reserved variables, if any, are judged as before:
// refused), the route when one layer states it whole, and an error wrapping
// ErrLayerRoute, naming layers and keys and never a value, when the layers
// route but not as one. layers are the launch's flag layers (nil for
// EXPLAIN); eff is EffectiveBlock(configDir, layers).
func LayerRouteFor(configDir string, layers []*manifest.Env, eff *manifest.Env) (*LayerRoute, error) {
	if eff == nil {
		return nil, nil
	}
	reserved := map[string]bool{}
	for k := range eff.Set {
		if account.IsReserved(k) {
			reserved[k] = true
		}
	}
	for k := range eff.Refs {
		if account.IsReserved(k) {
			reserved[k] = true
		}
	}
	if !reserved[routeAddress] {
		// No address, no route: a key or a model on its own is not one.
		return nil, nil
	}
	origin, err := reservedOrigins(configDir, layers, reserved)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(reserved))
	for k := range reserved {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	where := origin[routeAddress]
	var others []string
	for _, k := range keys {
		_, model := routeModels[k]
		switch {
		case model:
			if origin[k].kind == envset.LayerDefaults {
				others = append(others, k+" (set by "+origin[k].label()+": DEFAULTS never routes)")
			}
		case k == routeAddress || k == routeHeaders || slices.Contains(routeKeys, k):
			if origin[k] != where {
				others = append(others, k+" (set by "+origin[k].label()+", the address by "+where.label()+")")
			}
		default:
			others = append(others, k+" (set by "+origin[k].label()+": only an account says it)")
		}
	}
	if where.kind == envset.LayerDefaults {
		return nil, fmt.Errorf("%w: ANTHROPIC_BASE_URL is set by %s, and DEFAULTS reaches every playbook, so it never routes one: give DEFAULTS an account (ALTER DEFAULTS USE ACCOUNT <account>) or state the route in the playbook's own env set", ErrLayerRoute, where.label())
	}
	if len(others) > 0 {
		return nil, fmt.Errorf("%w: the address is stated in %s, but %s; a route a layer states comes from that one layer, its address and its key together", ErrLayerRoute, where.label(), strings.Join(others, "; "))
	}

	var kind string
	for _, k := range routeKeys {
		if reserved[k] {
			if kind != "" {
				return nil, fmt.Errorf("%w: %s states both ANTHROPIC_AUTH_TOKEN and ANTHROPIC_API_KEY; a route has one key", ErrLayerRoute, where.label())
			}
			kind = k
		}
	}
	if kind == "" {
		return nil, fmt.Errorf("%w: %s states an address with no key (ANTHROPIC_AUTH_TOKEN or ANTHROPIC_API_KEY): a route a layer states gives both; for a router that takes no key, CREATE ACCOUNT <name> SET provider = 'gateway', base_url = '…', auth = 'none'", ErrLayerRoute, where.label())
	}

	a := &account.Account{Provider: account.Gateway, BaseURL: eff.Set[routeAddress]}
	if a.BaseURL == "" {
		return nil, fmt.Errorf("%w: %s gives ANTHROPIC_BASE_URL by reference; an address is never a secret: state it as a value", ErrLayerRoute, where.label())
	}
	if u, err := account.ParseBaseURL(a.BaseURL); err == nil && account.IsAnthropicURL(u) {
		a.Provider = account.Anthropic
	}
	authKind := account.AuthToken
	if kind == "ANTHROPIC_API_KEY" {
		authKind = account.APIKey
	}
	a.Auth = account.Auth{Kind: authKind, Ref: eff.Refs[kind], Value: eff.Set[kind]}
	if a.Auth.Ref != "" {
		a.Auth.Value = ""
	}
	// Models: the explicit name wins over its older alias.
	for _, k := range []string{"ANTHROPIC_SMALL_FAST_MODEL", "ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_MODEL",
		"ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_FABLE_MODEL"} {
		if v := eff.Set[k]; v != "" {
			a.SetModel(routeModels[k], v)
		}
	}
	r := &LayerRoute{Account: a, Name: where.name(), Why: "a route stated in " + where.label(),
		Headers: eff.Set[routeHeaders], HeadersRef: eff.Refs[routeHeaders]}
	a.Name = r.Name
	if err := a.Validate(); err != nil {
		return nil, fmt.Errorf("%w: the route stated in %s: %v", ErrLayerRoute, where.label(), err)
	}
	return r, nil
}

// layerOrigin is the layer that decided a variable.
type layerOrigin struct {
	kind string // envset.LayerDefaults, LayerEnv, LayerPlaybook, or "flags"
	set  string
}

const layerFlags = "flags"

func (o layerOrigin) label() string {
	switch o.kind {
	case envset.LayerDefaults:
		return "ENV " + o.set + " (in DEFAULTS)"
	case envset.LayerEnv:
		return "ENV " + o.set
	case envset.LayerPlaybook:
		return "the playbook's own block (SET VAR)"
	}
	return "the launch's flags (--env, --env-set, --env-file)"
}

func (o layerOrigin) name() string {
	switch o.kind {
	case envset.LayerEnv:
		return "env:" + o.set
	case envset.LayerPlaybook:
		return "own-block"
	}
	return "launch-flags"
}

// reservedOrigins says which layer decided each reserved variable: the
// launch's flags when one of them sets it (they come last), else what
// Explain says of the manifest's layers.
func reservedOrigins(configDir string, layers []*manifest.Env, keys map[string]bool) (map[string]layerOrigin, error) {
	dir := envset.Dir(config.ResolvePlaybooksDir())
	out := map[string]layerOrigin{}
	var block *manifest.Env
	if m, _ := manifest.Nearest(configDir); m != nil {
		block = m.Env
	}
	origins, err := envset.Explain(dir, block)
	if err != nil {
		return nil, err
	}
	for _, o := range origins {
		if keys[o.Key] && !o.Blocked {
			out[o.Key] = layerOrigin{kind: o.Kind, set: o.Set}
		}
	}
	for _, l := range layers {
		expanded, err := envset.Expand(dir, l)
		if err != nil {
			return nil, err
		}
		if expanded == nil {
			continue
		}
		for k := range keys {
			_, s := expanded.Set[k]
			_, r := expanded.Refs[k]
			if s || r {
				out[k] = layerOrigin{kind: layerFlags}
			}
		}
	}
	return out, nil
}
