# Environment overrides

Variables set or blocked for every launch of one playbook and no other, so one
playbook gets its own tool settings or keeps its own login while the rest of
your shell does not. The statements below are the [CLI grammar](../../SPEC.md).

Where requests go, how they authenticate and which models they name are not
variables you set here: they come from the playbook's **account**
([Accounts](accounts.md)). A layer that sets one of those (`ANTHROPIC_BASE_URL`,
a key, `CLAUDE_CODE_OAUTH_TOKEN`, the model variables) refuses the launch.

A fresh playbook has no overrides. Launching it runs `claude` with your shell's
environment plus `CLAUDE_CONFIG_DIR`, exactly as before.

## What happens at launch

Every launch of a playbook (its launcher command, `run`, or `start` at its
directory) builds the child `claude` process's environment in layers, later
layers winning:

```text
your shell's environment
  + DEFAULTS: each env set in the list, in order        (ALTER DEFAULTS USE ENV …)
  + each env set the playbook uses, in order             (ALTER PLAYBOOK p USE ENV …)
  + the playbook's own SET VAR, minus its BLOCK VAR      (in its .playbook)
  + one-off launch flags (--env-set, --env, --block, --env-file)
  + the account's variables, last: the address, the key, the models
    (--account, the playbook's USE ACCOUNT list, DEFAULTS, else machine);
    every other routing and credential variable is removed
  + CLAUDE_CONFIG_DIR, bound by the tool, cannot be overridden
  = what claude sees
```

`EXPLAIN PLAYBOOK <name>` prints that result: every variable a launch would
change, its value (masked when it looks like a credential), and the layer that
decided it. `--json` gives the same as an object for scripts.

A `SET` overrides whatever the shell exported; a `BLOCK` removes a variable
even when the shell exports it. Raw `claude` launches bypass all of this.
Claude Code's own `env` block in `settings.json` is applied later, inside the
`claude` process, and wins over these layers; it can set variables but cannot
remove one the shell exported, which is what `BLOCK` is for.

## One playbook, its own variables

```bash
cpb ALTER PLAYBOOK work SET VAR MAX_THINKING_TOKENS=16000
cpb ALTER PLAYBOOK work BLOCK VAR CLAUDE_CODE_OAUTH_TOKEN
```

The playbook's `.playbook` now ends with:

```toml
[env]
block = ["CLAUDE_CODE_OAUTH_TOKEN"]

[env.set]
MAX_THINKING_TOKENS = "16000"
```

Inspect and undo:

```bash
cpb SHOW PLAYBOOK work                      # its own layer, under "Variables:"
cpb EXPLAIN PLAYBOOK work                   # what a launch would apply, and from where
cpb ALTER PLAYBOOK work UNSET VAR MAX_THINKING_TOKENS   # forget the entry; the layer below applies again
```

`UNSET VAR` forgets the playbook's own entry, whether it was a `SET`, a
reference or a `BLOCK`. Inside `ALTER PLAYBOOK` the word `VAR` is required.

## Env sets: define once, attach to many

When several playbooks want the same variables, put them in an **env set**:
a named file under
`~/.claude-playbooks/.env-sets/`, attached to playbooks by name.

```bash
cpb CREATE ENV tools DESCRIPTION 'Shared tool settings' \
    SET BASH_DEFAULT_TIMEOUT_MS=300000 DISABLE_TELEMETRY=1 \
    BLOCK HTTP_PROXY
```

That wrote `~/.claude-playbooks/.env-sets/tools.toml` (mode `0600`):

```toml
description = "Shared tool settings"
block = ["HTTP_PROXY"]

[set]
BASH_DEFAULT_TIMEOUT_MS = "300000"
DISABLE_TELEMETRY = "1"
```

Inside `CREATE ENV` / `ALTER ENV` the word `VAR` is optional. Attach the set;
the playbook's manifest records only its name.

