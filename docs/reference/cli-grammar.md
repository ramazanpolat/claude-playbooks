# CLI grammar

Status: **implemented, v3.21.0.** Decided with the pilot on 2026-09-25/26.
Everything on this page is built; a section specified before it is built is
marked **planned**.

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
command    := write | read | select | APPLY <file> [<file> ...] [TO <playbook|dir>] [--dry-run] [--yes]
                                           TO: see "Targets"
                                           select: see SELECT

write      := CREATE ENV [IF NOT EXISTS] <name> [env-clause ...]
            | CREATE OR REPLACE ENV <name> [env-clause ...]
            | ALTER  ENV <name> env-clause ...
            | DROP   ENV [IF EXISTS] <name>
            | CREATE PLAYBOOK [IF NOT EXISTS] <name> [origin] [launcher] [SANDBOX]
            | ALTER  PLAYBOOK [<name>] pb-clause ...   no name: a recipe, see "Targets"
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
            | ADD MARKETPLACE <name> FROM '<source>'   see "Plugins and the agent"
            | DROP MARKETPLACE <name>
            | ADD PLUGIN <plugin>@<marketplace>
            | DROP PLUGIN <plugin>@<marketplace>
            | SET AGENT '<agent>'
            | UNSET AGENT
            | ADD MCP SERVER <name> mcp-target [mcp-part ...]   v3.21.0, see "An agent's configuration"
            | DROP MCP SERVER <name>
            | ALLOW TOOL '<rule>' ...      settings.json permissions.allow
            | DENY TOOL '<rule>' ...       settings.json permissions.deny
            | UNSET TOOL '<rule>' ...      forget a rule, allowed or denied
            | SET STATUSLINE '<command>' | UNSET STATUSLINE
            | SET MODEL '<model>' | UNSET MODEL
            | ADD MODEL '<id>' [LABEL '<text>'] [DESCRIPTION '<text>'] [BEHAVES AS '<id>']   v3.22.0, "Model picker"
            | DROP MODEL '<id>'
            | SET MODEL PICKER ONLY | SET MODEL PICKER APPEND | UNSET MODEL PICKER
            | ADD SKILL <name> FROM '<dir>'
            | ADD SKILL <name> FROM <git-url> [BRANCH <ref>] [SUBDIR <dir>]
            | DROP SKILL <name>

mcp-target := COMMAND '<command>' [ARGS '<arg>' ...]    a stdio server
            | URL '<url>' [TRANSPORT SSE]               a remote server (HTTP unless SSE)
mcp-part   := ENV <key>=<value> ...          literal values; a credential needs FROM
            | ENV <key> FROM '<ref>'
            | HEADER '<name>' '<value>'
            | HEADER '<name>' FROM '<ref>'

read       := SHOW [ PLAYBOOKS | ENVS | DEFAULTS | PLAYBOOK <name> | ENV <name> ] [--json]
            | SHOW CREATE { PLAYBOOK <name> | ENV <name> | ALL } [--skip-secrets]
            | EXPLAIN PLAYBOOK <name> [--json]
