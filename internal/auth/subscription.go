package auth

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
)

// SubscriptionTypeEnv and RateLimitTierEnv are the environment variables Claude
// Code consults when deciding plan entitlements in the interactive model picker.
//
// They matter only under long-lived OAuth token auth. With a token in
// CLAUDE_CODE_OAUTH_TOKEN, Claude Code's own account profile
// (~/.claude.json -> oauthAccount) carries no subscriptionType, so a
// client-side entitlement check cannot confirm the plan and fails closed --
// a Max/Team account is told the model "requires usage credits" even though the
// server grants it. See anthropics/claude-code#79597.
//
// Injecting these restores the descriptors the token path drops. The values are
// read from the account's own credential store rather than assumed, so a Pro
// seat is described as Pro and a Team seat as Team; nothing here upgrades an
// account or asserts an entitlement the credential store does not already
// record. That also keeps the injection harmless once the upstream check learns
// to fall back to the server: it only ever restates what the account is.
const (
	SubscriptionTypeEnv = "CLAUDE_CODE_SUBSCRIPTION_TYPE"
	RateLimitTierEnv    = "CLAUDE_CODE_RATE_LIMIT_TIER"
)

// Subscription holds the plan descriptors Claude Code drops under token auth.
// Either field may be empty when the credential store does not record it.
type Subscription struct {
	Type          string
	RateLimitTier string
}

// Empty reports whether nothing usable was found, so callers can skip injection
// rather than exporting empty variables. An empty CLAUDE_CODE_SUBSCRIPTION_TYPE
// is not documented to mean "unknown", and exporting one risks reading as a
// definite answer the credential store never gave.
func (s Subscription) Empty() bool { return s.Type == "" && s.RateLimitTier == "" }

// globalCredentialsJSON returns the raw global credentials JSON, or nil.
//
// This deliberately does NOT reuse EnsureGlobalCredentials: that function
// materializes the Keychain item into ~/.claude/.credentials.json as a side
// effect. Under token auth PrepareLaunchEnv otherwise touches no credential
// file, and writing a plaintext copy of the account's refresh token merely to
// learn its plan name would be a poor trade. Read, do not persist.
func globalCredentialsJSON() []byte {
	if runtime.GOOS == "darwin" {
		// The bare service name is the default config dir's item. Playbook config
		// dirs use a sha256-suffixed service, but the account's plan is a property
		// of the account, not of whichever playbook happens to be launching.
		if out, err := findGenericPassword("Claude Code-credentials"); err == nil {
			if out = bytes.TrimSpace(out); len(out) > 0 && json.Valid(out) {
				return out
			}
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(home, ".claude", CredentialsFileName))
	if err != nil {
		return nil
	}
	if data = bytes.TrimSpace(data); len(data) > 0 && json.Valid(data) {
		return data
	}
	return nil
}

// GlobalSubscription reports the account's plan descriptors from the global
// credential store. A missing or unreadable store yields an empty Subscription
// rather than an error: the descriptors are an enhancement, and failing a
// playbook launch because a plan name could not be read would be worse than
// launching without them.
func GlobalSubscription() Subscription {
	data := globalCredentialsJSON()
	if data == nil {
		return Subscription{}
	}
	var parsed struct {
		ClaudeAiOauth struct {
			SubscriptionType string `json:"subscriptionType"`
			RateLimitTier    string `json:"rateLimitTier"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return Subscription{}
	}
	return Subscription{
		Type:          parsed.ClaudeAiOauth.SubscriptionType,
		RateLimitTier: parsed.ClaudeAiOauth.RateLimitTier,
	}
}

// appendSubscriptionEnv adds the plan descriptors to env for a token-authenticated
// child, leaving any value the caller already exported untouched -- an explicit
// override in the user's shell is a deliberate act and outranks what this
// process infers from disk.
func appendSubscriptionEnv(env []string) []string {
	sub := GlobalSubscription()
	if sub.Empty() {
		return env
	}
	for _, kv := range [...]struct{ key, value string }{
		{SubscriptionTypeEnv, sub.Type},
		{RateLimitTierEnv, sub.RateLimitTier},
	} {
		if kv.value == "" || os.Getenv(kv.key) != "" {
			continue
		}
		env = append(env, kv.key+"="+kv.value)
	}
	return env
}
