package auth

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// The login guard (v4.2.0, accounts). Claude Code sends its credential to
// whatever ANTHROPIC_BASE_URL names. Another provider issues its own key
// (ANTHROPIC_AUTH_TOKEN or ANTHROPIC_API_KEY), which outranks the Claude
// login; with no such key, the Claude login or token itself goes to that
// address. The guard refuses such a launch before anything is changed, and
// names the fixes. It reads presence only: never a credential's value.

// Guard verdicts.
const (
	GuardOK      = "ok"
	GuardRefused = "refused"
)

// GuardResult is the guard's verdict for one launch. Host is the request
// address's host ("" for Anthropic's default), Reason one line saying why.
type GuardResult struct {
	Verdict string `json:"verdict"`
	Host    string `json:"host,omitempty"`
	Reason  string `json:"reason"`
	// Sends names the Claude credential the launch would send: "token" or
	// "login". Empty when nothing would be sent.
	Sends string `json:"sends,omitempty"`
}

// providerKeyVars are the keys another provider authenticates with. Either
// one, set (a value or a reference) and not blocked, keeps the Claude
// credential at home.
var providerKeyVars = []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY"}

// cloudProviderVars route Claude Code to a cloud provider, which
// authenticates with that cloud's own credentials, never the Claude login.
var cloudProviderVars = []string{"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY"}

// Guard decides whether a launch of configDir under the effective block eff
// and the starting environment environ would send a Claude credential to an
// address that is not Anthropic's. It changes nothing.
func Guard(configDir string, eff *manifest.Env, environ []string) GuardResult {
	sets, helper := readSettingsAuth(configDir)
	lookup := func(key string) (string, bool, bool) {
		// settings.json's env block is applied by Claude Code over the
		// environment it starts with, so it wins.
		if v, ok := sets[key]; ok {
			return v, false, true
		}
		return lookupLaunch(eff, environ, key)
	}
	for _, k := range cloudProviderVars {
		if v, _, _ := lookup(k); isTruthy(v) {
			return GuardResult{Verdict: GuardOK, Reason: k + " routes to a cloud provider, which takes that cloud's credentials"}
		}
	}

	base, ref, set := lookup("ANTHROPIC_BASE_URL")
	host := ""
	switch {
	case ref:
		host = "the address a secret reference holds"
	case !set || strings.TrimSpace(base) == "":
		return GuardResult{Verdict: GuardOK, Reason: "requests go to Anthropic"}
	default:
		h, anthropic := anthropicEndpoint(base)
		if anthropic {
			return GuardResult{Verdict: GuardOK, Host: h, Reason: "requests go to Anthropic"}
		}
		host = h
	}

	for _, k := range providerKeyVars {
		if v, isRef, ok := lookup(k); ok && (isRef || strings.TrimSpace(v) != "") {
			return GuardResult{Verdict: GuardOK, Host: host, Reason: k + " is the provider's own key, and it outranks the Claude login"}
		}
	}
	if helper {
		return GuardResult{Verdict: GuardOK, Host: host, Reason: "settings.json's apiKeyHelper supplies the provider's key"}
	}

	sends := claudeCredentialReachable(configDir, eff)
	if v, ok := sets[OAuthTokenEnv]; ok && strings.TrimSpace(v) != "" {
		// settings.json's env block is applied by Claude Code over the
		// launch environment, a blocked variable included: its token is
		// sent whatever the layers say.
		sends = "token"
	}
	if sends != "" {
		return GuardResult{Verdict: GuardRefused, Host: host, Sends: sends,
			Reason: fmt.Sprintf("requests go to %s, which is not Anthropic, and no provider key is set: Claude Code would send your Claude %s there", host, sends)}
	}
	return GuardResult{Verdict: GuardOK, Host: host, Reason: "no Claude credential can reach this launch (isolated login, no token, not logged in)"}
}

