# Environment overrides

Variables set or blocked for every launch of one playbook and no other, so one
playbook talks to a proxy or keeps its own login while the rest of your shell
does not. The statements below are the [CLI grammar](../reference/cli-grammar.md)
(v3.20.0); the older `env` / `env-profile` commands still work, see the end.

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
  + one-off launch flags (--env-profile, --env, --unset, --env-file)
  + CLAUDE_CONFIG_DIR, bound by the tool, cannot be overridden
  = what claude sees
```

`EXPLAIN PLAYBOOK <name>` prints that result: every variable a launch would
change, its value (masked when it looks like a credential), and the layer that
decided it. `--json` gives the same as a stable object for scripts.

A `SET` overrides whatever the shell exported; a `BLOCK` removes a variable
even when the shell exports it. Raw `claude` launches bypass all of this.
Claude Code's own `env` block in `settings.json` is applied later, inside the
`claude` process, and wins over these layers; it can set variables but cannot
remove one the shell exported, which is what `BLOCK` is for.

## One playbook, its own variables

```bash
cpb ALTER PLAYBOOK kommander SET VAR ANTHROPIC_MODEL=claude-opus-5
cpb ALTER PLAYBOOK kommander BLOCK VAR CLAUDE_CODE_OAUTH_TOKEN
```

The playbook's `.playbook` now ends with:

```toml
[env]
unset = ["CLAUDE_CODE_OAUTH_TOKEN"]

[env.set]
ANTHROPIC_MODEL = "claude-opus-5"
```

Inspect and undo:

```bash
cpb SHOW PLAYBOOK kommander                      # its own layer, under "Variables:"
cpb EXPLAIN PLAYBOOK kommander                   # what a launch would apply, and from where
cpb ALTER PLAYBOOK kommander UNSET VAR ANTHROPIC_MODEL   # forget the entry; the layer below applies again
```

`UNSET VAR` forgets the playbook's own entry, whether it was a `SET`, a
reference or a `BLOCK`. Inside `ALTER PLAYBOOK` the word `VAR` is required.

## Env profiles: define once, attach to many

When several playbooks want the same variables, put them in an **env set**
(called an env profile before v3.20.0): a named file under
`~/.claude-playbooks/.env-profiles/`, attached to playbooks by name.

```bash
cpb CREATE ENV glm DESCRIBE 'GLM through the local router' \
    SET ANTHROPIC_BASE_URL=http://proxy:1/v1 ANTHROPIC_DEFAULT_OPUS_MODEL=glm/glm-5.3 \
    BLOCK CLAUDE_CODE_OAUTH_TOKEN
```

That wrote `~/.claude-playbooks/.env-profiles/glm.toml` (mode `0600`):

```toml
description = "GLM through the local router"
unset = ["CLAUDE_CODE_OAUTH_TOKEN"]

[set]
ANTHROPIC_BASE_URL = "http://proxy:1/v1"
ANTHROPIC_DEFAULT_OPUS_MODEL = "glm/glm-5.3"
```

Inside `CREATE ENV` / `ALTER ENV` the word `VAR` is optional. Attach the set;
the playbook's manifest records only its name:

```bash
cpb ALTER PLAYBOOK router USE ENV glm                 # the whole list, in order
cpb ALTER PLAYBOOK router ADD ENV work FIRST          # insert one: FIRST, LAST (default), BEFORE x, AFTER x
cpb ALTER PLAYBOOK router DROP ENV work               # detach
cpb ALTER PLAYBOOK router SET VAR ANTHROPIC_DEFAULT_OPUS_MODEL=glm/glm-5.4   # own entry on top
```

```toml
[env]
profiles = ["glm"]

[env.set]
ANTHROPIC_DEFAULT_OPUS_MODEL = "glm/glm-5.4"
```

Sets apply in the order listed, later ones overriding earlier; the playbook's
own entries apply last. `USE ENV` states the whole list, so it is the form to
repeat safely; `ADD ENV` places one set without restating the rest.

Manage the sets:

```bash
cpb SHOW ENVS                   # one line per set: NAME SET BLOCKED USED BY DESCRIPTION
cpb SHOW ENV glm                # one set: description, users, whether it is in DEFAULTS, variables
cpb ALTER ENV glm SET ANTHROPIC_DEFAULT_HAIKU_MODEL=glm/glm-5.3-flash
cpb CREATE OR REPLACE ENV glm SET …          # replace the whole content
cpb DROP ENV glm                # refused while a playbook or DEFAULTS uses it; the error lists them
```

A set that a playbook names but that is missing, unreadable or invalid
**refuses the launch** rather than silently running without it: a dropped layer
could send traffic to the wrong endpoint with the wrong credentials.

## DEFAULTS: layers under every playbook

`DEFAULTS` is an ordered list of env sets applied under every playbook's own
layers, manifest or not, `start` included:

```bash
cpb ALTER DEFAULTS USE ENV claude-default          # the whole list
cpb ALTER DEFAULTS ADD ENV metu-proxy              # append (or FIRST / BEFORE x / AFTER x)
cpb ALTER DEFAULTS DROP ENV metu-proxy
cpb SHOW DEFAULTS                                  # the list, and the secret helper
```

The list lives in `~/.claude-playbooks/.env-profiles/.default`, one name per
line; a single-name file from an older cpb is read as a one-element list. An
older cpb refuses a multi-line file rather than guess.

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
  cpb ALTER DEFAULTS SET SECRET HELPER my-keychain-helper   # one command, no arguments
  cpb ALTER ENV glm SET ANTHROPIC_AUTH_TOKEN FROM 'keychain:9router'
  ```
  cpb stores the reference (`[refs]` in the set, `[env.refs]` in a manifest)
  and never the value. The helper is asked `--check KEY=REF` when the statement
  runs, and at launch cpb execs `<helper> KEY=REF … -- claude <args>`, so the
  value exists only in that one process. `CPB_SECRET_HELPER` in the environment
  overrides the stored setting. Without a helper, `SET … FROM` is refused, and
  so is launching a playbook whose layers hold a reference.
  `CLAUDE_CODE_OAUTH_TOKEN` never takes a reference: cpb reads its value itself.
  A sandboxed launch of a playbook with references is refused in this release.
