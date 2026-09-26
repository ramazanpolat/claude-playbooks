# claude-playbook for agents

How an AI agent (Claude Code, Codex, OpenCode, a cron job, a CI step) drives `claude-playbook` without a human at the keyboard. It is the same CLI a person uses; the difference is which forms are safe unattended and what to read instead of guess.

The binary is `claude-playbook`, also on PATH as `cpb`. State changes go through the statement grammar (`cpb <VERB> <OBJECT> <name> <clause> …`), whose contract is [docs/reference/cli-grammar.md](../reference/cli-grammar.md); `SPEC-v4.md` is the contract for everything else. When this guide and a spec disagree, the spec wins.

## What a playbook is, in one sentence

A directory under `~/.claude-playbooks/` that Claude Code treats as its whole configuration (`CLAUDE_CONFIG_DIR`), plus an optional `.playbook` manifest, launched through a command that binds that directory and prepares its authentication and environment.

## Read state from --json, never from the human form

Never assume a playbook exists or a name is free. The registry is the filesystem, read fresh on every call. Every `SHOW` and `EXPLAIN` has a `--json` form that is the contract: fields may be added, an existing field never changes meaning within a major version. The human form may change between releases; do not grep it.

```bash
cpb SHOW PLAYBOOKS --json            # an array of playbook objects (a bare `cpb SHOW --json` is the same)
cpb SHOW PLAYBOOK <name> --json      # name, version, path, source, linked, launcher, envs, vars, sandbox, marketplaces, plugins, agent
cpb SHOW ENVS --json                 # env sets: name, description, vars, used_by, default
cpb SHOW DEFAULTS --json             # {"envs": [...], "secret_helper": {...} | null}
cpb EXPLAIN PLAYBOOK <name> --json   # every variable a launch would change, with its layer
```

A variable is one object with exactly one of `value`, `ref`, `redacted` (with `plaintext`) or `blocked`. A credential's value is never printed in any form.

Before a headless run that must not hit a login wall, check authentication without launching:

```bash
cpb auth status --json           # per playbook: mode, store, expires_at, reauth_required
cpb auth status <name>           # one row, human-readable
```

Blockers: `"mode":"error"` (the launch is refused) and `"reauth_required":true` (only set for the stored-login modes `own-login`, `shared-login`, `isolated`). Report these instead of retrying. `"expired":true` is advisory: Claude Code refreshes the stored grant at launch while its refresh token is valid. Token modes (`token`, `own-token`) carry no stored login to judge.

## Launch a session headlessly

`run` forwards every argument after the name straight to `claude`, so Claude Code's own headless flags apply:

```bash
cpb run <name> -p "summarize the open tasks"            # one prompt, print, exit
cpb run <name> -p "..." --output-format json
cpb run <name> --version                                 # cheapest liveness check
<name> -p "..."                                          # the launcher form is identical
```

The exit code is `claude`'s. Put the playbook name **before** any `claude` flag; `--help` before the name is the tool's help, after it belongs to `claude`.

One-off environment for a single launch, nothing written to disk. Launch flags are recognised only as a leading run, before the name or immediately after it:

```bash
cpb run --env-profile work <name> -p "..."               # an existing env set, this launch only
cpb run <name> --env ANTHROPIC_MODEL=claude-opus-5 -p "..."
cpb run --unset CLAUDE_CODE_OAUTH_TOKEN <name> -p "..."  # use the stored login for this run
cpb run --env-file ./job.env <name> -p "..."             # KEY=VALUE lines; validated like a manifest
```

A missing or broken `--env-profile` refuses the launch. `--env` after a `claude` argument is forwarded to `claude`, not applied.

For a throwaway config directory that is not a registered playbook:

```bash
cpb start /tmp/scratch-$$ -p "..." --delete    # directory created, then removed on exit
```

## Change state without prompts

Statements never prompt, with one exception: `DROP PLAYBOOK` asks on a terminal, so pass `--yes`.

```bash
cpb CREATE PLAYBOOK IF NOT EXISTS <name> NO ALIAS            # no launcher; run via `run <name>`
cpb CREATE PLAYBOOK <name> ALIAS <cmd>
cpb CREATE PLAYBOOK <name> FROM <git-url> BRANCH <ref> SUBDIR <path> NO ALIAS
cpb CREATE PLAYBOOK <name> LINK <dir> NO ALIAS              # the target needs a .playbook first
cpb ALTER PLAYBOOK <name> RENAME TO <new>
cpb DROP PLAYBOOK IF EXISTS <name> --yes
cpb install <git-url-or-dir> --name <name> --no-alias       # the one-step shortcut
cpb update <name>                                           # from [source]; settings.json, data/, [env] survive
cpb update <name> --check                                   # versions only, touches nothing
```

