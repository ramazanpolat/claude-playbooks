# CLI grammar — spec (draft)

Status: **draft for the pilot's review.** Nothing here is implemented yet.
Decided with the pilot on 2026-09-25; no open points remain.

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
- Clauses are **two words** (`SET VAR`, `USE PILOT`), never hyphenated.
- Lists are **space-separated**. No commas; a trailing comma on a token is
  tolerated and ignored.
- Names are lowercase-with-dashes. A **keyword is not a valid name**
  (refused with an error naming the keyword).

## Objects

| Object | What it is | Lives at |
|---|---|---|
| `PLAYBOOK` | an installed playbook: dir, launcher, source, attached ENVs and PILOT, own variables | `<root>/<name>/` |
| `ENV` | an **env set**: a named, reusable set of variables (formerly "env profile") | `<root>/.env-profiles/<name>.toml` |
| `PILOT` | a pilot profile — one persona of the pilot — attachable to a playbook | `~/.pilots/<name>/` (pilot-profile) |
| `DEFAULTS` | the machine-wide layer under every playbook: an ordered list of env sets; a singleton, no name | `<root>/.env-profiles/.default` |

Two words keep the variables apart: **`ENV` is a named set**, **`VAR` is one
variable**. "Profile" is deliberately not a keyword: it would be ambiguous
between an `ENV` and a `PILOT`.

## Grammar

```
command    := write | read | APPLY <file> [--dry-run]

write      := CREATE [OR REPLACE] ENV [IF NOT EXISTS] <name> [env-clause ...]
            | ALTER  ENV <name> env-clause ...
            | DROP   ENV [IF EXISTS] <name>
            | CREATE PLAYBOOK [IF NOT EXISTS] <name> [FROM <source>] [BRANCH <ref>]
                     [SUBDIR <dir>] [LINK <dir>] [ALIAS <launcher>] [NO ALIAS] [SANDBOX]
            | ALTER  PLAYBOOK <name> pb-clause ...
            | DROP   PLAYBOOK [IF EXISTS] <name>
            | ALTER  DEFAULTS set-clause ...

env-clause := SET [VAR] <key>=<value> ...  literal values
            | SET [VAR] <key> FROM '<ref>' secret by reference; resolved at launch
            | BLOCK [VAR] <key> ...        removed at launch even if the shell exports it
            | UNSET [VAR] <key> ...        forgotten; the layer below applies again
            | DESCRIBE '<text>'

set-clause := USE ENV <env> ...            replace the attached list with exactly these, in order
            | ADD ENV <env> [FIRST | LAST | BEFORE <env> | AFTER <env>]
                                           insert one (default LAST)
            | DROP ENV <env> ...           detach

pb-clause  := set-clause
            | USE PILOT <pilot>            attach a pilot (one per playbook)
            | DROP PILOT
            | SET VAR <key>=<value> ...    the playbook's own layer
            | SET VAR <key> FROM '<ref>'
            | BLOCK VAR <key> ...
            | UNSET VAR <key> ...          forget the playbook's own entry (set, ref or block)
            | RENAME TO <name>
            | ALIAS <launcher>             set or replace the launcher (one per playbook)
            | NO ALIAS                     remove the launcher

read       := SHOW PLAYBOOKS | SHOW ENVS | SHOW PILOTS | SHOW DEFAULTS
            | SHOW PLAYBOOK <name> | SHOW ENV <name>
            | SHOW CREATE PLAYBOOK <name> | SHOW CREATE ENV <name> | SHOW CREATE ALL
            | EXPLAIN PLAYBOOK <name>
```

Inside `ALTER ENV` the word `VAR` is optional (`SET FOO=1`): the object
already says it. Inside `ALTER PLAYBOOK` it is required (`SET VAR FOO=1`),
because a playbook has other things to set.

Order matters for env sets: a later set overrides an earlier one. `USE ENV a b`
states the whole list, so it is the idempotent form; `ADD ENV` places one set
without restating the rest.

One rule keeps the verbs apart: **`DROP` acts on objects** (an ENV, a
PILOT, a playbook) and **`UNSET` acts on variables**. So `DROP ENV
evren-router` inside `ALTER PLAYBOOK` detaches that set, and `UNSET VAR FOO`
forgets the playbook's own `FOO`.

`IF NOT EXISTS` / `IF EXISTS` turn "already there" / "not there" into a no-op
instead of an error; they sit before the name, as in ClickHouse.
`CREATE OR REPLACE ENV` replaces the set's whole content.
`CREATE PLAYBOOK IF NOT EXISTS` never re-clones an existing playbook.

`DROP ENV` is refused while a playbook or `DEFAULTS` uses the set; the error
lists the users.

