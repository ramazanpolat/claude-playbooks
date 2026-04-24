# claude-playbook CLI Specification

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

There is no index file, no database, no registry. The playbooks root directory and the user's shell configuration file are the only state the tool reads from or writes to. Any mutation users make with `mv`, `rm`, or a text editor is immediately consistent with what the tool sees on its next invocation.

### The playbooks root

All playbooks live under a single **playbooks root** directory. The default is `~/.claude-playbooks/`. This is configurable via the `--playbooks-dir` flag or the `CLAUDE_PLAYBOOKS_DIR` environment variable, applied globally to every command.

### Playbook discovery

A directory is a **playbook** if it contains a `.playbook` file at its root. A directory that is not itself a playbook but contains playbooks one level down is a **container** (typically a cloned repo). The tool treats playbooks and containers identically in terms of storage — both are just directories — and only distinguishes them during discovery.

Discovery walks the playbooks root to a **maximum of two levels**:

- Level 1: a direct child of the playbooks root with a `.playbook` file is a playbook.
- Level 2: a grandchild with a `.playbook` file is a playbook, as long as the parent does not itself have a `.playbook`.

Once a `.playbook` is found at a level, discovery stops descending into that directory. A playbook named `foo` at the top level is never "inside" a playbook named `foo/bar` — the outer one wins and fully shadows anything below.

```
~/.claude-playbooks/
    experiment/               ← playbook (has .playbook)
        .playbook
        CLAUDE.md
    pai/                      ← playbook (single-playbook repo)
        .playbook
        CLAUDE.md
        settings.json
    multi-repo/               ← container (no .playbook at root)
        work/                 ← playbook
            .playbook
            CLAUDE.md
        research/             ← playbook
            .playbook
```

This is the entire storage model. Containers are not a special type; they're just directories that happen to contain playbooks.

### Playbook names

A playbook's **name** is its path relative to the playbooks root, using forward slashes. For the tree above:

- `experiment`
- `pai`
- `multi-repo/work`
- `multi-repo/research`

Names are used wherever a playbook is referenced: `run`, `delete`, `info`, `rename`, `alias`, `update`.

Recommended characters: lowercase letters, digits, and dashes. Names must not start with `.` (to avoid hidden directories). Slashes are path separators, not part of the directory name itself.

---

## Commands

### `claude-playbook` (no arguments)

Prints a one-line description and lists all discovered playbooks with how to run each.

```
claude-playbook -- manage isolated Claude Code instances

Available playbooks:

  experiment          claude-playbook run experiment          (or: experiment)
  pai                 claude-playbook run pai                 (or: pai)
  multi-repo/work     claude-playbook run multi-repo/work     (or: work)
  multi-repo/research claude-playbook run multi-repo/research (no alias set)

Run 'claude-playbook --help' for all commands.
```

The output is intentionally terse and machine-readable.

Empty state:
```
claude-playbook -- manage isolated Claude Code instances

No playbooks found. Run 'claude-playbook create <name>' to get started.
```

---

### `claude-playbook list [prefix]`

Lists all playbooks in a table. If a `prefix` argument is given, only playbooks whose names start with that prefix are shown — useful for exploring a single container.

```bash
claude-playbook list
claude-playbook list multi-repo/
```

**Output:**

```
NAME                  PATH                                            ALIAS       LAST USED
----                  ----                                            -----       ---------
experiment            ~/.claude-playbooks/experiment                  experiment  2 days ago
pai                   ~/.claude-playbooks/pai                         pai         1 hour ago
multi-repo/work       ~/.claude-playbooks/multi-repo/work             work        30 min ago
multi-repo/research   ~/.claude-playbooks/multi-repo/research         -           never
```

Column widths are computed from the longest NAME, PATH, and ALIAS values, with minimum widths of 4, 4, and 5. `ALIAS` shows `-` when none is set. `LAST USED` is derived from the playbook directory's mtime.

---

### `claude-playbook create <name>`

Creates a new, empty playbook.

```bash
claude-playbook create experiment
claude-playbook create experiment --no-alias
claude-playbook create experiment --alias exp
claude-playbook create multi-repo/scratch    # nested under an existing container
```

**Steps:**
1. Validate the name. If it contains slashes, parent directories must already exist, or be creatable (implicitly created as plain container directories, without `.playbook`).
2. Check the target directory does not exist.
3. Create the directory.
4. Write a minimal `.playbook` file:
   ```toml
   version = "0.1.0"
   ```
