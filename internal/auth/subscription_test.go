package auth

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// stubKeychain replaces the Keychain lookup and returns a pointer to a call
// counter, so a test can assert the lookup was NOT reached -- the cheap-first
// ordering is only meaningful if the expensive source stays untouched.
func stubKeychain(t *testing.T, out []byte, err error) *int {
	t.Helper()
	calls := 0
	prev := findGenericPassword
	findGenericPassword = func(string) ([]byte, error) {
		calls++
		return out, err
	}
	t.Cleanup(func() { findGenericPassword = prev })
	return &calls
}

// homeWithoutCreds points HOME at an empty directory.
func homeWithoutCreds(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
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
	homeWithoutCreds(t)

	if got := GlobalSubscription(); !got.Empty() {
		t.Fatalf("GlobalSubscription() = %+v, want empty", got)
	}
}

// Selection is on "has a descriptor", not "is valid JSON". A store can be
// syntactically valid yet carry no plan descriptors -- a bare {}, or a
// credential block holding only an access token. Choosing on validity alone
// would let such a store shadow a populated one and yield nothing.
func TestGlobalSubscriptionSkipsValidButDescriptorlessSource(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Keychain is consulted only on darwin")
	}
	for _, body := range []string{`{}`, `{"claudeAiOauth":{"accessToken":"x"}}`} {
		t.Run(body, func(t *testing.T) {
			stubKeychain(t, []byte(`{"claudeAiOauth":{"subscriptionType":"from_keychain"}}`), nil)
			writeGlobalCreds(t, body) // valid JSON, no descriptors

			if got := GlobalSubscription(); got.Type != "from_keychain" {
				t.Fatalf("Type = %q, want the Keychain's value; a descriptorless "+
					"file shadowed a populated Keychain", got.Type)
			}
		})
	}
}

// The same rule from the other side, and the actual regression this fixes: a
// Keychain item holding valid JSON with no descriptors must not suppress a
// populated file. Selecting on validity rather than content returned empty here
// and ignored a perfectly good file.
func TestGlobalSubscriptionDescriptorlessKeychainDoesNotShadowFile(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Keychain is consulted only on darwin")
	}
	stubKeychain(t, []byte(`{}`), nil)
	writeGlobalCreds(t, `{"claudeAiOauth":{"subscriptionType":"from_file"}}`)

	if got := GlobalSubscription(); got.Type != "from_file" {
		t.Fatalf("Type = %q, want the file's value; a descriptorless Keychain "+
			"item shadowed a populated file", got.Type)
	}
}

// The two descriptors are independent, so a source recording only one must not
// suppress the other source that holds the rest. Returning the first source
// that answered at all silently dropped the tier here.
func TestGlobalSubscriptionMergesAcrossSources(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Keychain is consulted only on darwin")
	}
	calls := stubKeychain(t, []byte(`{"claudeAiOauth":{"rateLimitTier":"from_keychain"}}`), nil)
	writeGlobalCreds(t, `{"claudeAiOauth":{"subscriptionType":"from_file"}}`)

	got := GlobalSubscription()
	if got.Type != "from_file" {
		t.Fatalf("Type = %q, want the file's value", got.Type)
	}
	if got.RateLimitTier != "from_keychain" {
		t.Fatalf("RateLimitTier = %q, want the Keychain's value; a partial file "+
			"suppressed the descriptor the Keychain held", got.RateLimitTier)
	}
	if *calls != 1 {
		t.Fatalf("Keychain consulted %d time(s), want exactly 1", *calls)
	}
}

// The earlier source wins any field both record -- merging must not let a later
// source overwrite what the file already answered.
func TestGlobalSubscriptionEarlierSourceWinsOnConflict(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Keychain is consulted only on darwin")
	}
	stubKeychain(t, []byte(`{"claudeAiOauth":{"subscriptionType":"kc","rateLimitTier":"kc_tier"}}`), nil)
	writeGlobalCreds(t, `{"claudeAiOauth":{"subscriptionType":"file"}}`)

	got := GlobalSubscription()
	if got.Type != "file" {
		t.Fatalf("Type = %q, want the file's value to win", got.Type)
	}
	if got.RateLimitTier != "kc_tier" {
		t.Fatalf("RateLimitTier = %q, want the Keychain to fill the gap", got.RateLimitTier)
	}
}

// A complete file must still short-circuit the Keychain: merging must not have
// reintroduced a `security` invocation on the common path.
func TestGlobalSubscriptionCompleteFileSkipsKeychain(t *testing.T) {
	calls := stubKeychain(t, []byte(`{"claudeAiOauth":{"subscriptionType":"kc"}}`), nil)
	writeGlobalCreds(t, `{"claudeAiOauth":{"subscriptionType":"pro","rateLimitTier":"tier_x"}}`)

	if got := GlobalSubscription(); got.Type != "pro" || got.RateLimitTier != "tier_x" {
		t.Fatalf("GlobalSubscription() = %+v, want {pro tier_x}", got)
	}
	if *calls != 0 {
		t.Fatalf("Keychain consulted %d time(s) despite the file being complete", *calls)
	}
}