`IF NOT EXISTS` / `IF EXISTS` make a statement safe to repeat: "already there" and "not there" become no-ops. Name collisions are hard errors before anything is copied (`command name "x" already addresses playbook "y"`). A statement applies whole or not at all, and a non-zero exit means nothing changed, with one exception: the plugin clauses run `claude plugin` commands in order, and the error names the ones that already ran (running the statement again finishes it).

## Keep a whole setup in one file

For more than a statement or two, write a playbook file and apply it; it is idempotent and reviewable:

```bash
cpb SHOW CREATE ALL --skip-secrets > playbook.cpb   # what is installed, as statements
cpb APPLY playbook.cpb --dry-run                    # per statement: created / changed / unchanged, and any `claude plugin` commands
cpb APPLY playbook.cpb                              # validates everything first, then runs in order
cpb APPLY base.cpb machine.cpb --yes                # several files; --yes confirms their DROP PLAYBOOKs
```

`APPLY` writes nothing when any statement fails validation (syntax, secret references, a `DROP PLAYBOOK` without `--yes`). It then runs the statements in order and stops at the first failure, reporting what was applied per file; there is no rollback, and running the fixed file again is the recovery. The summary line is `Applied <files>: N created, N changed, N unchanged, N dropped`. `SHOW CREATE` without `--skip-secrets` exits non-zero when it had to withhold a credential-looking literal.

A file may `INCLUDE '<path>'` another (relative to itself; local regular files only; a cycle is refused; a file reached twice runs once).

## Sandbox with --playbooks-dir

Point the whole registry at a scratch root to test without touching the user's installs. Launchers are not managed for a non-default root, which is what you want in a sandbox.

```bash
export CLAUDE_PLAYBOOKS_DIR=/tmp/pb-$$              # or --playbooks-dir before the verb
cpb CREATE PLAYBOOK demo NO ALIAS
cpb run demo --version
rm -rf /tmp/pb-$$
```

Before a statement, only `--playbooks-dir` and `--launcher-dir` are accepted. `run`, `start` and `update` accept `--playbooks-dir` before the name as well. Env sets are resolved from the same root (`<root>/.env-profiles/`).

## Run inside a Docker Sandbox

`cpb run --sandbox <name>` runs the playbook's Claude Code in a microVM (the `sbx` CLI must be installed and logged in; `cpb` refuses with an install hint otherwise). Only the working directory and the playbook's own directory are mounted, at their host paths; the environment is reduced to what the playbook's layers set plus the authentication variables.

```bash
cpb run --sandbox --workdir "$REPO" demo -p "run the tests"      # sandbox cpb-demo, created on first use
cpb run --sandbox --sandbox-fresh --clone --workdir "$REPO" demo -p "..."   # new sandbox on a private clone
cpb run --sandbox --mount /data:ro demo                          # extra read-only mount
cpb CREATE PLAYBOOK demo SANDBOX NO ALIAS                        # [sandbox] always = true + isolate_auth = true
cpb run --no-sandbox demo -p "..."                               # host launch; stderr says the manifest was overridden
```

The machine login never enters the sandbox: a shared-login playbook authenticates on its own inside (`/login` once there); a token from an env set works unchanged. A playbook whose layers hold a secret reference is refused in a sandbox in this release. `--sandbox-host user@host` (or `[sandbox] host`) forwards the whole launch over ssh. `ANTHROPIC_AUTH_TOKEN` and `ANTHROPIC_API_KEY` from the layers are proxy-injected unless `[sandbox] secrets = "env"`. Network egress is the `sbx` policy's plus `[sandbox].allow_net` and the host of `ANTHROPIC_BASE_URL`; check `sbx policy log` before blaming a tool that cannot reach a service. See [Sandboxed sessions](sandbox.md).

## Environment and secrets

