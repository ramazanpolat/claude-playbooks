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

### The filesystem is the source of truth

There is no index file, no database, no registry file. The state the tool reads from or writes to is: the playbooks root directory (including the optional `.playbook` manifests inside it, where a custom command alias is recorded), the launcher directory (symlinks to the binary that serve as per-playbook commands), a flock lock file in the user cache dir (`<cache>/claude-playbook/registry.lock`, used only to serialize concurrent mutations — it holds no data), and, for legacy alias-era installs, the user's shell configuration file. Any mutation users make with `mv`, `rm`, or a text editor is immediately consistent with what the tool sees on its next invocation.

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

Names are used wherever a playbook is referenced: `run`, `delete`, `info`, `rename`, `alias`, `update`.

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

Prints a one-line description and lists all discovered playbooks with how to run each. The parenthetical shows the playbook's registered command — launcher commands take display precedence over legacy rc aliases; a playbook with neither shows `(no command registered)`.

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

Column widths are computed from the longest NAME, PATH, and COMMAND values, with minimum widths of 4, 4, and 7. `COMMAND` shows the playbook's launcher command name (matching its directory name or manifest alias); launchers take display precedence over legacy rc aliases, which are shown only when no launcher exists. `-` means no command is registered. `LAST USED` is derived from the playbook directory's mtime.

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

### `claude-playbook run <name> [claude-flags...]`

Runs Claude Code using the named playbook. Any flags after the name are forwarded to `claude` unchanged.

```bash
claude-playbook run experiment
claude-playbook run sre
claude-playbook run sre --model claude-opus-4-6
```

Equivalent to:
```bash
CLAUDE_CONFIG_DIR=~/.claude-playbooks/<name> claude [claude-flags...]
```

Flag parsing is disabled so arbitrary `claude` flags pass through. The global `--playbooks-dir` and `--shell-config` flags are extracted from the argument list before forwarding.

**Errors:**
- Playbook not found → `unknown playbook "experiment". Run 'claude-playbook list' to see available playbooks`
- `claude` not on PATH → `'claude' command not found. Install Claude Code first: https://claude.ai/download`

---

### `claude-playbook start <path> [claude-flags...]`

Starts an ad-hoc Claude Code session at any directory. Creates the directory if it doesn't exist. No playbook registration, no `.playbook` file, no discovery — just set `CLAUDE_CONFIG_DIR` and run. The throwaway-experiment command.

```bash
claude-playbook start /tmp/scratch
claude-playbook start /tmp/scratch --model claude-opus-4-6
claude-playbook start /tmp/scratch --delete
```

Equivalent to:
```bash
CLAUDE_CONFIG_DIR=/tmp/scratch claude [claude-flags...]
```

**Flags:**

| Flag | Description |
|------|-------------|
| `--delete` | Delete the directory when the session ends |

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

