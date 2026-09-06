# claude-playbook CLI Specification (v4)

## Overview

`claude-playbook` is a CLI tool for creating and managing **Claude Code playbooks**. A playbook is an isolated Claude Code instance — a directory with its own settings, CLAUDE.md, hooks, MCP servers, and history, completely separate from the default `~/.claude/` installation and from every other playbook.

Playbooks solve a simple problem: Claude Code stores everything in a single config directory. If you want to try a new hook, a different model default, or a custom CLAUDE.md without risking your main setup, you need a separate environment. Under the hood, a playbook is just a directory, and Claude Code reads from wherever `CLAUDE_CONFIG_DIR` points. `claude-playbook` makes creating, running, sharing, and maintaining those directories easy.

---

## Concepts

### Isolation

Every playbook is a directory that Claude Code treats as its entire configuration root. Launching Claude Code with `CLAUDE_CONFIG_DIR=<dir>` produces a completely fresh, independent instance.

```bash
# Default Claude Code
claude

# An isolated playbook
CLAUDE_CONFIG_DIR=~/.claude-playbooks/experiment claude
```

`claude-playbook` is a thin convenience layer over this pattern.

### Authentication preparation

Every launch (`run`, `start`, launcher dispatch) prepares the config directory's authentication before exec, in `auth.PrepareLaunchEnv`. The decision, in order:

1. **Isolation wins.** If the nearest valid manifest walking up from the config directory has `isolate_auth = true`, or `CLAUDE_PLAYBOOKS_ISOLATE_AUTH=true` is set: detach a symlinked `.credentials.json` (the target file survives), sync no account metadata, strip `CLAUDE_CODE_OAUTH_TOKEN` and the plan descriptors (`CLAUDE_CODE_SUBSCRIPTION_TYPE`, `CLAUDE_CODE_RATE_LIMIT_TIER`) from the child environment. A `CLAUDE_CODE_OAUTH_TOKEN` the playbook's own flattened `[env]` sets is honoured as its own token: injected, with the stored `claudeAiOauth` grant quarantined as on the token path.
2. **Token active?** True when the flattened `[env]` sets `CLAUDE_CODE_OAUTH_TOKEN` to a non-empty value, else when the process environment carries it, else when `~/.config/claude-code/oauth-token` (overridable via `CLAUDE_PLAYBOOKS_OAUTH_TOKEN_FILE`) is non-empty; false when the flattened `[env]` unsets it, regardless of the rest.
3. **Token path.** The plan descriptors (`CLAUDE_CODE_SUBSCRIPTION_TYPE`, `CLAUDE_CODE_RATE_LIMIT_TIER`) are appended from the global account's store only when the token IS the machine-global one, read from the token file by this launch; a token the flattened `[env]` block supplies (`own-token`) belongs to some other account, so the global descriptors are neither appended nor inherited (the block may set its own); a token inherited from the shell is of unknown provenance, so nothing is appended and whatever descriptors the shell exported beside it pass through unchanged. Then: quarantine the playbook's stored `claudeAiOauth` grant (other keys such as `mcpOAuth` survive; a symlinked store is detached, never written through; the global store is never quarantined — identity is decided with symlinks resolved and `os.SameFile` for the directory and for the credentials file itself, so `start <symlink-to-~/.claude>`, a linked registration of it, or a config directory whose store is the target of a symlinked `~/.claude/.credentials.json` are all recognised as the global store), sync non-secret account metadata into `.claude.json` so an interactive start presents as logged in, inject the token (replacing any inherited entry), and append the plan descriptors read from the account's own store. Rationale: under token auth Claude Code never refreshes a stored grant, and its 401-recovery path adopts a differing stored `accessToken` over the environment token; an expired stored grant would replace a working token with a dead one.
4. **No-token path.** `SyncCredentials`: the playbook's `.credentials.json` becomes a symlink to the global `~/.claude/.credentials.json` (an existing regular file with a valid grant is left alone; a grantless store never overwrites the global one; a target that is the global directory by identity, or whose store is the global file itself, is left untouched), so `/login` in any playbook is visible to all. Nothing is removed. Claude Code refreshes the shared grant itself.

Every preparation failure is advisory (a warning, then launch) except a profile that cannot be resolved, which refuses the launch before step 1. Raw `claude` invocations bypass this entirely.

### The filesystem is the source of truth

There is no index file, no database, no registry file. The state the tool reads from or writes to is: the playbooks root directory (including the optional `.playbook` manifests inside it, where a custom command alias is recorded, and the dot-prefixed `.env-profiles/` directory holding shared env profiles), the launcher directory (symlinks to the binary that serve as per-playbook commands), a flock lock file in the user cache dir (`<cache>/claude-playbook/registry.lock`, used only to serialize concurrent mutations — it holds no data). Any mutation users make with `mv`, `rm`, or a text editor is immediately consistent with what the tool sees on its next invocation.

### The playbooks root

All playbooks live under a single **playbooks root** directory. The default is `~/.claude-playbooks/`. This is configurable via the `--playbooks-dir` flag or the `CLAUDE_PLAYBOOKS_DIR` environment variable, applied globally to every command.

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

Directories nested more deeply than the first level are just ordinary files belonging to a playbook — they are never themselves discovered as playbooks. If you drop a whole monorepo into the root by hand, the tool sees one playbook (the top directory), not the playbooks inside it; use `install --subdir` to extract the slice you actually want.

### Playbook names

A playbook's **name** is simply its directory name under the playbooks root:

- `experiment`
- `sre`
- `dba`

Names are used wherever a playbook is referenced: `run`, `delete`, `info`, `rename`, `alias`, `env`, `auth status`, `update`. Env profile names are a separate namespace (`env-profile`).

The charset is enforced for names being **created** (`create`, `install`, `link`, `rename`): a name must match `^[A-Za-z0-9_][A-Za-z0-9_-]*$` — letters, digits, underscores and dashes, starting with an alphanumeric or underscore. A playbook name is interpolated into a launcher command name, a `run <name>` argument, and commands printed for the user to paste, so shell metacharacters are rejected at the front door rather than escaped at each site. Names must not start with `.` (to avoid hidden directories) and must not contain `/` or `\` (names are single directory segments, never paths). Lookup paths (`delete`, discovery) only require a single path segment, so an existing playbook with an odd name can still be listed, run and removed.

---

## Launcher Commands (v2.13.0)

Since v2.13.0, per-playbook commands are **launchers** — symlinks to the `claude-playbook` binary placed in a PATH directory — replacing the shell-alias registration of earlier releases. `create`, `install`, and `link` register one.

- **Multicall dispatch.** Invoked through a launcher, the binary sees the link's name in argv[0] and dispatches as `run <name>` (the busybox/git pattern). The launcher carries no state: the name resolves at invocation time against the live registry — playbook directory names first, then manifest `alias` fields — so nothing goes stale on rename or move.
- **Launcher directory.** `--launcher-dir` / `CLAUDE_LAUNCHER_DIR`, else the directory the binary was invoked from (on PATH by construction), falling back to `~/.local/bin` when that is unwritable.
- **Default root only.** Launcher mutations happen only when operating on the default playbooks root (`~/.claude-playbooks`). A symlink carries no root identity, so managing links on behalf of a custom `--playbooks-dir` root would corrupt the default registry's commands; under a custom root the tool prints a note and the `claude-playbook --playbooks-dir <root> run <name>` form instead.
- **Reserved names.** `claude-playbook` and `cpb` always mean the CLI itself; they never dispatch and may never name a launcher.
- **Collisions and locking.** The registry is the ownership authority: a command name that already addresses another playbook (by directory name or manifest alias) is a hard error before any mutation. Preflight-through-registration is serialized across concurrent processes by a flock in the user cache dir (`<cache>/claude-playbook/registry.lock`).
- **Retention doctrine.** `delete` and `rename` keep a launcher whose name still resolves to another playbook, and also keep unclaimed ones — a stateless symlink may be serving a playbook in another registry root — printing `Kept command ...` with a manual `rm <path>` hint.
- **Stale launchers fail loudly.** Invoking a launcher whose name no longer resolves errors with `unknown playbook "<name>" — this launcher no longer matches any playbook` and exit code 1, never a silent fall-through to the CLI overview.
- **Foreign files are never touched.** A file occupying a launcher name that is not a symlink to this binary is left alone; attempting to write over it degrades to a warning with manual instructions.

---

## Commands

### `claude-playbook` (no arguments)

Prints a one-line description and lists all discovered playbooks with how to run each. The parenthetical shows the playbook's registered command (its launcher, by directory name or manifest alias); a playbook with none shows `(no command registered)`.

```
claude-playbook -- manage isolated Claude Code instances