```

The alternatives are exclusive, and the parser enforces them: `OR REPLACE`
and `IF NOT EXISTS` cannot be combined; a playbook has one origin, `FROM` or `LINK`,
and `BRANCH` / `SUBDIR` only with `FROM`; `ALIAS` and `NO ALIAS` exclude each
other. The clauses of `origin` and `launcher` may come in any order.
`DROP PLAYBOOK` asks for confirmation on a terminal, as `delete` does;
`--yes` skips it.

Two limits keep every statement whole-or-nothing (decided 2026-09-26):

- `RENAME TO`, `ALIAS` and `NO ALIAS` are not combined with environment or
  variable clauses in one statement: a rename after an environment write
  could not be undone as one step. `RENAME TO <name> ALIAS <launcher>` is
  one statement; the environment change is a second.
- `CREATE PLAYBOOK … LINK <dir>` needs the target to have a `.playbook`: a
  statement never prompts, and the hidden `link` command asks for the
  metadata when it is missing. The error names both ways out: add a
  `.playbook` to the target, or run `claude-playbook link <dir>`
  interactively. `SANDBOX` does not apply to `LINK`, whose manifest belongs
  to the target.

The lifecycle statements (`CREATE`/`DROP PLAYBOOK`, `RENAME TO`, `ALIAS`,
`NO ALIAS`) run the same code as the hidden `install`, `create`, `link`,
`delete`, `rename` and `alias` commands, with the statement's options in
place of flags; that code is not changed by them.

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
every `SET … FROM` (see Secrets). The exception is the clauses that run
Claude Code's own commands or change files under the playbook, the
marketplace, plugin, MCP server and skill clauses: they run in order, the first failure stops the statement and names
what already ran, and running it again finishes it (see "Plugins and the
agent", and "An agent's configuration" for replacing an MCP server).

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
| `ALTER PLAYBOOK … ADD / DROP MARKETPLACE`, `ADD / DROP PLUGIN` | nothing directly: runs `claude plugin …` with the playbook as `CLAUDE_CONFIG_DIR` (see "Plugins and the agent") |
| `ALTER PLAYBOOK … SET / UNSET AGENT` | the playbook's `settings.json`, `agent` |
| `ALTER PLAYBOOK … ADD / DROP MCP SERVER` | nothing directly: runs `claude mcp add-json / remove --scope user` for the playbook; a reference also writes the playbook's `[env.refs]` (see "An agent's configuration") |
| `ALTER PLAYBOOK … ALLOW / DENY / UNSET TOOL` | the playbook's `settings.json`, `permissions.allow` / `permissions.deny` |
| `ALTER PLAYBOOK … SET / UNSET STATUSLINE`, `SET / UNSET MODEL` | the playbook's `settings.json`, `statusLine` / `model` |
| `ALTER PLAYBOOK … ADD / DROP SKILL` | `<playbook>/skills/<name>` (a link or a copy) and the manifest's `[skills.<name>]` record |

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
- **Keys cpb reads itself never take a reference** (decided 2026-09-26):
  today `CLAUDE_CODE_OAUTH_TOKEN`, whose value cpb's authentication handling
  reads to decide injection and credential quarantine. A reference hides the
  value by design, and "set, value unknown" would break that logic silently.
  `SET CLAUDE_CODE_OAUTH_TOKEN FROM …` is refused in a playbook's block and
  in every env set (so under DEFAULTS too), and a hand-written reference for
  it makes the file invalid. The list lives in one place
  (`manifest.RefRefusedKeys`); a future key cpb reads joins it.

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

`cpb APPLY <file> [<file> ...]` (several files decided 2026-09-26):

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

Decided with the pilot on 2026-09-25/26.

`--dry-run` does step 1 and reports what step 2 would do, judging each
statement against the files as they are plus what earlier statements, in
any of the files, would have created.

**Source drift is a warning, never an error** (decided 2026-09-26). When
`CREATE PLAYBOOK IF NOT EXISTS x FROM <source> [BRANCH b] [SUBDIR d]` meets an
existing `x` whose recorded source, branch or subdirectory differs, `APPLY`
reports `PLAYBOOK x exists; source differs (installed <…>, file says <…>)`
and changes nothing; `--dry-run` shows it too, and the summary counts the
warnings. The exit code stays 0 when that is the only issue: moving an
install to another source is `DROP PLAYBOOK` and `CREATE PLAYBOOK`, a
deliberate step.

**A file never consents to `DROP PLAYBOOK`** (decided 2026-09-26). It is the
one irreversible statement: it deletes the install directory, and for a
Kommander install that includes `data/` (tasks and logs). So `APPLY <file>`
refuses at validation, writing nothing, when any of its files contains a
`DROP PLAYBOOK`, and lists each one as `file:line`, unless `--yes` is given;
`--yes` covers the drops in all of them.
`--dry-run` shows the drops, and what each would delete, without `--yes`.
`SHOW CREATE` never emits `DROP PLAYBOOK`, so a drop in a file is always
hand-written, which is exactly when a second confirmation is worth it.
`DROP ENV` and the `DROP ENV` clause only detach or delete an env set file
and need no `--yes`.

### APPLY --dry-run --json

Built for v3.22.0. The schema was confirmed 2026-09-27 with root and with
cockpit, its first consumer. `cpb APPLY <file> … [TO <target>] --dry-run
--json` prints the plan as **one JSON object on stdout**, in every case,
refusals included. It follows the `--json` rule: fields may be added, and
none changes meaning within a major version. `schema` is bumped only on a
meaning change, and so only with a major version; a consumer refuses a
schema it does not know. **Verdicts, action types and warning codes are
closed sets** within a major version.

`--json` needs `--dry-run`: the JSON form is a plan, and a real `APPLY
--json` is not in this release. The human lines of the dry run go to
stderr.

A dry run **creates nothing**: not the store (`CLAUDE_PLAYBOOKS_DIR`),
nothing under it, and no lock file. A store that does not exist yet is
planned as empty.

```
{
  "schema": 1,
  "cpb_version": "v3.22.0",
  "files": ["/abs/main.cpb", "/abs/base.cpb"],
  "target": {"kind": "playbook", "name": "fresh"},
  "ok": true,
  "error": null,
  "warnings": [{"code": "use_playbook_overridden", "file": "/abs/main.cpb", "line": 2,
                "message": "USE PLAYBOOK other is ignored: TO fresh sets the target"}],
  "statements": [
    {"file": "/abs/main.cpb", "line": 3, "statement": "ALTER PLAYBOOK fresh",
     "verb": "ALTER", "object": "PLAYBOOK", "target": {"kind": "playbook", "name": "fresh"},
     "recipe": true, "implicit": false, "verdict": "changed", "reason": null, "warning": null,
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
  - `warning` is `null` or a warning object (below).
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

- **Warning codes:** `use_playbook_overridden` (TO ignores a file's `USE PLAYBOOK`) and `source_drift` (an existing playbook's recorded source differs). A warning is `{"code", "file", "line", "message"}`. `summary.warnings` counts the file warnings and the statement warnings.
- **No secret value** appears anywhere: references stay references, and a literal credential a file sets is not in the plan.

**Exit codes:**

- `0`: every statement was planned.
- `1`: the files are refused.
  - A statement the dry run refuses ends the list with `"verdict": "refused"` and its `reason`, and later statements are absent.
  - A refusal before any statement runs (a parse error, a missing reference, a refused clause on a plain directory) leaves `statements` empty, with `error: {"file", "line", "message"}`.
- `2`: a usage or internal error: `--json` without `--dry-run`, a missing file, a `TO` that cannot be resolved, an unreadable registry. `error.file` and `error.line` are `null` when no file is at fault.

`ok` is true exactly when the exit code is 0.

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
cpb ALTER PLAYBOOK kommander-idea ADD MCP SERVER sentry URL 'https://mcp.sentry.dev/mcp' HEADER 'Authorization' FROM 'keychain:pilot/sentry-auth'
cpb ALTER PLAYBOOK kommander-idea ALLOW TOOL 'Bash(kommander-helper *)' SET MODEL 'claude-opus-5-5'
cpb ALTER PLAYBOOK kommander-idea SET STATUSLINE 'bash ~/bin/statusline.sh'
cpb ALTER PLAYBOOK kommander-idea ADD SKILL release-notes FROM 'github:acme/skills' SUBDIR release-notes
cpb APPLY kommander.cpb TO kommander-lab
cpb APPLY kommander.cpb TO '~/.claude' --dry-run
cpb "SELECT name, version FROM PLAYBOOKS"
cpb "SELECT playbook, key FROM VARS WHERE effective ORDER BY playbook"
```

Every clause has a runnable example under [`examples/`](../../examples/),
applied in CI.

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

**`SHOW PLAYBOOKS`** (also a bare `SHOW`, and `SHOW --json`): human form, one header line and then one line per
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

Decided with the pilot on 2026-09-26 (a first design was withdrawn the same
day, then replaced by this one). `SELECT` queries the same state `SHOW`
prints, as tables, with two engines:

```
select := [EXPLAIN] SELECT … FROM <table> …        [--json]
cpb "SELECT name, version FROM PLAYBOOKS"                              built in
cpb "SELECT name FROM PLAYBOOKS WHERE version_tuple > [3, 10] ORDER BY name"   clickhouse-local
```

- **Built in, always:** exactly `SELECT <col>[, <col> …] FROM <table>`: plain
  column names, spelled exactly (ClickHouse identifiers are case-sensitive:
  `name`, not `NAME`), no `*`, no functions, no `WHERE`. It is a strict subset
  of ClickHouse SQL, so a query means the same on both paths. The output is a
  table as `SHOW` prints one; `--json` prints the selected fields.
- **Anything else goes to ClickHouse** when it is installed: `clickhouse` or
  `ch` on `PATH`, or the command `CPB_CLICKHOUSE` names. cpb pipes the
  table's rows to `clickhouse local --input-format JSONEachRow --structure
  '<typed columns>' -q "<query>"`, with `FROM <table>` rewritten to read
  stdin (`FROM table`; for `PLAYBOOKS`, a subquery over it that adds
  `version_tuple`). The `FROM` is found as ClickHouse reads the query, so
  `'FROM VARS'` in a string or a comment is not a table. The output is
  ClickHouse's own: a table on a terminal, TSV in a pipe, or the query's
  `FORMAT`. One statement per query: text after a `;` is refused. Without ClickHouse the query is refused in one line: "this
  query needs ClickHouse (clickhouse local); install it, or pick columns
  only". One table per query.
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
| `PLAYBOOKS` | playbook | the `SHOW PLAYBOOK` object, plus the computed `version_tuple` |
| `ENVS` | env set | `name description vars used_by default` |
| `VARS` | variable, per layer, per playbook | `playbook key value ref redacted plaintext blocked layer effective` |
| `DEFAULTS` | (one row) | `envs secret_helper` |

`version_tuple` is `Array(UInt32)`, the numbers of the version's leading
numeric part (`"v3.12.3-rc1"` → `[3, 12, 3]`; no version → `[]`): compare
and sort versions with it, because strings sort `"v3.9.0"` after
`"v3.12.3"`. It is computed, not handed over: cpb computes it for the
built-in form, the query computes it for ClickHouse. A `VARS` row is one entry of one layer
(`DEFAULTS` sets, the playbook's sets, its own block), `effective` when it is
the one a launch uses. The manual form, piping `SHOW … --json` yourself, is in
[Query with SQL](../guides/query-with-sql.md).

## INCLUDE

Decided with the pilot on 2026-09-26; ships in v3.20.0 with "Plugins and
the agent". A
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

## Plugins and the agent

Decided with the pilot on 2026-09-26; ships in v3.20.0. The goal it serves: a playbook built by stacking playbook files,
for example Kommander as a plugin and an agent on a bare playbook:

```
-- bare.cpb
CREATE PLAYBOOK IF NOT EXISTS kommander NO ALIAS;
ALTER PLAYBOOK kommander USE ENV glm-5.3;

-- kommander.cpb
INCLUDE 'bare.cpb';
ALTER PLAYBOOK kommander
  ADD MARKETPLACE kommander FROM 'github:ramazanpolat/kommander-playbook'
  ADD PLUGIN kommander@kommander
  SET AGENT 'kommander';

-- chaos.cpb
INCLUDE 'kommander.cpb';
ALTER PLAYBOOK kommander
  ADD MARKETPLACE chaos FROM 'github:santiment/chaos'
  ADD PLUGIN chaos@chaos;
```

**How the clauses act: through Claude Code's own CLI** (decided with the
pilot on 2026-09-26, replacing an earlier cut that wrote `settings.json`
itself). A playbook is a Claude Code config directory, so with
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
| `'https://…'`, `'git@…'` (a git URL) | the URL | `{"source": "git", "url": "…"}` |
| a git URL with `#<ref>` (`'https://…/repo.git#v1.2.0'`) | the URL with its ref | `{"source": "git", "url": "…", "ref": "<ref>"}`: url and ref apart |
| `'/abs/path'` or `'~/path'`, a local directory | the absolute path (`~/` expanded) | `{"source": "directory", "path": "/abs/path"}` |
| `'./path'` or `'../path'`, in a playbook file only | resolved against the file's directory | as above, absolute |

A directory source is the marketplace root, the directory that holds
`.claude-plugin/marketplace.json`. In a playbook file, a path starting with
`./` or `../` resolves against the directory of that file, exactly as
`INCLUDE` does (decided 2026-09-26), so a layer can ship its plugin beside
it; on the command line, and in a file read from a pipe, it is refused. Any
other relative path is refused, and so is a URL carrying credentials. This is how a plugin is used from a local checkout
before it is published. `--sparse` is not in the first cut. A git URL's
`#<ref>` is compared as Claude Code records it, url and ref apart (v3.21.1):
applying the same `url#ref` again changes nothing, another ref or none is
another source, and `SHOW CREATE` writes it back as `url#ref`. The
`github:` form takes no ref.
A marketplace name follows the env-set name rule; a plugin id is
`<plugin>@<marketplace>`.

**A marketplace's name is its source's.** `marketplace add` takes no name:
the source's `marketplace.json` declares it. The statement names one anyway,
so a playbook file reads the same as the state it makes, and cpb checks that
they agree: before anything runs for a directory source (it reads the file),
after the command for a git or GitHub one. A source that declares another
name is removed again, and the statement fails naming both.

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
  marketplace Claude Code knows by default (decided 2026-09-26): a plugin id
  always names its marketplace, so a playbook that uses the official one
  declares it
  (`ADD MARKETPLACE claude-plugins-official FROM 'github:anthropics/claude-plugins-official'`).
- `DROP MARKETPLACE m` is refused while installed plugins come from `m`; the
  error lists them. (`marketplace remove` would uninstall them silently.)
- `DROP PLUGIN` keeps the plugin's saved data (`--keep-data`): dropping
  detaches it, as `DROP ENV` detaches a set. Purging the data is not in the
  first cut.
- **A marketplace-declared command is never accepted for the pilot.** A
  plugin installed by running a command its marketplace declares (or whose
  archive is fetched through one) needs a confirmation. cpb never passes `-y`
  or `--accept-command`: the statement fails, shows the command and its
  `sha256`, and gives the line the pilot runs by hand after reviewing it
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
  and none is planned (decided 2026-09-26). It would end dry runs,
  validation before writing, `SHOW CREATE` and every safety rule above.

**The agent** (verified 2026-09-26, nine `claude -p` runs). A plugin can
name an agent in its own `settings.json`, and two plugins that both do are
resolved by load order, the last one winning. The `agent` of the user
scope overrides every plugin, and a playbook's `settings.json` *is* its user
scope, so `SET AGENT` is the deterministic pin. It accepts an agent's bare
name (`kommander`) or its namespaced id (`kommander:kommander`); both
resolve, and cpb stores what was typed. A layer above does not need `SET
AGENT`: its plugin's SessionStart context stacks on top of the agent's.
The agent's prompt replaces Claude Code's default system prompt; that is
the plugin's concern, not cpb's.

**Visible where state is visible.** `SHOW PLAYBOOK --json` gains
`"marketplaces": [{"name", "source"}]`, `"plugins": [{"id", "enabled"}]` and
`"agent"` (null when unset); the human form gains `Marketplaces:`, `Plugins:`
and `Agent:` lines when the playbook has any. These reads take the
playbook's `settings.json` as Claude Code wrote it and run nothing.
`EXPLAIN PLAYBOOK` names the enabled plugins a launch starts with and the
agent the playbook pins, `Agent: kommander (playbook settings)`; with no pin
and plugins enabled, it says that a plugin may name one (cpb does not read
the plugins' own files). `SHOW CREATE` writes the clauses, so a
playbook's plugins and agent travel in its playbook file. The fields are in
`SHOW PLAYBOOK --json`, so the SQL recipe sees them.

**INCLUDE** is specified in its own section and built in the same
release, so the stacked files above run with one `cpb APPLY chaos.cpb`.

These clauses exist on `ALTER PLAYBOOK` only; there is no `ALTER DEFAULTS`
form in the first cut (decided 2026-09-26).

## Targets: recipes, USE PLAYBOOK and APPLY … TO (v3.21.0)

Built (v3.21.0), `TO '<dir>'` included.

Decided with the pilot on 2026-09-26: a playbook file can be a **recipe**,
written once and applied to any playbook, or to a plain Claude Code config
directory such as `~/.claude`.

```
-- kommander.cpb, a recipe: no playbook named in its statements
INCLUDE 'bare.cpb';
ALTER PLAYBOOK
  ADD MARKETPLACE kommander FROM '~/path/to/kommander-playbook'
  ADD PLUGIN kommander@kommander
  SET AGENT 'kommander'
  ALLOW TOOL 'Bash(kommander-helper *)';
```

```
cpb APPLY kommander.cpb TO kommander-agent     # a playbook (created bare if missing)
cpb APPLY kommander.cpb TO '~/.claude'         # a plain config directory
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
(`SHOW CREATE`'s output) names every one. `SHOW CREATE` keeps writing names; a
recipe form (`SHOW CREATE … --recipe`) may come later.

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

- `SET VAR K FROM '<ref>'`, and `ENV` / `HEADER … FROM` on an MCP server: a
  reference is resolved by cpb's launcher, which never runs for that
  directory.
- `BLOCK VAR`: removing a variable at launch is the launcher's job.
- `USE / ADD / DROP ENV`, and `ALTER DEFAULTS`: env sets and `DEFAULTS` are
  layered by the launcher.
- `RENAME TO`, `ALIAS`, `NO ALIAS`, `SANDBOX`: the directory is not in the
  registry and has no launcher.

**Safety.** Before its first write to a directory in a run, cpb copies that
directory's `settings.json` to `settings.json.cpb-backup-<YYYY-MM-DD-HH_MM_SS>`
beside it, and `.claude.json` the same way before an MCP change; a skill
change is a write too. Each file is backed up once per run, and a dry run
plans the same single backup. A file that
does not exist yet has nothing to back up: the clause creates it, and the dry
run says which files would be created rather than backed up. Applying to a
directory that is not a playbook asks for confirmation on a terminal, and
`--yes` gives it (as for `DROP PLAYBOOK`); without a terminal and without
`--yes` it is refused before anything is written. `--dry-run` works as
always and names the backups it would make.

## An agent's configuration (v3.21.0)

Decided with the pilot on 2026-09-26: a playbook file describes a Claude Code
agent completely, from its route to its tools. Four clause groups, on
`ALTER PLAYBOOK` only, in the order they are built:

```
ALTER PLAYBOOK kommander-agent
  ADD MCP SERVER sentry URL 'https://mcp.sentry.dev/mcp' HEADER 'Authorization' FROM 'keychain:pilot/sentry-auth'
  ADD MCP SERVER files COMMAND 'npx' ARGS '-y' '@modelcontextprotocol/server-filesystem' '/srv/data'
  ALLOW TOOL 'Bash(kommander-helper *)'
  DENY TOOL 'Bash(rm -rf *)'
  SET STATUSLINE '~/.claude-playbooks/kommander-agent/bin/statusline.sh'
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

Built (v3.21.0).

`ADD MCP SERVER <name>` declares one server, stdio (`COMMAND … [ARGS …]`) or
remote (`URL …`, HTTP unless `TRANSPORT SSE`), with its environment (`ENV`)
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
- **Secrets never enter Claude's config.** `ENV K FROM '<ref>'` and
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
- **Credentials take a reference, always.** A credential-looking `ENV` key or
  `HEADER` name (the variable rule, plus `Authorization`, `Proxy-Authorization`
  and `Cookie`) must use `FROM '<ref>'`; a literal is refused, and `AS
  PLAINTEXT` is not accepted on an MCP server, because the literal would be
  written into Claude's config. Other literals are written as given.
- `DROP MCP SERVER` also forgets the references it derived.
- **Visible.** `SHOW PLAYBOOK --json` gains `"mcp_servers"`: name, transport,
  command and args or URL, and env and headers as variable objects (a
  reference shown as the reference, a credential-looking literal redacted).
  `SHOW CREATE` writes the clauses, turning a derived placeholder back into
  its `FROM '<ref>'`. `EXPLAIN PLAYBOOK` lists the servers and the derived
  variables the launch supplies.
- Not in the first cut: OAuth (`--client-id`, `--client-secret`, `claude mcp
  login`: interactive, the pilot's to run), WebSocket servers, and the
  `local` and `project` scopes.

### Tool permissions

Built (v3.21.0).

`ALLOW TOOL '<rule>'` and `DENY TOOL '<rule>'` add rules to the playbook's
`settings.json` `permissions.allow` / `permissions.deny`; `UNSET TOOL
'<rule>'` removes a rule from either. A rule is Claude Code's own permission
syntax, stored as typed (`'Bash(kommander-helper *)'`, `'Read(~/secrets/**)'`,
`'mcp__sentry'`). Adding a rule to one list removes it from the other, so a
rule is in at most one. Order and every rule cpb did not write are kept.
Claude Code has no CLI for permissions, so cpb writes the key.
`permissions.ask`, `defaultMode` and `additionalDirectories` are not in the
first cut.

`SHOW CREATE` writes every rule; `EXPLAIN PLAYBOOK` shows
`Tools: allow …; deny …`. Kommander as an agent needs one today:
`ALLOW TOOL 'Bash(kommander-helper *)'`, which example 08 sets.

### Status line and model

Built (v3.21.0).

- `SET STATUSLINE '<command>'` writes `statusLine = {"type": "command",
  "command": "<command>"}`, keeping any other field of an existing
  `statusLine` (such as `padding`); `UNSET STATUSLINE` removes it.
- `SET MODEL '<model>'` writes `model`; `UNSET MODEL` removes it. It is the
  playbook's default model and the lowest-priority choice: `ANTHROPIC_MODEL`
  from an env set or `SET VAR`, a launch's `--model`, and `/model` in a
  session all win over it. `EXPLAIN PLAYBOOK` says which one decides.

Both are settings keys with no CLI.

### Model picker

Built for v3.22.0. The pilot said "do it" on 2026-09-27; root confirmed
the shape. The `/model` picker of a playbook, or of a plain config
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
  - `SHOW PLAYBOOK --json` gains `"model_picker": null | {"mode": "only" | "append", "options": [{"model", "label", "description", "behaves_as"}]}`, with `mode` "append" unless `replaceBuiltInOptions` is true.
  - `EXPLAIN PLAYBOOK` and the human `SHOW` print a `Model picker:` line.
  - `SHOW CREATE` writes the clauses back.
  - `SELECT`'s `PLAYBOOKS` has a `model_picker` column.
- **Claude Code versions:** `modelPicker` is read from Claude Code 2.1.242, and `behavesAs` from 2.1.257. cpb writes the key either way and does not check the version.
- `SET MODEL '<model>'` (the `model` key, above) is a separate clause. After `SET MODEL`, `PICKER` begins this one; a model id is quoted.

### Skills

Built (v3.21.0). A git source may also be `file://…` (a local repository).

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
  it from the recorded source (`cpb update <playbook>`; a bare `cpb update`
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
- `SHOW CREATE` writes the recorded skills, and `SHOW PLAYBOOK --json` gains
  `"skills"`.

Claude Code has no CLI that installs an existing skill (`claude plugin init`
scaffolds a new, empty skills-dir plugin under `skills/`), so cpb manages the
directory. A skill that ships inside a
plugin stays the plugin's: `ADD PLUGIN` brings it.

## Completion

Every slot has a closed set, so TAB completes verbs, then objects, then
existing names of that object, then the clause keywords valid for it, then
keys (from the object's current entries).
