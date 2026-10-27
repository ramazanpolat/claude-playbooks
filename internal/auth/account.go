package auth

import (
	"os"

	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envset"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// launchMode is how a launch of configDir with no flags authenticates: the
// same decision PrepareLaunchEnv makes, minus its side effects. Isolation is
// a manifest property and is reported even when env set resolution fails.
// blocked is set in shared-login when the block blocks the token or sets it
// empty. block is the governing manifest's own [env] block (nil without
// one), for callers that attribute the token to a layer.
func launchMode(configDir string) (mode Mode, modeErr string, blocked bool, block *manifest.Env) {
	isolated := isAuthIsolated(configDir)
	if m, _ := manifest.Nearest(configDir); m != nil {
		block = m.Env
	}
	// No manifest: the registry default still applies.
	menv, err := envset.ExpandWithDefault(envset.Dir(config.ResolvePlaybooksDir()), block)
	if err != nil {
		return ModeError, sanitizeProfileError(err), false, block
	}
	if isolated {
		// DEFAULTS never supplies an isolated playbook's token. One it
		// supplies itself is honoured: PrepareLaunchEnv injects it and keeps
		// the playbook's own login out of the run.
		own, ownRef, err := ownToken(configDir, nil)
		switch {
		case err != nil:
			return ModeError, sanitizeProfileError(err), false, block
		case own != "" || ownRef:
			return ModePlaybookToken, "", false, block
		}
		return ModeIsolatedLogin, "", false, block
	}
	setsToken := false
	if menv != nil {
		_, setsToken = menv.Set[OAuthTokenEnv]
	}
	switch {
	case setsToken && manifestToken(menv) != "", tokenRef(menv):
		return ModePlaybookToken, "", false, block
	case menv.Blocks(OAuthTokenEnv):
		return ModeSharedLogin, "", true, block
	case setsToken:
		// Set to an empty value: resolveToken treats that as inactive, so
		// the launch takes the stored-login path.
		return ModeSharedLogin, "", true, block
	}
	if _, active := TokenActive(); active {
		return ModeToken, "", false, block
	}
	return ModeSharedLogin, "", false, block
}

// Token sources (Account.TokenSource).
const (
	TokenSourceNone     = "none"
	TokenSourcePlaybook = "playbook" // the playbook's own block
	TokenSourceEnvSet   = "env-set:" // + the env set's name: an account
	TokenSourceFile     = "file"     // the machine's token file
	TokenSourceShell    = "shell"    // exported in the environment cpb runs in
	TokenSourceUnknown  = "unknown"  // an env set could not be resolved
	TokenSourceAccount  = "account"  // the launch's account (claude_token)
)

// Account is which Claude credential a launch of a playbook with no flags
// uses, and where it comes from (v4.2.0): what EXPLAIN, SHOW --json and
// SELECT report. States and names only, never a credential character; no
// Keychain probe and no process is spawned, so it is cheap for every
// playbook.
type Account struct {
	// AuthMode is the auth status mode: isolated-login, token,
	// playbook-token, shared-login, or error.
	AuthMode Mode `json:"auth_mode"`
	// TokenSource is none, playbook, env-set:<name>, file, shell, or
	// unknown when an env set cannot be resolved.
	TokenSource string `json:"token_source"`
	// TokenKind is value or reference; null when no token is used.
	TokenKind *string `json:"token_kind"`
	// TokenBlocked is true when the playbook blocks the token (BLOCK VAR
	// CLAUDE_CODE_OAUTH_TOKEN, or sets it empty), so a launch takes a
	// stored login wherever a token is active.
	TokenBlocked bool `json:"token_blocked"`
}

// AccountOf reports the Account of a launch of configDir.
func AccountOf(configDir string) Account {
	mode, _, blocked, block := launchMode(configDir)
	a := Account{AuthMode: mode, TokenSource: TokenSourceNone, TokenBlocked: blocked}
	if mode == ModeError {
		a.TokenSource = TokenSourceUnknown
		return a
	}
	if !blocked {
		// An isolated playbook may block the token too; say so in every
		// mode, as the playbook's own choice.
		if menv, err := envset.ExpandWithDefault(envset.Dir(config.ResolvePlaybooksDir()), block); err == nil {
			a.TokenBlocked = menv.Blocks(OAuthTokenEnv)
		}
	}
	kind := func(k string) *string { return &k }
	switch mode {
	case ModePlaybookToken:
		origins, err := envset.Explain(envset.Dir(config.ResolvePlaybooksDir()), block)
		if err != nil {
			a.TokenSource = TokenSourceUnknown
			return a
		}
		for _, o := range origins {
			if o.Key != OAuthTokenEnv || o.Blocked {
				continue
			}
			if o.Ref != "" {
				a.TokenKind = kind("reference")
			} else {
				a.TokenKind = kind("value")
			}
			if o.Kind == envset.LayerPlaybook {
				a.TokenSource = TokenSourcePlaybook
			} else {
				a.TokenSource = TokenSourceEnvSet + o.Set
			}
		}
	case ModeToken:
		a.TokenKind = kind("value")
		if os.Getenv(OAuthTokenEnv) != "" {
			a.TokenSource = TokenSourceShell
		} else {
			a.TokenSource = TokenSourceFile
		}
	}
	return a
}