Playbooks directory: ~/.claude-playbooks

Available playbooks:

  experiment    claude-playbook run experiment    (or: experiment)
  sre           claude-playbook run sre           (or: sre)
  dba           claude-playbook run dba           (no command registered)

Run 'claude-playbook --help' for all commands.
```

Empty state:
```
claude-playbook -- manage isolated Claude Code instances

Playbooks directory: ~/.claude-playbooks
No playbooks installed yet. Get started with one of:

  # Install a single playbook from a Git repo:
  claude-playbook install https://github.com/user/pai

  # Cherry-pick one playbook out of a monorepo (e.g. DBA):
  claude-playbook install https://github.com/ramazanpolat/awesome-playbooks/tree/main/playbooks/dba

  # Create your own from scratch:
  claude-playbook create <name>

Run 'claude-playbook --help' for all commands.
```

---

### `claude-playbook list [prefix]`

Lists all playbooks in a table. If a `prefix` argument is given, only playbooks whose names start with that prefix are shown.

```bash
claude-playbook list
claude-playbook list s
```

**Output:**

```
NAME          PATH                                  COMMAND  LAST USED
----          ----                                  -------  ---------
experiment    ~/.claude-playbooks/experiment        -        2 days ago
sre           ~/.claude-playbooks/sre               sre      1 hour ago
dba           ~/.claude-playbooks/dba               -        never
```

Column widths are computed from the longest NAME, PATH, and COMMAND values, with minimum widths of 4, 4, and 7. `COMMAND` shows the playbook's launcher command name (matching its directory name or manifest alias). `-` means no command is registered. `LAST USED` is derived from the playbook directory's mtime.

---

### `claude-playbook create <name>`

Creates a new, empty playbook.

```bash
claude-playbook create experiment
claude-playbook create experiment --no-alias
claude-playbook create experiment --alias exp
```

**Steps:**
1. Validate the name (single segment; enforced charset; must not start with `.`).
2. Check the target directory does not exist.
3. Preflight the command names against the registry under the registry lock — the directory name, plus the launcher name (`--alias` or the playbook name) unless `--no-alias` — erroring before anything is created if a name already addresses another playbook.
4. Create the directory and write a starter `CLAUDE.md` into it (a short template explaining what a playbook is and how to customize it).
5. Unless `--no-alias`, register a **launcher command**: a symlink to the `claude-playbook` binary in the launcher directory. The command name defaults to the playbook name. Override with `--alias`.

`create` writes **no `.playbook` manifest** in the default case — the directory is a valid playbook simply by living under the playbooks root. The one exception: when `--alias` differs from the playbook name, `create` writes a `.playbook` recording the alias, because multicall dispatch resolves the command name against the registry at invocation time and a custom name is only findable through the manifest `alias` field. If that manifest write fails, the directory is rolled back. Add a `.playbook` yourself only when you want to set metadata (version, description, homepage, author).

**Flags:**

| Flag | Description |
|------|-------------|
| `--alias <alias>` | Use a custom launcher command name (default: the playbook name) |
| `--no-alias` | Skip launcher creation |

`--alias` and `--no-alias` cannot be combined.

**Errors:**
- Name already exists → `playbook "experiment" already exists at ~/.claude-playbooks/experiment`
- Name starts with `.` → `playbook name cannot start with '.'`
- Name contains a slash → `playbook name cannot contain '/'`
- Command name taken → `command name "exp" already addresses playbook "other". Pick another name or alias`
- Both `--alias` and `--no-alias` → `--no-alias and --alias cannot be used together`

---

### `claude-playbook run [launch-flags] <name> [launch-flags] [claude-flags...]`

Runs Claude Code using the named playbook. Any flags after the name are forwarded to `claude` unchanged, except a leading run of **launch flags**, which add one-off environment layers for this launch only.

```bash
claude-playbook run experiment
claude-playbook run sre
claude-playbook run sre --model claude-opus-5
claude-playbook run --env-profile work sre                       # one launch with an env profile
claude-playbook run sre --env ANTHROPIC_MODEL=claude-opus-5      # flags may also follow the name
claude-playbook run --unset CLAUDE_CODE_OAUTH_TOKEN sre          # this launch uses the stored login
claude-playbook run --env-file ./work.env sre -p "..."
```

**Launch flags** (each repeatable; value as the next argument or after `=`):

| Flag | Layer |
|------|-------|
| `--env-profile NAME` | an existing env profile (`<playbooks root>/.env-profiles/NAME.toml`); a missing or invalid one refuses the launch |
| `--env KEY=VALUE` | set one variable |
| `--unset KEY` | remove one variable |
| `--env-file PATH` | a dotenv-style file: `KEY=VALUE` per line, blank and `#` lines skipped, a leading `export ` tolerated, one pair of matching surrounding quotes stripped, later lines win; every line validated like a manifest entry |

Scanning: launch flags are recognised only as a **leading run**, before the name and again immediately after it; the first argument that is not a launch flag ends the scan and everything from there on is `claude`'s. Through a launcher (`sre --env K=V -p hi`) the same rule applies to the arguments after the command name. Keys and values follow the manifest rules (`CLAUDE_CONFIG_DIR` reserved, valid UTF-8, no NUL). The layers apply on top of the playbook's flattened `[env]` block in command-line order, are resolved against the same profiles root, drive the token decision exactly as the block would (a one-off `--unset CLAUDE_CODE_OAUTH_TOKEN` takes the stored-credentials path for this launch), and are never written to disk.

Equivalent to:
```bash
CLAUDE_CONFIG_DIR=~/.claude-playbooks/<name> claude [claude-flags...]
```

Flag parsing is disabled so arbitrary `claude` flags pass through. The global `--playbooks-dir` flag is extracted from the argument list before forwarding; launch flags are extracted from the leading positions described above.