No shell reload is needed — the launcher is a symlink in a PATH directory, live the moment it is written. If the launcher directory is not on PATH, or another executable or alias-era rc alias shadows the command, a warning explains the fix.

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
Updater:     bin/update-playbook.sh
```

**Fields:**

| Field | Meaning |
|-------|---------|
| `Name` | Playbook name (its directory name under the playbooks root) |
| `Version` | `version` field from the `.playbook` manifest, if set |
| `Path` | Absolute path to the directory |
| `Type` | `directory`, `symlink → <target>`, or `symlink → <target> (BROKEN)` |
| `Alias` | Shell alias for this playbook, or `(none)` |
| `Size` | File and directory counts |
| `Last used` | Human-readable time since the directory was last modified |
| `Description` | From `.playbook` manifest, if present |
| `Homepage` | From `.playbook` manifest, if present |
| `Author` | From `.playbook` manifest, if present |
| `Updater` | `bin/update-playbook.sh` if it exists and is executable, else `(none)` |

**Errors:**
- Target not found → `unknown playbook "experiment"`

---

### `claude-playbook rename <old-name> <new-name>`

Renames a playbook directory and updates affected aliases.

```bash
claude-playbook rename experiment exp-1
claude-playbook rename sre site-reliability
```

**Steps:**
1. Validate the old name exists and the new name doesn't. Both must be single-segment names.
2. Rename the directory with `mv`.
3. Update all legacy shell aliases whose `CLAUDE_CONFIG_DIR=<path>` points at the old location — rewrite them to the new location.

**Flags:**

| Flag | Description |
|------|-------------|
| `--alias <alias>` | Use a custom alias name for the renamed playbook |
| `--no-alias` | Drop the alias if one existed |

`--alias` and `--no-alias` cannot be combined.

**Errors:**
- Old name not found → `unknown playbook "experiment"`
- New name already exists → `"exp-1" already exists at ~/.claude-playbooks/exp-1`
- Either name contains a slash → `playbook name cannot contain '/'`
- Both `--alias` and `--no-alias` → `--no-alias and --alias cannot be used together`

---

### `claude-playbook alias [name] [new-alias]` (legacy)

Lists or manages **legacy rc-file shell aliases** — the alias-era registration mechanism kept working for installs that predate launcher commands (v2.13.0). New registrations use launchers; this command never touches them. **Read-only when given one argument** — no hidden side effects.

```bash
claude-playbook alias                    # list all playbooks and their aliases
claude-playbook alias sre                # show alias for this playbook, or say "none"
claude-playbook alias sre s              # set alias to 's' (creates or replaces)
claude-playbook alias sre --remove       # remove alias
```

**No arguments** — lists all playbooks with the full alias lines from the shell config:

```
experiment    alias experiment='CLAUDE_CONFIG_DIR="/Users/you/.claude-playbooks/experiment" claude-playbook run experiment'
sre           alias sre='CLAUDE_CONFIG_DIR="/Users/you/.claude-playbooks/sre" claude-playbook run sre'
dba           (no alias)
```

Showing the full alias line lets users see exactly what will run, including any flags they've added manually.

**One argument, alias exists** — prints it.
```
Alias for "sre": alias sre='CLAUDE_CONFIG_DIR="/Users/you/.claude-playbooks/sre" claude-playbook run sre'
```

**One argument, no alias** — reports only; does **not** create one.
```
Playbook "dba" has no alias set.
Use 'claude-playbook alias dba <alias-name>' to create one.
```

**Two arguments** — sets the alias (creates or replaces).

**With `--remove`** — removes any aliases pointing at this playbook. No-op with a message if none exist.

**Flags:**

| Flag | Description |
|------|-------------|
| `--remove` | Remove the alias(es) for the named playbook |

**Errors:**
- Playbook not found → `unknown playbook "sre"`
- Shell config cannot be found or written → see Alias Management

---

### `claude-playbook dealias <name>` (legacy)

Removes every **legacy rc-file shell alias** pointing at the named playbook. This is exactly equivalent to `claude-playbook alias <name> --remove`, provided as a standalone verb for convenience. Launcher commands are untouched.

```bash
claude-playbook dealias sre
```

**Behaviour:**
1. Resolve the named playbook (error if unknown).
2. If it has no alias, report that and exit successfully — nothing to do.
3. Otherwise remove all alias lines whose `CLAUDE_CONFIG_DIR` points at the playbook's directory, and report how many were removed.

The playbook directory itself is untouched; only legacy shell aliases are removed.

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

The `Alias` line covers legacy rc aliases (`(no alias)` when none); a `Command` line appears for each launcher matching the playbook's name or manifest alias.

**Deletion scope:**
- The target directory (for a symlink, the link is removed; the symlink target is preserved).
- All legacy shell aliases pointing into the deleted directory.
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

Uses a delegated update script when one exists. Git installs also carry source metadata, allowing safe native updates when no script is present.

```bash
claude-playbook update sre
```

**Delegated-script behaviour:**
1. Resolve the named playbook.
2. Resolve the script from `source.update_script`, defaulting to `bin/update-playbook.sh`, and require it to remain below the playbook root and be executable.
3. Run the script with:
   - Working directory: the playbook directory
   - Environment: inherited, with `CLAUDE_CONFIG_DIR=<path>`, `CLAUDE_PLAYBOOK_TARGET=<name>`, and `CLAUDE_PLAYBOOK_PATH=<path>`
   - Arguments: any remaining command-line arguments are forwarded to the script
4. Forward stdout, stderr, and exit code.

**Native-update behaviour:**
1. If no delegated script exists, require `[source].repository` metadata.
2. Refuse linked playbooks and legacy installs whose config is selected through top-level `subdir`; those require a delegated script.
3. Fetch `[source].repository` at the recorded branch and source subdirectory.
4. Copy the current playbook into a same-filesystem staging directory, then overlay the fetched source. This preserves credentials, account state, history, and other local-only files while replacing source-owned files.
5. Preserve local alias, authentication-isolation, and source metadata.
6. Rename the old directory to a timestamped backup and atomically activate the staged directory. Restore the old directory if activation fails.

**Example `bin/update-playbook.sh` for a git-backed install:**
```bash
#!/bin/sh
set -e
cd "$(dirname "$0")/.."
git pull --ff-only
```

**Errors:**
- Target not found → `unknown playbook "sre". Run 'claude-playbook list' to see available playbooks`
- Update script and source metadata missing → `"sre" has no update script (...) and no [source] metadata in .playbook`
- Script not executable → `update script is not executable: <path>`
- Script exits non-zero → exit code forwarded; `update script exited with code <n>` is printed to stderr
- Linked or legacy-subdir native update → an error requiring a delegated script

---

### `claude-playbook self-uninstall`

Removes `claude-playbook` and everything it created: all playbooks, their launcher commands and legacy shell aliases, the completion lines `install.sh` added to shell rc files, the playbooks root directory, and the binary itself. The complete undo for an install.

```bash
claude-playbook self-uninstall               # prompts
claude-playbook self-uninstall -y            # skip the prompt
claude-playbook self-uninstall --dry-run     # show what would be removed
claude-playbook self-uninstall --keep-data   # remove the binary but keep playbooks
```

**Steps:**
1. For each discovered playbook: remove its legacy shell aliases, and (unless `--keep-data`) remove its directory.
2. Unless `--keep-data`, remove the playbooks root directory.
3. Unless `--keep-binary`, sweep **all** launcher symlinks pointing at the binary — every one of them would dangle once the binary is gone, whichever registry root it served. With `--keep-binary`, no launchers are touched: a same-named command may be serving another registry root, and a removed default-root playbook's launcher fails loudly as stale rather than being silently deleted.
4. Remove the `source <(claude-playbook completion bash|zsh)` lines `install.sh` appended to `~/.bashrc` and `~/.zshrc` — after the binary is gone they would error on every new shell.
5. Sweep any leftover aliases whose `CLAUDE_CONFIG_DIR` points anywhere inside the playbooks root.
6. Unless `--keep-binary`, remove the running binary and its sibling `cpb`/`claude-playbook` link. If removal is denied by permissions, print the `sudo rm <path>` command to run manually rather than failing.
7. Print a summary of what was removed and remind the user to reload their shell.

**Flags:**

| Flag | Description |
|------|-------------|
| `-y`, `--yes` | Skip the confirmation prompt |
| `--keep-data` | Preserve the playbooks directory and its playbooks |
| `--keep-binary` | Leave the binary in place |
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

Playbook name completion is wired for commands that take a name: `run`, `delete`, `info`, `rename`, `alias`, and `update`.

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
update_script = "bin/update-playbook.sh"
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
| `isolate_auth` | When true, detach shared credentials and do not copy global credentials or account metadata into this playbook. |
| `source.repository` | Git URL or local source used by native update. Git installs populate this automatically. |
| `source.branch` | Optional Git branch or tag used by native update. |
| `source.subdir` | Optional source-relative directory selected during native update. Must remain physically below the fetched source, including through symlinks. |
| `source.update_script` | Optional delegated update script path, relative to and physically below the playbook config root. |

**Forward compatibility:** unknown fields are ignored. Manifest authors may include fields for future tool versions without breaking older installs.

**Errors:**
- Invalid TOML → `invalid .playbook at <path>: <reason>`
- `subdir` escapes the install directory (e.g. `../foo`) → `invalid .playbook at <path>: 'subdir' must be relative and stay inside the directory`
- `subdir` does not exist → `~/.claude-playbooks/<name>/.playbook declares subdir "<path>" but the directory is missing`
- `source.subdir` or `source.update_script` escapes its root → `invalid .playbook at <path>: <field> must be a relative path below the playbook root`

---

## Alias Management (legacy)

**This section documents the alias-era registration mechanism, retained so installs that predate launcher commands (v2.13.0) keep working.** New registrations write launchers (see Launcher Commands); the `alias`/`dealias` commands and the machinery below manage only the old rc-file alias lines.

Aliases are plain single-line `alias` definitions in the user's shell configuration file — no metadata, no registry. The alias lines in the config are the complete truth: discovery works purely by content match (the alias name, or the `CLAUDE_CONFIG_DIR` path inside the definition).

### Shell config detection

The file to read and write is chosen in this order:

1. The `--shell-config <path>` flag, if given.
2. The `CLAUDE_SHELL_CONFIG` environment variable, if set.
3. Auto-detection from `$SHELL`:
   - `zsh` → `~/.zshrc`
   - `bash` → `~/.bashrc`
   - `fish` → `~/.config/fish/config.fish`
4. If undetectable, commands that need the shell config fail with:
   ```
   Could not find shell config. Use --shell-config <path> to specify one.
   ```

### Alias format

```bash
alias <alias-name>='CLAUDE_CONFIG_DIR="<playbook-path>" <bin-name> run <playbook-name>'
```

Aliases route their execution back through `claude-playbook run` (or its active command name link like `cpb run`) to allow credentials sharing, keychains, and symlink healing to execute transparently on every session startup.

### Lookup

Two lookup directions, both by plain grep:

- **By alias name** (for duplicate checks, removals): match lines where the alias definition is `alias <name>=...` (tolerating leading whitespace).
- **By playbook** (for `list`, `info`, and `alias` with no args): match lines containing `CLAUDE_CONFIG_DIR=<path>` where `<path>` (with `~` and `$HOME` expanded) resolves to the playbook's directory.

Because lookup works on the actual `alias` line content, hand-maintained aliases are fully supported. If a user writes:

```bash
alias myexp="CLAUDE_CONFIG_DIR=$HOME/.claude-playbooks/experiment cpb run experiment --model claude-opus-4-6"
```

— the tool sees it, reports it in `list` and `info`, and `alias experiment --remove` will delete it.

### Updates and removals

- **Set/update:** remove any existing line matching the alias name or any existing line pointing at the playbook path, then append the new alias line.
- **Remove:** delete any line matching the target (by alias name or by playbook path).

If multiple aliases point to the same playbook, they are all reported; removal deletes all of them.

### Manual customization

Because aliases are just shell commands, users can hand-edit them freely to add `claude` flags:

```bash
alias experiment='CLAUDE_CONFIG_DIR=~/.claude-playbooks/experiment claude'
alias sre='CLAUDE_CONFIG_DIR=~/.claude-playbooks/sre claude --permission-mode auto --effort max'
```

`alias` (no args) shows the full line so users can see exactly what's configured.

---

## Global Flags

These flags work on every command.

| Flag | Description |
|------|-------------|
| `--playbooks-dir <path>` | Override the playbooks root directory. Default: `~/.claude-playbooks` |
| `--launcher-dir <path>` | Override the launcher directory. Default: the directory of the binary as invoked, falling back to `~/.local/bin` when unwritable |
| `--shell-config <path>` | Override the shell config file (only affects the legacy alias features). Default: auto-detected from `$SHELL` |
| `--version` | Print the version of `claude-playbook` |
| `--help`, `-h` | Show help for the command or subcommand |

### Environment variables

| Variable | Flag equivalent |
|----------|----------------|
| `CLAUDE_PLAYBOOKS_DIR` | `--playbooks-dir` |
| `CLAUDE_LAUNCHER_DIR` | `--launcher-dir` |
| `CLAUDE_SHELL_CONFIG` | `--shell-config` |

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
Error: "sre" has no update script at bin/update-playbook.sh. This install does not support updates; see its documentation.
Error: invalid .playbook at ~/.claude-playbooks/foo/.playbook: toml: line 3: expected '=', got ':'
```
