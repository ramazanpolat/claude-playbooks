# 23 — accounts

```
cpb APPLY playbook.cpb
cpb SHOW ACCOUNTS                                        # the accounts, never a credential
cpb "SELECT name, account FROM PLAYBOOKS"                # which account each playbook runs on
cpb run sre                                              # on acct-work, its first
cpb run --account acct-personal sre                      # one launch on another it lists
cpb ALTER PLAYBOOK sre USE ACCOUNT acct-personal, acct-work   # switch for good
```

An **account** is one record that says where a playbook's requests go and
how they authenticate: the provider, its address, who you are there, one
credential, and that provider's model names. Here two Claude accounts each
hold the long-lived token `claude setup-token` prints, by reference, in your
secret store, never in a file cpb writes. The launch execs through the
secret helper, which sets the token for claude only. This example uses the
sample [`cpb-secret-file`](../secret-helper/) helper, and `.setup` stores two
placeholders.

- **Derived, never set one by one.** At launch cpb sets the routing,
  credential and model variables from the account, and removes every other
  one the shell exports. An env set, a playbook variable, a launch flag or a
  settings file that sets one of them refuses the launch, so an address can
  never change while its key stays behind.
- **Which account.** `--account`, else the playbook's first, else the
  `DEFAULTS` account (`ALTER DEFAULTS USE ACCOUNT …`), else `machine`: the
  machine's own login, as before accounts.
- **Who may use which.** A playbook's `USE ACCOUNT` list is an allow-list. An
  account with `access = 'listed'` runs only for a playbook that lists it.
- **Moving an account moves its credential.** `ALTER ACCOUNT … SET
  base_url = …` is refused unless the same statement states the credential.
- **The login follows the account.** An account that uses no Claude login (a
  router's key, an API key) never reaches one: an isolated playbook's own
  login is kept out of the run, and a shared playbook is refused.
- **A gateway you trust** with your Claude credential is an account with
  `claude_login_trusted = true`, over https only.
- **Headers a gateway asks for** go on the account by reference too
  (`SET header.<Name> FROM '<ref>'`), joined into `ANTHROPIC_CUSTOM_HEADERS`
  for that one launch; the check gives the trusted gateway two.
- **Names, never tokens.** `SHOW ACCOUNTS`, `SELECT`, `SHOW PLAYBOOK --json`
  and `EXPLAIN` show the account and the reference, never a value.

Reference: [CREATE / ALTER / DROP ACCOUNT](../../SPEC.md#create--alter--drop-account);
guide: [Accounts](../../docs/guides/accounts.md).
