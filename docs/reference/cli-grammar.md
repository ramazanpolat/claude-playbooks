# CLI grammar — spec (draft)

Status: **agreed 2026-09-26; implementation in progress (phase 1, the
parser). Ships as v3.20.0.** Decided with the pilot on 2026-09-25/26; no
open points remain.

## Why

The state-changing commands grew one at a time and read inconsistently:

- two top-level commands for one idea (`env`, `env-profile`);
- name before verb (`env kommander-idea use glm`), so the words don't say
  whether you are acting on a playbook or on a profile;
- `env-profile x default` means *every playbook*, the opposite of what
  "a default for this playbook" suggests;
- `unset` and `clear` look like synonyms and are not.

The fix is one regular grammar, read and written like DDL: the pilot
*commands* cpb.

## Shape

```
cpb  <VERB>   <OBJECT>   <name>   <clause> <clause> ...
     ALTER    PLAYBOOK   k-idea   USE ENV evren-router  SET VAR FOO=1
```

- Keywords are **case-insensitive**. Docs write them in capitals.
- Clauses are **two words** (`SET VAR`, `USE ENV`), never hyphenated.
- Lists are **space-separated**, without commas. An unquoted comma at the
  end of an item is read as a separator when another item follows it
  (`SET A=1, B=2`, `USE ENV a, b`); a final value keeps its comma, so
  `SET NO_PROXY=a,b,` stores `a,b,`. Quote a value to be explicit.
- Names follow the rules each object already has: a playbook name is
  letters, digits, `_` and `-`; an env set name also allows dots
  (`glm-5.3`). A **keyword is not a valid new name**
  (refused with an error naming the keyword); an existing object whose name
  is a keyword can still be addressed in the name slot.
- **Global flags go before the verb** (`cpb --playbooks-dir X ALTER …`), and
  `CLAUDE_PLAYBOOKS_DIR` works as today. cpb recognises a statement before
  its flag parser runs, so every word after the verb belongs to the
  statement: `SET VAR OPTS=-v` is a value, and `--dry-run` /
  `--skip-secrets` are the statement's own.

## Objects

| Object | What it is | Lives at |
|---|---|---|
| `PLAYBOOK` | an installed playbook: dir, launcher, source, attached ENVs, own variables | `<root>/<name>/` |
| `ENV` | an **env set**: a named, reusable set of variables (formerly "env profile") | `<root>/.env-profiles/<name>.toml` |
| `DEFAULTS` | the machine-wide layer under every playbook: an ordered list of env sets; a singleton, no name | `<root>/.env-profiles/.default` |

Two words keep the variables apart: **`ENV` is a named set**, **`VAR` is one
variable**. "Profile" is deliberately not a keyword: it would be ambiguous with other
tools' profiles.

**cpb knows nothing about pilots** (decided with the pilot on 2026-09-26:
components stay standalone and loosely coupled). A playbook takes the pilot
profile through its own `CLAUDE.md` imports, and choosing a pilot for a
playbook is pilot-profile's own command, run by the pilot. No statement
here reads, writes or calls anything of pilot-profile's.

## Grammar

```
command    := write | read | select | APPLY <file> [--dry-run]
                                           select: v3.21.0, see SELECT

write      := CREATE ENV [IF NOT EXISTS] <name> [env-clause ...]
            | CREATE OR REPLACE ENV <name> [env-clause ...]
            | ALTER  ENV <name> env-clause ...
            | DROP   ENV [IF EXISTS] <name>
            | CREATE PLAYBOOK [IF NOT EXISTS] <name> [origin] [launcher] [SANDBOX]
            | ALTER  PLAYBOOK <name> pb-clause ...
            | DROP   PLAYBOOK [IF EXISTS] <name> [--yes]
            | ALTER  DEFAULTS defaults-clause ...

origin     := FROM <source> [BRANCH <ref>] [SUBDIR <dir>]   clone or copy a source
            | LINK <dir>                                    develop in place
launcher   := ALIAS <launcher> | NO ALIAS                   default: the name

env-clause := SET [VAR] <key>=<value> ... [AS PLAINTEXT]
                                           literal values; AS PLAINTEXT: see Secrets
            | SET [VAR] <key> FROM '<ref>' secret by reference; resolved at launch
            | BLOCK [VAR] <key> ...        removed at launch even if the shell exports it
            | UNSET [VAR] <key> ...        forgotten; the layer below applies again
            | DESCRIBE '<text>'

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
            | ALIAS <launcher>             set or replace the launcher (one per playbook)
            | NO ALIAS                     remove the launcher

read       := SHOW { PLAYBOOKS | ENVS | DEFAULTS | PLAYBOOK <name> | ENV <name> } [--json]
            | SHOW CREATE { PLAYBOOK <name> | ENV <name> | ALL } [--skip-secrets]
            | EXPLAIN PLAYBOOK <name> [--json]
```

