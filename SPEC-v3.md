# claude-playbook CLI Specification (v3)

## Overview

`claude-playbook` is a CLI tool for creating and managing **Claude Code playbooks**. A playbook is an isolated Claude Code instance — a directory with its own settings, CLAUDE.md, hooks, MCP servers, and history, completely separate from the default `~/.claude/` installation and from every other playbook.

Playbooks solve a simple problem: Claude Code stores everything in a single config directory. If you want to try a new hook, a different model default, or a custom CLAUDE.md without risking your main setup, you need a separate environment. Under the hood, a playbook is just a directory, and Claude Code reads from wherever `CLAUDE_CONFIG_DIR` points. `claude-playbook` makes creating, running, sharing, and maintaining those directories easy.

### What changed from v2

v3 simplifies the multi-playbook-repo model:

- A playbook is **a directory**. It does not need a marker file.
- The `.playbook` manifest is **opt-in metadata** — including, when needed, a declaration of nested playbooks.
- The 2-level discovery rule, the "container vs playbook" type distinction, and the synthetic-`.playbook`-written-into-the-clone behaviour are all gone.
- `install` no longer mutates the cloned source.
- A new `--subdir` flag lets users install a single slice of an unstructured source repo.

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

There is no index file, no database, no registry. The playbooks root directory and the user's shell configuration file are the only state the tool reads from or writes to. Any mutation users make with `mv`, `rm`, or a text editor is immediately consistent with what the tool sees on its next invocation.

### The playbooks root

All playbooks live under a single **playbooks root** directory. The default is `~/.claude-playbooks/`. This is configurable via the `--playbooks-dir` flag or the `CLAUDE_PLAYBOOKS_DIR` environment variable, applied globally to every command.

### Playbook discovery

Each direct child of the playbooks root is an **install** — usually a cloned repo or a manually created directory. An install yields one or more playbooks based on a single rule:

- If the install root has a `.playbook` manifest containing a `playbooks = [...]` field, the install is a **group**. Each entry in `playbooks` is a nested playbook; the install root itself is not a runnable playbook.
- Otherwise the install root itself is **one playbook**. A `.playbook` manifest, if present, provides metadata only.

Nesting is unbounded: a nested playbook may itself have a `.playbook` declaring further nested playbooks. The same rule applies recursively.

```
~/.claude-playbooks/
    experiment/                       ← playbook (no manifest needed)
        CLAUDE.md
    pai/                              ← playbook (manifest provides metadata)
        .playbook
        CLAUDE.md
        settings.json
    my-suite/                         ← group (.playbook declares children)
        .playbook                     ← contains playbooks = [...]
        shared/                       ← ordinary directory, not a playbook
        frontend/                     ← nested playbook
            CLAUDE.md
        backend/                      ← nested playbook
            .playbook
            CLAUDE.md
            api/                      ← nested playbook (declared by backend's manifest)
                CLAUDE.md
```

There is no maximum depth, no "container" type, and no shadowing rule to memorize. Discovery is fully driven by manifest declarations after the first level.

### Playbook names

A playbook's **name** is its path relative to the playbooks root, using forward slashes. For the tree above:

- `experiment`
- `pai`
- `my-suite/frontend`
- `my-suite/backend`
- `my-suite/backend/api`

Names are used wherever a playbook is referenced: `run`, `delete`, `info`, `rename`, `alias`, `update`.

Recommended characters: lowercase letters, digits, and dashes. Names must not start with `.` (to avoid hidden directories). Slashes are path separators inside the playbooks root, not part of any single directory name.

---

## Commands

### `claude-playbook` (no arguments)

Prints a one-line description and lists all discovered playbooks with how to run each.

```
claude-playbook -- manage isolated Claude Code instances

Available playbooks:

  experiment            claude-playbook run experiment            (or: experiment)
  pai                   claude-playbook run pai                   (or: pai)
  my-suite/frontend     claude-playbook run my-suite/frontend     (or: fe)
  my-suite/backend      claude-playbook run my-suite/backend      (or: be)
  my-suite/backend/api  claude-playbook run my-suite/backend/api  (no alias set)

Run 'claude-playbook --help' for all commands.
```

