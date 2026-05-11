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

**Build from source** (requires [Go](https://go.dev/dl/) 1.21+):

```bash
git clone https://github.com/ramazanpolat/claude-playbooks.git
cd claude-playbooks
./build.sh
mv claude-playbook /usr/local/bin/
```

## Release process

GitHub releases are created from `v*` tags only when the tagged commit is already on `main`. Tags pushed from feature branches are ignored by the release workflow.

```bash
git checkout main
git pull --ff-only
git tag -a vX.Y.Z -m vX.Y.Z
git push origin vX.Y.Z
```

## Usage

### List your playbooks

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

Children are indented under their parent. Pass a prefix to filter (`claude-playbook list awesome/`).

### Create a new playbook

```bash
claude-playbook create experiment
```

Creates a new isolated directory at `~/.claude-playbooks/experiment` with a fresh Claude Code setup inside. By default, also adds a shell alias with the same name so you can launch it immediately:

```bash
experiment    # launches Claude Code with the experiment playbook
```

Override or skip the alias:

```bash
claude-playbook create experiment --alias exp
claude-playbook create experiment --no-alias
```

`create` only makes top-level playbooks. To add a child, edit the parent's `.playbook` and declare it under `[[children]]`.

### Install a playbook from a repo or directory

There are two install modes, selected by whether you specify a subdirectory.

#### Tree install (whole repo)

```bash
# Install the whole repo. Uses metadata from the repo's .playbook (name, alias).
claude-playbook install https://github.com/ramazanpolat/awesome-playbooks

# Override the install name and alias
claude-playbook install https://github.com/user/awesome --name ap --alias ap

# Also write aliases for every declared child
claude-playbook install https://github.com/user/awesome --alias-all

# Install a local directory (always copied)
claude-playbook install ~/dev/my-playbook

# Source has no .playbook — write a minimal one at the install destination
claude-playbook install ~/dev/scratch --init
```

By default the root playbook gets a shell alias; declared children do not, unless you pass `--alias-all`. You can always alias one explicitly later: `claude-playbook alias awesome/dba ap-dba`.

#### Cherry-pick a subdirectory

```bash
# Pull just one playbook out of a larger repo
claude-playbook install https://github.com/user/awesome --subdir playbooks/dba

# Same thing, expressed as a GitHub browser URL (branch + subdir parsed automatically)
claude-playbook install https://github.com/user/awesome/tree/main/playbooks/dba

# Custom name and alias
claude-playbook install https://github.com/user/awesome --subdir playbooks/dba --name dba --alias ap-dba
```

Cherry-pick installs are flat — any `[[children]]` declarations inside the cherry-picked subdir are ignored.

#### `.playbook` manifest

Every playbook has a `.playbook` file at its root. Top-level metadata plus optional declared children:

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

Children declared here become addressable as `awesome/dba` and `awesome/sre`. The child's `name` is independent of its on-disk path, so you can refactor your repo without breaking the user-facing names.

### Symlink an external directory

Use `link` when you're actively developing a playbook outside the playbooks root and want to edit-in-place:

```bash
claude-playbook link ~/dev/my-playbook
```

If the target has a `.playbook` already, the symlink is created and an alias is written. If it doesn't, you'll be prompted ssh-keygen-style for the metadata, and a `.playbook` will be written into the target directory.

```bash
claude-playbook link ~/dev/my-playbook --name scratch --alias sc
claude-playbook link ~/dev/my-playbook --no-alias
```

`link` is a separate command from `install` because the lifecycle is different: deletes only remove the symlink (the source is preserved), edits are live, and `.playbook` is written into your source directory.

### Run a playbook

```bash
claude-playbook run experiment
claude-playbook run awesome/dba          # children are addressed as parent/child
claude-playbook run experiment --model claude-opus-4-6 --permission-mode auto
```

Any flags after the name are forwarded directly to `claude`. Useful for temporary or one-off playbooks where setting up an alias isn't worth it.

### Start an ad-hoc session

```bash
claude-playbook start /tmp/scratch
claude-playbook start /tmp/scratch --model claude-opus-4-6
claude-playbook start /tmp/scratch --delete
```

Opens Claude Code at any directory without registering a named playbook. The directory is created if it doesn't exist. `--delete` removes the directory when the session ends — useful for fully throwaway experiments.

### Manage aliases

```bash
# Show all aliases (full alias line as it appears in shell config)
claude-playbook alias

# Show alias for a specific playbook (read-only — does not create anything)
claude-playbook alias awesome/dba

# Set or replace an alias
claude-playbook alias awesome/dba ap-dba

# Remove an alias
claude-playbook alias awesome/dba --remove
# or:
claude-playbook dealias awesome/dba
```

Since an alias is just a shell command, you can hand-edit your `~/.zshrc` to add any Claude Code flags:

```bash
# Default generated alias
alias experiment='CLAUDE_CONFIG_DIR=~/.claude-playbooks/experiment claude'

# Pin a specific model
alias experiment='CLAUDE_CONFIG_DIR=~/.claude-playbooks/experiment claude --model claude-opus-4-6'

# Auto-approve everything (no permission prompts)
alias experiment='CLAUDE_CONFIG_DIR=~/.claude-playbooks/experiment claude --permission-mode auto'

# Max effort + specific model (good for deep work)
alias work='CLAUDE_CONFIG_DIR=~/.claude-playbooks/awesome/playbooks/dba claude --model claude-opus-4-6 --permission-mode auto --effort max'
```

Any flag that `claude` accepts can go in the alias. Run `claude --help` to see all available options.

### Delete a playbook

```bash
claude-playbook delete experiment      # prompts for confirmation
claude-playbook delete awesome -y      # also removes children + their aliases
```

`uninstall` and `unlink` are accepted as command aliases — same behavior, different word that may read better depending on how the playbook arrived:

```bash
claude-playbook uninstall awesome      # reads better after 'install'
claude-playbook unlink my-thing        # reads better after 'link'
```

For symlinked playbooks, all three remove only the symlink — the target directory is preserved.

Children cannot be deleted independently — to drop a child, edit the parent's `.playbook`. To drop just a child's alias, use `dealias`.

### Update a playbook

`claude-playbook` does not know how to update playbook contents itself; instead, it runs `bin/update-playbook.sh` from inside the playbook directory if the playbook author has provided one:

```bash
claude-playbook update awesome    # runs awesome/bin/update-playbook.sh
```

Children don't have independent updaters — pass the parent's name.

A typical updater script for a Git-backed playbook:

```bash
#!/bin/sh
set -e
cd "$(dirname "$0")/.."
git pull --ff-only
```

### Adding a playbook's bin/ to PATH

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

## License

MIT