The alternatives are exclusive, and the parser enforces them: `OR REPLACE`
and `IF NOT EXISTS` cannot be combined; a playbook has one origin, `FROM` or `LINK`,
and `BRANCH` / `SUBDIR` only with `FROM`; `ALIAS` and `NO ALIAS` exclude each
other. The clauses of `origin` and `launcher` may come in any order.
`DROP PLAYBOOK` asks for confirmation on a terminal, as `delete` does;
`--yes` skips it.

Inside `ALTER ENV` the word `VAR` is optional (`SET FOO=1`): the object
already says it. Inside `ALTER PLAYBOOK` it is required (`SET VAR FOO=1`),
because a playbook has other things to set.

Order matters for env sets: a later set overrides an earlier one. `USE ENV a b`
states the whole list, so it is the idempotent form; `ADD ENV` places one set
without restating the rest.

One rule keeps the verbs apart: **`DROP` acts on objects** (an ENV, a
playbook) and **`UNSET` acts on variables**. So `DROP ENV
evren-router` inside `ALTER PLAYBOOK` detaches that set, and `UNSET VAR FOO`
forgets the playbook's own `FOO`.

`IF NOT EXISTS` / `IF EXISTS` turn "already there" / "not there" into a no-op
instead of an error; they sit before the name, as in ClickHouse.
`CREATE OR REPLACE ENV` replaces the set's whole content.
`CREATE PLAYBOOK IF NOT EXISTS` never re-clones an existing playbook.

`DROP ENV` is refused while a playbook or `DEFAULTS` uses the set; the error
lists the users.

Clauses in one command apply **atomically**: all or none, validated before
anything is written. Validation includes the secret helper's check for
every `SET … FROM` (see Secrets).

## Where each clause writes

The grammar is a new front end over the files cpb already keeps. Nothing
stores commands; files store the result.

| Clause | Writes |
|---|---|
| `CREATE / ALTER / DROP ENV` | `<root>/.env-profiles/<name>.toml`: `description`, `[set]`, `[refs]`, `unset` |
| `ALTER PLAYBOOK … USE / ADD / DROP ENV` | the playbook's `.playbook`, `[env] profiles = [...]` |
| `ALTER PLAYBOOK … SET VAR K=V` | `.playbook` `[env.set]` |
| `ALTER PLAYBOOK … SET VAR K FROM '<ref>'` | `.playbook` `[env.refs]` (new) |
| `ALTER PLAYBOOK … BLOCK VAR K` | `.playbook` `[env] unset = [...]` |
| `ALTER PLAYBOOK … UNSET VAR K` | removes K from whichever of the three holds it |
| `ALTER DEFAULTS … USE / ADD / DROP ENV` | `<root>/.env-profiles/.default`, one set name per line, in order |
| `ALTER DEFAULTS SET / UNSET SECRET HELPER` | `<root>/.env-profiles/.secret-helper`, one line: the command |
| `CREATE / DROP PLAYBOOK`, `RENAME TO`, `ALIAS`, `NO ALIAS` | the playbook dir, the registry and the launcher, as `create`/`install`/`link`/`delete`/`rename`/`alias` do today |

