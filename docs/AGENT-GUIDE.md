# claude-playbook for agents

How an AI agent (Claude Code, Codex, OpenCode, a cron job, a CI step) drives `claude-playbook` without a human at the keyboard. Everything here is the same CLI a person uses; the difference is which forms are safe unattended and what to read instead of guess.

The binary is `claude-playbook`, also on PATH as `cpb`; examples below use `cpb`, the two are the same program. `SPEC-v4.md` is the contract; when this guide and the spec disagree, the spec wins.

## What a playbook is, in one sentence

A directory under `~/.claude-playbooks/` that Claude Code treats as its whole configuration (`CLAUDE_CONFIG_DIR`), plus an optional `.playbook` manifest, launched through a command that binds that directory and prepares its authentication and environment.

## Discover before acting

Never assume a playbook exists or a name is free. The registry is the filesystem, read fresh on every call.

```bash
cpb list                 # NAME  PATH  COMMAND  LAST USED
cpb list kom             # prefix filter
cpb info <name>          # path, alias, env block, update source, exit 1 if unknown
cat ~/.claude-playbooks/<name>/.playbook   # the manifest, TOML; absent for a flat playbook
```

`info` prints `Update from: (no [source] metadata; cannot update)` when a playbook cannot be updated. `Env:` lines appear only when the playbook declares overrides.

Before a headless run that must not hit a login wall, check authentication state without launching:

```bash
cpb auth status --json           # per playbook: mode, store, expires_at, reauth_required
cpb auth status <name>           # one row, human-readable
```

A row with `"mode":"error"` or `"reauth_required":true`, or an `own-login`/`shared-login` row with `"expired":true`, will not authenticate headlessly; report it instead of retrying.

## Launch a session headlessly

`run` forwards every argument after the name straight to `claude`, so Claude Code's own headless flags apply:

```bash
cpb run <name> -p "summarize the open tasks"            # one prompt, print, exit
cpb run <name> -p "..." --output-format json
cpb run <name> --version                                 # cheapest liveness check
<name> -p "..."                                                      # the launcher form is identical
```

The exit code is `claude`'s exit code. Put the playbook name **before** any `claude` flag; `--help` before the name is the tool's help, after it belongs to `claude`.

One-off environment for a single launch, nothing written to disk. Launch flags are recognised only as a leading run, before the name or immediately after it; the first other argument ends the scan:

```bash
cpb run --env-profile work <name> -p "..."               # existing profile, this launch only
cpb run <name> --env ANTHROPIC_MODEL=claude-opus-5 -p "..."
cpb run --unset CLAUDE_CODE_OAUTH_TOKEN <name> -p "..."  # use the stored login for this run
cpb run --env-file ./job.env <name> -p "..."             # KEY=VALUE lines; validated like a manifest
<name> --env-profile work -p "..."                       # launcher form, flags first
```

A missing or broken `--env-profile` refuses the launch. `--env` after a `claude` argument is forwarded to `claude`, not applied.

For a throwaway config directory that is not a registered playbook:

```bash
cpb start /tmp/scratch-$$ -p "..." --delete    # directory created, then removed on exit
```

## Create, install, link, delete without prompts

Every mutating command has a non-interactive form. Use it.

```bash
cpb create <name> --no-alias                    # no launcher; run via `run <name>`
cpb create <name> --alias <cmd>                 # launcher named <cmd>
cpb install <git-url-or-dir> --name <name> --no-alias
cpb install <git-url> --branch <ref> --subdir <path> --name <name> --alias <cmd>
cpb link <dir> --name <name> --no-alias         # symlink an external dir; its manifest is shared state
cpb delete <name> -y                            # no confirmation; launcher kept if it still resolves elsewhere
cpb update <name>                               # from [source]; settings.json, data/, [env] survive
cpb update <name> --check                       # versions only, touches nothing
```

`link` prompts for metadata when the target has no manifest; without a TTY it refuses instead (`target has no .playbook and stdin is not a TTY; cannot prompt for metadata. Add a .playbook to the target first`). Write a minimal `.playbook` with at least `name = "..."` into the target before linking from a script.

Name collisions are hard errors before anything is copied (`command name "x" already addresses playbook "y"`). Treat a non-zero exit as "nothing changed" for `create`, `install`, `link`, and `alias`; they roll back their own partial work.

