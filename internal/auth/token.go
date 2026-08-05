package auth

import (
	"os"
	"path/filepath"
	"strings"
)

// OAuthTokenEnv is the environment variable Claude Code reads for a long-lived
// OAuth token (produced by `claude setup-token`). When set, it takes precedence
// over stored subscription credentials, so no per-config-dir refresh/rotation
// happens and no interactive /login is triggered.
const OAuthTokenEnv = "CLAUDE_CODE_OAUTH_TOKEN"

// oauthTokenFileEnv lets callers/tests override the token file location.
const oauthTokenFileEnv = "CLAUDE_PLAYBOOKS_OAUTH_TOKEN_FILE"

// OAuthTokenFile returns the path of the long-lived OAuth token file.
// Default: $HOME/.config/claude-code/oauth-token. Overridable via
// CLAUDE_PLAYBOOKS_OAUTH_TOKEN_FILE (primarily for tests).
func OAuthTokenFile() string {
	if p := os.Getenv(oauthTokenFileEnv); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "claude-code", "oauth-token")
}

// TokenActive reports whether a long-lived OAuth token governs authentication
// for child claude processes. Two cases count as active:
//
//   - The token is already exported in this process's environment. Then inject
//     is "" (children inherit it via os.Environ()) and active is true.
//   - A non-empty token file exists. Then inject is the token to add to the
//     child environment and active is true.
//
// When no token is available, inject is "" and active is false.
func TokenActive() (inject string, active bool) {
	if os.Getenv(OAuthTokenEnv) != "" {
		return "", true
	}
	f := OAuthTokenFile()
	if f == "" {
		return "", false
	}
	data, err := os.ReadFile(f)
	if err != nil {
		return "", false
	}
	tok := strings.TrimSpace(string(data))
	if tok == "" {
		return "", false
	}
	return tok, true
}

// removeEnv returns a copy of environ with every entry for key dropped.
//
// Dropping is deliberate rather than overriding with an empty value: an empty
// CLAUDE_CODE_OAUTH_TOKEN is not documented to mean "no token", and appending an
// override would rely on exec taking the last duplicate — true on Unix, not a
// guarantee worth depending on. The result never aliases environ's backing
// array, so callers may keep using the original slice.
func removeEnv(environ []string, keys ...string) []string {
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		drop := false
		for _, key := range keys {
			if strings.HasPrefix(kv, key+"=") {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return out
}

// PrepareLaunchEnv builds the environment for a child claude process bound to
// configDir and returns it along with a non-fatal sync warning (nil on success).
//
// Authentication isolation is checked FIRST and wins over everything below.
// isAuthIsolated is consulted only inside SyncCredentials, so any branch that
// skips that call also silently skips the isolate_auth contract — which
// SPEC-v4.md defines as "detach shared credentials and do not copy global
// credentials or account metadata into this playbook". An isolated playbook
// therefore takes the full SyncCredentials path (which detaches), syncs no
// account metadata, and has any inherited token stripped from its environment:
// a token left in place would authenticate the child as the global account,
// defeating the documented different-accounts-concurrently workflow. The plan
// descriptors are stripped for the same reason — they describe the global
// account, and leaking them would tell an isolated playbook's session the plan
// of an account it is deliberately not using.
//
// Otherwise, when a long-lived OAuth token is active, the token supersedes
// stored credentials, so credential-file syncing is SKIPPED — this avoids
// recreating the shared-symlink / refresh-rotation hazards that cause repeated
// /login prompts across config dirs. Non-secret account metadata is still
// synced so a fresh config dir presents as logged in. The token is injected into
// the returned environment when it is not already present, alongside the plan
// descriptors the token path drops (see subscription.go).
//
// Without a token, the full credential sync runs, preserving prior behavior —
// including leaving the plan descriptors alone, since stored credentials carry
// them natively and Claude Code reads them from there.
//
// The returned error is advisory: env is always usable, and callers should warn
// (not abort) on a non-nil error, matching the previous SyncCredentials call
// sites.
func PrepareLaunchEnv(configDir string) ([]string, error) {
	env := append(os.Environ(), "CLAUDE_CONFIG_DIR="+configDir)

	if isAuthIsolated(configDir) {
		return removeEnv(env, OAuthTokenEnv, SubscriptionTypeEnv, RateLimitTierEnv),
			SyncCredentials(configDir)
	}

	inject, active := TokenActive()

	var syncErr error
	if active {
		syncErr = SyncAccountMetadata(configDir)
	} else {
		syncErr = SyncCredentials(configDir)
	}

	if inject != "" {
		env = append(env, OAuthTokenEnv+"="+inject)
	}
	if active {
		env = appendSubscriptionEnv(env)
	}
	return env, syncErr
}
