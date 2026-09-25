# CLI grammar — spec (draft)

Status: **draft for the pilot's review.** Nothing here is implemented yet.
Decided with the pilot on 2026-09-25; the open points are listed at the end.

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
     ALTER    PLAYBOOK   k-idea   USE ENV evren-router  SET ENV FOO=1
```

- Keywords are **case-insensitive**. Docs write them in capitals.
- Clauses are **two words** (`SET ENV`, `USE PILOT`), never hyphenated.
- Lists are **space-separated**. No commas; a trailing comma on a token is
  tolerated and ignored.
- Names are lowercase-with-dashes. A **keyword is not a valid name**
  (refused with an error naming the keyword).

## Objects

| Object | What it is | Lives at |
|---|---|---|
| `PLAYBOOK` | an installed playbook: dir, launcher, source, attached ENVs and PILOT, own env block | `<root>/<name>/` |
| `ENV` | a named, reusable set of environment variables (formerly "env profile") | `<root>/.env-profiles/<name>.toml` |
| `PILOT` | a pilot profile — one persona of the pilot — attachable to a playbook | `~/.pilots/<name>/` (pilot-profile) |
| `DEFAULTS` | the machine-wide layer under every playbook; a singleton, no name | the registry |

"Profile" is deliberately not a keyword: it would be ambiguous between an
`ENV` and a `PILOT`.

## Grammar

```
command    := write | read

write      := CREATE ENV <name> [env-clause ...]
            | ALTER  ENV <name> env-clause ...
            | DROP   ENV <name>
            | CREATE PLAYBOOK <name> [FROM <source>] [BRANCH <ref>] [SUBDIR <dir>]
                                     [LINK <dir>] [ALIAS <launcher>] [NO ALIAS] [SANDBOX]
            | ALTER  PLAYBOOK <name> pb-clause ...
            | DROP   PLAYBOOK <name>
            | ALTER  DEFAULTS pb-clause ...

env-clause := SET <key>=<value> ...        literal values
            | SET <key> FROM '<ref>'       secret by reference; resolved at launch
            | BLOCK <key> ...              removed at launch even if the shell exports it
            | UNSET <key> ...              forgotten; the layer below applies again
            | DESCRIBE '<text>'

pb-clause  := USE ENV <env> ...            attach, in order; later wins
            | DROP ENV <env> ...           detach
            | USE PILOT <pilot>            attach a pilot (one per playbook)
            | DROP PILOT
            | SET ENV <key>=<value> ...    the playbook's own layer
            | SET ENV <key> FROM '<ref>'
            | BLOCK ENV <key> ...
            | UNSET ENV <key> ...          forget the playbook's own entry (set or block)
            | RENAME TO <name>
            | ALIAS <launcher>
            | NO ALIAS

read       := SHOW PLAYBOOKS | SHOW ENVS | SHOW PILOTS
            | SHOW PLAYBOOK <name> | SHOW ENV <name>
            | EXPLAIN PLAYBOOK <name>
```

Inside `ALTER ENV` the clauses omit the word `ENV` (`SET FOO=1`): the object
already says it. Inside `ALTER PLAYBOOK` they keep it (`SET ENV FOO=1`),
because a playbook has other things to set.

One rule keeps the verbs apart: **`DROP` acts on objects** (an ENV, a
PILOT, a playbook) and **`UNSET` acts on variables**. So `DROP ENV
evren-router` inside `ALTER PLAYBOOK` detaches that ENV, and `UNSET ENV FOO`
forgets the playbook's own `FOO`.

Clauses in one command apply **atomically**: all or none, validated before
anything is written.

## Layers at launch

```
shell  ->  DEFAULTS  ->  USE ENV (listed order)  ->  playbook's own SET / BLOCK
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

`SET <key> FROM '<ref>'` stores the **reference** only (`keychain:…`,
`op://…`, `env:…`, `file:…`), in the pilot-profile reference forms. cpb
resolves it at launch through `with-secret` and hands the value to the child
process only. It never appears in a file, in argv, or in any `SHOW`/`EXPLAIN`
output.

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
  other command is affected.
