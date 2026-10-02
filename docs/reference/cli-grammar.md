# CLI grammar

Status: **implemented, v3.27.0.**
Everything on this
page is built; a section specified before it is built is marked
**planned**.

## Shape

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
  (`glm-5.3`). A **keyword is not a valid new name**
  (refused with an error naming the keyword); an existing object whose name
  is a keyword can still be addressed in the name slot.
- **Global flags go before the verb** (`cpb --playbooks-dir X ALTER …`), and
  `CPB_PLAYBOOKS_DIR` works as today. cpb recognises a statement before
  its flag parser runs, so every word after the verb belongs to the
  statement: `SET VAR OPTS=-v` is a value, and `--dry-run` /
  `--skip-secrets` are the statement's own.

## Objects

| Object | What it is | Lives at |
|---|---|---|
| `PLAYBOOK` | an installed playbook: dir, launcher, source, attached ENVs, own variables | `<root>/<name>/` |
| `ENV` | an **env set**: a named, reusable set of variables (formerly "env profile") | `<root>/.env-sets/<name>.toml` |
| `DEFAULTS` | the machine-wide layer under every playbook: an ordered list of env sets; a singleton, no name | `<root>/.env-sets/.defaults` |

Two words keep the variables apart: **`ENV` is a named set**, **`VAR` is one
variable**. "Profile" is deliberately not a keyword: it would be ambiguous with other
tools' profiles.

**A playbook routed away from Anthropic** sends every request to its
`ANTHROPIC_BASE_URL`, with what its `CLAUDE.md` holds. The `CLAUDE.md` that
`CREATE PLAYBOOK` writes imports nothing; whatever you add to it, `@` imports
included, goes along. `ISOLATED LOGIN` keeps such a playbook's login apart,
and `BLOCK VAR` keeps your Anthropic credentials out of its launches
([example 15](../../examples/15-third-party-route/)).

## Grammar

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
            | SET STATUSLINE '<command>' [REFRESH <n>] [IF UNSET] | UNSET STATUSLINE
            | SET STATUSLINE REFRESH <n> | UNSET STATUSLINE REFRESH   v3.23.0
            | SET STATUSLINE PREVIOUS      v3.25.0, the status line cpb replaced last
            | SET ISOLATED LOGIN | UNSET ISOLATED LOGIN   v3.23.0, see "Isolated login"
            | SET SANDBOX | UNSET SANDBOX  every launch sandboxed (the login isolated too) | not; see "Sandbox"
            | SET SANDBOX <key>=<value> ...   the [sandbox] table's own keys: SET SANDBOX backend=openshell
            | UNSET SANDBOX <key> ...      forget a setting: UNSET SANDBOX host
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
            | SHOW SESSIONS [FOR PLAYBOOK <name>] [--json]
            | SHOW CREATE { PLAYBOOK <name> | ENV <name> | ALL } [--skip-secrets]
            | EXPLAIN PLAYBOOK <name> [--json]
