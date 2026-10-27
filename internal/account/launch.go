package account

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// Reserved are the variables an account owns (SPEC.md, "Derived at
// launch"): where requests go, how they authenticate, and which models
// they name. A launch removes every one of them from the environment it
// starts with and sets exactly what its account renders; no other layer may
// set one.
var Reserved = []string{
	// routing
	"ANTHROPIC_BASE_URL", "ANTHROPIC_BEDROCK_BASE_URL", "ANTHROPIC_BEDROCK_MANTLE_BASE_URL",
	"ANTHROPIC_VERTEX_BASE_URL", "ANTHROPIC_FOUNDRY_BASE_URL", "ANTHROPIC_AWS_BASE_URL",
	"ANTHROPIC_UNIX_SOCKET",
	"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY",
	"CLAUDE_CODE_USE_MANTLE", "CLAUDE_CODE_USE_ANTHROPIC_AWS",
	"CLAUDE_CODE_SKIP_BEDROCK_AUTH", "CLAUDE_CODE_SKIP_VERTEX_AUTH", "CLAUDE_CODE_SKIP_FOUNDRY_AUTH",
	"CLAUDE_CODE_SKIP_MANTLE_AUTH", "CLAUDE_CODE_SKIP_ANTHROPIC_AWS_AUTH",
	// credentials
	"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", OAuthTokenVar,
	"CLAUDE_CODE_OAUTH_REFRESH_TOKEN", "CLAUDE_CODE_OAUTH_SCOPES",
	"ANTHROPIC_CUSTOM_HEADERS",
	"ANTHROPIC_FOUNDRY_API_KEY", "ANTHROPIC_FOUNDRY_AUTH_TOKEN", "ANTHROPIC_AWS_API_KEY",
	"AWS_BEARER_TOKEN_BEDROCK",
	"CLAUDE_CODE_CLIENT_CERT", "CLAUDE_CODE_CLIENT_KEY", "CLAUDE_CODE_CLIENT_KEY_PASSPHRASE",
	// Anthropic profiles and federation
	"ANTHROPIC_PROFILE", ConfigDirVar,
	"ANTHROPIC_FEDERATION_RULE_ID", "ANTHROPIC_ORGANIZATION_ID", "ANTHROPIC_SERVICE_ACCOUNT_ID",
	"ANTHROPIC_IDENTITY_TOKEN", "ANTHROPIC_IDENTITY_TOKEN_FILE", "ANTHROPIC_WORKSPACE_ID",
	// models
	"ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_MODEL",
	"ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_FABLE_MODEL",
	"ANTHROPIC_SMALL_FAST_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL",
}

// OAuthTokenVar is the Claude subscription token variable.
const OAuthTokenVar = "CLAUDE_CODE_OAUTH_TOKEN"

// ConfigDirVar locates Anthropic profiles (~/.config/anthropic by default),
// which Claude Code reads with no variable set: every launch points it at
// an empty directory of cpb's, so a profile is never the credential.
const ConfigDirVar = "ANTHROPIC_CONFIG_DIR"

// IsReserved reports a variable an account owns.
func IsReserved(key string) bool { return slices.Contains(Reserved, key) }

// modelVars are the variables the model keys render as.
var modelVars = map[string]string{
	"model":        "ANTHROPIC_DEFAULT_MODEL",
	"opus_model":   "ANTHROPIC_DEFAULT_OPUS_MODEL",
	"sonnet_model": "ANTHROPIC_DEFAULT_SONNET_MODEL",
	"haiku_model":  "ANTHROPIC_DEFAULT_HAIKU_MODEL",
	"fable_model":  "ANTHROPIC_DEFAULT_FABLE_MODEL",
}

// cloudSwitch and cloudBaseURL are what a cloud account renders.
var cloudSwitch = map[string]string{Bedrock: "CLAUDE_CODE_USE_BEDROCK", Vertex: "CLAUDE_CODE_USE_VERTEX", Foundry: "CLAUDE_CODE_USE_FOUNDRY"}
var cloudBaseURL = map[string]string{Bedrock: "ANTHROPIC_BEDROCK_BASE_URL", Vertex: "ANTHROPIC_VERTEX_BASE_URL", Foundry: "ANTHROPIC_FOUNDRY_BASE_URL"}

// CredentialVar is the variable a secret credential kind renders as.
var CredentialVar = map[string]string{
	AuthToken:   "ANTHROPIC_AUTH_TOKEN",
	APIKey:      "ANTHROPIC_API_KEY",
	ClaudeToken: OAuthTokenVar,
}

// Selection is the account a launch runs on, and why.
type Selection struct {
	Name    string   // Machine for the implicit account
	Account *Account // nil for Machine
	Why     string   // "--account", "the playbook's first", "DEFAULTS", "none named"
}