- The pilot side (`~/.pilots/<name>/`, `<install>/.pilot`, `pilot list/new/
  default`) is owned and specified by pilot-profile, not here.

## Examples

```
cpb CREATE ENV evren-router SET ANTHROPIC_BASE_URL=http://tr0:20128/v1 ANTHROPIC_MODEL=glm-5.3
cpb ALTER ENV evren-router SET ANTHROPIC_AUTH_TOKEN FROM 'keychain:pilot/9router-client'
cpb ALTER ENV evren-router UNSET ANTHROPIC_MODEL
cpb ALTER PLAYBOOK kommander-idea USE ENV evren-router USE PILOT ramazan-metu
cpb ALTER PLAYBOOK kommander-idea BLOCK ENV HTTP_PROXY
cpb ALTER DEFAULTS USE ENV claude-default
cpb CREATE PLAYBOOK kommander-x FROM https://github.com/ramazanpolat/kommander-playbook ALIAS kx
cpb ALTER PLAYBOOK kommander-x RENAME TO kommander-lab
cpb DROP PLAYBOOK kommander-lab
cpb SHOW ENVS
cpb EXPLAIN PLAYBOOK kommander-idea
```

## Mapping from today's commands

| Today | New |
|---|---|
| `create <n> [--alias a]` | `CREATE PLAYBOOK <n> [ALIAS a]` |
| `install <src> --name n --branch b --subdir d` | `CREATE PLAYBOOK n FROM <src> BRANCH b SUBDIR d` |
| `link <dir> <n>` | `CREATE PLAYBOOK <n> LINK <dir>` |
| `delete <n>` | `DROP PLAYBOOK <n>` |
| `rename <a> <b>` | `ALTER PLAYBOOK <a> RENAME TO <b>` |
| `alias <n> <a>` / `dealias <n>` | `ALTER PLAYBOOK <n> ALIAS <a>` / `NO ALIAS` |
| `list`, `info <n>` | `SHOW PLAYBOOKS`, `SHOW PLAYBOOK <n>` |
| `env <n> set K=V` | `ALTER PLAYBOOK <n> SET ENV K=V` |
| `env <n> unset K` | `ALTER PLAYBOOK <n> BLOCK ENV K` |
| `env <n> clear K` | `ALTER PLAYBOOK <n> UNSET ENV K` |
| `env <n> use P` / `unuse P` | `ALTER PLAYBOOK <n> USE ENV P` / `DROP ENV P` |
| `env <n>` | `EXPLAIN PLAYBOOK <n>` |
| `env-profile P set K=V` | `ALTER ENV P SET K=V` (`CREATE ENV` when new) |
| `env-profile P unset K` / `clear K` | `ALTER ENV P BLOCK K` / `UNSET K` |
| `env-profile P default` / `undefault` | `ALTER DEFAULTS USE ENV P` / `DROP ENV P` |
| `env-profile P delete` | `DROP ENV P` |
| `env-profile` | `SHOW ENVS` |

Action commands stay as they are, lowercase verbs with no object: `run`,
`start`, `update`, `auth`, `completion`, `self-uninstall`. They do
something rather than change state, and forcing them into DDL shape would
make them harder to read, not easier.

## Compatibility

- Today's commands keep working as **hidden aliases** for one minor release,
  printing a one-line hint with the new form; the release after removes them.
- Scripts that parse today's output are not covered; `SHOW` output is new.
- The manifest (`.playbook` `[env]`) and `.env-profiles/*.toml` formats do
  not change; the grammar is a new front end over the same state. `SET … FROM`
  adds one field (`ref`) to an env entry.

## Completion

Every slot has a closed set, so TAB completes verbs, then objects, then
existing names of that object, then the clause keywords valid for it, then
keys (from the object's current entries).

## Open points

1. `FROM` means *source* in `CREATE PLAYBOOK` and *secret reference* in
   `SET`. Position disambiguates; the alternative is `REF` for secrets.
   Draft keeps `FROM`.
2. Hidden-alias window: one minor release (draft) or two.
