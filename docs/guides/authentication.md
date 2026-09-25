# Authentication

Which Anthropic account, token, or login a playbook's session uses — the
identity boundary.

At every launch `cpb` first decides whether a **long-lived token** is active for
that playbook, then prepares the config directory accordingly. Nothing else in
the tool touches credentials.

```text
token active for this playbook?
  = the playbook's [env] (or a profile it uses) sets CLAUDE_CODE_OAUTH_TOKEN
    or the shell exports CLAUDE_CODE_OAUTH_TOKEN
    or ~/.config/claude-code/oauth-token is non-empty
  and the playbook's [env] does not unset it

yes  ->  inject the token; remove the playbook's own stored login (claudeAiOauth only,
         MCP logins survive); sync non-secret account metadata so the dir presents as logged in
no   ->  link the playbook's .credentials.json to ~/.claude/.credentials.json; remove nothing;
         Claude Code refreshes the shared login itself
isolate_auth = true  ->  neither: detach from the shared store, strip the global token,
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
| One playbook keeps its own `/login` while the others use the token | `cpb env <name> unset CLAUDE_CODE_OAUTH_TOKEN` | that playbook takes the no-token path; the rest unchanged |
| One playbook uses its own token | `cpb env <name> set CLAUDE_CODE_OAUTH_TOKEN=...` | that token wins over the file; its own login removed |
| One playbook is a different account, sharing nothing | `isolate_auth = true` in its `.playbook`, or `CLAUDE_PLAYBOOKS_ISOLATE_AUTH=true` | detached; log in there once; add `set CLAUDE_CODE_OAUTH_TOKEN` for a per-account token |

The unset and set forms can come from an
[env profile](environment.md#env-profiles-define-once-attach-to-many) shared by
several playbooks.

## The token middle ground

If you use a long-lived token (`claude setup-token`, stored at
`~/.config/claude-code/oauth-token`), every playbook launch injects it as
`CLAUDE_CODE_OAUTH_TOKEN` and removes the playbook's own stored login so a
transient 401 cannot swap the working token for a dead one. That is right for
most playbooks and wrong for one that must use a different account or a proxy
that does not want the token.

Unsetting `CLAUDE_CODE_OAUTH_TOKEN` for a playbook, directly or through a
profile, does more than drop the variable: the token is treated as inactive for
that playbook, so the launch takes the stored-credentials path. No token is
injected, the playbook's own login is left alone, and the shared credentials are
synced. `/login` once there and it sticks, while every other playbook keeps using
the token. Setting the variable instead supplies a per-playbook token that wins
over the machine-global file; such a launch counts as another account, so the
plan descriptors read from your global login are not injected into it (the
profile may set its own). The same holds for a token you export in the shell
yourself: only the token read from the token file gets the global descriptors.
This is a middle ground between sharing the token and `isolate_auth` (the
playbook shares nothing).

## Routing a playbook to another backend

A playbook whose `settings.json` or env block points `ANTHROPIC_BASE_URL` at a
third-party Anthropic-compatible endpoint (a GLM plan through a router, say)
should carry `isolate_auth = true`. Claude Code decides which claude.ai-hosted
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
NAME               MODE          STORE                                   EXPIRES   DAEMON  NOTE
~/.claude          shared-login  file                                    in 6h12m  -
kommander          token         absent                                  -         -
kommander-9router  own-login     symlink -> ~/.claude/.credentials.json  in 6h12m  -
personal           isolated      file                                    in 22m    -
```

`MODE` is the decision `run` would make. `EXPIRES` is the stored grant's expiry.
`DAEMON` reads Claude Code's own `daemon-auth-status.json`, shown as
`auth_required` only when the marker is newer than the current grant. `--json`
for scripts, `--claude` to add `claude auth status` per directory.

## Two things to know about shared-login mode

Claude Code namespaces its macOS Keychain entry per config directory and
refreshes the OAuth grant from whichever directory hits expiry first; with many
playbooks sharing one symlinked file, two concurrent refreshes can race and the
loser's `invalid_grant` empties the shared file, logging every playbook out at
once. That race is why the long-lived token path exists.

And raw `claude` launches bypass all of the above: only launchers, `run`, and
`start` prepare authentication.
