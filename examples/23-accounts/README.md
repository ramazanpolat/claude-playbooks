# 23 — accounts

```
cpb APPLY playbook.cpb
cpb SELECT name, token_account FROM PLAYBOOKS           # which account each playbook runs on
cpb run sre                                             # on acct-work
cpb run --env-set acct-personal sre                     # one launch on acct-personal
cpb ALTER PLAYBOOK sre REPLACE ENV acct-work WITH acct-personal   # switch for good
```

An account is an env set that holds `CLAUDE_CODE_OAUTH_TOKEN` by reference:
the long-lived token `claude setup-token` prints for that account, stored in
your secret store, never in a file cpb writes. Attaching the set switches the
playbook's account; `--env-set` picks one for a single launch. The launch
execs through the secret helper, which sets the token for claude only, so cpb
never holds it. This example uses the sample
[`cpb-secret-file`](../secret-helper/) helper, and `.setup` stores two
placeholders.

- **Names, never tokens.** `SELECT`, `SHOW PLAYBOOK --json` and `EXPLAIN`
  report `token_source` (`env-set:acct-work`), `token_kind` (`reference`)
  and `token_account` (`acct-work`).
- **The account wins over the shell.** A token exported in your shell is
  removed from the launch: if the helper fails, nothing stands in for the
  account's token.
- **Login stays a separate switch.** `SET login = 'isolated'` decides what
  the playbook shares with your machine's login, not which account it uses.
  A playbook that also holds a `/login` of its own keeps it: on Linux it is
  set aside for the token run and put back after; on macOS such a launch is
  refused, naming the fixes (see the
  [authentication guide](../../docs/guides/authentication.md)).
- **Another provider never gets your token.** Pointing a playbook with an
  account at a router without the router's own key is refused at launch
  (the login guard; see [02](../02-env-sets-and-order/)).