```

The alternatives are exclusive, and the parser enforces them: `OR REPLACE`
and `IF NOT EXISTS` cannot be combined; a playbook has one origin, `FROM` or `LINK`,
and `BRANCH` / `SUBDIR` only with `FROM`; `ALIAS` and `NO ALIAS` exclude each
other; `SANDBOX` and `ISOLATED LOGIN` do not take `LINK`. The clauses of
`origin`, `launcher`, `SANDBOX` and `ISOLATED LOGIN` may come in any order.
`DROP PLAYBOOK` asks for confirmation on a terminal; `--yes` skips it.

Two limits keep every statement whole-or-nothing:

- `RENAME TO`, `ALIAS` and `NO ALIAS` are not combined with environment or
  variable clauses in one statement: a rename after an environment write
  could not be undone as one step. `RENAME TO <name> ALIAS <launcher>` is
  one statement; the environment change is a second.
- `CREATE PLAYBOOK … LINK <dir>` needs the target to have a `.playbook`: a
  statement never prompts for one. `SANDBOX` does not apply to `LINK`,
  whose manifest belongs to the target.

**A source never carries a login** (v3.22.1, a security fix).
- `CREATE PLAYBOOK … FROM` and `install` leave a source's
  `.credentials.json` out of the install, whether it is a file or a link.
  They also leave out the account state in its `.claude.json`: `oauthAccount`,
  `userID`, the onboarding and install markers, and the cached feature flags.
  Each prints one stderr line naming the source and the keys, never a value.
  Before this, the first sync copied a shipped login over the machine's
  `~/.claude/.credentials.json`, so installing a source could switch the
  account to the source's.
- `LINK` deletes nothing in your directory. It renames a
  `.credentials.json` there to `.credentials.json.cpb-ignored-<stamp>`, and
  backs up `.claude.json` to `.claude.json.cpb-backup-<stamp>` before
  removing the same keys. A directory with `isolated_login = true` keeps both.
- `update` already kept the install's own files.
- See `docs/known-issues/shared-launch-copies-own-login-over-machine-login.md`.

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

## Where each clause writes

The grammar is a new front end over the files cpb already keeps. Nothing
stores commands; files store the result.

| Clause | Writes |
|---|---|
| `CREATE / ALTER / DROP ENV` | `<root>/.env-sets/<name>.toml`: `description`, `[set]`, `[refs]`, `block` |
| `ALTER PLAYBOOK … USE / ADD / DROP ENV` | the playbook's `.playbook`, `[env] sets = [...]` |
| `ALTER PLAYBOOK … SET VAR K=V` | `.playbook` `[env.set]` |
| `ALTER PLAYBOOK … SET VAR K FROM '<ref>'` | `.playbook` `[env.refs]` (new) |
| `ALTER PLAYBOOK … BLOCK VAR K` | `.playbook` `[env] block = [...]` |
| `ALTER PLAYBOOK … UNSET VAR K` | removes K from whichever of the three holds it |
| `ALTER DEFAULTS … USE / ADD / DROP ENV` | `<root>/.env-sets/.defaults`, one set name per line, in order |
| `ALTER DEFAULTS SET / UNSET SECRET HELPER` | `<root>/.env-sets/.secret-helper`, one line: the command |
| `CREATE / DROP PLAYBOOK`, `RENAME TO`, `ALIAS`, `NO ALIAS` | the playbook dir, the registry and the launcher |
| `ALTER PLAYBOOK … ADD / DROP MARKETPLACE`, `ADD / DROP PLUGIN` | nothing directly: runs `claude plugin …` with the playbook as `CLAUDE_CONFIG_DIR` (see "Plugins and the agent") |
| `ALTER PLAYBOOK … SET / UNSET AGENT` | the playbook's `settings.json`, `agent` |
| `ALTER PLAYBOOK … ADD / DROP MCP SERVER` | nothing directly: runs `claude mcp add-json / remove --scope user` for the playbook; a reference also writes the playbook's `[env.refs]` (see "An agent's configuration") |
| `ALTER PLAYBOOK … ALLOW / DENY / UNSET TOOL` | the playbook's `settings.json`, `permissions.allow` / `permissions.deny` |
| `ALTER PLAYBOOK … SET / UNSET STATUSLINE`, `SET / UNSET MODEL` | the playbook's `settings.json`, `statusLine` / `model` |
| `ALTER PLAYBOOK … ADD / DROP SKILL` | `<playbook>/skills/<name>` (a link or a copy) and the manifest's `[skills.<name>]` record |
| `ALTER PLAYBOOK … SET / UNSET SANDBOX` | the playbook's `.playbook`, `[sandbox]` (bare `SET SANDBOX` also `isolated_login = true`) |

A key lives in exactly one of `set`, `refs`, `block` within a layer; writing
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

## Isolated login (v3.23.0)

A playbook normally shares the machine's login: its `.credentials.json` is a
link to `~/.claude/.credentials.json`, and `/login` in any playbook logs in
all of them. An **isolated login** shares nothing. There is no link, no
machine-wide token, and no account record carried over from a shared past,
so the playbook is logged in only if it runs `/login` itself. It is the
manifest's `isolated_login = true` (see the authentication guide). Before
v3.23.0 only `SANDBOX` or a hand edit set it.

```
CREATE PLAYBOOK <name> … ISOLATED LOGIN      create --isolated-login
ALTER PLAYBOOK <name> SET ISOLATED LOGIN
ALTER PLAYBOOK <name> UNSET ISOLATED LOGIN
```

- **Use it** for a second account, or for a throwaway or a third-party route
  where a `/login` must not land in the machine's shared store. In a shared
  playbook, `/login` writes through the link.
- **`SET ISOLATED LOGIN`** records `isolated_login = true` and removes the link
  to the shared store at once, so `cpb auth status` reports `isolated`
  straight away. A `.credentials.json` that is a file, the playbook's own
  login, is kept.
- **`UNSET ISOLATED LOGIN`** removes it. The next launch links the shared
  store again. It is refused in two cases, each with its reason:
  - The playbook always runs in a sandbox. `SANDBOX` implies an isolated
    login.
  - The playbook holds a login of its own: a `.credentials.json` file
    carrying an account grant. A shared launch would take it out of use.
    Since v3.23.1 another account's login is set aside, and before that it
    was copied over the machine's login in `~/.claude`. The same account's
    login is copied over the machine's, since it is newer. Run `/logout` in
    it first.
- **Not "own login".** `cpb auth status` already reports `own-login` for
  something else: a playbook that blocks the machine token and uses the
  shared stored login. An isolated playbook is reported as `isolated`.
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

## Sandbox (v4.0.0)

A playbook's `[sandbox]` table says how its launches are sandboxed.
`ALTER PLAYBOOK` writes it:

```
ALTER PLAYBOOK sre SET SANDBOX               -- every launch sandboxed (always = true); the login isolated too
ALTER PLAYBOOK sre UNSET SANDBOX             -- always = false; the login stays isolated
ALTER PLAYBOOK sre SET SANDBOX backend=openshell host=me@buildbox mounts=~/libs:ro,~/data
ALTER PLAYBOOK sre UNSET SANDBOX host mounts
```

- **One meaning per form.** Bare `SET SANDBOX` is `always = true`, and it
  isolates the login, as `CREATE PLAYBOOK … SANDBOX` does: a sandbox shares
  nothing with `~/.claude`. `SET SANDBOX <key>=<value> …` sets only the keys
  it names and never changes `always`; `SET SANDBOX always=true` is the bare
  form spelled out. Bare `UNSET SANDBOX` is `always = false` and leaves the
  login isolated (`UNSET ISOLATED LOGIN` shares it again). `UNSET SANDBOX
  <key> …` forgets settings.
- **The keys** are the table's own: `always`; `backend` (`sbx`, `openshell`);
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

## Secrets (optional)

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
…)". Literal values work as today.

`FROM` also names a playbook's source in `CREATE PLAYBOOK`; the position
disambiguates.

- The value never appears in a file, in argv, or in any `SHOW`/`EXPLAIN`
  output.
- A sandboxed launch of a playbook with references is refused in the first
  release, with a message saying so.
- **Keys cpb reads itself never take a reference**:
  today `CLAUDE_CODE_OAUTH_TOKEN`, whose value cpb's authentication handling
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
**`AS PLAINTEXT` stores one knowingly**, for a user
without a secret helper: `SET VAR ANTHROPIC_AUTH_TOKEN=… AS PLAINTEXT`. It
applies to every literal in its `SET` clause and never to a reference. It
keeps cpb usable standalone, and it is loud where it matters: `EXPLAIN` marks
such an entry `(plaintext)`, and `SHOW CREATE` never carries the value (see
playbook files). Files that already hold such literals keep working unchanged.

All output redacts credential-looking literals; no form prints the value.

## playbook.cpb: SHOW CREATE and APPLY

Two senses of "playbook", kept apart:

- **`PLAYBOOK`**, an *installed playbook*: a Claude Code config directory cpb manages.
- **a playbook file**, also *a playbook script* or *the agent's playbook*: a `.cpb` file of statements, conventionally `playbook.cpb`, that `APPLY` runs. `APPLY` accepts any file name.

A **playbook file** (conventionally `playbook.cpb`) is plain text holding cpb
statements: the same grammar as the command line, without the `cpb` prefix.
It describes a machine's setup: env sets, DEFAULTS, playbooks and their
wiring. It is the script beside the database, never inside it: the manifest
keeps holding state, the file holds the recipe. It belongs in dotfiles or a
devbox project, and moving a setup to another machine is one command.

```
-- playbook.cpb, from: cpb SHOW CREATE ALL > playbook.cpb