// GuardError is the launch refusal for a refused verdict: the reason, then
// the fixes, each a statement to run. name is the playbook's name ("" for a
// path launched with cpb start).
func GuardError(r GuardResult, name string) error {
	target := "<playbook>"
	if name != "" {
		target = name
	}
	return fmt.Errorf("%s. Fix one of:\n"+
		"  - give the provider its own key, by reference: cpb ALTER ENV <set> SET ANTHROPIC_AUTH_TOKEN FROM '<ref>'\n"+
		"  - keep every Claude credential out: cpb ALTER PLAYBOOK %s SET login = 'isolated' BLOCK VAR CLAUDE_CODE_OAUTH_TOKEN (and /logout in it, if it holds a login of its own)",
		r.Reason, target)
}

// claudeCredentialReachable names the Claude credential a launch would
// carry: "token" when a token is active for it (the same decision
// PrepareLaunchEnv makes), "login" when a stored login is reachable, ""
// when none is. A shared launch always counts as "login": /login in it logs
// the machine in. A store that cannot be read counts as holding a login.
func claudeCredentialReachable(configDir string, eff *manifest.Env) string {
	if isAuthIsolated(configDir) {
		if suppliesToken(eff) {
			return "token"
		}
		// A store that is still a link (to the shared one) is detached
		// before Claude Code starts: what it reaches is not this
		// playbook's login.
		store := filepath.Join(configDir, CredentialsFileName)
		if info, err := os.Lstat(store); err == nil && info.Mode()&os.ModeSymlink == 0 {
			if absent, err := storeGrantAbsent(store); err != nil || !absent {
				return "login"
			}
		} else if err != nil && !os.IsNotExist(err) {
			return "login"
		}
		if keychainLogin(configDir, false, false) != KeychainAbsent {
			return "login"
		}
		return ""
	}
	if _, active := resolveToken(eff); active {
		return "token"
	}
	return "login"
}

// lookupLaunch returns what key holds at launch: the block's value or
// reference wins, a blocked key holds nothing, else the starting
// environment's value. ref reports a reference (value then "").
func lookupLaunch(eff *manifest.Env, environ []string, key string) (value string, ref, set bool) {
	if eff.Blocks(key) {
		return "", false, false
	}
	if eff != nil {
		if _, ok := eff.Refs[key]; ok {
			return "", true, true
		}
		if v, ok := eff.Set[key]; ok {
			return v, false, true
		}
	}
	for i := len(environ) - 1; i >= 0; i-- {
		if k, v, ok := strings.Cut(environ[i], "="); ok && k == key {
			return v, false, true
		}
	}
	return "", false, false
}

// anthropicEndpoint reports the host of base and whether it is Anthropic's:
// https, and anthropic.com or a subdomain of it.
func anthropicEndpoint(base string) (host string, anthropic bool) {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Host == "" {
		// Never echo the raw value: it may carry userinfo.
		return "an address that does not parse as a URL", false
	}
	h := strings.ToLower(u.Hostname())
	return u.Host, u.Scheme == "https" && (h == "anthropic.com" || strings.HasSuffix(h, ".anthropic.com"))
}

func isTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// readSettingsAuth reads what the playbook's settings.json contributes:
// its env block (Claude Code applies it over the starting environment) and
// whether it names an apiKeyHelper, which supplies the provider's key. An
// unreadable or absent file contributes nothing.
func readSettingsAuth(configDir string) (env map[string]string, keyHelper bool) {
	data, err := os.ReadFile(filepath.Join(configDir, "settings.json"))
	if err != nil {
		return nil, false
	}
	var s struct {
		APIKeyHelper string         `json:"apiKeyHelper"`
		Env          map[string]any `json:"env"`
	}
	if json.Unmarshal(data, &s) != nil {
		return nil, false
	}
	env = map[string]string{}
	for k, v := range s.Env {
		if str, ok := v.(string); ok {
			env[k] = str
		}
	}
	return env, strings.TrimSpace(s.APIKeyHelper) != ""
}