**Errors:**
- Playbook not found → `unknown playbook "experiment". Run 'claude-playbook list' to see available playbooks`
- Launch flag without a value → `flag needs an argument: --env`
- `--env` without `=` → `--env expects KEY=VALUE, got "X"`; `--unset` with `=` → `--unset expects a variable name, got "K=V"`
- Invalid key, reserved key, or bad value → the manifest's `[env]` errors (`invalid environment variable name "x"`, `CLAUDE_CONFIG_DIR is managed by claude-playbook and cannot be overridden`, ...)
- `--env-file` problems → `--env-file: <path>:<line>: expected KEY=VALUE` or `<path>:<line>: invalid environment variable name (not shown; the line may hold a secret)`: neither the line nor a rejected key is echoed, since a secret containing `=` splits into a bogus key; the reserved-key and value errors are the manifest's, prefixed with `<path>:<line>`
- `--env-profile` missing or broken → the launch refusal from `env` (`env profile "x" not found in <dir> ...`)
- `claude` not on PATH → `'claude' command not found. Install Claude Code first: https://claude.ai/download`

---

### `claude-playbook start [launch-flags] <path> [claude-flags...]`

Starts an ad-hoc Claude Code session at any directory. Creates the directory if it doesn't exist. No playbook registration, no `.playbook` file, no discovery — just set `CLAUDE_CONFIG_DIR` and run. The throwaway-experiment command.

```bash
claude-playbook start /tmp/scratch
claude-playbook start /tmp/scratch --model claude-opus-5
claude-playbook start /tmp/scratch --delete
claude-playbook start --env-profile glm /tmp/scratch             # launch flags go before the path
```

The launch flags of `run` (`--env-profile`, `--env`, `--unset`, `--env-file`) are accepted before the path, with the same semantics; profiles resolve from the root named by `--playbooks-dir` or `CLAUDE_PLAYBOOKS_DIR`.

Equivalent to:
```bash
CLAUDE_CONFIG_DIR=/tmp/scratch claude [claude-flags...]
```

**Flags:**

| Flag | Description |
|------|-------------|
| `--delete` | Delete the directory when the session ends |
| `--env-profile`, `--env`, `--unset`, `--env-file` | One-off environment layers; see `run` |

All wrapper flags, `--delete` included, are recognised only as a **leading run** before the path and again immediately after it; the first other argument, or a literal `--`, ends the run and everything from there on is `claude`'s verbatim. A `--delete` that appears later -- after another `claude` argument, as the value of a `claude` flag such as `-p --delete`, or after `--` -- is forwarded, never treated as permission to remove the directory. `--` is never accepted as the path itself (`path required`).

`--delete` runs after `claude` exits regardless of exit code. If deletion fails, a warning is printed to stderr but the tool preserves `claude`'s exit code.

**Errors:**
- Path exists and is a file → `"/tmp/foo" is not a directory`
- Cannot create directory → `could not create "/tmp/foo": <reason>`
- No path given → `path required`
- `claude` not on PATH → same as `run`

---

### `claude-playbook install <source>`

Installs a single playbook from a Git repository or a local directory. The result is always **one flat playbook** under the playbooks root.

