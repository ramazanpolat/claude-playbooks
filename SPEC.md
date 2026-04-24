# claude-playbook CLI Spec

## Overview

`claude-playbook` is a CLI tool for creating and managing Claude Code playbooks. A playbook is an isolated Claude Code instance -- a directory pointed to by `CLAUDE_CONFIG_DIR`.

All playbooks are stored under `~/.claude-playbooks/` by default.

---

## Naming Convention

Playbook names and aliases can be any string that is valid as a directory name on the OS and a shell alias name. There are no enforced restrictions beyond that.

Recommended style: lowercase letters, numbers, and dashes (e.g., `work`, `hook-test`, `team-alpha`). This keeps names shell-friendly and consistent, but it is not required.

---

## Storage

```
~/.claude-playbooks/
    <name>/          # One directory per playbook
```

The filesystem is the source of truth. Any directory directly under `~/.claude-playbooks/` is a playbook. There is no separate index file. Alias and PATH entries are tracked via comment markers in the shell config (see [Alias Management](#alias-management)).

---

## Commands

### `claude-playbook` (no arguments)

Prints a one-line description of the tool, lists all local playbooks, and shows how to run each one.

```
claude-playbook -- manage isolated Claude Code instances

Available playbooks:

  experiment    claude-playbook run experiment    (or: experiment)
  work          claude-playbook run work          (or: k-work)
  scratch       claude-playbook run scratch       (no alias set)

Run 'claude-playbook --help' for all commands.
```

- If no playbooks exist: print the description and `No playbooks found. Run 'claude-playbook create <name>' to get started.`
- This output is intentionally terse and machine-readable so agents can parse it to discover and invoke playbooks.

---

### `claude-playbook list`

Lists all registered playbooks.

```
NAME          PATH                               ALIAS      LAST USED
experiment    ~/.claude-playbooks/experiment     experiment  2 days ago
work          ~/.claude-playbooks/work           work        1 hour ago
scratch       ~/.claude-playbooks/scratch        -           never
```

- `ALIAS` shows `-` if no alias is set in the shell config.
- `LAST USED` is derived from the playbook directory's `mtime`. Shows `never` if the directory has never been accessed.
- If `~/.claude-playbooks/` is empty or does not exist: print `No playbooks found. Run 'claude-playbook create <name>' to get started.`

---

### `claude-playbook create <name>`

Creates a new playbook.

```bash
claude-playbook create experiment
claude-playbook create experiment --no-alias
claude-playbook create experiment --alias exp
```

**Steps:**
1. Validate `name` against naming rules.
2. Check `~/.claude-playbooks/<name>` does not already exist.
3. Create the directory.
4. Unless `--no-alias` is given, add an alias to the shell config (see [Alias Management](#alias-management)). Default alias name equals the playbook name.
5. Print confirmation and next steps.

**Flags:**

| Flag | Description |
|------|-------------|
| `--no-alias` | Skip alias creation |
| `--alias <alias>` | Use a different alias name instead of the playbook name |

**Edge cases:**

- Name already exists as a directory → error: `Playbook 'experiment' already exists at ~/.claude-playbooks/experiment`
- Alias name already exists in shell config (pointing to something else) → warn and ask for confirmation before overwriting
- Alias name is an existing shell built-in or command on PATH → warn: `Alias 'ls' shadows an existing command. Continue? [y/N]`
- `--no-alias` and `--alias` given together → error

**Output on success:**
```
Created playbook 'experiment' at ~/.claude-playbooks/experiment
Alias 'experiment' added to ~/.zshrc

Reload your shell or run:
  source ~/.zshrc

Then run with:
  experiment
```

---

### `claude-playbook run <name> [claude-flags...]`

Runs Claude Code using the named playbook without requiring an alias. Any flags after the playbook name are forwarded directly to `claude`.

Useful for temporary or one-off playbooks where setting up an alias is not worth it.

```bash
# Basic run
claude-playbook run experiment

# Forward flags to claude
claude-playbook run experiment --model claude-opus-4-6
claude-playbook run experiment --permission-mode auto --effort max
claude-playbook run experiment --model claude-haiku-4-5-20251001 --permission-mode auto
```

All of the above are equivalent to:
```bash
CLAUDE_CONFIG_DIR=~/.claude-playbooks/experiment claude [claude-flags...]
```

**Edge cases:**

- Playbook not found (no directory at `~/.claude-playbooks/experiment`) → error: `Unknown playbook 'experiment'. Run 'claude-playbook list' to see available playbooks.`
- `claude` not on PATH → error: `'claude' command not found. Install Claude Code first: https://claude.ai/download`
- Unknown flag passed → forwarded to `claude` as-is; if `claude` rejects it, its error is shown directly

---

### `claude-playbook start <path> [claude-flags...]`

Starts an ad-hoc Claude Code session at the given directory. Unlike `run`, no named playbook is required — any directory works. The directory is created if it does not exist.

```bash
# Start a session at a directory
claude-playbook start /tmp/scratch

# Pass flags to claude
claude-playbook start /tmp/scratch --model claude-opus-4-6

# Delete the directory when the session ends
claude-playbook start /tmp/scratch --delete
```

All of the above are equivalent to:
```bash
CLAUDE_CONFIG_DIR=/tmp/scratch claude [claude-flags...]
```

**Flags:**

| Flag | Description |
|------|-------------|
| `--delete` | Delete the directory when the claude session ends |

**`--delete` behavior:**
- Runs cleanup after `claude` exits, regardless of exit code.
- Best-effort: if deletion fails, prints a warning to stderr but does not change the exit code.

**Edge cases:**

- Path exists but is a file → error: `'/tmp/foo' is not a directory`
- Directory cannot be created → error: `could not create '/tmp/foo': <reason>`
- `claude` not on PATH → error: `'claude' command not found. Install Claude Code first: https://claude.ai/download`
- Unknown flags → forwarded to `claude` as-is

---

### `claude-playbook alias <name> [alias]`

Shows all aliases, or shows/sets/removes the alias for a specific playbook.

```bash
claude-playbook alias                         # list all playbooks and their aliases
claude-playbook alias experiment              # show existing alias, or create one using the playbook name if none exists
claude-playbook alias experiment exp          # set alias to 'exp'
claude-playbook alias experiment --remove     # remove alias from shell config
```

**`claude-playbook alias` with no arguments** shows the full alias definition as it appears in the shell config for every playbook that has one:

```
experiment    alias experiment='CLAUDE_CONFIG_DIR=~/.claude-playbooks/experiment claude'
work          alias k-work='CLAUDE_CONFIG_DIR=~/.claude-playbooks/work claude --model claude-opus-4-6 --permission-mode auto'
scratch       (no alias)
```

This lets users see exactly what command runs when they type an alias, including any flags they may have added manually.

**Edge cases:**

- Playbook not found → error: `Unknown playbook 'experiment'.`
- New alias name conflicts with existing alias for a different playbook → error: `Alias 'exp' is already used by playbook 'other'. Use --force to override.`
- New alias name conflicts with a command on PATH → warn and confirm
- `--remove` when no alias is set → no-op: `Playbook 'experiment' has no alias set.`
- Shell config file not found → error with instructions to create it

**Behavior of `claude-playbook alias <name>` with no alias argument:**
- Alias exists → print it: `Alias for 'experiment': experiment`
- No alias → create one using the playbook name, print confirmation and reload reminder

**On success (when creating or updating):** prints the alias line and reminds the user to reload their shell.

---

### `claude-playbook install <source>`

Installs a playbook from a Git repository or a local directory.

```bash
# Git repo — auto-detects playbook manifest
claude-playbook install https://github.com/danielmiessler/Personal_AI_Infrastructure --name pai

# Git repo with multiple playbooks — pick one by name
claude-playbook install https://github.com/user/repo --playbook work
claude-playbook install https://github.com/user/repo --playbook work --name my-work --alias mw

# Local directory (symlink by default)
claude-playbook install ~/dev/my-playbook
claude-playbook install ~/dev/my-playbook --copy
claude-playbook install ~/dev/my-playbook --name mypb --alias mp
```

**Source types:**

| Source | Behaviour |
|--------|-----------|
| URL (`http://`, `https://`, `git@`) | Cloned, then manifest resolved |
| Local path | Symlinked by default; `--copy` to copy instead |

**Flags:**

| Flag | Description |
|------|-------------|
| `--playbook <name>` | Select `<name>.playbook` from the source root |
| `--subdir <path>` | Use this subdirectory as the playbook root (when no manifest) |
| `--name <name>` | Override playbook name |
| `--alias <alias>` | Override alias (default: same as name) |
| `--no-alias` | Skip alias creation |
| `--copy` | Copy instead of symlink (local paths only) |

---

## Playbook Manifests

A `.playbook` file is a TOML file at the root of a repo or directory that describes how to install a playbook. It can define one or more playbook entries using TOML array-of-tables syntax.

**File format:**

```toml
[[playbook]]
name = "pai"
alias = "pai"
subdir = "playbook"
description = "Personal AI Infrastructure by Daniel Miessler"
```

All fields are optional. Any field can be overridden by a CLI flag.

| Field | Description |
|-------|-------------|
| `name` | Playbook name (default: derived from source) |
| `alias` | Shell alias (default: same as name) |
| `subdir` | Subdirectory within the repo/directory to use as the playbook root |
| `description` | Human-readable description |

**A repo with multiple playbook configurations:**

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

**Manifest resolution:**

1. No `.playbook` file → install the source root (or `--subdir` if given) directly as the playbook.
2. `.playbook` exists, no `--playbook` flag → use the first `[[playbook]]` entry.
3. `.playbook` exists, `--playbook <name>` given → find the entry whose `name` field matches; error if not found, listing available names.

**CLI flags always override manifest fields.**

---

**Name derivation (when not set by manifest or `--name`):**
- Git URL: last path segment, stripped of `.git` suffix (e.g., `Personal_AI_Infrastructure` from `.../Personal_AI_Infrastructure.git`)
- Local path: directory name as-is

**Edge cases:**

- `--playbook <name>` given but no matching entry → error: `no playbook "work" in .playbook. Available: personal`
- `.playbook` exists but has no `[[playbook]]` entries → error: `.playbook has no [[playbook]] entries`
- Invalid TOML → error: `invalid .playbook: <reason>`
- Name already taken → error: `Playbook 'pai' already exists. Use --name to choose a different name.`
- `--copy` given with a URL → error: `--copy only applies to local paths. Git installs always clone.`
- `--subdir` path not found inside source → error: `Subdirectory 'playbook' not found in source.`
- Git not on PATH → error: `'git' command not found.`
- Clone fails → show git's error output directly
- Source path does not exist → error: `'~/dev/my-playbook' not found.`
- Source is a file, not a directory → error: `'~/dev/my-playbook' is not a directory.`

**Output on success:**
```
Installed playbook 'pai'
Source:   https://github.com/danielmiessler/Personal_AI_Infrastructure (cloned)
Manifest: .playbook
Path:     ~/.claude-playbooks/pai
Alias:    pai added to ~/.zshrc

Reload your shell or run:
  source ~/.zshrc

Then run with:
  pai
```

---

### `claude-playbook delete <name>`

Deletes the playbook directory and all its data, and removes its alias from the shell config.

```bash
claude-playbook delete experiment      # prompts for confirmation
claude-playbook delete experiment -y   # skip confirmation
```

**Confirmation prompt** (shown unless `-y` is given):
```
Playbook: experiment
Location: ~/.claude-playbooks/experiment
Alias:    experiment (will be removed from ~/.zshrc)
Contents: 12 files, 3 directories

Permanently delete? [y/N]
```

**Edge cases:**

- Playbook not found → error: `Unknown playbook 'experiment'.`
- No alias set → skip alias cleanup silently, still delete the directory
- Directory already missing → skip deletion silently, still clean up alias if present

---

### Adding a playbook's `bin/` to PATH

`claude-playbook` does not manage PATH entries. If a playbook ships a `bin/` directory, add it manually to your shell config:

```bash
export PATH="$HOME/.claude-playbooks/experiment/bin:$PATH"
```

---

## Alias Management

Aliases are written to and removed from the user's shell config file.

**Shell detection order:**
1. `$SHELL` environment variable → if it contains `zsh`, target `~/.zshrc`; if `bash`, target `~/.bashrc`
2. If shell is unknown or config file does not exist → error with message: `Could not find shell config. Use --shell-config <path> to specify one.`

**Format of generated alias:**
```bash
# claude-playbook: <name>
alias <alias>='CLAUDE_CONFIG_DIR=~/.claude-playbooks/<name> claude'
```

The comment line `# claude-playbook: <name>` is used to locate and remove the alias on `remove` or `alias --remove`. Do not remove or edit this line manually.

**Updating an alias:** find the existing block by the comment line and replace it in place. Never append a duplicate.

---

## Global Flags

These flags work on any command:

| Flag | Description |
|------|-------------|
| `--shell-config <path>` | Use this file instead of the auto-detected shell config |
| `--playbooks-dir <path>` | Use this directory instead of `~/.claude-playbooks` |
| `--help` | Show help for the command |
| `--version` | Show the claude-playbooks version |

---

## Error Conventions

- All errors print to stderr.
- Exit code `0` = success, `1` = user error (bad input, missing resource), `2` = system error (file permission, missing dependency).
- Error messages are plain English, one line, no stack traces. Always suggest the next action where possible.

```
Error: Playbook 'experiment' already exists.
Error: Unknown playbook 'typo'. Did you mean 'work'?
Error: 'claude' not found on PATH. Install Claude Code first.
```