5. Unless `--no-alias`, write a shell alias. The alias name defaults to the last segment of the playbook name (`scratch` for `multi-repo/scratch`). Override with `--alias`.

**Flags:**

| Flag | Description |
|------|-------------|
| `--alias <alias>` | Use a custom alias name (default: last segment of the playbook name) |
| `--no-alias` | Skip alias creation |

`--alias` and `--no-alias` cannot be combined.

**Errors:**
- Name already exists → `playbook "experiment" already exists at ~/.claude-playbooks/experiment`
- Name starts with `.` → `playbook name cannot start with '.'`
- Both `--alias` and `--no-alias` → `--no-alias and --alias cannot be used together`

---

### `claude-playbook run <name> [claude-flags...]`

Runs Claude Code using the named playbook. Any flags after the name are forwarded to `claude` unchanged.

```bash
claude-playbook run experiment
claude-playbook run multi-repo/work
claude-playbook run pai --model claude-opus-4-6
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

Installs playbooks from a Git repository or a local directory. The source becomes a direct child of the playbooks root. If it contains a `.playbook` at its root, the source itself is one playbook. If it contains `.playbook`-marked directories one level down, it's a container for multiple playbooks. If neither, a `.playbook` file is written at the root, treating the whole thing as a single playbook.

```bash
# Git repo (derives container name from the URL)
claude-playbook install https://github.com/user/pai

# Git repo with a custom directory name
claude-playbook install https://github.com/user/repo --name myrepo

# Local directory (symlinked by default)
claude-playbook install ~/dev/my-playbook

# Local directory (copied, becomes independent of source)
claude-playbook install ~/dev/my-playbook --copy

# Multi-playbook repo, opt into aliases for all of them
claude-playbook install https://github.com/user/multi-repo --alias-all

# Skip alias creation regardless
claude-playbook install https://github.com/user/repo --no-alias
```

**Source types:**

| Source | Behaviour |
|--------|-----------|
| URL (`http://`, `https://`, `git@`, `git://`) | Shallow-cloned (`git clone --depth=1`) |
| Anything else | Treated as a local filesystem path |

**Flags:**

| Flag | Description |
|------|-------------|
| `--name <name>` | Override the top-level directory name under the playbooks root |
| `--alias <alias>` | Single-playbook case only: custom alias name. Error if the source contains multiple playbooks. |
| `--alias-all` | Multi-playbook case: write an alias for every discovered playbook |
| `--no-alias` | Skip alias creation |
| `--copy` | Copy instead of symlink (local paths only) |

**Steps:**
1. Derive the target directory name from `--name`, or from the last path segment of the URL (stripped of `.git`), or the source directory's name.
2. Check the target doesn't already exist under the playbooks root.
3. Fetch the source:
   - Git URL → `git clone --depth=1` into the target.
   - Local path (default) → symlink target → source.
   - Local path with `--copy` → recursive copy.
4. Walk the installed tree for playbooks (2-level rule). Each directory containing `.playbook` is a playbook.
5. If no `.playbook` is found anywhere, write a minimal one at the root of the install. The installed source becomes one playbook.
6. Write aliases per the default rules below.
7. Print a summary.

**Default alias behaviour**

- **One playbook discovered** → one alias is written, using either the `alias` field from the playbook's `.playbook` manifest, or the `name` field, or the last segment of the playbook's name, in that order. Override with `--alias`.
- **Multiple playbooks discovered** → no aliases are written. The tool prints the list with next steps. Use `--alias-all` to opt into writing one alias per playbook.

`--no-alias` suppresses alias writing in both cases.

**Alias collision handling** (only when writing):

- Between playbooks in this install (two playbooks whose default alias names collide): the colliding aliases are prefixed with the container name, e.g. `multi-repo-work`.
- With existing shell aliases: the conflicting alias is skipped with a warning; the user can set it manually with `claude-playbook alias <name> <alias>`.

**CLAUDE.md warning:** for any discovered playbook missing a `CLAUDE.md`, a warning is printed. Claude Code works without one, but most playbooks benefit from having one.

**Errors:**
- `--copy` with a URL → `--copy only applies to local paths. Git installs always clone`
- `--alias` with multiple playbooks discovered → `--alias accepts a single name; this install produced N playbooks. Use --alias-all or add aliases with 'claude-playbook alias'`
- Source not found → `'~/dev/foo' not found`
- Source is a file → `'~/dev/foo' is not a directory`
- Name already taken → `"myrepo" already exists at ~/.claude-playbooks/myrepo. Use --name to choose a different name`
- `git` not on PATH → `'git' command not found`
- Clone fails → git's error output is shown directly

