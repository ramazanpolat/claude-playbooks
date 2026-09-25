# CLI grammar — spec (draft)

Status: **agreed 2026-09-26; implementation in progress (phase 1, the
parser). Ships as v4.0.0.** Decided with the pilot on 2026-09-25/26; one
open point, listed at the end.

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
- Lists are **space-separated**. No commas; a trailing comma on a token is
  tolerated and ignored.
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
command    := write | read | APPLY <file> [--dry-run]

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

env-clause := SET [VAR] <key>=<value> ...  literal values
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
            | SET VAR <key>=<value> ...    the playbook's own layer
            | SET VAR <key> FROM '<ref>'
            | BLOCK VAR <key> ...
            | UNSET VAR <key> ...          forget the playbook's own entry (set, ref or block)
            | RENAME TO <name>
            | ALIAS <launcher>             set or replace the launcher (one per playbook)
            | NO ALIAS                     remove the launcher

read       := SHOW PLAYBOOKS | SHOW ENVS | SHOW DEFAULTS
            | SHOW PLAYBOOK <name> | SHOW ENV <name>
            | SHOW CREATE { PLAYBOOK <name> | ENV <name> | ALL } [--skip-secrets]
            | EXPLAIN PLAYBOOK <name>
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
FOO                   bar                              <- DEFAULTS (ENV claude-default)
```

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
Existing files that already hold such literals keep working: launch reads
them as before, and output redacts them. Whether the grammar offers an
explicit way to store a new one is the open point below.

All output redacts credential-looking literals, as `env` does today; there is
no `--reveal` in the new grammar.

## setup.cpb: SHOW CREATE and APPLY

A **setup file** (conventionally `setup.cpb`) is plain text holding cpb
statements: the same grammar as the command line, without the `cpb` prefix.
It describes a machine's setup: env sets, DEFAULTS, playbooks and their
wiring. It is the script beside the database, never inside it: the manifest
keeps holding state, the file holds the recipe. It belongs in dotfiles or a
devbox project, and moving a setup to another machine is one command.

```
-- setup.cpb (macminim), from: cpb SHOW CREATE ALL > setup.cpb

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
  never prints a credential-looking literal: it writes a comment telling the
  pilot to convert it to `SET VAR K FROM '<ref>'` and exits non-zero, unless
  `--skip-secrets` is given.
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
cpb SHOW CREATE ALL > setup.cpb
cpb APPLY setup.cpb --dry-run
```

## Pre-grammar commands: removed in v4.0.0

Decided with the pilot on 2026-09-26, replacing the earlier "keep them
indefinitely": the state-changing commands are **removed**, with no hidden
aliases and no translation layer. The grammar is the one way to change state.

- **Removed:** `env`, `env-profile`, `create <name>`, `link`, `delete`,
  `rename`, `alias`, `dealias`, `list`, `info`. Each removed word answers
  with one line naming the statement to use, and exits non-zero:
  `cpb env-profile …` → "`env-profile` was removed in v4.0.0; use: cpb ALTER
  ENV … (see cpb help grammar)". A word that points the way is worth more
  than one that means nothing.
- **Kept, unchanged:** the action commands `run`, `start`, `update`, `auth`,
  `completion`, `self-uninstall`, and `install <source>`, the one shortcut:
  clone, install and create the launcher in one step, taking the name and
  launcher from the source's manifest (sugar for `CREATE PLAYBOOK <name> FROM
  <source> ALIAS <launcher>`). They act rather than manage state. `SHOW
  CREATE` always writes the long form.
- **`create` is only the grammar verb.** `cpb create x` is a statement that
  fails to parse ("CREATE needs an object"), with the expected words listed.
- **Semver:** this breaks every script and document using the removed forms,
  so the release is **v4.0.0**.
- **Tests:** each removed command's tests become tests of the equivalent
  statements (same behaviour, new syntax), plus the removed-word errors.

**Migration** (the table the removed-word errors and the docs draw on):

| Removed | Use instead |
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

- Scripts using the removed commands break (v4.0.0); each removed word
  names its replacement. `SHOW` output is new.
- The manifest (`.playbook` `[env]`) and `.env-profiles/*.toml` formats gain
  one table, `refs`; everything else is unchanged.
- `.env-profiles/.default` changes from one name to one name per line. A
  single-name file is read as a one-element list, so existing machines need
  no migration. An older cpb reading a multi-line file refuses it rather than
  guess, so downgrading after `ALTER DEFAULTS` with two sets needs a
  one-line edit.

## Completion

Every slot has a closed set, so TAB completes verbs, then objects, then
existing names of that object, then the clause keywords valid for it, then
keys (from the object's current entries).

## Open point

1. **A plain-text escape for credentials.** With the short forms gone, a
   pilot without a secret helper has no way to store a credential-looking
   literal (for example an `ANTHROPIC_AUTH_TOKEN` for a router). Options: an
   explicit clause that says what it does (`SET VAR K=V AS PLAINTEXT`), or no
   escape (a helper becomes required for credentials). Raised with the root
   agent 2026-09-26.
