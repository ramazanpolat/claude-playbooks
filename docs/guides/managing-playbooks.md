# Managing playbooks

Creating, installing, linking, launching, renaming, updating and deleting them,
and keeping a whole setup in one playbook file. The statements below are the
[CLI grammar](../reference/cli-grammar.md) (v3.20.0); keywords are
case-insensitive. The older commands still work, see the end.

cpb reuses your existing Claude Code authentication for new playbooks, so a new
playbook normally opens Claude Code directly instead of asking you to log in
again. How depends on whether you use a long-lived token; see
[Authentication](authentication.md).

## Create your own playbook

```bash
cpb CREATE PLAYBOOK experiment
experiment
```

This creates `~/.claude-playbooks/experiment`, drops in a starter `CLAUDE.md`
that introduces the playbook concept to the session opened inside it, syncs
Claude auth metadata, and registers a launcher command named `experiment`: a
symlink to the `claude-playbook` binary on your PATH. It works immediately, in
every shell, with no rc-file edit.

```bash
cpb CREATE PLAYBOOK backend ALIAS be          # the launcher is `be`
cpb CREATE PLAYBOOK scratch NO ALIAS          # no launcher
cpb CREATE PLAYBOOK boxed SANDBOX             # always sandboxed, with its own login
cpb CREATE PLAYBOOK IF NOT EXISTS experiment  # a no-op when it exists
```

Run it without the launcher, passing Claude Code flags after the name:

```bash
cpb run experiment
cpb run experiment --model claude-opus-5 --permission-mode auto
```

## See what is installed

```bash
cpb SHOW                          # the same as SHOW PLAYBOOKS
cpb SHOW PLAYBOOK experiment      # path, version, source, launcher, env sets, variables, sandbox
cpb SHOW PLAYBOOKS --json         # the stable form for scripts
```

```
NAME        VERSION  LAUNCHER  ENV SETS  SOURCE
awesome     1.4.0    ap        glm       https://github.com/user/awesome
experiment  -        -         -         -
```

The human layout may change between releases; scripts read `--json`.

## Install a shared playbook repo

`install` is the one-step shortcut: clone, install and create the launcher,
taking the name and launcher from the source's manifest.

```bash
cpb install https://github.com/ramazanpolat/awesome-playbooks
cpb install https://github.com/user/awesome --name team-tools --alias tt
cpb install ~/dev/my-playbook              # a local directory, copied
```

The statement form says the same with every choice written out, which is what a
playbook file uses:

```bash
cpb CREATE PLAYBOOK team-tools FROM https://github.com/user/awesome BRANCH main ALIAS tt
```

### Install one playbook from a larger repo

```bash
cpb install https://github.com/user/awesome/tree/main/playbooks/dba
cpb install https://github.com/user/awesome --subdir playbooks/dba --name dba --alias ap-dba
cpb CREATE PLAYBOOK dba FROM https://github.com/user/awesome SUBDIR playbooks/dba ALIAS ap-dba
```

Cherry-picked installs are flat top-level playbooks. Branch names containing `/`
are resolved against the repository's remote refs; `--branch` / `BRANCH` makes
the boundary explicit.

## Develop a playbook in place

`LINK` registers a directory you are editing outside `~/.claude-playbooks`, so
changes are live:

```bash
cpb CREATE PLAYBOOK dev LINK ~/dev/my-playbook
cpb CREATE PLAYBOOK dev LINK ~/dev/my-playbook NO ALIAS
```

A statement never prompts, so the target needs a `.playbook` first (at least
`name = "..."`). A linked playbook's manifest belongs to the target: its
environment and plugin clauses are refused, edit the target's files instead.
Dropping a linked playbook removes only the symlink.

## Launcher commands

`CREATE PLAYBOOK` and `install` register each playbook as a **launcher command**:
a symlink to the `claude-playbook` binary placed next to it (falling back to
`~/.local/bin` when that directory is not writable):

```text
~/.local/bin/experiment -> /usr/local/bin/claude-playbook
```

Invoked through the link, the binary sees the link's name in `argv[0]` and
behaves as `cpb run <name>`, the multicall pattern of busybox and git. The name
resolves against the live registry (directory name first, then the manifest's
`alias`) at invocation time, so the launcher carries no state that can go stale.
Launchers work from any shell, in scripts and in cron.

A playbook has one launcher: its alias, or its name.

```bash
cpb ALTER PLAYBOOK experiment ALIAS exp          # set or replace it
cpb ALTER PLAYBOOK experiment ALIAS experiment   # back to the name
cpb ALTER PLAYBOOK experiment NO ALIAS           # none
```

Dropping a playbook removes the launchers named for it. It never removes a
launcher another playbook still claims, by spelling or, on a case-insensitive
filesystem, by being the same entry under another spelling. Launchers are only
ever written for the default playbooks root.

## Temporary sessions

`start` runs Claude Code on any directory without registering a playbook:

```bash
cpb start /tmp/scratch
cpb start /tmp/scratch --model claude-opus-5
cpb start /tmp/scratch --delete        # remove the directory when the session ends
```

`--delete` counts only before the path or right after it; later in the line it
goes to `claude` untouched.

## Rename and drop

```bash
cpb ALTER PLAYBOOK experiment RENAME TO lab
cpb ALTER PLAYBOOK lab RENAME TO experiment ALIAS exp
cpb DROP PLAYBOOK experiment              # asks for confirmation on a terminal
cpb DROP PLAYBOOK IF EXISTS awesome --yes
```

`RENAME TO`, `ALIAS` and `NO ALIAS` are not combined with environment clauses in
one statement: use two, so each applies whole or not at all.