**Sample output (single-playbook install):**
```
Cloning https://github.com/user/pai...
Installed "pai" at ~/.claude-playbooks/pai
Found 1 playbook:
  pai
Alias:  pai added to ~/.zshrc

Reload your shell or run:
  source ~/.zshrc

Then run with:
  pai
```

**Sample output (multi-playbook install, no aliases):**
```
Cloning https://github.com/user/multi-repo...
Installed "multi-repo" at ~/.claude-playbooks/multi-repo
Found 3 playbooks:
  multi-repo/work
  multi-repo/research
  multi-repo/personal

No aliases created. Add ones you want:
  claude-playbook alias multi-repo/work work
  claude-playbook alias multi-repo/research r

Or run without an alias:
  claude-playbook run multi-repo/work
```

---

### `claude-playbook info <name>`

Shows detailed information about a playbook.

```bash
claude-playbook info experiment
claude-playbook info multi-repo/work
```

**Output:**
```
Name:        multi-repo/work
Version:     1.2.0
Path:        ~/.claude-playbooks/multi-repo/work
Type:        directory
Alias:       work
Size:        24 files, 3 directories
Last used:   2 hours ago
Description: Work configuration
Updater:     bin/update-playbook.sh
```

**Fields:**

| Field | Meaning |
|-------|---------|
| `Name` | Playbook name (path relative to playbooks root) |
| `Version` | `version` field from the `.playbook` manifest, if set |
| `Path` | Absolute path to the playbook directory |
| `Type` | `directory`, `symlink → <target>`, or `symlink → <target> (BROKEN)` |
| `Alias` | Shell alias for this playbook, or `(none)` |
| `Size` | File and directory counts from walking the playbook |
| `Last used` | Human-readable time since the directory was last modified |
| `Description` | From `.playbook` manifest, if present |
| `Updater` | `bin/update-playbook.sh` if it exists and is executable, else `(none)` |

**Errors:**
- Playbook not found → `unknown playbook "experiment"`

---

### `claude-playbook rename <old-name> <new-name>`

Renames a playbook directory (or a container) and updates affected aliases.

```bash
claude-playbook rename experiment exp-1
claude-playbook rename multi-repo/work multi-repo/w   # rename within a container
claude-playbook rename multi-repo big-repo            # rename a container
```

**Steps:**
1. Validate the old name exists and the new name doesn't.
2. Rename the directory with `mv`.
3. Update all shell aliases whose `CLAUDE_CONFIG_DIR=<path>` points into the old location — rewrite them to point at the new location.

**Renaming a container:** any aliases that pointed at playbooks inside the old container are updated to point at the new container path. The container itself has no alias; only the playbooks inside do.

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
claude-playbook alias                              # list all playbooks and their aliases
claude-playbook alias multi-repo/work              # show alias for this playbook, or say "none"
claude-playbook alias multi-repo/work w            # set alias to 'w' (creates or replaces)
claude-playbook alias multi-repo/work --remove     # remove alias
```

**No arguments** — lists all playbooks with the full alias lines from the shell config:

```
experiment          alias experiment='CLAUDE_CONFIG_DIR=~/.claude-playbooks/experiment claude'
pai                 alias pai='CLAUDE_CONFIG_DIR=~/.claude-playbooks/pai claude'
multi-repo/work     alias w='CLAUDE_CONFIG_DIR=~/.claude-playbooks/multi-repo/work claude --model claude-opus-4-6'
multi-repo/research (no alias)
```

Showing the full alias line lets users see exactly what will run, including any flags they've added manually.

**One argument, alias exists** — prints it.
```
Alias for "multi-repo/work": alias w='CLAUDE_CONFIG_DIR=~/.claude-playbooks/multi-repo/work claude'
```

**One argument, no alias** — reports only; does **not** create one.
```
Playbook "multi-repo/work" has no alias set.
Use 'claude-playbook alias multi-repo/work <alias-name>' to create one.
```

**Two arguments** — sets the alias (creates or replaces).

**With `--remove`** — removes any aliases pointing at this playbook. No-op with a message if none exist.

**Flags:**

| Flag | Description |
|------|-------------|
| `--remove` | Remove the alias(es) for the named playbook |

**Errors:**
- Playbook not found → `unknown playbook "multi-repo/work"`
- Shell config cannot be found or written → see Alias Management

---

### `claude-playbook delete <name>`

Deletes a playbook or an entire container.

```bash
claude-playbook delete experiment                  # prompts
claude-playbook delete multi-repo/work -y          # just one playbook
claude-playbook delete multi-repo                  # the whole container; lists playbooks first
```

**Confirmation prompt for a single playbook:**
```
Playbook: multi-repo/work
Location: ~/.claude-playbooks/multi-repo/work
Alias:    w (will be removed from ~/.zshrc)
Contents: 12 files, 3 directories