`install` always **copies** the source into the playbooks root — both Git URLs (via clone) and local directories (via recursive copy). The installed playbook is a self-contained, independent copy; later edits to the original source do not affect it. To keep an *external* directory in place and expose it under the playbooks root as a symlink instead of a copy, use the separate [`link`](#claude-playbook-link-target) command.

```bash
# Git repo (derives install name from the URL)
claude-playbook install https://github.com/user/pai

# Git repo with a custom install name
claude-playbook install https://github.com/user/repo --name myrepo

# Install a specific branch/tag
claude-playbook install https://github.com/user/repo --branch dev

# Local directory (copied into the playbooks root, becomes independent of source)
claude-playbook install ~/dev/my-playbook

# Install one playbook out of a monorepo (the primary multi-playbook-repo path)
claude-playbook install https://github.com/ramazanpolat/awesome-playbooks/tree/main/playbooks/sre --name sre --alias sre

# Same thing, spelled with explicit flags
claude-playbook install https://github.com/ramazanpolat/awesome-playbooks --subdir playbooks/sre --branch main --name sre --alias sre
```

**Source types:**

| Source | Behaviour |
|--------|-----------|
| URL (`http://`, `https://`, `git@`, `git://`, `ssh://`, `file://`) | Shallow-cloned (`git clone --depth=1`) into the install directory |
| GitHub `/tree/<ref>/<path>` URL | Recognized and split automatically into clone URL + `--branch <ref>` + `--subdir <path>`; remote refs are consulted so branch names containing `/` work |
| Anything else | Treated as a local filesystem path and **copied** into the install directory |

**Flags:**

| Flag | Description |
|------|-------------|
| `--name <name>` | Override the install directory name under the playbooks root |
| `--subdir <path>` | Install only this subdirectory of the source (see below) |
| `--branch <ref>` | Git URL only: clone this branch/tag/ref instead of the default branch |
| `--alias <alias>` | Custom launcher command name for the installed playbook |
| `--no-alias` | Skip launcher creation |

**Steps (no `--subdir`):**
1. Stage the source (Git URL → `git clone --depth=1`, with `--branch <ref>` if given, into a temp dir; local path → read in place) so its `.playbook` can be consulted before choosing a name.
2. Derive the install directory name from `--name`, or the manifest `name`, or the last path segment of the URL (stripped of `.git`), or the source directory's name.
3. Check the target doesn't already exist under the playbooks root.
4. Preflight command names against the registry under the registry lock — the install name and the effective alias (`--alias`, or the staged manifest's `alias`) — erroring **before anything is copied** if a name already addresses another playbook.
5. Copy the staged tree into the target. The installed directory **is** the playbook. If a `.playbook` is present it supplies metadata; if not, the directory is still a valid playbook.
6. Register a launcher command per the rules below.
7. Print a summary.

`install` never writes a `.playbook` into the source, and never needs one to succeed.

**Steps (with `--subdir <path>`):**
1. Fetch the source as above into a scratch location (for URLs, a temp directory; for local paths, the source itself).
2. Verify `<source>/<path>` exists and is a directory.
3. Copy `<source>/<path>` into `~/.claude-playbooks/<name>/`. For URL sources, the rest of the clone is discarded.
4. Treat the result as one flat playbook. Any `.playbook` already inside `<source>/<path>` provides metadata for that one playbook.
5. Default install name (`--name` not given) is the last segment of `<path>`.

`--subdir` is how you consume a monorepo — a repo laid out as `playbooks/sre`, `playbooks/dba`, `playbooks/frontend`, etc. Each `install --subdir` (or `/tree/<ref>/<path>` URL) copies everything under that one directory into its own playbook. To take several, run several installs; there is intentionally no "install the whole suite at once."

**Default command name**

One launcher is registered, named by the `--alias` value, or the manifest's `alias` field, or its `name` field, or the install directory name, in that order. `--no-alias` skips it. When `--alias` differs from what the installed manifest records, the alias is written into the installed playbook's `.playbook` — a custom command name is only resolvable at invocation time through the manifest `alias` field (on manifest-write failure the install is rolled back).

**Command-name collision handling**: collisions against the registry are a hard **pre-copy error**, not a skip-with-warning — `command name "sre" already addresses playbook "other". Pick another name or alias`, and nothing is copied. Only a *foreign file* (not a launcher) already occupying the name in the launcher directory degrades to a post-install warning: the playbook is installed and runnable via `claude-playbook run <name>`, and the warning suggests renaming or removing the conflicting file.

**CLAUDE.md warning:** if the installed playbook has no `CLAUDE.md`, a warning is printed. Claude Code works without one, but most playbooks benefit from having one.

**Errors:**
- `--branch` with a local path → `--branch only applies to Git URLs`
- `--subdir` path missing in source → `subdirectory "<path>" not found in source`
- Source not found → `'~/dev/foo' not found`
- Source is a file → `'~/dev/foo' is not a directory`
- Install name already taken → `"myrepo" already exists at ~/.claude-playbooks/myrepo. Use --name to choose a different name`
- Command name taken → `command name "sre" already addresses playbook "other". Pick another name or alias`
- `git` not on PATH → `'git' command not found`
- Clone fails → git's error output is shown directly

**Sample output:**
```
Cloning https://github.com/ramazanpolat/awesome-playbooks (branch main) (subdir playbooks/sre)...
Installed "sre" at ~/.claude-playbooks/sre
Command:  sre  (launcher at /Users/you/.local/bin/sre)

Run it now:
  sre
```

No shell reload is needed — the launcher is a symlink in a PATH directory, live the moment it is written. If the launcher directory is not on PATH, or another executable shadows the command, a warning explains the fix.

---

### `claude-playbook link <target>`

Symlinks an existing **external** directory into the playbooks root, exposing it as a playbook without copying it. Unlike `install` (which always copies and leaves the source untouched), `link` keeps the directory where it lives and points a symlink at it — edits made in either place are the same files. This is the way to develop a playbook in a working tree while running it through `claude-playbook`.

```bash
claude-playbook link ~/dev/my-playbook
claude-playbook link ~/dev/my-playbook --name mp --alias mp
claude-playbook link ~/dev/my-playbook --no-alias
```

**Steps:**
1. Resolve `<target>` to an absolute path; it must exist and be a directory.
2. Pick the link name from `--name`, or the target's basename. It must be a single-segment name (no `/`).
3. Check `<root>/<name>` does not already exist.
4. If the target has no `.playbook`:
   - If stdin is a TTY, prompt interactively for a playbook name, alias, and description (the `--alias` value seeds the alias default), and **write a `.playbook` into the target directory** with those values. The prompt runs *before* the registry lock is taken; if a concurrent link initialized the manifest in the meantime, that manifest wins and the prompted metadata is discarded with a note.
   - If stdin is not a TTY, error out — there is nothing to prompt with. Add a `.playbook` to the target first.
5. Preflight command names against the registry under the registry lock — the link name and the effective alias (`--alias`, or the target manifest's `alias`) — erroring before the symlink joins the registry if a name already addresses another playbook.
6. Create the symlink `<root>/<name>` → `<target>`.
7. Unless `--no-alias`, register a launcher command. The command name comes from `--alias`, or the target manifest's `alias`, or the link name.

**`--alias` persistence.** A custom command name must be resolvable at invocation time, so `--alias` is persisted into the target's `.playbook` as its `alias` field — but only when that manifest was created by this invocation (the flag then wins over whatever was typed at the prompt, and it is persisted before the symlink joins the registry). A **pre-existing** target manifest is shared state: the same external directory may already be linked from other registry roots whose launchers resolve through it, so `--alias` that differs from its recorded alias — including adding one where none exists — is refused: `target's .playbook is shared state (alias "x"); --alias "y" would mutate it for every registration of this target. Use the manifest's alias or edit the target's .playbook directly`. `--alias` matching the recorded alias is fine (nothing to write).

**Flags:**

| Flag | Description |
|------|-------------|
| `--name <name>` | Name under the playbooks root (default: the target's basename) |
| `--alias <alias>` | Launcher command name (default: the link name) |
| `--no-alias` | Skip launcher creation |

`--alias` and `--no-alias` cannot be combined.

**Errors:**
- Target not found → `'~/dev/foo' not found`
- Target is a file → `'~/dev/foo' is not a directory`
- Name contains a slash → `link name may not contain '/'`
- Name already taken → `"mp" already exists at ~/.claude-playbooks/mp. Use --name to choose a different name`
- Command name taken → `command name "mp" already addresses playbook "other". Pick another name or alias`
- `--alias` against a pre-existing shared manifest → the shared-state refusal above
- No `.playbook` and stdin is not a TTY → `target has no .playbook and stdin is not a TTY; cannot prompt for metadata. Add a .playbook to the target first`

Because the entry under the playbooks root is a symlink, `info` reports its `Type` as `symlink → <target>` (or `symlink → <target> (BROKEN)` if the target is gone), and `delete` removes only the link, never the target.

---

### `claude-playbook info <name>`

Shows detailed information about a playbook.

```bash
claude-playbook info sre
```

**Output:**
```
Name:        sre
Version:     1.2.0
Path:        ~/.claude-playbooks/sre
Type:        directory
Alias:       sre
Size:        24 files, 3 directories
Last used:   2 hours ago
Description: Site Reliability Engineering assistant
Homepage:    https://github.com/ramazanpolat/awesome-playbooks
Author:      Ramazan Polat
Update from: https://github.com/ramazanpolat/awesome-playbooks
Migrations:  migrations/apply.sh
```

**Fields:**

| Field | Meaning |
|-------|---------|
| `Name` | Playbook name (its directory name under the playbooks root) |
| `Version` | `version` field from the `.playbook` manifest, if set |
| `Path` | Absolute path to the directory |
| `Type` | `directory`, `symlink → <target>`, or `symlink → <target> (BROKEN)` |
| `Alias` | The playbook's manifest alias, or `(none)` |
| `Size` | File and directory counts |
| `Last used` | Human-readable time since the directory was last modified |
| `Description` | From `.playbook` manifest, if present |
| `Homepage` | From `.playbook` manifest, if present |
| `Author` | From `.playbook` manifest, if present |
| `Env` | One line per `[env]` entry (`profiles a, b`, `set KEY=VALUE`, `unset KEY`) when the manifest declares any; omitted otherwise |
| `Update from` | `[source].repository` from the manifest, else `(no [source] metadata; cannot update)` |
| `Migrations` | `migrations/apply.sh` when it exists and is executable; omitted otherwise |

**Errors:**
- Target not found → `unknown playbook "experiment"`

---

### `claude-playbook rename <old-name> <new-name>`

Renames a playbook directory and keeps its command registrations consistent.

```bash
claude-playbook rename experiment exp-1
claude-playbook rename sre site-reliability
```

**Steps:**
1. Validate the old name exists and the new name doesn't. Both must be single-segment names.
2. Persist any requested manifest-alias change, then rename the directory.
3. Retire the old name's launcher (claim-aware) and register the new command.

**Flags:**

| Flag | Description |
|------|-------------|
| `--alias <alias>` | Set the manifest alias (and its launcher) for the renamed playbook |
| `--no-alias` | Drop the alias and launcher registration |

`--alias` and `--no-alias` cannot be combined.

**Errors:**
- Old name not found → `unknown playbook "experiment"`
- New name already exists → `"exp-1" already exists at ~/.claude-playbooks/exp-1`
- Either name contains a slash → `playbook name cannot contain '/'`
- Both `--alias` and `--no-alias` → `--no-alias and --alias cannot be used together`

---

### `claude-playbook alias [name] [new-alias]`

Shows or manages a playbook's **alias**: one alternate command name, recorded in the playbook's `.playbook` manifest and materialized as a launcher command. A playbook is addressed by its directory name plus at most one alias. **Read-only with zero or one argument** — no hidden side effects.

```bash
claude-playbook alias                    # list every playbook's alias
claude-playbook alias sre                # show the alias for this playbook, or say "none"
claude-playbook alias sre s              # set the alias to 's' (replaces any previous one)
claude-playbook alias sre --remove       # remove the alias and its launcher
```

**No arguments** — lists all playbooks and their aliases:

```
experiment    exp
sre           s
dba           (no alias)
```

**One argument, no alias** — reports only; does **not** create one.
```
Playbook "dba" has no alias set.
Use 'claude-playbook alias dba <alias-name>' to set one.
```

**Two arguments** — sets the alias: validates the name (reserved names and the playbook's own name are refused), preflights registry ownership under the registration lock, records the alias in the manifest (bootstrapping one for a flat playbook), retires the previous alias's launcher (claim-aware), and writes the new launcher.

**With `--remove`** — clears the manifest alias and removes its launcher (claim-aware: a name that still addresses another playbook keeps its launcher).

Linked playbooks: the manifest is the LINK TARGET's shared state, so alias mutations are refused — edit the target's manifest directly if you really mean it.

**Flags:**

| Flag | Description |
|------|-------------|
| `--remove` | Remove the alias for the named playbook |

**Errors:**
- Playbook not found → `unknown playbook "sre"`
- Alias name reserved, invalid, or already addressing another playbook
- Linked playbook → refused (shared manifest)

---

### `claude-playbook env [name] [set KEY=VALUE... | unset KEY... | clear KEY...]`

Shows or manages a playbook's **environment overrides**: the `[env]` block of its `.playbook` manifest, applied to the child `claude` process by `run`, `start`, and launcher dispatch. **Read-only with zero or one argument.**

```bash
claude-playbook env                                    # list every playbook that declares overrides
claude-playbook env router                             # show this playbook's overrides
claude-playbook env router set A=1 B=2                 # record values (replacing previous ones)
claude-playbook env router unset CLAUDE_CODE_OAUTH_TOKEN
claude-playbook env router clear A                     # forget the entry; the shell's value applies again
claude-playbook env router use glm no-oauth            # attach env profiles (must exist)
claude-playbook env router unuse no-oauth              # detach
```

**Output** (show). When profiles are attached, the flattened result follows the declared block:

```
Environment overrides for "router":
  profiles  glm
  set    MODEL=own
Effective at launch:
  set    ANTHROPIC_BASE_URL=http://proxy:1/v1
  set    MODEL=own
  unset  CLAUDE_CODE_OAUTH_TOKEN
```

**Layering.** Later layers win:

```text
process environment
  + the registry default profile, when one is set (`env-profile <name> default`)
  + each profile in env.profiles, in list order
  + the block's own env.set
  - the block's own env.unset
  + one-off launch flags, in command-line order
  + CLAUDE_CONFIG_DIR (bound by the tool; reserved)
  = the child claude process's environment
```

The registry default applies to every launch of every playbook, manifest or not, and to `start`. It is recorded in `<playbooks root>/.env-profiles/.default` (mode `0600`, the profile's name). A default that names a missing or invalid profile refuses the launch like any other profile.

**Semantics at launch.** The block is first flattened: each profile in `env.profiles`, in order, then the block's own `set`/`unset` on top, where a later `set` cancels an earlier `unset` of the same key and vice versa. `PrepareLaunchEnv` then builds the child's environment as: the process environment; the authentication branch (isolation, long-lived token, or stored credentials); flattened `set` entries overriding any inherited value; flattened `unset` entries removed; finally `CLAUDE_CONFIG_DIR` bound to the playbook. A profile named by the manifest that does not exist under `<playbooks root>/.env-profiles/` refuses the launch: `env profile "x" not found in <dir> (create it with: claude-playbook env-profile x set KEY=VALUE)`; one that exists but cannot be read or parsed refuses it too: `env profile "x": invalid env profile at <path>: <reason>`. Neither is downgraded to the advisory warning other preparation failures get, and the refusal happens before any credential sync or quarantine touches the config directory. `start` resolves profiles from the root named by its `--playbooks-dir` (or `CLAUDE_PLAYBOOKS_DIR`), the same root `run` uses. Whether the long-lived token is active is decided **with the block applied**: `unset` of `CLAUDE_CODE_OAUTH_TOKEN` means inactive (the stored-credentials path runs, the playbook's own grant is not quarantined, an inherited token is stripped); `set` of it supplies a per-playbook token that replaces the machine-global file's. The manifest governing a config directory is the nearest one walking up from it, so a manifest `subdir` layout is covered by the install root's block.

**Mutations** parse and validate every argument before taking the registry lock and rewriting the manifest (bootstrapping one for a flat playbook). `set` removes the key from `unset`; `unset` removes it from `set`; `clear` removes it from both; `use` appends profile names (moving an already-listed one to the end) after checking each exists; `unuse` removes them. An emptied block is dropped from the file. A manifest that cannot be parsed is reported as an advisory launch warning and treated as declaring nothing.

**Install-local.** `update` carries the live block forward and ignores the source's, assembling the final manifest in the staged tree *before* the overlay so a source-shipped block is never live, even transiently; `install` drops a source-shipped block with a note, assembling the install in a dot-prefixed staging directory (invisible to discovery) and renaming it into the registry only once its manifest is sanitized. A local source directory is always staged into a private copy first (in the system temp dir, or the user cache dir when that lies inside the source), so neither command ever writes into the pilot's source. A published manifest must not be able to redirect an install's API endpoint or strip its authentication.

Linked playbooks: the manifest is the LINK TARGET's shared state, so mutations are refused — edit the target's manifest directly if you really mean it.

**Errors:**
- Playbook not found → `unknown playbook "router"`
- `set` without `=` → `set expects KEY=VALUE, got "X"`
- Invalid variable name → `invalid environment variable name "bad-name"`
- `CLAUDE_CONFIG_DIR` → `CLAUDE_CONFIG_DIR is managed by claude-playbook and cannot be overridden`
- Unknown action → `unknown action "frob": expected set, unset, clear, use, or unuse`
- `use` of a profile that does not exist → `unknown env profile "x". Create it with 'claude-playbook env-profile x set KEY=VALUE'`
- Linked playbook → refused (shared manifest)

---

### `claude-playbook env-profile [name] [set KEY=VALUE... | unset KEY... | clear KEY... | describe TEXT | delete]`

Shows or manages **env profiles**: named, reusable `set`/`unset` blocks stored as `<playbooks root>/.env-profiles/<name>.toml` (mode `0600`; values may be secrets) and attached to playbooks with `env <playbook> use <name>`. **Read-only with zero or one argument.**

```bash
claude-playbook env-profile                                # list profiles, with descriptions and users
claude-playbook env-profile glm                            # show one, and which playbooks use it
claude-playbook env-profile glm set ANTHROPIC_BASE_URL=http://proxy:1/v1   # creates the profile on first use
claude-playbook env-profile glm unset CLAUDE_CODE_OAUTH_TOKEN
claude-playbook env-profile glm clear ANTHROPIC_BASE_URL
claude-playbook env-profile glm describe "GLM 5.3 through the local router"
claude-playbook env-profile glm default                    # registry default: under every playbook's own block
claude-playbook env-profile glm undefault                  # clear it (only if glm is the default)
claude-playbook env-profile glm delete                     # refused while any playbook uses it, or while it is the default
```

**File format** — the manifest `[env]` shape hoisted to top level:

```toml
description = "GLM 5.3 through the local router"
unset = ["CLAUDE_CODE_OAUTH_TOKEN"]

[set]
ANTHROPIC_BASE_URL = "http://proxy:1/v1"
```

Profile files are written mode `0600` and an existing file is tightened to it on every write. Profile names match `[A-Za-z0-9][A-Za-z0-9._-]*`. Keys follow the `[env]` rules (valid variable names, `CLAUDE_CONFIG_DIR` reserved, no key in both lists). Only `set` creates a profile; every other action on an unknown name is an error. Mutations validate every argument before taking the registry lock. `delete` scans the registry and refuses while a playbook references the profile, naming the users. The directory is never touched by `install` or `update`.

The listing marks the default with `registry default`; `env <playbook>` shows a `default   <name>` line and includes it in "Effective at launch"; a playbook without a block is told the default applies to it.

**Errors:**
- Unknown profile → `unknown env profile "glm". Create it with 'claude-playbook env-profile glm set KEY=VALUE'`
- Delete while attached → `env profile "glm" is used by router, sre; detach it first with 'claude-playbook env <playbook> unuse glm'`
- Delete while default → `env profile "glm" is the registry default; clear it first with 'claude-playbook env-profile glm undefault'`
- `undefault` of a profile that is not the default → `the registry default is "x", not "glm"` or `no registry default is set`
- Invalid file on disk → `invalid env profile at <path>: TOML syntax error at line <n> (content not shown)` (listing and launch both fail loudly rather than skip it; the parser's message, which quotes file content, is never echoed)

---

### `claude-playbook auth status [name...]`

Read-only view of how each playbook authenticates and what its stored login looks like. Nothing is written, no credential value is read into the output, no network call is made, no process is spawned unless `--claude` is given.

```bash
claude-playbook auth status                 # every playbook, ~/.claude first
claude-playbook auth status sre router      # named ones
claude-playbook auth status --json
claude-playbook auth status --claude        # add 'claude auth status --json' per directory
```

**Columns:**

| Column | Meaning |
|--------|---------|
| `MODE` | How a launch would authenticate, decided exactly as `run` decides it: `token` (machine-global long-lived token injected), `own-token` (a token the manifest or a profile sets), `own-login` (token unset for this playbook; stored login), `shared-login` (no token anywhere; stored login), `isolated` (`isolate_auth`), `error` (the launch would be refused, reason in `NOTE`). An isolated playbook whose manifest or profile sets a non-empty token is `own-token (isolated)`, matching the launch, which injects that token and quarantines the stored grant. For `~/.claude`, which claude-playbook does not launch, only an exported `CLAUDE_CODE_OAUTH_TOKEN` counts as `token`. |
| `STORE` | What sits at `.credentials.json`: `symlink -> <target>`, `file`, `file (no grant)`, or `absent`. |
| `EXPIRES` | The stored grant's `expiresAt` as `in 6h12m`, `expired`, `unknown`, or `-` when there is no grant. |
| `DAEMON` | Claude Code's `daemon-auth-status.json`: `auth_required` when its `since` is at or after the current grant's refresh instant (`expiresAt` minus 4 minutes, the daemon's proactive-refresh lead) and the row is a stored-login mode, `<status> (stale)` otherwise (the file is never cleared on recovery, and under a token mode the stored login is quarantined and unused), `-` when absent. |
| `NOTE` | `launch refused` (with a sanitized reason: no file content is ever echoed), `re-auth required`, `no login`, `grant expired (refreshes at launch if the refresh token is still valid)`, or empty. Token modes have no stored login to judge and show nothing. An explicitly empty token set by the manifest or a profile is `own-login`, matching the launch decision. |
| `CLAUDE` | With `--claude`: `logged in, <subscription>`, `not logged in`, or `error: <reason>`. |

When a long-lived token file exists, a trailing line names it and notes that its own expiry is not recorded anywhere.

`--json` emits one object per row with the raw fields (`name`, `dir`, `mode`, `mode_error`, `isolated`, `store`, `store_target`, `has_grant`, `expires_at`, `expired`, `daemon_status`, `daemon_since`, `reauth_required`, `token_file`, and `claude` when requested). `reauth_required` is only ever true for a stored-login mode with a grant present whose `expiresAt` is known and a marker whose `since` is known; a marker that cannot be ordered against the grant is reported as stale.

**Errors:**
- Named playbook not found → `unknown playbook "x". Run 'claude-playbook list' to see available playbooks`

---

### `claude-playbook dealias <name>`

Removes the playbook's alias and its launcher. Exactly equivalent to `claude-playbook alias <name> --remove`, provided as a standalone verb for convenience.

```bash
claude-playbook dealias sre
```

The playbook directory itself is untouched.

**Errors:**
- Playbook not found → `unknown playbook "sre". Run 'claude-playbook list' to see available playbooks`

---

### `claude-playbook delete <name>`

Deletes a playbook. (Aliases: `uninstall`, `unlink`.)

```bash
claude-playbook delete experiment        # prompts
claude-playbook delete sre -y            # skip the prompt
```

**Confirmation prompt:**
```
Playbook: sre
Location: ~/.claude-playbooks/sre
Alias:    sre (will be removed from ~/.zshrc)
Command:  sre (launcher kept; removal hint printed after delete)
Contents: 12 files, 3 directories

Permanently delete? [y/N]
```

The `Alias` line shows the manifest alias (`(none)` when unset); a `Command` line appears for each launcher matching the playbook's name or manifest alias.

**Deletion scope:**
- The target directory (for a symlink, the link is removed; the symlink target is preserved).
- Launcher symlinks are **kept**, claim-aware: a command name that still resolves to another playbook keeps its launcher outright (`Kept command "sre" (still addresses playbook "other")`); an unclaimed name's launcher is also kept — a stateless symlink may be serving a playbook in another registry root — with a manual removal hint: `Kept command "sre" — launchers may serve other registry roots; remove it manually if unused:` followed by `rm <path>`. A kept launcher whose name no longer resolves fails loudly as stale when invoked, so retention is noisy, never silently wrong.

**Flags:**

| Flag | Description |
|------|-------------|
| `-y`, `--yes` | Skip the confirmation prompt |

**Errors:**
- Name not found → `"experiment" not found under ~/.claude-playbooks`

**Graceful cases:** if the directory is already gone, the command still cleans up any dangling aliases and reports success.

---

### `claude-playbook update [name]`

Updates either the `claude-playbook` tool itself, or a specific playbook — based on whether a name is given.

#### `claude-playbook update` (no arguments) — self-update

Updates the running `claude-playbook` binary in place to the latest GitHub release.

```bash
claude-playbook update            # download + install the latest release
claude-playbook update --check    # report the latest version without installing
claude-playbook update --force    # reinstall even if already on the latest
```

It resolves the latest release tag from the GitHub API, downloads the asset for
the running OS/architecture (`claude-playbook-<goos>-<goarch>`), verifies it by
running `--version` against the downloaded file, and then atomically replaces
the current executable (it stages a temp file in the executable's own directory
and `rename`s it into place, so the swap is atomic and never a partial write).
Symlinks are resolved first, so invoking through the `cpb` symlink updates the
real binary and leaves the link intact.

```
Current version: v1.2.0
Latest version:  v1.3.0
Downloading claude-playbook-darwin-arm64 v1.3.0 (darwin/arm64)...
Updated to v1.3.0 at /Users/you/.local/bin/claude-playbook.
```

If already on the latest version, it prints `Already up to date.` and exits
(pass `--force` to reinstall anyway). If the install directory is not writable
(e.g. a root-owned `/usr/local/bin`), it reports that elevated privileges are
needed. `GITHUB_TOKEN`, when set, is used for the GitHub API request to avoid
rate limits.

#### `claude-playbook update <name>` — update a playbook

Updates the playbook from the `[source]` metadata recorded in its `.playbook`. The CLI owns the update end to end; there is no delegated update script.

```bash
claude-playbook update sre
claude-playbook update sre --check
```

**Behaviour:**
1. Resolve the named playbook and require `[source].repository` metadata.
2. Refuse linked playbooks and installs whose config is selected through a top-level `subdir`.
3. Validate `[update].preserve` before touching anything, so an escaping path fails before the overlay starts.
4. Fetch `[source].repository` at the recorded branch and source subdirectory into a staging directory.
5. With `--check`, report the installed version (`version` from the live `.playbook`) against the available one (`version` from the staged `.playbook`, falling back to the staged `VERSION` file), and stop.
6. Take the registry lock and re-read the live manifest. If the install directory is no longer the same filesystem object, or any `[source]` field changed while staging ran, activate nothing.
7. Move every top-level entry the staged source also provides into a timestamped `.<name>.bak.<stamp>` beside the install, then copy the staged source over the install **in place**. Entries the source does not ship — `data/`, `projects/`, `sessions/`, `history.jsonl` and the like — are never read, moved, or copied, so concurrent writes to them cannot be lost. The live root directory keeps its own mode; only entries below it take the source's. A failure at any point rolls back: entries the source **introduced** are removed and moved entries are restored from the backup; if any restoration fails the backup is kept and named in the error rather than deleted.
8. Restore the preserved files over the incoming copies: `settings.json`, `settings.local.json`, `.credentials.json`, `.claude.json`, plus every path in `[update].preserve`. A preserved file the source ships but the install did not have is removed rather than adopted, whether it arrived under a moved entry or a newly introduced one. Preserved paths under an entry the overlay never touched are left alone. Whether a preserved path's top-level entry was touched is decided by filesystem identity, not spelling, so on a case-insensitive filesystem a source-shipped `SETTINGS.JSON` is the preserved `settings.json`. Before anything is removed or written, every ancestor between a nested preserved path and its top-level entry is checked for **physical** containment inside that entry (symlinks evaluated, nearest existing ancestor; the final component itself is never followed, so a preserved entry that is a symlink such as the shared `.credentials.json` is recreated as a link). A source that turned a directory on the path into a symlink — pointing outside the install, or into a sibling entry the overlay never backed up — makes the update fail and roll back: `<rel> resolves outside <top>/ after the update (a symlinked ancestor); refusing to restore through it`.
9. Assemble the manifest that goes live in the staged tree before the overlay: local alias, authentication-isolation, `[env]` overrides, and source metadata are preserved from the live manifest (a source-shipped `[env]` block is never adopted, not even transiently), and the install's `name` is always reset to its directory name.
10. If `migrations/apply.sh` exists in the updated install and is executable, run it as `migrations/apply.sh <from-version> <to-version> <install-dir>` with working directory the install and `CLAUDE_CONFIG_DIR`, `CLAUDE_PLAYBOOK_TARGET`, `CLAUDE_PLAYBOOK_PATH` in the environment. Runners are expected to be idempotent; the CLI does not track which migrations have run. Migrations are skipped with a warning when either side has no `version`.

**Errors:**
- Target not found → `unknown playbook "sre". Run 'claude-playbook list' to see available playbooks`
- Source metadata missing → `"sre" has no [source] metadata in .playbook; nothing to update from`
- Linked install → `"sre" is linked; native update is disabled to avoid replacing its external source`
- Subdir-selected install → `"sre" uses manifest subdir "...": native update requires a flat playbook`
- Extra arguments → `unexpected argument "..."; `update <name>` accepts only --check`
- Install changed while staging → `playbook "sre" changed while the update was staging (deleted, re-created, or re-sourced); nothing activated -- re-run update`
- Migration runner exits non-zero → `"sre" is at code version <v> but migrations failed: <err>`

---

### `claude-playbook self-uninstall`

Removes `claude-playbook` and everything it created: all playbooks, their launcher commands, any `source <(... completion ...)` lines in shell rc files, the playbooks root directory, and the binary itself. The complete undo for an install.

```bash
claude-playbook self-uninstall               # prompts
claude-playbook self-uninstall -y            # skip the prompt
claude-playbook self-uninstall --dry-run     # show what would be removed
claude-playbook self-uninstall --keep-data   # remove the binary but keep playbooks
claude-playbook self-uninstall --binary-only # remove binary/launchers/completions, keep playbooks (what uninstall.sh runs)
```

**Steps:**
1. For each discovered playbook (unless `--keep-data` or `--binary-only`): remove its directory.
2. Unless `--keep-data` or `--binary-only`, remove the playbooks root directory.
3. Unless `--keep-binary`, sweep **all** launcher symlinks pointing at the binary — every one of them would dangle once the binary is gone, whichever registry root it served. The sweep unions two sources: a resolution scan of the standard launcher directories (the resolved launcher dir plus the `~/.local/bin` fallback), and the launcher receipt file (`~/.local/state/claude-playbook/launchers`) in which every launcher the tool creates is recorded — covering custom `--launcher-dir` locations the scan cannot know about. Every candidate is verified to still be a symlink resolving to this binary (or dangling); a link the user renamed or repointed resolves elsewhere and is left alone. The reserved names (`claude-playbook`, `cpb`) are owned by the binary-removal step. Once the launchers are gone, the receipt is removed too. With `--keep-binary`, no launchers are touched: a same-named command may be serving another registry root, and a removed default-root playbook's launcher fails loudly as stale rather than being silently deleted.
4. Unless `--keep-binary`, remove any `source <(claude-playbook|cpb completion bash|zsh)` lines from `~/.bashrc` and `~/.zshrc` — after the binary is gone they would error on every new shell.
5. Unless `--keep-binary`, remove the running binary and its sibling `cpb`/`claude-playbook` link. If removal is denied by permissions, print the `sudo rm <path>` command to run manually rather than failing.
6. Print a summary of what was removed. When completion lines were removed from rc files, remind the user that already-open shells still hold the stale completion functions until reloaded. In `--binary-only` mode, state explicitly that the playbooks directory was not touched.

**Flags:**

| Flag | Description |
|------|-------------|
| `-y`, `--yes` | Skip the confirmation prompt |
| `--keep-data` | Preserve the playbooks directory and its playbooks |
| `--keep-binary` | Leave the binary in place |
| `--binary-only` | Remove only the binary, its `cpb` sibling, launchers, and completion lines — playbooks stay untouched (the mode `uninstall.sh` delegates to) |
| `--dry-run` | Print what would be removed without changing anything |

`--dry-run` never prompts and never modifies anything. Without `--dry-run` or `-y`, the command prints what will be removed and asks for confirmation.

---

### `claude-playbook completion [bash|zsh|fish|powershell]`

Generates a shell completion script. Auto-generated by cobra and includes completion for subcommands, flags, and playbook names.

```bash
# zsh
claude-playbook completion zsh > "${fpath[1]}/_claude-playbook"

# bash
claude-playbook completion bash > /etc/bash_completion.d/claude-playbook

# fish
claude-playbook completion fish > ~/.config/fish/completions/claude-playbook.fish
```

Playbook name completion is wired for commands that take a name: `run`, `delete`, `info`, `rename`, `alias`, `env`, and `update`.

---

## Playbook Manifest

The `.playbook` manifest is **optional** and holds **metadata only**. It never affects discovery, and it never declares other playbooks. A directory without a `.playbook` is a perfectly valid playbook.

**Format:**

```toml
version = "1.0.0"
name = "sre"
alias = "sre"
description = "Site Reliability Engineering assistant"
homepage = "https://github.com/ramazanpolat/awesome-playbooks"
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
| `version` | Version of the playbook itself (free-form semver string). Shown by `info`. Not enforced by the tool. |
| `name` | Preferred playbook name. `install` uses it as a suggestion; the actual name is always the install directory name. |
| `alias` | Preferred alias for `install`/`create` to suggest when writing the default alias. |
| `subdir` | Optional. Used for backward compatibility. Points at a subdirectory of the install that holds the Claude config. New installations will automatically extract the subdir flatly into the target directory and clear this field in the manifest to ensure all playbooks remain flat at the root level. |
| `description` | Human-readable description, shown by `info`. |
| `homepage` | Optional URL, shown by `info`. |
| `author` | Optional author name or contact, shown by `info`. |
| `isolate_auth` | When true, detach shared credentials and do not copy global credentials or account metadata into this playbook. The machine-global long-lived token and plan descriptors never reach it; a `CLAUDE_CODE_OAUTH_TOKEN` this playbook's own `env.set` (or an attached profile) supplies is honoured as its own token, with its stored grant quarantined as on the shared token path. The manifest governing a config directory is the nearest valid one walking up from it; an unreadable manifest on the way is reported but does not switch isolation off. |
| `env.profiles` | Optional list of env profile names (files under `<playbooks root>/.env-profiles/<name>.toml`) layered under `env.set`/`env.unset` at launch, in list order. A profile that is missing, unreadable, or invalid refuses the launch. Install-local. |
| `env.set` | Optional table of environment variables applied to the child `claude` process on every launch, overriding inherited values. Install-local: never adopted from a source. A manifest carrying any `env.set` value is written owner-only (its existing mode masked to `0600`; values may be tokens); otherwise an existing file keeps its mode exactly, and a new one is `0644`. A rewrite never loosens a file. |
| `env.unset` | Optional list of environment variable names removed from the child's environment on every launch, even when the shell exports them. Install-local. Unsetting `CLAUDE_CODE_OAUTH_TOKEN` makes the long-lived token inactive for this playbook (stored-credentials path: no injection, no quarantine). |
| `source.repository` | Git URL or local source used by native update. Git installs populate this automatically. |
| `source.branch` | Optional Git branch or tag used by native update. |
| `source.subdir` | Optional source-relative directory selected during native update. Must remain physically below the fetched source, including through symlinks. |
| `update.preserve` | Optional list of install-local paths that survive an update even when the source ships its own copy. Each must be relative to and physically below the playbook root. `settings.json`, `settings.local.json`, `.credentials.json` and `.claude.json` are always preserved and need not be listed. |

**Forward compatibility:** unknown fields are ignored. Manifest authors may include fields for future tool versions without breaking older installs.

**Errors:**
- Invalid TOML → `invalid .playbook at <path>: TOML syntax error at line <n> (content not shown)`. The parser's own message is never echoed: since v3.5.0 a manifest may hold credential values under `[env.set]`, and this error reaches the terminal from every command that discovers playbooks.
- `subdir` escapes the install directory (e.g. `../foo`) → `invalid .playbook at <path>: 'subdir' must be relative and stay inside the directory`
- `subdir` does not exist → `~/.claude-playbooks/<name>/.playbook declares subdir "<path>" but the directory is missing`
- `source.subdir` or any `update.preserve` entry escapes its root → `invalid .playbook at <path>: <field> must be a relative path below the playbook root`
- An `env` key is not a valid variable name → `invalid .playbook at <path>: env.set: invalid environment variable name "<key>"`
- An `env` key is `CLAUDE_CONFIG_DIR` → `invalid .playbook at <path>: env.set: CLAUDE_CONFIG_DIR is managed by claude-playbook and cannot be overridden`
- A key appears in both `env.set` and `env.unset` → `invalid .playbook at <path>: env: <key> is both set and unset`
- An `env.set` value is not valid UTF-8 → `invalid .playbook at <path>: env.set: value of <key> is not valid UTF-8 and cannot be stored in a manifest`.
- An `env.set` value contains a NUL byte → `invalid .playbook at <path>: env.set: value of <key> contains a NUL byte, which cannot be passed in an environment` (refused on write and on read; a NUL in an environment fails every launch). Any other value round-trips: control characters are written as TOML `\uXXXX` escapes.
- An `env.profiles` entry is not a valid profile name → `invalid .playbook at <path>: env.profiles: invalid profile name "<name>": use letters, digits, dots, dashes, underscores`

---

## Aliases

A playbook's alias is one alternate command name, stored as the `alias` field of its `.playbook` manifest — no rc files, no separate registry. Dispatch resolves directory names first, then manifest aliases; the alias is materialized as a launcher command like the playbook's own name. `create --alias`, `install --alias`, `link --alias`, `rename --alias`, and `alias <name> <alias>` all write the same field.

---

## Global Flags

These flags work on every command.

| Flag | Description |
|------|-------------|
| `--playbooks-dir <path>` | Override the playbooks root directory. Default: `~/.claude-playbooks` |
| `--launcher-dir <path>` | Override the launcher directory. Default: the directory of the binary as invoked, falling back to `~/.local/bin` when unwritable |
| `--version` | Print the version of `claude-playbook` |
| `--help`, `-h` | Show help for the command or subcommand |

### Environment variables

| Variable | Flag equivalent |
|----------|----------------|
| `CLAUDE_PLAYBOOKS_DIR` | `--playbooks-dir` |
| `CLAUDE_LAUNCHER_DIR` | `--launcher-dir` |

**Resolution precedence:** CLI flag → environment variable → default.

---

## Exit Codes and Error Conventions

- Exit code `0` on success, non-zero on failure. Cobra's default is `1` for user errors.
- All errors go to stderr.
- Messages are plain English, one line, no stack traces. Always suggest the next action where possible.

**Examples:**
```
Error: "myrepo" already exists at ~/.claude-playbooks/myrepo. Use --name to choose a different name
Error: unknown playbook "typo". Run 'claude-playbook list' to see available playbooks
Error: 'claude' command not found. Install Claude Code first: https://claude.ai/download
Error: subdirectory "playbooks/sre" not found in source
Error: "sre" has no [source] metadata in .playbook; nothing to update from
Error: invalid .playbook at ~/.claude-playbooks/foo/.playbook: toml: line 3: expected '=', got ':'
```