// Select picks the account a launch runs on (SPEC.md, "Which account a
// launch runs on"): --account, else the playbook's first, else DEFAULTS,
// else the implicit account. list is the playbook's USE ACCOUNT list, flag
// the --account value ("" for none). Both sides are allow-lists: a playbook
// with a list runs only on what it lists, and a listed account only for a
// playbook that lists it.
func Select(dir, playbookName string, list []string, flag string) (Selection, error) {
	label := "this playbook"
	if playbookName != "" {
		label = "playbook " + playbookName
	}
	pick := func(name, why string) (Selection, error) {
		if name == Machine {
			return Selection{Name: Machine, Why: why}, nil
		}
		a, err := Must(dir, name)
		if err != nil {
			return Selection{}, err
		}
		if a.Listed() && !slices.Contains(list, name) {
			return Selection{}, fmt.Errorf("account %q is access = 'listed': it runs only for a playbook that lists it, and %s does not (ALTER PLAYBOOK <playbook> USE ACCOUNT …, %s)", name, label, name)
		}
		return Selection{Name: name, Account: a, Why: why}, nil
	}
	if flag != "" {
		if len(list) > 0 && !slices.Contains(list, flag) {
			return Selection{}, fmt.Errorf("%s runs only on the accounts it lists (%s): --account %s is not one of them", label, strings.Join(list, ", "), flag)
		}
		return pick(flag, "--account")
	}
	if len(list) > 0 {
		return pick(list[0], "the playbook's first")
	}
	def, err := Default(dir)
	if err != nil {
		return Selection{}, &ResolveError{Name: DefaultMarker, Err: err}
	}
	if def != "" {
		return pick(def, "DEFAULTS")
	}
	return Selection{Name: Machine, Why: "none named"}, nil
}

// Rendered is what an account puts into a launch.
type Rendered struct {
	// Layer is the launch's last env layer: the variables the account sets
	// (Set, and Refs for credentials by reference, which the secret helper
	// sets at exec) and every other reserved variable blocked, so nothing
	// inherited survives.
	Layer *manifest.Env
	// KeyHelper is an apiKeyHelper command, passed to Claude Code as a
	// launch-only setting; "" for none.
	KeyHelper string
	// NoClaudeLogin: the account authenticates without a Claude login or
	// token, so none may be reachable during the run.
	NoClaudeLogin bool
}

// Render is what s puts into a launch. configDirVar is the directory
// ANTHROPIC_CONFIG_DIR points at: empty, cpb's, so no Anthropic profile is
// read.
func (s Selection) Render(configDirVar string) Rendered {
	set := map[string]string{ConfigDirVar: configDirVar}
	refs := map[string]string{}
	r := Rendered{}
	if a := s.Account; a != nil {
		switch a.Provider {
		case Anthropic, Gateway:
			if a.BaseURL != "" {
				set["ANTHROPIC_BASE_URL"] = a.BaseURL
			}
		default:
			set[cloudSwitch[a.Provider]] = "1"
			if a.BaseURL != "" {
				set[cloudBaseURL[a.Provider]] = a.BaseURL
			}
		}
		switch a.Auth.Kind {
		case AuthToken, APIKey, ClaudeToken:
			if a.Auth.Ref != "" {
				refs[CredentialVar[a.Auth.Kind]] = a.Auth.Ref
			} else {
				set[CredentialVar[a.Auth.Kind]] = a.Auth.Value
			}
		case Helper:
			r.KeyHelper = a.Auth.Helper
		}
		for _, k := range ModelKeys {
			if m := a.ModelOf(k); m != "" {
				set[modelVars[k]] = m
			}
		}
		r.NoClaudeLogin = !a.UsesClaudeCredential()
	}
	var block []string
	for _, k := range Reserved {
		_, inSet := set[k]
		_, inRefs := refs[k]
		// The implicit account keeps today's token decision: the machine's
		// token file on a shared playbook, or none on an isolated one.
		if inSet || inRefs || (s.Account == nil && k == OAuthTokenVar) {
			continue
		}
		block = append(block, k)
	}
	if len(refs) == 0 {
		refs = nil
	}
	r.Layer = &manifest.Env{Set: set, Refs: refs, Block: block}
	return r
}

// Describe says, for a human, what the account renders: never a value.
func (s Selection) Describe() string {
	a := s.Account
	if a == nil {
		return fmt.Sprintf("%s (Anthropic, the playbook's own login; %s)", Machine, s.Why)
	}
	where := a.Provider
	if a.BaseURL != "" {
		where += " " + a.BaseURL
	}
	cred := a.Auth.Kind
	switch {
	case a.Auth.Ref != "":
		cred += " from a reference"
	case a.Auth.Value != "":
		cred += " (plaintext)"
	case a.Auth.Kind == Helper:
		cred = "an apiKeyHelper"
	case a.Auth.Kind == ClaudeLogin:
		cred = "the playbook's own Claude login"
	}
	return fmt.Sprintf("%s (%s, %s; %s)", s.Name, where, cred, s.Why)
}
