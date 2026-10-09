package cmd

import (
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

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
	// api.anthropic.com over https (or no base URL is set and neither Bedrock
	// nor Vertex is switched on), "unknown" otherwise. cpb names no other class.
	Egress string `json:"egress"`
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
			k == "CLAUDE_CODE_USE_BEDROCK", k == "CLAUDE_CODE_USE_VERTEX", defaultModelVar.MatchString(k):
			covered[k] = true // the first entry is the one a lookup sees
			out = append(out, envset.Origin{Key: k, Value: v})
		}
	}
	return out
}

func launchRoute(pb *playbook.Playbook, origins []envset.Origin, model *modelJSON) routeJSON {
	r := routeJSON{Models: map[string]string{}, Auth: "none", Egress: "anthropic"}
	if model != nil && model.Name != "" {
		r.Models["default"] = model.Name
	}
	inherited := routeInherited(origins, os.Environ())
	for _, o := range inherited {
		// An inherited model wins over the playbook's settings, as it does
		// in Claude Code; a layer that sets or blocks it is already in model.
		if o.Key == "ANTHROPIC_MODEL" && o.Value != "" {
			r.Models["default"] = o.Value
		}
	}
	origins = append(append([]envset.Origin{}, origins...), inherited...)
	tokenSet := false
	cloud := false
	baseFromRef := false
	for _, o := range origins {
		if o.Blocked {
			continue
		}
		switch {
		case o.Key == "ANTHROPIC_BASE_URL":
			if o.Ref != "" {
				baseFromRef = true
				continue
			}
			if o.Value == "" {
				continue
			}
			u, err := url.Parse(o.Value)
			if err != nil || u.Host == "" {
				baseFromRef = true // unparsable: say nothing about it, egress unknown
				continue
			}
			u.User = nil
			base, host := u.String(), u.Host
			r.BaseURL, r.Host = &base, &host
			if !(u.Scheme == "https" && u.Hostname() == "api.anthropic.com") {
				r.Egress = "unknown"
			}
		case routeTokenVars[o.Key]:
			if o.Ref != "" || o.Value != "" {
				tokenSet = true
			}
		case o.Key == "CLAUDE_CODE_USE_BEDROCK" || o.Key == "CLAUDE_CODE_USE_VERTEX":
			if v := strings.ToLower(o.Value); o.Ref != "" || (v != "" && v != "0" && v != "false") {
				cloud = true
			}
		default:
			if m := defaultModelVar.FindStringSubmatch(o.Key); m != nil && o.Ref == "" && o.Value != "" {
				r.Models[strings.ToLower(m[1])] = o.Value
			}
		}
	}
	if baseFromRef || cloud {
		r.Egress = "unknown"
	}
	// The login: the same report auth status prints, so the two agree.
	rep := auth.Inspect(pb.Name, pb.Path, time.Now())
	switch {
	case tokenSet || rep.Mode == auth.ModeToken || rep.Mode == auth.ModePlaybookToken:
		r.Auth = "token-set"
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
