package cmd

import (
	"fmt"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/account"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

// cpb ask --account <name>: the question signs in on an account, as a
// launch on it would, on Anthropic only. An account cpb ask can sign in on
// is an Anthropic one, at Anthropic's own address, with no request headers
// of its own, that signs in with a Claude token (claude_token) or a Claude
// login (claude_login). Its token goes into the child's environment only,
// through the secret helper when it is a reference, as at launch: cpb never
// reads the value of a reference.

// askLoginAccount signs in with an account's Claude token: a fresh config
// directory, and the token as CLAUDE_CODE_OAUTH_TOKEN, the one credential.
const askLoginAccount = "account"

// askAccountSignIn decides how a question signs in on the account name,
// addressed to pb (nil for none). With a playbook, the account must be one
// that playbook may run on, as for `cpb run --account`.
func askAccountSignIn(root string, pb *playbook.Playbook, name string) (askSignIn, error) {
	if err := manifest.ValidateAccountName(name); err != nil {
		return askSignIn{}, askUsage("--account: " + err.Error())
	}
	dir := account.Dir(root)
	if name == account.Machine {
		// The implicit account: the way a question signs in without one.
		if pb == nil {
			mode, err := askLogin()
			if err != nil {
				return askSignIn{}, askUsage(err.Error())
			}
			return askSignIn{mode: mode}, nil
		}
		if a := askPlaybookAccount(pb.Path, pb.Name); a != nil {
			return askSignIn{}, fmt.Errorf("%s runs on account %s, not on %s: nothing was sent. Ask without naming it: cpb ask --account %s \"<request>\"", pb.Name, a.Name, account.Machine, account.Machine)
		}
		return askTargetSignIn(root, pb)
	}
	var a *account.Account
	if pb != nil {
		var list []string
		if m, _ := manifest.Nearest(pb.Path); m != nil && m.Env != nil {
			list = m.Env.Accounts
		}
		sel, err := account.Select(dir, pb.Name, list, name)
		if err != nil {
			return askSignIn{}, fmt.Errorf("%w: nothing was sent", err)
		}
		a = sel.Account
	} else {
		var err error
		if a, err = account.Must(dir, name); err != nil {
			return askSignIn{}, fmt.Errorf("%w: nothing was sent", err)
		}
		if a.Listed() {
			return askSignIn{}, fmt.Errorf("account %s is access = 'listed': it runs only for a playbook that lists it, so a question on it names one: cpb ask <playbook> --account %s \"<request>\". Nothing was sent", name, name)
		}
	}
	if a.Auth.Kind == account.ClaudeLogin {
		// The playbook's own Claude login: the playbook's, or, for a
		// question addressed to none, Claude Code's own on this machine.
		if err := askAccountUsable(a); err != nil {
			return askSignIn{}, err
		}
		if pb == nil {
			return askSignIn{mode: askLoginClaude}, nil
		}
		return askLoginSignIn(pb)
	}
	return askTokenAccountSignIn(a)
}

// askTokenAccountSignIn signs in with a's Claude token, or refuses an
// account cpb ask cannot sign in on.
func askTokenAccountSignIn(a *account.Account) (askSignIn, error) {
	if err := askAccountUsable(a); err != nil {
		return askSignIn{}, err
	}
	if a.Auth.Kind != account.ClaudeToken {
		return askSignIn{}, fmt.Errorf("account %s signs in with its %s, and cpb ask signs in with a Claude login or a Claude token (claude_token) only: nothing was sent", a.Name, a.Auth.Kind)
	}
	return askSignIn{mode: askLoginAccount, account: a}, nil
}

// askAccountUsable refuses an account whose requests would not go to
// Anthropic as cpb ask sends them: another provider, or request headers of
// its own, which cpb ask does not send. An Anthropic account's base_url is
// on anthropic.com (account.Validate); cpb ask sets none, so its question
// goes to Anthropic's API as every question does.
func askAccountUsable(a *account.Account) error {
	if a.Provider != account.Anthropic {
		return fmt.Errorf("account %s sends its requests away from Anthropic (provider %s), and cpb ask asks Anthropic only: nothing was sent", a.Name, a.Provider)
	}
	if n := a.HeaderNames(); len(n) > 0 {
		return fmt.Errorf("account %s sends request headers of its own (%s), which cpb ask does not send: nothing was sent", a.Name, strings.Join(n, ", "))
	}
	return nil
}

// askOtherAccounts names the accounts a question could sign in on instead
// of the one that failed (except, "" for none), for the hint that ends a
// failure: Claude token accounts open to any playbook. Names only.
func askOtherAccounts(root, except string) []string {
	all, err := account.List(account.Dir(root))
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range all {
		if a.Name == except || a.Listed() || a.Auth.Kind != account.ClaudeToken || askAccountUsable(a) != nil {
			continue
		}
		out = append(out, a.Name)
	}
	return out
}

// askFailureHint ends every failure of the call: how the question signed
// in, and the accounts it could sign in on instead.
func askFailureHint(root string, signIn askSignIn) string {
	var hint string
	except := ""
	if signIn.mode == askLoginAccount && signIn.account != nil {
		except = signIn.account.Name
		hint = fmt.Sprintf("cpb ask signed in on account %s, with its Claude token", except)
	} else {
		hint = askLoginHint
	}
	if others := askOtherAccounts(root, except); len(others) > 0 {
		hint += fmt.Sprintf("; or ask on another account: cpb ask --account %s \"<request>\" (yours: %s)", others[0], strings.Join(others, ", "))
	}
	return hint
}
