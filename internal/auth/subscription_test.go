package auth

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// stubKeychain replaces the Keychain lookup for the duration of a test.
// Returning an error models "no such item", which is what makes the non-darwin
// and darwin paths converge on the file fallback.
func stubKeychain(t *testing.T, out []byte, err error) {
	t.Helper()
	prev := findGenericPassword
	findGenericPassword = func(string) ([]byte, error) { return out, err }
	t.Cleanup(func() { findGenericPassword = prev })
}

// writeGlobalCreds seeds $HOME/.claude/.credentials.json with raw body.
func writeGlobalCreds(t *testing.T, body string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, CredentialsFileName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestGlobalSubscriptionReadsFile(t *testing.T) {
	stubKeychain(t, nil, errors.New("no such item"))
	writeGlobalCreds(t, `{"claudeAiOauth":{"subscriptionType":"pro","rateLimitTier":"tier_x"}}`)

	got := GlobalSubscription()
	if got.Type != "pro" || got.RateLimitTier != "tier_x" {
		t.Fatalf("GlobalSubscription() = %+v, want {pro tier_x}", got)
	}
	if got.Empty() {
		t.Fatal("Empty() = true for a populated subscription")
	}
}

// Malformed or unexpected stores must degrade to "nothing found" rather than
// erroring or panicking: the descriptors are an enhancement, and a launch must
// never fail because a plan name could not be parsed.
func TestGlobalSubscriptionDegradesGracefully(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"malformed json", `{"claudeAiOauth":{`},
		{"empty object", `{}`},
		{"no oauth block", `{"mcpOAuth":{"notion":{"accessToken":"x"}}}`},
		{"null oauth block", `{"claudeAiOauth":null}`},
		{"descriptors absent", `{"claudeAiOauth":{"accessToken":"x"}}`},
		{"empty file", ``},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubKeychain(t, nil, errors.New("no such item"))
			writeGlobalCreds(t, tc.body)

			got := GlobalSubscription()
			if !got.Empty() {
				t.Fatalf("GlobalSubscription() = %+v, want empty", got)
			}
		})
	}
}

func TestGlobalSubscriptionMissingStore(t *testing.T) {
	stubKeychain(t, nil, errors.New("no such item"))
	t.Setenv("HOME", t.TempDir()) // no .claude directory at all

	if got := GlobalSubscription(); !got.Empty() {
		t.Fatalf("GlobalSubscription() = %+v, want empty", got)
	}
}

// On darwin the Keychain is the primary store; the plaintext file is only a
// fallback. A regression that reversed this would read a stale file while the
// Keychain held the current account.
func TestGlobalSubscriptionPrefersKeychainOnDarwin(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Keychain is consulted only on darwin")
	}
	stubKeychain(t, []byte(`{"claudeAiOauth":{"subscriptionType":"from_keychain"}}`), nil)
	writeGlobalCreds(t, `{"claudeAiOauth":{"subscriptionType":"from_file"}}`)

	if got := GlobalSubscription(); got.Type != "from_keychain" {
		t.Fatalf("Type = %q, want the Keychain's value", got.Type)
	}
}

// A Keychain item holding garbage must not shadow a usable file: json.Valid
// gates the Keychain branch precisely so a corrupt item degrades to the
// fallback instead of yielding nothing.
func TestGlobalSubscriptionFallsBackWhenKeychainInvalid(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Keychain is consulted only on darwin")
	}
	stubKeychain(t, []byte(`not json at all`), nil)
	writeGlobalCreds(t, `{"claudeAiOauth":{"subscriptionType":"from_file"}}`)

	if got := GlobalSubscription(); got.Type != "from_file" {
		t.Fatalf("Type = %q, want the file's value", got.Type)
	}
}

// appendSubscriptionEnv must never emit a bare key or an empty value, and must
// leave an operator's explicit export untouched.
func TestAppendSubscriptionEnvRespectsExistingExport(t *testing.T) {
	stubKeychain(t, nil, errors.New("no such item"))
	writeGlobalCreds(t, `{"claudeAiOauth":{"subscriptionType":"pro","rateLimitTier":"tier_x"}}`)
	t.Setenv(SubscriptionTypeEnv, "operator_choice")

	got := appendSubscriptionEnv(nil)
	for _, kv := range got {
		if kv == SubscriptionTypeEnv+"=pro" {
			t.Fatalf("overrode the operator's exported %s", SubscriptionTypeEnv)
		}
	}
	found := false
	for _, kv := range got {
		if kv == RateLimitTierEnv+"=tier_x" {
			found = true
		}
	}
	if !found {
		t.Fatalf("appendSubscriptionEnv() = %v, want it to still add %s", got, RateLimitTierEnv)
	}
}