- **As plain text, knowingly:** add `AS PLAINTEXT` to the `SET` clause.
  `EXPLAIN` marks such an entry `(plaintext)`, and `SHOW CREATE` never carries
  its value.

**Output never prints a credential.** `SHOW` and `EXPLAIN` mask credential-looking
values, showing only the ends and the length (`sk-a...7f2c (43 chars)`); a value
too short to hide 8 characters is masked whole. A credential inside a connection
URL is masked too, both halves of `user:password`, since a git remote often
carries the token as the user name:

```text
DATABASE_URL  postgres://<redacted, 4 chars>:<redacted, 11 chars>@db.internal:5432/app
```

A credential in a query parameter (`?password=`) is not covered. The grammar has
no `--reveal`; the older `env … --reveal` still exists.

## One launch only

The same layers can be added for a single launch without touching any file.
Launch flags go before the playbook name, or right after it, and stop at the
first argument that is not one of them; everything after that is `claude`'s:

```bash
cpb run --env-profile work kommander                     # an existing env set, this launch only
cpb run kommander --env ANTHROPIC_MODEL=claude-opus-5 -p "..."
cpb run --unset CLAUDE_CODE_OAUTH_TOKEN kommander        # this launch uses the stored login
cpb run --env-file ./work-account.env kommander          # KEY=VALUE lines, dotenv style
cpb start --env-profile glm /tmp/scratch
kommander --env-profile work -p "..."                    # launchers take them too, at the start
```

They apply on top of the playbook's own layer, in command-line order, and obey
the same rules: `CLAUDE_CONFIG_DIR` refused, a missing set refuses the launch, an
unset of the token switches this launch to the stored login. `cpb run --help`
lists them.

## The authentication case

Setting or blocking `CLAUDE_CODE_OAUTH_TOKEN` changes which account a playbook
runs as. The full decision and every mode are in
[Authentication](authentication.md).

## A caller-supplied config directory

A playbook directory is both its **content** (`CLAUDE.md`, `settings.json`,
`hooks/`, `skills/`) and where Claude Code writes its **state** (`.claude.json`,
`sessions/`, `projects/`, `history.jsonl`). To give several sessions one
playbook's content but separate state, run it from different working
directories, or install it twice. For the narrower case of binding a config
directory you built yourself:

```bash
CLAUDE_CONFIG_DIR_OVERRIDE=~/records/q1 cpb run kommander
CLAUDE_CONFIG_DIR_OVERRIDE=~/records/q1 k
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
your block and ignores one the source ships; `install` drops a source-shipped
block with a note; nothing ships env sets and `update` never touches their
directory. A shared playbook repository cannot redirect your API endpoint or
strip your authentication by publishing a manifest. Manifests holding values
are written `0600`; a file's mode is never loosened by a rewrite.

## Older commands

`env` and `env-profile` still work, with their flags, and write the same files. They are hidden from help, and on a terminal each prints one stderr line naming its statement.
The statement for each:

| Older | Statement |
|---|---|
| `env <n> set K=V` / `unset K` / `clear K` | `ALTER PLAYBOOK <n> SET VAR K=V` / `BLOCK VAR K` / `UNSET VAR K` |
| `env <n> use P` / `unuse P` | `ALTER PLAYBOOK <n> ADD ENV P` / `DROP ENV P` |
| `env <n>` | `EXPLAIN PLAYBOOK <n>` |
| `env-profile P set K=V` / `unset K` / `clear K` | `ALTER ENV P SET K=V` / `BLOCK K` / `UNSET K` (`CREATE ENV` when new) |
| `env-profile P describe TEXT` | `ALTER ENV P DESCRIBE 'TEXT'` |
| `env-profile P default` / `undefault` | `ALTER DEFAULTS USE ENV P` / `DROP ENV P` |
| `env-profile P delete` | `DROP ENV P` |
| `env-profile [--values]` | `SHOW ENVS` |
