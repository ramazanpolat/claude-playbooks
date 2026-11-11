package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/account"
	"github.com/ramazanpolat/claude-playbooks/internal/auth"
	"github.com/ramazanpolat/claude-playbooks/internal/manifest"
	"github.com/ramazanpolat/claude-playbooks/internal/playbook"
)

// cpb ask --allow-other-provider: the one way a question leaves Anthropic.
// Without it, an account or a playbook whose requests go away from
// Anthropic is refused, and the refusal names the flag and what it would
// send. With it, the question on a gateway account goes to the account's
// own address, signed in with the account's own credential, as a launch on
// it would. A Claude login or token never goes with it: an account that
// would send one to a gateway (claude_login_trusted) is refused, flag or
// not, and the child gets a new, empty config directory, so no stored
// login can be reached either. Such an ask neither reads nor writes the
// ask store: no history and no .ask/CLAUDE.md go there, and nothing is
// recorded, so another provider's answer never reaches a later ask.

// askAllowOtherFlag is the flag's name.
const askAllowOtherFlag = "allow-other-provider"

// askLoginOther signs in on a gateway account, with --allow-other-provider.
const askLoginOther = "other"

// askOtherSends is what a question on another provider sends there:
// askContext, what the tool can return, and Claude Code's own.
const askOtherSends = "the request; the names of your playbooks, launchers, env sets, defaults and accounts, " +
	"each playbook's route, account and login class, and each account's provider and login class; " +
	"the cpb version and the playbooks root (with ~ for your home, in full when it is outside it); " +
	"what the model reads through cpb's tool (versions, plugin, MCP server and skill names, and session counts, kinds, statuses, times and models); " +
	"and what Claude Code itself sends, the platform and OS version among them"

// askOtherNoStore is what such an ask leaves out, and what that costs.
const askOtherNoStore = "It neither reads nor writes your ask history and does not send .ask/CLAUDE.md, " +
	"with or without --no-history, so a follow-up that leans on an earlier answer does not work there."

// askOtherRefusal refuses an account --allow-other-provider cannot ask on:
// nil for a gateway account that signs in with its own credential.
func askOtherRefusal(a *account.Account) error {
	if a.Provider != account.Gateway {
		return fmt.Errorf("account %s is a %s account, and --%s asks a gateway account only: nothing was sent", a.Name, a.Provider, askAllowOtherFlag)
	}
	if a.UsesClaudeCredential() {
		return fmt.Errorf("account %s would send your Claude credential to a gateway (claude_login_trusted), and cpb ask keeps a Claude login or token on Anthropic, --%s or not: nothing was sent", a.Name, askAllowOtherFlag)
	}
	return nil
}

// askOtherHint ends the refusal of an account away from Anthropic: how to
// ask there anyway and what that sends, or why the flag would not help.
func askOtherHint(a *account.Account) string {
	switch {
	case a.Provider != account.Gateway:
		return fmt.Sprintf(" --%s asks a gateway account only.", askAllowOtherFlag)
	case a.UsesClaudeCredential():
		return fmt.Sprintf(" It would send your Claude credential there, which stays on Anthropic, so --%s does not lift this.", askAllowOtherFlag)
	}
	return fmt.Sprintf(" To ask there anyway, add --%s. Then %s receives, signed in with the account's own credential: %s. %s", askAllowOtherFlag, askOtherWhere(a), askOtherSends, askOtherNoStore)
}

// askAwayRefusal refuses a question on a for going away from Anthropic, and
// says how to ask there anyway when the flag would.
func askAwayRefusal(a *account.Account) error {
	return fmt.Errorf("account %s sends its requests away from Anthropic (provider %s), and cpb ask asks Anthropic only: nothing was sent.%s", a.Name, a.Provider, askOtherHint(a))
}

// askOtherSignIn signs in on a, a gateway account, for a question the
// caller allowed to leave Anthropic.
func askOtherSignIn(a *account.Account) (askSignIn, error) {
	if err := askOtherRefusal(a); err != nil {
		return askSignIn{}, err
	}
	return askSignIn{mode: askLoginOther, account: a}, nil
}

// askOtherWhere names where a's requests go: its scheme and host (http
// says the credential goes unencrypted), never a path, a query or anything
// else of the address.
func askOtherWhere(a *account.Account) string {
	if u, err := url.Parse(a.BaseURL); err == nil && u.Host != "" && u.Scheme != "" {
		return fmt.Sprintf("account %s's gateway at %s://%s", a.Name, u.Scheme, u.Host)
	}
	return fmt.Sprintf("account %s's gateway", a.Name)
}

