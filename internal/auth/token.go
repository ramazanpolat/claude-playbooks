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

// PrepareLaunchEnv builds the environment for a child claude process bound to
// configDir and returns it along with a non-fatal sync warning (nil on success).
//
// When a long-lived OAuth token is active, the token supersedes stored
// credentials, so credential-file syncing is SKIPPED — this avoids recreating
// the shared-symlink / refresh-rotation hazards that cause repeated /login
// prompts across isolated config dirs. Non-secret account metadata is still
// synced so a fresh config dir presents as logged in. The token is injected
// into the returned environment when it is not already present.
//
// Without a token, the full credential sync runs, preserving prior behavior.
//
// The returned error is advisory: env is always usable, and callers should warn
// (not abort) on a non-nil error, matching the previous SyncCredentials call
// sites.
func PrepareLaunchEnv(configDir string) ([]string, error) {
	inject, active := TokenActive()

	var syncErr error
	if active {
		syncErr = SyncAccountMetadata(configDir)
	} else {
		syncErr = SyncCredentials(configDir)
	}

	env := append(os.Environ(), "CLAUDE_CONFIG_DIR="+configDir)
	if inject != "" {
		env = append(env, OAuthTokenEnv+"="+inject)
	}
	return env, syncErr
}