Empty state:
```
claude-playbook -- manage isolated Claude Code instances

No playbooks found. Run 'claude-playbook create <name>' to get started.
```

---

### `claude-playbook list [prefix]`

Lists all playbooks in a table. If a `prefix` argument is given, only playbooks whose names start with that prefix are shown — useful for exploring a single group.

```bash
claude-playbook list
claude-playbook list my-suite/
```

**Output:**

```
NAME                    PATH                                              ALIAS  LAST USED
----                    ----                                              -----  ---------
experiment              ~/.claude-playbooks/experiment                    -      2 days ago
pai                     ~/.claude-playbooks/pai                           pai    1 hour ago
my-suite/frontend       ~/.claude-playbooks/my-suite/frontend             fe     30 min ago
my-suite/backend        ~/.claude-playbooks/my-suite/backend              be     never
my-suite/backend/api    ~/.claude-playbooks/my-suite/backend/api          -      never
```

Column widths are computed from the longest NAME, PATH, and ALIAS values, with minimum widths of 4, 4, and 5. `ALIAS` shows `-` when none is set. `LAST USED` is derived from the playbook directory's mtime.

---

### `claude-playbook create <name>`

Creates a new, empty playbook.

```bash
claude-playbook create experiment
claude-playbook create experiment --no-alias
claude-playbook create experiment --alias exp
claude-playbook create my-suite/scratch    # nested under an existing install
```

**Steps:**
1. Validate the name. If it contains slashes, parent directories must already exist (created by `install` or a previous `create`).
2. Check the target directory does not exist.
3. Create the directory.
4. Unless `--no-alias`, write a shell alias. The alias name defaults to the last segment of the playbook name (`scratch` for `my-suite/scratch`). Override with `--alias`.

No `.playbook` is written by default. Add one only when you want to set metadata or declare nested children.

**Flags:**

| Flag | Description |
|------|-------------|
| `--alias <alias>` | Use a custom alias name (default: last segment of the playbook name) |
| `--no-alias` | Skip alias creation |

`--alias` and `--no-alias` cannot be combined.

**Errors:**
- Name already exists → `playbook "experiment" already exists at ~/.claude-playbooks/experiment`
- Name starts with `.` → `playbook name cannot start with '.'`
- Parent does not exist → `parent directory "my-suite" does not exist`
- Both `--alias` and `--no-alias` → `--no-alias and --alias cannot be used together`
- Target lies inside a group's declared child path → `"my-suite/frontend" is declared in ~/.claude-playbooks/my-suite/.playbook; edit the manifest to add new playbooks under this group`

---

### `claude-playbook run <name> [claude-flags...]`

Runs Claude Code using the named playbook. Any flags after the name are forwarded to `claude` unchanged.

```bash
claude-playbook run experiment
claude-playbook run my-suite/backend
claude-playbook run pai --model claude-opus-4-6
```

Equivalent to:
```bash
CLAUDE_CONFIG_DIR=~/.claude-playbooks/<name> claude [claude-flags...]
```

Flag parsing is disabled so arbitrary `claude` flags pass through. The global `--playbooks-dir` and `--shell-config` flags are extracted from the argument list before forwarding.

A group (a directory whose `.playbook` declares children) is not runnable; `run my-suite` errors. Run a nested playbook instead.

**Errors:**
- Playbook not found → `unknown playbook "experiment". Run 'claude-playbook list' to see available playbooks`
- Target is a group → `"my-suite" is a group, not a playbook. Run a nested playbook (e.g. 'claude-playbook run my-suite/frontend')`
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

Installs a playbook — or a group of nested playbooks — from a Git repository or a local directory. The cloned source is placed under the playbooks root as a single install.

```bash
# Git repo (derives install name from the URL)
claude-playbook install https://github.com/user/pai

# Git repo with a custom install name
claude-playbook install https://github.com/user/repo --name myrepo

# Local directory (symlinked by default)
claude-playbook install ~/dev/my-playbook

# Local directory (copied, becomes independent of source)
claude-playbook install ~/dev/my-playbook --copy

# Group repo (the .playbook declares N children) — opt into aliases for all of them
claude-playbook install https://github.com/user/my-suite --alias-all

# Skip alias creation regardless
claude-playbook install https://github.com/user/repo --no-alias

# Install only a slice of an unstructured source
claude-playbook install https://github.com/user/dotfiles --subdir claude-config --name dots
```