CREATE OR REPLACE ENV router
  DESCRIBE 'GLM via a local router'
  SET ANTHROPIC_BASE_URL=http://localhost:8080/v1 ANTHROPIC_MODEL=glm-5.3
  SET ANTHROPIC_AUTH_TOKEN FROM 'keychain:router-token';

CREATE OR REPLACE ENV claude-default
  BLOCK HTTP_PROXY;

ALTER DEFAULTS USE ENV claude-default;

CREATE PLAYBOOK IF NOT EXISTS work
  FROM https://github.com/example/work-playbook BRANCH v1.2.0 ALIAS w;

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

Built for v3.22.0. `cpb APPLY <file> … [TO <target>] --dry-run
--json` prints the plan as **one JSON object on stdout**, in every case,
refusals included. It follows the `--json` rule: fields may be added, and
none changes meaning within a major version. `schema` is bumped only on a
meaning change, and so only with a major version; a consumer refuses a
schema it does not know. **Verdicts, action types and warning codes are
closed sets** within a major version.

`--json` needs `--dry-run`: the JSON form is a plan, and a real `APPLY
--json` is not in this release. The human lines of the dry run go to
stderr.

A dry run **creates nothing**: not the store (`CPB_PLAYBOOKS_DIR`),
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

- **Warning codes:** `use_playbook_overridden` (TO ignores a file's `USE PLAYBOOK`), `source_drift` (an existing playbook's recorded source differs), and from v3.27.0 `marketplace_ref_not_cloneable` (an `ADD MARKETPLACE` git source whose `#<ref>` looks like a commit, 7 to 40 hex characters, which Claude Code will not clone; see "Plugins and the agent"). A warning is `{"code", "file", "line", "message"}`. `summary.warnings` counts the file warnings and the statement warnings.
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
cpb CREATE PLAYBOOK scratch FROM https://github.com/example/work-playbook ALIAS sc
cpb ALTER PLAYBOOK scratch ALIAS scr
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

