# claude-playbook CLI Spec

## Overview

`claude-playbook` is a CLI tool for creating and managing Claude Code playbooks. A playbook is an isolated Claude Code instance — a directory used as `CLAUDE_CONFIG_DIR`.

All playbooks live under `~/.claude-playbooks/` by default.

---

## Naming Convention

Playbook names can be any string that is valid as a directory name and a shell alias name. Recommended style: lowercase letters, numbers, and dashes (e.g. `work`, `hook-test`, `team-alpha`). Not enforced, but keeps names shell-friendly.

---

## Storage

```
~/.claude-playbooks/
    <name>/          # one directory per playbook
```

The filesystem is the source of truth. Any directory directly under `~/.claude-playbooks/` is a playbook. There is no index file. Aliases are tracked via comment markers in the shell config (see [Alias Management](#alias-management)).

---

## Commands

### `claude-playbook` (no arguments)

Prints a one-line description and lists all playbooks with how to run each one.

```
claude-playbook -- manage isolated Claude Code instances

Available playbooks:

  experiment    claude-playbook run experiment    (or: experiment)
  work          claude-playbook run work          (or: k-work)
  scratch       claude-playbook run scratch       (no alias set)

Run 'claude-playbook --help' for all commands.
```

- If no playbooks exist: `No playbooks found. Run 'claude-playbook create <name>' to get started.`
- Output is intentionally terse and machine-readable so agents can parse it.

---

### `claude-playbook list`

Lists all playbooks in a table.

```
NAME          PATH                               ALIAS       LAST USED
----          ----                               -----       ---------
experiment    ~/.claude-playbooks/experiment     experiment  2 days ago
work          ~/.claude-playbooks/work           work        1 hour ago
scratch       ~/.claude-playbooks/scratch        -           never
```

- `ALIAS` shows `-` if no alias is set.
- `LAST USED` is derived from the directory mtime. Shows `never` if never accessed, `just now` if under a minute ago.
- If empty or no playbooks dir: `No playbooks found. Run 'claude-playbook create <name>' to get started.`

---

### `claude-playbook create <name>`

Creates a new playbook.

```bash
claude-playbook create experiment
claude-playbook create experiment --no-alias
claude-playbook create experiment --alias exp
```

**Steps:**
1. Check `~/.claude-playbooks/<name>` does not already exist.
2. Create the directory.
3. Unless `--no-alias`, write an alias to the shell config. Default alias name equals the playbook name.
4. Print confirmation and next steps.

**Flags:**

| Flag | Description |
|------|-------------|
| `--alias <alias>` | Use a different alias name |
| `--no-alias` | Skip alias creation |

**Edge cases:**

- `--no-alias` and `--alias` together → error
- Name already exists → error: `playbook "experiment" already exists at ~/.claude-playbooks/experiment`

**Output on success (with alias):**
```
Created playbook "experiment" at ~/.claude-playbooks/experiment
Alias "experiment" added to ~/.zshrc

Reload your shell or run:
  source ~/.zshrc

Then run with:
  experiment
```

**Output on success (--no-alias):**
```
Created playbook "experiment" at ~/.claude-playbooks/experiment

Run with:
  claude-playbook run experiment
```

---

### `claude-playbook run <name> [claude-flags...]`

Runs Claude Code using the named playbook. Any flags after the name are forwarded directly to `claude`.

```bash
claude-playbook run experiment
claude-playbook run experiment --model claude-opus-4-6
claude-playbook run experiment --permission-mode auto --effort max
```

Equivalent to:
```bash
CLAUDE_CONFIG_DIR=~/.claude-playbooks/experiment claude [claude-flags...]
```

Uses `DisableFlagParsing` internally to pass all flags through. Root flags (`--playbooks-dir`, `--shell-config`) are extracted manually before forwarding.

**Edge cases:**

- Playbook not found → error: `unknown playbook "experiment". Run 'claude-playbook list' to see available playbooks`
- `claude` not on PATH → error: `'claude' command not found. Install Claude Code first: https://claude.ai/download`
- Unknown flags → forwarded to `claude` as-is

---

### `claude-playbook start <path> [claude-flags...]`

Starts an ad-hoc Claude Code session at any directory. The directory is created if it does not exist. Any flags after the path are forwarded directly to `claude`.

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
| `--delete` | Delete the directory after the session ends |

`--delete` runs after `claude` exits regardless of exit code. Best-effort: if deletion fails, prints a warning to stderr but does not change the exit code.

**Edge cases:**

- Path exists but is a file → error: `"/tmp/foo" is not a directory`
- Directory cannot be created → error: `could not create "/tmp/foo": <reason>`
- No path given → error: `path required`
- `claude` not on PATH → error: same as `run`

---

### `claude-playbook install <source>`

Installs a playbook from a Git repository or local directory.

```bash
# Git repo
claude-playbook install https://github.com/user/repo --name mypb

# Git repo, select named entry from .playbook manifest
claude-playbook install https://github.com/user/repo --playbook work

# Local directory (symlink by default)
claude-playbook install ~/dev/my-playbook
claude-playbook install ~/dev/my-playbook --name mypb --alias mp

# Local directory (copy)
claude-playbook install ~/dev/my-playbook --copy
```

**Source types:**

| Source | Behaviour |
|--------|-----------|
| URL (`http://`, `https://`, `git@`, `git://`) | Shallow-cloned with `git clone --depth=1` |
| Local path | Symlinked by default; `--copy` to copy instead |

**Flags:**

| Flag | Description |
|------|-------------|
| `--name <name>` | Override playbook name |
| `--alias <alias>` | Override alias (default: same as name) |
| `--no-alias` | Skip alias creation |
| `--copy` | Copy instead of symlink (local paths only) |
| `--playbook <name>` | Select a `[[playbook]]` entry by name from `.playbook` |
| `--subdir <path>` | Use this subdirectory as the playbook root |

**Flag precedence (highest to lowest):** CLI flags → manifest fields → derived from source.

**Name derivation (when not set by manifest or `--name`):**
- Git URL: last path segment stripped of `.git` (e.g. `Personal_AI_Infrastructure`)
- Local path: directory name as-is

**Edge cases:**

- `--no-alias` and `--alias` together → error
- `--copy` with a URL → error: `--copy only applies to local paths. Git installs always clone`
- Source not found → error: `'~/dev/my-playbook' not found`
- Source is a file → error: `'~/dev/my-playbook' is not a directory`
- `--subdir` not found in source → error: `subdirectory 'foo' not found in source`
- Name already taken → error: `playbook "pai" already exists. Use --name to choose a different name`
- `git` not on PATH → error: `'git' command not found`
- Clone fails → git's error output shown directly
- No `CLAUDE.md` found → warning to stderr (install still succeeds)

**Output on success:**
```
Installed playbook "pai"
Source:   https://github.com/user/repo (cloned)
Manifest: .playbook
Path:     ~/.claude-playbooks/pai
Alias:    pai added to ~/.zshrc

Reload your shell or run:
  source ~/.zshrc

Then run with:
  pai
```

`Manifest: .playbook` line is omitted when no `.playbook` file was present.

---

### `claude-playbook alias [name] [alias]`

Shows all aliases, or shows/sets/removes the alias for a specific playbook.

```bash
claude-playbook alias                         # show all playbooks and their aliases
claude-playbook alias experiment              # show alias, or create one using the playbook name
claude-playbook alias experiment exp          # set alias to 'exp'
claude-playbook alias experiment --remove     # remove alias from shell config
```

**No arguments** — shows the full alias line for every playbook:

```
experiment  alias experiment='CLAUDE_CONFIG_DIR=~/.claude-playbooks/experiment claude'
work        alias k-work='CLAUDE_CONFIG_DIR=~/.claude-playbooks/work claude --model claude-opus-4-6'
scratch     (no alias)
```

**One argument, alias exists** — prints it:
```
Alias for "experiment": alias experiment='CLAUDE_CONFIG_DIR=~/.claude-playbooks/experiment claude'
```

**One argument, no alias** — creates one using the playbook name:
```
Alias "experiment" created for playbook "experiment" in ~/.zshrc
```

**Two arguments** — sets the alias:
```
Alias "exp" set for playbook "experiment" in ~/.zshrc
```

**`--remove`** — removes the alias. No-op if no alias is set:
```
Playbook "experiment" has no alias set.
```

**Flags:**

| Flag | Description |
|------|-------------|
| `--remove` | Remove alias from shell config |

**Edge cases:**

- Playbook not found → error: `unknown playbook "experiment". Run 'claude-playbook list' to see available playbooks`
- Shell config not found → error with instructions to use `--shell-config`

---

### `claude-playbook delete <name>`

Deletes the playbook directory and removes its alias from the shell config.

```bash
claude-playbook delete experiment      # prompts for confirmation
claude-playbook delete experiment -y   # skip confirmation
```

**Confirmation prompt:**
```
Playbook: experiment
Location: ~/.claude-playbooks/experiment
Alias:    experiment (will be removed from ~/.zshrc)
Contents: 12 files, 3 directories

Permanently delete? [y/N]
```

If the playbook is a symlink, the symlink is removed but the target is left intact.

**Flags:**

| Flag | Description |
|------|-------------|
| `-y`, `--yes` | Skip confirmation prompt |

**Edge cases:**

- Playbook not found → error: `unknown playbook "experiment". Run 'claude-playbook list' to see available playbooks`
- No alias → skip alias cleanup silently, still delete the directory
- Directory already missing → skip deletion silently, still clean up alias if present

---

## Playbook Manifests

A `.playbook` file is a TOML file at the root of a repo or directory. It defines one or more playbook entries using TOML array-of-tables syntax.

**Single entry:**
```toml
[[playbook]]
subdir = "config"
description = "My playbook"
```

**Multiple entries:**
```toml
[[playbook]]
name = "work"
alias = "work"
subdir = "configs/work"
description = "Work configuration"

[[playbook]]
name = "personal"
alias = "personal"
subdir = "configs/personal"
description = "Personal configuration"
```

All fields are optional.

| Field | Description |
|-------|-------------|
| `name` | Playbook name (default: derived from source) |
| `alias` | Shell alias (default: same as name) |
| `subdir` | Subdirectory to use as the playbook root |
| `description` | Human-readable description |

**Manifest resolution:**

1. No `.playbook` file → install the source root (or `--subdir` if given) directly.
2. `.playbook` exists, no `--playbook` flag → use the first `[[playbook]]` entry.
3. `.playbook` exists, `--playbook <name>` given → find the entry with matching `name`; error if not found, listing available names.

**CLI flags always override manifest fields.**

**Edge cases:**

- `--playbook <name>` but no matching entry → error: `no playbook "work" in .playbook. Available: personal`
- `.playbook` exists but has no `[[playbook]]` entries → error: `.playbook has no [[playbook]] entries`
- Invalid TOML → error: `invalid .playbook: <reason>`

---

## Alias Management

Aliases are written to and removed from the user's shell config file.

**Shell detection order:**
1. `--shell-config <path>` flag
2. `$CLAUDE_SHELL_CONFIG` environment variable
3. `$SHELL` → `zsh` → `~/.zshrc`; `bash` → `~/.bashrc`
4. If undetectable → error: `Could not find shell config. Use --shell-config <path> to specify one.`

**Format of generated alias:**
```bash
# claude-playbook: <name>
alias <alias>='CLAUDE_CONFIG_DIR=~/.claude-playbooks/<name> claude'
```

The comment line `# claude-playbook: <name>` is the marker used to find and remove the alias. Do not edit or remove it manually.

**Updating an alias:** finds the existing block by the comment line and replaces it in-place. Never appends a duplicate.

**Detection of unmanaged aliases:** if a playbook has no comment-marked alias, the tool scans all `alias` lines in the shell config for `CLAUDE_CONFIG_DIR=<path>` where `<path>` resolves to the playbook directory. This handles aliases created by other tools or manually.

---

## Global Flags

These flags work on all commands:

| Flag | Description |
|------|-------------|
| `--playbooks-dir <path>` | Use this directory instead of `~/.claude-playbooks` |
| `--shell-config <path>` | Use this file instead of the auto-detected shell config |
| `--version` | Show the version |
| `--help` | Show help |

Environment variable equivalents:

| Variable | Equivalent flag |
|----------|----------------|
| `CLAUDE_PLAYBOOKS_DIR` | `--playbooks-dir` |
| `CLAUDE_SHELL_CONFIG` | `--shell-config` |

---

## Error Conventions

- All errors print to stderr via cobra's default error handling.
- Exit code `0` = success, non-zero = failure.
- Error messages are plain English, one line, no stack traces. Always suggest a next action where possible.

```
Error: playbook "experiment" already exists at ~/.claude-playbooks/experiment
Error: unknown playbook "typo". Run 'claude-playbook list' to see available playbooks
Error: 'claude' command not found. Install Claude Code first: https://claude.ai/download
```
