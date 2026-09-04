package auth

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envprofile"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
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

// resolveToken is TokenActive with the install's manifest [env] block applied
// on top. The manifest is the per-install authority over the machine-global
// token file:
//
//   - env.unset lists CLAUDE_CODE_OAUTH_TOKEN: the token is inactive here, no
//     matter what the file or the shell says. The launch then takes the
//     stored-credentials path. Stripping the variable alone would be the
//     worst of both worlds -- the quarantine would still wipe the stored
//     grant and nothing would authenticate the child.
//   - env.set supplies CLAUDE_CODE_OAUTH_TOKEN: that value is the token for
//     this install, injected in place of the file's. An empty value counts as
//     inactive, matching the file rule.
//   - otherwise TokenActive decides.
func resolveToken(menv *manifest.Env) (inject string, active bool) {
	if menv.Unsets(OAuthTokenEnv) {
		return "", false
	}
	if menv != nil {
		if _, ok := menv.Set[OAuthTokenEnv]; ok {
			tok := manifestToken(menv)
			return tok, tok != ""
		}
	}
	return TokenActive()
}

// manifestToken returns the token the flattened block sets, "" when it sets
// none or an empty value.
func manifestToken(menv *manifest.Env) string {
	if menv == nil {
		return ""
	}
	return strings.TrimSpace(menv.Set[OAuthTokenEnv])
}

// applyManifestEnv overlays the manifest [env] block on environ: set entries
// replace any inherited value, unset entries are dropped. The token variable
// is excluded from BOTH directions here because resolveToken has already
// folded it into the token decision and PrepareLaunchEnv places it itself.
func applyManifestEnv(environ []string, menv *manifest.Env) []string {
	if menv.Empty() {
		return environ
	}
	keys := make([]string, 0, len(menv.Set)+len(menv.Unset))
	for key := range menv.Set {
		if key != OAuthTokenEnv {
			keys = append(keys, key)
		}
	}
	for _, key := range menv.Unset {
		if key != OAuthTokenEnv {
			keys = append(keys, key)
		}
	}
	out := removeEnv(environ, keys...)
	for _, key := range keys {
		if value, ok := menv.Set[key]; ok {
			out = append(out, key+"="+value)
		}
	}
	return out
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
// Authentication isolation is checked FIRST and wins over everything below,
// with one install-local exception: a CLAUDE_CODE_OAUTH_TOKEN the manifest
// itself sets is that playbook's own token and is honoured (see the branch).
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
// The manifest's [env] block is the per-install override layer. Whether the
// token is active is decided with it applied (see resolveToken), and its
// set/unset entries are overlaid last, after the auth branches, so a
// declared value is what the child sees regardless of what the shell
// exported. CLAUDE_CONFIG_DIR is bound after that: manifest validation
// refuses it, and the binding here is what makes the refusal unnecessary
// to trust. A manifest that cannot be read is reported through the advisory
// error and treated as having no [env] block; the launch still proceeds --
// except for a profile that cannot be resolved, which callers refuse to
// launch over.
//
// The returned error is advisory: env is always usable, and callers should warn
// (not abort) on a non-nil error, matching the previous SyncCredentials call
// sites.
func PrepareLaunchEnv(configDir string) ([]string, error) {
	env := os.Environ()

	// The block is resolved with its profiles flattened in. Any profile
	// resolution failure satisfies errors.Is(err, envprofile.ErrProfile);
	// callers that launch treat it as fatal (see cmd/run.go), everything
	// else stays advisory.
	var menv *manifest.Env
	m, merr := manifest.Nearest(configDir)
	if m != nil {
		var perr error
		menv, perr = envprofile.Expand(envprofile.Dir(config.ResolvePlaybooksDir()), m.Env)
		if perr != nil {
			// Reported ahead of a manifest read error: it is the one the
			// launch refuses on.
			menv, merr = nil, perr
		}
	}

	if isAuthIsolated(configDir) {
		// The machine-global token and the plan descriptors describe the
		// global account and never apply here. A token the MANIFEST sets is
		// this install's own choice, not the global one, and is honoured:
		// injected, with the stored grant quarantined exactly as on the
		// non-isolated token path (the adoption hazard is the same).
		env = removeEnv(env, OAuthTokenEnv, SubscriptionTypeEnv, RateLimitTierEnv)
		syncErr := SyncCredentials(configDir)
		if own := manifestToken(menv); own != "" {
			if qErr := QuarantineStoredOAuth(configDir); syncErr == nil {
				syncErr = qErr
			}
			env = append(env, OAuthTokenEnv+"="+own)
		}
		env = applyManifestEnv(env, menv)
		env = removeEnv(env, "CLAUDE_CONFIG_DIR")
		env = append(env, "CLAUDE_CONFIG_DIR="+configDir)
		return env, firstErr(merr, syncErr)
	}

	inject, active := resolveToken(menv)

	var syncErr error
	if active {
		// Order matters: quarantine first, so a launch that fails afterwards
		// still leaves the config dir without the stale grant that would
		// hijack the token. Its error is reported only if metadata sync had
		// none of its own -- both are advisory, and the sync warning is the
		// more actionable of the two.
		qErr := QuarantineStoredOAuth(configDir)
		syncErr = SyncAccountMetadata(configDir)
		if syncErr == nil {
			syncErr = qErr
		}
	} else {
		syncErr = SyncCredentials(configDir)
	}

	if inject != "" {
		// Strip first: an inherited entry can exist even though TokenActive
		// chose the file, because `export CLAUDE_CODE_OAUTH_TOKEN=` is present
		// in the environment yet indistinguishable from unset to os.Getenv.
		// Appending alone would leave two entries for the key, and removeEnv's
		// own comment rejects relying on exec preferring the last duplicate.
		env = removeEnv(env, OAuthTokenEnv)
		env = append(env, OAuthTokenEnv+"="+inject)
	} else if !active {
		// Inactive by manifest decision (unset, or set to empty): an inherited
		// token would otherwise still reach the child and re-arm the very
		// adoption hazard the stored-credentials path exists to avoid.
		env = removeEnv(env, OAuthTokenEnv)
	}
	if active {
		env = appendSubscriptionEnv(env)
	}
	env = applyManifestEnv(env, menv)
	env = removeEnv(env, "CLAUDE_CONFIG_DIR")
	env = append(env, "CLAUDE_CONFIG_DIR="+configDir)
	return env, firstErr(merr, syncErr)
}

// firstErr returns the first non-nil error.
func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
