# Claude Playbooks Specification

This document describes the current behavior of `claude-playbook`.

## Purpose

`claude-playbook` manages isolated Claude Code configuration directories.
Claude Code uses `CLAUDE_CONFIG_DIR` as its config root; each playbook is one
separate config root with its own settings, hooks, memory, history, MCP state,
and `CLAUDE.md`.

The tool provides commands to create, install, link, list, run, alias, rename,
delete, and update those isolated Claude Code instances.

## Storage Model

The default playbooks root is:

```text
~/.claude-playbooks
```

It can be overridden with:

- `--playbooks-dir <path>`
- `CLAUDE_PLAYBOOKS_DIR=<path>`

A directory is a playbook when it contains a `.playbook` manifest.

Top-level playbooks live directly under the playbooks root. Child playbooks live
inside a top-level playbook and are declared by that top-level playbook's
manifest. Children are addressed as:

```text
<parent>/<child>
```

Example:

```text
~/.claude-playbooks/
  awesome/
    .playbook
    CLAUDE.md
    playbooks/
      dba/
        CLAUDE.md
      sre/
        CLAUDE.md
```

If `awesome/.playbook` declares `dba` and `sre`, the playbooks are addressed as
`awesome`, `awesome/dba`, and `awesome/sre`.

## Manifest

The manifest file is `.playbook` and uses TOML.

```toml
version = "1.0.0"
name = "awesome"
alias = "ap"
description = "A bundle of role-focused playbooks"

[[children]]
name = "dba"
path = "playbooks/dba"
alias = "ap-dba"
description = "DBA playbook"

[[children]]
name = "sre"
path = "playbooks/sre"
description = "SRE playbook"
```

Top-level fields:

- `version`: manifest or playbook version string.
- `name`: preferred install/display name.
- `alias`: suggested shell alias for the root playbook.
- `description`: optional human-readable description.
- `children`: optional list of child playbooks.

Child fields:

- `name`: user-facing child name. It must be present and must not contain `/`.
- `path`: path to the child directory relative to the top-level playbook root.
- `alias`: optional suggested shell alias.
- `description`: optional human-readable description.

Child names must be unique inside one parent. During tree install, every child
path declared in the manifest must exist and must be a directory.

## Authentication Sync

Newly created, installed, and linked playbooks are prepared to reuse the user's
existing Claude Code authentication where possible.

The tool syncs authentication in two layers:

- It links the playbook's `.credentials.json` to the global Claude Code
  credentials file when a usable global credentials file exists.
- It copies selected non-token account/onboarding metadata into the playbook's
  `.claude.json`, so Claude Code can recognize the config directory as already
  authenticated.

On macOS, if `~/.claude/.credentials.json` is missing, the tool attempts to
materialize it from the Keychain item used by Claude Code.

Existing valid regular credentials inside a playbook are preserved, which allows
a playbook to use a separate login.

## Shell Aliases

Aliases are written to the detected shell config file, or to the path supplied
by:

- `--shell-config <path>`
- `CLAUDE_SHELL_CONFIG=<path>`

The generated alias form is:

```sh
alias example='CLAUDE_CONFIG_DIR=/path/to/playbook claude'
```

Aliases are intentionally plain shell aliases. Users may edit them manually to
add Claude Code flags, for example:

```sh
alias example='CLAUDE_CONFIG_DIR=/path/to/playbook claude --permission-mode auto'
```

Alias discovery scans the shell config for alias lines containing
`CLAUDE_CONFIG_DIR=`.

## Commands

### `create <name>`

Creates a new top-level playbook directory under the playbooks root and writes a
minimal `.playbook`.

Rules:

- `create` only creates top-level playbooks.
- Names must not contain `/`.
- Names must not start with `.`.
- Existing target directories are not overwritten.
- Authentication metadata is synced into the new playbook.

Flags:

- `--alias <alias>` writes a custom alias.
- `--no-alias` skips alias creation.