**Source types:**

| Source | Behaviour |
|--------|-----------|
| URL (`http://`, `https://`, `git@`, `git://`) | Shallow-cloned (`git clone --depth=1`) |
| Anything else | Treated as a local filesystem path |

**Flags:**

| Flag | Description |
|------|-------------|
| `--name <name>` | Override the install directory name under the playbooks root |
| `--subdir <path>` | Use only this subdirectory of the source as the playbook (see below) |
| `--alias <alias>` | Single-playbook install only: custom alias name. Errors if the source declares a group. |
| `--alias-all` | Group install: write an alias for every nested playbook |
| `--no-alias` | Skip alias creation |
| `--copy` | Copy instead of symlink (local paths only) |

**Steps (no `--subdir`):**
1. Derive the install directory name from `--name`, or from the last path segment of the URL (stripped of `.git`), or the source directory's name.
2. Check the target doesn't already exist under the playbooks root.
3. Fetch the source:
   - Git URL → `git clone --depth=1` into the target.
   - Local path (default) → symlink target → source.
   - Local path with `--copy` → recursive copy.
4. Read the install root's `.playbook` if present:
   - If it contains `playbooks = [...]`, the install is a group. Each entry yields a nested playbook.
   - Otherwise the install root is one playbook.
   - If there is no `.playbook` at all, the install root is one playbook.
5. Write aliases per the default rules below.
6. Print a summary.

`install` never writes a `.playbook` into the source. A repo with no manifest is a valid single-playbook install on its own.

**Steps (with `--subdir <path>`):**
1. Fetch the source as above into a scratch location (for URLs, a temp directory; for local paths, the source itself).
2. Verify `<source>/<path>` exists and is a directory.
3. Move (or copy, for local) `<source>/<path>` to `~/.claude-playbooks/<name>/`. For URL sources, the rest of the clone is discarded.
4. Treat the result as a single-playbook install. Any `.playbook` already inside `<source>/<path>` provides metadata for that one playbook. Any `playbooks = [...]` field in `<source>/<path>/.playbook` is ignored — `--subdir` always produces exactly one playbook.
5. Default install name (`--name` not given) is the last segment of `<path>`.

`--subdir` exists for **one** use case: extracting a single usable slice from a repo that was not laid out as a playbook collection. For repos that *are* laid out as a group (with a manifest declaring children), install without `--subdir` and let the manifest do its job.

**Default alias behaviour**

