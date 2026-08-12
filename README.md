# Claude Playbooks

A **Claude Playbook** is an isolated instance of Claude Code.

That's it. Each playbook has its own configuration, settings, hooks, memory, and task history -- completely separate from your default `~/.claude` installation and from every other playbook.

## Why would I need this?

Claude Code stores everything in `~/.claude/`: your settings, conversation history, permissions, hooks, MCP servers. If you want to try something -- a different model, a custom hook, a new CLAUDE.md behavior -- you have to touch your main setup. One wrong change and your daily workflow breaks.

Playbooks solve this by giving each experiment (or workflow) its own isolated directory.

Common use cases:

- **Test a new hook or setting** without risking your main `~/.claude`
- **Separate work and personal** configurations that don't interfere
- **Run two Claude Code instances concurrently** on different tasks with different personalities
- **Authenticate with different accounts concurrently** (e.g., keep one playbook authenticated with your corporate account and another with your personal account)
- **Share a configuration** with your team by putting the playbook in a Git repo
- **Consume a repository** containing one or more playbook configurations (e.g. via subdirectories)

## How isolation works

Claude Code reads its configuration from the directory set in `CLAUDE_CONFIG_DIR` (defaults to `~/.claude`). Change that variable, and you get a completely fresh, independent instance:

```bash
# Your normal Claude Code (uses ~/.claude)
claude

# An isolated playbook (uses ~/.claude-playbooks/experiment)
CLAUDE_CONFIG_DIR=~/.claude-playbooks/experiment claude
```

That's all a playbook is under the hood. `claude-playbook` just makes creating, sharing, and managing them easy.

```
~/.claude-playbooks/                Shell aliases:

├── experiment/                     ◄── alias experiment='CLAUDE_CONFIG_DIR="$HOME/.claude-playbooks/experiment" cpb run experiment'
│   ├── CLAUDE.md
│   └── settings.json
│
└── awesome/                        ◄── alias ap='CLAUDE_CONFIG_DIR="$HOME/.claude-playbooks/awesome" cpb run awesome'
    ├── .playbook                       (marker + metadata)
    └── CLAUDE.md

Each playbook directory is a completely isolated Claude Code instance.
```

A directory is a playbook if it exists under the playbooks root. A `.playbook` manifest file is optional and used for storing metadata (like version, author, description).

## Installation

```bash
curl -fsSL https://raw.githubusercontent.com/ramazanpolat/claude-playbooks/main/install.sh | sh
```

The script detects your OS and architecture, downloads the right binary from the latest GitHub Release, and installs it to `/usr/local/bin` (or `~/.local/bin` if that's not writable).

Verify:

```bash
claude-playbook --version
```

Install with a shorter command name:

```bash
curl -fsSL https://raw.githubusercontent.com/ramazanpolat/claude-playbooks/main/install.sh | INSTALL_NAME=cpb sh
cpb --version
```

You can also clone the repo and run the installer locally:

```bash
git clone https://github.com/ramazanpolat/claude-playbooks.git
cd claude-playbooks
./install.sh
```

Local installs support the same shorter command name:

```bash
INSTALL_NAME=cpb ./install.sh
cpb --version
```

Uninstall only the binary:

```bash
curl -fsSL https://raw.githubusercontent.com/ramazanpolat/claude-playbooks/main/uninstall.sh | sh
```

To uninstall a custom command name:

```bash
curl -fsSL https://raw.githubusercontent.com/ramazanpolat/claude-playbooks/main/uninstall.sh | INSTALL_NAME=cpb sh
```

Or run the local uninstaller from a clone. Use the same `INSTALL_NAME` if you installed a custom command name:

```bash
./uninstall.sh
INSTALL_NAME=cpb ./uninstall.sh
```

Uninstalling does not delete `~/.claude-playbooks`.

### Uninstalling claude-playbook itself

To remove the tool, all its installed playbooks, their shell aliases, and the
binary in one step:

```bash
claude-playbook self-uninstall          # prompts for confirmation
claude-playbook self-uninstall -y       # skip prompt
claude-playbook self-uninstall -y --keep-data     # keep ~/.claude-playbooks
claude-playbook self-uninstall -y --keep-binary   # keep the binary
claude-playbook self-uninstall --dry-run          # preview without removing
```

If the binary can't be removed (e.g. installed to `/usr/local/bin` and you're
not root), the command prints a `sudo rm <path>` hint and continues cleaning up
everything else.

