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

## How isolation works

Claude Code reads its configuration from the directory set in `CLAUDE_CONFIG_DIR` (defaults to `~/.claude`). Change that variable, and you get a completely fresh, independent instance:

```bash
# Your normal Claude Code (uses ~/.claude)
claude

# An isolated playbook (uses ~/.claude-playbooks/experiment)
CLAUDE_CONFIG_DIR=~/.claude-playbooks/experiment claude
```

That's all a playbook is under the hood. `claude-playbook` just makes creating and managing them easy.

## Installation

```bash
# Coming soon: install via brew or curl
# For now, clone and add to PATH
git clone https://github.com/ramazanpolat/claude-playbooks.git
cd claude-playbooks
./install.sh
```

## Usage

### List your playbooks

```bash
claude-playbook list
```

```
NAME          PATH                               ALIAS         LAST USED
experiment    ~/.claude-playbooks/experiment     experiment    2 days ago
work          ~/.claude-playbooks/work           work          1 hour ago
```

### Create a new playbook

```bash
claude-playbook create experiment
```

Creates a new isolated directory at `~/.claude-playbooks/experiment` with a fresh Claude Code setup inside. By default, also adds a shell alias with the same name so you can launch it immediately:

```bash
experiment    # launches Claude Code with the experiment playbook
```

To skip the alias:

```bash
claude-playbook create experiment --no-alias
```

To use a different alias:

```bash
claude-playbook create experiment --alias exp
```

### Install a playbook from a repo or directory

```bash
# Git repo — auto-detects playbook manifest
claude-playbook install https://github.com/danielmiessler/Personal_AI_Infrastructure --name pai

# Git repo with multiple playbooks — pick one
claude-playbook install https://github.com/user/repo --playbook work

# Local directory (symlink by default)
claude-playbook install ~/dev/my-playbook

# Local directory (copy)
claude-playbook install ~/dev/my-playbook --copy
```

If the repo contains a `.playbook` manifest file, the installer reads it for defaults (name, alias, which subdirectory to use). You can override any of these with CLI flags.

A repo can contain multiple `*.playbook` files — one per configuration. Use `--playbook <name>` to pick one:

```
my-repo/
    .playbook           # default
    work.playbook       # selected with --playbook work
    personal.playbook   # selected with --playbook personal
```

If no `*.playbook` file exists, the repo root (or `--subdir`) is installed directly as the playbook.

### Run a playbook

```bash
claude-playbook run experiment
```

Opens Claude Code using that playbook without requiring an alias. Any flags after the name are forwarded directly to `claude`:

```bash
claude-playbook run experiment --model claude-opus-4-6 --permission-mode auto
```

Useful for temporary or one-off playbooks where setting up an alias is not worth it.

### Manage aliases

```bash
# Show all aliases (full alias line as it appears in shell config)
claude-playbook alias

# Show or create alias for a specific playbook
claude-playbook alias experiment

# Set a custom alias
claude-playbook alias experiment exp

# Remove an alias
claude-playbook alias experiment --remove
```

Since an alias is just a shell command, you can manually edit it to include any Claude Code flags. Open your `~/.zshrc` and adjust the generated alias to your liking:

```bash
# Default generated alias
alias experiment='CLAUDE_CONFIG_DIR=~/.claude-playbooks/experiment claude'

# Pin a specific model
alias experiment='CLAUDE_CONFIG_DIR=~/.claude-playbooks/experiment claude --model claude-opus-4-6'

# Auto-approve everything (no permission prompts)
alias experiment='CLAUDE_CONFIG_DIR=~/.claude-playbooks/experiment claude --permission-mode auto'

# Max effort + specific model (good for deep work)
alias work='CLAUDE_CONFIG_DIR=~/.claude-playbooks/work claude --model claude-opus-4-6 --permission-mode auto --effort max'

# Lightweight model for quick tasks or experimentation
alias scratch='CLAUDE_CONFIG_DIR=~/.claude-playbooks/scratch claude --model claude-haiku-4-5-20251001'
```

Any flag that `claude` accepts can go in the alias. Run `claude --help` to see all available options.

### Delete a playbook

```bash
claude-playbook delete experiment      # prompts for confirmation
claude-playbook delete experiment -y   # skip confirmation
```

Deletes the playbook directory and removes its alias from the shell config.

### Adding a playbook's bin/ to PATH

Some playbooks ship CLI tools in a `bin/` directory. Add them to your PATH manually:

```bash
# In ~/.zshrc
export PATH="$HOME/.claude-playbooks/experiment/bin:$PATH"
```

## Relationship to CLAUDE.md

Every playbook can have a `CLAUDE.md` file in its root directory. Claude Code loads this file as standing instructions at the start of every session -- your rules, protocols, and context that apply to every conversation in that playbook.

This is separate from project-level `CLAUDE.md` files (which live in your project directories and describe the project itself). Both are loaded simultaneously; the playbook's `CLAUDE.md` defines *how you work*, the project's `CLAUDE.md` defines *what you're working on*.

## Example: a focused experiment

You want to test a custom `SessionStart` hook that shows your active tasks at the start of each conversation. You don't want to break your normal Claude Code workflow while iterating.

```bash
# Create an isolated sandbox (alias 'hook-test' added automatically)
claude-playbook create hook-test

# Edit the new playbook's config freely
$EDITOR ~/.claude-playbooks/hook-test/settings.json

# Launch it
hook-test

# When you're done testing, clean up
claude-playbook delete hook-test
```

Your main `claude` setup was never touched.

## License

MIT