```bash
cpb ALTER PLAYBOOK <name> SET VAR KEY=VALUE [KEY=VALUE ...]
cpb ALTER PLAYBOOK <name> BLOCK VAR KEY          # removed at launch even if the shell exports it
cpb ALTER PLAYBOOK <name> UNSET VAR KEY          # forget the playbook's own entry
cpb CREATE OR REPLACE ENV <set> SET KEY=VALUE    # an env set, stated whole
cpb ALTER PLAYBOOK <name> USE ENV <set> [<set> ...]
cpb ALTER DEFAULTS USE ENV <set> [...]           # layered under every playbook
cpb DROP ENV <set>                               # refused while a playbook or DEFAULTS uses it
```

Layering at launch, later wins: shell environment, `DEFAULTS` in order, the playbook's env sets in order, the playbook's own `SET VAR` / `BLOCK VAR`, one-off launch flags, then `CLAUDE_CONFIG_DIR`. Details: [Environment overrides](environment.md).

A credential-looking literal (`*TOKEN*`, `*SECRET*`, `*_KEY`, …) is refused. Store it by reference through a secret helper the pilot configured (`SET KEY FROM '<ref>'`; `SHOW DEFAULTS --json` says whether one is), or, only when told to, `AS PLAINTEXT`. Never put a secret value on a command line you log, and never paste one into a playbook file.

## Choose an authentication mode per playbook

At every launch cpb decides whether a long-lived token is active for that playbook (`~/.config/claude-code/oauth-token`, or the variable exported, or the playbook's layers setting it, and not blocking it). Token active: inject it and remove the playbook's own stored login so a 401 cannot swap the token for a dead grant. Token inactive: symlink the playbook's credentials to `~/.claude/.credentials.json`. `isolate_auth = true`: share nothing.

| Goal | Statement |
|---|---|
| this playbook keeps its own `/login` while others use the token | `ALTER PLAYBOOK <name> BLOCK VAR CLAUDE_CODE_OAUTH_TOKEN` |
| this playbook uses a specific token | `ALTER PLAYBOOK <name> SET VAR CLAUDE_CODE_OAUTH_TOKEN=<token> AS PLAINTEXT` (this key never takes a reference) |
| a different account entirely, or `ANTHROPIC_BASE_URL` to another backend | `isolate_auth = true` in its `.playbook` (no statement for it yet) |
| this playbook talks to a proxy | `CREATE ENV <p> SET ANTHROPIC_BASE_URL=…`, then `ALTER PLAYBOOK <name> ADD ENV <p>` |

An agent cannot complete an interactive `/login`. If a headless run exits with an authentication error, report it and stop; do not retry in a loop, and do not edit `.credentials.json`.

## Rules that keep the registry consistent

- Go through the CLI for anything it has a statement for. Hand edits are honoured but never defended: a broken manifest fails loudly at the next use.
- Never write into a playbook source directory you were given to install from; `install` and `update` stage a private copy, and so should you.
- Do not put secrets into a playbook you intend to publish. Env blocks and env sets are install-local by design: `update` ignores a source-shipped block and `install` drops it with a note.
- A raw `claude` launch bypasses authentication preparation and environment layers. For the playbook's semantics, launch through `run`, `start`, or the launcher.
- Registry mutations are serialized by a lock; launches take no lock and read the manifest at launch time.
- The self-update is `cpb update` with no name; `--check` reports without installing.

## Reading errors

Errors go to stderr with exit 1, and name the thing that is wrong without echoing a value. A statement's error starts with its position: `word N` on the command line, `line N, col M` in a file, and `APPLY` adds the file name:

```text
unknown playbook "x". Run 'claude-playbook list' to see available playbooks
command name "x" already addresses playbook "y". Pick another name or alias
no env set "x": create it with CREATE ENV x
env set "x" is used by a, b: detach it first with ALTER PLAYBOOK <playbook> DROP ENV x
ANTHROPIC_AUTH_TOKEN looks like a credential: use SET ANTHROPIC_AUTH_TOKEN FROM '<ref>' (needs a secret helper), or add AS PLAINTEXT to store the literal knowingly
```

A launch that was refused prints the reason and never starts `claude`; a preparation *warning* (`Warning: failed to prepare authentication state: ...`) still launches.

## Older commands

`env`, `env-profile`, `create <name>`, `link`, `delete`, `rename`, `alias`, `dealias`, `list` and `info` still work and write the same files; scripts that use them keep working in v3.20.0. They are hidden from help and completion; on a terminal they print one stderr line naming the statement, and never anything new on stdout. Move them to the statements, and parsing to `SHOW … --json`, before they are removed. The mapping is in the [CLI grammar](../reference/cli-grammar.md#pre-grammar-commands-a-hidden-fallback).