### `list [prefix]`

Lists discovered playbooks and aliases. Children are shown under their parent.

`prefix` filters the displayed names, including child names such as
`awesome/`.

### `run <name> [claude-flags...]`

Runs Claude Code with `CLAUDE_CONFIG_DIR` set to the selected playbook.

Any arguments after the playbook name are forwarded directly to `claude`.

Examples:

```sh
claude-playbook run experiment
claude-playbook run awesome/dba --model claude-opus-4-6
```

### `start <path> [claude-flags...]`

Starts Claude Code with `CLAUDE_CONFIG_DIR` set to an arbitrary path without
registering a named playbook.

The directory is created if it does not exist.

Flags:

- `--delete` removes the directory after Claude Code exits.

### `install <source>`

Installs a playbook from a Git URL or local directory. Local directory installs
are copied; they are not symlinked.

Install has two modes.

Tree install installs the whole source:

```sh
claude-playbook install https://github.com/user/awesome-playbooks
claude-playbook install ~/dev/my-playbook
```

Cherry-pick install installs one subdirectory as a flat top-level playbook:

```sh
claude-playbook install https://github.com/user/awesome --subdir playbooks/dba
claude-playbook install https://github.com/user/awesome/tree/main/playbooks/dba
```

Name selection order:

1. `--name`
2. manifest `name`
3. source-derived fallback

Rules:

- Git sources are cloned into temporary staging before copy/install.
- `--branch` applies only to Git URLs.
- GitHub `/tree/<ref>/<path>` URLs are parsed into clone URL, branch, and
  subdirectory.
- If the source has no `.playbook`, install fails unless `--init` is supplied.
- `--init` writes a minimal `.playbook` at the installed destination.
- Tree installs validate declared child directories.
- Cherry-pick installs ignore child declarations and install as a flat playbook.
- Authentication metadata is synced into the root playbook and, for tree
  installs, into declared children.

Alias behavior:

- Root alias is written by default unless `--no-alias` is used.
- Root alias selection order is `--alias`, manifest `alias`, manifest `name`,
  target name.
- `--alias-all` also writes aliases for children that declare an alias.
- `--alias-all` is tree-install only behavior.
- Alias collisions are skipped with a warning. Child alias collisions try a
  `<target>-<alias>` fallback before skipping.

Flags:

- `--name <name>`
- `--subdir <path>`
- `--branch <ref>`
- `--alias <alias>`
- `--alias-all`
- `--no-alias`
- `--init`

### `link <target>`

Symlinks an external directory into the playbooks root.

Use `link` for edit-in-place development of a playbook stored outside
`~/.claude-playbooks`.

Rules:

- The target must exist and must be a directory.
- Link names must not contain `/`.
- Link names must not start with `.`.
- Existing targets under the playbooks root are not overwritten.
- If the target has no `.playbook`, the command prompts for metadata and writes
  `.playbook` into the target.
- In non-interactive stdin, linking a target without `.playbook` fails.
- Authentication metadata is synced into the linked target.
- Later delete/uninstall/unlink operations remove only the symlink, not the
  source directory.

Flags:

- `--name <name>`
- `--alias <alias>`
- `--no-alias`

### `info <name>`

Shows details for one playbook.

### `rename <old-name> <new-name>`

Renames a top-level playbook directory.

Rules:

- Only top-level playbooks can be renamed.
- New names must not contain `/`.
- New names must not start with `.`.
- Existing target directories are not overwritten.
- Alias paths that point into the old playbook are rewritten to the new path.

Flags:

- `--alias <alias>` sets a custom alias after rename.
- `--no-alias` removes aliases for the renamed playbook.

### `alias [name] [alias]`

Manages shell aliases.

Forms:

```sh
claude-playbook alias
claude-playbook alias awesome/dba
claude-playbook alias awesome/dba ap-dba
claude-playbook alias awesome/dba --remove
```