A key lives in exactly one of `set`, `refs`, `unset` within a layer; writing
it to one removes it from the others.

## Layers at launch

```
shell  ->  DEFAULTS (listed order)  ->  USE ENV (listed order)  ->  playbook's own SET / BLOCK
```

Each layer overrides the one before it. `CLAUDE_CONFIG_DIR` is never
overridable, as today.

`EXPLAIN PLAYBOOK <name>` prints every variable that a launch would change,
its effective value, and the layer that decided it:

```
ANTHROPIC_BASE_URL    http://tr0:20128/v1              <- ENV evren-router
ANTHROPIC_AUTH_TOKEN  <from keychain:9router>          <- ENV evren-router
ANTHROPIC_MODEL       glm-5.3                          <- PLAYBOOK kommander-idea
HTTP_PROXY            (blocked)                        <- PLAYBOOK kommander-idea
OPENAI_API_KEY        sk-a...9f2c (51 chars, plaintext) <- PLAYBOOK kommander-idea
FOO                   bar                              <- DEFAULTS (ENV claude-default)

Secret helper: my-keychain-helper (from CPB_SECRET_HELPER)
```

The last line names the helper that resolves this playbook's references and
where it came from (`setting` or `CPB_SECRET_HELPER`), or reads
`Secret helper: (none)`.

## Secrets (optional)

Secret references are **optional**: cpb works fully without them, and
nothing else in the grammar depends on them. They follow git's
`credential.helper` pattern: cpb defines a small interface, and the pilot
configures a program that implements it (for example a keychain helper).
cpb never names or discovers one.

**Configuring the helper** (decided 2026-09-26):

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
…)". Literal values work as today.

`FROM` also names a playbook's source in `CREATE PLAYBOOK`; the position
disambiguates (decided 2026-09-25).

- The value never appears in a file, in argv, or in any `SHOW`/`EXPLAIN`
  output.
- A sandboxed launch of a playbook with references is refused in the first
  release, with a message saying so.

**The grammar refuses a credential-looking literal** (ruled 2026-09-26): a
`SET [VAR] K=V` whose key looks like a credential (the rule `env` already
uses to redact: `TOKEN`, `SECRET`, `PASSWORD`, `AUTH`, `*_KEY`, …) is
refused, naming the key and never the value, and pointing at
`SET K FROM '<ref>'`. A value that cannot be a secret is let through: empty,
an integer, or `true`/`false`, so `SET VAR MAX_THINKING_TOKENS=8000` works.
**`AS PLAINTEXT` stores one knowingly** (ruled 2026-09-26), for a pilot
without a secret helper: `SET VAR ANTHROPIC_AUTH_TOKEN=… AS PLAINTEXT`. It
applies to every literal in its `SET` clause and never to a reference. It
keeps cpb usable standalone, and it is loud where it matters: `EXPLAIN` marks
such an entry `(plaintext)`, and `SHOW CREATE` never carries the value (see
playbook files). Files that already hold such literals keep working unchanged.

All output redacts credential-looking literals, as `env` does today; there is
no `--reveal` in the new grammar.

## playbook.cpb: SHOW CREATE and APPLY

Two senses of "playbook", kept apart (pilot, 2026-09-26):

- **`PLAYBOOK`**, an *installed playbook*: a Claude Code config directory cpb manages.
- **a playbook file**, also *a playbook script* or *the agent's playbook*: a `.cpb` file of statements, conventionally `playbook.cpb`, that `APPLY` runs. `APPLY` accepts any file name.

A **playbook file** (conventionally `playbook.cpb`) is plain text holding cpb
statements: the same grammar as the command line, without the `cpb` prefix.
It describes a machine's setup: env sets, DEFAULTS, playbooks and their
wiring. It is the script beside the database, never inside it: the manifest
keeps holding state, the file holds the recipe. It belongs in dotfiles or a
devbox project, and moving a setup to another machine is one command.

