package cmd

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/ramazanpolat/claude-playbooks/internal/account"
)

// unresolvedAccountLine is cpb's line after a launch through the secret
// helper that ended with an error, when a reference of the account's own
// (its credential, a header) cannot be resolved now: it names the account
// and the reference by kind, never a value. For an address on this machine
// it adds that the server behind it may not be running, since a local
// server may make the very secret the account reads. It is printed after
// the helper's own message, so it is the line read last. A launch that
// succeeded is never checked; "" when there is nothing to say. env is the
// environment the launch passed to launchPlan.command: the check sees what
// the launch's helper saw (a store location a layer sets, say).
func unresolvedAccountLine(acct *accountLaunch, plan *launchPlan, env []string) string {
	if acct == nil || plan == nil || plan.helper == "" || acct.sel.Account == nil || acct.rendered.Layer == nil {
		return ""
	}
	x := helperExe{path: plan.helper, pre: plan.helperPre, def: plan.def}
	env = withoutKeys(withoutPrefix(env, account.HeaderVarPrefix), sortedKeys(plan.refs))
	for _, key := range sortedKeys(acct.rendered.Layer.Refs) {
		ref := acct.rendered.Layer.Refs[key]
		if runHelper(x, nil, nil, nil, env, "--check", key+"="+ref) == nil {
			continue
		}
		a := acct.sel.Account
		line := fmt.Sprintf("cpb: the launch ended with an error, and the secret helper cannot resolve account %s's %s (a %s reference)", acct.sel.Name, accountRefLabel(acct, key), ref[:strings.IndexByte(ref, ':')+1])
		if u, err := url.Parse(a.BaseURL); err == nil && a.BaseURL != "" && account.IsLoopback(u.Hostname()) {
			line += fmt.Sprintf(". Its address, %s, is on this machine: the local server behind it may not be running (cpb SHOW ACCOUNT %s shows what the account points at)", a.BaseURL, acct.sel.Name)
		}
		return line + "\n"
	}
	return ""
}

// accountRefLabel is the account key a launch variable holds: its
// credential's (auth_token, api_key, claude_token) or a header's name.
func accountRefLabel(acct *accountLaunch, key string) string {
	if n, ok := strings.CutPrefix(key, account.HeaderVarPrefix); ok {
		if i, err := strconv.Atoi(n); err == nil && i < len(acct.rendered.Headers) {
			return "header " + acct.rendered.Headers[i]
		}
	}
	return acct.sel.Account.Auth.Kind
}
