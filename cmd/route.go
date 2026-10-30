package cmd

import (
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/ramazanpolat/claude-playbooks/internal/account"
	"github.com/ramazanpolat/claude-playbooks/internal/auth"
	"github.com/ramazanpolat/claude-playbooks/internal/envset"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

// routeJSON is EXPLAIN's route: where a launch from this environment sends
// its requests and how it authenticates. It carries non-secret values and
// states only. No reference is resolved and no secret helper runs. The login
// is judged by the same in-process reading of the stores that auth status
// does, so the two agree; no value, and no part of one, reaches the output.
type routeJSON struct {
	// BaseURL is the effective ANTHROPIC_BASE_URL with any userinfo removed
	// (not masked: absent); null when unset (Anthropic's own endpoint) or
	// when it comes from a reference (not resolved here).
	BaseURL *string `json:"base_url"`
	// Host is BaseURL's host[:port].
	Host *string `json:"host"`
	// Models: "default" from the launch's model (ANTHROPIC_MODEL, else the
	// playbook's settings), and one entry per ANTHROPIC_DEFAULT_<NAME>_MODEL,
	// lowercased.
	Models map[string]string `json:"models"`
	// Auth is a state, never a value: none | token-set | oauth-login |
	// unknown (the login could not be determined; never reported as none).
	Auth string `json:"auth"`
	// Egress is derived only: "anthropic" when the requests go to
	// api.anthropic.com over https (or no base URL is set and none of
	// Bedrock, Vertex and Foundry is switched on or given a base URL),
	// "unknown" otherwise. cpb names no other class.
	Egress string `json:"egress"`
	// Account is the account a launch with no flags runs on, and why
	// (v4.2.0); the route above is derived from it.
	Account routeAccountJSON `json:"account"`
	// Refused is why such a launch would be refused before anything
	// changes (a variable an account owns set elsewhere, a login it cannot
	// keep out, an account it may not use); null when it would run.
	Refused *string `json:"refused"`
}

// routeAccountJSON names the account a launch runs on.
type routeAccountJSON struct {
	Name     string `json:"name"`     // "machine" for the implicit account
	Why      string `json:"why"`      // --account, the playbook's first, DEFAULTS, account.WhyInherited, or a layer route's
	Provider string `json:"provider"` // anthropic for machine
}

var defaultModelVar = regexp.MustCompile(`^ANTHROPIC_DEFAULT_([A-Z0-9_]+)_MODEL$`)

// routeTokenVars are the credentials a launch sends with its requests; any
// of them set (a value or a reference) and not blocked makes it token-set.
var routeTokenVars = map[string]bool{
	"ANTHROPIC_API_KEY":    true,
	"ANTHROPIC_AUTH_TOKEN": true,
	auth.OAuthTokenEnv:     true,
}

// routeInherited is what a launch keeps from the environment it starts in:
// every route variable no layer sets or blocks. CLAUDE_CODE_OAUTH_TOKEN is
// left to the login report, which already decides it the way the launch
// does (an isolated launch removes an inherited one).
func routeInherited(origins []envset.Origin, environ []string) []envset.Origin {
	covered := map[string]bool{}
	for _, o := range origins {
		covered[o.Key] = true
	}
	var out []envset.Origin
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || covered[k] || k == auth.OAuthTokenEnv {
			continue
		}
		switch {
		case k == "ANTHROPIC_BASE_URL", k == "ANTHROPIC_MODEL", routeTokenVars[k],
			k == "CLAUDE_CODE_USE_BEDROCK", k == "CLAUDE_CODE_USE_VERTEX", k == "CLAUDE_CODE_USE_FOUNDRY",
			k == "ANTHROPIC_FOUNDRY_BASE_URL", defaultModelVar.MatchString(k):
			covered[k] = true // the first entry is the one a lookup sees
			out = append(out, envset.Origin{Key: k, Value: v})
		}
	}
	return out
}