Clauses in one command apply **atomically**: all or none, validated before
anything is written. Validation includes `pilot wire … --check` for
`USE PILOT` and `with-secret --check` for every `SET … FROM`. `USE PILOT` /
`DROP PILOT` are applied **last**, after cpb's own writes, because they write
outside cpb's state. If one of them fails, cpb restores its own writes; if a
pilot step succeeded and something after it fails, cpb re-wires the previous
pilot from `<install>/.pilot` (or unwires when there was none).

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
| `ALTER DEFAULTS …` | `<root>/.env-profiles/.default`, one set name per line, in order |
| `USE PILOT` / `DROP PILOT` | nothing of cpb's: `pilot wire` / `pilot unwire` write `<install>/.pilot` and the import block in the playbook's `CLAUDE.local.md` |
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
ANTHROPIC_AUTH_TOKEN  <from keychain:pilot/9router>    <- ENV evren-router
ANTHROPIC_MODEL       glm-5.3                          <- PLAYBOOK kommander-idea
HTTP_PROXY            (blocked)                        <- PLAYBOOK kommander-idea
FOO                   bar                              <- DEFAULTS (ENV claude-default)
PILOT                 ramazan-metu                     <- PLAYBOOK kommander-idea
```

## Secrets

`SET <key> FROM '<ref>'` stores the **reference** only. The accepted forms
are pilot-profile's, cited rather than copied so they cannot drift (its
`capture-protocol` *Secrets* section and `ADAPTERS.md`): `keychain:pilot/<name>`,
`keychain:<service>`, `op://…`, `age:<file>#<key>`, `env:…`, `file:…`, and
adapter schemes such as `vault:<path>#<field>`.

`FROM` also names a playbook's source in `CREATE PLAYBOOK`; the position
disambiguates (decided 2026-09-25).

- **At write time** cpb runs `with-secret --check KEY=REF`: presence only,
  never the value. Anything but present fails the command, so a mistyped
  reference is caught before launch.
- **At launch** cpb does not fetch values. It **execs the launch through
  `with-secret`**: `with-secret K1=REF1 [K2=REF2 …] -- claude …`. There is no
  value-returning call and none may be built. On `with-secret` exit 3
  (refused), 4 (missing) or 5 (adapter unavailable) cpb does not launch and
  prints the reason, which never contains the value.
- The value never appears in a file, in argv, or in any `SHOW`/`EXPLAIN`
  output.
- A sandboxed launch of a playbook with references is refused in the first
  release, with a message saying so.

A literal `SET` whose key looks like a credential (`*_TOKEN`, `*_KEY`,
`*_SECRET`, `*PASSWORD*`) is accepted but warned about, pointing at `FROM`.
All output redacts credential-looking literals, as `env` does today; there is
no `--reveal` in the new grammar.

## PILOT

Decided with the pilot on 2026-09-25: the unit is a **PILOT**, pilot-profile
keeps its name, and several pilots on one machine are **personas of one
person** — presence and secrets stay shared.

- `USE PILOT <name>` runs `pilot wire <install> --pilot <name>`;
  `DROP PILOT` (no name: a playbook has at most one) runs
  `pilot unwire <install>`. cpb reads exit codes only, never the output text.
- `DROP PILOT` only **detaches**. cpb never creates or deletes a pilot: a
  pilot is the pilot's memory, managed with `pilot new` and `pilot`'s own
  commands.
- Exit codes, per pilot-profile's wire contract v2:

  | `pilot wire --pilot` | cpb says |
  |---|---|
  | 0 ok (also an idempotent re-wire, or a switch to another pilot) | done |
  | 1 usage, including a malformed pilot name | the name is invalid |
  | 2 bad target | internal error: cpb passed a bad install path |
  | 3 write failed | could not write the playbook's files |
  | 4 no such pilot | no pilot `<name>`; create it with `pilot new <name>` |

  `pilot unwire` returns 0 (also when not wired), 2 or 3; never 4.
- Without `pilot` on `PATH`, `USE PILOT` fails with a one-line message; no
  other command is affected. A `pilot` too old for `--pilot` / `--check`
  answers usage (1) to `--check`; cpb reports "pilot-profile too old for USE
  PILOT".
- `SHOW PILOTS` parses `pilot list --json`
  (`[{"name", "path", "default"}]`), never the human form.
- The pilot side (`~/.pilots/<name>/`, `<install>/.pilot`, `pilot list/new/
  default`, `wire --check`) is owned and specified by pilot-profile, not here.

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
  SET ANTHROPIC_AUTH_TOKEN FROM 'keychain:pilot/9router-client';

CREATE OR REPLACE ENV claude-default
  BLOCK HTTP_PROXY;

ALTER DEFAULTS USE ENV claude-default;

CREATE PLAYBOOK IF NOT EXISTS kommander-idea
  FROM https://github.com/ramazanpolat/kommander-playbook BRANCH v3.12.2 ALIAS ki;

ALTER PLAYBOOK kommander-idea
  USE ENV evren-router
  USE PILOT ramazan-metu
  SET VAR MAX_THINKING_TOKENS=8000
  BLOCK VAR CLAUDE_CODE_OAUTH_TOKEN;
