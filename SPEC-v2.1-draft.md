# claude-playbook CLI Specification (v2.1 draft)

## Overview

`claude-playbook` is a CLI tool for creating and managing **Claude Code playbooks**. A playbook is an isolated Claude Code instance — a directory with its own settings, CLAUDE.md, hooks, MCP servers, and history, completely separate from the default `~/.claude/` installation and from every other playbook.

Playbooks solve a simple problem: Claude Code stores everything in a single config directory. If you want to try a new hook, a different model default, or a custom CLAUDE.md without risking your main setup, you need a separate environment. Under the hood, a playbook is just a directory, and Claude Code reads from wherever `CLAUDE_CONFIG_DIR` points. `claude-playbook` makes creating, running, sharing, and maintaining those directories easy.

### What changed from v2

- **`.playbook` is now mandatory.** The tool no longer silently writes one for you. `install` errors out if the source has no `.playbook`, unless you pass `--init`.
- **Two install modes:** *cherry-pick* (`--subdir`, or a GitHub `/tree/<branch>/<path>` URL) and *tree* (whole repo with a root `.playbook`).
- **No more containers.** A repo with multiple playbooks must have a root `.playbook` that declares them under `[[children]]`. Filesystem walking is no longer used to discover children.
- **Local installs always copy.** `--copy` is removed; symlink-on-install is removed. The new `link` command covers the symlink case explicitly.
- **`link` command** symlinks an external directory into the playbooks root, prompting for metadata if the target has no `.playbook`.
- **`dealias`** is a new top-level command (sugar for `alias <name> --remove`).

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

A directory is a **playbook** if it contains a `.playbook` file at its root. The presence of the file is what makes the directory a playbook; the file's contents are metadata.

Discovery walks the **immediate children** of the playbooks root only:

- A direct child of the playbooks root with a `.playbook` file is a **top-level playbook**.
- A top-level playbook's `.playbook` may declare **child playbooks** in a `[[children]]` table. These children are exposed under the namespace `<top-level>/<child-name>`.
- Directories without a `.playbook` are ignored. Directories not declared as children of a top-level playbook are ignored, even if they themselves contain a `.playbook`.

The parent's `[[children]]` table is the sole source of truth for what children exist; the filesystem is no longer walked to find them.

```
~/.claude-playbooks/
    experiment/               ← top-level playbook
        .playbook
        CLAUDE.md
    pai/                      ← top-level playbook
        .playbook
        CLAUDE.md
    awesome/                  ← top-level playbook with children
        .playbook             ← declares [[children]] for dba and sre
        playbooks/
            dba/              ← child "awesome/dba"
                CLAUDE.md
            sre/              ← child "awesome/sre"
                CLAUDE.md
        scripts/              ← undeclared, invisible to the tool
```

Children do not need their own `.playbook` file. If they have one (for example so they can also be cherry-pick installed), it is used as fallback metadata; the parent's declaration is authoritative for tree-installed trees.

### Playbook names

A playbook's **name** is its identifier in commands.

- Top-level playbooks: the directory name under the playbooks root (`experiment`, `pai`, `awesome`).
- Children: `<top-level>/<child-name>`, where `<child-name>` is the `name` field from the parent's `[[children]]` entry. The child name is independent of the directory's path on disk — `awesome/dba` may live at `awesome/playbooks/dba/`.

Names are used wherever a playbook is referenced: `run`, `delete`, `info`, `rename`, `alias`, `update`.

Recommended characters: lowercase letters, digits, and dashes. Names must not start with `.`. The slash is reserved as the namespace separator.

---

## Commands

### `claude-playbook` (no arguments)

Prints a one-line description and lists all discovered playbooks with how to run each.

```
claude-playbook -- manage isolated Claude Code instances

Available playbooks:

  experiment          claude-playbook run experiment          (or: experiment)
  pai                 claude-playbook run pai                 (or: pai)
  awesome             claude-playbook run awesome             (or: ap)
    awesome/dba       claude-playbook run awesome/dba         (or: ap-dba)
    awesome/sre       claude-playbook run awesome/sre         (no alias set)

Run 'claude-playbook --help' for all commands.
```

