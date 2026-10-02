# cpb for agents

How an AI agent (Claude Code, Codex, OpenCode, a cron job, a CI step) drives `cpb` without a human at the keyboard. It is the same CLI a person uses; the difference is which forms are safe unattended and what to read instead of guess.

The binary is `cpb`. State changes go through the statement grammar (`cpb <VERB> <OBJECT> <name> <clause> …`), whose contract, with every command and file, is [SPEC.md](../../SPEC.md). When this guide and the spec disagree, the spec wins.

## What a playbook is, in one sentence

A directory under `~/.claude-playbooks/` that Claude Code treats as its whole configuration (`CLAUDE_CONFIG_DIR`), plus an optional `.playbook` manifest, launched through a command that binds that directory and prepares its authentication and environment.

## Read state from --json, never from the human form

Never assume a playbook exists or a name is free. The registry is the filesystem, read fresh on every call. Every `SHOW` and `EXPLAIN` has a `--json` form that is the contract: fields may be added, an existing field never changes meaning within a major version. The human form may change between releases; do not grep it.

```bash
cpb SHOW PLAYBOOKS --json            # an array of playbook objects (a bare `cpb SHOW --json` is the same)
cpb SHOW PLAYBOOK <name> --json      # name, version, path, source, linked, launcher, envs, vars, sandbox, marketplaces, plugins, agent, mcp_servers, tools, skills, statusline, model, play
cpb SHOW ENVS --json                 # env sets: name, description, vars, used_by, default
cpb "SELECT name, envs FROM PLAYBOOKS" --json   # chosen columns, one table (anything beyond columns needs clickhouse-local)
cpb SHOW DEFAULTS --json             # {"envs": [...], "secret_helper": {...} | null}
cpb EXPLAIN PLAYBOOK <name> --json   # every variable a launch would change, with its layer
```

A variable is one object with exactly one of `value`, `ref`, `redacted` (with `plaintext`) or `blocked`. A credential's value is never printed in any form.

Before a headless run that must not hit a login wall, check authentication without launching:

```bash
cpb auth status --json           # per playbook: mode, store, expires_at, reauth_required
cpb auth status <name>           # one row, human-readable
```