```
-- playbook.cpb (macminim), from: cpb SHOW CREATE ALL > playbook.cpb

CREATE OR REPLACE ENV evren-router
  DESCRIBE 'GLM via 9router on tr0'
  SET ANTHROPIC_BASE_URL=http://tr0:20128/v1 ANTHROPIC_MODEL=glm-5.3
  SET ANTHROPIC_AUTH_TOKEN FROM 'keychain:9router-client';

CREATE OR REPLACE ENV claude-default
  BLOCK HTTP_PROXY;

ALTER DEFAULTS USE ENV claude-default;

CREATE PLAYBOOK IF NOT EXISTS kommander-idea
  FROM https://github.com/ramazanpolat/kommander-playbook BRANCH v3.12.2 ALIAS ki;

ALTER PLAYBOOK kommander-idea
  USE ENV evren-router
  SET VAR MAX_THINKING_TOKENS=8000
  BLOCK VAR CLAUDE_CODE_OAUTH_TOKEN;
```

File rules:

- **Only write statements** (`CREATE`, `ALTER`, `DROP`). A read is refused.
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

`cpb APPLY <file>`:

1. Parses the whole file and validates every statement — syntax, names,
   the secret helper's check, sources reachable — and
   writes nothing if any fails.
2. Executes the statements in order, each one atomic, reporting each as
   `created`, `changed` or `unchanged`.
3. **Stops at the first failure** and reports which statement failed and
   which were already applied. There is no whole-file rollback: a clone
   cannot be undone cheaply. Because every statement `SHOW CREATE` emits is
   idempotent, re-running the fixed file is the recovery.

Decided with the pilot on 2026-09-25.

`cpb APPLY <file> --dry-run` does step 1 and reports what step 2 would do.

## Examples

```
cpb CREATE ENV evren-router SET ANTHROPIC_BASE_URL=http://tr0:20128/v1 ANTHROPIC_MODEL=glm-5.3
cpb ALTER ENV evren-router SET ANTHROPIC_AUTH_TOKEN FROM 'keychain:9router-client'
cpb ALTER ENV evren-router UNSET ANTHROPIC_MODEL
cpb ALTER PLAYBOOK kommander-idea USE ENV glm-5.3 deepseek-flash
cpb ALTER PLAYBOOK kommander-idea ADD ENV claude-metu FIRST
cpb ALTER PLAYBOOK kommander-idea ADD ENV evren-router AFTER glm-5.3
cpb ALTER PLAYBOOK kommander-idea DROP ENV deepseek-flash
cpb ALTER PLAYBOOK kommander-idea BLOCK VAR HTTP_PROXY
cpb ALTER DEFAULTS USE ENV claude-default metu-proxy
cpb ALTER DEFAULTS SET SECRET HELPER 'my-keychain-helper'
cpb CREATE PLAYBOOK kommander-x FROM https://github.com/ramazanpolat/kommander-playbook ALIAS kx
cpb ALTER PLAYBOOK kommander-x ALIAS kxx
cpb ALTER PLAYBOOK kommander-x RENAME TO kommander-lab
cpb DROP PLAYBOOK kommander-lab
cpb SHOW ENVS
cpb EXPLAIN PLAYBOOK kommander-idea
cpb SHOW CREATE ALL > playbook.cpb
cpb APPLY playbook.cpb --dry-run
```

## Pre-grammar commands: a hidden fallback

Decided with the pilot on 2026-09-26: the grammar release is **v3.20.0**
and breaks nothing. The pre-grammar state-changing commands stay working as a
fallback, in case the new code is buggy, and are removed in a later release
the pilot names (that one is v4.0.0).

- **Hidden:** `env`, `env-profile`, `create <name>`, `link`, `delete`,
  `rename`, `alias`, `dealias`, `list`, `info` are hidden from help and
  completion and keep working with all their flags. Nothing is printed on
  stdout; when stderr is a terminal, one line goes there:
  `(hidden command; the grammar form is: cpb …)`, with the exact statement
  for the given arguments where it can be derived. Scripts see no change.
