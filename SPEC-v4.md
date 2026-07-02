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

There is no index file, no database, no registry. The playbooks root directory and the user's shell configuration file are the only state the tool reads from or writes to. Any mutation users make with `mv`, `rm`, or a text editor is immediately consistent with what the tool sees on its next invocation.

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

Recommended characters: lowercase letters, digits, and dashes. Names must not start with `.` (to avoid hidden directories) and must not contain `/` or `\` (names are single directory segments, never paths).

---

## Commands

### `claude-playbook` (no arguments)

Prints a one-line description and lists all discovered playbooks with how to run each.

```
claude-playbook -- manage isolated Claude Code instances

Playbooks directory: ~/.claude-playbooks

Available playbooks:

  experiment    claude-playbook run experiment    (or: experiment)
  sre           claude-playbook run sre           (or: sre)
  dba           claude-playbook run dba           (no alias set)

Run 'claude-playbook --help' for all commands.
```

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
claude-playbook list s
```

**Output:**

```
NAME          PATH                                  ALIAS  LAST USED
----          ----                                  -----  ---------
experiment    ~/.claude-playbooks/experiment        -      2 days ago
sre           ~/.claude-playbooks/sre               sre    1 hour ago
dba           ~/.claude-playbooks/dba               -      never
```

Column widths are computed from the longest NAME, PATH, and ALIAS values, with minimum widths of 4, 4, and 5. `ALIAS` shows `-` when none is set. `LAST USED` is derived from the playbook directory's mtime.

---

### `claude-playbook create <name>`

Creates a new, empty playbook.

```bash
claude-playbook create experiment
claude-playbook create experiment --no-alias
claude-playbook create experiment --alias exp
```

