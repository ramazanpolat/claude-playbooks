package auth

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

// parseSubscription extracts the plan descriptors from a credential blob,
// ignoring everything else it contains -- notably the live access and refresh
// tokens, which are never copied out of the decoder.
func parseSubscription(data []byte) Subscription {
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

// readCredentialFile returns the global plaintext credential store, or nil.
func readCredentialFile() []byte {
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

// readCredentialKeychain returns the global Keychain item, or nil off darwin.
//
// Shelling out to `security` can block on a locked Keychain or raise a GUI
// authorization prompt, which is precisely what token auth exists to avoid, so
// this is the LAST source consulted rather than the first -- see
// GlobalSubscription.
func readCredentialKeychain() []byte {
	if runtime.GOOS != "darwin" {
		return nil
	}
	// The bare service name is the default config dir's item. Playbook config
	// dirs use a sha256-suffixed service, but the account's plan is a property
	// of the account, not of whichever playbook happens to be launching.
	out, err := findGenericPassword("Claude Code-credentials")
	if err != nil {
		return nil
	}
	if out = bytes.TrimSpace(out); len(out) > 0 && json.Valid(out) {
		return out
	}
	return nil
}

// GlobalSubscription reports the account's plan descriptors from the global
// credential store. A missing or unreadable store yields an empty Subscription
// rather than an error: the descriptors are an enhancement, and failing a
// playbook launch because a plan name could not be read would be worse than
// launching without them.
//
// Sources are tried cheapest-first and their descriptors are MERGED, with the
// earlier source winning any field both record. Merging rather than taking the
// first source that answers at all matters because the two descriptors are
// independent: a store can record subscriptionType without rateLimitTier, and
// returning early on it would silently drop a tier the next source does hold.
//
// A later source is read only while some field is still missing, which keeps
// the ordering's real point intact: the plaintext file is consulted before the
// Keychain, so a store recording both descriptors costs one file read and
// spawns no subprocess. Reversing that would put a `security` invocation -- and
// its potential GUI authorization prompt -- on every launch under token auth,
// defeating the reason token auth is used at all.
//
// Selection is on "has a descriptor", not on "is valid JSON": a store can be
// syntactically valid yet carry no plan descriptors (a bare `{}`, or a
// credential block holding only an access token), and choosing on validity
// would let such a store shadow a populated one and yield nothing.
//
// On the file-vs-Keychain precedence: Claude Code's composite store writes the
// Keychain first and DELETES the plaintext file once an item lands there, so
// the two coexist only when Keychain writes are failing -- and then the file is
// the live copy, not a stale leftover. Preferring the file is therefore both
// the cheap choice and the correct one; the case where a stale file shadows a
// current Keychain item is not one this storage model produces.
func GlobalSubscription() Subscription {
	var merged Subscription
	for _, read := range []func() []byte{readCredentialFile, readCredentialKeychain} {
		if merged.Type != "" && merged.RateLimitTier != "" {
			break
		}
		data := read()
		if data == nil {
			continue
		}
		sub := parseSubscription(data)
		if merged.Type == "" {
			merged.Type = sub.Type
		}
		if merged.RateLimitTier == "" {
			merged.RateLimitTier = sub.RateLimitTier
		}
	}
	return merged
}

// envHas reports whether env already carries an entry for key. An explicitly
// exported empty value (`FOO=`) counts as present: os.Getenv cannot tell that
// apart from unset, but the operator who typed it meant something by it.
func envHas(env []string, key string) bool {
	prefix := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return true
		}
	}
	return false
}

// appendSubscriptionEnv adds the plan descriptors to env for a token-authenticated
// child, leaving any value already present untouched -- an explicit export is a
// deliberate act and outranks what this process infers from disk. A Team seat
// needs exactly that: its real subscriptionType is not a value the picker
// accepts, so the operator must be able to override it.
//
// env is scanned rather than os.Getenv consulted, because env -- not this
// process's environment -- is what the child will actually receive, and the two
// diverge as soon as a caller appends to it.
//
// The result never aliases env's backing array, matching removeEnv's contract
// in token.go: appending in place would write past env's length into memory a
// caller may still append to itself.
func appendSubscriptionEnv(env []string) []string {
	missing := make([]string, 0, 2)
	for _, key := range [...]string{SubscriptionTypeEnv, RateLimitTierEnv} {
		if !envHas(env, key) {
			missing = append(missing, key)
		}
	}
	// Nothing to fill in: skip the credential read entirely rather than paying
	// for a store lookup whose every result would be discarded.
	if len(missing) == 0 {
		return env
	}

	sub := GlobalSubscription()
	if sub.Empty() {
		return env
	}
	values := map[string]string{
		SubscriptionTypeEnv: sub.Type,
		RateLimitTierEnv:    sub.RateLimitTier,
	}

	out := make([]string, len(env), len(env)+len(missing))
	copy(out, env)
	for _, key := range missing {
		if v := values[key]; v != "" {
			out = append(out, key+"="+v)
		}
	}
	return out
}