Children are indented under their parent. The output is intentionally terse and machine-readable.

Empty state:
```
claude-playbook -- manage isolated Claude Code instances

No playbooks found. Run 'claude-playbook create <name>' to get started.
```

---

### `claude-playbook list [prefix]`

Lists all playbooks in a table. If a `prefix` argument is given, only playbooks whose names start with that prefix are shown.

```bash
claude-playbook list
claude-playbook list awesome/
```

**Output:**

```
NAME                  PATH                                            ALIAS       LAST USED
----                  ----                                            -----       ---------
experiment            ~/.claude-playbooks/experiment                  experiment  2 days ago
pai                   ~/.claude-playbooks/pai                         pai         1 hour ago
awesome               ~/.claude-playbooks/awesome                     ap          2 hours ago
  awesome/dba         ~/.claude-playbooks/awesome/playbooks/dba       ap-dba      2 hours ago
  awesome/sre         ~/.claude-playbooks/awesome/playbooks/sre       -           never
```

Children are indented two spaces. Column widths are computed from the longest NAME, PATH, and ALIAS values, with minimum widths of 4, 4, and 5. `ALIAS` shows `-` when none is set. `LAST USED` is derived from the playbook directory's mtime.

---

### `claude-playbook create <name>`

Creates a new, empty top-level playbook under the playbooks root.

```bash
claude-playbook create experiment
claude-playbook create experiment --no-alias
claude-playbook create experiment --alias exp
```

