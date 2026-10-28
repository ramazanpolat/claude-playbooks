package auth

import (
	"fmt"
	"sort"

	"github.com/ramazanpolat/claude-playbooks/internal/account"
	"github.com/ramazanpolat/claude-playbooks/internal/config"
	"github.com/ramazanpolat/claude-playbooks/internal/envset"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
)

// launchMode is how a launch of configDir with no flags authenticates: the
// same decision PrepareLaunchEnv makes, on the account the launch runs on
// (internal/account), minus its side effects. Isolation is a manifest
// property and is reported even when env set resolution fails. blocked is
// set in shared-login when the token is blocked: by the playbook (BLOCK VAR,
// or set empty) or by an account that uses a Claude login. block is the
// governing manifest's own [env] block (nil without one); acct is the
// launch's account, nil for the machine account or an error.
func launchMode(configDir string) (mode Mode, modeErr string, blocked bool, block *manifest.Env, acct *account.Account) {
	isolated := isAuthIsolated(configDir)
	m, _ := manifest.Nearest(configDir)
	if m != nil {
		block = m.Env
	}
	root := config.ResolvePlaybooksDir()
	// No manifest: the registry default still applies.
	menv, err := envset.ExpandWithDefault(envset.Dir(root), block)
	if err != nil {
		return ModeError, sanitizeProfileError(err), false, block, nil
	}
	// Since v4.2.0 an account owns these: a layer that sets one refuses the
	// launch (cmd/account_launch.go), so the mode is that refusal. Judged on
	// the launch's effective block, as the launch judges it (an isolated
	// playbook never takes DEFAULTS' token).
	eff, err := EffectiveBlock(configDir, nil)
	if err != nil {
		return ModeError, sanitizeProfileError(err), false, block, nil
	}
	if k := reservedSetBy(eff); k != "" {
		return ModeError, k + " is set by a layer, and since v4.2.0 it is the account's: a launch is refused (cpb EXPLAIN PLAYBOOK says where)", false, block, nil
	}
	var list []string
	if block != nil {
		list = block.Accounts
	}
	sel, err := account.Select(account.Dir(root), "", list, "")
	if err != nil {
		return ModeError, "its account: " + err.Error(), false, block, nil
	}
	if a := sel.Account; a != nil {
		switch {
		case a.Auth.Kind == account.ClaudeToken:
			return ModePlaybookToken, "", false, block, a
		case a.Auth.Kind == account.ClaudeLogin && isolated:
			return ModeIsolatedLogin, "", false, block, a
		case a.Auth.Kind == account.ClaudeLogin:
			// The account blocks the machine's token: the stored login.
			return ModeSharedLogin, "", true, block, a
		}
		if !isolated {
			// AccountLoginRefusal's first half, which reads no store.
			return ModeError, fmt.Sprintf("account %s uses no Claude login, and the playbook shares the machine's: a launch is refused (SET login = 'isolated')", a.Name), false, block, a
		}
		return ModeAccount, "", false, block, a
	}
	if isolated {
		return ModeIsolatedLogin, "", false, block, nil
	}
	if menv.Blocks(OAuthTokenEnv) {
		return ModeSharedLogin, "", true, block, nil
	}
	if _, active := TokenActive(); active {
		return ModeToken, "", false, block, nil
	}
	return ModeSharedLogin, "", false, block, nil
}

// reservedSetBy is the first variable an account owns that the flattened
// layers set (a value or a reference), "" for none. Blocking one is fine.
func reservedSetBy(menv *manifest.Env) string {
	if menv == nil {
		return ""
	}
	var keys []string
	for k := range menv.Set {
		if account.IsReserved(k) {
			keys = append(keys, k)
		}
	}
	for k := range menv.Refs {
		if account.IsReserved(k) {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)
	return keys[0]
}

// Token sources (Account.TokenSource).
const (
	TokenSourceNone    = "none"
	TokenSourceFile    = "file"    // the machine's token file
	TokenSourceUnknown = "unknown" // the launch would be refused
	TokenSourceAccount = "account" // the launch's account (claude_token)
)

// Account is which Claude credential a launch of a playbook with no flags
// uses, and where it comes from (v4.2.0): what EXPLAIN, SHOW --json and
// SELECT report. States and names only, never a credential character; no
// Keychain probe and no process is spawned, so it is cheap for every
// playbook.
type Account struct {
	// AuthMode is the auth status mode: isolated-login, token,
	// playbook-token, shared-login, account, or error.
	AuthMode Mode `json:"auth_mode"`
	// TokenSource is none, account (a claude_token account), file (the
	// machine's token file), or unknown when the launch would be refused.
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
	mode, _, blocked, block, acct := launchMode(configDir)
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
		a.TokenSource = TokenSourceAccount
		if acct != nil && acct.Auth.Ref != "" {
			a.TokenKind = kind("reference")
		} else {
			a.TokenKind = kind("value")
		}
	case ModeToken:
		a.TokenKind = kind("value")
		a.TokenSource = TokenSourceFile
	}
	return a
}
