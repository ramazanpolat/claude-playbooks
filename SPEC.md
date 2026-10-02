# cpb specification

`cpb` is a CLI tool for creating and managing **Claude Code playbooks**. A playbook is an isolated Claude Code instance — a directory with its own settings, CLAUDE.md, hooks, MCP servers, and history, completely separate from the default `~/.claude/` installation and from every other playbook.

Playbooks solve a simple problem: Claude Code stores everything in a single config directory. If you want to try a new hook, a different model default, or a custom CLAUDE.md without risking your main setup, you need a separate environment. Under the hood, a playbook is just a directory, and Claude Code reads from wherever `CLAUDE_CONFIG_DIR` points. `cpb` makes creating, running, sharing, and maintaining those directories easy.

This is the specification of cpb v4: what every statement, command and file
does. A section that describes something not built yet is marked **planned**.

## Concepts

### Isolation

Every playbook is a directory that Claude Code treats as its entire configuration root. Launching Claude Code with `CLAUDE_CONFIG_DIR=<dir>` produces a completely fresh, independent instance.

```bash
# Default Claude Code
claude

# An isolated playbook
CLAUDE_CONFIG_DIR=~/.claude-playbooks/experiment claude
```

`cpb` is a thin convenience layer over this pattern.

### The playbooks root

All playbooks live under a single **playbooks root** directory. The default is `~/.claude-playbooks/`. This is configurable via the `--playbooks-dir` flag or the `CPB_PLAYBOOKS_DIR` environment variable, applied globally to every command.

### Playbook discovery

Discovery is a single, flat rule:

- **Each direct child directory of the playbooks root is exactly one playbook.**

That is the whole rule. There is no nesting, no groups, no "container" type, no depth limit to reason about, and no manifest declaration that changes discovery. A directory does not need a `.playbook` file to be a playbook; if one is present it supplies metadata only.

```
~/.claude-playbooks/
    experiment/                 ← playbook (no manifest needed)
        CLAUDE.md
    sre/                        ← playbook (installed from a monorepo subdir)
        .playbook               ← optional metadata
        CLAUDE.md
        settings.json
    dba/                        ← playbook
        CLAUDE.md
```

Directories nested more deeply than the first level are just ordinary files belonging to a playbook — they are never themselves discovered as playbooks. If you drop a whole monorepo into the root by hand, the tool sees one playbook (the top directory), not the playbooks inside it; use `CREATE PLAYBOOK <name> FROM <source> SUBDIR <dir>` to extract the slice you actually want.

### Playbook names

A playbook's **name** is simply its directory name under the playbooks root:

- `experiment`
- `sre`
- `dba`

Names are used wherever a playbook is referenced: `run`, `auth status`, `update`, and every statement that names a playbook. Env set names are a separate namespace (`CREATE ENV`).

The charset is enforced for names being **created** (`CREATE PLAYBOOK`, with `FROM`, `LINK` or neither, and `ALTER PLAYBOOK … RENAME TO`): a name must match `^[A-Za-z0-9_][A-Za-z0-9_-]*$` — letters, digits, underscores and dashes, starting with an alphanumeric or underscore. A playbook name is interpolated into a launcher command name, a `run <name>` argument, and commands printed for the pilot to paste, so shell metacharacters are rejected at the front door rather than escaped at each site. Names must not start with `.` (to avoid hidden directories) and must not contain `/` or `\` (names are single directory segments, never paths). Lookup paths (`DROP PLAYBOOK`, discovery) only require a single path segment, so an existing playbook with an odd name can still be listed, run and removed.

### Objects

| Object | What it is | Lives at |
|---|---|---|
| `PLAYBOOK` | an installed playbook: dir, launcher, source, attached ENVs, own variables | `<root>/<name>/` |
| `ENV` | an **env set**: a named, reusable set of variables | `<root>/.env-sets/<name>.toml` |
| `DEFAULTS` | the machine-wide layer under every playbook: an ordered list of env sets; a singleton, no name | `<root>/.env-sets/.defaults` |

Two words keep the variables apart: **`ENV` is a named set**, **`VAR` is one
variable**. "Profile" is deliberately not a keyword: it would be ambiguous with other
tools' profiles.

**A playbook routed away from Anthropic** sends every request to its
`ANTHROPIC_BASE_URL`, with what its `CLAUDE.md` holds. The `CLAUDE.md` that
`CREATE PLAYBOOK` writes imports nothing; whatever you add to it, `@` imports
included, goes along. `ISOLATED LOGIN` keeps such a playbook's login apart,
and `BLOCK VAR` keeps your Anthropic credentials out of its launches
([example 15](examples/15-third-party-route/)).

## Statement grammar

### Shape

One regular grammar, read and written like DDL: you *command* cpb.

```
cpb  <VERB>   <OBJECT>   <name>   <clause> <clause> ...
     ALTER    PLAYBOOK   work     USE ENV router  SET VAR FOO=1
```

- Keywords are **case-insensitive**. Docs write them in capitals.
- Clauses are **two words** (`SET VAR`, `USE ENV`), never hyphenated.
- Lists are **space-separated**, without commas. An unquoted comma at the
  end of an item is read as a separator when another item follows it
  (`SET A=1, B=2`, `USE ENV a, b`); a final value keeps its comma, so
  `SET NO_PROXY=a,b,` stores `a,b,`. Quote a value to be explicit.
- Names follow the rules each object already has: a playbook name is
  letters, digits, `_` and `-`; an env set name also allows dots
  (`glm-5.3`). An **unquoted keyword is not a valid new name** (refused with an error naming the keyword); quoted, it is one, which is how `SHOW CREATE` writes such a name back, and an existing object whose name is a keyword can be addressed in the name slot. A launcher name is never a keyword, quoted or not.
- **Global flags go before the verb** (`cpb --playbooks-dir X ALTER …`), and
  `CPB_PLAYBOOKS_DIR` works as it does for every command. cpb recognises a statement before
  its flag parser runs, so every word after the verb belongs to the
  statement: `SET VAR OPTS=-v` is a value, and `--dry-run` /
  `--skip-secrets` are the statement's own.

### Grammar

```
command    := write | read | select | APPLY <file> [<file> ...] [TO <playbook|dir>] [--dry-run] [--yes]
                                           TO: see "Targets"
                                           select: see SELECT

write      := CREATE ENV [IF NOT EXISTS] <name> [env-clause ...]
            | CREATE OR REPLACE ENV <name> [env-clause ...]
            | ALTER  ENV <name> env-clause ...
            | DROP   ENV [IF EXISTS] <name>
            | CREATE PLAYBOOK [IF NOT EXISTS] <name> [origin] [launcher] [SANDBOX] [ISOLATED LOGIN]
            | ALTER  PLAYBOOK [<name>] pb-clause ...   no name: a recipe, see "Targets"
            | DROP   PLAYBOOK [IF EXISTS] <name> [--yes]
            | ALTER  DEFAULTS defaults-clause ...

origin     := FROM <source> [BRANCH <ref>] [SUBDIR <dir>]   clone or copy a source
            | LINK <dir>                                    develop in place
launcher   := LAUNCHER <launcher> | NO LAUNCHER                   default: the name

env-clause := SET [VAR] <key>=<value> ... [AS PLAINTEXT]
                                           literal values; AS PLAINTEXT: see Secrets
            | SET [VAR] <key> FROM '<ref>' secret by reference; resolved at launch
            | BLOCK [VAR] <key> ...        removed at launch even if the shell exports it
            | UNSET [VAR] <key> ...        forgotten; the layer below applies again
            | DESCRIPTION '<text>'

set-clause := USE ENV <env> ...            replace the attached list with exactly these, in order
            | ADD ENV <env> [FIRST | LAST | BEFORE <env> | AFTER <env>]
                                           insert one (default LAST)
            | DROP ENV <env> ...           detach

defaults-clause := set-clause
            | SET SECRET HELPER '<command>' the secret helper (see Secrets)
            | UNSET SECRET HELPER

pb-clause  := set-clause
            | SET VAR <key>=<value> ... [AS PLAINTEXT]
                                           the playbook's own layer
            | SET VAR <key> FROM '<ref>'
            | BLOCK VAR <key> ...
            | UNSET VAR <key> ...          forget the playbook's own entry (set, ref or block)
            | RENAME TO <name>
            | LAUNCHER <launcher>             set or replace the launcher (one per playbook)
            | NO LAUNCHER                     remove the launcher
            | ADD MARKETPLACE <name> FROM '<source>'   see "Plugins and the agent"
            | DROP MARKETPLACE <name>
            | ADD PLUGIN <plugin>@<marketplace>
            | DROP PLUGIN <plugin>@<marketplace>
            | SET AGENT '<agent>'
            | UNSET AGENT
            | ADD MCP SERVER <name> mcp-target [mcp-part ...]   see "An agent's configuration"
            | DROP MCP SERVER <name>
            | ALLOW TOOL '<rule>' ...      settings.json permissions.allow
            | DENY TOOL '<rule>' ...       settings.json permissions.deny
            | UNSET TOOL '<rule>' ...      forget a rule, allowed or denied
            | SET STATUSLINE '<command>' [REFRESH <n>] [IF UNSET] | UNSET STATUSLINE
            | SET STATUSLINE REFRESH <n> | UNSET STATUSLINE REFRESH
            | SET STATUSLINE PREVIOUS      the status line cpb replaced last
            | SET ISOLATED LOGIN | UNSET ISOLATED LOGIN   see "Isolated login"
            | SET SANDBOX | UNSET SANDBOX  every launch sandboxed (the login isolated too) | not; see "Sandbox"
            | SET SANDBOX <key>=<value> ...   the [sandbox] table's own keys: SET SANDBOX backend=sbx
            | UNSET SANDBOX <key> ...      forget a setting: UNSET SANDBOX host
            | SET MODEL '<model>' | UNSET MODEL
            | ADD MODEL '<id>' [LABEL '<text>'] [DESCRIPTION '<text>'] [BEHAVES AS '<id>']   see "Model picker"
            | DROP MODEL '<id>'
            | SET MODEL PICKER ONLY | SET MODEL PICKER APPEND | UNSET MODEL PICKER
            | ADD SKILL <name> FROM '<dir>'
            | ADD SKILL <name> FROM <git-url> [BRANCH <ref>] [SUBDIR <dir>]
            | DROP SKILL <name>

mcp-target := COMMAND '<command>' [ARGS '<arg>' ...]    a stdio server
            | URL '<url>' [TRANSPORT SSE]               a remote server (HTTP unless SSE)
mcp-part   := VAR <key>=<value> ...          literal values; a credential needs FROM
            | VAR <key> FROM '<ref>'
            | HEADER '<name>' '<value>'
            | HEADER '<name>' FROM '<ref>'

read       := SHOW [ PLAYBOOKS | ENVS | DEFAULTS | PLAYBOOK <name> | ENV <name> ] [--json]
            | SHOW SESSIONS [FOR PLAYBOOK <name>] [--json]
            | SHOW CREATE { PLAYBOOK <name> | ENV <name> | ALL } [--skip-secrets]
            | EXPLAIN PLAYBOOK <name> [--json]
```

The alternatives are exclusive, and the parser enforces them: `OR REPLACE`
and `IF NOT EXISTS` cannot be combined; a playbook has one origin, `FROM` or `LINK`,
and `BRANCH` / `SUBDIR` only with `FROM`; `LAUNCHER` and `NO LAUNCHER` exclude each
other; `SANDBOX` and `ISOLATED LOGIN` do not take `LINK`. The clauses of
`origin`, `launcher`, `SANDBOX` and `ISOLATED LOGIN` may come in any order.
`DROP PLAYBOOK` asks for confirmation on a terminal; `--yes` skips it.

Two limits keep every statement whole-or-nothing:

- `RENAME TO`, `LAUNCHER` and `NO LAUNCHER` are not combined with environment or
  variable clauses in one statement: a rename after an environment write
  could not be undone as one step. `RENAME TO <name> LAUNCHER <launcher>` is
  one statement; the environment change is a second.
- `CREATE PLAYBOOK … LINK <dir>` needs the target to have a `.playbook`: a
  statement never prompts for one. `SANDBOX` does not apply to `LINK`,
  whose manifest belongs to the target.

Inside `ALTER ENV` the word `VAR` is optional (`SET FOO=1`): the object
already says it. Inside `ALTER PLAYBOOK` it is required (`SET VAR FOO=1`),
because a playbook has other things to set.

Order matters for env sets: a later set overrides an earlier one. `USE ENV a b`
states the whole list, so it is the idempotent form; `ADD ENV` places one set
without restating the rest.

One rule keeps the verbs apart: **`DROP` acts on objects** (an ENV, a
playbook) and **`UNSET` acts on variables**. So `DROP ENV
router` inside `ALTER PLAYBOOK` detaches that set, and `UNSET VAR FOO`
forgets the playbook's own `FOO`.

`IF NOT EXISTS` / `IF EXISTS` turn "already there" / "not there" into a no-op
instead of an error; they sit before the name, as in ClickHouse.
`CREATE OR REPLACE ENV` replaces the set's whole content.
`CREATE PLAYBOOK IF NOT EXISTS` never re-clones an existing playbook.

`DROP ENV` is refused while a playbook or `DEFAULTS` uses the set; the error
lists the users.

Clauses in one command apply **atomically**: all or none, validated before
anything is written. Validation includes the secret helper's check for
every `SET … FROM` (see Secrets). The exception is the clauses that run
Claude Code's own commands or change files under the playbook, the
marketplace, plugin, MCP server and skill clauses: they run in order, the first failure stops the statement and names
what already ran, and running it again finishes it (see "Plugins and the
agent", and "An agent's configuration" for replacing an MCP server).

### Where each clause writes

The grammar is a front end over the files cpb keeps. Nothing
stores commands; files store the result.

| Clause | Writes |
|---|---|
| `CREATE / ALTER / DROP ENV` | `<root>/.env-sets/<name>.toml`: `description`, `[set]`, `[refs]`, `block` |
| `ALTER PLAYBOOK … USE / ADD / DROP ENV` | the playbook's `.playbook`, `[env] sets = [...]` |
| `ALTER PLAYBOOK … SET VAR K=V` | `.playbook` `[env.set]` |
| `ALTER PLAYBOOK … SET VAR K FROM '<ref>'` | `.playbook` `[env.refs]` |
| `ALTER PLAYBOOK … BLOCK VAR K` | `.playbook` `[env] block = [...]` |
| `ALTER PLAYBOOK … UNSET VAR K` | removes K from whichever of the three holds it |
| `ALTER DEFAULTS … USE / ADD / DROP ENV` | `<root>/.env-sets/.defaults`, one set name per line, in order |
| `ALTER DEFAULTS SET / UNSET SECRET HELPER` | `<root>/.env-sets/.secret-helper`, one line: the command |
| `CREATE / DROP PLAYBOOK`, `RENAME TO`, `LAUNCHER`, `NO LAUNCHER` | the playbook dir, the registry and the launcher |
| `ALTER PLAYBOOK … ADD / DROP MARKETPLACE`, `ADD / DROP PLUGIN` | nothing directly: runs `claude plugin …` with the playbook as `CLAUDE_CONFIG_DIR` (see "Plugins and the agent") |
| `ALTER PLAYBOOK … SET / UNSET AGENT` | the playbook's `settings.json`, `agent` |
| `ALTER PLAYBOOK … ADD / DROP MCP SERVER` | nothing directly: runs `claude mcp add-json / remove --scope user` for the playbook; a reference also writes the playbook's `[env.refs]` (see "An agent's configuration") |
| `ALTER PLAYBOOK … ALLOW / DENY / UNSET TOOL` | the playbook's `settings.json`, `permissions.allow` / `permissions.deny` |
| `ALTER PLAYBOOK … SET / UNSET STATUSLINE`, `SET / UNSET MODEL` | the playbook's `settings.json`, `statusLine` / `model` |
| `ALTER PLAYBOOK … ADD / DROP SKILL` | `<playbook>/skills/<name>` (a link or a copy) and the manifest's `[skills.<name>]` record |
| `ALTER PLAYBOOK … SET / UNSET SANDBOX` | the playbook's `.playbook`, `[sandbox]` (bare `SET SANDBOX` also `isolated_login = true`) |

A key lives in exactly one of `set`, `refs`, `block` within a layer; writing
it to one removes it from the others.

### Examples

```
cpb CREATE ENV router SET ANTHROPIC_BASE_URL=http://localhost:8080/v1 ANTHROPIC_MODEL=glm-5.3
cpb ALTER ENV router SET ANTHROPIC_AUTH_TOKEN FROM 'keychain:router-token'
cpb ALTER ENV router UNSET ANTHROPIC_MODEL
cpb ALTER PLAYBOOK work USE ENV glm-5.3 deepseek-flash
cpb ALTER PLAYBOOK work ADD ENV claude-work FIRST
cpb ALTER PLAYBOOK work ADD ENV router AFTER glm-5.3
cpb ALTER PLAYBOOK work DROP ENV deepseek-flash
cpb ALTER PLAYBOOK work BLOCK VAR HTTP_PROXY
cpb ALTER DEFAULTS USE ENV claude-default corp-proxy
cpb ALTER DEFAULTS SET SECRET HELPER 'my-keychain-helper'
cpb CREATE PLAYBOOK scratch FROM https://github.com/example/work-playbook LAUNCHER sc
cpb ALTER PLAYBOOK scratch LAUNCHER scr
cpb ALTER PLAYBOOK scratch RENAME TO lab
cpb DROP PLAYBOOK lab
cpb SHOW ENVS
cpb EXPLAIN PLAYBOOK work
cpb SHOW CREATE ALL > playbook.cpb
cpb APPLY playbook.cpb --dry-run
cpb ALTER PLAYBOOK work ADD MCP SERVER sentry URL 'https://mcp.sentry.dev/mcp' HEADER 'Authorization' FROM 'keychain:sentry-auth'
cpb ALTER PLAYBOOK work ALLOW TOOL 'Bash(git diff *)' SET MODEL 'claude-opus-5-5'
cpb ALTER PLAYBOOK work SET STATUSLINE 'bash ~/bin/statusline.sh'
cpb ALTER PLAYBOOK work ADD SKILL release-notes FROM 'github:acme/skills' SUBDIR release-notes
cpb APPLY agent.cpb TO lab
cpb APPLY agent.cpb TO '~/.claude' --dry-run
cpb "SELECT name, version FROM PLAYBOOKS"
cpb "SELECT playbook, key FROM VARS WHERE effective ORDER BY playbook"
```

Every clause has a runnable example under [`examples/`](examples/),
applied in CI.

## CREATE and DROP PLAYBOOK

### `CREATE PLAYBOOK <name>`

Creates `<root>/<name>/` with a `CLAUDE.md` that says what a playbook is and imports nothing (replace it with the playbook's own instructions), links the machine's login (see *Authentication preparation*), and registers a launcher named `<name>`, or the one `LAUNCHER <launcher>` names, recorded as the manifest's `launcher`. `NO LAUNCHER` registers none and prints `cpb run <name>` instead. `ISOLATED LOGIN` writes `isolated_login = true` before the login is linked, so the playbook shares none; `SANDBOX` writes `[sandbox] always = true` with it. A name that is taken refuses (`playbook "<name>" already exists (write CREATE PLAYBOOK IF NOT EXISTS to keep it)`); `IF NOT EXISTS` leaves an existing playbook unchanged. The launcher names are checked before the directory exists, under the registry lock (see *Launchers*).

### `CREATE PLAYBOOK <name> LINK <dir>`

Registers a directory in place: `<root>/<name>` becomes a symlink to `<dir>`, and nothing is copied. The directory must have a `.playbook`; a statement never prompts for one. The launcher is `LAUNCHER <launcher>`, else the target manifest's `launcher`, else `<name>`. A launcher that differs from the target manifest's is refused: that manifest is shared with every registry that links the directory, so it is the target's state, and statements that write it are refused too (see *Environment overrides*). A login the directory carries is set aside before the credential sync, unless the directory is isolated (`isolated_login = true` keeps its own). Nothing in the directory is deleted, and `DROP PLAYBOOK` removes only the link. `SANDBOX` and `ISOLATED LOGIN` do not take `LINK`.

### `CREATE PLAYBOOK <name> FROM <source>`

Installs a single playbook from a Git repository or a local directory, as `<name>`. The result is always **one flat playbook** under the playbooks root.

It always **copies** the source into the playbooks root — Git URLs via clone, local directories via recursive copy. The installed playbook is a self-contained, independent copy; later edits to the original source do not affect it. To keep an *external* directory in place and expose it under the playbooks root as a symlink instead of a copy, use `CREATE PLAYBOOK <name> LINK <dir>`.

```bash
cpb CREATE PLAYBOOK pai FROM https://github.com/user/pai                     # a Git repository
cpb CREATE PLAYBOOK repo FROM https://github.com/user/repo BRANCH dev        # a branch or tag
cpb CREATE PLAYBOOK mine FROM ~/dev/my-playbook                              # a local directory, copied
cpb CREATE PLAYBOOK sre FROM https://github.com/user/repo SUBDIR playbooks/sre LAUNCHER sre
                                                                             # one playbook out of a monorepo
cpb CREATE PLAYBOOK sre FROM https://github.com/user/repo/tree/main/playbooks/sre LAUNCHER sre
                                                                             # the same, as a GitHub tree URL
```

**Source types:**

| Source | Behaviour |
|--------|-----------|
| URL (`http://`, `https://`, `git@`, `git://`, `ssh://`, `file://`) | Shallow-cloned (`git clone --depth=1`) into the install directory |
| GitHub `/tree/<ref>/<path>` URL | Recognized and split automatically into clone URL + `BRANCH <ref>` + `SUBDIR <path>`; remote refs are consulted so branch names containing `/` work |
| Anything else | Treated as a local filesystem path and **copied** into the install directory |

**Clauses:**

| Clause | Description |
|------|-------------|
| `SUBDIR <path>` | Install only this subdirectory of the source (see below) |
| `BRANCH <ref>` | Git URL only: clone this branch/tag/ref instead of the default branch |
| `LAUNCHER <launcher>` | Custom launcher command name for the installed playbook |
| `NO LAUNCHER` | Skip launcher creation |
| `SANDBOX` | Set `[sandbox] always = true` and `isolated_login = true` on the installed manifest: every launch is sandboxed and the playbook authenticates on its own. A `[sandbox]` block shipped by the source is never adopted, with or without this clause (`Note: ignoring the [sandbox] block shipped in the source's .playbook; sandbox settings are install-local ...`). |
| `ISOLATED LOGIN` | Set `isolated_login = true` without a sandbox |