// askOtherEnv is the child's environment on a gateway account: env (the
// allowlist) without any Anthropic key or token, a new, empty Claude Code
// config directory, and what the account renders, as a launch on it sets
// it: its address, its models, and a credential stored AS PLAINTEXT. A
// credential or a header by reference is set by the secret helper
// (askOtherCommand). A keyless account (auth = 'none') gets cpb's fixed,
// non-secret placeholder, as a launch does: Claude Code does not start
// with no credential at all.
func askOtherEnv(a *account.Account, env []string, configDir string) []string {
	out := make([]string, 0, len(env)+8)
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if askEnvLogin[k] || account.IsReserved(k) || k == "CLAUDE_CONFIG_DIR" {
			continue
		}
		out = append(out, kv)
	}
	out = append(out, "CLAUDE_CONFIG_DIR="+configDir)
	r := account.Selection{Name: a.Name, Account: a}.Render(filepath.Join(configDir, "anthropic"))
	for _, k := range sortedKeys(r.Layer.Set) {
		out = append(out, k+"="+r.Layer.Set[k])
	}
	if a.Auth.Kind == account.None {
		out = auth.WithNoCredential(out)
	}
	return out
}

// askAccountAlone is the account a launch of pb on flag (its own choice
// when "") runs on, when that account alone decides where it goes: the
// launch plans cleanly (no layer, settings file or reserved variable routes
// or signs it in beside the account), and the account is a named one, not
// one a layer's route derives. A launch cpb would refuse returns the
// refusal; any other route returns nil. It touches no credential.
func askAccountAlone(pb *playbook.Playbook, flag string) (*account.Account, error) {
	if _, err := auth.EffectiveBlock(pb.Path, nil); err != nil {
		return nil, err
	}
	var layers []*manifest.Env
	if flag != "" {
		layers = []*manifest.Env{{Accounts: []string{flag}}}
	}
	saved := showAccount
	showAccount = false
	defer func() { showAccount = saved }()
	acct, err := planAccount(pb.Path, pb.Name, layers, "", nil)
	if err != nil {
		return nil, err
	}
	if acct.route != nil {
		return nil, nil
	}
	return acct.sel.Account, nil
}

// askOtherCommand builds the claude child on a gateway account, as a launch
// on it runs: through the secret helper for a credential or headers by
// reference, cpb's header step for the headers, and the account's
// apiKeyHelper as a call-only setting. No value is on an argument list.
func askOtherCommand(ctx context.Context, a *account.Account, claudePath string, args, env []string) (*exec.Cmd, error) {
	r := account.Selection{Name: a.Name, Account: a}.Render("")
	if r.KeyHelper != "" {
		s, _ := json.Marshal(map[string]string{"apiKeyHelper": r.KeyHelper})
		args = append(append([]string{}, args...), "--settings", string(s))
	}
	lp := &launchPlan{}
	if len(r.Layer.Refs) > 0 {
		x, err := resolveHelperFor(refValues(r.Layer.Refs))
		if err != nil {
			return nil, fmt.Errorf("account %s's credential or headers are secret references, and %w: nothing was sent", a.Name, err)
		}
		lp.helper, lp.helperPre, lp.def, lp.refs = x.path, x.pre, x.def, r.Layer.Refs
		if err := lp.withHeaders(r.Headers); err != nil {
			return nil, fmt.Errorf("account %s: %w: nothing was sent", a.Name, err)
		}
	}
	lc := lp.command(claudePath, args, env)
	c := exec.CommandContext(ctx, lc.Path, lc.Args[1:]...)
	c.Env = lc.Env
	return c, nil
}

// askOtherGate is askGate for a question on a gateway account: the one
// base URL is the account's own; every other route, proxy or trust-store
// variable still refuses the call.
func askOtherGate(env []string) error {
	rest := make([]string, 0, len(env))
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); k != "ANTHROPIC_BASE_URL" {
			rest = append(rest, kv)
		}
	}
	if err := askGate(rest); err != nil {
		return fmt.Errorf("%s", strings.Replace(err.Error(), "cpb ask asks Anthropic only, directly", "cpb ask asks a gateway directly", 1))
	}
	return nil
}
