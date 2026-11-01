# Authentication

Which Anthropic account, token, or login a playbook's session uses — the
identity boundary.

Every launch runs on one **account** (since v4.2.0, see
[Accounts](accounts.md)): where its requests go and how they authenticate.
A playbook that names none runs on `machine`, the machine's own login, and
this page is mostly about that account: how a launch decides whether a
**long-lived token** is active, then prepares the config directory
accordingly. Nothing else in the tool touches credentials.

```text
token active for this playbook?
  = its account is a claude_token account
    or, on machine: ~/.config/claude-code/oauth-token is non-empty
       and the playbook does not block it (BLOCK VAR CLAUDE_CODE_OAUTH_TOKEN)
  (a CLAUDE_CODE_OAUTH_TOKEN exported in your shell never counts: every
   launch removes it, since v4.2.0)

yes  ->  inject the token; remove the playbook's own stored login (claudeAiOauth only,
         MCP logins survive); sync non-secret account metadata so the dir presents as logged in
no   ->  link the playbook's .credentials.json to ~/.claude/.credentials.json; remove nothing;
         Claude Code refreshes the shared login itself
isolated_login = true  ->  neither: detach from the shared store, strip the global token,
         keep only what this playbook logs in itself; while it has no login of its own,
         drop the account record and cached feature flags left from a non-isolated past
```

The removal on the token path exists for one reason: under token auth Claude Code
never refreshes a stored login, and its 401-recovery path adopts a stored login
over the token. A stale stored login would therefore replace a working year-long
token with a dead one on the first transient 401. Removing it leaves nothing to
adopt. It is never done on the no-token path, where that stored login *is* the
session.

## The modes, per playbook

