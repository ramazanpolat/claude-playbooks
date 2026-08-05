//go:build !windows

// Plan-descriptor propagation under long-lived OAuth token auth.
//
// Claude Code drops subscriptionType from its account profile when
// CLAUDE_CODE_OAUTH_TOKEN is set, so its interactive entitlement check cannot
// confirm the plan and fails closed -- a Max seat is told the model requires
// usage credits (anthropics/claude-code#79597). claude-playbook restores the
// descriptors from the account's own credential store.
//
// These assert on what the child process actually received, for the same reason
// launch_env_test.go does: between PrepareLaunchEnv's return value and the
// child's environment sit exec.Command, c.Env assignment and exec.LookPath.
//
// The `security` stub from shimDir exits non-zero, so the darwin Keychain branch
// always misses and every case below resolves through the seeded HOME. That
// makes these tests deterministic and identical on every OS.
package e2e

import (
	"os"
	"path/filepath"
	"testing"
)

const (
	subTypeEnv  = "CLAUDE_CODE_SUBSCRIPTION_TYPE"
	rateTierEnv = "CLAUDE_CODE_RATE_LIMIT_TIER"
)

// seedGlobalCredentials writes a global credential store carrying the given plan
// descriptors. Values are arbitrary sentinels rather than realistic plan names:
// a test that passes only for the literal "max" would hide exactly the bug this
// design avoids -- hardcoding one account's plan for every user.
func seedGlobalCredentials(subType, tier string) func(*testing.T, string) {
	return func(t *testing.T, home string) {
		t.Helper()
		dir := filepath.Join(home, ".claude")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := `{"claudeAiOauth":{"accessToken":"GLOBAL",` +
			`"subscriptionType":"` + subType + `","rateLimitTier":"` + tier + `"}}`
		if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// The feature itself: under token auth the descriptors must reach the child, or
// the credits wall that made token auth unusable stays put.
func TestTokenAuthReceivesSubscriptionDescriptors(t *testing.T) {
	root := t.TempDir()
	playbook(t, root, "shared", false)

	env := childEnv(t, root, launch{
		env:      []string{tokenFile(t, "sk-ant-oat01-FROMFILE")},
		args:     []string{"run", "shared"},
		seedHome: seedGlobalCredentials("sentinel_plan", "sentinel_tier"),
	})

	if got := env[subTypeEnv]; got != "sentinel_plan" {
		t.Fatalf("%s = %q, want the credential store's value", subTypeEnv, got)
	}
	if got := env[rateTierEnv]; got != "sentinel_tier" {
		t.Fatalf("%s = %q, want the credential store's value", rateTierEnv, got)
	}
}

// The control, and the guard against a blanket injection: without a token Claude
// Code reads the descriptors from stored credentials natively. Injecting anyway
// would be redundant at best, and would override the credential store's own view
// with a copy that can drift from it.
func TestSubscriptionDescriptorsAbsentWithoutToken(t *testing.T) {
	root := t.TempDir()
	playbook(t, root, "shared", false)

	env := childEnv(t, root, launch{
		env:      []string{tokenFileEnv + "=" + filepath.Join(t.TempDir(), "absent")},
		args:     []string{"run", "shared"},
		seedHome: seedGlobalCredentials("sentinel_plan", "sentinel_tier"),
	})

	for _, key := range []string{subTypeEnv, rateTierEnv} {
		if v, present := env[key]; present {
			t.Fatalf("no token configured, yet child received %s=%s", key, v)
		}
	}
}

// An explicit export is a deliberate act by the user and outranks what this
// process infers from disk -- notably for a Team seat, whose real
// subscriptionType the picker does not accept, and which #79597 documents as
// needing a manual override.
func TestExportedSubscriptionDescriptorWins(t *testing.T) {
	root := t.TempDir()
	playbook(t, root, "shared", false)

	env := childEnv(t, root, launch{
		env: []string{
			tokenFile(t, "sk-ant-oat01-FROMFILE"),
			subTypeEnv + "=operator_override",
		},
		args:     []string{"run", "shared"},
		seedHome: seedGlobalCredentials("sentinel_plan", "sentinel_tier"),
	})

	if got := env[subTypeEnv]; got != "operator_override" {
		t.Fatalf("%s = %q, want the operator's exported value", subTypeEnv, got)
	}
	// The un-overridden descriptor is still filled in: one explicit value must
	// not suppress the other.
	if got := env[rateTierEnv]; got != "sentinel_tier" {
		t.Fatalf("%s = %q, want the credential store's value", rateTierEnv, got)
	}
}

// isolate_auth exists so two accounts can run side by side. The descriptors
// describe the GLOBAL account, so leaking them tells an isolated playbook's
// session the plan of an account it is deliberately not authenticating as.
// Inheritance is the leak here, so they must be actively removed, not merely
// left un-injected.
func TestIsolatedPlaybookNeverReceivesSubscriptionDescriptors(t *testing.T) {
	root := t.TempDir()
	playbook(t, root, "iso", true)

	env := childEnv(t, root, launch{
		env: []string{
			tokenEnv + "=sk-ant-oat01-INHERITED",
			subTypeEnv + "=leaked_plan",
			rateTierEnv + "=leaked_tier",
		},
		args:     []string{"run", "iso"},
		seedHome: seedGlobalCredentials("sentinel_plan", "sentinel_tier"),
	})

	for _, key := range []string{subTypeEnv, rateTierEnv} {
		if v, present := env[key]; present {
			t.Fatalf("isolated playbook leaked a global plan descriptor: %s=%s", key, v)
		}
	}
}

// A missing credential store must yield no variable at all. Exporting an empty
// CLAUDE_CODE_SUBSCRIPTION_TYPE would read to Claude Code as a definite answer
// the store never gave, which is worse than staying silent.
func TestUnreadableCredentialStoreSkipsInjection(t *testing.T) {
	root := t.TempDir()
	playbook(t, root, "shared", false)

	env := childEnv(t, root, launch{
		env:  []string{tokenFile(t, "sk-ant-oat01-FROMFILE")},
		args: []string{"run", "shared"},
		// No seedHome: HOME is empty and the security stub exits non-zero.
	})

	for _, key := range []string{subTypeEnv, rateTierEnv} {
		if v, present := env[key]; present {
			t.Fatalf("no credential store, yet child received %s=%q", key, v)
		}
	}
}

// A store that exists but records only one descriptor must yield only that one,
// rather than an empty companion.
func TestPartialCredentialStoreInjectsOnlyWhatItHas(t *testing.T) {
	root := t.TempDir()
	playbook(t, root, "shared", false)

	env := childEnv(t, root, launch{
		env:      []string{tokenFile(t, "sk-ant-oat01-FROMFILE")},
		args:     []string{"run", "shared"},
		seedHome: seedGlobalCredentials("sentinel_plan", ""),
	})

	if got := env[subTypeEnv]; got != "sentinel_plan" {
		t.Fatalf("%s = %q, want the credential store's value", subTypeEnv, got)
	}
	if v, present := env[rateTierEnv]; present {
		t.Fatalf("credential store had no rate limit tier, yet child received %s=%q", rateTierEnv, v)
	}
}