Every clause has a runnable example under [`examples/`](../../examples/),
applied in CI.

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

**`play`** (v4.0.0) is the object's last field. It is the `[play]` record of a
playbook `cpb play --keep` built, and `null` for every other:
`{"ref", "url", "sha256", "played_at"}`. `ref` is what `cpb update <name>`
fetches again (a template name, a URL, a `github:` ref, or a local file's
absolute path); `url` is empty for a local file; `played_at` is RFC 3339,
UTC. The human form has a `Played from:` line.

**`SHOW PLAYBOOKS`** (also a bare `SHOW`, and `SHOW --json`): human form, one header line and then one line per
playbook sorted by name, columns `NAME VERSION LAUNCHER ENV SETS SOURCE`
(`-` for none). `--json`: an array of the `SHOW PLAYBOOK` objects.

**`SHOW ENV <name>`**: human form, `Name:`, `Description:`, `Used by:`,
`Default:` (yes/no), then `Variables:` as above. `--json`:

```
{"name": "router", "description": "GLM via a local router",
 "vars": [<variable>, ...], "used_by": ["work"], "default": false}
```

**`SHOW ENVS`**: human form, one line per set, columns
`NAME SET BLOCKED USED BY DESCRIPTION`, a `*` after the name of each set in
`DEFAULTS`. `--json`: an array of the `SHOW ENV` objects.

**`SHOW DEFAULTS`**: human form, `Env sets:` in order, and
`Secret helper:` with the command and where it came from (`setting` or
`CPB_SECRET_HELPER`), or `(none)`. `--json`:

```
{"envs": ["claude-default", "corp-proxy"],
 "secret_helper": {"command": "my-keychain-helper", "from": "setting"}}
```

(`secret_helper` is null when none is configured.)

**`EXPLAIN PLAYBOOK <name>`**: human form as in *Layers at launch*.
`--json`:

```
{"playbook": "work",
 "vars": [{<variable>, "layer": {"kind": "ENV", "name": "router"}}, ...],
 "secret_helper": {"command": "...", "from": "setting"} | null}
```