**Steps (no `SUBDIR`):**
1. Stage the source (Git URL → `git clone --depth=1`, with `BRANCH <ref>` if given, into a temp dir; local path → read in place) so its `.playbook` can be consulted.
2. The install directory is `<name>`; the source manifest's `name` never chooses it.
3. Check the target doesn't already exist under the playbooks root.
4. Preflight command names against the registry under the registry lock — the name and the effective launcher name (`LAUNCHER`, or the staged manifest's `launcher`) — erroring **before anything is copied** if a name already addresses another playbook.
5. Copy the staged tree into the target. The installed directory **is** the playbook. If a `.playbook` is present it supplies metadata; if not, the directory is still a valid playbook.
6. Register a launcher command per the rules below.
7. Print a summary.

The source never gets a `.playbook` written into it, and does not need one.

**Steps (with `SUBDIR <path>`):**
1. Fetch the source as above into a scratch location (for URLs, a temp directory; for local paths, the source itself).
2. Verify `<source>/<path>` exists and is a directory.
3. Copy `<source>/<path>` into `~/.claude-playbooks/<name>/`. For URL sources, the rest of the clone is discarded.
4. Treat the result as one flat playbook. Any `.playbook` already inside `<source>/<path>` provides metadata for that one playbook.

`SUBDIR` is how you consume a monorepo — a repo laid out as `playbooks/sre`, `playbooks/dba`, `playbooks/frontend`, etc. Each `CREATE PLAYBOOK … FROM … SUBDIR` (or `/tree/<ref>/<path>` URL) copies everything under that one directory into its own playbook. To take several, write several statements (a playbook file holds them).

**Default command name**

One launcher is registered, named by `LAUNCHER`, or the source manifest's `launcher` field, or `<name>`, in that order. `NO LAUNCHER` skips it. When `LAUNCHER` differs from what the installed manifest records, the name is written into the installed playbook's `.playbook` — a custom command name is only resolvable at invocation time through the manifest `launcher` field (on manifest-write failure the install is rolled back).

**Command-name collision handling**: collisions against the registry are a hard **pre-copy error**, not a skip-with-warning — `launcher name "sre" already addresses playbook "other". Pick another name`, and nothing is copied. Only a *foreign file* (not a launcher) already occupying the name in the launcher directory degrades to a post-install warning: the playbook is installed and runnable via `cpb run <name>`, and the warning suggests renaming or removing the conflicting file.

**CLAUDE.md warning:** if the installed playbook has no `CLAUDE.md`, a warning is printed. Claude Code works without one, but most playbooks benefit from having one.

**Errors:**
- `BRANCH` with a local path → `BRANCH only applies to Git URLs`
- `SUBDIR` path missing in source → `source.subdir "<path>" not found below <source root>: <stat error>` (it is resolved as a source-relative path, so it reports under that field name)
- Source not found → `'~/dev/foo' not found`
- Source is a file → `'~/dev/foo' is not a directory`
- Name already taken → `"myrepo" already exists at ~/.claude-playbooks/myrepo; choose another name`
- Command name taken → `launcher name "sre" already addresses playbook "other". Pick another name`
- `git` not on PATH → `'git' command not found`
- Clone fails → git's error output is shown directly

**Sample output:**
```
Cloning https://github.com/user/repo (branch main) (subdir playbooks/sre)...
Installed "sre" at ~/.claude-playbooks/sre
Launcher: sre  (at /Users/you/.local/bin/sre)

Run it now:
  sre
```

No shell reload is needed — the launcher is a symlink in a PATH directory, live the moment it is written. If the launcher directory is not on PATH, or another executable shadows the command, a warning explains the fix.

**A source never carries a login.**
- `CREATE PLAYBOOK … FROM` leaves a source's
  `.credentials.json` out of the install, whether it is a file or a link.
  It also leaves out the account state in its `.claude.json`: `oauthAccount`,
  `userID`, the onboarding and install markers, and the cached feature flags.
  Each prints one stderr line naming the source and the keys, never a value.
- `LINK` deletes nothing in your directory. It renames a
  `.credentials.json` there to `.credentials.json.cpb-ignored-<stamp>`, and
  backs up `.claude.json` to `.claude.json.cpb-backup-<stamp>` before
  removing the same keys. A directory with `isolated_login = true` keeps both.
- `update` keeps the install's own files (see *`cpb update <name>`*).

### `DROP PLAYBOOK [IF EXISTS] <name> [--yes]`

Deletes the playbook's directory (for a `LINK`, only the link), then retires its launchers by the rule in *Launchers*, printing `Removed launcher "<n>"` or `Kept launcher "<n>" (still addresses playbook "<other>")` for each. Without `--yes` it first shows the playbook, its location, its launchers and how many files go, and asks on stdin; any answer but `y` or `yes`, end of input included, prints `Cancelled.` and deletes nothing. The confirmation runs before the registry lock is taken; the playbook is found again under the lock, and nothing is removed if it moved meanwhile (`"<name>" disappeared while waiting for confirmation (deleted or renamed concurrently); nothing removed`). An unknown name refuses (`unknown playbook "<name>". `cpb SHOW PLAYBOOKS` lists them`); `IF EXISTS` makes it a no-op. In a file, `APPLY` never takes a drop on the file's word (see *playbook.cpb: SHOW CREATE and APPLY*). Under a custom playbooks root, launchers are not touched.

## CREATE / ALTER / DROP ENV and ALTER DEFAULTS

### Layers at launch

**Layering.** Later layers win:

```text
process environment
  + each env set in DEFAULTS, in list order (`ALTER DEFAULTS USE ENV …`)
  + each env set in env.sets, in list order
  + the table's own env.set
  - the table's own env.block
  + one-off launch flags, in command-line order
  + CLAUDE_CONFIG_DIR (bound by the tool; reserved -- to the playbook's
    install directory, or to $CPB_CONFIG_DIR when the caller set it)
  - CPB_CONFIG_DIR (consumed by the launch; never reaches the child.
    Bound here, after every layer above, so no layer can reintroduce it)
  = the child claude process's environment
```

DEFAULTS apply to every launch of every playbook, manifest or not, and to `start`. It is recorded in `<playbooks root>/.env-sets/.defaults` (mode `0600`, one env set name per line). A default that names a missing or invalid env set refuses the launch like any other env set. Only an absent marker means "no default": an empty marker, one holding an invalid name, or a dangling symlink refuses the launch too, rather than silently dropping the layer (and the token decision it may carry); `ALTER DEFAULTS USE ENV …` replaces such a marker.

**Semantics at launch.** The table is first flattened: each env set in `env.sets`, in order, then the table's own `set`/`block` on top, where a later `set` cancels an earlier `block` of the same key and vice versa. `PrepareLaunchEnv` then builds the child's environment as: the process environment; the authentication branch (isolation, long-lived token, or stored credentials); flattened `set` entries overriding any inherited value; flattened `block` entries removed; finally `CLAUDE_CONFIG_DIR` bound to the launch's config directory (the playbook, or the caller's override, below). An env set named by the manifest that does not exist under `<playbooks root>/.env-sets/` refuses the launch: `env set "x" not found in <dir> (create it with: cpb CREATE ENV x SET KEY=VALUE)`; one that exists but cannot be read or parsed refuses it too: `env set "x": invalid env set at <path>: <reason>`. Neither is downgraded to the advisory warning other preparation failures get, and the refusal happens before any credential sync or quarantine touches the config directory. `start` resolves env sets from the root named by its `--playbooks-dir` (or `CPB_PLAYBOOKS_DIR`), the same root `run` uses. Whether the long-lived token is active is decided **with the block applied**: `unset` of `CLAUDE_CODE_OAUTH_TOKEN` means inactive (the stored-credentials path runs, the playbook's own grant is not quarantined, an inherited token is stripped); `set` of it supplies a per-playbook token that replaces the machine-global file's. The manifest governing a config directory is the nearest valid one walking up from it, so a manifest `subdir` layout is covered by the install root's block, unless the subdirectory (or a directory between it and the root) carries a manifest of its own, which then governs; `EXPLAIN PLAYBOOK <playbook>` shows what the governing block puts in effect, while `ALTER PLAYBOOK <playbook> SET VAR` edits the root manifest.

`EXPLAIN PLAYBOOK <name>` prints every variable that a launch would change,
its effective value, and the layer that decided it:

```
ANTHROPIC_BASE_URL    http://localhost:8080/v1              <- ENV router
ANTHROPIC_AUTH_TOKEN  <from keychain:router-token>          <- ENV router
ANTHROPIC_MODEL       glm-5.3                          <- PLAYBOOK work
HTTP_PROXY            (blocked)                        <- PLAYBOOK work
OPENAI_API_KEY        sk-a...9f2c (51 chars, plaintext) <- PLAYBOOK work
FOO                   bar                              <- DEFAULTS (ENV claude-default)

Secret helper: my-keychain-helper (from CPB_SECRET_HELPER)
```

The last line names the helper that resolves this playbook's references and
where it came from (`setting` or `CPB_SECRET_HELPER`), or reads
`Secret helper: (none)`.

### Secrets (optional)

Secret references are **optional**: cpb works fully without them, and
nothing else in the grammar depends on them. They follow git's
`credential.helper` pattern: cpb defines a small interface, and you
configure a program that implements it (for example a keychain helper).
cpb never names or discovers one.

**Configuring the helper**:

- `ALTER DEFAULTS SET SECRET HELPER '<command>'` stores it and
  `ALTER DEFAULTS UNSET SECRET HELPER` removes it. It appears in
  `SHOW DEFAULTS` and in `SHOW CREATE ALL`.
- `CPB_SECRET_HELPER=<command>` in the environment overrides the stored
  setting for that process (devbox projects, tests). `EXPLAIN PLAYBOOK`
  says which of the two is in effect.
- The helper is **one command**: a name on `PATH` or an absolute path, with
  no arguments. cpb execs it directly with an argument vector, never
  through `sh -c`. A value with whitespace is refused.

`SET <key> FROM '<ref>'` stores the **reference** only, as an opaque string.
cpb checks one thing about its shape, a scheme followed by a colon
(`keychain:…`, `op://…`, `vault:…`), so a value pasted where a reference
belongs is refused, and never echoed. Everything else about a reference is
the helper's business.

**The helper interface** (cpb's own contract):

- **Check**, at write time: `<helper> --check KEY=REF`. Exit 0 means the
  reference resolves. Anything else fails the statement, showing the
  helper's own message, which must never contain the value.
- **Exec**, at launch: `<helper> K1=REF1 [K2=REF2 …] -- claude <args>`. The
  helper resolves the references, sets them in the environment of that one
  command, and execs it. cpb fetches no values; there is no value-returning
  call, and none may be built. A non-zero exit before `claude` starts means
  cpb did not launch, and the helper's message says why.

**Without a configured helper**, cpb stays fully usable. `SET … FROM` is
refused, and so is launching a playbook whose layers hold a reference, each
with one line: "no secret helper configured (ALTER DEFAULTS SET SECRET HELPER
…)". Literal values work without one.

`FROM` also names a playbook's source in `CREATE PLAYBOOK`; the position
disambiguates.

- The value never appears in a file, in argv, or in any `SHOW`/`EXPLAIN`
  output.
- A sandboxed launch of a playbook with references is refused, with a message saying so.
- **Keys cpb reads itself never take a reference**: `CLAUDE_CODE_OAUTH_TOKEN`, whose value cpb's authentication handling
  reads to decide injection and credential quarantine. A reference hides the
  value by design, and "set, value unknown" would break that logic silently.
  `SET CLAUDE_CODE_OAUTH_TOKEN FROM …` is refused in a playbook's block and
  in every env set (so under DEFAULTS too), and a hand-written reference for
  it makes the file invalid. The list lives in one place
  (`manifest.RefRefusedKeys`); a future key cpb reads joins it.

**The grammar refuses a credential-looking literal**: a
`SET [VAR] K=V` whose key looks like a credential (the rule SHOW uses to
redact: `TOKEN`, `SECRET`, `PASSWORD`, `AUTH`, `*_KEY`, …) is
refused, naming the key and never the value, and pointing at
`SET K FROM '<ref>'`. A value that cannot be a secret is let through: empty,
an integer, or `true`/`false`, so `SET VAR MAX_THINKING_TOKENS=8000` works.
**`AS PLAINTEXT` stores one knowingly**, for a pilot
without a secret helper: `SET VAR ANTHROPIC_AUTH_TOKEN=… AS PLAINTEXT`. It
applies to every literal in its `SET` clause and never to a reference. It
keeps cpb usable standalone, and it is loud where it matters: `EXPLAIN` marks
such an entry `(plaintext)`, and `SHOW CREATE` never carries the value (see
playbook files). The refusal is the grammar's: a manifest or env set file that already holds such a literal is read and launched as it is, and `SHOW` redacts the value.

All output redacts credential-looking literals; no form prints the value.

## ALTER PLAYBOOK

### Environment overrides (`[env]`)

A playbook's **environment overrides** are the `[env]` block of its `.playbook` manifest, applied to the child `claude` process by `run`, `start`, and launcher dispatch. Statements write them (`ALTER PLAYBOOK <name> SET VAR`, `BLOCK VAR`, `UNSET VAR`, `USE ENV`, `ADD ENV`, `DROP ENV`; see *Statement grammar*) and read them (`SHOW PLAYBOOK`, `EXPLAIN PLAYBOOK`, which shows the result at launch).

**Writes** parse and validate the whole statement before taking the registry lock and rewriting the manifest (bootstrapping one for a flat playbook). A key lives in exactly one of `set`, `refs` and `block`, so writing it into one removes it from the others (see *Where each clause writes*); `UNSET VAR` forgets it from whichever holds it. `ADD ENV` places a set after checking it exists (`LAST` by default, or `FIRST`, `BEFORE <env>`, `AFTER <env>`, moving an attached one), `USE ENV` replaces the list, and `DROP ENV` detaches. An emptied block is dropped from the file. A launch refuses a config directory whose manifest, or one on the way up, cannot be read: it may ask for an isolated login or a sandbox (`<error>. cpb does not launch over a manifest it cannot read: it may ask for an isolated login or a sandbox`). Registry discovery refuses one already, for every command.

**Install-local.** `update` carries the live block forward and ignores the source's, assembling the final manifest in the staged tree *before* the overlay so a source-shipped block is never live, even transiently; `CREATE PLAYBOOK … FROM` drops a source-shipped block with a note, assembling the install in a dot-prefixed staging directory (invisible to discovery) and renaming it into the registry only once its manifest is sanitized. A local source directory is always staged into a private copy first (in the system temp dir, or the user cache dir when that lies inside the source), so neither command ever writes into the pilot's source. A published manifest must not be able to redirect an install's API endpoint or strip its authentication.

Linked playbooks: the manifest is the LINK TARGET's shared state, so mutations are refused — edit the target's manifest directly if you really mean it.

### Isolated login

A playbook normally shares the machine's login: its `.credentials.json` is a
link to `~/.claude/.credentials.json`, and `/login` in any playbook logs in
all of them. An **isolated login** shares nothing. There is no link, no
machine-wide token, and no account record carried over from a shared past,
so the playbook is logged in only if it runs `/login` itself. It is the
manifest's `isolated_login = true` (see the authentication guide). 

```
CREATE PLAYBOOK <name> … ISOLATED LOGIN
ALTER PLAYBOOK <name> SET ISOLATED LOGIN
ALTER PLAYBOOK <name> UNSET ISOLATED LOGIN
```

- **Use it** for a second account, or for a throwaway or a third-party route
  where a `/login` must not land in the machine's shared store. In a shared
  playbook, `/login` writes through the link.
- **`SET ISOLATED LOGIN`** records `isolated_login = true` and removes the link
  to the shared store at once, so `cpb auth status` reports
  `isolated-login` straight away. A `.credentials.json` that is a file, the playbook's own
  login, is kept.
- **`UNSET ISOLATED LOGIN`** removes it. The next launch links the shared
  store again. It is refused in two cases, each with its reason:
  - The playbook always runs in a sandbox. `SANDBOX` implies an isolated
    login.
  - The playbook holds a login of its own: a `.credentials.json` file
    carrying an account grant. A shared launch would take it out of use.
    A shared launch sets another account's login aside. The same account's
    login is copied over the machine's, since it is newer. Run `/logout` in
    it first.
- **Not "own login".** A playbook that blocks the machine's token and uses
  the shared stored login is `shared-login` with `token_blocked: true` in
  `cpb auth status --json` (`shared-login (token blocked)` in the table). An
  isolated playbook is `isolated-login`.
- **Refusals.** It does not apply to `LINK`, where the manifest is the
  target's, or to a plain config directory, which has no manifest.
- **Reads.** `SHOW` prints `Login: isolated (shares nothing with ~/.claude)`,
  and `EXPLAIN` prints a `Login:` line. `SHOW PLAYBOOK --json` has
  `isolated_login` (a bool, true for a sandboxed playbook too), and `SELECT`'s
  `PLAYBOOKS` has an `isolated_login` column. `SHOW CREATE` writes `SET
  ISOLATED LOGIN` for an isolated playbook that is not sandboxed, and applying
  it again changes nothing.
- `ISOLATED` and `LOGIN` are read only in these positions, so they are not
  reserved words.

### Sandbox

A playbook's `[sandbox]` table says how its launches are sandboxed.
`ALTER PLAYBOOK` writes it:

```
ALTER PLAYBOOK sre SET SANDBOX               -- every launch sandboxed (always = true); the login isolated too
ALTER PLAYBOOK sre UNSET SANDBOX             -- always = false; the login stays isolated
ALTER PLAYBOOK sre SET SANDBOX backend=sbx host=me@buildbox mounts=~/libs:ro,~/data
ALTER PLAYBOOK sre UNSET SANDBOX host mounts
```

- **One meaning per form.** Bare `SET SANDBOX` is `always = true`, and it
  isolates the login, as `CREATE PLAYBOOK … SANDBOX` does: a sandbox shares
  nothing with `~/.claude`. `SET SANDBOX <key>=<value> …` sets only the keys
  it names and never changes `always`; `SET SANDBOX always=true` is the bare
  form spelled out. Bare `UNSET SANDBOX` is `always = false` and leaves the
  login isolated (`UNSET ISOLATED LOGIN` shares it again). `UNSET SANDBOX
  <key> …` forgets settings.
- **The keys** are the table's own: `always`; `backend` (`sbx`);
  `host` (`user@host`: the launch runs there, over ssh); `workdir`; `mounts`
  (comma-separated, `:ro` for read-only); `allow_net` (comma-separated
  hosts); `secrets` (`proxy`, `env`); `claude_version`; `share_skills`
  (`true` or `false`). A value the table refuses is refused before anything
  is written. They are not reserved words.
- **Refusals.** Bare `SET SANDBOX` does not combine with `UNSET SANDBOX` or
  with `UNSET ISOLATED LOGIN`, and a statement sets or unsets a key once. A
  linked playbook's table is the target's, and a plain config directory has
  no manifest.
- **Reads.** `SHOW PLAYBOOK` prints `Sandbox: yes` or `no`, then the set keys
  in parentheses. `--json` has `"sandbox"`, the table key for key (`always`,
  then each setting, null or empty when unset), and `SELECT`'s `PLAYBOOKS`
  has it as a JSON column. `EXPLAIN` says when every launch is sandboxed.
  `SHOW CREATE` writes a bare `SET SANDBOX` for `always` and one `SET SANDBOX
  <key>=<value> …` for the rest, and applying it again changes nothing.
- A source's `[sandbox]` block is never adopted (`CREATE PLAYBOOK … FROM`
  drops it with a note). A launch's own flags (`--sandbox[=BACKEND]`,
  `--sandbox-host`, `--mount`, `--workdir`) apply on top, for that launch.

### Plugins and the agent

The goal they serve: a playbook built by stacking playbook files,
for example a reviewer agent from a plugin, on a bare playbook:

```
-- base.cpb
CREATE PLAYBOOK IF NOT EXISTS reviewer NO LAUNCHER;
ALTER PLAYBOOK reviewer USE ENV router;

-- agent.cpb
INCLUDE 'base.cpb';
ALTER PLAYBOOK reviewer
  ADD MARKETPLACE team FROM 'github:example/team-plugins'
  ADD PLUGIN reviewer@team
  SET AGENT 'reviewer';

-- team.cpb
INCLUDE 'agent.cpb';
ALTER PLAYBOOK reviewer
  ADD MARKETPLACE team-rules FROM 'github:example/team-rules'
  ADD PLUGIN team@team-rules;
```

**How the clauses act: through Claude Code's own CLI.** A playbook is a Claude Code config directory, so with
`CLAUDE_CONFIG_DIR` set to it, Claude Code's *user* scope is that playbook.
The marketplace and plugin clauses run `claude plugin …` there, with
`--scope user`: the format of `settings.json` and of the plugin cache stays
Claude Code's to own, and cpb writes neither. `SET AGENT` has no command in
that CLI, so it is the one key cpb writes itself.

| Clause | What runs |
|---|---|
| `ADD MARKETPLACE m FROM '<source>'` | `claude plugin marketplace add <source> --scope user` |
| `DROP MARKETPLACE m` | `claude plugin marketplace remove m --scope user` |
| `ADD PLUGIN p@m` | `claude plugin install p@m --scope user --json` (also re-enables a disabled one) |
| `DROP PLUGIN p@m` | `claude plugin uninstall p@m --scope user --keep-data --json` |
| `SET AGENT '<agent>'` | `settings.json`: `agent = "<agent>"`, as typed; the main session runs as that agent |
| `UNSET AGENT` | `settings.json`: removes `agent` |