**Steps:**
1. Validate the name. Names may not contain slashes — `create` only makes top-level playbooks. (To add a child, edit the parent's `.playbook` and add a `[[children]]` entry.)
2. Validate the name does not start with `.`.
3. Check the target directory does not already exist under the playbooks root.
4. Create the directory.
5. Write a minimal `.playbook` file:
   ```toml
   version = "0.1.0"
   name = "<name>"
   ```
6. Unless `--no-alias`, write a shell alias. The alias name defaults to `<name>`. Override with `--alias`.

**Flags:**

| Flag | Description |
|------|-------------|
| `--alias <alias>` | Use a custom alias name (default: `<name>`) |
| `--no-alias` | Skip alias creation |

`--alias` and `--no-alias` cannot be combined.

**Errors:**
- Name contains a slash → `'create' only creates top-level playbooks. To add a child, declare it in the parent's .playbook.`
- Name already exists → `playbook "experiment" already exists at ~/.claude-playbooks/experiment`
- Name starts with `.` → `playbook name cannot start with '.'`
- Both `--alias` and `--no-alias` → `--no-alias and --alias cannot be used together`

---

### `claude-playbook run <name> [claude-flags...]`

Runs Claude Code using the named playbook. Any flags after the name are forwarded to `claude` unchanged.

```bash
claude-playbook run experiment
claude-playbook run awesome/dba
claude-playbook run pai --model claude-opus-4-6
```

Equivalent to:
```bash
CLAUDE_CONFIG_DIR=<resolved-path> claude [claude-flags...]
```

For a top-level playbook, the resolved path is `<playbooks-root>/<name>`. For a child, it is `<playbooks-root>/<top-level>/<child-path>`, where `<child-path>` comes from the parent's `[[children]]` entry.

Flag parsing is disabled so arbitrary `claude` flags pass through. Global `--playbooks-dir` and `--shell-config` are extracted before forwarding.

**Errors:**
- Playbook not found → `unknown playbook "experiment". Run 'claude-playbook list' to see available playbooks`
- `claude` not on PATH → `'claude' command not found. Install Claude Code first: https://claude.ai/download`

---

### `claude-playbook start <path> [claude-flags...]`

Starts an ad-hoc Claude Code session at any directory. Creates the directory if it doesn't exist. No playbook registration, no `.playbook` file, no discovery — just set `CLAUDE_CONFIG_DIR` and run.

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

Installs a playbook from a Git URL or a local directory into the playbooks root. **Always copies** for local sources; cloning shallow for Git sources.

There are two modes, selected by whether a subdirectory was specified:

- **Tree install** — no subdir. The whole source is installed at the playbooks root. The source's root `.playbook` may declare `[[children]]` to expose child playbooks.
- **Cherry-pick install** — `--subdir <path>` or a GitHub `/tree/<branch>/<path>` URL. Only that subdirectory is installed, as a flat top-level playbook. Children declarations in any internal `.playbook` are ignored — cherry-picks are always flat.

```bash
# Tree install
claude-playbook install https://github.com/user/awesome
claude-playbook install https://github.com/user/awesome --name ap --alias ap

# Tree install with all children's aliases
claude-playbook install https://github.com/user/awesome --alias-all

# Cherry-pick a subdir
claude-playbook install https://github.com/user/awesome --subdir playbooks/dba
claude-playbook install https://github.com/user/awesome --subdir playbooks/dba --name dba --alias ap-dba

# Cherry-pick via browser URL (branch + subdir parsed automatically)
claude-playbook install https://github.com/user/awesome/tree/main/playbooks/dba

# Local source
claude-playbook install ~/dev/my-thing

# Source has no .playbook — write one at the install destination
claude-playbook install ~/dev/scratch --init
```

**Source types:**

| Source | Behaviour |
|--------|-----------|
| URL (`http://`, `https://`, `git@`, `git://`) | Shallow-cloned (`git clone --depth=1`). For cherry-picks, optionally `--filter=blob:none --sparse` + `sparse-checkout set <subdir>`. |
| GitHub `/tree/<ref>/<path>` URL | Parsed into `<repo-url>` + `--branch <ref>` + `--subdir <path>` |
| Anything else | Treated as a local filesystem path; **always copied** (recursive copy) |

**Flags:**

| Flag | Description |
|------|-------------|
| `--name <name>` | Override the top-level directory name under the playbooks root |
| `--subdir <path>` | Cherry-pick: install only this subdirectory of the source |
| `--branch <ref>` | Git URL only: clone this ref instead of the default branch |
| `--alias <alias>` | Custom alias for the installed top-level playbook |
| `--no-alias` | Skip alias creation entirely |
| `--alias-all` | Tree install only: write aliases for the root and every child that declares one |
| `--init` | Source has no `.playbook` — write a minimal one at the install destination |

**Steps:**
1. Parse the source. If it is a GitHub `/tree/<ref>/<path>` URL, split into `<repo-url>`, `<ref>`, `<path>` and treat as `<repo-url> --branch <ref> --subdir <path>`. CLI `--branch`/`--subdir` override.
2. Determine the install name (`--name`, else manifest's `name` field if present, else the last path segment of the URL stripped of `.git`, else the source directory's name).
3. Verify the target name is not taken.
4. Fetch:
   - Git URL → `git clone --depth=1 [--branch <ref>]` into a temp dir; if `--subdir`, sparse-checkout the subdir; then move the relevant tree to the install destination.
   - Local path → recursive copy of the source (or of the subdir, if `--subdir`).
5. Look for `.playbook` at the install destination root.
   - **Found**: parse it. If tree install and the manifest has `[[children]]`, validate every declared child path exists.
   - **Not found** + `--init`: write a minimal `.playbook` at the install destination.
   - **Not found** + no `--init`: error.
6. Write aliases:
   - Tree install: root alias by default (unless `--no-alias`). Children get aliases only with `--alias-all`, and only those that declare an `alias` field in `[[children]]`.
   - Cherry-pick: single alias for the installed playbook (default name = manifest alias > manifest name > install name).
7. Print a summary.

**Default alias resolution (top-level)**

In order: `--alias`, then manifest `alias` field, then `--name`, then derived install name.

**Default alias resolution (children, only on `--alias-all`)**

The `alias` field on the `[[children]]` entry. If the entry has no `alias` field, no alias is suggested for that child.

**Alias collision handling** (only when writing):

- Between aliases in the same install: prefix later collisions with the install name (e.g. `ap-dba` instead of `dba` if `dba` already exists).
- With existing shell aliases: skip with a warning; suggest `claude-playbook alias <name> <alias>` to set manually.

**CLAUDE.md warning:** for any installed playbook (top-level or child) without a `CLAUDE.md`, print a warning. Claude Code works without one, but most playbooks benefit from having one.

**Errors:**
- `--subdir <path>` not present in source → `subdir "playbooks/dba" not found in source`
- Source `.playbook` missing, no `--init` → `<install-path> has no .playbook. Add one to the source, or pass --init.`
- Manifest declares a child whose path does not exist → `child "dba" path "playbooks/dba" not found`
- `--alias` with `--alias-all` → `--alias and --alias-all cannot be combined`
- `--alias` and `--no-alias` → `--no-alias and --alias cannot be used together`
- Source not found → `'~/dev/foo' not found`
- Source is a file → `'~/dev/foo' is not a directory`
- Name already taken → `"awesome" already exists at ~/.claude-playbooks/awesome. Use --name to choose a different name`
- `git` not on PATH → `'git' command not found`
- Clone fails → git's error output is shown directly

**Sample output (tree install, default):**
```
Cloning https://github.com/user/awesome...
Installed "awesome" at ~/.claude-playbooks/awesome
1 root playbook + 2 children:
  awesome           (alias: ap)
  awesome/dba       (no alias — pass --alias-all to add ap-dba)
  awesome/sre       (no alias)

Run with:
  ap                       # the root playbook
  claude-playbook run awesome/dba
```

**Sample output (cherry-pick):**
```
Cloning https://github.com/user/awesome (subdir playbooks/dba)...
Installed "dba" at ~/.claude-playbooks/dba
Alias:  dba added to ~/.zshrc

Reload your shell or run:
  source ~/.zshrc

Then run with:
  dba
```

---

### `claude-playbook link <target> [--name <name>]`

Symlinks an external directory into the playbooks root. Used for **active development**: edit in your repo, see changes immediately under the playbooks root.

```bash
claude-playbook link ~/dev/my-thing
claude-playbook link ~/dev/my-thing --name scratch
claude-playbook link ~/dev/my-thing --no-alias
```

**Steps:**
1. Resolve `<target>` to an absolute path; verify it is a directory.
2. Determine the link name (`--name`, else the target directory's basename).
3. Verify the link name is not taken under the playbooks root.
4. Check whether `<target>/.playbook` exists.
   - **Found**: just create the symlink.
   - **Not found**: prompt the user interactively (ssh-keygen style) for `name`, `alias`, `description`. Write `.playbook` **into the target directory**. Print a clear notice that the target was modified. If the user declines or stdin is not a TTY, abort with an error.
5. Create the symlink: `~/.claude-playbooks/<link-name>` → `<target>`.
6. Unless `--no-alias`, write a shell alias.

**Flags:**

| Flag | Description |
|------|-------------|
| `--name <name>` | Override the link name |
| `--alias <alias>` | Custom alias name |
| `--no-alias` | Skip alias creation |

**Why `link` and not `install --symlink`:** symlinking has different lifecycle semantics from copying — deletes affect only the link, the source is shared with whatever else uses it, and writing `.playbook` mutates the user's source dir. A separate command makes the trade-off explicit.

**Sample interactive flow:**
```
$ claude-playbook link ~/dev/my-thing
Target ~/dev/my-thing has no .playbook file.
This will write a .playbook into the target directory.

Playbook name [my-thing]:
Alias name [my-thing]:
Description []: Quick test playbook
Wrote ~/dev/my-thing/.playbook
Linked ~/.claude-playbooks/my-thing -> ~/dev/my-thing
Alias my-thing added to ~/.zshrc
```

**Errors:**
- Target not found → `'~/dev/foo' not found`
- Target is a file → `'~/dev/foo' is not a directory`
- Name already taken → `"my-thing" already exists at ~/.claude-playbooks/my-thing. Use --name to choose a different name`
- No `.playbook` and stdin is not a TTY → `target has no .playbook and stdin is not a TTY; cannot prompt for metadata. Add a .playbook to the target first.`

---

### `claude-playbook info <name>`

Shows detailed information about a playbook.

```bash
claude-playbook info experiment
claude-playbook info awesome
claude-playbook info awesome/dba
```

**Output (top-level with children):**
```
Name:        awesome
Version:     1.2.0
Path:        ~/.claude-playbooks/awesome
Type:        directory
Alias:       ap
Size:        24 files, 3 directories
Last used:   2 hours ago
Description: A collection of useful playbooks
Updater:     bin/update-playbook.sh
Children:
  awesome/dba    playbooks/dba    (alias: ap-dba)
  awesome/sre    playbooks/sre    (no alias)
```

**Output (child):**
```
Name:        awesome/dba
Parent:      awesome
Path:        ~/.claude-playbooks/awesome/playbooks/dba
Alias:       ap-dba
Size:        8 files, 1 directory
Last used:   2 hours ago
Description: DBA playbook
```

**Fields:**

| Field | Meaning |
|-------|---------|
| `Name` | Playbook name |
| `Parent` | (Children only) the top-level playbook this child belongs to |
| `Version` | `version` field from the `.playbook` manifest, if set |
| `Path` | Absolute path to the playbook directory |
| `Type` | `directory`, `symlink → <target>`, or `symlink → <target> (BROKEN)` |
| `Alias` | Shell alias for this playbook, or `(none)` |
| `Size` | File and directory counts |
| `Last used` | Human-readable time since the directory was last modified |
| `Description` | From the manifest, if present |
| `Updater` | `bin/update-playbook.sh` if it exists and is executable, else `(none)`. Only meaningful for top-level playbooks. |
| `Children` | (Top-level only) declared `[[children]]` from the manifest |

**Errors:**
- Playbook not found → `unknown playbook "experiment"`

---

### `claude-playbook rename <old-name> <new-name>`

Renames a top-level playbook directory and updates affected aliases. **Children cannot be renamed via this command** — edit the parent's `.playbook` to change a child's name.

```bash
claude-playbook rename experiment exp-1
claude-playbook rename awesome ap-pack
```

**Steps:**
1. Validate `<old-name>` is a top-level playbook and `<new-name>` is not in use.
2. Rename the directory with `mv`.
3. Update all shell aliases whose `CLAUDE_CONFIG_DIR=<path>` points into the old location, including any child aliases — they all get rewritten to the new path prefix.

**Flags:**

| Flag | Description |
|------|-------------|
| `--alias <alias>` | Use a custom alias name for the renamed playbook |
| `--no-alias` | Drop the alias if one existed |

**Errors:**
- Old name is a child → `"awesome/dba" is a child playbook; rename children by editing awesome/.playbook`
- Old name not found → `unknown playbook "experiment"`
- New name already exists → `"exp-1" already exists at ~/.claude-playbooks/exp-1`
- New name contains a slash → `playbook names may not contain '/' here`
- Both `--alias` and `--no-alias` → `--no-alias and --alias cannot be used together`

---

### `claude-playbook alias [name] [new-alias]`

Lists or manages shell aliases. **Read-only when given fewer than two arguments** — no hidden side effects.

```bash
claude-playbook alias                              # list all playbooks and their aliases
claude-playbook alias awesome/dba                  # show alias for this playbook
claude-playbook alias awesome/dba ap-dba           # set alias to 'ap-dba' (creates or replaces)
claude-playbook alias awesome/dba --remove         # remove alias
```

**No arguments** — lists all playbooks with the full alias lines from the shell config:

```
experiment        alias experiment='CLAUDE_CONFIG_DIR=~/.claude-playbooks/experiment claude'
pai               alias pai='CLAUDE_CONFIG_DIR=~/.claude-playbooks/pai claude'
awesome           alias ap='CLAUDE_CONFIG_DIR=~/.claude-playbooks/awesome claude'
awesome/dba       alias ap-dba='CLAUDE_CONFIG_DIR=~/.claude-playbooks/awesome/playbooks/dba claude'
awesome/sre       (no alias)
```

**One argument, alias exists** — prints it.
**One argument, no alias** — reports only; does **not** create one.
**Two arguments** — sets the alias (creates or replaces).
**With `--remove`** — removes any aliases pointing at this playbook.

**Flags:**

| Flag | Description |
|------|-------------|
| `--remove` | Remove the alias(es) for the named playbook |

**Errors:**
- Playbook not found → `unknown playbook "awesome/dba"`
- Shell config cannot be found or written → see Alias Management

---

### `claude-playbook dealias <name>`

Sugar for `claude-playbook alias <name> --remove`. Removes any aliases pointing at the named playbook.

```bash
claude-playbook dealias awesome
claude-playbook dealias awesome/dba
```

**Errors:** identical to `alias <name> --remove`.

---

### `claude-playbook delete <name>`

Deletes a top-level playbook and all of its children. **Children cannot be deleted independently** — to drop a child, edit the parent's `.playbook` (or `dealias` the child to remove just its shell alias).

```bash
claude-playbook delete experiment
claude-playbook delete awesome -y
```

**Confirmation prompt for a top-level with children:**
```
Playbook: awesome
Location: ~/.claude-playbooks/awesome
Alias:    ap (will be removed from ~/.zshrc)
Children: 2
  awesome/dba    (alias: ap-dba — will be removed)
  awesome/sre    (no alias)
Contents: 142 files, 28 directories

Permanently delete? [y/N]
```

**Confirmation prompt for a flat top-level:**
```
Playbook: experiment
Location: ~/.claude-playbooks/experiment
Alias:    experiment (will be removed from ~/.zshrc)
Contents: 12 files, 3 directories

Permanently delete? [y/N]
```

**Deletion scope:**
- The target directory and everything under it (for symlinks: the symlink is removed; the target is preserved).
- All shell aliases pointing into the deleted tree, including child aliases.

**Flags:**

| Flag | Description |
|------|-------------|
| `-y`, `--yes` | Skip the confirmation prompt |

**Errors:**
- Name is a child → `"awesome/dba" is a child playbook; delete the parent "awesome" or run 'claude-playbook dealias awesome/dba' to drop just the alias`
- Name not found → `"experiment" not found under ~/.claude-playbooks`

**Graceful cases:** if the directory is already gone, the command still cleans up any dangling aliases and reports success.

---

### `claude-playbook update [name]`

Updates either the `claude-playbook` tool itself, or a top-level playbook — based on whether a name is given. The tool deliberately does **not** know how to update playbook contents; instead, it delegates to a script that each playbook can provide.

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

#### `claude-playbook update <name>` — update a top-level playbook

Delegates to `<top-level>/bin/update-playbook.sh`. **Children do not have independent updaters.**

```bash
claude-playbook update pai
claude-playbook update awesome
```

**Behaviour:**
1. Resolve the target. Children are rejected.
2. Check `<target>/bin/update-playbook.sh` exists and is executable.
3. Run the script with:
   - Working directory: the top-level playbook directory
   - Environment: inherited, with `CLAUDE_CONFIG_DIR=<target-path>` added
   - Arguments: any remaining command-line arguments are forwarded
4. Forward stdout, stderr, and exit code.

**Why delegated:** playbooks come from many sources with many update strategies. The playbook author writes the right logic for their own distribution.

**Example `bin/update-playbook.sh` for a git-backed playbook:**
```bash
#!/bin/sh
set -e
cd "$(dirname "$0")/.."
git pull --ff-only
```

**Errors:**
- Target is a child → `update operates on top-level playbooks; "awesome/dba" is a child of "awesome". Try: claude-playbook update awesome`
- Target not found → `"pai" not found under ~/.claude-playbooks`
- Update script missing → `"pai" has no update script at bin/update-playbook.sh`
- Script not executable → `update script is not executable: <path>`
- Script exits non-zero → exit code forwarded; `update-playbook.sh exited with code <n>` printed to stderr

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

Playbook name completion is wired for commands that take a name: `run`, `delete`, `info`, `rename`, `alias`, `dealias`, and `update`.

---

## Playbook Manifests

Every top-level playbook has a `.playbook` file at its root. The file's **presence** marks the directory as a playbook; the fields inside are metadata and (for trees) a declaration of children.

`create` writes a minimal `.playbook`. `install` requires one (or `--init`). `link` writes one interactively if the target lacks it.

**Format:**

```toml
version = "1.0.0"
name = "awesome"
alias = "ap"
description = "A collection of useful playbooks"

[[children]]
name = "dba"
path = "playbooks/dba"
alias = "ap-dba"
description = "DBA playbook"

[[children]]
name = "sre"
path = "playbooks/sre"
description = "SRE playbook"
# no alias field → no alias suggested for this child
```

**Top-level fields:**

| Field | Meaning |
|-------|---------|
| `version` | Free-form semver string for the playbook itself. Shown by `info`. |
| `name` | Preferred top-level name. `install` uses it as a default; CLI `--name` overrides. |
| `alias` | Preferred alias for the top-level playbook. |
| `description` | Human-readable description, shown by `info`. |
| `[[children]]` | Array of child playbook declarations (see below). Optional. |

**Per-child fields (under `[[children]]`):**

| Field | Meaning |
|-------|---------|
| `name` | Child name, used in `<top-level>/<child-name>`. Required. |
| `path` | Path to the child's directory, relative to the top-level playbook root. Required. Must exist. |
| `alias` | Suggested alias name. Used only when `--alias-all` is passed or the user runs `claude-playbook alias` explicitly. |
| `description` | Human-readable description, shown by `info`. |

**Forward compatibility:** unknown fields are ignored. Manifest authors may include fields for future tool versions without breaking older installs.

**Errors:**
- Invalid TOML → `invalid .playbook at <path>: <reason>`
- `[[children]]` declares a child whose `path` does not exist → `child "<name>" path "<path>" not found`
- Two `[[children]]` entries with the same `name` → `duplicate child name "<name>" in <path>`

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

Hand-maintained aliases are fully supported; the tool sees them and treats them like any other.

### Updates and removals

- **Set/update:** remove any existing line matching the alias name or any existing line pointing at the playbook path, then append the new alias line.
- **Remove:** delete any line matching the target (by alias name or by playbook path).

If multiple aliases point to the same playbook, they are all reported; removal deletes all of them.

### Manual customization

Because aliases are just shell commands, users can hand-edit them freely:

```bash
alias experiment='CLAUDE_CONFIG_DIR=~/.claude-playbooks/experiment claude --model claude-opus-4-6'
alias ap-dba='CLAUDE_CONFIG_DIR=~/.claude-playbooks/awesome/playbooks/dba claude --permission-mode auto'
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
Error: "awesome" already exists at ~/.claude-playbooks/awesome. Use --name to choose a different name
Error: unknown playbook "typo". Run 'claude-playbook list' to see available playbooks
Error: 'claude' command not found. Install Claude Code first: https://claude.ai/download
Error: <install-path> has no .playbook. Add one to the source, or pass --init.
Error: child "dba" path "playbooks/dba" not found
Error: invalid .playbook at ~/.claude-playbooks/foo/.playbook: toml: line 3: expected '=', got ':'
```