```bash
cpb CREATE PLAYBOOK work
cpb ALTER PLAYBOOK work USE ENV tools                 # the whole list, in order
cpb ALTER PLAYBOOK work ADD ENV team FIRST            # insert one: FIRST, LAST (default), BEFORE x, AFTER x
cpb ALTER PLAYBOOK work DROP ENV team                 # detach
cpb ALTER PLAYBOOK work REPLACE ENV tools WITH tools-ci   # swap one, same position
cpb ALTER PLAYBOOK work SET VAR BASH_DEFAULT_TIMEOUT_MS=600000   # own entry on top
```

```toml
[env]
sets = ["tools"]

[env.set]
BASH_DEFAULT_TIMEOUT_MS = "600000"
```

Sets apply in the order listed, later ones overriding earlier; the playbook's
own entries apply last. `USE ENV` states the whole list, so it is the form to
repeat safely; `ADD ENV` places one set without restating the rest;
`REPLACE ENV old WITH new` swaps one set in place (run again, it changes
nothing). Recipes state `USE ENV`; `SHOW CREATE` never writes `REPLACE`.

Manage the sets:

```bash
cpb SHOW ENVS                   # one line per set: NAME SET BLOCKED USED BY DESCRIPTION
cpb SHOW ENV tools              # one set: description, users, whether it is in DEFAULTS, variables
cpb ALTER ENV tools SET MAX_THINKING_TOKENS=8000
cpb CREATE OR REPLACE ENV tools SET …        # replace the whole content
cpb DROP ENV tools              # refused while a playbook or DEFAULTS uses it; the error lists them
```

A set that a playbook names but that is missing, unreadable or invalid
**refuses the launch** rather than silently running without it: a dropped layer
could change what the session does without a word.

## DEFAULTS: layers under every playbook

`DEFAULTS` is an ordered list of env sets applied under every playbook's own
layers, manifest or not, `start` included:

```bash
cpb ALTER DEFAULTS USE ENV claude-default          # the whole list
cpb ALTER DEFAULTS ADD ENV corp-proxy              # append (or FIRST / BEFORE x / AFTER x)
cpb ALTER DEFAULTS DROP ENV corp-proxy
cpb ALTER DEFAULTS USE ACCOUNT work                 # the account a playbook with none of its own runs on
cpb SHOW DEFAULTS                                  # the list, the account, and the secret helper
```

The list lives in `~/.claude-playbooks/.env-sets/.defaults`, one name per
line.

## Secrets

**A credential-looking literal is refused.** `SET` of a key that looks like a
credential is refused, naming the key and never the value. The rule is the one
the output uses for masking (case-insensitive): `TOKEN`, `SECRET`, `PASSWORD`,
`PASSWD`, `PASSPHRASE` or `CREDENTIAL` anywhere in the name; `AUTH`, `PWD`,
`PASS` or `PAT` as a whole `_`-separated word; or a word ending in `KEY`/`KEYS`
unless a `PUBLIC`/`PUB` word says the material is public. A value that cannot
be a secret passes: empty, an integer, or `true`/`false`, so
`SET VAR MAX_THINKING_TOKENS=8000` works. Two ways through:

- **By reference**, when a secret helper is configured:
  ```bash
  cpb ALTER DEFAULTS SET secret_helper = my-keychain-helper   # one command, no arguments
  cpb ALTER ENV tools SET GITHUB_TOKEN FROM 'keychain:github-token'
  ```
  cpb stores the reference (`[refs]` in the set, `[env.refs]` in a manifest)
  and never the value. The helper is asked `--check KEY=REF` when the statement
  runs, and at launch cpb execs `<helper> KEY=REF … -- claude <args>`, so the
  value exists only in that one process. `CPB_SECRET_HELPER` in the environment
  overrides the stored setting. Without a helper, `SET … FROM` is refused, and
  so is launching a playbook whose layers hold a reference.
  An account's credential takes a reference the same way
  (`auth_token FROM '<ref>'`, see [Accounts](accounts.md)).
  A sandboxed launch of a playbook with references is refused in this release.