## Plugins and the agent

A playbook is Claude Code's user scope, so its plugins and its main-thread agent
are its own:

```bash
cpb ALTER PLAYBOOK k ADD MARKETPLACE kommander FROM 'github:ramazanpolat/kommander-playbook' \
    ADD PLUGIN kommander@kommander SET AGENT 'kommander'
```

The marketplace and plugin clauses run Claude Code's own `claude plugin …` with
the playbook as `CLAUDE_CONFIG_DIR`, reading the state first so a repeat runs
nothing; `SET AGENT` writes `agent` in the playbook's `settings.json`. Sources,
rules and the confirmation guard for marketplace-declared commands are in
[Plugins and the agent](../reference/cli-grammar.md#plugins-and-the-agent).

## One file for a whole setup

A **playbook file** (conventionally `playbook.cpb`) holds statements, one per
`;`, and `APPLY` runs it. `SHOW CREATE` writes one from what is installed:

```bash
cpb SHOW CREATE ALL > playbook.cpb       # env sets, DEFAULTS, playbooks, plugins
cpb APPLY playbook.cpb --dry-run         # what would change, nothing written
cpb APPLY playbook.cpb                   # on another machine, or again here: nothing changes
```

`APPLY` checks every statement (syntax, secret references, `DROP PLAYBOOK` needs
`--yes`) before writing anything, then runs them in order and stops at the first
failure; every statement `SHOW CREATE` writes is safe to repeat, so running the
fixed file again is the recovery. `SHOW CREATE` never prints a credential: a
credential-looking literal becomes a comment and the command exits non-zero
unless `--skip-secrets`. A file can `INCLUDE 'base.cpb'` another, relative to
itself, so layers stack; see
[playbook.cpb](../reference/cli-grammar.md#playbookcpb-show-create-and-apply) and
[INCLUDE](../reference/cli-grammar.md#include).

## Update

Update pulls the playbook from the source recorded in its `.playbook`:

```bash
cpb update awesome
cpb update awesome --check    # report the available version only
```

Git installs record their repository, branch and selected subdirectory, and a
flat, non-linked install updates natively from that source. There is no
delegated update script: the CLI owns the update.

The update replaces only the top-level entries the source itself ships, in place.
Runtime state the source knows nothing about (`data/`, `projects/`, `sessions/`,
`history.jsonl`) is never read, moved or copied, so a session writing to it
during the update cannot lose work. Replaced entries are moved to a timestamped
`.<name>.bak.<stamp>` beside the install first, and rolled back if the overlay
fails.

Local configuration survives even when the source ships its own copy:
`settings.json`, `settings.local.json`, `.credentials.json` and `.claude.json`
are always restored over the incoming files, and a playbook names anything
further in its manifest:

```toml
[update]
preserve = ["settings.json", "config/local.toml"]
```

New stock settings still arrive alongside (conventionally
`settings.json.template`) for you to merge by hand. Afterwards, an executable
`migrations/apply.sh` in the playbook runs as
`migrations/apply.sh <from-version> <to-version> <install-dir>`; runners are
expected to be idempotent.

Linked playbooks and manifests that select their config through a top-level
`subdir` cannot be updated this way. `cpb update --all` was withdrawn in v3.15.0;
update playbooks one at a time. `cpb update` with no name self-updates the
binary; see [Installation](installation.md#updating-the-tool).

## Use temporary config locations

For tests or demos, keep playbooks away from your real files:

```bash
CLAUDE_PLAYBOOKS_DIR=/tmp/playbooks cpb CREATE PLAYBOOK demo
cpb --playbooks-dir /tmp/playbooks CREATE PLAYBOOK demo
```

Before a statement, only `--playbooks-dir` and `--launcher-dir` are accepted.
Launcher commands are only managed for the default root (`~/.claude-playbooks`),
so a temporary root never touches your PATH.

## A playbook's bin directory

Some playbooks ship CLI tools in `bin/`. Add them to your PATH yourself:

```bash
export PATH="$HOME/.claude-playbooks/experiment/bin:$PATH"   # in ~/.zshrc
```

## Relationship to CLAUDE.md

A playbook's `CLAUDE.md` is loaded as standing instructions at the start of every
session in it. It is separate from a project's `CLAUDE.md`, and both are loaded:
the playbook's says *how you work*, the project's *what you are working on*.

## Older commands

`create <name>`, `link`, `delete` (and `uninstall`, `unlink`), `rename`,
`alias`, `dealias`, `list` and `info` still work with all their flags,
hidden from help; on a terminal each prints one stderr line naming its
statement. The statement for each:

| Older | Statement |
|---|---|
| `create <n> [--alias a \| --no-alias] [--sandbox]` | `CREATE PLAYBOOK <n> [ALIAS a \| NO ALIAS] [SANDBOX]` |
| `link <dir> [--name n] [--alias a \| --no-alias]` | `CREATE PLAYBOOK <n> LINK <dir> [ALIAS a \| NO ALIAS]` |
| `delete <n> [-y]` | `DROP PLAYBOOK <n> [--yes]` |
| `rename <a> <b> [--alias x \| --no-alias]` | `ALTER PLAYBOOK <a> RENAME TO <b> [ALIAS x \| NO ALIAS]` |
| `alias <n> <a>` / `alias <n> --remove`, `dealias <n>` | `ALTER PLAYBOOK <n> ALIAS <a>` / `NO ALIAS` |
| `list [prefix]` / `info <n>` | `SHOW PLAYBOOKS` / `SHOW PLAYBOOK <n>` |

`dealias` clears an alias only; `NO ALIAS` removes the playbook's launcher,
whichever it is.