// appendSubscriptionEnv must not append into a caller's spare capacity, or a
// caller that later appends to its own slice would overwrite what we added.
func TestAppendSubscriptionEnvDoesNotAliasCaller(t *testing.T) {
	stubKeychain(t, nil, errors.New("no such item"))
	writeGlobalCreds(t, `{"claudeAiOauth":{"subscriptionType":"pro","rateLimitTier":"tier_x"}}`)

	base := make([]string, 1, 8) // deliberate spare capacity
	base[0] = "KEEP=1"

	got := appendSubscriptionEnv(base)
	if len(got) != 3 {
		t.Fatalf("appendSubscriptionEnv() = %v, want 3 entries", got)
	}

	// The caller appends to its ORIGINAL slice; that must not disturb got.
	_ = append(base, "CALLER=2") //nolint:gocritic // the aliasing is the point
	for i, want := range []string{"KEEP=1", SubscriptionTypeEnv + "=pro", RateLimitTierEnv + "=tier_x"} {
		if got[i] != want {
			t.Fatalf("got[%d] = %q after the caller appended, want %q -- "+
				"the result aliased the caller's backing array", i, got[i], want)
		}
	}
}

// With no file at all the Keychain is the only source left, so it must be
// reached -- the cheap-first ordering must not become "file-only".
func TestGlobalSubscriptionFallsBackToKeychain(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Keychain is consulted only on darwin")
	}
	calls := stubKeychain(t, []byte(`{"claudeAiOauth":{"subscriptionType":"from_keychain"}}`), nil)
	homeWithoutCreds(t)

	if got := GlobalSubscription(); got.Type != "from_keychain" {
		t.Fatalf("Type = %q, want the Keychain's value", got.Type)
	}
	if *calls == 0 {
		t.Fatal("Keychain was never consulted despite being the only source")
	}
}

// An explicit export outranks the inferred value, and must survive in the
// returned slice -- asserting only that the inferred value is absent would pass
// against an implementation that dropped the operator's value entirely.
func TestAppendSubscriptionEnvRespectsExistingExport(t *testing.T) {
	stubKeychain(t, nil, errors.New("no such item"))
	writeGlobalCreds(t, `{"claudeAiOauth":{"subscriptionType":"pro","rateLimitTier":"tier_x"}}`)

	got := appendSubscriptionEnv([]string{SubscriptionTypeEnv + "=operator_choice"})

	var sawOperator, sawInferred, sawTier bool
	for _, kv := range got {
		switch kv {
		case SubscriptionTypeEnv + "=operator_choice":
			sawOperator = true
		case SubscriptionTypeEnv + "=pro":
			sawInferred = true
		case RateLimitTierEnv + "=tier_x":
			sawTier = true
		}
	}
	if !sawOperator {
		t.Fatalf("appendSubscriptionEnv() = %v, dropped the operator's export", got)
	}
	if sawInferred {
		t.Fatalf("appendSubscriptionEnv() = %v, overrode the operator's export", got)
	}
	// One explicit value must not suppress the other descriptor.
	if !sawTier {
		t.Fatalf("appendSubscriptionEnv() = %v, want it to still add %s", got, RateLimitTierEnv)
	}
}

// os.Getenv cannot distinguish unset from `export FOO=`, but the operator who
// typed the latter meant something by it. Scanning the slice can tell them
// apart, and must.
func TestAppendSubscriptionEnvRespectsExplicitEmptyExport(t *testing.T) {
	stubKeychain(t, nil, errors.New("no such item"))
	writeGlobalCreds(t, `{"claudeAiOauth":{"subscriptionType":"pro","rateLimitTier":"tier_x"}}`)

	got := appendSubscriptionEnv([]string{SubscriptionTypeEnv + "="})

	for _, kv := range got {
		if kv == SubscriptionTypeEnv+"=pro" {
			t.Fatalf("appendSubscriptionEnv() = %v, overrode an explicit empty export", got)
		}
	}
}

// When both descriptors are already present there is nothing to fill in, so the
// credential store must not be read at all -- on darwin that read can spawn a
// subprocess and raise a Keychain prompt.
func TestAppendSubscriptionEnvSkipsLookupWhenNothingMissing(t *testing.T) {
	calls := stubKeychain(t, []byte(`{"claudeAiOauth":{"subscriptionType":"from_keychain"}}`), nil)
	homeWithoutCreds(t) // force the file source to miss, so only the Keychain could answer

	in := []string{SubscriptionTypeEnv + "=a", RateLimitTierEnv + "=b"}
	got := appendSubscriptionEnv(in)

	if len(got) != len(in) {
		t.Fatalf("appendSubscriptionEnv() = %v, want it unchanged", got)
	}
	if *calls != 0 {
		t.Fatalf("credential store was consulted %d time(s) with nothing to fill in", *calls)
	}
}

// A store recording only one descriptor must yield only that one, never an
// empty companion: an empty CLAUDE_CODE_SUBSCRIPTION_TYPE reads to Claude Code
// as a definite answer the store never gave.
func TestAppendSubscriptionEnvOmitsEmptyValues(t *testing.T) {
	stubKeychain(t, nil, errors.New("no such item"))
	writeGlobalCreds(t, `{"claudeAiOauth":{"subscriptionType":"pro"}}`)

	got := appendSubscriptionEnv(nil)

	if len(got) != 1 || got[0] != SubscriptionTypeEnv+"=pro" {
		t.Fatalf("appendSubscriptionEnv() = %v, want exactly [%s=pro]", got, SubscriptionTypeEnv)
	}
}