With no arguments, the command lists discovered alias lines. With one playbook
name, it shows the alias for that playbook. With a playbook and alias, it writes
or replaces the alias.

### `dealias <name>`

Removes the alias for a playbook.

### `delete <name>`

Deletes a top-level playbook.

Command aliases:

- `uninstall`
- `unlink`

Rules:

- The command prompts for confirmation unless `--yes` is supplied.
- Children cannot be deleted independently.
- Deleting a parent removes aliases whose `CLAUDE_CONFIG_DIR` points at the
  parent or inside the parent.
- If the playbook root entry is a symlink, only the symlink is removed.

Flags:

- `-y`, `--yes`

### `update [name]`

With a playbook name, runs `bin/update-playbook.sh` from inside that playbook if
the script exists.

Without a playbook name, prints self-update guidance for the `claude-playbook`
binary.

Children do not have independent updaters. Pass the parent name.

### `completion [shell]`

Generates shell completion scripts for:

- `bash`
- `zsh`
- `fish`
- `powershell`

## Installer Scripts

### `install.sh`

Downloads a release asset and installs the binary.

Environment:

- `REPO`: GitHub repo, default `ramazanpolat/claude-playbooks`.
- `ASSET_PREFIX`: release asset prefix, default `claude-playbook`.
- `INSTALL_NAME`: installed command name, default `claude-playbook`.
- `BINARY_NAME`: fallback for `INSTALL_NAME`.
- `DEFAULT_INSTALL_DIR`: default system install dir, default `/usr/local/bin`.
- `INSTALL_DIR`: explicit install directory.
- `VERSION`: release tag to install. If empty, latest GitHub Release is used.
- `DOWNLOAD_BASE_URL`: release download base URL.
- `INSTALL_URL`: exact asset URL override.

If `INSTALL_DIR` is unset, the script installs to `/usr/local/bin` when
writable, otherwise to `~/.local/bin`.

`INSTALL_NAME` must be a command name, not a path. This supports custom command
names such as:

```sh
curl -fsSL https://raw.githubusercontent.com/ramazanpolat/claude-playbooks/main/install.sh | INSTALL_NAME=cpb sh
```

### `uninstall.sh`

Removes the installed binary only. It does not delete playbooks.

Environment:

- `INSTALL_NAME`: command name to remove, default `claude-playbook`.
- `BINARY_NAME`: fallback for `INSTALL_NAME`.
- `DEFAULT_INSTALL_DIR`: default system install dir, default `/usr/local/bin`.
- `INSTALL_DIR`: explicit install directory.
- `CLAUDE_PLAYBOOKS_DIR`: only used in the final informational message.

If `INSTALL_DIR` is set, only `$INSTALL_DIR/$INSTALL_NAME` is removed. If it is
unset, the script checks `/usr/local/bin`, `~/.local/bin`, and `command -v`.

## Release Workflow

GitHub releases are created from `v*` tags only when the tagged commit is already
on `main`. Tags pushed from feature branches are intentionally skipped.

## Tests

Markdown acceptance suites live under `tests/`.

- `tests/TEST_SUITE.md` verifies the built binary without installing it.
- `tests/INSTALL_UNINSTALL_TEST_SUITE.md` verifies `install.sh` and
  `uninstall.sh`, including custom command names such as `cpb`.

These suites are written for Codex or Claude Code to run in real cmux panes,
simulating visible terminal usage. They should use temporary playbooks roots,
temporary shell config files, and temporary install directories so they do not
modify the user's real playbooks, shell config, or installed binary.

## Non-Goals

`claude-playbook` does not implement Claude Code itself. It does not parse or
validate Claude Code settings beyond preparing config directories and launching
`claude` with `CLAUDE_CONFIG_DIR`.

The tool also does not update playbook contents directly. Playbook authors may
provide `bin/update-playbook.sh`, and `claude-playbook update <name>` delegates
to that script.