- **Single-playbook install** → one alias is written, using either the manifest's `alias` field, or its `name` field, or the install directory name, in that order. Override with `--alias`.
- **Group install** → no aliases are written. The tool prints the list with next steps. Use `--alias-all` to opt into writing one alias per nested playbook (using each child's manifest fields, falling back to its directory name).

`--no-alias` suppresses alias writing in both cases.

**Alias collision handling** (only when writing):

- Between playbooks in this install (two playbooks whose default alias names collide): the colliding aliases are prefixed with the install name, e.g. `my-suite-frontend`.
- With existing shell aliases: the conflicting alias is skipped with a warning; the user can set it manually with `claude-playbook alias <name> <alias>`.

**CLAUDE.md warning:** for any discovered playbook missing a `CLAUDE.md`, a warning is printed. Claude Code works without one, but most playbooks benefit from having one.

**Errors:**
- `--copy` with a URL → `--copy only applies to local paths. Git installs always clone`
- `--alias` with a group → `--alias accepts a single name; this install declares N nested playbooks. Use --alias-all or add aliases with 'claude-playbook alias'`
- `--subdir` path missing in source → `subdirectory "<path>" not found in source`
- Manifest declares a child whose directory does not exist → `~/.claude-playbooks/my-suite/.playbook declares "frontend" but the directory is missing`
- Source not found → `'~/dev/foo' not found`
- Source is a file → `'~/dev/foo' is not a directory`
- Install name already taken → `"myrepo" already exists at ~/.claude-playbooks/myrepo. Use --name to choose a different name`
- `git` not on PATH → `'git' command not found`
- Clone fails → git's error output is shown directly

**Sample output (single-playbook install):**
```
Cloning https://github.com/user/pai...
Installed "pai" at ~/.claude-playbooks/pai
Alias:  pai added to ~/.zshrc

Reload your shell or run:
  source ~/.zshrc

Then run with:
  pai
```

**Sample output (group install, no aliases):**
```
Cloning https://github.com/user/my-suite...
Installed "my-suite" at ~/.claude-playbooks/my-suite
Group declares 3 playbooks:
  my-suite/frontend
  my-suite/backend
  my-suite/data

No aliases created. Add ones you want:
  claude-playbook alias my-suite/frontend fe
  claude-playbook alias my-suite/backend be

Or run without an alias:
  claude-playbook run my-suite/frontend
```

**Sample output (`--subdir`):**
```
Cloning https://github.com/user/dotfiles...
Extracted "claude-config" from the source.
Installed "dots" at ~/.claude-playbooks/dots
Alias:  dots added to ~/.zshrc
```

---

### `claude-playbook info <name>`

Shows detailed information about a playbook or a group.

```bash
claude-playbook info experiment
claude-playbook info my-suite              # group
claude-playbook info my-suite/backend
```

**Output (playbook):**
```
Name:        my-suite/backend
Version:     1.2.0
Path:        ~/.claude-playbooks/my-suite/backend
Type:        directory
Alias:       be
Size:        24 files, 3 directories
Last used:   2 hours ago
Description: Backend development assistant
Homepage:    https://github.com/user/my-suite
Author:      Jane Doe
Updater:     bin/update-playbook.sh
```

**Output (group):**
```
Name:        my-suite
Type:        group
Path:        ~/.claude-playbooks/my-suite
Declares 3 playbooks:
  my-suite/frontend   (alias: fe)
  my-suite/backend    (alias: be)
  my-suite/data       (no alias)
Updater:     bin/update-playbook.sh
```

**Fields:**

| Field | Meaning |
|-------|---------|
| `Name` | Playbook or group name (path relative to playbooks root) |
| `Version` | `version` field from the `.playbook` manifest, if set |
| `Path` | Absolute path to the directory |
| `Type` | `directory`, `symlink → <target>`, `symlink → <target> (BROKEN)`, or `group` |
| `Alias` | Shell alias for this playbook, or `(none)`. Omitted for groups. |
| `Size` | File and directory counts. Omitted for groups. |
| `Last used` | Human-readable time since the directory was last modified. Omitted for groups. |
| `Description` | From `.playbook` manifest, if present |
| `Homepage` | From `.playbook` manifest, if present |
| `Author` | From `.playbook` manifest, if present |
| `Updater` | `bin/update-playbook.sh` if it exists and is executable, else `(none)` |

**Errors:**
- Target not found → `unknown playbook "experiment"`

---

### `claude-playbook rename <old-name> <new-name>`

Renames a directory (a playbook, a group, or any install root) and updates affected aliases.

```bash
claude-playbook rename experiment exp-1
claude-playbook rename my-suite/backend my-suite/api-server   # rename a nested playbook
claude-playbook rename my-suite big-suite                     # rename a group
```

**Steps:**
1. Validate the old name exists and the new name doesn't.
2. Rename the directory with `mv`.
3. Update all shell aliases whose `CLAUDE_CONFIG_DIR=<path>` points into the old location — rewrite them to point at the new location.
4. If the renamed target is a nested playbook declared in a parent group's `.playbook`, update the manifest entry accordingly.

**Renaming a group:** any aliases that pointed at playbooks inside the old group are updated to point at the new path. The group itself has no alias.

**Flags:**

| Flag | Description |
|------|-------------|
| `--alias <alias>` | (Playbook rename only) use a custom alias name for the renamed playbook |
| `--no-alias` | (Playbook rename only) drop the alias if one existed |

**Errors:**
- Old name not found → `unknown playbook "experiment"`
- New name already exists → `"exp-1" already exists at ~/.claude-playbooks/exp-1`
- Both `--alias` and `--no-alias` → `--no-alias and --alias cannot be used together`

---

### `claude-playbook alias [name] [new-alias]`

Lists or manages shell aliases. **Read-only when given one argument** — no hidden side effects.

```bash
claude-playbook alias                                # list all playbooks and their aliases
claude-playbook alias my-suite/backend               # show alias for this playbook, or say "none"
claude-playbook alias my-suite/backend be            # set alias to 'be' (creates or replaces)
claude-playbook alias my-suite/backend --remove      # remove alias
```

**No arguments** — lists all playbooks with the full alias lines from the shell config:

```
experiment           alias experiment='CLAUDE_CONFIG_DIR=~/.claude-playbooks/experiment claude'
pai                  alias pai='CLAUDE_CONFIG_DIR=~/.claude-playbooks/pai claude'
my-suite/frontend    alias fe='CLAUDE_CONFIG_DIR=~/.claude-playbooks/my-suite/frontend claude'
my-suite/backend     (no alias)
```

Showing the full alias line lets users see exactly what will run, including any flags they've added manually.

**One argument, alias exists** — prints it.
```
Alias for "my-suite/frontend": alias fe='CLAUDE_CONFIG_DIR=~/.claude-playbooks/my-suite/frontend claude'
```

**One argument, no alias** — reports only; does **not** create one.
```
Playbook "my-suite/backend" has no alias set.
Use 'claude-playbook alias my-suite/backend <alias-name>' to create one.
```

**Two arguments** — sets the alias (creates or replaces).

**With `--remove`** — removes any aliases pointing at this playbook. No-op with a message if none exist.

Groups never have aliases. `alias my-suite` errors with `"my-suite" is a group, not a playbook`.

**Flags:**

| Flag | Description |
|------|-------------|
| `--remove` | Remove the alias(es) for the named playbook |

**Errors:**
- Playbook not found → `unknown playbook "my-suite/backend"`
- Target is a group → `"my-suite" is a group, not a playbook`
- Shell config cannot be found or written → see Alias Management

---

### `claude-playbook delete <name>`

Deletes a playbook or an entire group.

```bash
claude-playbook delete experiment                  # prompts
claude-playbook delete my-suite/backend -y         # just one playbook
claude-playbook delete my-suite                    # the whole group; lists playbooks first
```

**Confirmation prompt for a single playbook:**
```
Playbook: my-suite/backend
Location: ~/.claude-playbooks/my-suite/backend
Alias:    be (will be removed from ~/.zshrc)
Contents: 12 files, 3 directories

Permanently delete? [y/N]
```

**Confirmation prompt for a group:**
```
Group:     my-suite
Location:  ~/.claude-playbooks/my-suite
Declares 3 playbooks:
  my-suite/frontend     (alias: fe)
  my-suite/backend      (alias: be)
  my-suite/data         (no alias)
Total:     142 files, 28 directories

Permanently delete group and all playbooks inside? [y/N]
```

**Deletion scope:**
- The target directory (symlink removed; target preserved).
- All shell aliases pointing into the deleted tree.
- If the target is a nested playbook declared in a parent group's `.playbook`, the corresponding entry is removed from the manifest.

**Flags:**

| Flag | Description |
|------|-------------|
| `-y`, `--yes` | Skip the confirmation prompt |

**Errors:**
- Name not found → `"experiment" not found under ~/.claude-playbooks`

**Graceful cases:** if the directory is already gone, the command still cleans up any dangling aliases and reports success.

---

### `claude-playbook update [name]`

Updates either the `claude-playbook` tool itself, or a specific install — based on whether a name is given.

#### `claude-playbook update` (no arguments) — self-update

Updates the `claude-playbook` binary to the latest release.

```bash
claude-playbook update
claude-playbook update --check
claude-playbook update --version v1.1.0
```

**Steps:**
1. Query the GitHub releases API for the latest tag.
2. Compare to the currently running version.
3. If newer (or `--version` is given), download the right asset for the current OS/arch.
4. Replace the running binary in place.
5. Print old → new version.

**Flags:**

| Flag | Description |
|------|-------------|
| `--check` | Report availability only; do not install |
| `--version <tag>` | Install a specific release tag |

**Errors:**
- Already latest (and no `--version`) → prints current version and exits successfully
- Binary path not writable → `cannot write to <path>. Try 'sudo claude-playbook update' or re-run the install script`
- Release not found → `release <tag> not found`
- No asset for current OS/arch → `no binary for <os>/<arch> in release <tag>`
- Dev build → warns and asks for confirmation before overwriting

#### `claude-playbook update <name>` — update a playbook or group

Delegates to `<install-root>/bin/update-playbook.sh`. For a nested playbook, the install root is the nearest ancestor that was the original install target (i.e. the direct child of the playbooks root). The script lives at the install root, not inside a nested playbook.

```bash
claude-playbook update pai               # updates the pai install
claude-playbook update my-suite          # updates the whole group (a single git working copy)
claude-playbook update my-suite/backend  # also resolves to the my-suite install root
```

**Behaviour:**
1. Resolve the target to its install root (the nearest ancestor directly under the playbooks root).
2. Check `<install-root>/bin/update-playbook.sh` exists and is executable.
3. Run the script with:
   - Working directory: the install root
   - Environment: inherited, with `CLAUDE_PLAYBOOK_TARGET=<original-name>` and `CLAUDE_PLAYBOOK_PATH=<original-path>` so the script can branch on what the user asked for
   - Arguments: any remaining command-line arguments are forwarded to the script
4. Forward stdout, stderr, and exit code.

**Why install-root-only:** for git-backed groups, the whole working copy updates together. Per-playbook updates inside a group are conceptually a no-op — the script can choose to honour them by inspecting `CLAUDE_PLAYBOOK_TARGET`, but the default is "update the whole install."

**Why delegated:** playbooks and repos come from many sources with many update strategies. A one-size-fits-all strategy is wrong for most. The playbook or group author writes the right logic for their own distribution.

**Example `bin/update-playbook.sh` for a git-backed install:**
```bash
#!/bin/sh
set -e
cd "$(dirname "$0")/.."
git pull --ff-only
```

**Errors:**
- Target not found → `"pai" not found under ~/.claude-playbooks`
- Update script missing → `"pai" has no update script at bin/update-playbook.sh. This install does not support updates; see its documentation.`
- Script not executable → `update script is not executable: <path>`
- Script exits non-zero → exit code forwarded; `update-playbook.sh exited with code <n>` is printed to stderr

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

The `.playbook` manifest is **optional**. Its presence and contents serve three purposes:

1. **Metadata** for `info` and for `install`'s default alias selection.
2. **Group declaration** via the `playbooks = [...]` field — turning the directory into a group of nested playbooks instead of a leaf.
3. **Defaults** that authors can set for their distribution.

A directory without a `.playbook` is a perfectly valid playbook. A `.playbook` without a `playbooks` field is a leaf playbook with metadata.

**Format:**

```toml
version = "1.0.0"
name = "pai"
alias = "pai"
description = "Personal AI Infrastructure by Daniel Miessler"
homepage = "https://github.com/danielmiessler/pai"
author = "Daniel Miessler"
```

**Group example:**

```toml
name = "my-suite"
description = "Frontend, backend, and data playbooks for project X"
homepage = "https://github.com/user/my-suite"

[[playbooks]]
path = "frontend"
name = "fe"
description = "Frontend development assistant"

[[playbooks]]
path = "backend"
name = "be"
description = "Backend development assistant"

[[playbooks]]
path = "data"
description = "Data analysis assistant"
```

Each `[[playbooks]]` entry declares one nested playbook. The `path` is interpreted relative to the directory containing this manifest. A nested playbook may itself contain a `.playbook` to provide its own metadata or declare further nesting.

**Top-level fields:**

| Field | Meaning |
|-------|---------|
| `version` | Version of the playbook itself (free-form semver string). Shown by `info`. Not enforced by the tool. |
| `name` | Preferred playbook name. `install` uses it as a suggestion; the actual name is always the path relative to the playbooks root. |
| `alias` | Preferred alias for `install` to suggest when writing the default alias. |
| `description` | Human-readable description, shown by `info`. |
| `homepage` | Optional URL, shown by `info`. |
| `author` | Optional author name or contact, shown by `info`. |
| `playbooks` | Array of nested playbook declarations. Presence of this field makes the directory a group. |

**`[[playbooks]]` entry fields:**

| Field | Meaning |
|-------|---------|
| `path` | **Required.** Subdirectory relative to the manifest. Must exist as a directory. |
| `name` | Preferred name for this nested playbook (used as alias suggestion). |
| `alias` | Preferred alias. |
| `description` | Description for `info`. |
| `homepage` | Optional URL for `info`. |
| `author` | Optional author for `info`. |

If a nested playbook also has its own `.playbook`, the nested manifest takes precedence over the parent's entry for fields they both set. The parent's entry alone is enough — nested manifests are optional.

**Forward compatibility:** unknown fields are ignored. Manifest authors may include fields for future tool versions without breaking older installs.

**Errors:**
- Invalid TOML → `invalid .playbook at <path>: <reason>`
- `playbooks` entry with missing or empty `path` → `invalid .playbook at <path>: 'path' is required for each playbooks entry`
- Declared `path` does not exist → `~/.claude-playbooks/my-suite/.playbook declares "frontend" but the directory is missing`
- Declared `path` escapes the manifest directory (e.g. `../foo`) → `invalid .playbook at <path>: 'path' must be relative and stay inside the directory`

---

## Alias Management

Aliases are plain `alias` lines in the user's shell configuration file. No registry, no metadata, no comment markers — shell lines in the config are the complete truth.

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
alias <alias-name>='CLAUDE_CONFIG_DIR=<playbook-path> claude'
```

No surrounding comments, no markers. A user-authored alias and a tool-written alias are indistinguishable — which is the point.

### Lookup

Two lookup directions, both by plain grep:

- **By alias name** (for duplicate checks, removals): match lines where the alias definition is `alias <name>=...` (tolerating leading whitespace).
- **By playbook** (for `list`, `info`, and `alias` with no args): match lines containing `CLAUDE_CONFIG_DIR=<path>` where `<path>` (with `~` and `$HOME` expanded) resolves to the playbook's directory.

Because lookup works on the actual `alias` line content, hand-maintained aliases are fully supported. If a user writes:

```bash
alias myexp="CLAUDE_CONFIG_DIR=$HOME/.claude-playbooks/experiment claude --model claude-opus-4-6"
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
alias experiment='CLAUDE_CONFIG_DIR=~/.claude-playbooks/experiment claude --model claude-opus-4-6'
alias be='CLAUDE_CONFIG_DIR=~/.claude-playbooks/my-suite/backend claude --permission-mode auto --effort max'
```

`alias` (no args) shows the full line so users can see exactly what's configured.

---

## Global Flags

These flags work on every command.

| Flag | Description |
|------|-------------|
| `--playbooks-dir <path>` | Override the playbooks root directory. Default: `~/.claude-playbooks` |
| `--shell-config <path>` | Override the shell config file. Default: auto-detected from `$SHELL` |
| `--version` | Print the version of `claude-playbook` |
| `--help`, `-h` | Show help for the command or subcommand |

### Environment variables

| Variable | Flag equivalent |
|----------|----------------|
| `CLAUDE_PLAYBOOKS_DIR` | `--playbooks-dir` |
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
Error: "my-suite" is a group, not a playbook. Run a nested playbook (e.g. 'claude-playbook run my-suite/frontend')
Error: 'claude' command not found. Install Claude Code first: https://claude.ai/download
Error: "pai" has no update script at bin/update-playbook.sh. This install does not support updates; see its documentation.
Error: invalid .playbook at ~/.claude-playbooks/foo/.playbook: toml: line 3: expected '=', got ':'
Error: ~/.claude-playbooks/my-suite/.playbook declares "frontend" but the directory is missing
```