```

File rules:

- **Only write statements** (`CREATE`, `ALTER`, `DROP`). A read is refused.
- **`;` ends a statement**, newlines are whitespace, `--` starts a comment.
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
   `with-secret --check`, `pilot wire --check`, sources reachable — and
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
cpb ALTER ENV evren-router SET ANTHROPIC_AUTH_TOKEN FROM 'keychain:pilot/9router-client'
cpb ALTER ENV evren-router UNSET ANTHROPIC_MODEL
cpb ALTER PLAYBOOK kommander-idea USE ENV glm-5.3 deepseek-flash
cpb ALTER PLAYBOOK kommander-idea ADD ENV claude-metu FIRST
cpb ALTER PLAYBOOK kommander-idea ADD ENV evren-router AFTER glm-5.3
cpb ALTER PLAYBOOK kommander-idea DROP ENV deepseek-flash
cpb ALTER PLAYBOOK kommander-idea USE PILOT ramazan-metu BLOCK VAR HTTP_PROXY
cpb ALTER DEFAULTS USE ENV claude-default metu-proxy
cpb CREATE PLAYBOOK kommander-x FROM https://github.com/ramazanpolat/kommander-playbook ALIAS kx
cpb ALTER PLAYBOOK kommander-x ALIAS kxx
cpb ALTER PLAYBOOK kommander-x RENAME TO kommander-lab
cpb DROP PLAYBOOK kommander-lab
cpb SHOW ENVS
cpb EXPLAIN PLAYBOOK kommander-idea
cpb SHOW CREATE ALL > setup.cpb
cpb APPLY setup.cpb --dry-run
```

## Today's commands

**They stay, indefinitely** (decided 2026-09-25): none of them clashes with
the grammar, so there is no reason to remove them. They are not deprecated
and print no nag.

- **`install <source>` is a first-class shortcut**, shown in help like `run`
  and `update`: clone, install and create the launcher in one step, taking
  the name and launcher from the source's manifest. It is sugar for
  `CREATE PLAYBOOK <name> FROM <source> ALIAS <launcher>`. `SHOW CREATE`
  always writes the long form.
- The other state-changing commands (`create`, `link`, `delete`, `rename`,
  `alias`, `dealias`, `list`, `info`, `env`, `env-profile`) keep working with
  their flags. Help leads with the grammar and lists these under
  "short forms".
- They are re-implemented as translations onto the same engine, so there is
  one code path and one set of rules; their existing tests become the
  compatibility suite.
- **The one routing rule:** `create` is both a legacy command
  (`cpb create <name>`) and a grammar verb. If the word after it is an object
  keyword (`PLAYBOOK`, `ENV`, …), the grammar applies; otherwise the legacy
  command. That is one more reason keywords are not valid names.

| Short form | Grammar |
|---|---|
| `install <src> [--name n] [--branch b] [--subdir d] [--alias a]` | `CREATE PLAYBOOK n FROM <src> [BRANCH b] [SUBDIR d] [ALIAS a]` |
| `create <n> [--alias a]` | `CREATE PLAYBOOK <n> [ALIAS a]` |
| `link <dir> <n>` | `CREATE PLAYBOOK <n> LINK <dir>` |
| `delete <n>` | `DROP PLAYBOOK <n>` |
| `rename <a> <b>` | `ALTER PLAYBOOK <a> RENAME TO <b>` |
| `alias <n> <a>` / `dealias <n>` | `ALTER PLAYBOOK <n> ALIAS <a>` / `NO ALIAS` |
| `list`, `info <n>` | `SHOW PLAYBOOKS`, `SHOW PLAYBOOK <n>` |
| `env <n> set K=V` | `ALTER PLAYBOOK <n> SET VAR K=V` |
| `env <n> unset K` | `ALTER PLAYBOOK <n> BLOCK VAR K` |
| `env <n> clear K` | `ALTER PLAYBOOK <n> UNSET VAR K` |
| `env <n> use P` / `unuse P` | `ALTER PLAYBOOK <n> ADD ENV P` / `DROP ENV P` |
| `env <n>` | `EXPLAIN PLAYBOOK <n>` |
| `env-profile P set K=V` | `ALTER ENV P SET K=V` (`CREATE ENV` when new) |
| `env-profile P unset K` / `clear K` | `ALTER ENV P BLOCK K` / `UNSET K` |
| `env-profile P default` / `undefault` | `ALTER DEFAULTS ADD ENV P` / `DROP ENV P` |
| `env-profile P delete` | `DROP ENV P` |
| `env-profile` | `SHOW ENVS` |

Action commands stay as they are, lowercase verbs with no object: `run`,
`start`, `update`, `auth`, `completion`, `self-uninstall`. They do
something rather than change state, and forcing them into DDL shape would
make them harder to read, not easier.

## Compatibility

- Scripts that parse today's output keep working for the short forms; `SHOW`
  output is new.
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