// routeEgress is the egress class of a launch whose variables are origins:
// "anthropic" when its requests go to api.anthropic.com over https, or no
// base URL is set and none of Bedrock, Vertex and Foundry is switched on or
// given a base URL; "unknown" otherwise, a base URL from a reference or one
// that does not parse included. EXPLAIN's route and cpb ask's route check both use it.
func routeEgress(origins []envset.Origin) string {
	for _, o := range origins {
		if o.Blocked {
			continue
		}
		switch o.Key {
		case "ANTHROPIC_BASE_URL":
			if o.Ref != "" {
				return "unknown"
			}
			if o.Value == "" {
				continue
			}
			u, err := url.Parse(o.Value)
			if err != nil || u.Host == "" || !(u.Scheme == "https" && u.Hostname() == "api.anthropic.com") {
				return "unknown"
			}
		case "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY":
			if v := strings.ToLower(o.Value); o.Ref != "" || (v != "" && v != "0" && v != "false") {
				return "unknown"
			}
		case "ANTHROPIC_FOUNDRY_BASE_URL":
			// A Foundry endpoint is never Anthropic's own API.
			if o.Ref != "" || o.Value != "" {
				return "unknown"
			}
		}
	}
	return "anthropic"
}

func launchRoute(pb *playbook.Playbook, origins []envset.Origin, model *modelJSON) routeJSON {
	r := routeJSON{Models: map[string]string{}, Auth: "none", Egress: "anthropic"}
	// The account decides the route: the same checks a launch makes, none
	// of which changes anything.
	wd, _ := os.Getwd()
	acct, err := planAccount(pb.Path, pb.Name, nil, wd, nil)
	if err != nil {
		msg := err.Error()
		r.Refused, r.Egress, r.Auth = &msg, "unknown", "unknown"
		return r
	}
	r.Account = routeAccountJSON{Name: acct.sel.Name, Why: acct.sel.Why, Provider: account.Anthropic}
	if acct.sel.Account != nil {
		r.Account.Provider = acct.sel.Account.Provider
	}
	if model != nil && model.Name != "" {
		r.Models["default"] = model.Name
	}
	// What the account renders; on the machine account the playbook's own
	// layers still decide the token, as a launch does.
	layer := acct.rendered.Layer
	routeOrigins := []envset.Origin{}
	for k, v := range layer.Set {
		routeOrigins = append(routeOrigins, envset.Origin{Key: k, Value: v})
	}
	for k, ref := range layer.Refs {
		routeOrigins = append(routeOrigins, envset.Origin{Key: k, Ref: ref})
	}
	// On the machine account the token is the token file's, which the
	// login report below reads; an exported one never counts (every launch
	// removes it), and a layer that sets one refuses the launch.
	if _, ok := r.Models["default"]; !ok && layer.Set["ANTHROPIC_DEFAULT_MODEL"] != "" {
		r.Models["default"] = layer.Set["ANTHROPIC_DEFAULT_MODEL"]
	}
	origins = routeOrigins
	tokenSet := false
	isolated := auth.IsAuthIsolated(pb.Path)
	for _, o := range origins {
		if o.Blocked {
			continue
		}
		switch {
		case o.Key == "ANTHROPIC_BASE_URL":
			if o.Ref != "" || o.Value == "" {
				continue
			}
			u, err := url.Parse(o.Value)
			if err != nil || u.Host == "" {
				continue // unparsable: say nothing about it, egress unknown
			}
			u.User = nil
			base, host := u.String(), u.Host
			r.BaseURL, r.Host = &base, &host
		case routeTokenVars[o.Key]:
			// An isolated launch drops a CLAUDE_CODE_OAUTH_TOKEN that only
			// DEFAULTS sets (the machine's layer), so it is not this route's.
			if o.Key == auth.OAuthTokenEnv && o.Kind == envset.LayerDefaults && isolated {
				continue
			}
			if o.Ref != "" || o.Value != "" {
				tokenSet = true
			}
		default:
			if m := defaultModelVar.FindStringSubmatch(o.Key); m != nil && o.Ref == "" && o.Value != "" {
				r.Models[strings.ToLower(m[1])] = o.Value
			}
		}
	}
	r.Egress = routeEgress(origins)
	// The login: the same report auth status prints, so the two agree.
	rep := auth.Inspect(pb.Name, pb.Path, time.Now())
	switch {
	case tokenSet || (acct.sel.Account == nil && (rep.Mode == auth.ModeToken || rep.Mode == auth.ModePlaybookToken)):
		r.Auth = "token-set"
	case acct.rendered.KeyHelper != "":
		r.Auth = "key-helper"
	case acct.rendered.NoClaudeLogin:
		r.Auth = "none"
	default:
		switch rep.Login() {
		case "store", "keychain":
			r.Auth = "oauth-login"
		case "unknown":
			r.Auth = "unknown"
		}
	}
	return r
}