- **As plain text, knowingly:** add `AS PLAINTEXT` to the `SET` clause.
  `EXPLAIN` marks such an entry `(plaintext)`, and `SHOW CREATE` never carries
  its value.

**Output never prints a credential.** `SHOW` and `EXPLAIN` mask credential-looking
values: no character of them, only the length (`<plaintext, 43 chars>`). A
credential inside a connection
URL is masked too, both halves of `user:password`, since a git remote often
carries the token as the user name:

```text
DATABASE_URL  postgres://<redacted, 4 chars>:<redacted, 11 chars>@db.internal:5432/app
```

A credential in a query parameter (`?password=`) is not covered. There is no
`--reveal`: no command prints a credential.

## One launch only

The same layers can be added for a single launch without touching any file.
Launch flags go before the playbook name, or right after it, and stop at the
first argument that is not one of them; everything after that is `claude`'s:

```bash
cpb run --env-set tools work                         # an existing env set, this launch only
cpb run work --env MAX_THINKING_TOKENS=8000 -p "..."
cpb run --account personal work                      # another account the playbook may run on
cpb run --block CLAUDE_CODE_OAUTH_TOKEN work             # on machine: this launch uses the stored login
cpb run --env-file ./job.env work                        # KEY=VALUE lines, dotenv style
cpb start --env-set tools /tmp/scratch
work --env-set tools -p "..."                        # launchers take them too, at the start
```

They apply on top of the playbook's own layer, in command-line order, and obey
the same rules: `CLAUDE_CONFIG_DIR` refused, a missing set refuses the launch, an
unset of the token switches this launch to the stored login. `cpb run --help`
lists them.

## The authentication case

Which account a playbook runs as is its account's ([Accounts](accounts.md));
on the `machine` account, blocking `CLAUDE_CODE_OAUTH_TOKEN` keeps the
machine's token file out. The full decision and every mode are in
[Authentication](authentication.md).

## A caller-supplied config directory

A playbook directory is both its **content** (`CLAUDE.md`, `settings.json`,
`hooks/`, `skills/`) and where Claude Code writes its **state** (`.claude.json`,
`sessions/`, `projects/`, `history.jsonl`). To give several sessions one
playbook's content but separate state, run it from different working
directories, or install it twice. For the narrower case of binding a config
directory you built yourself:

```bash
CPB_CONFIG_DIR=~/records/q1 cpb run work
CPB_CONFIG_DIR=~/records/q1 work
```

That directory becomes the launch's config directory; authentication, credential
sync and the manifest lookup treat it as they would the playbook's own.

- **You provision it.** cpb creates nothing, so a typo cannot mint an empty
  memory. If it should expose the playbook's `CLAUDE.md`, `hooks/` or skills,
  put them there (a symlink farm is the usual answer).
- The path must be absolute or `~`-prefixed; an empty value means unset.
- **A bare `CLAUDE_CONFIG_DIR` is ignored**, so one stray `export` cannot
  redirect every launcher on the machine.
- **It does not travel.** cpb strips it from the session, and setting it
  through a manifest, an env set, `--env` or `--env-file` is refused. An
  `export` in your shell stays in that shell until you unset it.
- **Not with `--sandbox`**: symlinked content would dangle inside the VM.
- **`cpb start` ignores it, and says so**, because `start` names its own
  directory on the command line.

## What stays yours

Env blocks and env sets are **install-local**, like the launcher. `update` keeps
your block and ignores one the source ships; `CREATE PLAYBOOK … FROM` drops a source-shipped
block with a note; nothing ships env sets or accounts, and `update` never
touches their directories. A shared playbook repository cannot redirect your
API endpoint or strip your authentication by publishing a manifest. Manifests holding values
are written `0600`; a file's mode is never loosened by a rewrite.
