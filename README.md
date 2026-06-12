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
- **Share a configuration** with your team by putting the playbook in a Git repo
- **Ship a bundle of role-focused playbooks** (DBA, SRE, Frontend, ...) as one repo with named child playbooks

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

├── experiment/                     ◄── alias experiment='CLAUDE_CONFIG_DIR=~/.claude-playbooks/experiment claude'
│   ├── .playbook                       (marker + metadata)
│   ├── CLAUDE.md
│   └── settings.json
│
├── pai/                            ◄── alias pai='CLAUDE_CONFIG_DIR=~/.claude-playbooks/pai claude'
│   ├── .playbook
│   └── ...
│
└── awesome/                        ◄── alias ap='CLAUDE_CONFIG_DIR=~/.claude-playbooks/awesome claude'
    ├── .playbook                       (declares [[children]] for dba and sre)
    └── playbooks/
        ├── dba/                    ◄── alias ap-dba='CLAUDE_CONFIG_DIR=~/.claude-playbooks/awesome/playbooks/dba claude'
        │   └── CLAUDE.md
        └── sre/                    ◄── alias ap-sre='CLAUDE_CONFIG_DIR=~/.claude-playbooks/awesome/playbooks/sre claude'
            └── CLAUDE.md

Each playbook directory is a completely isolated Claude Code instance.
```

A directory is a playbook if it contains a `.playbook` file. Top-level playbooks may declare child playbooks in their `.playbook` manifest; children are addressed as `<top-level>/<child-name>` (e.g. `awesome/dba`).

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
#    Delete any lines matching: alias ...='CLAUDE_CONFIG_DIR=~/.claude-playbooks/...
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

### Create and run your own playbook

Use `create` when you want a fresh isolated Claude Code setup.

```bash
claude-playbook create experiment
source ~/.zshrc
experiment
```

This creates `~/.claude-playbooks/experiment`, writes a `.playbook` marker, drops in a starter `CLAUDE.md` that introduces the playbook concept to the Claude Code session opened inside it, syncs Claude auth metadata, and adds a shell alias named `experiment`.

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

`create` only creates top-level playbooks. Child playbooks are declared in a parent `.playbook` manifest.

### See what is installed

```bash
claude-playbook list
```

```
NAME           PATH                                            ALIAS    LAST USED
experiment     ~/.claude-playbooks/experiment                  exp      2 days ago
awesome        ~/.claude-playbooks/awesome                     ap       2 hours ago
  awesome/dba  ~/.claude-playbooks/awesome/playbooks/dba       ap-dba   2 hours ago
  awesome/sre  ~/.claude-playbooks/awesome/playbooks/sre       -        never
```

Children are indented under their parent. Filter a tree with a prefix:

```bash
claude-playbook list awesome/
```

### Install a shared playbook repo

Use `install` when the playbook is in a Git repo or local directory and you want a copied install under `~/.claude-playbooks`.

Install a whole repo:

```bash
claude-playbook install https://github.com/ramazanpolat/awesome-playbooks
```

If the repo's `.playbook` declares child playbooks, the root playbook is installed with its default alias. Child aliases are only created when you ask for them:

```bash
claude-playbook install https://github.com/ramazanpolat/awesome-playbooks --alias-all
```

After that, children can be run by name:

```bash
claude-playbook run awesome/dba
ap-dba
```

Override the install name or alias:

```bash
claude-playbook install https://github.com/user/awesome --name team-tools --alias tt
```

Install a local directory by copying it:

```bash
claude-playbook install ~/dev/my-playbook
```

If the source has no `.playbook`, initialize one at the installed copy:

```bash
claude-playbook install ~/dev/scratch --init
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

Cherry-picked installs are flat top-level playbooks. Any child declarations inside the picked directory are ignored.

Customize the name and alias:

```bash
claude-playbook install https://github.com/user/awesome --subdir playbooks/dba --name dba --alias ap-dba
```

### Develop a playbook in place

Use `link` when you are actively editing a playbook outside `~/.claude-playbooks` and want live changes.

```bash
claude-playbook link ~/dev/my-playbook
```

`link` creates a symlink under the playbooks root. If the target has no `.playbook`, it prompts for metadata and writes one into the target directory.

```bash
claude-playbook link ~/dev/my-playbook --name scratch --alias sc
claude-playbook link ~/dev/my-playbook --no-alias
```

Deleting a linked playbook removes only the symlink. The source directory is preserved.

### Use child playbooks

A repo can ship a root playbook plus named children. The root `.playbook` declares them:

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
# no alias field → no alias suggested for this child
```

Children are addressed as `<parent>/<child>`:

```bash
claude-playbook run awesome/dba
claude-playbook alias awesome/dba ap-dba
claude-playbook info awesome/dba
```

The child's public name is independent of its directory path, so a repo can move files around without changing the command users run.

### Manage aliases

Generated aliases are plain shell aliases that set `CLAUDE_CONFIG_DIR`:

```bash
alias experiment='CLAUDE_CONFIG_DIR=~/.claude-playbooks/experiment claude'
```

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
alias work='CLAUDE_CONFIG_DIR=~/.claude-playbooks/work claude --model claude-opus-4-6 --permission-mode auto'
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

Rename a top-level playbook:

```bash
claude-playbook rename experiment lab
claude-playbook rename lab experiment --alias exp
```

Delete a top-level playbook:

```bash
claude-playbook delete experiment      # prompts for confirmation
claude-playbook delete awesome -y      # also removes children + their aliases
```

`uninstall` and `unlink` are command aliases for `delete`:

```bash
claude-playbook uninstall awesome
claude-playbook unlink my-linked-playbook
```

Children cannot be deleted independently. To remove a child, edit the parent's `.playbook`. To remove only a child's alias, use `dealias`.

Update delegates to a playbook-provided script:

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

## Example: a bundle of role-focused playbooks

[awesome-playbooks](https://github.com/ramazanpolat/awesome-playbooks) ships a collection of role-focused configurations (DBA, SRE, SEO, SecOps, Frontend, Backend, Data, Writer) as one repo with named child playbooks:

```bash
# Pull the whole bundle, root alias only
claude-playbook install https://github.com/ramazanpolat/awesome-playbooks

# Or grab everything with all the suggested aliases
claude-playbook install https://github.com/ramazanpolat/awesome-playbooks --alias-all

# Or just one
claude-playbook install https://github.com/ramazanpolat/awesome-playbooks/tree/main/playbooks/dba
```

After install with `--alias-all`, every role becomes a one-word command: `ap-dba`, `ap-sre`, `ap-fe`, ...

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