`layer.kind` is `DEFAULTS` (with `name` the env set), `ENV` or `PLAYBOOK`.

## SELECT (v3.21.0)

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
  the order the query names the columns, as `clickhouse local` writes them
  (v3.26.0; before, they were sorted). A column named twice is one key, at
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
- **What a terminal and a pipe get** (v3.22.0):
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
    prints `clickhouse local`'s own default, TSV, exactly as before, so
    scripts keep working.
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
| `PLAYBOOKS` | playbook | the `SHOW PLAYBOOK` object, plus the computed `version_tuple` (`play`, v4.0.0, is the last column) |
| `ENVS` | env set | `name description vars used_by default` |
| `VARS` | variable, per layer, per playbook | `playbook key value ref redacted plaintext blocked layer effective` |
| `SESSIONS` | live Claude Code session (v3.25.0) | the `SHOW SESSIONS --json` object: `playbook pid session_id cwd kind status name claude_version started_at last_active model launcher config_dir resume tty` (`tty` v3.25.0) |
| `DEFAULTS` | (one row) | `envs secret_helper` |

`version_tuple` is `Array(UInt32)`, the numbers of the version's leading
numeric part (`"v3.12.3-rc1"` → `[3, 12, 3]`; no version → `[]`): compare
and sort versions with it, because strings sort `"v3.9.0"` after
`"v3.12.3"`. It is computed, not handed over: cpb computes it for the
built-in form, the query computes it for ClickHouse. A `VARS` row is one entry of one layer
(`DEFAULTS` sets, the playbook's sets, its own block), `effective` when it is
the one a launch uses. The manual form, piping `SHOW … --json` yourself, is in
[Query with SQL](../guides/query-with-sql.md).

### DESCRIBE

v3.22.0.
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

## INCLUDE

Built in v3.20.0, with "Plugins and the agent". A
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
time; anything more needs your explicit approval first.

## Plugins and the agent

Built in v3.20.0. The goal it serves: a playbook built by stacking playbook files,
for example a reviewer agent from a plugin, on a bare playbook:

```
-- base.cpb
CREATE PLAYBOOK IF NOT EXISTS reviewer NO ALIAS;
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
| `'github:<owner>/<repo>#<ref>'` or `@<ref>`, a branch or tag (v3.27.0) | `<owner>/<repo>#<ref>` | `{"source": "github", "repo": "<owner>/<repo>", "ref": "<ref>"}`: repo and ref apart |
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
before it is published. `--sparse` is not in the first cut. A git URL's
`#<ref>` is compared as Claude Code records it, url and ref apart (v3.21.1):
applying the same `url#ref` again changes nothing, another ref or none is
another source, and `SHOW CREATE` writes it back as `url#ref`. A `github:`
source's ref works the same way (v3.27.0): `#` and `@` spell one source,
`SHOW CREATE` writes it back with `#`, and `github:<owner>/<repo>` without a
ref is unchanged. **A commit cannot be pinned:** Claude Code clones a
marketplace by branch or tag only, so a `github:` ref of 7 to 40 hex
characters is refused ("Claude Code clones marketplaces by branch or tag; a
commit cannot be pinned"), rather than written and broken at session start.
Tag the commit instead. A git URL's `#<ref>` that looks like a commit is
still accepted, as before v3.27.0, and warned about
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
`claude plugin install --json` and `uninstall --json`, which arrived then
(the changelog's 2.1.268; nixpkgs carried 2.1.245 at the time). A
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
  detaches it, as `DROP ENV` detaches a set. Purging the data is not in the
  first cut.
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

**Visible where state is visible.** `SHOW PLAYBOOK --json` gains
`"marketplaces": [{"name", "source"}]`, `"plugins": [{"id", "enabled"}]` and
`"agent"` (null when unset); the human form gains `Marketplaces:`, `Plugins:`
and `Agent:` lines when the playbook has any. These reads take the
playbook's `settings.json` as Claude Code wrote it and run nothing.
`EXPLAIN PLAYBOOK` names the enabled plugins a launch starts with and the
agent the playbook pins, `Agent: reviewer (playbook settings)`; with no pin
and plugins enabled, it says that a plugin may name one (cpb does not read
the plugins' own files). `SHOW CREATE` writes the clauses, so a
playbook's plugins and agent travel in its playbook file. The fields are in
`SHOW PLAYBOOK --json`, so the SQL recipe sees them.

**INCLUDE** is specified in its own section and built in the same
release, so the stacked files above run with one `cpb APPLY team.cpb`.

These clauses exist on `ALTER PLAYBOOK` only; there is no `ALTER DEFAULTS`
form in the first cut.

## Targets: recipes, USE PLAYBOOK and APPLY … TO (v3.21.0)

Built (v3.21.0), `TO '<dir>'` included.

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
`--yes` it is refused before anything is written. `--dry-run` works as
always and names the backups it would make.

## An agent's configuration (v3.21.0)

A playbook file describes a Claude Code
agent completely, from its route to its tools. Four clause groups, on
`ALTER PLAYBOOK` only, in the order they are built:

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
  login`: interactive, yours to run), WebSocket servers, and the
  `local` and `project` scopes.

### Tool permissions

Built (v3.21.0).

`ALLOW TOOL '<rule>'` and `DENY TOOL '<rule>'` add rules to the playbook's
`settings.json` `permissions.allow` / `permissions.deny`; `UNSET TOOL
'<rule>'` removes a rule from either. A rule is Claude Code's own permission
syntax, stored as typed (`'Bash(git diff *)'`, `'Read(~/secrets/**)'`,
`'mcp__sentry'`). Adding a rule to one list removes it from the other, so a
rule is in at most one. Order and every rule cpb did not write are kept.
Claude Code has no CLI for permissions, so cpb writes the key.
`permissions.ask`, `defaultMode` and `additionalDirectories` are not in the
first cut.

`SHOW CREATE` writes every rule; `EXPLAIN PLAYBOOK` shows
`Tools: allow …; deny …`. An agent that runs a tool without asking needs
one, as example 08's `ALLOW TOOL 'Bash(git diff *)'`.

### Status line and model

Built (v3.21.0).

- `SET STATUSLINE '<command>'` writes `statusLine = {"type": "command",
  "command": "<command>"}`, keeping any other field of an existing
  `statusLine` (such as `padding`); `UNSET STATUSLINE` removes it. It always
  applies, whatever command the slot holds.
- **`IF UNSET`** (v4.0.0): `SET STATUSLINE '<command>' [REFRESH <n>] IF UNSET`
  applies only where no `statusLine` is set yet, and otherwise reports
  unchanged. A recipe that offers a status line uses it, so applying the
  recipe leaves a status line you chose in place. `SHOW CREATE` writes the
  status line as it is, never the condition
  ([example 17](../../examples/17-statusline-if-unset/)).
- **REFRESH** (v3.23.0) sets `statusLine.refreshInterval`, in whole seconds:
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
  - `SHOW PLAYBOOK --json` gains `"statusline_refresh": <n> | null`, and
    `statusline` keeps its meaning. `SELECT`'s `PLAYBOOKS` has a
    `statusline_refresh` column. `SHOW` and `EXPLAIN` print `Status line:
    <command> (refreshes every <n> s)`. It is valid on a plain config
    directory too.
- **History and `SET STATUSLINE PREVIOUS`** (v3.25.0). Every time a
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

Built for v3.22.0. The `/model` picker of a playbook, or of a plain config
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
- `SHOW CREATE` writes the recorded skills, and `SHOW PLAYBOOK --json` gains
  `"skills"`.

Claude Code has no CLI that installs an existing skill (`claude plugin init`
scaffolds a new, empty skills-dir plugin under `skills/`), so cpb manages the
directory. A skill that ships inside a
plugin stays the plugin's: `ADD PLUGIN` brings it.

## Sessions (v3.25.0)

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
| `tty` | string or null | v3.25.0: the process's controlling terminal (`pts/3`, `ttys012`), null for none (a `bg` session). It is read in the same pass as the start time: `/proc/<pid>/stat`'s tty_nr on Linux, `ps`'s tty elsewhere |

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

## cpb tui (v3.25.0)

`cpb tui` is a terminal UI over this grammar. v1 only reads: it browses,
shows `SHOW CREATE`, copies statements and commands, and exports a `.cpb`.

**A front-end, not a second engine.**
- **Reads.** Everything on screen is a statement's `--json` output. cpb
  runs its own binary for each read, and the TUI parses that output and
  nothing else.
- **The statement behind each screen** is named on its last line, for
  example `reads: cpb SHOW SESSIONS --json`, so anything seen can be
  scripted.
- **What it can do.** Only what the grammar can. v1 changes no state. Its
  only write is the `.cpb` file `e` exports.

**Views** (`1`–`5`):

| View | Reads | Shows |
|---|---|---|
| Playbooks | `SHOW PLAYBOOKS --json`, `SHOW SESSIONS --json` | name, launcher, version, env sets, login kind (`shared`, `isolated`, `sandbox`), live-session count, model |
| a playbook (`enter`) | `SHOW PLAYBOOK`, `EXPLAIN PLAYBOOK --json` | tabs: Overview, Env, Vars (effective, with the layer each comes from), Plugins, MCP, Skills, Status line (history), Model (the picker), Sessions |
| Sessions | `SHOW SESSIONS --json` | pid, tty, kind, status, age, last active, model, folder; the footer says where past sessions are, `past sessions: <launcher> --resume` |
| Env sets | `SHOW ENVS --json` | the env sets (env profiles): variables, `used_by`, default |
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
    stall. bubbletea v1 failed this: its package `init` queried the
    terminal's background colour in every command and waited up to 5 s
    for a reply.
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

**Bare `cpb`** on a terminal ends with one more line, `Browse and manage
them: cpb tui`. Off a terminal, its output is exactly what it was.

**v2** (not yet): the actions, each built as a statement, shown, run
through `APPLY --dry-run --json` for its plan, then applied on
confirmation.

## cpb play (v4.0.0)

`cpb play <ref>` tries someone else's playbook: it fetches a recipe once,
checks it, shows exactly what it would do, asks, and runs it as a throwaway
playbook that is removed when the session ends; sandboxed where a sandbox is
available. `--keep` keeps it as a playbook of your own instead. The guide is
[Try someone else's playbook](../guides/play.md).

```
cpb play <ref> [--yes] [--trust-endpoint <host>|TLS]... [--trust-secret <ref>]... [--env <set>]...
               [--sandbox[=sbx|openshell] | --no-sandbox] [--sha256 <hex>] [-- <claude arguments>]
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
- `SET ISOLATED LOGIN`, `NO ALIAS`.

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
- **`UNSET ISOLATED LOGIN`, `RENAME TO`, `ALIAS`, `NO ALIAS`:** play decides
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
  ALIAS`, with `ISOLATED LOGIN` and the `BLOCK VAR` above when
  the endpoint moves;
- the recipe.

**`--json`** prints the `APPLY --dry-run --json` object with a `"play"`
block added. It is a new top-level field, absent from `APPLY`'s own report:

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
- **the backend:** `sbx` where it is installed, otherwise OpenShell where its
  preflight passes (Linux). `--sandbox=<backend>` picks one, and refuses if
  it is not available;
- **`--no-sandbox`** runs it on this machine. The preview says "Sandbox off
  (--no-sandbox): this agent runs on your machine, as you.", and the plan
  carries the `no_sandbox` risk;
- **no backend here:** the preview says "No sandbox available here (sbx, or
  OpenShell on Linux): this agent will run on your machine, as you.", with
  the same risk;
- **`create-with: SANDBOX`** is refused where no backend is available,
  unless `--no-sandbox` (and then the preview names the override);
- **a secret reference** cannot be resolved by a sandboxed launch yet, so a
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

**`--env <set>`** copies one of your env sets into the throwaway store and
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
  NOT follow it to <host>"), except those of an `--env` set (attached with
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

**Exit codes:** 0 when checked, planned, kept, updated or unchanged; 1 when
refused (a check, the header, `--sha256`, or a confirmation); 2 on a usage
error. A session's own exit code passes through.

## Completion

Every slot has a closed set, so TAB completes verbs, then objects, then
existing names of that object, then the clause keywords valid for it, then
keys (from the object's current entries).