- **Their own code paths.** They are **not** re-implemented on the new
  engine: a bug there must not take the fallback down with it. The two paths
  share the same files, so the old code must tolerate the grammar's two
  format additions: rewriting a manifest preserves `[env.refs]`, and reading
  `.env-profiles/.default` accepts a multi-line list (`env-profile P default`
  still replaces it with `P`). Tests pin both on the old paths.
- **Visible and unchanged:** the action commands `run`, `start`, `update`,
  `auth`, `completion`, `self-uninstall`, and `install <source>`, the one
  shortcut: clone, install and create the launcher in one step, taking the
  name and launcher from the source's manifest (sugar for `CREATE PLAYBOOK
  <name> FROM <source> ALIAS <launcher>`). `SHOW CREATE` always writes the
  long form.
- **`create` routing:** if the word after `create` is an object keyword
  (`PLAYBOOK`, `ENV`, or `OR`), the grammar applies; otherwise the hidden
  `create`. That is one more reason keywords are not valid names.
- `AS PLAINTEXT` stays regardless: the hidden commands are a fallback, not
  the plain-text route.

**Migration** (the table the stderr hints and the docs draw on):

| Hidden | Grammar |
|---|---|
| `create <n> [--alias a \| --no-alias] [--sandbox]` | `CREATE PLAYBOOK <n> [ALIAS a \| NO ALIAS] [SANDBOX]` |
| `link <target> [--name n] [--alias a \| --no-alias]` | `CREATE PLAYBOOK n LINK <target> [ALIAS a \| NO ALIAS]` (`n` defaults to the target's basename) |
| `delete <n> [--yes]` | `DROP PLAYBOOK <n> [--yes]` |
| `rename <a> <b> [--alias x \| --no-alias]` | `ALTER PLAYBOOK <a> RENAME TO <b> [ALIAS x \| NO ALIAS]` |
| `alias <n> <a>` | `ALTER PLAYBOOK <n> ALIAS <a>` |
| `alias <n> --remove`, `dealias <n>` | `ALTER PLAYBOOK <n> NO ALIAS` |
| `list [prefix]`, `alias` | `SHOW PLAYBOOKS` (the prefix filter is gone) |
| `info <n> [--reveal]` | `SHOW PLAYBOOK <n>` (no `--reveal` in the grammar) |
| `env <n> set K=V ...` | `ALTER PLAYBOOK <n> SET VAR K=V ...` |
| `env <n> unset K ...` | `ALTER PLAYBOOK <n> BLOCK VAR K ...` |
| `env <n> clear K ...` | `ALTER PLAYBOOK <n> UNSET VAR K ...` |
| `env <n> use P Q` / `unuse P Q` | `ALTER PLAYBOOK <n> ADD ENV P ADD ENV Q` (one `ADD ENV` per set; an attached set moves to the end) / `DROP ENV P Q` |
| `env <n> [--reveal]` | `EXPLAIN PLAYBOOK <n>` |
| `env-profile P set K=V ...` | `ALTER ENV P SET K=V ...` (`CREATE ENV` when new) |
| `env-profile P unset K ...` / `clear K ...` | `ALTER ENV P BLOCK K ...` / `UNSET K ...` |
| `env-profile P describe TEXT` | `ALTER ENV P DESCRIBE 'TEXT'` |
| `env-profile P default` / `undefault` | `ALTER DEFAULTS USE ENV P` (replaces the list, as `default` replaced the single default) / `ALTER DEFAULTS DROP ENV P` |
| `env-profile P delete` | `DROP ENV P` |
| `env-profile [--values] [--reveal]` | `SHOW ENVS` |

## Compatibility

- Nothing breaks in v3.20.0: the pre-grammar commands keep working, hidden.
  Scripts should move to `SHOW … --json` (below) before the removal release.
- The manifest (`.playbook` `[env]`) and `.env-profiles/*.toml` formats gain
  one table, `refs`; everything else is unchanged.
- `.env-profiles/.default` changes from one name to one name per line. A
  single-name file is read as a one-element list, so existing machines need
  no migration. An older cpb reading a multi-line file refuses it rather than
  guess, so downgrading after `ALTER DEFAULTS` with two sets needs a
  one-line edit.

## Output

Every `SHOW` and `EXPLAIN` has two forms. The **human form** is for reading;
its layout may change between releases. The **`--json` form** is the
contract for scripts: fields may be added, and an existing field never
changes meaning within a major version. Nothing should grep the human form.

A variable, wherever it appears, is one JSON object with exactly one of:

```
{"key": "MODEL",   "value": "glm-5.3"}                       literal
{"key": "TOKEN",   "ref": "keychain:9router"}                secret by reference
{"key": "API_KEY", "redacted": true, "plaintext": true}      credential-looking literal: value never shown
{"key": "HTTP_PROXY", "blocked": true}                       BLOCK
```

**`SHOW PLAYBOOK <name>`**, human form (one `Label:` per line; labels
aligned; `Version:` keeps today's `info` spelling):

```
Name:       kommander-idea
Version:    3.12.2
Path:       /Users/polat/.claude-playbooks/kommander-idea
Source:     https://github.com/ramazanpolat/kommander-playbook (branch v3.12.2)
Launcher:   ki
Env sets:   evren-router, glm-5.3
Variables:  MAX_THINKING_TOKENS=8000
            ANTHROPIC_AUTH_TOKEN <from keychain:9router>
            HTTP_PROXY (blocked)
Sandbox:    no
```

`Source:` reads `(linked) <dir>` for a linked playbook and `(none)` for one
created empty; `Launcher:` reads `(none)` without one.

`--json`, one object:

```
{"name": "kommander-idea", "version": "3.12.2",
 "path": "/Users/polat/.claude-playbooks/kommander-idea",
 "source": {"url": "https://github.com/ramazanpolat/kommander-playbook", "branch": "v3.12.2", "subdir": null},
 "linked": null,
 "launcher": "ki",
 "envs": ["evren-router", "glm-5.3"],
 "vars": [<variable>, ...],
 "sandbox": false}
```

`source` is null for a playbook without one; `linked` is the target directory
of a linked playbook, else null; `launcher` is null without one.

**`SHOW PLAYBOOKS`**: human form, one header line and then one line per
playbook sorted by name, columns `NAME VERSION LAUNCHER ENV SETS SOURCE`
(`-` for none). `--json`: an array of the `SHOW PLAYBOOK` objects.

**`SHOW ENV <name>`**: human form, `Name:`, `Description:`, `Used by:`,
`Default:` (yes/no), then `Variables:` as above. `--json`:

```
{"name": "evren-router", "description": "GLM via 9router on tr0",
 "vars": [<variable>, ...], "used_by": ["kommander-idea"], "default": false}
```

**`SHOW ENVS`**: human form, one line per set, columns
`NAME SET BLOCKED USED BY DESCRIPTION`, a `*` after the name of each set in
`DEFAULTS`. `--json`: an array of the `SHOW ENV` objects.

**`SHOW DEFAULTS`**: human form, `Env sets:` in order, and
`Secret helper:` with the command and where it came from (`setting` or
`CPB_SECRET_HELPER`), or `(none)`. `--json`:

```
{"envs": ["claude-default", "metu-proxy"],
 "secret_helper": {"command": "my-keychain-helper", "from": "setting"}}
```

(`secret_helper` is null when none is configured.)

**`EXPLAIN PLAYBOOK <name>`**: human form as in *Layers at launch*.
`--json`:

```
{"playbook": "kommander-idea",
 "vars": [{<variable>, "layer": {"kind": "ENV", "name": "evren-router"}}, ...],
 "secret_helper": {"command": "...", "from": "setting"} | null}
```

`layer.kind` is `DEFAULTS` (with `name` the env set), `ENV` or `PLAYBOOK`.

## SELECT (v3.21.0)

Decided with the pilot on 2026-09-26; implemented after v3.20.0 ships.
`SELECT` queries the same state `SHOW` prints, as tables. **Files stay the
only store:** each query builds its tables from the playbooks, env sets and
DEFAULTS as they are on disk at that moment, and nothing is cached or kept.

```
select  := SELECT { * | count() | <column> ... } FROM <table>
           [WHERE <condition>]
           [ORDER BY <column> [ASC | DESC] ...]
           [LIMIT <n>]
           [FORMAT { Pretty | TSV | JSON | JSONEachRow }]
         | SHOW TABLES [FORMAT …]
         | DESCRIBE <table> [FORMAT …]
```

`SHOW TABLES` lists the tables with their row counts; `DESCRIBE <table>`
lists a table's columns and their types (`String`, `Bool`, `Nullable(…)`,
`Array(String)`, `Object`), as in ClickHouse. `DESCRIBE` is also the clause
that sets an env set's description (`ALTER ENV e DESCRIBE '…'`). The
position tells them apart: as the first word of a statement it is the verb,
inside `ALTER ENV` / `CREATE ENV` it is the clause.

`select` is a third kind of `command`, beside `write` and `read`; like a
read, it never writes. Column and `ORDER BY` lists follow the list rule of
*Shape*: items are separated by spaces, and the SQL habit `name, version`
works because an unquoted comma at the end of an item is read as a
separator.

**Tables.** Their columns are exactly the `--json` fields of *Output*; there
is no second schema. The one addition is two `VARS` columns that place a
row, `playbook` and `effective`, because a `VARS` row is a variable seen from
one playbook, which the *Output* variable object never needs to say. A nested object is addressed with a dot
(`source.url`, `layer.kind`), and an array is filtered with `HAS`.

| Table | One row per | Columns |
|---|---|---|
| `PLAYBOOKS` | playbook | `name version path source linked launcher envs vars sandbox` (the `SHOW PLAYBOOK` object) |
| `ENVS` | env set | `name description vars used_by default` (the `SHOW ENV` object) |
| `VARS` | variable, per layer, per playbook | `playbook key value ref redacted plaintext blocked layer effective` (with `layer.kind`, `layer.name`) |
| `DEFAULTS` | (one row) | `envs secret_helper` (the `SHOW DEFAULTS` object) |

A `VARS` row is one variable as one layer of one playbook's launch declares
it: the *Output* variable object (`key`, `value`, `ref`, `redacted`,
`plaintext`, `blocked`, and `layer`, the same object `EXPLAIN --json` gives:
`layer.kind` is `DEFAULTS`, `ENV` or `PLAYBOOK`, `layer.name` the env set,
absent for `PLAYBOOK`), plus `playbook`, and `effective`, which is true on the
one row per `(playbook, key)` that decides the launch, which is the row
`EXPLAIN` shows. So `WHERE effective` is `EXPLAIN` for every playbook at once,
and the other rows show what was overridden.

**Conditions.** `=`, `!=`, `<`, `>`, `LIKE` (`%` and `_`), `HAS` (an array
contains a value), `IS NULL`, `IS NOT NULL`, combined with `AND`, `OR`, `NOT`
and parentheses. Literals are single-quoted strings, integers, `true` and
`false`. A bare boolean column is a condition (`WHERE effective`).
Comparisons are on strings as text; `version` is compared as text.

**Only what is listed.** `count()` is the one aggregate, and only without
grouping. There are no joins, no `GROUP BY`, no subqueries, and no writes:
`SELECT` only reads, so a playbook file refuses it like any other read.

**Formats.** `Pretty` (the default: an aligned table) and `TSV` (tab-separated,
no header) are for people and `cut`; `JSON` is one array; `JSONEachRow` is one
object per line, which pipes straight into real ClickHouse
(`… FORMAT JSONEachRow | clickhouse-client -q "INSERT INTO t FORMAT
JSONEachRow"`). In `TSV` a nested object or array is printed as its JSON
text. cpb embeds and calls no database.

**Secrets.** A `SELECT` can never produce a value that `SHOW` or `EXPLAIN`
would redact: the rows are the *Output* objects, with the same redaction, so
a credential-looking literal has `value` null and `redacted` true in every
format, and a reference is a reference. The tests check the output bytes of
every format for the secret, as `SHOW`'s do. Filtering on a redacted value
is impossible by construction: its `value` is null.

**Keywords.** `SELECT`, `WHERE`, `ORDER`, `BY`, `ASC`, `DESC`, `LIMIT`,
`FORMAT`, `LIKE`, `HAS`, `IS`, `NULL`, `AND`, `TABLES` and the table name
`VARS` join the reserved words (with `OR`, `NOT`, `FROM`, `PLAYBOOKS`, `ENVS`,
`DEFAULTS` already there). None collides with a playbook, env set or
launcher name on the pilot's machine (checked 2026-09-26: 85 names,
`TABLES` included).
Format names and `count` are not reserved.

**On the command line.** `*`, `<`, `>` and `(` mean something to the shell,
so a query is best passed as **one quoted argument** (decided 2026-09-26),
and the rule is exact:

- It applies when the whole command line after the global flags is **exactly
  one word, and that word contains whitespace**. The word is read with the
  playbook-file lexer as exactly one statement: single quotes, doubled quotes
  and `-- ` comments work, a trailing `;` is optional, and a second
  statement is an error.
- Reads are allowed in this form, unlike in a playbook file: it is still the
  command line.
- Everything else is read word by word, as the shell split it, as before.
- **The unquoted `*`.** `cpb SELECT * FROM PLAYBOOKS` without quotes is
  globbed by the shell: zsh stops with "no matches found", and bash silently
  passes the file names in the current directory. cpb does not probe the
  filesystem to guess at this. The unknown-column error carries one fixed
  sentence instead: "unknown column 'X' (columns: …). If you typed *
  unquoted, the shell expanded it: quote the statement." A test pins the
  message. zsh users can add `alias cpb='noglob cpb'` to their shell; cpb
  installs nothing.

This works for any statement:

```
cpb "SELECT name, version FROM PLAYBOOKS WHERE envs HAS 'glm-5.3' ORDER BY name"
cpb "SELECT playbook, key, layer.kind, layer.name FROM VARS WHERE effective AND ref IS NOT NULL"
cpb "SELECT count() FROM PLAYBOOKS WHERE version < '3.12'"
cpb "SELECT * FROM ENVS WHERE default FORMAT JSONEachRow" | clickhouse-client -q "INSERT INTO envs FORMAT JSONEachRow"
```

**Not planned.** The scope above is closed (decided 2026-09-26): `SELECT`
columns or `*` `FROM` a table, `WHERE` with the listed operators, `ORDER BY`,
`LIMIT`, `count()`, `FORMAT`, plus `SHOW TABLES` and `DESCRIBE <table>`.
Anything beyond it needs the pilot's explicit approval first: functions,
`GROUP BY`, joins, subqueries, expressions or aliases in the column list, a
history or journal table, and variables, loops or conditionals in playbook
files. It is not added because it would be easy.

## INCLUDE (v3.21.0)

Decided with the pilot on 2026-09-26; implemented after v3.20.0 ships. A
playbook file can pull in another, so one machine's file can share a base
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
  refused. A playbook file runs with the pilot's authority, so what it pulls
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
- `INCLUDE` is a keyword and joins the reserved words. An existing object
  whose name is a keyword stays reachable: `SHOW CREATE` quotes such a name,
  and a quoted keyword is accepted as a new name, so its output still
  applies (a change to the reserved-word rule, made with this section).

**Secret references and a helper set in the same run.** A playbook file may
set the secret helper and use it: the reference check that runs before
anything is written uses, for each `SET … FROM`, the helper an earlier
`ALTER DEFAULTS SET SECRET HELPER` of the expanded set would configure, and
the configured one otherwise. (The same rule applies to `APPLY` without
`INCLUDE`.)

**Not planned** in playbook files: variables, loops and conditionals. A
playbook file stays a flat list of statements that reads the same every
time; anything more needs the pilot's explicit approval first.

## Completion

Every slot has a closed set, so TAB completes verbs, then objects, then
existing names of that object, then the clause keywords valid for it, then
keys (from the object's current entries).