## Sandbox with `--playbooks-dir`

Point the whole registry at a scratch root to test without touching the user's installs. Launchers are not managed for a non-default root, which is what you want in a sandbox.

```bash
export CLAUDE_PLAYBOOKS_DIR=/tmp/pb-$$        # or --playbooks-dir on every call
cpb create demo --no-alias
cpb run demo --version
rm -rf /tmp/pb-$$
```

`run`, `start`, and `update` accept `--playbooks-dir` before the name as well. Env profiles are resolved from the same root (`<root>/.env-profiles/`).

## Environment overrides and profiles

Prefer the CLI over editing `.playbook` by hand: the CLI validates names, refuses `CLAUDE_CONFIG_DIR`, refuses values TOML or `os/exec` cannot carry, takes the registry lock, and writes secret-bearing files `0600`.

```bash
cpb env <name> set KEY=VALUE [KEY=VALUE...]
cpb env <name> unset KEY [KEY...]
cpb env <name> clear KEY [KEY...]
cpb env <name>                                   # shows the block and, with profiles, "Effective at launch"
cpb env-profile <profile> set KEY=VALUE ...      # creates the profile on first use
cpb env-profile <profile> unset KEY ...
cpb env <name> use <profile> [...]               # profile must exist; launch refuses a missing one
cpb env <name> unuse <profile>
cpb env-profile                                  # list: description, counts, users
cpb env-profile <profile> delete                 # refused while any playbook uses it
```

Layering at launch, later wins: shell environment, each profile in list order, the playbook's own `set`, the playbook's own `unset`, then `CLAUDE_CONFIG_DIR`. Claude Code's `settings.json` `env` is applied later inside the process and wins over all of these but cannot unset.

## Choose an authentication mode per playbook

`claude-playbook` decides at every launch whether a long-lived token is active for that playbook (`~/.config/claude-code/oauth-token`, or the variable exported, or the playbook's block setting it, and the block not unsetting it). Token active: inject it and remove the playbook's own stored login so a 401 cannot swap the token for a dead grant. Token inactive: symlink the playbook's credentials to `~/.claude/.credentials.json` and remove nothing. `isolate_auth = true`: share nothing.

| Goal | Command |
|---|---|
| this playbook keeps its own `/login` while others use the token | `cpb env <name> unset CLAUDE_CODE_OAUTH_TOKEN` |
| this playbook uses a specific token | `cpb env <name> set CLAUDE_CODE_OAUTH_TOKEN=<token>` |
| this playbook is a different account entirely | `isolate_auth = true` in its `.playbook` (this one has no CLI verb) |
| this playbook talks to a proxy | `env-profile <p> set ANTHROPIC_BASE_URL=... ` then `env <name> use <p>` |

An agent cannot complete an interactive `/login`. If a headless run exits with an authentication error, report it and stop; do not retry in a loop, and do not edit `.credentials.json`.

## Rules that keep the registry consistent

- Go through the CLI for anything it has a verb for. Hand edits are honoured but never defended: a broken manifest fails loudly at the next use, a duplicate alias dispatches deterministically but not the way you meant.
- Never write into a playbook source directory you were given to install from; `install` and `update` stage a private copy and never touch it, and neither should you.
- Do not put secrets into a playbook you intend to publish. `[env]` blocks and profiles are install-local by design: `update` ignores a source-shipped block and `install` drops it with a note.
- A raw `claude` launch bypasses authentication preparation and environment overrides. If you need the playbook's semantics, launch through `run`, `start`, or the launcher.
- Concurrency is safe for registry mutations (a flock serializes `create`, `install`, `link`, `alias`, `env`, `env-profile`, `update`); launches take no lock and read the manifest at launch time.
- The self-update is `cpb update` with no name; `--check` reports without installing.

## Reading errors

Errors are one line on stderr, exit 1, and name the thing that is wrong:

```text
unknown playbook "x". Run 'claude-playbook list' to see available playbooks
command name "x" already addresses playbook "y". Pick another name or alias
env profile "x" not found in <dir> (create it with: claude-playbook env-profile x set KEY=VALUE)
invalid .playbook at <path>: <reason>
"x" has no [source] metadata in .playbook; nothing to update from
```

A launch that was refused prints the reason and never starts `claude`; a preparation *warning* (`Warning: failed to prepare authentication state: ...`) still launches.