Blockers: `"mode":"error"` (the launch is refused) and `"reauth_required":true` (only set for the stored-login modes `shared-login` and `isolated-login`). Report these instead of retrying. `"expired":true` is advisory: Claude Code refreshes the stored grant at launch while its refresh token is valid. Token modes (`token`, `playbook-token`) carry no stored login to judge. `"token_blocked":true` means the playbook blocks the machine's token and uses the stored login.

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
cpb run --env-set work <name> -p "..."               # an existing env set, this launch only
cpb run <name> --env ANTHROPIC_MODEL=claude-opus-5 -p "..."
cpb run --block CLAUDE_CODE_OAUTH_TOKEN <name> -p "..."  # use the stored login for this run
cpb run --env-file ./job.env <name> -p "..."             # KEY=VALUE lines; validated like a manifest
```

A missing or broken `--env-set` refuses the launch. `--env` after a `claude` argument is forwarded to `claude`, not applied.

For a throwaway config directory that is not a registered playbook:

```bash
cpb start /tmp/scratch-$$ -p "..." --delete    # directory created, then removed on exit
```

## Change state without prompts

Statements never prompt, with two exceptions: `DROP PLAYBOOK`, and `APPLY … TO '<dir>'` to a config directory that is not a playbook, ask on a terminal, so pass `--yes`.

```bash
cpb CREATE PLAYBOOK IF NOT EXISTS <name> NO LAUNCHER            # no launcher; run via `run <name>`
cpb CREATE PLAYBOOK <name> LAUNCHER <cmd>
cpb CREATE PLAYBOOK <name> FROM <git-url> BRANCH <ref> SUBDIR <path> NO LAUNCHER
cpb CREATE PLAYBOOK <name> LINK <dir> NO LAUNCHER              # the target needs a .playbook first
cpb ALTER PLAYBOOK <name> RENAME TO <new>
cpb DROP PLAYBOOK IF EXISTS <name> --yes
cpb update <name> --yes                                     # from [source]; settings.json, data/, [env] survive; --yes runs a declared migrate step
cpb update <name> --dry-run                                 # versions and the migrate step, touches nothing
```

`IF NOT EXISTS` / `IF EXISTS` make a statement safe to repeat: "already there" and "not there" become no-ops. Name collisions are hard errors before anything is copied (`command name "x" already addresses playbook "y"`). A statement applies whole or not at all, and a non-zero exit means nothing changed, with one exception: the plugin, MCP server and skill clauses run in clause order (`claude plugin`, `claude mcp`, files under `skills/`), and the error names what already ran (running the statement again finishes it).

## Keep a whole setup in one file

For more than a statement or two, write a playbook file and apply it; it is idempotent and reviewable:

```bash
cpb SHOW CREATE ALL --skip-secrets > playbook.cpb   # what is installed, as statements
cpb APPLY playbook.cpb --dry-run                    # per statement: created / changed / unchanged, and any `claude plugin` commands
cpb APPLY playbook.cpb                              # validates everything first, then runs in order
cpb APPLY base.cpb machine.cpb --yes                # several files; --yes confirms their DROP PLAYBOOKs
```

For a program, `cpb APPLY <files> [TO <target>] --dry-run --json` prints the plan as one JSON object: the verdict per statement with its resolved target and `file:line`, the exact `claude` commands a real run would make (references, never values), what it would delete and how big it is, and warning codes. Exit 0 means planned, 1 refused by the files, 2 a usage error. The schema is in the [CLI grammar](../../SPEC.md#apply---dry-run---json).

`APPLY` writes nothing when any statement fails validation (syntax, secret references, a `DROP PLAYBOOK` without `--yes`). It then runs the statements in order and stops at the first failure, reporting what was applied per file; there is no rollback, and running the fixed file again is the recovery. The summary line is `Applied <files>: N created, N changed, N unchanged, N dropped`. `SHOW CREATE` without `--skip-secrets` exits non-zero when it had to withhold a credential-looking literal.

A file may `INCLUDE '<path>'` another (relative to itself; local regular files only; a cycle is refused; a file reached twice runs once per target).

A **recipe** is a file whose `ALTER PLAYBOOK` names no playbook. Give it a target when you apply it, or with a `USE PLAYBOOK <name>;` line in the file; a file with name-less statements and no target is refused before anything is written:

```bash
cpb APPLY recipe.cpb TO <name>                  # created bare if missing; USE PLAYBOOK lines are ignored, with a warning
cpb APPLY recipe.cpb TO ~/.claude --dry-run     # a plain config directory: Claude Code's own settings only
cpb APPLY recipe.cpb TO ~/.claude --yes         # backs up settings.json (and .claude.json) once per run first
```

## Sandbox with --playbooks-dir

Point the whole registry at a scratch root to test without touching the pilot's installs. Launchers are not managed for a non-default root, which is what you want in a sandbox.

```bash
export CPB_PLAYBOOKS_DIR=/tmp/pb-$$              # or --playbooks-dir before the verb
cpb CREATE PLAYBOOK demo NO LAUNCHER
cpb run demo --version
rm -rf /tmp/pb-$$
```

Before a statement, only `--playbooks-dir` and `--launcher-dir` are accepted. `run`, `start` and `update` accept `--playbooks-dir` before the name as well. Env sets are resolved from the same root (`<root>/.env-sets/`).

## Run inside a Docker Sandbox

`cpb run --sandbox <name>` runs the playbook's Claude Code in a microVM (the `sbx` CLI must be installed and logged in; `cpb` refuses with an install hint otherwise). Only the working directory and the playbook's own directory are mounted, at their host paths; the environment is reduced to what the playbook's layers set plus the authentication variables.

```bash
cpb run --sandbox --workdir "$REPO" demo -p "run the tests"      # sandbox cpb-demo, created on first use
cpb run --sandbox --sandbox-fresh --clone --workdir "$REPO" demo -p "..."   # new sandbox on a private clone
cpb run --sandbox --mount /data:ro demo                          # extra read-only mount
cpb CREATE PLAYBOOK demo SANDBOX NO LAUNCHER                        # [sandbox] always = true + isolated_login = true
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

