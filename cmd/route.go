package cmd

import (
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/ramazanpolat/claude-playbooks/internal/auth"
	"github.com/ramazanpolat/claude-playbooks/internal/envset"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

// routeJSON is EXPLAIN's route: where a launch's requests go and how it
// authenticates, from non-secret values only. No reference is resolved and
// no credential is read to answer it.
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

func launchRoute(pb *playbook.Playbook, origins []envset.Origin, model *modelJSON) routeJSON {
	r := routeJSON{Models: map[string]string{}, Auth: "none", Egress: "anthropic"}
	if model != nil && model.Name != "" {
		r.Models["default"] = model.Name
	}
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