**Steps:**
1. Validate the name (single segment; not empty; no `/` or `\`; must not start with `.`).
2. Check the target directory does not exist.
3. Create the directory.
4. Unless `--no-alias`, write a shell alias. The alias name defaults to the playbook name. Override with `--alias`.

No `.playbook` is written by default. Add one only when you want to set metadata.

**Flags:**

| Flag | Description |
|------|-------------|
| `--alias <alias>` | Use a custom alias name (default: the playbook name) |
| `--no-alias` | Skip alias creation |

`--alias` and `--no-alias` cannot be combined.

**Errors:**
- Name already exists → `playbook "experiment" already exists at ~/.claude-playbooks/experiment`
- Name starts with `.` → `playbook name cannot start with '.'`
- Name contains a slash → `playbook name cannot contain '/'`
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

```bash
# Git repo (derives install name from the URL)
claude-playbook install https://github.com/user/pai

# Git repo with a custom install name
claude-playbook install https://github.com/user/repo --name myrepo

# Install a specific branch/tag
claude-playbook install https://github.com/user/repo --branch dev

# Local directory (symlinked by default)
claude-playbook install ~/dev/my-playbook

# Local directory (copied, becomes independent of source)
claude-playbook install ~/dev/my-playbook --copy

# Install one playbook out of a monorepo (the primary multi-playbook-repo path)
claude-playbook install https://github.com/ramazanpolat/awesome-playbooks/tree/main/playbooks/sre --name sre --alias sre

# Same thing, spelled with explicit flags
claude-playbook install https://github.com/ramazanpolat/awesome-playbooks --subdir playbooks/sre --branch main --name sre --alias sre
```

**Source types:**

| Source | Behaviour |
|--------|-----------|
| URL (`http://`, `https://`, `git@`, `git://`) | Shallow-cloned (`git clone --depth=1`) |
| GitHub `/tree/<ref>/<path>` URL | Recognized and split automatically into clone URL + `--branch <ref>` + `--subdir <path>` (only fills flags you didn't set explicitly) |
| Anything else | Treated as a local filesystem path |

**Flags:**

| Flag | Description |
|------|-------------|
| `--name <name>` | Override the install directory name under the playbooks root |
| `--subdir <path>` | Install only this subdirectory of the source (see below) |
| `--branch <ref>` | Git URL only: clone this branch/tag/ref instead of the default branch |
| `--alias <alias>` | Custom alias name for the installed playbook |
| `--no-alias` | Skip alias creation |
| `--copy` | Copy instead of symlink (local paths only) |

**Steps (no `--subdir`):**
1. Derive the install directory name from `--name`, or the manifest `name`, or the last path segment of the URL (stripped of `.git`), or the source directory's name.
2. Check the target doesn't already exist under the playbooks root.
3. Fetch the source:
   - Git URL → `git clone --depth=1` (with `--branch <ref>` if given) into the target.
   - Local path (default) → symlink target → source.
   - Local path with `--copy` → recursive copy.
4. The installed directory **is** the playbook. If a `.playbook` is present it supplies metadata; if not, the directory is still a valid playbook.
5. Write an alias per the rules below.
6. Print a summary.

`install` never writes a `.playbook` into the source, and never needs one to succeed.

**Steps (with `--subdir <path>`):**
1. Fetch the source as above into a scratch location (for URLs, a temp directory; for local paths, the source itself).
2. Verify `<source>/<path>` exists and is a directory.
3. Copy `<source>/<path>` into `~/.claude-playbooks/<name>/`. For URL sources, the rest of the clone is discarded.
4. Treat the result as one flat playbook. Any `.playbook` already inside `<source>/<path>` provides metadata for that one playbook.
5. Default install name (`--name` not given) is the last segment of `<path>`.

`--subdir` is how you consume a monorepo — a repo laid out as `playbooks/sre`, `playbooks/dba`, `playbooks/frontend`, etc. Each `install --subdir` (or `/tree/<ref>/<path>` URL) copies everything under that one directory into its own playbook. To take several, run several installs; there is intentionally no "install the whole suite at once."

**Default alias behaviour**

One alias is written, using the `--alias` value, or the manifest's `alias` field, or its `name` field, or the install directory name, in that order. `--no-alias` skips it.

**Alias collision handling** (only when writing): if the chosen alias name is already defined in the shell config, it is skipped with a warning; the user can set one manually with `claude-playbook alias <name> <alias>`.

**CLAUDE.md warning:** if the installed playbook has no `CLAUDE.md`, a warning is printed. Claude Code works without one, but most playbooks benefit from having one.

**Errors:**
- `--copy` with a URL → `--copy only applies to local paths. Git installs always clone`
- `--branch` with a local path → `--branch only applies to Git URLs`
- `--subdir` path missing in source → `subdirectory "<path>" not found in source`
- Source not found → `'~/dev/foo' not found`
- Source is a file → `'~/dev/foo' is not a directory`
- Install name already taken → `"myrepo" already exists at ~/.claude-playbooks/myrepo. Use --name to choose a different name`
- `git` not on PATH → `'git' command not found`
- Clone fails → git's error output is shown directly

**Sample output:**
```
Cloning https://github.com/ramazanpolat/awesome-playbooks (branch main)...
Extracted "playbooks/sre" from the source.
Installed "sre" at ~/.claude-playbooks/sre
Alias:  sre added to ~/.zshrc

Reload your shell or run:
  source ~/.zshrc

Then run with:
  sre
```

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
3. Update all shell aliases whose `CLAUDE_CONFIG_DIR=<path>` points at the old location — rewrite them to the new location.

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

### `claude-playbook alias [name] [new-alias]`

Lists or manages shell aliases. **Read-only when given one argument** — no hidden side effects.

```bash
claude-playbook alias                    # list all playbooks and their aliases
claude-playbook alias sre                # show alias for this playbook, or say "none"
claude-playbook alias sre s              # set alias to 's' (creates or replaces)
claude-playbook alias sre --remove       # remove alias
```

**No arguments** — lists all playbooks with the full alias lines from the shell config:

```
experiment    alias experiment='CLAUDE_CONFIG_DIR=~/.claude-playbooks/experiment claude'
sre           alias sre='CLAUDE_CONFIG_DIR=~/.claude-playbooks/sre claude'
dba           (no alias)
```

Showing the full alias line lets users see exactly what will run, including any flags they've added manually.

**One argument, alias exists** — prints it.
```
Alias for "sre": alias sre='CLAUDE_CONFIG_DIR=~/.claude-playbooks/sre claude'
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
Contents: 12 files, 3 directories

Permanently delete? [y/N]
```

**Deletion scope:**
- The target directory (for a symlink, the link is removed; the symlink target is preserved).
- All shell aliases pointing into the deleted directory.

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

#### `claude-playbook update <name>` — update a playbook

Delegates to `<playbook>/bin/update-playbook.sh`. Because playbooks come from many sources with many update strategies, the update logic belongs to the playbook author, not the tool.

```bash
claude-playbook update sre
```

**Behaviour:**
1. Resolve the named playbook.
2. Check `<playbook>/bin/update-playbook.sh` exists and is executable.
3. Run the script with:
   - Working directory: the playbook directory
   - Environment: inherited, with `CLAUDE_PLAYBOOK_TARGET=<name>` and `CLAUDE_PLAYBOOK_PATH=<path>`
   - Arguments: any remaining command-line arguments are forwarded to the script
4. Forward stdout, stderr, and exit code.

**Example `bin/update-playbook.sh` for a git-backed install:**
```bash
#!/bin/sh
set -e
cd "$(dirname "$0")/.."
git pull --ff-only
```

**Errors:**
- Target not found → `"sre" not found under ~/.claude-playbooks`
- Update script missing → `"sre" has no update script at bin/update-playbook.sh. This install does not support updates; see its documentation.`
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

The `.playbook` manifest is **optional** and holds **metadata only**. It never affects discovery, and it never declares other playbooks. A directory without a `.playbook` is a perfectly valid playbook.

**Format:**

```toml
version = "1.0.0"
name = "sre"
alias = "sre"
description = "Site Reliability Engineering assistant"
homepage = "https://github.com/ramazanpolat/awesome-playbooks"
author = "Ramazan Polat"
```

**Fields:**

| Field | Meaning |
|-------|---------|
| `version` | Version of the playbook itself (free-form semver string). Shown by `info`. Not enforced by the tool. |
| `name` | Preferred playbook name. `install` uses it as a suggestion; the actual name is always the install directory name. |
| `alias` | Preferred alias for `install`/`create` to suggest when writing the default alias. |
| `subdir` | Optional. Points at a subdirectory of the install that holds the actual Claude config (CLAUDE.md, settings, hooks). When set, `run`/aliases target `<install>/<subdir>` instead of the install root. Used by repos that keep tool/repo files at the top and the playbook itself one level down. This is unrelated to the `--subdir` install flag. |
| `description` | Human-readable description, shown by `info`. |
| `homepage` | Optional URL, shown by `info`. |
| `author` | Optional author name or contact, shown by `info`. |

**Forward compatibility:** unknown fields are ignored. Manifest authors may include fields for future tool versions without breaking older installs.

**Errors:**
- Invalid TOML → `invalid .playbook at <path>: <reason>`
- `subdir` escapes the install directory (e.g. `../foo`) → `invalid .playbook at <path>: 'subdir' must be relative and stay inside the directory`
- `subdir` does not exist → `~/.claude-playbooks/<name>/.playbook declares subdir "<path>" but the directory is missing`

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
alias sre='CLAUDE_CONFIG_DIR=~/.claude-playbooks/sre claude --permission-mode auto --effort max'
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
Error: subdirectory "playbooks/sre" not found in source
Error: "sre" has no update script at bin/update-playbook.sh. This install does not support updates; see its documentation.
Error: invalid .playbook at ~/.claude-playbooks/foo/.playbook: toml: line 3: expected '=', got ':'
```