| You want | Do | At launch |
|---|---|---|
| Everything shares one login, no token | nothing (no `oauth-token` file) | credentials symlinked to `~/.claude`; `/login` anywhere logs in everywhere |
| Everything shares one long-lived token | `claude setup-token` once | token injected everywhere; a playbook's stored login is stripped when it is the machine's account (a newer copy goes over the machine's first), and any other login is first copied aside to `.credentials.json.cpb-own-<stamp>` |
| One playbook keeps its own `/login` while the others use the token | `cpb ALTER PLAYBOOK <name> BLOCK VAR CLAUDE_CODE_OAUTH_TOKEN` | that playbook takes the no-token path; the rest unchanged |
| One playbook uses its own token | an account, `cpb CREATE ACCOUNT work SET provider = 'anthropic', claude_token FROM 'keychain:…'` (a reference, resolved by your secret helper at launch; `claude_token = '…' AS PLAINTEXT` also works); then `cpb ALTER PLAYBOOK <name> USE ACCOUNT work` | that token wins over the file; its stored login is handled as on the second row |
| Switch a playbook between accounts | one account per Claude account; the playbook lists them, `cpb ALTER PLAYBOOK <name> USE ACCOUNT work, personal`, the first by default; `cpb run --account personal <name>` for one launch | the account's token wins; its stored login is handled as on the second row |
| One playbook is a different account, sharing nothing | `cpb ALTER PLAYBOOK <name> SET login = 'isolated'` (or `CREATE PLAYBOOK <name> SET login = 'isolated'`); it writes `isolated_login = true` in its `.playbook`. `CPB_ISOLATED_LOGIN=true` does the same for one launch | detached at once; log in there once (an account with `auth = 'claude_login'` names that login), or run it on a token account. Over a login of its own, a token run sets that login aside for the run on Linux and is refused on macOS (see below). `SET login = 'shared'` is refused while it holds its own login, which a shared launch would set aside (another account's) or copy over the machine's (the same account's) |

A token or a key is never a loose variable: an env set, `DEFAULTS` or a
playbook's own `SET VAR` that sets `CLAUDE_CODE_OAUTH_TOKEN` (or any other
variable an account owns) refuses the launch. `BLOCK VAR` of it is fine.

## The token middle ground

If you use a long-lived token (`claude setup-token`, stored at
`~/.config/claude-code/oauth-token`), every playbook launch injects it as
`CLAUDE_CODE_OAUTH_TOKEN` and removes the playbook's own stored login so a
transient 401 cannot swap the working token for a dead one. That is right for
most playbooks and wrong for one that must use a different account or a proxy
that does not want the token.

Blocking `CLAUDE_CODE_OAUTH_TOKEN` for a playbook, directly or through an
env set, does more than drop the variable: the token is treated as inactive for
that playbook, so the launch takes the stored-credentials path. No token is
injected, the playbook's own login is left alone, and the shared credentials are
synced. `/login` once there and it sticks, while every other playbook keeps using
the token. A per-playbook token is an account (`claude_token`), never a
variable: setting `CLAUDE_CODE_OAUTH_TOKEN` in a layer refuses the launch,
and one exported in your shell is removed at every launch, so the token file
decides for the `machine` account. A launch on a `claude_token` account counts
as another account, so the plan descriptors read from your global login are
not injected into it; only the token read from the token file gets them.
This is a middle ground between sharing the token and `isolated_login` (the
playbook shares nothing).

## A token over an isolated playbook's own login

An isolated playbook can hold a `/login` of its own and still be launched on
a token account, or on an account that uses no Claude login at all (a
router's key). While it runs, the stored login must not stay readable: on an
error Claude Code would swap it in over the token, or send it where the
account's requests go.

- **On Linux** the login is a file. cpb moves the grant out of the
  playbook's `.credentials.json` into `.credentials.json.cpb-aside` for the
  run, and puts it back when the last such run of that playbook ends. Other
  keys in the store (MCP logins) stay where they are. A run that was killed
  leaves the file aside; the next launch of the playbook puts it back first.
  A `/login` during the token run wins: the earlier login is kept as
  `.credentials.json.cpb-aside-<stamp>`, never overwritten.
- **On macOS** the login lives in the Keychain, which cpb cannot move
  without reading it, so it stays in place. With Claude Code 2.1.225 or
  newer that is safe (since v4.3.2): the token outranks any stored login,
  and Claude Code keeps it on a 401 instead of swapping the login in. With
  an older claude the launch is refused before anything changes, naming
  where the login is, the version, and the fixes: `claude update`, or

```
cpb run --account <account> <name>   # on an account it lists that uses its login (or machine)
```

  Or `/logout` in the playbook once, and it runs on the account from then
  on.

## Only the machine's own account is shared

Claude Code writes its plaintext login store by renaming a new file over
`.credentials.json`. So a refresh or a `/login` inside a shared playbook
replaces cpb's link with a file of its own. At the next sync, cpb decides
what to do with that file by account:

- **The same account as the machine's** (the `accountUuid` in the
  playbook's `.claude.json` equals the one in `~/.claude.json`): the newer
  login is copied over the machine's store and the link comes back. This is
  an ordinary refresh.
- **Another account, or one cpb cannot confirm:** the file is kept as
  `.credentials.json.cpb-own-<stamp>`. Its account state leaves the
  playbook's `.claude.json`, with a backup. The link comes back, and one line
  says so. The machine's login is never replaced by another account's.
  - To keep that account in that playbook, run `cpb ALTER PLAYBOOK <name>
    SET login = 'isolated'` and move the file back.

cpb also takes account state for a shared playbook only from the machine's
own `~/.claude/.claude.json` or `~/.claude.json`, never from another
playbook's.

**On macOS** Claude Code keeps each config directory's login in the Keychain
(`Claude Code-credentials-<hash of the directory>`), with the file as a
fallback:
- A shared playbook uses the machine's login through the link only until its
  first refresh or `/login`. From then on it reads its own Keychain item.
- cpb never copies a Keychain item, so this path cannot move a login onto
  the machine.
- The file rules above apply when Claude Code falls back to the file. On
  Linux the file is the only store.

## A source never carries a login

A playbook you install brings no login with it. `CREATE PLAYBOOK … FROM`
leaves a source's `.credentials.json`, and the
account keys of its `.claude.json`, out of the install, and says so in one
line each. `LINK` sets them aside in place rather than deleting anything.

## Routing a playbook to another backend

A route to another backend (a GLM plan through a router, say) is an
account: its address and its key travel together, so your Claude login
never goes there ([Accounts](accounts.md)):

```bash
cpb CREATE ACCOUNT glm SET provider = 'gateway', base_url = 'http://localhost:4000/v1', auth_token FROM 'keychain:router-key', model = 'glm-5.3'
cpb ALTER PLAYBOOK glm SET login = 'isolated' USE ACCOUNT glm
```

The playbook should carry `login = 'isolated'`: an account that uses no
Claude login never reaches one, so on a shared playbook the launch is
refused. Isolation also matters for another reason: Claude Code decides
which claude.ai-hosted tools to send from the feature flags it cached while
an Anthropic account was logged in, not from where requests go; a playbook
that ran as your global account before being rerouted keeps sending them,
and since Claude Code 2.1.265 at least one backend (GLM) rejects the
Artifact tool's schema with `400` on every interactive turn. Isolation
removes that leftover state at launch while the playbook has no login of its
own, and `cpb auth status` shows `no login; stale account state, purged at
launch` until it has.

**A gateway you trust.** Some setups put a gateway in front of Anthropic (a
company proxy that logs or rate-limits) and want your Claude credential to
go through it. That gateway is an account that says so, over `https` only:

```bash
cpb CREATE ACCOUNT corp SET provider = 'gateway', base_url = 'https://gateway.corp.example/v1', claude_token FROM 'keychain:claude-work', claude_login_trusted = true
```

Without `claude_login_trusted`, a gateway account with a Claude credential
is refused when written. A playbook installed from a source, or built by
`cpb play` from a recipe, never names an account: that is yours to do.

## See where every playbook stands

Which account each playbook runs on, and where its token comes from (names
and states only, never the token):

```
cpb SELECT name, account, auth_mode, token_source FROM PLAYBOOKS
```

`account` is the account a launch runs on; `token_source` is `none`,
`account` or `file` (the machine's token file). `EXPLAIN PLAYBOOK
<name>` says the same in its `Account:` line (and, on `machine`, a `Claude
token:` line).

Without launching anything:

```bash
cpb auth status
```

```text
NAME               MODE                          STORE                                   EXPIRES   DAEMON  NOTE
~/.claude          shared-login                  file                                    in 6h12m  -
work               token                         absent                                  -         -
review             shared-login (token blocked)  symlink -> ~/.claude/.credentials.json  in 6h12m  -
personal           isolated-login                file                                    in 22m    -
```

`MODE` is the decision `run` would make on the account the playbook runs on
and its own layers: `token` (the machine's token file), `playbook-token` (a
`claude_token` account), `shared-login`, `isolated-login`, `account` (an
account with its own key, no Claude login) or `error` (the launch would be
refused, the reason in `NOTE`). `(token blocked)` marks a playbook that blocks
the machine's token, or runs on an `auth = 'claude_login'` account, so it uses
the stored login; `--json` says `"token_blocked": true`. `EXPIRES` is the stored grant's expiry.
`DAEMON` reads Claude Code's own `daemon-auth-status.json`, shown as
`auth_required` only when the marker is newer than the current grant. `--json`
for scripts (times in UTC), `--claude` to add `claude auth status` per
directory (`logged_in`, `subscription_type`, `auth_method`).

## Two things to know about shared-login mode

Claude Code namespaces its macOS Keychain entry per config directory and
refreshes the OAuth grant from whichever directory hits expiry first; with many
playbooks sharing one symlinked file, two concurrent refreshes can race and the
loser's `invalid_grant` empties the shared file, logging every playbook out at
once. That race is why the long-lived token path exists.

And raw `claude` launches bypass all of the above: only launchers, `run`, and
`start` prepare authentication.
