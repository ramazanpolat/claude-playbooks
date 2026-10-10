# Authentication

Which Anthropic account, token, or login a playbook's session uses — the
identity boundary.

At every launch `cpb` first decides whether a **long-lived token** is active for
that playbook, then prepares the config directory accordingly. Nothing else in
the tool touches credentials.

```text
token active for this playbook?
  = the playbook's [env] (or an env set it uses) sets CLAUDE_CODE_OAUTH_TOKEN
    or the shell exports CLAUDE_CODE_OAUTH_TOKEN
    or ~/.config/claude-code/oauth-token is non-empty
  and the playbook's [env] does not unset it

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
| Everything shares one long-lived token | `claude setup-token` once | token injected everywhere; each playbook's own login removed |
| One playbook keeps its own `/login` while the others use the token | `cpb ALTER PLAYBOOK <name> BLOCK VAR CLAUDE_CODE_OAUTH_TOKEN` | that playbook takes the no-token path; the rest unchanged |
| One playbook uses its own token | `cpb ALTER PLAYBOOK <name> SET VAR CLAUDE_CODE_OAUTH_TOKEN=... AS PLAINTEXT` (this key never takes a reference: cpb reads it itself) | that token wins over the file; its own login removed |
| One playbook is a different account, sharing nothing | `cpb ALTER PLAYBOOK <name> SET login = 'isolated'` (or `CREATE PLAYBOOK <name> SET login = 'isolated'`); it writes `isolated_login = true` in its `.playbook`. `CPB_ISOLATED_LOGIN=true` does the same for one launch | detached at once; log in there once; add `set CLAUDE_CODE_OAUTH_TOKEN` for a per-account token. `SET login = 'shared'` is refused while it holds its own login, which a shared launch would set aside (another account's) or copy over the machine's (the same account's) |

The unset and set forms can come from an
[env set](environment.md#env-sets-define-once-attach-to-many) shared by
several playbooks.

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
the token. Setting the variable instead supplies a per-playbook token that wins
over the machine-global file; such a launch counts as another account, so the
plan descriptors read from your global login are not injected into it (the
env set may set its own). The same holds for a token you export in the shell
yourself: only the token read from the token file gets the global descriptors.
This is a middle ground between sharing the token and `isolated_login` (the
playbook shares nothing).

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

A playbook you install brings no login with it. `install` and
`CREATE PLAYBOOK … FROM` leave a source's `.credentials.json`, and the
account keys of its `.claude.json`, out of the install, and say so in one
line each. `LINK` sets them aside in place rather than deleting anything.

## Routing a playbook to another backend

A playbook whose `settings.json` or env block points `ANTHROPIC_BASE_URL` at a
third-party Anthropic-compatible endpoint (a GLM plan through a router, say)
should carry `isolated_login = true`. Claude Code decides which claude.ai-hosted
tools to send from the feature flags it cached while an Anthropic account was
logged in, not from where requests go; a playbook that ran as your global account
before being rerouted keeps sending them, and since Claude Code 2.1.265 at least
one backend (GLM) rejects the Artifact tool's schema with `400` on every
interactive turn. Isolation removes that leftover state at launch while the
playbook has no login of its own, and `cpb auth status` shows `no login; stale
account state, purged at launch` until it has.

## See where every playbook stands

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

`MODE` is the decision `run` would make: `token`, `playbook-token` (a token
the playbook or its env set sets), `shared-login`, `isolated-login` or
`error`. `(token blocked)` marks a playbook that blocks the machine's token,
so it uses the stored login; `--json` says `"token_blocked": true`. `EXPIRES` is the stored grant's expiry.
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