Permanently delete? [y/N]
```

**Confirmation prompt for a container** (a directory that holds multiple playbooks):
```
Container: multi-repo
Location:  ~/.claude-playbooks/multi-repo
Contains 3 playbooks:
  multi-repo/work        (alias: w)
  multi-repo/research    (no alias)
  multi-repo/personal    (alias: pers)
Total:     142 files, 28 directories

Permanently delete container and all playbooks inside? [y/N]
```

**Deletion scope:**
- The target directory (symlink removed; target preserved).
- All shell aliases pointing into the deleted tree.

**Flags:**

| Flag | Description |
|------|-------------|
| `-y`, `--yes` | Skip the confirmation prompt |

**Errors:**
- Name not found → `"experiment" not found under ~/.claude-playbooks`

**Graceful cases:** if the directory is already gone, the command still cleans up any dangling aliases and reports success.

---

### `claude-playbook update [name]`

Updates either the `claude-playbook` tool itself, or a specific playbook — based on whether a name is given. The tool deliberately does **not** know how to update playbook contents; instead, it delegates to a script that each playbook (or container) can provide.

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

#### `claude-playbook update <name>` — update a playbook or container

Delegates to `<target>/bin/update-playbook.sh`. The target can be either a playbook or a container.

```bash
claude-playbook update pai               # updates the pai playbook
claude-playbook update multi-repo        # updates the container (the whole repo)
claude-playbook update multi-repo/work   # updates just one playbook inside
```

**Behaviour:**
1. Resolve the target directory (playbook or container).
2. Check `<target>/bin/update-playbook.sh` exists and is executable.
3. Run the script with:
   - Working directory: the target directory
   - Environment: inherited, with `CLAUDE_CONFIG_DIR=<target-path>` added (applies even for containers — the script can ignore it)
   - Arguments: any remaining command-line arguments are forwarded to the script
4. Forward stdout, stderr, and exit code.

**Why delegated:** playbooks and repos come from many sources with many update strategies. A one-size-fits-all strategy is wrong for most. The playbook or repo author writes the right logic for their own distribution.

**Example `bin/update-playbook.sh` for a git-backed repo:**
```bash
#!/bin/sh
set -e
cd "$(dirname "$0")/.."
git pull --ff-only
```

**Errors:**
- Target not found → `"pai" not found under ~/.claude-playbooks`
- Update script missing → `"pai" has no update script at bin/update-playbook.sh. This target does not support updates; see its documentation.`
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

## Playbook Manifests

Every playbook has a `.playbook` file at its root. The file's **presence** marks the directory as a playbook; the fields inside are all optional defaults and metadata.

`create` writes a minimal `.playbook` automatically. `install` auto-creates one at the install root if none is found during discovery. Playbook authors can hand-write richer manifests to provide better defaults and metadata.

**Format:**

```toml
version = "1.0.0"
name = "pai"
alias = "pai"
description = "Personal AI Infrastructure by Daniel Miessler"
```

**Fields:**

| Field | Meaning |
|-------|---------|
| `version` | Version of the playbook itself (free-form semver string). Shown by `info`. The tool does not enforce or compare it; it's for the playbook author and for `bin/update-playbook.sh` to inspect. |
| `name` | Preferred playbook name. `install` uses it as a suggestion but the actual name is always the path relative to the playbooks root. Shown in `info`. |
| `alias` | Preferred alias for `install` to suggest when writing the default alias. |
| `description` | Human-readable description, shown by `info`. |

**Forward compatibility:** unknown fields are ignored. Manifest authors may include fields for future tool versions without breaking older installs.

**Errors:**
- Invalid TOML → `invalid .playbook at <path>: <reason>`

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
alias work='CLAUDE_CONFIG_DIR=~/.claude-playbooks/multi-repo/work claude --permission-mode auto --effort max'
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
Error: 'claude' command not found. Install Claude Code first: https://claude.ai/download
Error: "pai" has no update script at bin/update-playbook.sh. This target does not support updates; see its documentation.
Error: invalid .playbook at ~/.claude-playbooks/foo/.playbook: toml: line 3: expected '=', got ':'
```