**Manual fallback** (if you can't run the binary):

```bash
# 1. Remove aliases from your shell config (~/.zshrc or ~/.bashrc)
#    Delete any lines matching: alias ...='CLAUDE_CONFIG_DIR="$HOME/.claude-playbooks/...
# 2. rm -rf ~/.claude-playbooks
# 3. sudo rm /usr/local/bin/claude-playbook   # or wherever the binary lives
```

**Build from source** (requires [Go](https://go.dev/dl/) 1.21+):

```bash
git clone https://github.com/ramazanpolat/claude-playbooks.git
cd claude-playbooks
./build.sh
mv claude-playbook /usr/local/bin/
```

## Usage

Most workflows start with either `create`, `install`, or `link`.

`claude-playbook` tries to reuse your existing Claude Code authentication for newly created, installed, and linked playbooks. That means a new playbook should normally open Claude Code directly instead of asking you to log in again.

If you want to use **different accounts concurrently**, you can enable authentication isolation for any playbook. Simply set `isolate_auth = true` in the playbook's `.playbook` manifest file, or run the playbook with the environment variable `CLAUDE_PLAYBOOKS_ISOLATE_AUTH=true`. This isolates that playbook's login session and prevents it from sharing or auto-syncing credentials with your other playbooks or global settings.

### Create and run your own playbook

Use `create` when you want a fresh isolated Claude Code setup.

```bash
claude-playbook create experiment
source ~/.zshrc
experiment
```

This creates `~/.claude-playbooks/experiment`, drops in a starter `CLAUDE.md` that introduces the playbook concept to the Claude Code session opened inside it, syncs Claude auth metadata, and adds a shell alias named `experiment`. A `.playbook` manifest is optional and is not created by this command.

You can also run it without using the alias:

```bash
claude-playbook run experiment
```

Pass Claude Code flags after the playbook name:

```bash
claude-playbook run experiment --model claude-opus-4-6 --permission-mode auto
```

Use a custom alias or skip alias creation:

```bash
claude-playbook create backend --alias be
claude-playbook create scratch --no-alias
```

### See what is installed

```bash
claude-playbook list
```

```
NAME           PATH                                            ALIAS    LAST USED
experiment     ~/.claude-playbooks/experiment                  exp      2 days ago
awesome        ~/.claude-playbooks/awesome                     ap       2 hours ago
```

### Install a shared playbook repo

Use `install` when the playbook is in a Git repo or local directory and you want a copied install under `~/.claude-playbooks`.

Install a repo:

```bash
claude-playbook install https://github.com/ramazanpolat/awesome-playbooks
```

Override the install name or alias:

```bash
claude-playbook install https://github.com/user/awesome --name team-tools --alias tt
```

Install a local directory by copying it:

```bash
claude-playbook install ~/dev/my-playbook
```

### Install one playbook from a larger repo

Use a GitHub tree URL when you want only one subdirectory:

```bash
claude-playbook install https://github.com/user/awesome/tree/main/playbooks/dba
```

Or pass the subdirectory explicitly:

```bash
claude-playbook install https://github.com/user/awesome --subdir playbooks/dba
```

Cherry-picked installs are flat top-level playbooks.

Branch names containing `/` are resolved against the repository's remote refs. You can also make the boundary explicit with `--branch feature/name`.

Customize the name and alias:

```bash
claude-playbook install https://github.com/user/awesome --subdir playbooks/dba --name dba --alias ap-dba
```

### Develop a playbook in place

Use `link` when you are actively editing a playbook outside `~/.claude-playbooks` and want live changes.

```bash
claude-playbook link ~/dev/my-playbook
```

`link` creates a symlink under the playbooks root.

```bash
claude-playbook link ~/dev/my-playbook --name scratch --alias sc
claude-playbook link ~/dev/my-playbook --no-alias
```

Deleting a linked playbook removes only the symlink. The source directory is preserved.

### Launcher commands

`create`, `install`, and `link` register each playbook as a **launcher command**: a small executable `#!/bin/sh` script placed next to the `claude-playbook` binary (falling back to `~/.local/bin` when that directory is not writable). Unlike shell aliases, launchers work identically from any shell, are available immediately with no rc-file edit or reload, and are visible to scripts and cron:

```sh
#!/bin/sh
# claude-playbook launcher for playbook: experiment
# config-dir: /home/you/.claude-playbooks/experiment
CLAUDE_CONFIG_DIR='/home/you/.claude-playbooks/experiment' exec '/usr/local/bin/cpb' run 'experiment' "$@"
```

`delete` removes a playbook's launchers; `rename` regenerates them against the new location.

### Manage aliases (legacy)

Installs made by older versions registered playbooks as shell aliases routing through `claude-playbook run` (or `cpb run`):

```bash
# claude-playbook: experiment
alias experiment='CLAUDE_CONFIG_DIR="$HOME/.claude-playbooks/experiment" cpb run experiment'
```

Existing aliases keep working and are still cleaned up by `delete`/`rename`. Note that in interactive shells an alias wins over a launcher of the same name; `create`/`install` warn when that shadows a different playbook.

Show, set, or remove aliases:

```bash
claude-playbook alias
claude-playbook alias experiment
claude-playbook alias experiment exp
claude-playbook alias experiment --remove
claude-playbook dealias experiment
```

Because aliases are ordinary shell lines, you can edit them to add Claude Code flags:

```bash
# claude-playbook: work
alias work='CLAUDE_CONFIG_DIR="$HOME/.claude-playbooks/work" cpb run work --model claude-opus-4-6 --permission-mode auto'
```

### Temporary sessions

Use `start` for a one-off Claude Code config directory without registering a playbook:

```bash
claude-playbook start /tmp/scratch
claude-playbook start /tmp/scratch --model claude-opus-4-6
claude-playbook start /tmp/scratch --delete
```

`--delete` removes the directory when the session ends, which is useful for disposable experiments.

### Rename, delete, and update

Rename a playbook:

```bash
claude-playbook rename experiment lab
claude-playbook rename lab experiment --alias exp
```

Delete a playbook:

```bash
claude-playbook delete experiment      # prompts for confirmation
claude-playbook delete awesome -y      # skip confirmation
```

`uninstall` and `unlink` are command aliases for `delete`:

```bash
claude-playbook uninstall awesome
claude-playbook unlink my-linked-playbook
```

Update first delegates to a playbook-provided script:

```bash
claude-playbook update awesome
```

If `~/.claude-playbooks/awesome/bin/update-playbook.sh` exists, it is run from inside the playbook directory. A Git-backed playbook might ship:

```bash
#!/bin/sh
set -e
cd "$(dirname "$0")/.."
git pull --ff-only
```

Git installs also record their repository, branch, and selected subdirectory in `.playbook`. If no update script exists, a flat, non-linked install can update natively from that source. Native update stages a full backup, overlays new source files while preserving local Claude state and credentials, and atomically activates the result. Linked playbooks and legacy manifests with `subdir` require a delegated update script.

With **no** name, `update` self-updates the `claude-playbook` binary itself to the latest GitHub release:

```bash
claude-playbook update            # download + install the latest release
claude-playbook update --check    # report the latest version without installing
claude-playbook update --force    # reinstall even if already on the latest
```

It downloads the release asset for your OS/architecture, verifies it, and atomically replaces the running binary (resolving the `cpb` symlink so the real binary is updated). If the install directory needs elevated privileges to write, it says so.

### Use temporary config locations

For tests or demos, keep playbooks and shell aliases away from your real files:

```bash
CLAUDE_PLAYBOOKS_DIR=/tmp/playbooks \
CLAUDE_SHELL_CONFIG=/tmp/zshrc \
claude-playbook create demo
```

The equivalent flags are:

```bash
claude-playbook --playbooks-dir /tmp/playbooks --shell-config /tmp/zshrc create demo
```

### Add a playbook's bin directory to PATH

Some playbooks ship CLI tools in a `bin/` directory. Add them to your PATH manually:

```bash
# In ~/.zshrc
export PATH="$HOME/.claude-playbooks/experiment/bin:$PATH"
```

## Relationship to CLAUDE.md

Every playbook can have a `CLAUDE.md` file in its root directory. Claude Code loads this file as standing instructions at the start of every session -- your rules, protocols, and context that apply to every conversation in that playbook.

This is separate from project-level `CLAUDE.md` files (which live in your project directories and describe the project itself). Both are loaded simultaneously; the playbook's `CLAUDE.md` defines *how you work*, the project's `CLAUDE.md` defines *what you're working on*.

## Example: role-focused playbooks

To install a specific role configuration from a repository containing multiple playbooks (like [awesome-playbooks](https://github.com/ramazanpolat/awesome-playbooks)):

```bash
# Install the DBA playbook flat under your playbooks root:
claude-playbook install https://github.com/ramazanpolat/awesome-playbooks --subdir playbooks/dba --name dba --alias ap-dba

# Or install the SRE playbook:
claude-playbook install https://github.com/ramazanpolat/awesome-playbooks --subdir playbooks/sre --name sre --alias ap-sre
```

## Release process

GitHub releases are created from `v*` tags only when the tagged commit is already on `main`. Tags pushed from feature branches are ignored by the release workflow.

```bash
git checkout main
git pull --ff-only
git tag -a vX.Y.Z -m vX.Y.Z
git push origin vX.Y.Z
```

## License

MIT