At every launch cpb decides whether a long-lived token is active for that playbook (`~/.config/claude-code/oauth-token`, or the variable exported, or the playbook's layers setting it, and not blocking it). Token active: inject it and remove the playbook's own stored login so a 401 cannot swap the token for a dead grant. Token inactive: symlink the playbook's credentials to `~/.claude/.credentials.json`. `isolated_login = true`: share nothing.

| Goal | Statement |
|---|---|
| this playbook keeps its own `/login` while others use the token | `ALTER PLAYBOOK <name> BLOCK VAR CLAUDE_CODE_OAUTH_TOKEN` |
| this playbook uses a specific token | none for an agent: stop and ask the human to set it. This key never takes a reference, so it would sit in the manifest as plain text, and the value would pass through your command line |
| a different account entirely, or a throwaway whose `/login` must not reach the machine's login | `ALTER PLAYBOOK <name> SET ISOLATED LOGIN` (or `CREATE PLAYBOOK <name> ISOLATED LOGIN`); the `/login` itself is the human's. Never `UNSET ISOLATED LOGIN` to "fix" a login: it is refused while the playbook holds one of its own, and the refusal is right |
| this playbook talks to a proxy | `CREATE ENV <p> SET ANTHROPIC_BASE_URL=…`, then `ALTER PLAYBOOK <name> ADD ENV <p>`. Create a playbook meant for a non-Anthropic route with `CREATE PLAYBOOK <name> ISOLATED LOGIN`; its `CLAUDE.md` goes to that provider with every request |

An agent cannot complete an interactive `/login`. If a headless run exits with an authentication error, report it and stop; do not retry in a loop, and do not edit `.credentials.json`.

## Rules that keep the registry consistent

- Go through the CLI for anything it has a statement for. Hand edits are honoured but never defended: a broken manifest fails loudly at the next use.
- Never write into a playbook source directory you were given to install from; `CREATE PLAYBOOK … FROM` and `update` stage a private copy, and so should you.
- Do not put secrets into a playbook you intend to publish. Env blocks and env sets are install-local by design: `update` ignores a source-shipped block and `CREATE PLAYBOOK … FROM` drops it with a note.
- A raw `claude` launch bypasses authentication preparation and environment layers. For the playbook's semantics, launch through `run`, `start`, or the launcher.
- Registry mutations are serialized by a lock; launches take no lock and read the manifest at launch time.
- The self-update is `cpb self-update`: the newest release of its major version, never a new major on its own (`--major` allows one). `--check` reports without installing.
- A source's migrate step (`[update] migrate`) never runs unattended by surprise: off a terminal, `update` refuses until you pass `--yes`, and `--dry-run` shows the step and its sha256.

## Reading errors

Errors go to stderr with exit 1, and name the thing that is wrong without echoing a value. A statement's error starts with its position: `word N` on the command line, `line N, col M` in a file, and `APPLY` adds the file name:

```text
unknown playbook "x". `cpb SHOW PLAYBOOKS` lists them
launcher name "x" already addresses playbook "y". Pick another name
no env set "x": create it with CREATE ENV x
env set "x" is used by a, b: detach it first with ALTER PLAYBOOK <playbook> DROP ENV x
ANTHROPIC_AUTH_TOKEN looks like a credential: use SET ANTHROPIC_AUTH_TOKEN FROM '<ref>' (needs a secret helper), or add AS PLAINTEXT to store the literal knowingly
```

A launch that was refused prints the reason and never starts `claude`; a preparation *warning* (`Warning: failed to prepare authentication state: ...`) still launches.