Every command runs with `CLAUDE_CONFIG_DIR` set to the playbook, from a
neutral working directory (so no project's settings join in), and never on
a terminal, so Claude Code never prompts.

**Sources**, as `marketplace add` takes them:

| `FROM` | passed as | recorded by Claude Code as |
|---|---|---|
| `'github:<owner>/<repo>'` | `<owner>/<repo>` | `{"source": "github", "repo": "<owner>/<repo>"}` |
| `'github:<owner>/<repo>#<ref>'` or `@<ref>`, a branch or tag | `<owner>/<repo>#<ref>` | `{"source": "github", "repo": "<owner>/<repo>", "ref": "<ref>"}`: repo and ref apart |
| `'https://…'`, `'git@…'` (a git URL) | the URL | `{"source": "git", "url": "…"}` |
| a git URL with `#<ref>` (`'https://…/repo.git#v1.2.0'`) | the URL with its ref | `{"source": "git", "url": "…", "ref": "<ref>"}`: url and ref apart |
| `'/abs/path'` or `'~/path'`, a local directory | the absolute path (`~/` expanded) | `{"source": "directory", "path": "/abs/path"}` |
| `'./path'` or `'../path'`, in a playbook file only | resolved against the file's directory | as above, absolute |

A directory source is the marketplace root, the directory that holds
`.claude-plugin/marketplace.json`. In a playbook file, a path starting with
`./` or `../` resolves against the directory of that file, exactly as
`INCLUDE` does, so a layer can ship its plugin beside
it; on the command line, and in a file read from a pipe, it is refused. Any
other relative path is refused, and so is a URL carrying credentials. This is how a plugin is used from a local checkout
before it is published. `--sparse` is not supported. A git URL's
`#<ref>` is compared as Claude Code records it, url and ref apart:
applying the same `url#ref` again changes nothing, another ref or none is
another source, and `SHOW CREATE` writes it back as `url#ref`. A `github:` source's ref works the same way: `#` and `@` spell one source,
`SHOW CREATE` writes it back with `#`, and writes a `github:<owner>/<repo>` without a ref back as it is. **A commit cannot be pinned:** Claude Code clones a
marketplace by branch or tag only, so a `github:` ref of 7 to 40 hex
characters is refused ("Claude Code clones marketplaces by branch or tag; a
commit cannot be pinned"), rather than written and broken at session start.
Tag the commit instead. A git URL's `#<ref>` that looks like a commit is accepted and warned about
(`marketplace_ref_not_cloneable`): "Claude Code clones marketplaces by branch
or tag; this ref looks like a commit and will not clone: use a tag at that
commit". The warning names the marketplace and the ref, never the URL.
A marketplace name follows the env-set name rule; a plugin id is
`<plugin>@<marketplace>`.

**A marketplace's name is its source's.** `marketplace add` takes no name:
the source's `marketplace.json` declares it. The statement names one anyway,
so a playbook file reads the same as the state it makes, and cpb checks that
they agree: before anything runs for a directory source (it reads the file),
after the command for a git or GitHub one. A source that declares another
name is removed again, and the statement fails naming both.

**Claude Code 2.1.268 or newer** runs the plugin clauses: they use
`claude plugin install --json` and `uninstall --json`, which Claude Code added in 2.1.268. A
statement whose plan installs or uninstalls a plugin reads `claude
--version` first and, on an older claude, is refused in one line before
any command that changes anything runs (the state reads, `plugin list
--json` and `marketplace list --json`, come first; they are older than
2.1.268): `the plugin clauses need Claude Code 2.1.268 or newer (they
run claude plugin install --json); this claude is 2.1.245: update Claude
Code`. A dry run says the same. A version that cannot be read is let
through, and the command then reports what is wrong.

**State first, so repeats run nothing.** Before a statement runs anything,
cpb reads the playbook's state (`claude plugin marketplace list --json`,
`claude plugin list --json`) and plans the commands: a marketplace already
declared from the same source, a plugin already installed and enabled, runs
nothing, and a statement whose clauses all hold reports `unchanged`. A
marketplace declared under the same name from another source is refused:
`DROP MARKETPLACE` it first. `APPLY --dry-run` reads the state and reports
the commands it would run, and runs none.

**Rules**

- `ADD PLUGIN p@m` is refused unless `m` is declared in this playbook, by
  its state or by an earlier clause. There is no exception for a
  marketplace Claude Code knows by default: a plugin id
  always names its marketplace, so a playbook that uses the official one
  declares it
  (`ADD MARKETPLACE claude-plugins-official FROM 'github:anthropics/claude-plugins-official'`).
- `DROP MARKETPLACE m` is refused while installed plugins come from `m`; the
  error lists them. (`marketplace remove` would uninstall them silently.)
- `DROP PLUGIN` keeps the plugin's saved data (`--keep-data`): dropping
  detaches it, as `DROP ENV` detaches a set. Purging the data is not supported.
- **A marketplace-declared command is never accepted for you.** A
  plugin installed by running a command its marketplace declares (or whose
  archive is fetched through one) needs a confirmation. cpb never passes `-y`
  or `--accept-command`: the statement fails, shows the command and its
  `sha256`, and gives the line you run by hand after reviewing it
  (`CLAUDE_CONFIG_DIR=<playbook> claude plugin install p@m --accept-command <sha256>`).
  It is a supply-chain guard.
- **Network.** `ADD MARKETPLACE` from git or GitHub, and `ADD PLUGIN`, fetch
  from the network; so does an `APPLY` of a file that holds them. A
  directory source is read in place.
- A linked playbook's `settings.json` belongs to the target, so these
  clauses are refused on it, as the environment clauses are.
- A statement's commands run in the order its clauses are written, after its
  manifest write and before its agent write. The first failure stops the
  statement and names the commands that already ran; there is no rollback.
  Every clause is safe to repeat, so running the statement (or the file)
  again finishes it, as with `APPLY`.
- `claude` must be on `PATH` for these clauses; the reads (`SHOW`,
  `EXPLAIN`, `SHOW CREATE`) do not run it.
- Plugin entries set to `false` (a plugin disabled by hand) are shown, not
  changed: the grammar adds and drops, it does not disable. `SHOW CREATE`
  does not reproduce such an entry: it writes a comment line
  (`-- PLUGIN p@m is false in settings.json; not written`).
- A playbook file never runs a shell command: there is no `RUN` statement,
  and none is planned. It would end dry runs,
  validation before writing, `SHOW CREATE` and every safety rule above.

**The agent.** A plugin can
name an agent in its own `settings.json`, and two plugins that both do are
resolved by load order, the last one winning. The `agent` of the user
scope overrides every plugin, and a playbook's `settings.json` *is* its user
scope, so `SET AGENT` is the deterministic pin. It accepts an agent's bare
name (`reviewer`) or its namespaced id (`reviewer:reviewer`); both
resolve, and cpb stores what was typed. A layer above does not need `SET
AGENT`: its plugin's SessionStart context stacks on top of the agent's.
The agent's prompt replaces Claude Code's default system prompt; that is
the plugin's concern, not cpb's.

**Visible where state is visible.** `SHOW PLAYBOOK --json` has
`"marketplaces": [{"name", "source"}]`, `"plugins": [{"id", "enabled"}]` and
`"agent"` (null when unset); the human form has `Marketplaces:`, `Plugins:`
and `Agent:` lines when the playbook has any. These reads take the
playbook's `settings.json` as Claude Code wrote it and run nothing.
`EXPLAIN PLAYBOOK` names the enabled plugins a launch starts with and the
agent the playbook pins, `Agent: reviewer (playbook settings)`; with no pin
and plugins enabled, it says that a plugin may name one (cpb does not read
the plugins' own files). `SHOW CREATE` writes the clauses, so a
playbook's plugins and agent travel in its playbook file. The fields are in
`SHOW PLAYBOOK --json`, so the SQL recipe sees them.

**INCLUDE** is specified in its own section; with it, the stacked files above run with one `cpb APPLY team.cpb`.

These clauses exist on `ALTER PLAYBOOK` only; there is no `ALTER DEFAULTS` form.

### An agent's configuration

A playbook file describes a Claude Code
agent completely, from its route to its tools. Four clause groups, on `ALTER PLAYBOOK` only:

```
ALTER PLAYBOOK reviewer-agent
  ADD MCP SERVER sentry URL 'https://mcp.sentry.dev/mcp' HEADER 'Authorization' FROM 'keychain:sentry-auth'
  ADD MCP SERVER files COMMAND 'npx' ARGS '-y' '@modelcontextprotocol/server-filesystem' '/srv/data'
  ALLOW TOOL 'Bash(git diff *)'
  DENY TOOL 'Bash(rm -rf *)'
  SET STATUSLINE '~/.claude-playbooks/reviewer-agent/bin/statusline.sh'
  SET MODEL 'claude-opus-5-5'
  ADD SKILL release-notes FROM '~/src/skills/release-notes';
```

**One rule for all of them.** The grammar has named clauses and never raw
settings JSON. Where Claude Code has a CLI for a setting, cpb runs it with the
playbook as `CLAUDE_CONFIG_DIR` (its user scope), as the plugin clauses do;
where it has none, cpb writes that one key of the playbook's `settings.json`,
keeping every other key. A linked playbook's configuration belongs to its
target, so these clauses are refused on it. Like the plugin clauses, a clause
that already holds runs and writes nothing, and `APPLY --dry-run` reports what
would change and runs nothing.

### MCP servers

`ADD MCP SERVER <name>` declares one server, stdio (`COMMAND … [ARGS …]`) or
remote (`URL …`, HTTP unless `TRANSPORT SSE`), with its environment (`VAR`)
and, for a remote server, its request headers (`HEADER`). `DROP MCP SERVER
<name>` removes it.

- **Delegated.** cpb runs `claude mcp add-json <name> '<config>' --scope user`
  and `claude mcp remove <name> --scope user` (verified on Claude Code
  2.1.283). The JSON is built from the clauses; it is never part of the
  grammar. `add` refuses an existing name, so a server whose declaration
  changed is removed and added again. That replacement is not atomic: if the
  add fails after the remove, the old server is gone until the statement is
  run again, which finishes it; the error says so. It is the same ordered,
  stop-at-first-failure rule the plugin clauses follow.
- **State.** User-scope servers live under `mcpServers` in the playbook's
  `.claude.json`. cpb reads them from there to decide what already holds;
  `claude mcp list` is not used, because it connects to every server to
  check its health.
- **Secrets never enter Claude's config.** `VAR K FROM '<ref>'` and
  `HEADER '<name>' FROM '<ref>'` store the reference in the playbook's own
  layer (`[env.refs]`) under a derived variable and write only `${<variable>}`
  into the server's config. The variable is
  `CPB_MCP_<SERVER>_<E|H>_<KEY>_<h8>`: the server name and the key upper-cased
  with other characters as `_` (for reading), `E` for an env entry or `H` for
  a header, and `<h8>` the first 8 hex digits of the SHA-256 of the exact
  server name, kind and key (for uniqueness). An env entry and a header of the
  same name, or two names that normalize alike, never share a variable, and
  cpb records which variables each server derived, so dropping one server
  forgets exactly its own. At launch the
  secret helper resolves the variable into claude's environment, and Claude
  Code expands `${…}` in an MCP server's `env` and `headers` when it starts
  the server (verified for both, in user scope). The value is therefore in
  the session's environment, as every `SET … FROM` value is, and in no file.
  A header's reference resolves to the whole value: store
  `Bearer <token>`, not the bare token.
- **Credentials take a reference, always.** A credential-looking `VAR` key or
  `HEADER` name (the variable rule, plus `Authorization`, `Proxy-Authorization`
  and `Cookie`) must use `FROM '<ref>'`; a literal is refused, and `AS
  PLAINTEXT` is not accepted on an MCP server, because the literal would be
  written into Claude's config. Other literals are written as given.
- `DROP MCP SERVER` also forgets the references it derived.
- **Visible.** `SHOW PLAYBOOK --json` has `"mcp_servers"`: name, transport,
  command and args or URL, and env and headers as variable objects (a
  reference shown as the reference, a credential-looking literal redacted).
  `SHOW CREATE` writes the clauses, turning a derived placeholder back into
  its `FROM '<ref>'`. `EXPLAIN PLAYBOOK` lists the servers and the derived
  variables the launch supplies.
- Not supported: OAuth (`--client-id`, `--client-secret`, `claude mcp
  login`: interactive, yours to run), WebSocket servers, and the
  `local` and `project` scopes.

### Tool permissions

`ALLOW TOOL '<rule>'` and `DENY TOOL '<rule>'` add rules to the playbook's
`settings.json` `permissions.allow` / `permissions.deny`; `UNSET TOOL
'<rule>'` removes a rule from either. A rule is Claude Code's own permission
syntax, stored as typed (`'Bash(git diff *)'`, `'Read(~/secrets/**)'`,
`'mcp__sentry'`). Adding a rule to one list removes it from the other, so a
rule is in at most one. Order and every rule cpb did not write are kept.
Claude Code has no CLI for permissions, so cpb writes the key.
`permissions.ask`, `defaultMode` and `additionalDirectories` have no clause.

`SHOW CREATE` writes every rule; `EXPLAIN PLAYBOOK` shows
`Tools: allow …; deny …`. An agent that runs a tool without asking needs
one, as example 08's `ALLOW TOOL 'Bash(git diff *)'`.

### Status line and model

- `SET STATUSLINE '<command>'` writes `statusLine = {"type": "command",
  "command": "<command>"}`, keeping any other field of an existing
  `statusLine` (such as `padding`); `UNSET STATUSLINE` removes it. It always
  applies, whatever command the slot holds.
- **`IF UNSET`**: `SET STATUSLINE '<command>' [REFRESH <n>] IF UNSET`
  applies only where no `statusLine` is set yet, and otherwise reports
  unchanged. A recipe that offers a status line uses it, so applying the
  recipe leaves a status line you chose in place. `SHOW CREATE` writes the
  status line as it is, never the condition
  ([example 17](examples/17-statusline-if-unset/)).
- **REFRESH** sets `statusLine.refreshInterval`, in whole seconds:
  - `SET STATUSLINE '<command>' REFRESH <n>` sets both the command and the
    interval.
  - `SET STATUSLINE REFRESH <n>` sets only the interval, and is refused when
    there is no command status line ("SET STATUSLINE REFRESH needs a status
    line"), since an interval alone means nothing to Claude Code.
  - `UNSET STATUSLINE REFRESH` removes only the interval; `UNSET STATUSLINE`
    still removes the whole status line.
  - `<n>` is a whole number, at least 1, with no unit (`10`, not `10s`).
  - **`SET STATUSLINE '<command>'` without REFRESH keeps an existing
    `refreshInterval`**, as it keeps `padding`. `SHOW CREATE` writes it back
    as `SET STATUSLINE '<command>' REFRESH <n>`, so it round-trips.
  - Why it matters: without `refreshInterval`, Claude Code (verified on
    2.1.283) does not re-render the status line while a session is idle.
    Anything that rides on renders stops, a heartbeat for example.
  - `SHOW PLAYBOOK --json` has `"statusline_refresh": <n> | null` beside `statusline`, the command. `SELECT`'s `PLAYBOOKS` has a
    `statusline_refresh` column. `SHOW` and `EXPLAIN` print `Status line:
    <command> (refreshes every <n> s)`. It is valid on a plain config
    directory too.
- **History and `SET STATUSLINE PREVIOUS`.** Every time a
  statement replaces the status line's command, or removes the status line,
  cpb keeps the whole `statusLine` object it replaced (`padding`,
  `refreshInterval` and all).
  - `SET STATUSLINE PREVIOUS` puts the newest one back and keeps the current
    one in its place, so a second `PREVIOUS` returns to where you were.
  - A `REFRESH`-only change is not history.
  - With nothing recorded, `PREVIOUS` is refused: "no earlier status line is
    recorded".
  - The history is cpb's own state:
    `<playbooks root>/.state/statusline-history.json` (mode 0600), keyed by
    the config directory. At most 10 entries are kept per directory. A
    change made outside cpb (`/statusline`, another tool, a hand edit)
    is recorded the next time a cpb statement replaces it.
  - `SHOW PLAYBOOK --json` has `statusline_history`: `[{"command",
    "refresh", "replaced_at"}]`, newest first. `SELECT`'s `PLAYBOOKS` has
    the same column. `EXPLAIN` prints `Status line history: N earlier (SET
    STATUSLINE PREVIOUS restores <command>)`.
  - `SHOW CREATE` never writes it, since it is state and not configuration.
  - It is valid on a plain config directory, and it is refused together with
    another status line clause in one statement.
- `SET MODEL '<model>'` writes `model`; `UNSET MODEL` removes it. It is the
  playbook's default model and the lowest-priority choice: `ANTHROPIC_MODEL`
  from an env set or `SET VAR`, a launch's `--model`, and `/model` in a
  session all win over it. `EXPLAIN PLAYBOOK` says which one decides.

Both are settings keys with no CLI.

### Model picker

The `/model` picker of a playbook, or of a plain config
directory (it is user scope), from settings.json `modelPicker` =
`{options: [{model, label, description, behavesAs}], replaceBuiltInOptions}`:

```
ALTER PLAYBOOK router-agent
  ADD MODEL 'glm-5.3' LABEL 'GLM 5.3' DESCRIPTION 'via the router'
  ADD MODEL 'glm-5.3-flash' LABEL 'GLM 5.3 Flash' BEHAVES AS 'claude-sonnet-5'
  SET MODEL PICKER ONLY;
```

- `ADD MODEL '<id>' [LABEL '<text>'] [DESCRIPTION '<text>'] [BEHAVES AS '<id>']` adds a row, or updates the row with that model id in place.
  - A field left out keeps what the row has.
  - A field given is written: `label`, `description`, `behavesAs`.
- `DROP MODEL '<id>'` removes that row, and is refused when there is no such row. Dropping the last row removes `options`.
- `SET MODEL PICKER ONLY` shows these rows only (`replaceBuiltInOptions: true`), and `APPEND` adds them after the built-in ones (`false`). Without either, Claude Code appends.
- `UNSET MODEL PICKER` removes `modelPicker` whole.
- **Rows no clause names are kept**, whatever wrote them, and so is a field cpb does not know. A key is written only when a clause gives it, so a playbook with only `ADD MODEL` rows has a `settings.json` holding just `modelPicker`.
- **Reads:**
  - `SHOW PLAYBOOK --json` has `"model_picker": null | {"mode": "only" | "append", "options": [{"model", "label", "description", "behaves_as"}]}`, with `mode` "append" unless `replaceBuiltInOptions` is true.
  - `EXPLAIN PLAYBOOK` and the human `SHOW` print a `Model picker:` line.
  - `SHOW CREATE` writes the clauses back.
  - `SELECT`'s `PLAYBOOKS` has a `model_picker` column.
- **Claude Code versions:** `modelPicker` is read from Claude Code 2.1.242, and `behavesAs` from 2.1.257. cpb writes the key either way and does not check the version.
- `SET MODEL '<model>'` (the `model` key, above) is a separate clause. After `SET MODEL`, `PICKER` begins this one; a model id is quoted.

### Skills

A git source may also be `file://…` (a local repository).

`ADD SKILL <name> FROM <source>` puts a skill directory (one that holds
`SKILL.md`) at `<playbook>/skills/<name>`; `DROP SKILL <name>` removes it.
How depends on the source, and that is deliberate:

- **A directory is linked.** `FROM '/abs/dir'`, `'~/dir'`, or in a playbook
  file `'./dir'`: `skills/<name>` becomes a symlink to it. A local directory
  is a skill under development, so edits there reach the next session
  without another `APPLY`, as `LINK` does for a playbook.
- **A git source is copied.** `FROM <url> [BRANCH <ref>] [SUBDIR <dir>]`:
  cpb clones it and copies the skill directory (the repository root, or
  `SUBDIR`) into `skills/<name>`. A published skill is a pinned artifact: the
  copy survives the source moving or disappearing, and `cpb update` refreshes
  it from the recorded source (`cpb update <playbook>`; `cpb self-update`
  updates cpb itself).

The source is recorded in the manifest, `[skills.<name>]` (`source`, `branch`,
`subdir`, `mode = "link" | "copy"`). The record is what makes the skill cpb's:

- `DROP SKILL` removes only a skill cpb recorded, and refuses a
  `skills/<name>` it did not put there.
- Each skill is recorded as soon as it is in place, so a statement that
  stops at a failed skill leaves the finished ones recorded, and running it
  again finishes the rest. A skill whose record cannot be written is taken
  away again, so the disk never holds a skill cpb has no record of.
- Skill clauses run in clause order with the plugin and MCP commands: a
  failed `ADD SKILL` stops the statement before a later clause's command.
- A skill name in the manifest is held to the grammar's rule when the
  manifest is read: a record cannot name a path outside `skills/`.
- `cpb update <playbook>` overlays the entries the playbook's source ships,
  which can replace `skills/` as a whole; it restores every recorded skill afterwards,
  and a recorded skill wins over a skill of the same name the source ships
  (re-links it, or re-copies it from its source).
- `SHOW CREATE` writes the recorded skills, and `SHOW PLAYBOOK --json` has
  `"skills"`.

Claude Code has no CLI that installs an existing skill (`claude plugin init`
scaffolds a new, empty skills-dir plugin under `skills/`), so cpb manages the
directory. A skill that ships inside a
plugin stays the plugin's: `ADD PLUGIN` brings it.

## SHOW and EXPLAIN

### SHOW PLAYBOOK

**`SHOW PLAYBOOK <name>`**, human form (one `Label:` per line; labels
aligned):

```
Name:       work
Version:    1.2.0
Path:       /Users/me/.claude-playbooks/work
Source:     https://github.com/example/work-playbook (branch v1.2.0)
Launcher:   w
Env sets:   router, glm-5.3
Variables:  MAX_THINKING_TOKENS=8000
            ANTHROPIC_AUTH_TOKEN <from keychain:router-token>
            HTTP_PROXY (blocked)
Sandbox:    no
```

`Source:` reads `(linked) <dir>` for a linked playbook and `(none)` for one
created empty; `Launcher:` reads `(none)` without one. `Description:`,
`Homepage:` and `Author:` follow `Version:` when the manifest has them.

`--json`, one object:

```
{"name": "work", "version": "1.2.0",
 "description": null, "homepage": null, "author": null,
 "path": "/Users/me/.claude-playbooks/work",
 "last_used": "2026-10-02T09:41:12.000Z",
 "migrate": null,
 "source": {"url": "https://github.com/example/work-playbook", "branch": "v1.2.0", "subdir": null},
 "linked": null,
 "launcher": "w",
 "envs": ["router", "glm-5.3"],
 "vars": [<variable>, ...],
 "sandbox": {"always": false, "backend": null, "host": null, "workdir": null, "mounts": [],
             "allow_net": [], "secrets": null, "claude_version": null, "share_skills": false}}
```

`description`, `homepage` and `author` are the manifest's, null when it has
none; `last_used` is when the playbook's config directory last changed
(RFC 3339, UTC); `migrate` is the declared migrate step (`[update] migrate`)
that `cpb update` runs, null without one, and the human form has a
`Migrate:` line for it. `source` is null for a playbook without one; `linked` is the
target directory of a linked playbook, else null; `launcher` is null without
one.

**`play`** is the object's last field. It is the `[play]` record of a
playbook `cpb play --keep` built, and `null` for every other:
`{"ref", "url", "sha256", "played_at"}`. `ref` is what `cpb update <name>`
fetches again (a template name, a URL, a `github:` ref, or a local file's
absolute path); `url` is empty for a local file; `played_at` is RFC 3339,
UTC. The human form has a `Played from:` line.

### SHOW PLAYBOOKS

**`SHOW PLAYBOOKS`** (also a bare `SHOW`, and `SHOW --json`): human form, one header line and then one line per
playbook sorted by name, columns `NAME VERSION LAUNCHER ENV SETS SOURCE`
(`-` for none). `--json`: an array of the `SHOW PLAYBOOK` objects.

### SHOW ENV and SHOW ENVS

**`SHOW ENV <name>`**: human form, `Name:`, `Description:`, `Used by:`,
`Default:` (yes/no), then `Variables:` as above. `--json`:

```
{"name": "router", "description": "GLM via a local router",
 "vars": [<variable>, ...], "used_by": ["work"], "default": false}
```

**`SHOW ENVS`**: human form, one line per set, columns
`NAME SET BLOCKED USED BY DESCRIPTION`, a `*` after the name of each set in
`DEFAULTS`. `--json`: an array of the `SHOW ENV` objects.

### SHOW DEFAULTS

**`SHOW DEFAULTS`**: human form, `Env sets:` in order, and
`Secret helper:` with the command and where it came from (`setting` or
`CPB_SECRET_HELPER`), or `(none)`. `--json`:

```
{"envs": ["claude-default", "corp-proxy"],
 "secret_helper": {"command": "my-keychain-helper", "from": "setting"}}
```

(`secret_helper` is null when none is configured.)

### EXPLAIN PLAYBOOK

**`EXPLAIN PLAYBOOK <name>`**: human form as in *Layers at launch*.
`--json`:

```
{"playbook": "work",
 "vars": [{<variable>, "layer": {"kind": "env", "name": "router"}}, ...],
 "secret_helper": {"command": "...", "from": "setting"} | null}
```

`layer.kind` is `defaults` (with `name` the env set), `env` or `playbook`.

### Redaction

A `set` key that looks like a credential
prints a masked value instead of the resolved one, everywhere a `set` entry
is shown: `SHOW PLAYBOOK`, `SHOW ENV`, `EXPLAIN PLAYBOOK` and the TUI. There is no separate per-field
secret marker in an env set's TOML (its `set` is a plain
`map[string]string`); a key-name heuristic (case-insensitive) is what decides
it, in three rules, each as wide as it can be without swallowing an obvious
non-secret:

| Rule | Matches | Because | Cost |
|---|---|---|---|
| substring anywhere | `TOKEN`, `SECRET`, `PASSWORD`, `PASSWD`, `PASSPHRASE`, `CREDENTIAL` | `GITHUBTOKEN` has no underscore to split on | over-matches `TOKENIZER_PATH` |
| whole `_`-separated segment | `AUTH`, `PWD`, `PASS`, `PAT` | `MYSQL_PWD` is the standard MySQL password variable; as substrings these would redact `AUTHOR_NAME`, `COMPASS_URL` and `PATH` | over-matches a bare `PWD` |
| end of a `_`-separated segment | `KEY`, `KEYS` | `API_KEY`, `MY_APIKEY` and `GPG_SIGNKEY` are all keys | spares `KEYBOARD_LAYOUT` |
| exemption | a `PUBLIC` or `PUB` segment defeats the `KEY`/`KEYS` rule only | `PUBLIC_KEY` is meant to be read and compared | a `PUBLIC_SECRET` is still masked |

Over-matching is deliberate throughout: a missed credential is a silent leak,
an over-redacted ordinary key costs one masked value. A bare `PWD` is masked on
those terms -- it usually names the working directory, but it could name a
password, and guessing "directory" is the assumption that leaks.

**A credential inside a connection URL is masked from the value**, since no
rule above can see it: `DATABASE_URL`, `REDIS_URL`, `AMQP_URL` and
`MONGODB_URI` name nothing secret while carrying a password. Only the URL's
userinfo is masked, not the whole value -- the scheme, host and database stay
legible, because a wholly masked `DATABASE_URL` would train the pilot to read
the file instead, which is how a feature like this stops being used.

**Both userinfo fields are masked**, because a colon says only that there are
two fields, never which one holds the secret:

| Shape | Where the credential is |
|---|---|
| `postgres://user:pw@host` | the password, on the right |
| `https://TOKEN:x-oauth-basic@host` | the username -- GitHub's documented form, beside a fixed dummy |
| `https://TOKEN:@host` | the username -- what `git credential` writes, beside an empty password |
| `https://TOKEN@host` | the whole userinfo, no colon at all |

Nothing in the structure distinguishes them, so masking one side leaks the
other half the time; the username is the cheaper thing to lose. An empty
field stays empty rather than becoming `<redacted, 0 chars>`, which would
be noise that also advertises which shape the URL is.

The authority ends at the first `/`, `?` or `#`, none of which may appear in
userinfo -- otherwise the `@` in
`https://service.test?email=a@example.com` reads as a userinfo delimiter and
an ordinary callback URL is mangled as though it carried a credential.

```
  set    DATABASE_URL=postgres://<redacted, 4 chars>:<redacted, 11 chars>@db.internal:5432/app
```

A credential in a URL **query parameter** (`?password=`, a presigned
signature) is **not** covered: recognising one needs per-scheme parameter
knowledge, and a list of parameter names would go stale the way a list of key
names does.

The length is stated
outright rather than left to be inferred from a run of masking characters. A
value keeps up to 4 characters at each end, scaled down as it shortens, and
**at least 8 characters always stay hidden** -- so anything under 12
characters is redacted whole rather than showing half of itself, which
matters because `PASSWORD` is in scope and a human-chosen password is short
and guessable enough that half of one is most of one:

```
  set    ANTHROPIC_AUTH_TOKEN=sk-a...7f2c (43 chars)
  set    ANTHROPIC_BASE_URL=http://proxy:1/v1
```

A value too short to leave that gap is redacted whole
(`<redacted, N chars>`). No form prints the resolved value, and a key that
does not match the heuristic (`ANTHROPIC_BASE_URL` above) is never touched.

## SELECT

`SELECT` queries the same state `SHOW`
prints, as tables, with two engines:

```
select := [EXPLAIN] SELECT … FROM <table> …        [--json]
cpb "SELECT name, version FROM PLAYBOOKS"                              built in
cpb "SELECT name FROM PLAYBOOKS WHERE version_tuple > [3, 10] ORDER BY name"   clickhouse-local
```

- **Built in, always:** exactly `SELECT <col>[, <col> …] FROM <table>`: plain
  column names, spelled exactly (ClickHouse identifiers are case-sensitive:
  `name`, not `NAME`), no `*`, no functions, no `WHERE`. It is a strict subset
  of ClickHouse SQL, so a query means the same on both paths. The output is
  described under "What a terminal and a pipe get"; `--json` prints the
  selected fields, an array of one object per row. Each object's keys come in
  the order the query names the columns, as `clickhouse local` writes them. A column named twice is one key, at
  its first position; that rule is cpb's own. A nested object's keys are as `SHOW … --json`
  prints them.
- **Anything else goes to ClickHouse** when it is installed: `clickhouse` or
  `ch` on `PATH`, or the command `CPB_CLICKHOUSE` names. cpb pipes the
  table's rows to `clickhouse local --input-format JSONEachRow --structure
  '<typed columns>' -q "<query>"`, with `FROM <table>` rewritten to read
  stdin (`FROM table`; for `PLAYBOOKS`, a subquery over it that adds
  `version_tuple`). The `FROM` is found as ClickHouse reads the query, so
  `'FROM VARS'` in a string or a comment is not a table. One statement per query: text after a `;` is refused. Without ClickHouse the query is refused in one line: "this
  query needs ClickHouse (clickhouse local); install it, or pick columns
  only". One table per query.
- **What a terminal and a pipe get:**
  - **On a terminal, with no `FORMAT` in the query,** cpb renders the result
    itself, the same way on both paths. For ClickHouse it asks
    `clickhouse local` for `JSONCompact` and reads the names and values
    from that.
    - Up to 6 columns print as a table.
    - More print one block per row (`Row 1`, then `NAME:  value` lines), so
      a wide result such as `SELECT *` does not wrap.
    - Headers are the column names in capitals, as `SHOW` prints them.
    - An object, or an array of objects, prints as compact JSON, `/`
      unescaped. Other arrays print as `a, b`.
    - `NULL`, an empty array and an empty object print as `-`, as in `SHOW`.
  - **In a pipe,** the built-in form prints its table, and a ClickHouse query
    prints `clickhouse local`'s own default, TSV.
  - **A `FORMAT` in the query always wins,** on a terminal too
    (`… FORMAT PrettyCompact`, `… FORMAT JSONEachRow`), and cpb adds no
    `--output-format`. A `FORMAT` inside a string or a comment is not the
    query's.
  - `EXPLAIN SELECT` shows the command as it would run in the terminal it
    is typed in.
- **Only what `--json` already shows is handed over:** the rows are exactly
  the `SHOW … --json` objects (a credential redacted, a reference shown as the
  reference), one per line, and nothing else. A test pins those bytes.
- **`EXPLAIN SELECT …`** prints which engine would run it and the exact
  clickhouse command, and runs nothing.
- **On the command line** a statement can be one quoted argument (the shell
  would glob `*` and split `(`): `cpb "SELECT …"`. It is read with the
  playbook-file lexer and the command line's rules; any statement works this
  way, flags inside the quotes (`cpb "SELECT name FROM PLAYBOOKS --json"`). An unknown column's error says "If you typed * unquoted, the shell
  expanded it: quote the statement."

**Tables.** Their columns are the `--json` fields, and ClickHouse reads each
with a typed structure (a nested object or list as `JSON` / `Array(JSON)`,
so `source.url` and `vars[1].key` work):

| Table | One row per | Columns |
|---|---|---|
| `PLAYBOOKS` | playbook | the `SHOW PLAYBOOK` object, plus the computed `version_tuple` (`play` is the last column) |
| `ENVS` | env set | `name description vars used_by default` |
| `VARS` | variable, per layer, per playbook | `playbook key value ref redacted plaintext blocked layer effective` |
| `SESSIONS` | live Claude Code session | the `SHOW SESSIONS --json` object: `playbook pid session_id cwd kind status name claude_version started_at last_active model launcher config_dir resume tty` (`tty`) |
| `DEFAULTS` | (one row) | `envs secret_helper` |

`version_tuple` is `Array(UInt32)`, the numbers of the version's leading
numeric part (`"v3.12.3-rc1"` → `[3, 12, 3]`; no version → `[]`): compare
and sort versions with it, because strings sort `"v3.9.0"` after
`"v3.12.3"`. It is computed, not handed over: cpb computes it for the
built-in form, the query computes it for ClickHouse. A `VARS` row is one entry of one layer
(`DEFAULTS` sets, the playbook's sets, its own block), `effective` when it is
the one a launch uses. The manual form, piping `SHOW … --json` yourself, is in
[Query with SQL](docs/guides/query-with-sql.md).

### DESCRIBE

`DESCRIBE [TABLE] <table>`, or `DESC`, lists a `SELECT` table's columns and
their types: the typed structure `clickhouse local` reads the rows with,
plus the computed `version_tuple`. A table name is case-insensitive, as in
`SELECT`.

```
cpb DESCRIBE playbooks          # NAME / TYPE, one row per column
cpb "DESC TABLE vars --json"    # [{"name": "playbook", "type": "String"}, …]
```

An unknown table is refused, naming the four; `DESCRIBE` with no table is
refused too. It reads no playbook: the columns are the same on every
machine.

## SHOW SESSIONS

cpb lists the live Claude Code sessions of its playbooks. A session is
resumed through its playbook, with Claude Code's own `--resume` or
`--continue` on the launcher or `cpb run <name>`, and cpb guards that
launch.

```
SHOW SESSIONS [FOR PLAYBOOK <name>] [--json]
SELECT … FROM SESSIONS
```

**Where it comes from.** Claude Code keeps a file for each of its live
processes, `<config dir>/sessions/<pid>.json`, and removes it on exit.
- **Which dirs.** cpb reads those files in the config dirs it knows: every
  playbook, and every plain directory in `.state/dirs.toml`. If that
  registry cannot be read, the statement fails rather than answer without
  its directories.
- **Live.** A session is live when its pid is alive **and** the process
  started when the file says. On Linux that is `/proc/<pid>/stat`'s
  starttime; elsewhere, `ps`'s lstart read in UTC. So a pid that was reused
  since does not count. A live pid whose file records no start time cannot
  be confirmed: it is listed, and a `--resume` of it is refused.
- **Kinds.** Kinds other than `interactive` and `bg` are not listed; a
  daemon's spare workers write no file.
- **Nothing else is read.** cpb reads no process's environment, runs no
  daemon, and never writes, deletes or renames anything under `sessions/`:
  a stale file is Claude Code's business.
- **The format is Claude Code's.** It is undocumented, observed on 2.1.233
  (Linux) and 2.1.282–2.1.283 (macOS), and read defensively: a file that
  does not decode is skipped, and a dir without `sessions/` has no live
  session.

**`SHOW SESSIONS`** prints the live sessions, newest first: one line each
(`PLAYBOOK PID TTY KIND STATUS AGE ACTIVE MODEL SESSION CWD`).
- `FOR PLAYBOOK` limits it to one playbook, and an unknown name is refused.
- **`--json`** is an array of objects:

| Field | Type | From |
|---|---|---|
| `playbook` | string | the playbook's name; a plain directory's path |
| `config_dir` | string | the config dir |
| `pid` | number | the session file |
| `session_id` | string | the session file |
| `cwd` | string | the session file |
| `kind` | `"interactive"` or `"bg"` | the session file |
| `status`, `name`, `claude_version` | string or null | the session file, passed through |
| `started_at` | RFC 3339, UTC | the session file |
| `last_active` | RFC 3339, UTC, or null | the transcript's modification time; null before the first message |
| `model` | string or null | the transcript's last assistant message, read from its last 256 KiB |
| `launcher` | string or null | the playbook's launcher |
| `resume` | string | `cd '<cwd>' && <launcher> --resume <id>`: the command that resumes this session from any folder once it ends (see below) |
| `tty` | string or null | the process's controlling terminal (`pts/3`, `ttys012`), null for none (a `bg` session). It is read in the same pass as the start time: `/proc/<pid>/stat`'s tty_nr on Linux, `ps`'s tty elsewhere |

A session's transcript is
`<config dir>/projects/<cwd, each character outside [A-Za-z0-9] as ->/<id>.jsonl`.
If that path is missing, cpb looks under every project directory.

**`SELECT … FROM SESSIONS`** has the same rows. ClickHouse reads
`started_at` and `last_active` as `DateTime64(3, 'UTC')`.

**Resuming.** There is no resume statement. `<launcher> --resume <id>`,
`--continue`, or `cpb run <name> --resume …` hand Claude Code's own flags to
claude under the playbook's config dir, with everything a launch has: env
sets, variables, secret references, the login and the exit line. Before
claude starts, cpb checks:
- **`--resume <id>`** (`-r <id>`, `--resume=<id>`) is refused when `<id>`
  is live in any config dir cpb knows, naming the playbook and the pid, and
  nothing is launched. Two processes on one session id corrupt it, which is
  the `claude --continue` hazard. A session cpb cannot confirm is refused
  too: one recorded in another pid domain (a sandbox, another host), or a
  live pid with no recorded start time. There is no `--force`.
- **The folder.** Claude Code looks a session up by the folder it ran in.
  When `<id>`'s transcript sits under another folder's project directory,
  the launch is refused with the command that resumes it, `cd '<folder>' &&
  <launcher> --resume <id>`. The folder is the transcript's last `cwd`, or
  its first when the tail holds none. A transcript that records neither is
  left to claude.
- **`--continue`** (`-c`) is refused when the newest transcript of this
  folder, in this playbook, belongs to a live session.
- **Not checked.** `--fork-session` beside either flag starts a new id.
  `--resume` with no id, or with a search term (a value that is not a
  UUID), opens Claude Code's picker; of several `--resume`, the last counts.
  An id cpb finds no transcript of is left to claude, which says so.
- **Sandboxed launches** hand the flags to the claude inside the sandbox.
  Its sessions live there, out of cpb's sight, so the launch prints a note
  instead of checking, and `SHOW SESSIONS` lists only what the host's
  config dirs hold.
- **A plain directory's session** resumes with `CLAUDE_CONFIG_DIR='<dir>'
  claude --resume <id>` from its folder. `SHOW SESSIONS` prints it with the
  `cd` in front, as for a playbook.

**The exit line.** After `claude` exits under `cpb run` or a launcher, cpb
prints one line on stderr:
```
Resume this playbook's session with: kd --resume 08c4811b-3867-4f18-b08f-de6d1e07395f
```
- **When.** Only when stderr is a terminal, the launch was local, and
  claude was interactive (no `-p` / `--print`).
- **Which session.** The id is the newest transcript written during the
  launch that no other live process holds.
- **Why.** Claude Code's own `claude --resume` line still prints. It would
  open the session under `~/.claude`, not the playbook's config dir, which
  is why this line exists.
- The exit code is claude's.

No word is reserved: `SESSIONS` and `FOR` still name playbooks and env
sets.

## playbook.cpb: SHOW CREATE and APPLY

Two senses of "playbook", kept apart:

- **`PLAYBOOK`**, an *installed playbook*: a Claude Code config directory cpb manages.
- **a playbook file**, also *a playbook script* or *the agent's playbook*: a `.cpb` file of statements, conventionally `playbook.cpb`, that `APPLY` runs. `APPLY` accepts any file name.

A **playbook file** (conventionally `playbook.cpb`) is plain text holding cpb
statements: the same grammar as the command line, without the `cpb` prefix.
It describes a machine's setup: env sets, DEFAULTS, playbooks and their
wiring. It is the script beside the database, never inside it: the manifest holds state, the file holds the recipe. It belongs in dotfiles or a
devbox project, and moving a setup to another machine is one command.

```
-- playbook.cpb, from: cpb SHOW CREATE ALL > playbook.cpb

CREATE OR REPLACE ENV router
  DESCRIPTION 'GLM via a local router'
  SET ANTHROPIC_BASE_URL=http://localhost:8080/v1 ANTHROPIC_MODEL=glm-5.3
  SET ANTHROPIC_AUTH_TOKEN FROM 'keychain:router-token';

CREATE OR REPLACE ENV claude-default
  BLOCK HTTP_PROXY;

ALTER DEFAULTS USE ENV claude-default;

CREATE PLAYBOOK IF NOT EXISTS work
  FROM https://github.com/example/work-playbook BRANCH v1.2.0 LAUNCHER w;

ALTER PLAYBOOK work
  USE ENV router
  SET VAR MAX_THINKING_TOKENS=8000
  BLOCK VAR CLAUDE_CODE_OAUTH_TOKEN;
```

File rules:

- **Only write statements** (`CREATE`, `ALTER`, `DROP`), and the directives
  `INCLUDE` and `USE PLAYBOOK` (see "Targets"). A read is refused.
- **`;` ends a statement**, newlines are whitespace, and `-- ` (two dashes
  and a space, or at the end of a line, as in MySQL) starts a comment, so a
  word such as `--dry-run` stays a word.
  On the command line no `;` is needed.
- **No secret values.** Secrets appear only as `FROM '<ref>'`. `SHOW CREATE`
  never prints a credential-looking literal: it writes it as a commented-out
  line with the value masked, `-- SET VAR K=<masked> AS PLAINTEXT`, followed
  by the statement that fixes it (`ALTER ENV <set> SET K FROM '<ref>'`, or
  `ALTER PLAYBOOK <name> SET VAR K FROM '<ref>'`), and exits
  non-zero, unless `--skip-secrets` is given. Plain text never travels in a
  playbook file.
- **Idempotent.** `SHOW CREATE` emits only idempotent forms (`CREATE OR
  REPLACE ENV`, `CREATE PLAYBOOK IF NOT EXISTS …`, `USE ENV` with the full
  list), so applying a file twice changes nothing the second time.
- **Machine-specific parts stay explicit.** `LINK <dir>` and `SANDBOX` are
  emitted as they are; a `LINK` path missing on the target fails validation.
- `SHOW CREATE ALL` orders statements: env sets by name, `DEFAULTS`,
  playbooks by name.

`cpb APPLY <file> [<file> ...]`:

1. Parses **every** file and validates every statement: syntax, names, the
   secret helper's check of each reference (against the helper the files
   will have set by then: a file may set the helper and use it in one run),
   and the `DROP PLAYBOOK` confirmation below. If anything in any file fails, nothing is written.
   (A source is fetched only when its `CREATE PLAYBOOK` runs.)
2. Runs the files in the order given, and their statements in order, each
   statement atomic, reporting each as `created`, `changed`, `unchanged` or
   `dropped`, located as `file:line`.
3. **Stops at the first failure** and reports the failing `file:line` and
   how many statements of each file were applied (`a.cpb 3 of 3, b.cpb 1 of
   4`). There is no whole-file rollback: a clone cannot be undone cheaply.
   Because every statement `SHOW CREATE` emits is idempotent, running the
   fixed files again is the recovery.

`--dry-run` does step 1 and reports what step 2 would do, judging each
statement against the files as they are plus what earlier statements, in
any of the files, would have created.

**Source drift is a warning, never an error**. When
`CREATE PLAYBOOK IF NOT EXISTS x FROM <source> [BRANCH b] [SUBDIR d]` meets an
existing `x` whose recorded source, branch or subdirectory differs, `APPLY`
reports `PLAYBOOK x exists; source differs (installed <…>, file says <…>)`
and changes nothing; `--dry-run` shows it too, and the summary counts the
warnings. The exit code stays 0 when that is the only issue: moving an
install to another source is `DROP PLAYBOOK` and `CREATE PLAYBOOK`, a
deliberate step.

**A file never consents to `DROP PLAYBOOK`**. It is the
one irreversible statement: it deletes the install directory, with
everything the playbook keeps there (its `data/`, for one that has some). So `APPLY <file>`
refuses at validation, writing nothing, when any of its files contains a
`DROP PLAYBOOK`, and lists each one as `file:line`, unless `--yes` is given;
`--yes` covers the drops in all of them.
`--dry-run` shows the drops, and what each would delete, without `--yes`.
`SHOW CREATE` never emits `DROP PLAYBOOK`, so a drop in a file is always
hand-written, which is exactly when a second confirmation is worth it.
`DROP ENV` and the `DROP ENV` clause only detach or delete an env set file
and need no `--yes`.

### APPLY --dry-run --json

`cpb APPLY <file> … [TO <target>] --dry-run
--json` prints the plan as **one JSON object on stdout**, in every case,
refusals included. It follows the `--json` rule: fields may be added, and
none changes meaning within a major version. `schema` is bumped only on a
meaning change, and so only with a major version; a consumer refuses a
schema it does not know. **Verdicts, action types and warning codes are
closed sets** within a major version.

`--json` needs `--dry-run`: the JSON form is a plan, and there is no real `APPLY --json`. The human lines of the dry run go to
stderr.

A dry run **creates nothing**: not the store (`CPB_PLAYBOOKS_DIR`),
nothing under it, and no lock file. A store that does not exist yet is
planned as empty.

```
{
  "schema": 1,
  "cpb_version": "v4.0.0",
  "files": ["/abs/main.cpb", "/abs/base.cpb"],
  "target": {"kind": "playbook", "name": "fresh"},
  "ok": true,
  "error": null,
  "warnings": [{"code": "use_playbook_overridden", "file": "/abs/main.cpb", "line": 2,
                "message": "USE PLAYBOOK other is ignored: TO fresh sets the target"}],
  "statements": [
    {"file": "/abs/main.cpb", "line": 3, "statement": "ALTER PLAYBOOK fresh",
     "verb": "ALTER", "object": "PLAYBOOK", "target": {"kind": "playbook", "name": "fresh"},
     "recipe": true, "implicit": false, "verdict": "changed", "reason": null, "warnings": [],
     "actions": [
       {"type": "command", "argv": ["claude", "plugin", "marketplace", "add", "/abs/mkt", "--scope", "user"],
        "env": {"CLAUDE_CONFIG_DIR": "/abs/.claude-playbooks/fresh"}, "network": false}
     ]}
  ],
  "summary": {"created": 0, "changed": 1, "unchanged": 0, "dropped": 0, "refused": 0, "warnings": 1}
}
```

- **`files`**: the files run, resolved (symlinks followed), in load order, included files too.
- **`target`**: `TO`'s target, or `null` without `TO`.
- **`statements[]`**: one per statement run, in order.
  - `file` is the resolved path of the file the statement is in (an included file's own path), and `line` is in that file.
  - `target` is resolved on every statement: `{"kind": "playbook", "name"}`, `{"kind": "dir", "path"}` (TO a plain config directory), `{"kind": "env", "name"}` or `{"kind": "defaults"}`.
  - `recipe` is true when TO or USE PLAYBOOK supplied the name.
  - `implicit` is true for the bare `CREATE PLAYBOOK` a missing target gets; its `file` and `line` are the recipe statement's.
  - `warnings` lists every warning the statement raised, each a warning object (below); empty when there is none.
- **Verdicts:** `created`, `changed`, `unchanged`, `dropped`, `refused`.
- **Actions**: what a real run would do beyond the playbook's own manifest and env files, which the verdict covers.
  - Paths are absolute. A `./` source is resolved against its file.
  - `network` is true on an action that fetches.

  | `type` | Fields | |
  |---|---|---|
  | `command` | `argv`, `env`, `refs` (MCP) | a `claude plugin …` or `claude mcp …` run. `env` holds only non-secret variables (`CLAUDE_CONFIG_DIR`). An MCP config carries `${CPB_MCP_…}` placeholders only, and `refs` maps each to its reference. `network` is true for `marketplace add` from git or GitHub, and for `plugin install`. |
  | `skill` | `op` (`link` or `copy`), `name`, `path`, `source` | a skill put in place. `network` is true for a copy from a remote git source. |
  | `fetch` | `source`, `branch`, `subdir`, `to` | `CREATE PLAYBOOK … FROM`: what a real run installs. `network` is false for a local directory. |
  | `backup` | `path`, `to` | TO a plain directory: a file backed up before its first write. |
  | `write` | `path` | TO a plain directory: its `settings.json`. |
  | `delete` | `what` (`playbook` or `skill`), `path`, `bytes` | what a real run removes, with its size on disk. Symlinks are not followed. A replaced skill is a `delete` then a `skill`. |

- **Warning codes:** `use_playbook_overridden` (TO ignores a file's `USE PLAYBOOK`), `source_drift` (an existing playbook's recorded source differs), `marketplace_ref_not_cloneable` (an `ADD MARKETPLACE` git source whose `#<ref>` looks like a commit, 7 to 40 hex characters, which Claude Code will not clone; see "Plugins and the agent"), and `shared_login_link_kept` (`SET ISOLATED LOGIN` is recorded, but the link to the shared login could not be removed at once; the next launch removes it). A warning is `{"code", "file", "line", "message"}`. The top-level `warnings` are the files' own; each statement's `warnings` lists every warning it raised, one entry each. `summary.warnings` counts them all.
- **No secret value** appears anywhere: references stay references, and a literal credential a file sets is not in the plan.

**Exit codes:**

- `0`: every statement was planned.
- `1`: the files are refused.
  - A statement the dry run refuses ends the list with `"verdict": "refused"` and its `reason`, and later statements are absent.
  - A refusal before any statement runs (a parse error, a missing reference, a refused clause on a plain directory) leaves `statements` empty, with `error: {"file", "line", "message"}`.
- `2`: a usage or internal error: `--json` without `--dry-run`, a missing file, a `TO` that cannot be resolved, an unreadable registry. `error.file` and `error.line` are `null` when no file is at fault.

`ok` is true exactly when the exit code is 0.

### INCLUDE

A playbook file can pull in another, so one machine's file can share a base
with the next:

```
-- playbook.cpb
INCLUDE 'base.cpb';
INCLUDE 'envs/routers.cpb';
ALTER PLAYBOOK work USE ENV glm;
```

```
statement := … | INCLUDE '<path>'
```

- **A directive, not a statement about state.** The playbook-file rule "only
  write statements" admits `INCLUDE` besides `CREATE`, `ALTER` and `DROP`;
  every other read stays refused.
- **Local regular files only.** A path is absolute, or relative to the
  directory of the file that includes it. It must name a regular file: a
  URL, a pipe, a device (`/dev/stdin`, `/dev/fd/…`) or a directory is
  refused. A playbook file runs with your authority, so what it pulls
  in must be a file on this machine. A root file that is not itself a
  regular file (a pipe given to `APPLY`) may be applied, but may not
  `INCLUDE` a relative path, which would have nothing to resolve against.
- **Only in playbook files.** `INCLUDE` on the command line is refused;
  `APPLY <file> [<file> ...]` is the command-line form of the same thing.
- **Expanded before anything runs.** Includes are expanded first, and the
  checks `APPLY` makes before writing (parsing, secret references, the
  `DROP PLAYBOOK` confirmation) run over the whole expanded set: an error
  they find anywhere means nothing is written. Execution then follows
  `APPLY`'s rule unchanged: statements in order, each atomic, stopping at
  the first failure with what was applied reported; there is no rollback of
  what ran before. Reports locate each statement as `file:line`.
- **File identity** is the fully resolved path (symlinks resolved, made
  absolute). Two spellings of one file are the same file.
- **A cycle is refused**, naming its chain (`a.cpb -> b.cpb -> a.cpb`).
- **A file reached twice runs once**, at its first occurrence, so a shared
  base included by two files is applied a single time.
- `INCLUDE` is a reserved word. An object whose name is a keyword stays reachable: `SHOW CREATE` quotes such a name, and a quoted keyword is accepted as a new name, so its output applies.

**Secret references and a helper set in the same run.** A playbook file may
set the secret helper and use it: the reference check that runs before
anything is written uses, for each `SET … FROM`, the helper an earlier
`ALTER DEFAULTS SET SECRET HELPER` of the expanded set would configure, and
the configured one otherwise. (The same rule applies to `APPLY` without
`INCLUDE`.)

**Not planned** in playbook files: variables, loops and conditionals. A
playbook file stays a flat list of statements that reads the same every
time; anything more needs your explicit approval first.

### Targets: recipes, USE PLAYBOOK and APPLY … TO

A playbook file can be a **recipe**,
written once and applied to any playbook, or to a plain Claude Code config
directory such as `~/.claude`.

```
-- agent.cpb, a recipe: no playbook named in its statements
INCLUDE 'base.cpb';
ALTER PLAYBOOK
  ADD MARKETPLACE team FROM './team-plugins'
  ADD PLUGIN reviewer@team
  SET AGENT 'reviewer'
  ALLOW TOOL 'Bash(git diff *)';
```

```
cpb APPLY agent.cpb TO reviewer-agent          # a playbook (created bare if missing)
cpb APPLY agent.cpb TO '~/.claude'             # a plain config directory
```

### The name is optional

`ALTER PLAYBOOK [<name>] <clause> ...`: when the word after `PLAYBOOK` is a
clause keyword, the statement names no playbook and its **target** is decided
when the file is applied. No new keyword is needed, and nothing is
ambiguous: a name cannot be an unquoted keyword, and a quoted word is always
a name. Only `ALTER PLAYBOOK` may leave the name out; `CREATE` and `DROP`
always name what they create or drop. On the command line a statement always
names its playbook.

### USE PLAYBOOK

`USE PLAYBOOK <name>;` in a playbook file sets the target of the name-less
statements after it, as `USE <db>` does in SQL. It is a directive, like
`INCLUDE`, and appears only in playbook files.

- A later `USE PLAYBOOK` changes the target for the statements after it.
- An included file starts with the including file's target at the point of
  the `INCLUDE`, and a `USE PLAYBOOK` inside it applies to that file and the
  files it includes; the including file's target resumes after the `INCLUDE`.
- **"A file reached twice runs once" is per target, for the statements that
  depend on it.** A file's name-less statements run once per target it is
  reached under; everything else in it (statements that name their object,
  env sets, `DEFAULTS`) runs at its first occurrence only. So a shared file
  that creates an env set and then alters "the" playbook, included for two
  targets, creates the env set once and applies its recipe to both. The
  directives are not statements that run: an `INCLUDE` is followed at every
  occurrence and a `USE PLAYBOOK` takes effect at every occurrence, so a
  shared file reached again for a second target still includes its files and
  switches its target.

### Which target a name-less statement gets

1. `cpb APPLY <file> TO <playbook|dir>`: every name-less statement in the
   files goes to that target, and the files' `USE PLAYBOOK` lines are
   ignored, each named in a warning.
2. Otherwise the `USE PLAYBOOK` in effect at the statement.
3. Otherwise the file is refused when it is validated, before anything is
   written: "this file has name-less statements and no target: add
   `TO <playbook|dir>` or a `USE PLAYBOOK` line".

There is no implicit target: not the current directory, not a default
playbook. `TO .` names the current directory, explicitly. A playbook named by
`TO` or `USE PLAYBOOK` that does not exist is created bare first, as
`CREATE PLAYBOOK <name>` would (its launcher is its name), and the dry run
lists that creation.

**Explicit names are literal.** A statement that names its playbook applies
to that playbook, whatever `TO` says; `TO` only fills the name-less ones. A
reusable recipe therefore names no playbook; a machine's setup
(`SHOW CREATE`'s output) names every one. `SHOW CREATE` writes names.

### TO a plain config directory

`TO '<dir>'` targets a Claude Code config directory, typically one that is
not a playbook, for example `~/.claude`. A target is a directory when it contains `/` or
starts with `~` or `.`; a playbook name never does. The directory must exist;
cpb creates nothing but what the clauses write. **A directory that is a
registered playbook is that playbook:** cpb resolves the path (symlinks
followed) and, when it is a playbook's directory or config directory, applies
with that playbook's semantics and safeguards (its manifest, its launcher,
the refusals on a linked playbook), never the plain-directory rules below.
When the path belongs to more than one registered playbook (several
registrations linked to one directory), the target is refused and the
playbooks are named: `TO <name>` picks one. The rules below are for a
directory no playbook claims.

Only the clauses that are Claude Code's own configuration apply, because
nothing of cpb runs at that directory's launches:

| Allowed | How |
|---|---|
| `ADD / DROP MARKETPLACE`, `ADD / DROP PLUGIN` | `claude plugin …` with `CLAUDE_CONFIG_DIR=<dir>`, as for a playbook |
| `SET / UNSET AGENT`, `ALLOW / DENY / UNSET TOOL`, `SET / UNSET STATUSLINE`, `SET / UNSET MODEL` | the directory's `settings.json`, as for a playbook |
| `ADD / DROP MCP SERVER` without credentials | `claude mcp … --scope user` with `CLAUDE_CONFIG_DIR=<dir>`; a credential needs a reference, which a plain directory cannot resolve, so a server that needs one is refused there |
| `ADD / DROP SKILL` | `<dir>/skills/<name>`; the record lives in cpb's own state, `<playbooks root>/.state/dirs.toml`, keyed by the directory's absolute path, never inside the directory |
| `SET VAR K=V ...` / `UNSET VAR K ...` | the `env` map of the directory's `settings.json` (Claude Code's own per-install variables); a credential-looking literal still needs `AS PLAINTEXT` |

Refused, each with its reason:

- `SET VAR K FROM '<ref>'`, and `VAR` / `HEADER … FROM` on an MCP server: a
  reference is resolved by cpb's launcher, which never runs for that
  directory.
- `BLOCK VAR`: removing a variable at launch is the launcher's job.
- `USE / ADD / DROP ENV`, and `ALTER DEFAULTS`: env sets and `DEFAULTS` are
  layered by the launcher.
- `RENAME TO`, `LAUNCHER`, `NO LAUNCHER`, `SANDBOX`: the directory is not in the
  registry and has no launcher.
- `SET / UNSET ISOLATED LOGIN`: `isolated_login` is recorded in a playbook's
  manifest, which the directory does not have.

**Safety.** Before its first write to a directory in a run, cpb copies that
directory's `settings.json` to `settings.json.cpb-backup-<YYYY-MM-DD-HH_MM_SS>`
beside it, and `.claude.json` the same way before an MCP change; a skill
change is a write too. Each file is backed up once per run, and a dry run
plans the same single backup. A file that
does not exist yet has nothing to back up: the clause creates it, and the dry
run says which files would be created rather than backed up. Applying to a
directory that is not a playbook asks for confirmation on a terminal, and
`--yes` gives it (as for `DROP PLAYBOOK`); without a terminal and without
`--yes` it is refused before anything is written. `--dry-run` works as it does for a playbook and names the backups it would make.

## Commands

### Authentication preparation

Every launch (`run`, `start`, launcher dispatch) prepares the config directory's authentication before exec, in `auth.PrepareLaunchEnv`. "The config directory" is whatever the launch bound: the playbook's install directory, `start`'s path argument, or a caller-supplied `CPB_CONFIG_DIR` (see *Caller-supplied config directory*). The decision below is identical in every case -- a supplied directory is judged, synced and quarantined exactly as a playbook's own would be, which is what makes it usable as a private state directory. The decision, in order:

1. **Isolation wins.** If the nearest manifest walking up from the config directory has `isolated_login = true`, or `CPB_ISOLATED_LOGIN=true` is set: detach a symlinked `.credentials.json` (the target file survives), sync no account metadata, strip `CLAUDE_CODE_OAUTH_TOKEN` and the plan descriptors (`CLAUDE_CODE_SUBSCRIPTION_TYPE`, `CLAUDE_CODE_RATE_LIMIT_TIER`) from the child environment. A `CLAUDE_CODE_OAUTH_TOKEN` the playbook's own flattened `[env]` sets is honoured as its own token: injected, with the stored `claudeAiOauth` grant quarantined as on the token path. When the isolated playbook holds no login of its own (no grant in its store) and its block sets no token, the Anthropic account state in its `.claude.json` is removed as well: `oauthAccount`, `cachedGrowthBookFeatures`, `cachedGrowthBookFeaturesAt`, `cachedExperimentFeatures`, `cachedExperimentData`, `passesEligibilityCache`, `cachedExtraUsageDisabledReason`. Rationale: Claude Code enables its claude.ai-hosted tools (Artifact and friends) from those cached flags regardless of `ANTHROPIC_BASE_URL`, so a playbook routed to another backend that once ran as the global account keeps sending them; since Claude Code 2.1.265 the Artifact tool's schema is rejected by at least one such backend (GLM, `400` code `1210`) and every interactive turn fails. A playbook that logs in on its own regenerates the state, so nothing of value is lost; an unreadable `.claude.json` leaves it in place and is reported as an advisory warning. `auth status` shows the pending removal as `no login; stale account state, purged at launch`, judged as the launch will see it: a grant reached only through a symlinked shared store does not count, since isolation detaches that link first (`stale account state, purged at launch (the shared login is detached at launch)`). A store that cannot be read or parsed leaves the state in place with an advisory warning, since it may hold a login. The rewrite keeps the file's owner permissions masked to `0600`, never widening a read-only state file.
2. **Token active?** True when the flattened `[env]` sets `CLAUDE_CODE_OAUTH_TOKEN` to a non-empty value, else when the process environment carries it, else when `~/.config/claude-code/oauth-token` (overridable via `CPB_OAUTH_TOKEN_FILE`) is non-empty; false when the flattened `[env]` unsets it, regardless of the rest.
3. **Token path.** The plan descriptors (`CLAUDE_CODE_SUBSCRIPTION_TYPE`, `CLAUDE_CODE_RATE_LIMIT_TIER`) are appended from the global account's store only when the token IS the machine-global one, read from the token file by this launch, and only for the descriptors the shell did not already export: an explicit export is the pilot's deliberate act and outranks what is inferred from disk (a Team seat needs exactly that, its real `subscriptionType` is not a value the picker accepts). A token the flattened `[env]` block supplies (`playbook-token`) belongs to some other account, so the global descriptors are neither appended nor inherited (the block may set its own); a token inherited from the shell is of unknown provenance, so nothing is appended and whatever descriptors the shell exported beside it pass through unchanged. Then: quarantine the playbook's stored `claudeAiOauth` grant (other keys such as `mcpOAuth` survive; a symlinked store is detached, never written through; the global store is never quarantined — identity is decided with symlinks resolved and `os.SameFile` for the directory and for the credentials file itself, so `start <symlink-to-~/.claude>`, a linked registration of it, or a config directory whose store is the target of a symlinked `~/.claude/.credentials.json` are all recognised as the global store), sync non-secret account metadata into `.claude.json` so an interactive start presents as logged in, and inject the token (replacing any inherited entry); the descriptor rule above then applies. Rationale: under token auth Claude Code never refreshes a stored grant, and its 401-recovery path adopts a differing stored `accessToken` over the environment token; an expired stored grant would replace a working token with a dead one.
4. **No-token path.** `SyncCredentials`: the playbook's `.credentials.json` becomes a symlink to the global `~/.claude/.credentials.json`, so `/login` in any playbook is visible to all, and Claude Code refreshes the shared grant itself. A target that is the global directory by identity, or whose store is the global file itself, is left untouched. A regular file there (Claude Code writes its store by rename, so a refresh or `/login` in a shared playbook leaves one) is judged before the link replaces it. A file with no account grant never overwrites the global store. A grant-bearing file becomes the machine's login when there is none, and is copied over it when it is the machine's own account and newer. Any other login, or one that cannot be confirmed as the machine's account, is set aside: renamed to `.credentials.json.cpb-own-<YYYY-MM-DD-HH_MM_SS>`, its account state backed up to `.claude.json.cpb-backup-<…>` and removed, with one stderr line naming the kept file and `ALTER PLAYBOOK <name> SET ISOLATED LOGIN` as the way to keep that account there. Nothing is deleted.

Before any of this, a launch refuses while a manifest on the config directory's way up cannot be read, sandboxed or not: it may ask for an isolated login or a sandbox (`<error>. cpb does not launch over a manifest it cannot read: it may ask for an isolated login or a sandbox`). Every preparation failure is advisory (a warning, then launch) except an env set that cannot be resolved, which refuses the launch before step 1. Raw `claude` invocations bypass this entirely.

### Caller-supplied config directory

A playbook directory serves two roles at once: it is the playbook's **content** (`CLAUDE.md`, `settings.json`, `hooks/`, `skills/`) and the sink for Claude Code's **state** (`.claude.json`, `sessions/`, `projects/`, `history.jsonl`, `cache/`).

Several sessions that share one playbook's content while each keeps its own state are **already expressible without this**, two ways: run the same playbook from different working directories, which partitions transcripts into `projects/<cwd>/` while sharing settings and login; or install the playbook more than once, which separates everything at the cost of a copy per install. Either is the ordinary answer.

What this variable adds is narrower: a caller may supply a config directory **it built itself**, without registering a playbook for it. That is the case neither of the above covers — a supervisor that composes its own directory (playbook content reached however it likes, state real and local) and wants a playbook launch to bind it. It is a seam for such a consumer, with none in this repository; unset, which is the default, nothing about a launch changes.

```bash
CPB_CONFIG_DIR=/path/to/record  cpb run <name>
CPB_CONFIG_DIR=/path/to/record  <launcher>
```

`run` and launcher dispatch then bind the child's `CLAUDE_CONFIG_DIR` from that value instead of from the install directory. Everything downstream operates on it uniformly -- the authentication decision, credential sync and quarantine, the governing-manifest lookup -- with no special case for where the directory came from. The value is expanded (a leading `~`) and must be **absolute**: the child resolves `CLAUDE_CONFIG_DIR` against its own working directory, so a relative request would name different directories from different places, and resolving it here would hide that (`CPB_CONFIG_DIR "<value>" must be an absolute or ~-prefixed path`). An empty value means unset. Existence is not checked and the directory is **not created**: provisioning it, and any playbook content it must expose, belongs to the caller; cpb binds what it is given and has no opinion about what is inside. A `settings.json` hook command spelled `$CLAUDE_CONFIG_DIR/hooks/...` therefore errors at startup if the supplied directory has no `hooks/` -- the caller's seeding problem, not cpb's.

A **bare inherited `CLAUDE_CONFIG_DIR` is discarded**. The opt-in is deliberately its own variable: a value left exported in a shell would otherwise silently redirect every launcher on the machine, running a playbook's content against an unrelated directory. Keeping the names separate also keeps the two halves distinct -- `CPB_CONFIG_DIR` is what the caller *requests*, `CLAUDE_CONFIG_DIR` is what the child *receives*.

`start` **names its own config directory**, so an override has nothing to override and the path argument wins: the command line outranks the environment. It says so, because the variable is consumed either way and silence would be indistinguishable from having been honoured: `CPB_CONFIG_DIR ignored: start uses the directory you named, <path>` on stderr — printed before a `--sandbox-host` forward as well, since the remote never receives the variable and names the path as given there (resolving it locally would print a directory that is not the one being used) — omitted when the override names that same directory, since then nothing was ignored. A **malformed** override is reported there too (`Warning: <the refusal> (start uses the directory you named, <path>)`) rather than passed over: a caller whose value is wrong should hear about it even on the one launch shape that would not have used it. Both are warnings, not refusals — `start`'s own path is valid and the session runs.

`CPB_CONFIG_DIR` is a **reserved key**, like `CLAUDE_CONFIG_DIR`: a manifest `[env]` block, an env set, `--env` and `--env-file` all refuse it (`CPB_CONFIG_DIR is managed by cpb and cannot be overridden`). Declaring it could never redirect the launch that declares it — the request is read from the process environment before any layer is applied — but it would place the variable in the child's environment, from where it *would* redirect a further launch made inside the session. A key that cannot do the thing it names, yet silently affects the next launch, is refused outright.

The override is **consumed**: the same step that binds `CLAUDE_CONFIG_DIR` removes `CPB_CONFIG_DIR`, on every path and whether or not this launch used it. It happens **after** the authentication branch and after every override layer, so a layer that names the key cannot reintroduce it; the reservation above and this binding are the same two-layer arrangement `CLAUDE_CONFIG_DIR` already has, where the binding is what makes the refusal unnecessary to trust. Every other subprocess the tool starts with an explicitly chosen `CLAUDE_CONFIG_DIR` — the declared `[update] migrate` step, the `claude auth status` probe — drops the variable too: that choice is authoritative, and a migration script that called `cpb run` would otherwise be redirected. Left in place it would reach `claude` and every process below, so an agent inside the session running `cpb run <other>` would have that launch redirected into this one's directory -- the wrong playbook writing into the wrong state. Stripping is an omission while building the child's environment array, not a mutation with a matching restore: a process environment is copied at spawn, is private to that process, and dies with it, so no signal, kill or crash can leave it half-done, and the tool cannot alter its parent's environment at all. One consequence worth stating: an override `export`ed interactively persists in that shell for later commands, since nothing can un-export a parent's variable, so every launch from that shell honours it -- the pilot's explicit request, not a leak.

A **sandboxed launch with an override is refused** (`CPB_CONFIG_DIR and a sandboxed launch together are not supported: a sandbox mounts the config directory, and content reached through symlinks dangles inside it. Launch on the host, or unset CPB_CONFIG_DIR`), for `--sandbox`, `--sandbox-host`, and a manifest's `[sandbox] always = true` alike. The backend mounts directories, so a supplied directory whose playbook content is reached through symlinks -- the shape a caller provisioning its own necessarily produces -- dangles inside, the same failure a shared login's symlinked credentials have. The refusal precedes every backend call.

**A manifest cannot redirect it**: `[env.set] CLAUDE_CONFIG_DIR` is a hard error. A shared playbook repository must never be able to redirect a config directory; only the caller's own environment has that power, which is a different trust model.

### `cpb run [launch-flags] <name> [launch-flags] [claude-flags...]`

Runs Claude Code using the named playbook. Any flags after the name are forwarded to `claude` unchanged, except a leading run of **launch flags**, which add one-off environment layers for this launch only.

```bash
cpb run experiment
cpb run sre
cpb run sre --model claude-opus-5
cpb run --env-set work sre                       # one launch with an env set
cpb run sre --env ANTHROPIC_MODEL=claude-opus-5      # flags may also follow the name
cpb run --block CLAUDE_CODE_OAUTH_TOKEN sre          # this launch uses the stored login
cpb run --env-file ./work.env sre -p "..."
```

**Launch flags** (each repeatable; value as the next argument or after `=`):

| Flag | Layer |
|------|-------|
| `--env-set NAME` | an existing env set (`<playbooks root>/.env-sets/NAME.toml`); a missing or invalid one refuses the launch |
| `--env KEY=VALUE` | set one variable |
| `--block KEY` | remove one variable |
| `--env-file PATH` | a dotenv-style file: `KEY=VALUE` per line, blank and `#` lines skipped, a leading `export ` tolerated, one pair of matching surrounding quotes stripped, later lines win; every line validated like a manifest entry |

Scanning: launch flags are recognised only as a **leading run**, before the name and again immediately after it; the first argument that is not a launch flag ends the scan and everything from there on is `claude`'s. Through a launcher (`sre --env K=V -p hi`) the same rule applies to the arguments after the command name. Keys and values follow the manifest rules (`CLAUDE_CONFIG_DIR` reserved, valid UTF-8, no NUL). The layers apply on top of the playbook's flattened `[env]` block in command-line order, are resolved against the same env sets, drive the token decision exactly as the block would (a one-off `--block CLAUDE_CODE_OAUTH_TOKEN` takes the stored-credentials path for this launch), and are never written to disk.

Equivalent to:
```bash
CLAUDE_CONFIG_DIR=~/.claude-playbooks/<name> claude [claude-flags...]
```

Flag parsing is disabled so arbitrary `claude` flags pass through. The global `--playbooks-dir` flag is extracted from the argument list before forwarding; launch flags are extracted from the leading positions described above.

**Resume guard.** There is no resume command: `--resume` and `--continue` are `claude`'s flags and pass through. Before `claude` starts, a local launch refuses to resume a session that is live, or one that ran in another folder; the rules and messages are in *SHOW SESSIONS*.

**Sandboxed launch.** With `--sandbox`, the playbook's Claude Code runs inside a Docker Sandbox: a microVM with its own kernel, filesystem, Docker daemon and network stack, driven through the `sbx` CLI (v0.38.0 or newer, installed and logged in by the pilot; `run` refuses with an install hint when it is not on PATH, and surfaces `sbx`'s own "not authenticated" failure). Only what is mounted crosses the boundary: the working directory, the playbook's own root directory (its `CLAUDE_CONFIG_DIR`, so the sandboxed Claude Code reads and writes the same playbook state), the manifest's `[sandbox].mounts`, and `--mount` entries, all at their host absolute paths. The host's `~/.claude`, the rest of the home directory, the shell environment and every other playbook stay outside.

```bash
cpb run --sandbox sre                                  # cwd is the workdir
cpb run --sandbox --workdir ~/proj sre -p "..."
cpb run --sandbox --sandbox-fresh --clone --workdir ~/untrusted-repo sre   # new sandbox on a private clone; host tree untouched
cpb run --sandbox --mount ~/shared-libs:ro sre
cpb run --sandbox --sandbox-fresh sre                  # recreate the sandbox first
```

| Flag | Effect |
|------|--------|
| `--sandbox`, `--sandbox=BACKEND` | launch inside the playbook's sandbox `cpb-<name>` (characters `sbx` does not accept folded to `-`), created on first use (without the shared skills store) and reused afterwards, so tools installed inside and the sandbox's own state persist between launches. `--sandbox=BACKEND` names the backend (`sbx`, the only one; anything else refuses the launch) |
| `--sandbox-host USER@HOST` | run the sandboxed launch on that machine (below); implies `--sandbox` |
| `--no-sandbox` | launch on the host although the manifest says `[sandbox] always = true`; prints `Sandbox off for this launch: playbook "<name>" is always sandboxed by its manifest` on stderr so the override never passes silently. Together with `--sandbox` it is an error. Without `always` there is nothing to override: the flag is accepted, nothing is printed, the launch is an ordinary host launch |
| `--sandbox-fresh` | remove the existing sandbox and create it again before launching |
| `--clone` | at creation, mount the working directory read-only and let the agent work on a private git clone inside the sandbox (`sbx --clone`; its commits are reachable from the host through the `sandbox-cpb-<name>` remote). Creation-time only, as in `sbx`: an existing sandbox is reused as it was created, so switching an existing one to clone mode takes `--sandbox-fresh` |
| `--workdir PATH` | the directory mounted and entered; default `[sandbox].workdir`, else the invocation directory. Must exist |
| `--mount PATH[:ro]` | one more host path to mount (repeatable), on top of `[sandbox].mounts` |

The flags belong to the same leading runs as the launch flags, in any order among them; `--sandbox-fresh`, `--clone`, `--workdir` and `--mount` without a sandbox (no flag and no `always`, or `--no-sandbox`) refuse the launch. `--workdir` and `--mount` values may be `~`-prefixed.

**Always sandboxed.** A manifest with `[sandbox] always = true` sandboxes every launch of the playbook without a flag: `run`, and launcher dispatch (`sre -p "..."`). `--no-sandbox` overrides it for one launch, loudly (above); there is no environment variable or setting that overrides it silently. `CREATE PLAYBOOK … SANDBOX` writes the key together with `isolated_login = true`, because the machine login cannot follow a playbook into its sandbox (below). The backend comes from `--sandbox=BACKEND`, else `[sandbox].backend`, else `sbx`; the launch logic talks to it through one seam (list, create, allow network, shell, attach, remove), so a further backend is an implementation, not a redesign. `[sandbox]` is install-local, like `[env]`: `CREATE PLAYBOOK … FROM` never adopts a source-shipped block (it would mount host paths or widen the network), dropping it with a note, and `update` preserves the live one.

Procedure: resolve the environment exactly as an unsandboxed launch would (DEFAULTS, the env sets, the block, launch flags, the authentication decision including quarantine and identity purge of the playbook's own store, which the sandbox then reads through the mount; the config directory is made absolute first, so a relative `--playbooks-dir` cannot leak a relative `CLAUDE_CONFIG_DIR`), then reduce it to the variables the sandbox receives: the keys the effective block and launch flags **set**, plus `CLAUDE_CODE_OAUTH_TOKEN`, `CLAUDE_CODE_SUBSCRIPTION_TYPE`, `CLAUDE_CODE_RATE_LIMIT_TIER` and `CLAUDE_CONFIG_DIR` when the launch decided them. Nothing else of the host environment enters the sandbox. **A shared login does not enter it either:** when the store is still a symlink after preparation and the environment carries no `CLAUDE_CODE_OAUTH_TOKEN` (the shared-login path; a token launch authenticates with the token, and the link it may leave behind, one to a grantless store, holds nothing to adopt), its target `~/.claude` is not mounted and `sbx` mounts directories only, so the link would dangle inside and Claude Code would be logged out. A missing store on a non-isolated target (a fresh directory or playbook on a machine without a login) is the same shape with nothing to link to yet, and is treated the same way. The link is re-pointed (or created) instead, at a **sandbox-local** file: `<sandbox home>/.cpb-logins/<sandbox name>/.credentials.json` (`/home/agent/...` under `sbx`), whose directory the attach command creates inside before `claude` starts. `/login` inside writes through the link into the sandbox, where the grant persists with the sandbox (and goes with `--sandbox-fresh`) and never reaches the host. The account state the host sync copied in (`oauthAccount`, cached flags) is purged as for an isolated playbook, and `Shared login stays on the host: playbook "<name>" authenticates on its own inside the sandbox (run /login once there; the login lives in the sandbox)` is printed on stderr. When the session returns, or the launch is refused anywhere before the attach (a mount check, a failed create, a key the proxy could not register), the launch runs the credential sync again, which replaces any link that is not the shared one with the shared one and copies nothing, so the host sees the shared link as before; the next sandboxed launch re-points it again and the sandbox login is still there. While a sandboxed session is live (or after a launch that never returned) the host store link dangles, which every host command tolerates: `update` preserves it as it is, `DROP PLAYBOOK` removes it, `auth status` shows `shared-login` with the sandbox target and `no login`, and the next host launch repairs it. A sandboxed **token** launch that finds the store linked to a sandbox login detaches the link first (on the host the quarantine saw a dangling link and nothing to detach, but inside it would resolve to the earlier grant, which Claude Code's 401 recovery adopts over a token), exactly as the quarantine detaches a shared link with a grant. The machine's own config directory (`~/.claude`, by identity) is never sandboxed: it holds the machine login as a regular store, and mounting it would hand that login to the sandbox; `start --sandbox ~/.claude` is refused before any preparation. Nor may any mount contain it: the working directory, the root, and every extra mount (read-only or not) are refused when the machine config directory, the machine credentials store at its resolved location (the store may be a symlink into another directory), the long-lived token file, or the registry's env sets directory (`<playbooks root>/.env-sets`, the secret store proxy injection keeps out of the sandbox) exists below them, decided by filesystem identity along the credential's ancestors (so `/`, a differently cased spelling, or any alias of the directory counts), so `--workdir ~`, `start --sandbox ~`, `--mount ~:ro` and `--mount /:ro` all refuse (`sandbox mount <path> contains the machine's Claude config directory <dir>: the machine login would enter the sandbox. Mount a narrower directory`). Every refusal happens before the backend is called. The login must never land as a regular file on the mount: the host sync promotes a newer regular grant-bearing store into the machine store, which is exactly what a sandbox login must not do. The machine login is never read, copied or moved. Every path that crosses into the sandbox is absolute and symlink-resolved (the target is what exists at that path inside): the working directory, the playbook root, the extra mounts (a missing one refuses the launch), and `CLAUDE_CONFIG_DIR` itself, which inside the sandbox names the resolved config directory, so a linked registry entry (`CREATE PLAYBOOK … LINK`) mounts and addresses its target. The root is not mounted again when it lies inside the working directory, and a config directory outside the mounted root is mounted on its own. `sbx ls -q` decides whether `cpb-<name>` exists; `--sandbox-fresh` removes it (`sbx rm -f`); a sandbox that is reused is inspected first (`sbx ls --json`, its `workspaces`): mounts are creation-time, so the existing mounts must cover every path this launch needs, by containment (a working directory below an existing mount is covered; a different one would not exist inside) and pass the same guards as new mounts (the machine-login and env-sets guard, and in proxy mode the manifest-key guard over the existing, possibly wider, mounts), else the launch refuses and names `--sandbox-fresh` (`sandbox cpb-<name> was created with mounts ...; this launch also needs ..., which a reused sandbox cannot add. Recreate it with --sandbox-fresh` / `sandbox cpb-<name> mounts <path>, which contains <what>: the machine login would be inside. Recreate it with --sandbox-fresh`); a missing sandbox is created with `sbx create --name cpb-<name> [--clone] claude <workdir> <playbook root> <extra mounts...>`. A service on this machine is a special case: inside the sandbox `localhost` is the sandbox itself, so an `ANTHROPIC_BASE_URL` at `localhost`, `127.0.0.1` or `::1` is rewritten for the sandbox to the backend's host alias (`host.docker.internal` under `sbx`), scheme, port and path kept, with `ANTHROPIC_BASE_URL names this machine: inside the sandbox it is <url>` on stderr; and the backend's policy and secret store know that service as `localhost` whatever name the sandbox used (verified on `sbx` 0.38.0: an allow rule or a secret for `host.docker.internal` never matches, one for `localhost` does), so allow rules and secret registrations for the host alias, `localhost`, `127.0.0.1` or `::1` are spelled `localhost`, in `[sandbox].allow_net` too. After creation, and only then, every host in `[sandbox].allow_net` and the host of `ANTHROPIC_BASE_URL` (when the effective environment sets one) is allowed for that sandbox (`sbx policy allow network --sandbox cpb-<name> <host>`; a failure is a warning, the launch continues), and a `[sandbox].claude_version` pin installs that Claude Code inside the sandbox through the official installer (a failure is a warning; the image's own version runs). **Remote sandbox host.** `sbx` drives only the machine it runs on, so "the host the sandbox runs on" is the host where `cpb` runs. With `--sandbox-host USER@HOST` (or the manifest's `[sandbox] host`, used whenever the launch is sandboxed) the whole launch is forwarded there over ssh, as the same subcommand rebuilt from what the launch parser consumed (never from the raw text, so `claude`'s own arguments travel verbatim and nothing in them is mistaken for a flag): `ssh [-t] -- USER@HOST 'env CPB_CMD=<base64> sh -c '"'"'eval "$(printf %s "$CPB_CMD" | base64 --decode)"'"'"''`, where the decoded text is `PATH="$HOME/.local/bin:/opt/homebrew/bin:/usr/local/bin:$PATH" exec cpb run [--playbooks-dir=D] --sandbox[=BACKEND] [--sandbox-fresh] [--clone] [--workdir=W] [--mount=M]... [--env-set=P | --env=K=V | --block=K]... NAME [claude args...]` (`start` likewise, with `--delete` before the path). The transport exists because ssh hands the text to the remote user's login shell, whose family is unknown (tcsh breaks POSIX single quotes on a newline and expands `!`) and whose non-interactive PATH lacks `~/.local/bin`: the outer text is plain words every shell family passes through untouched, only POSIX `sh` parses the command, `sh`'s stdin is the ssh channel and its exit status is `cpb`'s (`exec`), and the PATH is widened inside with the installer's and the package managers' directories. `base64 --decode` is GNU and BSD alike, every value flag in its inline form so a value that looks like a flag stays a value there too (a value that is exactly `--` travels as two words, since a standalone `--` is where the registry scan stops on both sides; every `--workdir` occurrence is forwarded in order, the last winning there as here), every argument single-quoted into the one command string ssh hands the remote shell, the launch flags forwarded as typed and unevaluated (no env file is read for a remote launch, whether the host comes from the flag or from the manifest: the flags are evaluated only once the launch is known to be local), `-t` only when this process has a terminal on both ends, ssh's own options ended by `--` before the destination. The host is validated like the manifest key (no whitespace, no slash, no leading dash: `--sandbox-host "<value>" must be an ssh destination such as user@host`). `--sandbox` is always present, so the remote launch is sandboxed there whatever its manifest says; the remote host's own registry, env sets, manifest and secrets apply, and the sandbox, its login and its proxy mappings live there. Requirements on the remote: `cpb` in `~/.local/bin` (the installer's default), `/opt/homebrew/bin`, `/usr/local/bin`, or on the PATH an ssh session gets, a credential store `sbx` can read from a non-interactive ssh session (a Linux host with a headless keyring unlocked at boot; a macOS host keeps the Hub session in the login Keychain, which an ssh session cannot read until `security unlock-keychain` runs, so `sbx` fails there with `cannot prompt the user for password`), the playbook installed there (for `start`, the path is a path there; a directory manifest naming a host forwards a sandboxed `start` the same way, and `--delete` then acts there), `sbx` logged in. With the flag, the playbook need not be registered here at all. A `--playbooks-dir` travels as given, a path on that host. Refused: `--env-file` (a local file, refused by name; it is never opened), and `--sandbox-host` together with `--no-sandbox`; `--no-sandbox` on a playbook whose manifest names a host runs it here, on this host. Printed first: `Sandbox on USER@HOST: cpb run ...`. The exit status is the remote launch's.

**Secrets stay on the host.** Before attaching, every backend API key the environment carries (`ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_API_KEY`) is registered with the backend as a proxy-injected secret scoped to the sandbox, for the endpoint host (the host of `ANTHROPIC_BASE_URL`, else `api.anthropic.com`): `sbx secret set-custom --host <host> --env <KEY> --value <value> --placeholder <sandbox>-<KEY> --sandbox <sandbox>`. Inside, the variable holds the placeholder; every request from the sandbox passes through the host-side proxy, which swaps the real value into the request headers only on its way to that host (verified on sbx 0.38.0 for `Authorization: Bearer` and `x-api-key`; a request to any other host carries the placeholder). The key never enters the sandbox's filesystem or environment. That holds only for keys that live outside the mounts, which is where env sets live (`<playbooks root>/.env-sets/`); a key set in the target's own `[env.set]` sits in a `.playbook` on the mounted root (the config directory's, or the install root's for a `subdir` install, or one above a `start` directory that the working-directory mount carries; every manifest walking up from the config directory whose directory lies under one of this launch's mounts is checked, and one that cannot be read refuses too, `cannot check <path> for keys the sandbox would mount: <error> ...`, since it may hold a key), so a proxy-mode launch refuses it before any backend call (`<KEY> is set in <path>/.playbook, which the sandbox mounts: the key would be readable inside. Move it to an env set, which lives outside the mount (cpb CREATE ENV <set> SET <KEY>=... AS PLAINTEXT; cpb ALTER PLAYBOOK <playbook> ADD ENV <set> UNSET VAR <KEY>), or set [sandbox] secrets = "env" to accept the exposure`). Files the pilot places on a mount themselves (`--env-file` under the working directory, a `settings.json` `env`) are the pilot's own exposure. The placeholder is deterministic, so registering it again on the next launch updates the value in place (rotation follows the env set). A mapping registered by an earlier launch for a key the environment no longer carries is **revoked** on the next launch (`sbx secret ls --sandbox` lists what is registered; the placeholder is re-registered as its own value for the current endpoint host, so the proxy substitutes it with itself, `Secret <KEY> revoked at the proxy: it is no longer in the environment`), since anyone inside could otherwise keep sending the predictable placeholder. One mapping exists per key per sandbox: two sessions of one sandbox launched with different one-off keys share it, and the later launch's key serves both (a known limitation; the placeholder must not change, or rotation would not work). `sbx` 0.38.0 cannot delete a custom secret; it persists in the backend's store after the sandbox or the playbook is gone, and the next launch of a sandbox of that name overwrites it. The value travels on `sbx`'s argument list for the moment of the call. A registration that fails **refuses the launch**: `<KEY> could not be registered at the sandbox proxy for <host> (<the backend's error, the value replaced by <redacted>>), so the launch stops: the key would otherwise enter the sandbox as a plain value. Retry, or set [sandbox] secrets = "env" to pass keys into the sandbox as plain variables`. Nothing is attached. `[sandbox] secrets = "env"` is the only way a key enters as a plain value. The scrub replaces the value exactly as it is; a backend error that echoed it transformed (encoded, truncated) would pass through, so a backend must never echo a secret. The subscription token (`CLAUDE_CODE_OAUTH_TOKEN`) is not injected: Claude Code checks its shape locally, and a placeholder would not pass. Sandboxes are created with `--no-share-skills` unless `[sandbox] share_skills = true`: `sbx` otherwise mounts its shared skills store read-write into every sandbox, and a sandbox could plant a skill a later sandbox runs. Creation-time choices cannot be read back from `sbx`, so creation writes a marker inside (`~/.cpb-sandbox`, `skills=private` or `skills=shared`) and a reused sandbox must present the marker this launch expects. One without a marker is never attached to, since cpb cannot prove it made it (`a sandbox named <name> exists but has no cpb marker: remove it (sbx rm -f <name>) or launch with --sandbox-fresh`); one with another value was created under a different `share_skills` (`sandbox <name> was created with other creation-time settings (found "...", this launch needs "..."): share_skills changed. Recreate it with --sandbox-fresh`).

The launch then attaches with `sbx exec -i [-t] -e KEY=VALUE... cpb-<name> bash -lc 'cd <workdir> && exec claude <args>'`; `-t` (a pty) is requested only when cpb's own stdin and stdout are terminals (a real terminal test; `/dev/null` is a character device but not a terminal), since `sbx exec -t` without one produces no output and exits 0, which would make a piped `-p` launch silently do nothing. The environment and the arguments travel as exec arguments (each argument single-quoted), never through a file inside the sandbox. `claude`'s exit status is preserved. The sandbox's network policy (deny-by-default with the pilot's preset) applies to everything not allowed explicitly; a sandboxed playbook that cannot reach a service is a policy question first (`sbx policy log`).

Launcher dispatch forwards to `run`, so `sre --sandbox -p "..."` launches inside `cpb-sre`, and a playbook with `always` is sandboxed through its launcher. `start` sandboxes a directory the same way (below).

**Errors:**
- Playbook not found → `unknown playbook "experiment". `cpb SHOW PLAYBOOKS` lists them`
- Launch flag without a value → `flag needs an argument: --env`
- `--env` without `=` → `--env expects KEY=VALUE, got "X"`; `--block` with `=` → `--block expects a variable name, got "K=V"`
- Invalid key, reserved key, or bad value → the manifest's `[env]` errors (`invalid environment variable name "x"`, `CLAUDE_CONFIG_DIR is managed by cpb and cannot be overridden`, ...)
- `--env-file` problems → `--env-file: <path>:<line>: expected KEY=VALUE` or `<path>:<line>: invalid environment variable name (not shown; the line may hold a secret)`: neither the line nor a rejected key is echoed, since a secret containing `=` splits into a bogus key; the reserved-key and value errors are the manifest's, prefixed with `<path>:<line>`
- `--env-set` missing or broken → the launch refusal (`env set "x" not found in <dir> ...`)
- `claude` not on PATH → `'claude' command not found. Install Claude Code first: https://claude.ai/download`. **Reported after every input error above it in this list**: the pilot's own input is validated before the machine is inspected, so a mistyped flag names itself instead of sending someone to install an agent they may already have. Launch-flag values, a reserved or malformed key, an `--env-file`, and an env set that does not resolve are all decided first. Everything that MUTATES stays after this check — credential quarantine and sync — so a machine with no agent is never written to; the env-set resolution done here is a read-only pre-pass, the same resolution the authentication preparation performs again when the launch proceeds.  `start` has the same order.
- Sandbox flag without a sandbox → `--sandbox-fresh, --clone, --workdir and --mount apply to a sandboxed launch: add --sandbox`
- `--sandbox` with `--no-sandbox` → `--sandbox and --no-sandbox together: pick one`; `--sandbox-host` with `--no-sandbox` → `--sandbox-host and --no-sandbox together: pick one`
- `--sandbox-host` with `--env-file` → `--env-file names a local file: a launch on <host> cannot read it. Use an env set on that host, or --env KEY=VALUE`; `ssh` missing → `'ssh' not found; a sandbox on <host> is reached over ssh`
- Unknown backend (flag or manifest) → `unknown sandbox backend "tart" (available: sbx)`; `--sandbox=` → `flag needs a non-empty argument: --sandbox=BACKEND`
- The machine config directory → `<path> is the machine's Claude config directory: a sandbox would mount the machine login. Sandbox a playbook or another directory`
- A mount above the machine login → `sandbox mount <path> contains the machine's Claude config directory <dir>: the machine login would enter the sandbox. Mount a narrower directory` (or `... the machine's long-lived token file <file> ...`)
- `--workdir`/`--mount` without a value → `flag needs an argument: --workdir`; empty → `flag needs a non-empty argument: --mount`
- `sbx` not on PATH → `'sbx' (Docker Sandboxes) not found; install it (macOS: brew trust docker/tap && brew install docker/tap/sbx) and run 'sbx login' once, or launch without --sandbox`
- `sbx ls` fails (not logged in, daemon down) → `sbx is not ready (run 'sbx login' if it reports not authenticated): <sbx error>`
- Working directory missing → `sandbox working directory <path> is not a directory`
- A `[sandbox].mounts` or `--mount` path missing → `sandbox mount <path>: <error>`
- Sandbox creation or removal fails → `could not create sandbox cpb-<name>: <sbx error>` / `could not remove sandbox cpb-<name>: <sbx error>`

### `cpb start [launch-flags] <path> [claude-flags...]`

Starts an ad-hoc Claude Code session at any directory. Creates the directory if it doesn't exist. No playbook registration, no `.playbook` file, no discovery — just set `CLAUDE_CONFIG_DIR` and run. The throwaway-experiment command. It never appears in `SHOW PLAYBOOKS`; `CREATE PLAYBOOK … LINK` registers a directory that should.

**Sandboxed start.** `start` takes the same sandbox flags as `run` (`--sandbox[=BACKEND]`, `--no-sandbox`, `--sandbox-fresh`, `--clone`, `--workdir`, `--mount`), in the same leading positions as `--delete` and the launch flags. The sandbox is `cpbstart-<directory basename>` (the prefix differs from a registered playbook's `cpb-` before the first hyphen, so no playbook name can produce it; `cpb-start-<x>` would collide with playbooks `start-x` and `start_x`); the directory is the mounted config root; a `.playbook` in the directory supplies `[sandbox]` (`always` and the defaults). Everything else is as under `run`, the shared-login detach included. With `--sandbox`, `--delete` removes the sandbox when the session ends (a failure is a warning), then the directory; a launch refused before a session attached (the machine config directory, a mount carrying the machine login, a missing backend) removes nothing.

```bash
cpb start --sandbox --workdir ~/proj /tmp/scratch -p "..."
cpb start --sandbox --delete /tmp/scratch          # sandbox and directory gone afterwards
```

```bash
cpb start /tmp/scratch
cpb start /tmp/scratch --model claude-opus-5
cpb start /tmp/scratch --delete
cpb start --env-set glm /tmp/scratch             # launch flags go before the path
```

The launch flags of `run` (`--env-set`, `--env`, `--block`, `--env-file`) are accepted before the path, with the same semantics; env sets resolve from the root named by `--playbooks-dir` or `CPB_PLAYBOOKS_DIR`.

Equivalent to:
```bash
CLAUDE_CONFIG_DIR=/tmp/scratch claude [claude-flags...]
```

**Flags:**

| Flag | Description |
|------|-------------|
| `--delete` | Delete the directory when the session ends |
| `--env-set`, `--env`, `--block`, `--env-file` | One-off environment layers; see `run` |

All wrapper flags, `--delete` included, are recognised only as a **leading run** before the path and again immediately after it; the first other argument, or a literal `--`, ends the run and everything from there on is `claude`'s verbatim. A `--delete` that appears later -- after another `claude` argument, as the value of a `claude` flag such as `-p --delete`, or after `--` -- is forwarded, never treated as permission to remove the directory. `--` is never accepted as the path itself (`path required`).

`--delete` runs after `claude` exits regardless of exit code. If deletion fails, a warning is printed to stderr but the tool preserves `claude`'s exit code.

**Errors:**
- Path exists and is a file → `"/tmp/foo" is not a directory`
- Cannot create directory → `could not create "/tmp/foo": <reason>`
- No path given → `path required`
- `claude` not on PATH → same as `run`

### Launchers

Per-playbook commands are **launchers**: symlinks to the `cpb` binary placed in a PATH directory. `CREATE PLAYBOOK` registers one.

- **Multicall dispatch.** Invoked through a launcher, the binary sees the link's name in argv[0] and dispatches as `run <name>` (the busybox/git pattern). The launcher carries no state: the name resolves at invocation time against the live registry — playbook directory names first, then manifest `launcher` fields — so nothing goes stale on rename or move.
- **Launcher directory.** `--launcher-dir` / `CPB_LAUNCHER_DIR`, else the directory the binary was invoked from (on PATH by construction), falling back to `~/.local/bin` when that is unwritable.
- **Default root only.** Launcher mutations happen only when operating on the default playbooks root (`~/.claude-playbooks`). A symlink carries no root identity, so managing links on behalf of a custom `--playbooks-dir` root would corrupt the default registry's commands; under a custom root the tool prints a note and the `cpb --playbooks-dir <root> run <name>` form instead.
- **Reserved name.** `cpb` always means the CLI itself; it never dispatches and may never name a launcher.
- **Collisions and locking.** The registry is the ownership authority: a command name that already addresses another playbook (by directory name or manifest launcher) is a hard error before any mutation. Preflight-through-registration is serialized across concurrent processes by a flock in the user cache dir (`<cache>/cpb/registry.lock`).
- **Retirement rule.** `DROP PLAYBOOK` and `RENAME TO` remove a launcher named for the playbook going away (its name, its manifest launcher, or a name a rename leaves behind), receipt line included, printing `Removed launcher "x"`, unless another playbook still claims the name: by spelling in the registry, or by directory-entry identity on a case-insensitive filesystem (`cpb CREATE PLAYBOOK one LAUNCHER Foo` and `cpb CREATE PLAYBOOK two LAUNCHER foo` share one entry). A claimed launcher is kept outright (`Kept launcher "x" (still addresses playbook "y")`); when the registry cannot be scanned the launcher is kept with a warning, since ownership could not be verified. The rule rests on the launcher gate: the tool only ever writes launchers for the default registry root, so a name nobody in that registry claims serves nothing the tool made. A hand-made link named for the playbook goes with it; it would only fail loudly as stale afterwards.
- **Stale launchers fail loudly.** Invoking a launcher whose name no longer resolves errors with `unknown playbook "<name>" — this launcher no longer matches any playbook` and exit code 1, never a silent fall-through to the CLI overview.
- **Foreign files are never touched.** A file occupying a launcher name that is not a symlink to this binary is left alone; attempting to write over it degrades to a warning with manual instructions.

A playbook's launcher name is one alternate command name, stored as the `launcher` field of its `.playbook` manifest — no rc files, no separate registry. Dispatch resolves directory names first, then manifest launcher names; the name is materialized as a launcher command like the playbook's own name. `CREATE PLAYBOOK … LAUNCHER`, `RENAME TO … LAUNCHER` and `ALTER PLAYBOOK <name> LAUNCHER` all write the same field.

### `cpb play`

`cpb play <ref>` tries someone else's playbook: it fetches a recipe once,
checks it, shows exactly what it would do, asks, and runs it as a throwaway
playbook that is removed when the session ends; sandboxed where a sandbox is
available. `--keep` keeps it as a playbook of your own instead. The guide is
[Try someone else's playbook](docs/guides/play.md).

```
cpb play <ref> [--yes] [--trust-endpoint <host>|TLS]... [--trust-secret <ref>]... [--env-set <set>]...
               [--sandbox[=sbx] | --no-sandbox] [--sha256 <hex>] [-- <claude arguments>]
                                                       preview, confirm, run, remove
cpb play <ref> --check [--json] [--sha256 <hex>]      fetch and check: refusals and risks
cpb play <ref> --dry-run [--json] [--sha256 <hex>]    the plan against a throwaway playbook
cpb play <ref> --keep [--as <name>] [--dry-run [--json]] [the run's confirmation flags]
                                                       keep it as a playbook in your store; no session
cpb update <name> [--dry-run [--json]] [--yes] [--trust-…] [--sha256 <hex>]
                                                       fetch a kept playbook's recipe again
cpb play <dir> --check                                 a template directory, as the website's CI runs it
```

**`<ref>`:**

| Form | Fetched from | Pinned |
|---|---|---|
| a template name (`[a-z0-9][a-z0-9-]*`), e.g. `reviewer` | `https://raw.githubusercontent.com/ramazanpolat/claude-playbooks/<this cpb's tag>/site/p/<name>.cpb`; a dev build reads `main`, and says so | by the release |
| `https://…` | that URL | only by `--sha256` |
| `github:<owner>/<repo>/<path>.cpb@<ref>` | `https://raw.githubusercontent.com/<owner>/<repo>/<ref>/<path>.cpb`. The `@<ref>` is required. A branch is allowed and flagged ("not a release tag: this may change"); a tag or a commit is pinned. | a tag or commit |
| `./x.cpb`, `/abs/x.cpb`, `~/x.cpb`, and, with no `:`, a name ending in `.cpb` or holding a `/` (`x.cpb`, `dir/x`) | a local file, with every check | |

A template name that is not there fetches `index.txt` beside it, only then,
and suggests close names.

**The fetch:**
- https only, and at most 3 redirects, each https.
- No credentials in a URL, no cookies, the system proxy.
- 10 s to connect and 30 s in all.
- At most 64 KiB, as UTF-8 text with no NUL byte.
- `User-Agent: cpb/<version> (play)`.

The recipe is read **once**. Its sha256 is shown, and everything after uses
those bytes. `--sha256 <hex>` refuses any other recipe before anything is
shown.

**What a played recipe may hold.** A played recipe is name-less `ALTER
PLAYBOOK` statements: a recipe. These clauses are allowed:
- `ADD MARKETPLACE`, `ADD PLUGIN`, `SET AGENT`;
- `ADD MCP SERVER`;
- `ALLOW TOOL`, `DENY TOOL`;
- `SET STATUSLINE`;
- `SET MODEL`, `ADD MODEL`, `SET MODEL PICKER`;
- `ADD SKILL` from a git source;
- `SET VAR` (not a credential), `SET VAR … FROM '<ref>'`, `BLOCK VAR`;
- `SET ISOLATED LOGIN`, `NO LAUNCHER`.

Refused, each with its line and reason:
- **anything outside the one playbook `play` makes:** `INCLUDE`, `USE
  PLAYBOOK`, an env set or `DEFAULTS` statement, a named or created playbook;
- **`USE ENV` and `ADD ENV`:** they would attach your env sets, and your
  keys;
- **a credential-looking literal, even `AS PLAINTEXT`:** a shared recipe
  carries no secret, only a reference. This is the grammar's own rule, so a
  value that cannot be a secret passes: empty, an integer, `true` or
  `false` (`MAX_THINKING_TOKENS=8000`);
- **an MCP server URL carrying credentials** (`https://user:token@…`);
- **a local directory source** for a marketplace or a skill, and a skill from
  `http://` or `file://`;
- **`UNSET ISOLATED LOGIN`, `RENAME TO`, `LAUNCHER`, `NO LAUNCHER`:** play decides
  these;
- **`DROP …` and `UNSET …`:** nothing to undo on a new playbook.

**Risks.** The preview marks each risky clause with a code. The codes are a
closed set: `runs_program`, `third_party_code`, `sends_data`,
`acts_without_asking`, `fetches_code`, `endpoint_change`, `tls_or_proxy`,
`telemetry_export`, `uses_secret`, `no_sandbox`.

Three kinds need a **typed confirmation** before a recipe runs (`!!` in the
preview, `confirm` in JSON):
- **the model endpoint** (`ANTHROPIC_BASE_URL` and the Bedrock and Vertex
  base URLs, unless the host is Anthropic's over https;
  `CLAUDE_CODE_USE_BEDROCK`, `CLAUDE_CODE_USE_VERTEX`): the host is typed;
- **a proxy** (`HTTP(S)_PROXY`, `ALL_PROXY`, any case): its host is typed;
  **a TLS change** (`NODE_EXTRA_CA_CERTS`, `SSL_CERT_FILE`, `SSL_CERT_DIR`,
  `NODE_TLS_REJECT_UNAUTHORIZED`): the word `TLS` is typed;
- **each secret reference**: the reference is typed, and the preview shows
  where its value goes, the destination host included.

Any other `*_URL`, `*_HOST`, `*_ENDPOINT` or `*_BASE_URL` is flagged
`sends_data`, not typed.

A recipe that moves the model endpoint is planned with `ISOLATED LOGIN` and
a `BLOCK VAR` of your credentials: `ANTHROPIC_API_KEY`,
`ANTHROPIC_AUTH_TOKEN`, `CLAUDE_CODE_OAUTH_TOKEN`, `AWS_ACCESS_KEY_ID`,
`AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN`, `AWS_PROFILE`,
`GOOGLE_APPLICATION_CREDENTIALS`, and every credential-looking variable the
shell exports, by name.

**A wide `ALLOW TOOL`** is flagged `acts_without_asking`, erring toward
flagging:
- `*`;
- `Bash`, `Bash(*)`;
- an interpreter or launcher with a wildcard, or alone: `sh`, `bash`,
  `zsh`, `fish`, `dash`, `python`, `python3`, `node`, `deno`, `bun`, `ruby`,
  `perl`, `php`, `lua`, `eval`, `exec`, `sudo`, `su`, `env`, `xargs`,
  `npx`, `uvx`. A path counts too (`/bin/sh *`);
- a tool alone or with a bare wildcard: `curl`, `wget`, `ssh`, `scp`,
  `rsync`, `nc`, `ncat`, `rm`, `docker`, `kubectl`, `pip`, `npm`
  (`Bash(kubectl *)`). With a subcommand it is narrow (`Bash(kubectl get *)`);
- `git push`;
- `Write`, `Edit`, `MultiEdit`, `NotebookEdit` or `Read` on everything,
  `/**`, `~/**`, `/*`, `~/*`, `/` or `~`;
- `WebFetch` with no domain, or `domain:*`.

An exact command line such as `Bash(npm test)` is narrow.

**The plan.** `--dry-run` plans the exact bytes against a **throwaway
store**, a fresh temp directory with only your secret helper setting copied.
So your `DEFAULTS` and env sets never layer into a played recipe, and
nothing is written to your store. The plan is `APPLY`'s, for two files:
- a setup file: `CREATE PLAYBOOK IF NOT EXISTS play-<name>-<6 hex> NO
  LAUNCHER`, with `ISOLATED LOGIN` and the `BLOCK VAR` above when
  the endpoint moves;
- the recipe.

**`--json`** prints the `APPLY --dry-run --json` object with a `"play"`
block added. It is a top-level field that `APPLY`'s own report does not have:

```
"play": {"ref", "kind", "url", "path", "sha256", "bytes", "pinned", "note",
         "header": {"title", "description", "needs", "create_with", "min_cpb"},
         "playbook", "endpoint", "refused": [{"line", "what", "reason"}],
         "risks": [{"code", "line", "clause", "detail", "confirm"}],
         "sandbox": {"backend", "note"},
         "keep": true, "update": {"from_sha256", "unchanged", "tag_moved"}}
```

`--check --json` is the same object with no statements, and `sandbox`
`null`. `sandbox.backend` is `""` when the play runs on this machine. `keep`
and `update` are present only for `--keep` and `cpb update`.

**The header.** A recipe may open with `-- key: value` lines, one per line,
then a blank line. The keys:
- `title`, `description` and `needs`, shown in the preview;
- `create-with`: advisory create-time flags. `SANDBOX` makes the play
  sandboxed: refused where no backend is available, unless `--no-sandbox`;
  a kept playbook is created `SANDBOX`;
- `min-cpb`, as `X.Y.Z`: an older cpb refuses ("this recipe needs cpb X.Y.Z
  or later").

**A template directory.** `cpb play <dir> --check` checks every
`<name>.cpb`. Each needs a `title` and a `description`, no unknown header
key, and a valid `min-cpb`. It also checks that `index.txt` lists exactly
those names, sorted, one per line.

**Running.** `cpb play <ref>` follows these steps:

1. It shows the preview: the recipe, its risks, and the plan of the exact
   bytes.
2. It asks `Run this playbook? [y/N]`.
3. It asks for each typed confirmation in turn:
   - `Type the host this playbook will send your requests to (<host>):`;
   - `Type the proxy …`, or `Type TLS …`;
   - `Type the secret this playbook may read, on this machine, as you (<ref>):`.

   An exact match goes on; anything else stops with nothing written.
4. It applies the same bytes to the throwaway playbook (its report is shown
   only if it fails), runs the session (arguments after `--` go to
   `claude`), and removes everything afterwards.

**The sandbox.** A play runs sandboxed by default:
- **the backend:** `sbx` where it is installed. `--sandbox=sbx` names it, and
  refuses if it is not available;
- **`--no-sandbox`** runs it on this machine. The preview says "Sandbox off
  (--no-sandbox): this agent runs on your machine, as you.", and the plan
  carries the `no_sandbox` risk;
- **no backend here:** the preview says "No sandbox available here (sbx):
  this agent will run on your machine, as you.", with
  the same risk;
- **`create-with: SANDBOX`** is refused where no backend is available,
  unless `--no-sandbox` (and then the preview names the override);
- **a secret reference** cannot be resolved by a sandboxed launch, so a
  recipe with one is refused where a sandbox is available, unless
  `--no-sandbox`. With no backend at all it runs on this machine like any
  recipe.

The sandbox is created only after every confirmation, so a moved endpoint
that is not confirmed never reaches a sandbox (whose proxy would inject a
key for that host), and it is removed with the throwaway playbook.

**Without a terminal**, `--yes` answers the yes. It **never** confirms what
must be typed: each model-endpoint or proxy host needs `--trust-endpoint
<host>` (`TLS` for a TLS change), and each secret reference needs
`--trust-secret <ref>`. A missing one refuses, naming the flags. Without
`--yes`, a run off a terminal is refused.

**`--env-set <set>`** copies one of your env sets into the throwaway store and
attaches it (`USE ENV`, in the order given). It is how a key reaches a moved
endpoint: the keys it sets are left out of the credential `BLOCK`, and
nothing else of yours follows.

**Clean-up on every way out.**
- **`^C`** reaches Claude Code from the terminal, so cpb waits for it to
  exit.
- **A closed terminal (`SIGHUP`) or `kill <cpb>` (`SIGTERM`)** is passed on
  to the session. cpb itself stays alive until the throwaway store is
  removed.
- **A panic** still removes it.
- **A shared login that Claude Code refreshed** during the session is handed
  back to your machine login first, so a rotated refresh token is not lost
  with the directory.
- **A cpb killed outright (`SIGKILL`)** leaves its store, with a marker
  naming its pid. The next `play` sweeps it once that pid is gone and the
  marker is more than 24 hours old.

The resume line a session usually ends with is not printed, because its
playbook is gone.

**`--keep [--as <name>]`** shows the same preview and asks the same
confirmations, then builds the recipe as a playbook in **your** store, and
runs no session:
- `CREATE PLAYBOOK <name>`, with a launcher; `ISOLATED
  LOGIN` when the endpoint moves; `SANDBOX` when the header asks for it or
  `--sandbox` is given (`--no-sandbox` overrides the header). A recipe with a
  secret reference cannot be kept sandboxed;
- `<name>` is `--as`, or the template's or file's name. A name that exists
  is refused;
- your `DEFAULTS` apply to it like to any playbook, and the preview names
  them. When the endpoint moves, every key they carry is blocked in it ("will
  NOT follow it to <host>"), except those of an `--env-set` set (attached with
  `USE ENV`) and those the recipe sets itself;
- the exact bytes are kept as `<playbook>/.play/recipe.cpb`, and the
  manifest gains:

  ```toml
  [play]
  ref = "github:acme/agents/reviewer.cpb@v1.2.0"   # what cpb update resolves again; a local file's absolute path
  url = "https://raw.githubusercontent.com/acme/agents/v1.2.0/reviewer.cpb"   # absent for a local file
  sha256 = "3f1a…c9"
  played_at = "2026-10-01T18:58:00Z"
  ```

  `SHOW PLAYBOOK --json` shows it as `play` (see Output);
- if applying fails part-way, the playbook it created is dropped.

**Exit codes:** 0 when checked, planned, kept, updated or unchanged; 1 when
refused (a check, the header, `--sha256`, or a confirmation); 2 on a usage
error. A session's own exit code passes through.

### `cpb tui`

`cpb tui` is a terminal UI over this grammar. It only reads: it browses,
shows `SHOW CREATE`, copies statements and commands, and exports a `.cpb`.

**A front-end, not a second engine.**
- **Reads.** Everything on screen is a statement's `--json` output. cpb
  runs its own binary for each read, and the TUI parses that output and
  nothing else.
- **The statement behind each screen** is named on its last line, for
  example `reads: cpb SHOW SESSIONS --json`, so anything seen can be
  scripted.
- **What it can do.** Only what the grammar can. It changes no state. Its
  only write is the `.cpb` file `e` exports.

**Views** (`1`–`5`):

| View | Reads | Shows |
|---|---|---|
| Playbooks | `SHOW PLAYBOOKS --json`, `SHOW SESSIONS --json` | name, launcher, version, env sets, login kind (`shared`, `isolated`, `sandbox`), live-session count, model |
| a playbook (`enter`) | `SHOW PLAYBOOK`, `EXPLAIN PLAYBOOK --json` | tabs: Overview, Env, Vars (effective, with the layer each comes from), Plugins, MCP, Skills, Status line (history), Model (the picker), Sessions |
| Sessions | `SHOW SESSIONS --json` | pid, tty, kind, status, age, last active, model, folder; the footer says where past sessions are, `past sessions: <launcher> --resume` |
| Env sets | `SHOW ENVS --json` | the env sets: variables, `used_by`, default |
| Defaults | `SHOW DEFAULTS --json` | the env sets under every playbook, the secret helper |
| Log | none | what this session copied and exported |

**Keys:** `↑↓`/`jk` move, `enter` opens, `esc` goes back, `/` filters,
`r` re-reads, and `?` is help.
- `c` shows `SHOW CREATE … --skip-secrets` of the selection.
- `y` copies the statement behind the selection, with OSC 52, the
  terminal's own clipboard (`tea.SetClipboard`): `SHOW PLAYBOOK <n>`, a
  session's `resume` command (`cd '<cwd>' && <launcher> --resume <id>`), or
  the whole SHOW CREATE text.
- `e` writes `<name>.cpb` in the current folder: the same SHOW CREATE text.
  - If the file exists, the TUI asks first: `Replace <name>.cpb? Type y to
    replace; any other key keeps it.` The question comes before the folder,
    so a deep folder never hides it. Only a typed `y` replaces the file.
  - Both writes are atomic. The content goes to a temporary file in the
    same folder, then is linked to a new name, or renamed over an existing
    one.
  - So a failed write leaves the old file, and a symlink with that name is
    replaced rather than written through.
- `q` quits.

**Secrets.** No value is displayed, since cpb's `--json` has already
withheld it: a reference shows as `FROM '<ref>'`, a plaintext credential
as `(redacted, plaintext)`. The TUI has no reveal key, and `SHOW CREATE`
always runs with `--skip-secrets`.

**No resume in the TUI.** Sessions shows the live ones, and a live
session is not resumed. `y` copies the command that resumes one once it
ends. Past sessions open in Claude Code's picker: `<launcher> --resume`.

**Sessions refresh.** `SHOW SESSIONS` is re-read every 5 s, and only while
a screen that shows sessions is up. Everything else is re-read on `r`.

**The terminal.**
- **Start.** `cpb tui` needs a terminal on stdin and stdout. Off one, it
  exits 1 with `cpb tui needs a terminal; use cpb SHOW … --json for
  scripts`.
- **Given back as found.** It takes raw mode and the alternate screen, and
  restores both however it ends (see below), and around a resumed session:
  bubbletea's `ExecProcess` stops reading input before the session starts.
- **Resize.** It redraws on SIGWINCH.
- **Colour.** It uses no colour, only reverse video for the selection and
  dim for hints, and none of that with `NO_COLOR` set.
- **Width.** Below the full width, a table drops its least useful columns
  first, so the name and the folder stay.
- **No startup cost for other commands.** The TUI is a
  [bubbletea v2](https://github.com/charmbracelet/bubbletea) program,
  pinned exactly: `charm.land/bubbletea/v2` v2.0.10, `charm.land/bubbles/v2`
  v2.1.1 and `charm.land/lipgloss/v2` v2.0.6. Linking them costs other
  commands nothing, and two permanent tests keep it that way:
  - **No terminal I/O at startup** (`TestNoTerminalQueryAtStartup` and the
    arena check `tui-ok`). Under a pty that answers nothing, `SHOW
    PLAYBOOKS`, `SHOW SESSIONS`, a launcher-style `run` and bare `cpb` must write
    no terminal query (`ESC]`, `ESC[6n`, `ESC[c`, `ESC[>`), and must not
    stall. bubbletea v1 fails it: its package `init` queries the terminal's background colour in every command and waits up to 5 s for a reply.
  - **No slow package initializer** (`TestNoSlowPackageInit`). Under
    `GODEBUG=inittrace=1`, no package may take more than 5 ms to
    initialize.
  - **bubbles is held at v2.1.1 for that reason.** bubbles v2.2 and later
    require go-runewidth v0.0.27, whose `init` precomputes a width table
    for all 65,536 BMP runes: about 48 ms of CPU in **every** cpb process
    and launcher. v2.1.1 resolves go-runewidth to v0.0.24, whose init
    takes 0.2 ms. Bump bubbles only when its go-runewidth no longer does
    that; the test fails otherwise.
- **The terminal.** bubbletea restores raw mode and the alternate screen on
  `q`, a panic, SIGINT and SIGTERM. SIGHUP, which bubbletea leaves to the
  default action, is handled as a quit, so a closed terminal window also
  restores it.

**Planned:** the actions, each built as a statement, shown, run
through `APPLY --dry-run --json` for its plan, then applied on
confirmation.

### `cpb auth status [name...]`

Read-only view of how each playbook authenticates and what its stored login looks like. Nothing is written, no credential value is read into the output, no network call is made, no process is spawned unless `--claude` is given.

```bash
cpb auth status                 # every playbook, ~/.claude first
cpb auth status sre router      # named ones
cpb auth status --json
cpb auth status --claude        # add 'claude auth status --json' per directory
```

**Columns:**

| Column | Meaning |
|--------|---------|
| `MODE` | How a launch would authenticate, decided exactly as `run` decides it: `token` (machine-global long-lived token injected), `playbook-token` (a token the manifest or an env set sets), `shared-login` (stored login, shared store; `shared-login (token blocked)` when the playbook blocks the machine's token, `token_blocked` in `--json`), `isolated-login` (`isolated_login`), `error` (the launch would be refused, reason in `NOTE`). An isolated playbook whose manifest or env set sets a non-empty token is `playbook-token (isolated)`, matching the launch, which injects that token and quarantines the stored grant. For `~/.claude`, which cpb does not launch, only an exported `CLAUDE_CODE_OAUTH_TOKEN` counts as `token`. |
| `STORE` | What sits at `.credentials.json`: `symlink -> <target>`, `file`, `file (no grant)`, or `absent`. |
| `EXPIRES` | The stored grant's `expiresAt` as `in 6h12m`, `expired`, `unknown`, or `-` when there is no grant. |
| `DAEMON` | Claude Code's `daemon-auth-status.json`: `auth_required` when its `since` is at or after the current grant's refresh instant (`expiresAt` minus 4 minutes, the daemon's proactive-refresh lead) and the row is a stored-login mode, `<status> (stale)` otherwise (the file is never cleared on recovery; under a token mode the stored login is quarantined and unused; for an isolated playbook whose store is still a symlink to the shared one, the marker concerns a login the launch detaches, so it never counts against that playbook), `-` when absent. |
| `NOTE` | `launch refused` (with a sanitized reason: no file content is ever echoed), `re-auth required`, `no login; stale account state, purged at launch` (an isolated playbook without a login of its own still carrying `oauthAccount` or cached feature flags; `--json` lists them under `stale_identity`), `stale account state, purged at launch (the shared login is detached at launch)` (the same, for an isolated playbook whose store is still a symlink to the shared one), `no login`, `grant expired (refreshes at launch if the refresh token is still valid)`, or empty. Token modes have no stored login to judge and show nothing. An explicitly empty token set by the manifest or an env set is `shared-login (token blocked)`, matching the launch decision. |
| `CLAUDE` | With `--claude`: `logged in, <subscription>`, `not logged in`, or `error: <reason>`. |

When a long-lived token file exists, a trailing line names it and notes that its own expiry is not recorded anywhere.

`--json` emits one object per row with the raw fields (`name`, `dir`, `mode`, `mode_error`, `token_blocked`, `isolated_login`, `store`, `store_target`, `has_grant`, `expires_at`, `expired`, `daemon_status`, `daemon_since`, `reauth_required`, `stale_identity` (when non-empty), `token_file`, and `claude` when requested: `logged_in`, `subscription_type`, `auth_method`, or `error`). Times are RFC 3339 in UTC. `reauth_required` is only ever true for a stored-login mode with a grant present whose `expiresAt` is known and a marker whose `since` is known; a marker that cannot be ordered against the grant is reported as stale.

**Errors:**
- Named playbook not found → `unknown playbook "x". `cpb SHOW PLAYBOOKS` lists them`

### `cpb update <name>`

Updates a playbook from where it came from: a playbook kept by `cpb play` (a `[play]` record) fetches its recorded recipe again (see *`cpb play`*); any other updates from the `[source]` metadata recorded in its `.playbook`. The CLI owns the update end to end.

```bash
cpb update sre --dry-run   # versions and the migrate step; changes nothing
cpb update sre             # asks about a migrate step on a terminal
cpb update sre --yes       # off a terminal: runs a declared migrate step
```

`--json`, `--sha256`, `--trust-endpoint` and `--trust-secret` apply to a played playbook only, as for `cpb play`.

**`cpb update <name>`** fetches a kept playbook's recorded ref again:
- the same sha256: "`<name>` is unchanged", and nothing runs;
- new bytes: the line diff from the kept bytes, a note when a pinned ref now
  serves other bytes ("the tag moved"), the plan, and every confirmation
  again. Then one `ALTER PLAYBOOK` undoes what the old recipe set and the new
  one no longer sets the same way (`UNSET VAR`, `UNSET TOOL`, `DROP PLUGIN`,
  `DROP MCP SERVER`, `DROP SKILL`, `DROP MODEL`, the `UNSET`s of
  the agent, model, picker and status line; plugins before their
  marketplace; a login is never unset), the new bytes are applied, and the
  record is updated;
- a playbook with neither a `[play]` nor a `[source]` record has nothing to
  update.

**Behaviour (`[source]`):**
1. Resolve the named playbook and require `[source].repository` metadata.
2. Refuse linked playbooks and installs whose config is selected through a top-level `subdir`.
3. Validate `[update].preserve` before touching anything, so an escaping path fails before the overlay starts.
4. Fetch `[source].repository` at the recorded branch and source subdirectory into a staging directory.
5. Read the staged source's migrate step, `[update] migrate`: it must resolve inside the staged tree (symlinks may not leave it) to an executable regular file, whose sha256 is taken. Nothing runs that the source does not declare.
6. With `--dry-run`, report the installed version (`version` from the live `.playbook`) against the available one (`version` from the staged `.playbook`, falling back to the staged `VERSION` file), and the migrate step with its sha256, and stop.
7. A declared step is agreed to before anything changes, even one that will not run because a version is unknown: `--yes`, or a yes on a terminal; otherwise the update is refused (`… declares a migrate step (…); pass --yes to run it, or --dry-run to see the update; nothing was changed`), and a no on a terminal cancels it.
8. Take the registry lock and re-read the live manifest. If the install directory is no longer the same filesystem object, or any `[source]` field changed while staging ran, activate nothing.
9. Move every top-level entry the staged source also provides into a timestamped `.<name>.bak.<stamp>` beside the install, then copy the staged source over the install **in place**. Entries the source does not ship — `data/`, `projects/`, `sessions/`, `history.jsonl` and the like — are never read, moved, or copied, so concurrent writes to them cannot be lost. The live root directory keeps its own mode; only entries below it take the source's. A failure at any point rolls back: entries the source **introduced** are removed and moved entries are restored from the backup; if any restoration fails the backup is kept and named in the error rather than deleted.
10. Restore the preserved files over the incoming copies: `settings.json`, `settings.local.json`, `.credentials.json`, `.claude.json`, plus every path in `[update].preserve`. A preserved file the source ships but the install did not have is removed rather than adopted, whether it arrived under a moved entry or a newly introduced one. Preserved paths under an entry the overlay never touched are left alone. Whether a preserved path's top-level entry was touched is decided by filesystem identity, not spelling, so on a case-insensitive filesystem a source-shipped `SETTINGS.JSON` is the preserved `settings.json`. Before anything is removed or written, every ancestor between a nested preserved path and its top-level entry is checked for **physical** containment inside that entry (symlinks evaluated, nearest existing ancestor; the final component itself is never followed, so a preserved entry that is a symlink such as the shared `.credentials.json` is recreated as a link). A source that turned a directory on the path into a symlink — pointing outside the install, or into a sibling entry the overlay never backed up — makes the update fail and roll back: `<rel> resolves outside <top>/ after the update (a symlinked ancestor); refusing to restore through it`.
11. Assemble the manifest that goes live in the staged tree before the overlay: local launcher, authentication-isolation, `[env]` overrides, the `[sandbox]` block, and source metadata are preserved from the live manifest (a source-shipped `[env]` or `[sandbox]` block is never adopted, not even transiently), and the install's `name` is always reset to its directory name.
12. Check, still under the registry lock, that the installed step resolves inside the install and has the sha256 agreed to; release the lock; then run it: as `<script> <from-version> <to-version> <install-dir>` with working directory the install and `CLAUDE_CONFIG_DIR`, `CPB_PLAYBOOK_NAME`, `CPB_PLAYBOOK_DIR` in the environment. It may run cpb statements, which take the lock themselves. Steps are expected to be idempotent; the CLI does not track which have run. A declared step is not run, with a warning, when either side has no `version`.

**Errors:**
- No name → `update takes one playbook name: cpb update <name> (cpb self-update updates cpb itself)`
- Target not found → `unknown playbook "sre". `cpb SHOW PLAYBOOKS` lists them`
- Neither record → `"sre" has no [source] or [play] record in .playbook; nothing to update from`
- Linked install → `"sre" is linked; native update is disabled to avoid replacing its external source`
- Subdir-selected install → `"sre" uses manifest subdir "...": native update requires a flat playbook`
- A played-playbook flag on a `[source]` one → `--json applies to a playbook kept by cpb play; sre updates from its [source]`
- Install changed while staging → `playbook "sre" changed while the update was staging (deleted, re-created, or re-sourced); nothing activated -- re-run update`
- Migrate step outside the playbook → `the source's migrate step: update.migrate "<path>" resolves outside <root>`
- Migrate step changed after the preview → `"sre" is at code version <v>, but its migrate step was not run: <path> changed between the preview and the run (…)`
- Migrate step exits non-zero → `"sre" is at code version <v>, but its migrate step failed: <err>`

### `cpb self-update`

Updates the running `cpb` binary in place to the newest GitHub release **of its own major version**. A new major version is never installed on its own.

```bash
cpb self-update            # the newest release of this major version
cpb self-update --check    # report the newest of this major and the newest overall; install nothing
cpb self-update --major    # allow a new major version (read its release notes first)
cpb self-update --force    # reinstall even if already on the newest
```

**Which release.** It reads the repository's release list from the GitHub API (`/repos/<repo>/releases?per_page=100`, following the `Link` header's `rel="next"`, at most 10 pages), never `/releases/latest`, which names a single release that may be of a higher major version.
- **Only releases count:** a release that is a draft or a pre-release is skipped, and so is any tag that is not exactly `vMAJOR.MINOR.PATCH` (`v4.0.0-rc1` is never installed, flagged or not).
- **By number, not by date:** the highest release in the running version's major is installed. A hotfix to an older major published after a newer one is still found.
- **The running version's major** comes from its own version, `vMAJOR.MINOR.PATCH` with an optional pre-release suffix: `v4.0.0-rc2` is major 4, and moves to `v4.0.0` once that is released.
- **A higher major** is named in one line, and nothing more happens: ``v5.0.0 is available, a new major version: run `cpb self-update --major` (read its release notes first)``. `--major` installs the highest release of any major.
- **Never a downgrade:** a running version newer than every release of its major (a build of an unreleased version) stays, `--force` included.
- **A build that is not a release** (version `dev`) has no major version. It refuses without `--major`, saying so; `--check` says so and installs nothing.
- **Fails closed:** if the list cannot be fetched or read (a rate limit, the network, malformed JSON, a `next` link outside the API base, more than 10 pages), it says why, exits non-zero and installs nothing. There is no fallback to `/releases/latest`.

It then downloads the asset for the running OS/architecture (`cpb-<goos>-<goarch>`), checks it against the release's `SHA256SUMS`, verifies it by running `--version` against the downloaded file, and atomically replaces the current executable (it stages a temp file in the executable's own directory and `rename`s it into place, so the swap is atomic and never a partial write). Symlinks are resolved first, so a `cpb` reached through a link (a package manager's, say) updates the real binary and leaves the link intact.

```
Current version: v4.1.0
Newest v4 release: v4.3.1
Newest release:  v5.0.0
v5.0.0 is available, a new major version: run `cpb self-update --major` (read its release notes first)
Downloading cpb-darwin-arm64 v4.3.1 (darwin/arm64)...
Checksum verified (sha256).
Updated to v4.3.1 at /Users/you/.local/bin/cpb.
```

If already on the newest release, it prints `Already up to date.` and exits (pass `--force` to reinstall it). If the install directory is not writable (e.g. a root-owned `/usr/local/bin`), it reports that elevated privileges are needed. `GITHUB_TOKEN`, when set, is used for the GitHub API requests to avoid rate limits; it is sent only to the API base.

**A Nix-managed binary is never replaced.** When the resolved executable lies in
`/nix/store/` (installed through devbox, `nix profile` or the flake), the store
is read-only and content-addressed: replacing a file there would corrupt the
package, and the generic permission advice would suggest `sudo` against it. So
`self-update` and `self-update --force` exit non-zero **before any release lookup** (no
network, whatever the latest version is), telling the pilot to change the
tag in `devbox.json` and run `devbox install` (a `devbox add` with a different
ref appends a second package rather than replacing the first); an up-to-date store binary never answers *Already up to
date.* as if it could update itself. `self-update --check` still reports, and
prints the same hint instead of *Run 'cpb self-update'*. The decision uses the
symlink-resolved path, because under devbox `argv[0]` is the profile's symlink,
not the store.

### `cpb self-uninstall`

Removes `cpb` and everything it created: all playbooks, their launcher commands, any `source <(... completion ...)` lines in shell rc files, the playbooks root directory, and the binary itself. The complete undo for an install.

```bash
cpb self-uninstall               # prompts
cpb self-uninstall -y            # skip the prompt
cpb self-uninstall --dry-run     # show what would be removed
cpb self-uninstall --keep-data   # remove the binary but keep playbooks
cpb self-uninstall --binary-only # remove binary/launchers/completions, keep playbooks (what uninstall.sh runs)
```

**Steps:**
1. For each discovered playbook (unless `--keep-data` or `--binary-only`): remove its directory.
2. Unless `--keep-data` or `--binary-only`, remove the playbooks root directory.
3. Unless `--keep-binary`, sweep **all** launcher symlinks pointing at the binary — every one of them would dangle once the binary is gone, whichever registry root it served. The sweep unions two sources: a resolution scan of the standard launcher directories (the resolved launcher dir plus the `~/.local/bin` fallback), and the launcher receipt file (`~/.local/state/cpb/launchers`, one absolute launcher path per line) in which every launcher the tool creates is recorded — covering custom `--launcher-dir` locations the scan cannot know about. Every candidate is verified to still be a symlink resolving to this binary (or dangling); a link the pilot renamed or repointed resolves elsewhere and is left alone. The reserved name `cpb` is owned by the binary-removal step. Once the launchers are gone, the receipt is removed too. With `--keep-binary`, no launchers are touched: a same-named command may be serving another registry root, and a removed default-root playbook's launcher fails loudly as stale rather than being silently deleted.
4. Unless `--keep-binary`, remove any `source <(cpb completion bash|zsh)` lines from `~/.bashrc` and `~/.zshrc` — after the binary is gone they would error on every new shell.
5. Unless `--keep-binary`, remove the running binary. If removal is denied by permissions, print the `sudo rm <path>` command to run manually rather than failing.
6. Print a summary of what was removed. When completion lines were removed from rc files, remind the pilot that already-open shells still hold the stale completion functions until reloaded. In `--binary-only` mode, state explicitly that the playbooks directory was not touched.

**Flags:**

| Flag | Description |
|------|-------------|
| `-y`, `--yes` | Skip the confirmation prompt |
| `--keep-data` | Preserve the playbooks directory and its playbooks |
| `--keep-binary` | Leave the binary in place |
| `--binary-only` | Remove only the binary, launchers, and completion lines — playbooks stay untouched (the mode `uninstall.sh` delegates to) |
| `--dry-run` | Print what would be removed without changing anything |

`--dry-run` never prompts and never modifies anything. Without `--dry-run` or `-y`, the command prints what will be removed and asks for confirmation.

### `cpb completion [bash|zsh|fish|powershell]`

Generates a shell completion script. Auto-generated by cobra and includes completion for subcommands, flags, and playbook names.

```bash
# zsh
cpb completion zsh > "${fpath[1]}/_cpb"

# bash
cpb completion bash > /etc/bash_completion.d/cpb

# fish
cpb completion fish > ~/.config/fish/completions/cpb.fish
```

Playbook name completion is wired for the commands that take a name: `run`, `auth status` and `update`. It completes the first argument only.

The bash script needs bash 4.2+ and the bash-completion package (it calls `_get_comp_words_by_ref`); the zsh script needs `compinit` to have run (it calls `compdef`). See `docs/guides/installation.md`.

**Planned:** statement completion. Every slot has a closed set, so TAB
could complete verbs, then objects, then existing names of that object, then
the clause keywords valid for it, then keys (from the object's current
entries). It is not built: only the playbook names above complete.

### `cpb` (no arguments)

Prints a one-line description and lists all discovered playbooks with how to run each. The parenthetical shows the playbook's registered command (its launcher, by directory name or manifest launcher); a playbook with none shows `(no launcher)`.

```
cpb (Claude PlayBooks) -- manage isolated Claude Code instances

Playbooks directory: ~/.claude-playbooks

Available playbooks:

  experiment    cpb run experiment    (or: experiment)
  sre           cpb run sre           (or: sre)
  dba           cpb run dba           (no launcher)

Run 'cpb --help' for all commands.
```

Empty state:
```
cpb (Claude PlayBooks) -- manage isolated Claude Code instances

Playbooks directory: ~/.claude-playbooks
No playbooks installed yet. Get started with one of:

  # Your own, from scratch:
  cpb CREATE PLAYBOOK <name>

  # One from a Git repository or a directory (SUBDIR picks one out of a monorepo):
  cpb CREATE PLAYBOOK <name> FROM <git-url-or-dir>

Run 'cpb --help' for all commands.
```

**Bare `cpb`** on a terminal ends with one more line, `Browse and manage
them: cpb tui`. Off a terminal, the output is the listing above, without that line.

## Files and state

### The filesystem is the source of truth

There is no index of playbooks and no database. The state cpb reads and writes is: the playbooks root (each playbook directory with its optional `.playbook` manifest, where its launcher is recorded; `.env-sets/`, the shared env sets and DEFAULTS; and `.state/`, cpb's own records about what it does not own: `dirs.toml` for the plain config directories statements target, `statusline-history.json` for the status lines `SET STATUSLINE` replaced), the launcher directory (symlinks to the binary that serve as per-playbook commands), the launcher receipt (`<state>/cpb/launchers`, `$XDG_STATE_HOME` or `~/.local/state`: one line per launcher cpb wrote), and a flock lock file in the user cache dir (`<cache>/cpb/registry.lock`, falling back to `<tmp>/cpb-registry-<uid>.lock` when no cache directory can be resolved or created; used only to serialize concurrent mutations — it holds no data). Discovery reads the directories themselves, so a change the pilot makes with `mv`, `rm`, or a text editor is what cpb sees on its next invocation.

### Playbook manifest

The `.playbook` manifest is **optional** and holds **metadata only**. It never affects discovery, and it never declares other playbooks. A directory without a `.playbook` is a perfectly valid playbook.

**Format:**

```toml
version = "1.0.0"
name = "sre"
launcher = "sre"
description = "Site Reliability Engineering assistant"
homepage = "https://github.com/user/repo"
author = "Ramazan Polat"

[source]
repository = "https://github.com/example/playbooks"
branch = "main"
subdir = "playbooks/sre"

[update]
preserve = ["settings.json"]
```

**Fields:**

| Field | Meaning |
|-------|---------|
| `version` | Version of the playbook itself (free-form semver string). Shown by `SHOW PLAYBOOK`. Not enforced by the tool. |
| `name` | Informational. A playbook's name is always its directory name, the one `CREATE PLAYBOOK <name>` gives. |
| `launcher` | The launcher `CREATE PLAYBOOK … FROM` registers when the statement names none. |
| `subdir` | Optional: the subdirectory that holds the Claude config. In a source, `CREATE PLAYBOOK … FROM` copies only that subdirectory, flat, into the playbook, clears the field, and records the slice in `source.subdir`, so `update` fetches the same one. An installed playbook whose manifest names a `subdir` keeps its config there: every launch binds that subdirectory as the config directory, `DROP PLAYBOOK` removes the whole playbook directory, and `update` refuses it (`"<name>" uses manifest subdir "<dir>"; native update requires a flat playbook`). |
| `description` | Human-readable description, shown by `SHOW PLAYBOOK`. |
| `homepage` | Optional URL, shown by `SHOW PLAYBOOK`. |
| `author` | Optional author name or contact, shown by `SHOW PLAYBOOK`. |
| `isolated_login` | When true, detach shared credentials and do not copy global credentials or account metadata into this playbook; while it has no login of its own, account state left from a non-isolated past (`oauthAccount`, cached feature flags) is removed at launch. The machine-global long-lived token and plan descriptors never reach it; a `CLAUDE_CODE_OAUTH_TOKEN` this playbook's own `env.set` (or an env set it uses) supplies is honoured as its own token, with its stored grant quarantined as on the shared token path. The manifest governing a config directory is the nearest valid one walking up from it; an unreadable manifest on the way is reported but does not switch isolation off. |
| `env.sets` | Optional list of env set names (files under `<playbooks root>/.env-sets/<name>.toml`) layered under `env.set`/`env.block` at launch, in list order. A set that is missing, unreadable, or invalid refuses the launch. Install-local. |
| `env.set` | Optional table of environment variables applied to the child `claude` process on every launch, overriding inherited values. Install-local: never adopted from a source. A manifest carrying any `env.set` value is written owner-only (its existing mode masked to `0600`; values may be tokens); otherwise an existing file keeps its mode exactly, and a new one is `0644`. A rewrite never loosens a file. |
| `env.block` | Optional list of environment variable names removed from the child's environment on every launch, even when the shell exports them. Install-local. Blocking `CLAUDE_CODE_OAUTH_TOKEN` makes the long-lived token inactive for this playbook (stored-credentials path: no injection, no quarantine). |
| `env.refs` | Optional table of variable names to secret references, written by `SET VAR <key> FROM '<ref>'`. The configured secret helper resolves each at launch; the value is never stored (see *Secrets (optional)*). A key lives in exactly one of `env.set`, `env.refs` and `env.block`. Install-local. |
| `mcp.<server>.vars` | Written by `ADD MCP SERVER … FROM '<ref>'`: the derived `CPB_MCP_…` variables under which that server's references live in `env.refs`, so `DROP MCP SERVER` forgets them (see *MCP servers*). |
| `skills.<name>.source`, `.branch`, `.subdir`, `.mode` | Written by `ADD SKILL`: where the skill came from, and whether it is a link to a directory (`mode = "link"`) or a copy of a git source (`"copy"`). `DROP SKILL` removes the record with the skill (see *Skills*). |
| `source.repository` | Git URL or local source used by native update. Git installs populate this automatically. |
| `source.branch` | Optional Git branch or tag used by native update. |
| `source.subdir` | Optional source-relative directory selected during native update. Must remain physically below the fetched source, including through symlinks. |
| `update.preserve` | Optional list of install-local paths that survive an update even when the source ships its own copy. Each must be relative to and physically below the playbook root. `settings.json`, `settings.local.json`, `.credentials.json` and `.claude.json` are always preserved and need not be listed. |
| `update.migrate` | Optional migrate step: a script, relative to and physically below the playbook root, that `update` runs after the new files are in place (`<script> <from-version> <to-version> <install-dir>`), once agreed to (`--yes`, or a yes on a terminal). Without it no migration runs. Read from the source being updated to. |
| `sandbox.always` | When true, every launch of this playbook is sandboxed (`run`, launcher dispatch; `start` for a directory carrying the manifest); `--no-sandbox` overrides one launch, loudly. Install-local: `CREATE PLAYBOOK … SANDBOX` writes it with `isolated_login = true`; never adopted from a source; preserved by `update`. |
| `sandbox.host` | Optional ssh destination (`user@host`; ports and jump hosts through `~/.ssh/config`) where sandboxed launches of this playbook run, with `cpb` and the playbook installed there. `--sandbox-host` overrides it for one launch. Install-local. |
| `sandbox.backend` | Optional sandbox implementation name; `sbx` (Docker Sandboxes), the only one. `--sandbox=BACKEND` overrides it for one launch. Install-local. |
| `sandbox.share_skills` | When true, the backend's shared skills store is mounted into the sandbox (what `sbx` does on its own). Default false: `sbx create` gets `--no-share-skills`, so a sandbox cannot plant a skill a later sandbox runs. Creation-time. |
| `sandbox.secrets` | How backend API keys reach the sandbox: `"proxy"` (default; an empty string is the same as omitting the key) registers them as proxy-injected secrets and hands the sandbox a placeholder; `"env"` passes the values as plain variables. Under `"proxy"`, a key set in the playbook's own `[env.set]` refuses the launch (the manifest is on the mount); keep such keys in env sets. |
| `sandbox.workdir` | Optional default working directory for `run --sandbox`, absolute or `~`-prefixed; `--workdir` overrides it, the invocation directory is used when neither is given. |
| `sandbox.mounts` | Optional list of extra host paths mounted into the sandbox at the same absolute path, each absolute or `~`-prefixed, with `:ro` as the only accepted option (read-only). Nothing else of the host is visible inside. |
| `sandbox.allow_net` | Optional list of hosts (domains, wildcards, CIDR ranges, no whitespace) allowed for this playbook's sandbox on top of the active sandbox policy, applied once when the sandbox is created. The host of an `ANTHROPIC_BASE_URL` the effective environment sets is allowed automatically. |
| `sandbox.claude_version` | Optional Claude Code version (`2.1.263`) installed inside the sandbox at creation; empty runs the sandbox image's own. A playbook routed to a backend that rejects a newer Claude Code's tool schemas pins the last version that works. |
| `play.ref`, `play.url`, `play.sha256`, `play.played_at` | Written by `cpb play --keep`: where the playbook's recipe came from. `ref` is what `cpb update <name>` resolves again (a template name, an https URL, a `github:` ref, or a local file's absolute path); `url` the address the bytes were read from (absent for a local file); `sha256` the recipe's; `played_at` when, in RFC 3339, UTC. The bytes themselves are kept as `.play/recipe.cpb` in the playbook. `cpb update <name>` rewrites both when it applies new bytes. See *`cpb play`*. |

**Unknown keys are refused.** A key the manifest does not define is an error naming it and its line, `unknown key "<key>" in <path>:<line>`, one per key. The playbook does not launch, and registry discovery refuses it for every command. A manifest written for another version of cpb fails loudly instead of being read in part.

**Errors:**
- Invalid TOML → `invalid .playbook at <path>: TOML syntax error at line <n> (content not shown)`. The parser's own message is never echoed: a manifest may hold credential values under `[env.set]`, and this error reaches the terminal from every command that discovers playbooks.
- `subdir`, `source.subdir`, `update.migrate` or any `update.preserve` entry escapes its root → `invalid .playbook at <path>: <field> must be a relative path below the playbook root`. One helper validates them all, so the field name is the only difference between them.
- `subdir` or `source.subdir` names a path that does not exist, or is not a directory → `<field> "<value>" not found below <root>: <stat error>` / `<field> "<value>" is not a directory below <root>`. Raised when the path is resolved, so it carries the root it was resolved against rather than the manifest path.
- An `env` key is not a valid variable name → `invalid .playbook at <path>: env.set: invalid environment variable name "<key>"`
- An `env` key is `CLAUDE_CONFIG_DIR` or `CPB_CONFIG_DIR` (the reserved keys) → `invalid .playbook at <path>: env.set: <key> is managed by cpb and cannot be overridden`
- A key appears in both `env.set` and `env.block` → `invalid .playbook at <path>: env: <key> is both set and blocked`
- An `env.set` value is not valid UTF-8 → `invalid .playbook at <path>: env.set: value of <key> is not valid UTF-8 and cannot be stored in a manifest`.
- A `sandbox.mounts` entry is relative, carries another option than `:ro`, or contains a colon or line break → `invalid .playbook at <path>: sandbox.mounts entry "<entry>" must be an absolute or ~-prefixed path, optionally suffixed :ro`
- A `sandbox.allow_net` entry is empty or contains whitespace → `invalid .playbook at <path>: sandbox.allow_net entry "<entry>" must be a host, wildcard or CIDR without whitespace`
- `sandbox.claude_version` is not `MAJOR.MINOR.PATCH` → `invalid .playbook at <path>: sandbox.claude_version "<value>" must look like 2.1.263`
- `sandbox.workdir` is relative → `invalid .playbook at <path>: sandbox.workdir "<value>" must be an absolute or ~-prefixed path`
- `sandbox.backend` unknown → `invalid .playbook at <path>: sandbox.backend "<value>" is not a known backend (sbx)`
- `sandbox.host` with whitespace, a slash, or a leading `-` → `invalid .playbook at <path>: sandbox.host "<value>" must be an ssh destination such as user@host`
- `sandbox.secrets` not `proxy` or `env` → `invalid .playbook at <path>: sandbox.secrets "<value>" must be "proxy" or "env"`
- An `env.set` value contains a NUL byte → `invalid .playbook at <path>: env.set: value of <key> contains a NUL byte, which cannot be passed in an environment` (refused on write and on read; a NUL in an environment fails every launch). Any other value round-trips: control characters are written as TOML `\uXXXX` escapes.
- An `env.sets` entry is not a valid env set name → `invalid .playbook at <path>: env.sets: invalid env set name "<name>": use letters, digits, dots, dashes, underscores`

## Global flags and environment variables

These flags work on every command.

| Flag | Description |
|------|-------------|
| `--playbooks-dir <path>` | Override the playbooks root directory. Default: `~/.claude-playbooks` |
| `--launcher-dir <path>` | Override the launcher directory. Default: the directory of the binary as invoked, falling back to `~/.local/bin` when unwritable |
| `--version` | Print the version of `cpb` |
| `--help`, `-h` | Show help for the command or subcommand |

### Environment variables

| Variable | Flag equivalent |
|----------|----------------|
| `CPB_PLAYBOOKS_DIR` | `--playbooks-dir` |
| `CPB_LAUNCHER_DIR` | `--launcher-dir` |

**Resolution precedence:** CLI flag → environment variable → default.

These have no flag equivalent:

| Variable | Effect |
|----------|--------|
| `CPB_ISOLATED_LOGIN=true` | Forces the isolation branch of the authentication decision for this launch, as `isolated_login = true` in the manifest does. |
| `CPB_OAUTH_TOKEN_FILE` | Overrides the long-lived token file read in step 2 of the authentication decision. Default `~/.config/claude-code/oauth-token`. |
| `CPB_CONFIG_DIR` | The config directory `run` and launcher dispatch bind, in place of the playbook's install directory. Absolute or `~`-prefixed; empty means unset; never created. Consumed -- stripped from the child's environment after every layer. A **reserved key**: a manifest, env set, `--env` or `--env-file` that declares it is refused. Refused together with a sandboxed launch. A bare inherited `CLAUDE_CONFIG_DIR` is discarded. See *Caller-supplied config directory*. |
| `CPB_SECRET_HELPER` | The secret helper command for this process, over the one `ALTER DEFAULTS SET SECRET HELPER` stored (see *Secrets (optional)*). |
| `CPB_CLICKHOUSE` | The `clickhouse` binary `SELECT` pipes a query to, instead of `clickhouse` or `ch` on `PATH` (see *SELECT*). |
| `XDG_STATE_HOME` | Parent of the launcher receipt directory (`<XDG_STATE_HOME>/cpb/launchers`). Default `~/.local/state`. |
| `CPB_LAUNCHER_RECEIPT` | Absolute path of the launcher receipt file, overriding the `XDG_STATE_HOME` computation. A test seam. |
| `GITHUB_TOKEN` | Sent as the bearer credential on the release-API requests `self-update` makes, raising the anonymous rate limit. |
| `CPB_UPDATE_REPO`, `CPB_UPDATE_API_BASE`, `CPB_UPDATE_DOWNLOAD_BASE` | Redirect self-update at another repository, API, or asset host. Test seams. |

A variable marked *test seam* exists so the suites can run without network or a real release.

## Distribution

The binary reaches a machine by one of four routes. All of them land the same
artifact: one `cpb` executable in one directory on `PATH`.

| Route | Entry point | Install directory |
|---|---|---|
| Install script | `install.sh`, piped from the raw repository URL or run from a clone | `$CPB_INSTALL_DIR`, else `/usr/local/bin` when writable, else `~/.local/bin` |
| npm / npx | the `cpb-cli` package, whose `bin` entry points at `bin/npx-shim.sh` | `~/.local/bin` only |
| Source | `build.sh`, then a manual `mv` | wherever the pilot puts it |
| devbox / Nix | `flake.nix`, as `git+https://github.com/ramazanpolat/claude-playbooks?ref=refs/tags/<tag>#cpb` | the Nix store, reached through the devbox (or `nix profile`) profile's `bin` |

Neither script edits a shell rc file. Completion lines are printed for the
pilot to add, never appended. A symlink to the binary under any name but
`cpb` is dispatched as a playbook launcher (see *Launchers*); another
name for the CLI is a shell alias or a hard link.

### The flake (devbox / Nix)

`flake.nix` builds `cpb` **from source** at the pinned ref with
`buildGoModule` (`CGO_ENABLED=0`, so a static binary whose runtime closure is
data only: tzdata, iana-etc, mailcap), for `x86_64`/`aarch64` × `linux`/`darwin`.
It never fetches release binaries: a tagged commit cannot carry the hashes of
binaries built after it was tagged. The version stamped in is `v` + the
`package.json` version, which `release.yml` requires to equal the tag, so a
**tag** is the ref to pin; a commit between releases reports the previous
release's version. `vendorHash` must be recomputed whenever `go.mod`/`go.sum`
change. The flake's `nixpkgs` input affects only the build, never a user's
profile. `.github/workflows/nix.yml` builds it on Linux and macOS and adds it to
a fresh devbox project by a real `git+https:` reference pinned to the commit.

The documented reference form is `git+https://…?ref=refs/tags/<tag>`, not
`github:…/<tag>`: a `github:` reference is resolved through GitHub's REST API,
which rate-limits unauthenticated callers per IP, and behind a shared IP both
`nix` and `devbox` then fail with HTTP 403 (devbox supplies no token for it).
`git+https` fetches over git, makes no API call, and devbox.lock records the
tag's exact `rev`.

Launchers written from a devbox-installed binary target the path as invoked (the
profile's stable `bin` entry), never the versioned store path, and an unwritable
binary directory falls back to `~/.local/bin` as for any read-only install.

### Release asset naming

Both downloading routes resolve the same asset name, `<prefix>-<os>-<arch>`,
with `os` from `uname -s` lowercased (`darwin` or `linux`; anything else is
refused, native Windows explicitly so in the shim, which names WSL) and `arch`
from `uname -m` mapped `x86_64`→`amd64`, `aarch64`/`arm64`→`arm64`. The asset is
fetched from `<download base>/<tag>/<asset>`.

### Checksum policy

Shared verbatim by `install.sh` and the npx shim. After downloading the asset to
a temporary file, the release's `SHA256SUMS` is fetched from the same base URL
and the asset's line located by name (`hash  name` and `hash *name` both parsed,
comparison lowercased since sums files may carry uppercase hex).

- **A hash that is present, well-formed, and different aborts the install**, printing
  the expected and actual digests. The temporary file is removed by the trap.
- **Every unverifiable case warns and continues**: no `SHA256SUMS` published for
  the tag, the asset not listed in it, a malformed or duplicated entry, no
  `sha256sum` or `shasum` on the machine, and — for `install.sh` — a
  `CPB_INSTALL_URL` override in use.

The asymmetry is deliberate and is the policy's whole content: the sums travel
over the same channel as the binary, so they establish that what arrived is what
was published, not that the publisher is honest. They guard against corruption
and truncation, not a compromised host. A malformed entry is therefore treated as
*unverifiable*, never as a mismatch, so a truncated sums file cannot fail a
legitimate binary.

The download is written to a `mktemp` file inside the destination directory (the
shim) or under `$TMPDIR` (`install.sh`), made executable, and moved into place
only after verification, so an aborted install never leaves a partial binary
where the previous one stood.

### `install.sh`

Resolves the release tag from `$CPB_INSTALL_VERSION` when set, else the `tag_name` of the
latest release from the GitHub API. Chooses the install directory as in the table
above — the `/usr/local/bin` probe is a writability test, so an unprivileged run
falls back to `~/.local/bin` rather than failing or escalating; the script never
invokes `sudo`. It then reports the install path, warns when that directory is
not on `PATH` (printing the `export` line to add), and prints the optional
completion lines.

### npx shim

`bin/npx-shim.sh` is the `bin` entry for `cpb` in the
`cpb-cli` package. It has three modes.

1. **Delegate.** An installed `cpb` found on `PATH` is
   `exec`'d with the original arguments. Nothing is downloaded and no version is
   negotiated: the installed binary is the source of truth, updated with `self-update`.
   The search resolves symlinks on both sides and skips any candidate that is
   this script, because under npx the package's own `bin` directory sits first on
   `PATH` and a naive lookup would find the shim and loop forever.
2. **Bootstrap** (the default when nothing is installed). Downloads and verifies
   as above, installs `cpb` to `~/.local/bin` — never `/usr/local/bin`, never
   `sudo` — announces the install and the `self-uninstall --keep-data`
   command that reverses it, warns when the directory is not on
   `PATH`, and `exec`s the binary. Afterwards the machine holds an ordinary
   install and later npx invocations take route 1.
3. **Ephemeral** (`CPB_NPX_BOOTSTRAP=0`). Neither delegates nor installs:
   downloads to `${XDG_CACHE_HOME:-~/.cache}/cpb/npx/<tag>/` (reusing an existing copy) and
   runs from there, which is also how a pinned version is exercised beside an
   installed one.

Tag resolution, first match wins: `$CPB_NPX_VERSION`; else the package's own version
as `v<npm_package_version>` (so `npx github:<repo>#v4.0.0` runs that release, and
`package.json`'s `version` is bumped with the release tag); else the latest
release from the GitHub API. A tag that resolves to nothing is an error naming
`CPB_NPX_VERSION` as the escape hatch.

The binary is `exec`'d with `argv[0]` set to its own path, so multicall dispatch
behaves exactly as a direct invocation.

The package declares `os` `darwin`/`linux` and `cpu` `x64`/`arm64`, so npm
refuses to install it on native Windows.

### `uninstall.sh`

Delegates to `cpb self-uninstall --binary-only`, so one
implementation owns all removal (see that command). Playbooks are never touched
by it.

### Environment knobs

Read by the two shell scripts only; the Go binary reads none of them.

| Variable | Scripts | Effect |
|---|---|---|
| `CPB_INSTALL_VERSION` | `install.sh` | Release tag to install, skipping the latest-release lookup |
| `CPB_NPX_VERSION` | shim | Same, for the shim; highest-priority tag source |
| `CPB_NPX_BOOTSTRAP=0` | shim | Ephemeral mode: no delegation, no install |
| `CPB_NPX_CACHE` | shim | Ephemeral-mode cache root. Default `${XDG_CACHE_HOME:-~/.cache}/cpb/npx`, outside the playbooks root |
| `CPB_NPX_INSTALL_DIR` | shim | Bootstrap install directory. Default `~/.local/bin` |
| `CPB_INSTALL_DIR` | `install.sh` | Install directory, overriding the writability probe |
| `CPB_INSTALL_DEFAULT_DIR` | `install.sh` | The directory that probe tests. Default `/usr/local/bin` |
| `CPB_INSTALL_URL` | `install.sh` | Exact asset URL; suppresses checksum verification with a warning |
| `CPB_INSTALL_REPO`, `CPB_INSTALL_ASSET_PREFIX`, `CPB_INSTALL_DOWNLOAD_BASE` | both | Redirect at another repository, asset name (default `cpb`), or asset host |

`CPB_INSTALL_REPO`, `CPB_INSTALL_ASSET_PREFIX`, `CPB_INSTALL_DOWNLOAD_BASE`,
`CPB_INSTALL_URL` and `CPB_INSTALL_DEFAULT_DIR` exist so the install suites can
run against a local fixture server.

## Output

Every `SHOW` and `EXPLAIN` has two forms. The **human form** is for reading;
its layout may change between releases. The **`--json` form** is for
scripts; the release notes name every change to it. Nothing should grep the
human form.

A variable, wherever it appears, is one JSON object with exactly one of:

```
{"key": "MODEL",   "value": "glm-5.3"}                       literal
{"key": "TOKEN",   "ref": "keychain:router-token"}                secret by reference
{"key": "API_KEY", "redacted": true, "plaintext": true}      credential-looking literal: value never shown
{"key": "HTTP_PROXY", "blocked": true}                       BLOCK
```

## Exit codes and error conventions

- Exit code `0` on success, non-zero on failure. Cobra's default is `1` for user errors.
- All errors go to stderr.
- Messages are plain English, one line, no stack traces. Always suggest the next action where possible.

**Examples:**
```
Error: playbook "myrepo" already exists (write CREATE PLAYBOOK IF NOT EXISTS to keep it)
Error: unknown playbook "typo". `cpb SHOW PLAYBOOKS` lists them
Error: 'claude' command not found. Install Claude Code first: https://claude.ai/download
Error: source.subdir "playbooks/sre" not found below /tmp/stage: lstat /tmp/stage/playbooks: no such file or directory
Error: "sre" has no [source] or [play] record in .playbook; nothing to update from
Error: invalid .playbook at ~/.claude-playbooks/foo/.playbook: TOML syntax error at line 3 (content not shown)
```
