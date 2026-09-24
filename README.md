# Claude Playbooks

[![CI](https://github.com/ramazanpolat/claude-playbooks/actions/workflows/ci.yml/badge.svg)](https://github.com/ramazanpolat/claude-playbooks/actions/workflows/ci.yml)

**Run many isolated Claude Codes on one machine.** Separate settings, hooks,
memory, environment, and logins — each behind its own command.

![claude-playbook demo](docs/demo.gif)

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/ramazanpolat/claude-playbooks/main/install.sh | sh
```

Linux and macOS, amd64/arm64. Installs `claude-playbook` and the shorter `cpb`.
In a devbox project: `devbox add github:ramazanpolat/claude-playbooks/v3.18.0#claude-playbook`.
[Other ways to install →](docs/installation.md)

## 60-second start

```bash
cpb create experiment     # a fresh, isolated Claude Code setup
experiment                # a real command on your PATH — launches it
```

That's the whole loop. `experiment` is now a directory under
`~/.claude-playbooks/` holding its own `CLAUDE.md`, `settings.json`, hooks,
history, and MCP servers — and a launcher command that opens Claude Code bound to
it. Your `~/.claude` never moved.

Three more you'll want on day one:

```bash
cpb list                                      # what you have, and its command
cpb install https://github.com/user/awesome   # someone else's playbook
cpb delete experiment                         # gone, launcher and all
```

## Why

Claude Code keeps everything in `~/.claude/`: settings, conversation history,
permissions, hooks, MCP servers. Trying a different model, a custom hook, or a
new `CLAUDE.md` behavior means touching your daily setup, and one wrong change
breaks it.

A playbook gives each experiment — or each role, client, or account — its own
directory.

- **Test a hook or setting** without risking your main `~/.claude`
- **Keep work and personal** configurations apart
- **Run two Claude Codes concurrently** with different personalities
- **Stay logged into two accounts at once** — corporate in one, personal in another
- **Share a setup with your team** by putting the playbook in a Git repo
- **Install one role** out of a repo that ships several

## Four boundaries

A playbook can isolate more than its config directory. Each layer is opt-in, and
they compose.

| | What it separates | Turn it on |
|---|---|---|
| **Config** | settings, hooks, memory, history, MCP servers | always — every playbook is its own `CLAUDE_CONFIG_DIR` |
| **[Identity](docs/authentication.md)** | which account or token the session runs as | a shared login, a long-lived token, a per-playbook `/login`, or `isolate_auth` |
| **[Environment](docs/environment.md)** | variables, API endpoints, proxies | `cpb env <name> set …`, or a profile shared by several playbooks |
| **[Process](docs/sandbox.md)** | kernel, filesystem, network | `--sandbox` — the session runs in a microVM that cannot see your home |

### How the first one works

Claude Code reads its configuration from `CLAUDE_CONFIG_DIR` (default
`~/.claude`). Change that variable and you get a completely fresh, independent
instance:

```bash
claude                                                      # your normal setup
CLAUDE_CONFIG_DIR=~/.claude-playbooks/experiment claude      # an isolated playbook
```

That's all a playbook is under the hood. `cpb` makes creating, launching,
sharing, and managing them easy.

```
~/.claude-playbooks/                Launcher commands (on PATH):

├── experiment/                     ◄── ~/.local/bin/experiment -> claude-playbook
│   ├── CLAUDE.md                       (typing `experiment` runs this playbook)
│   └── settings.json
│
└── awesome/                        ◄── ~/.local/bin/ap -> claude-playbook
    ├── .playbook                       (marker + metadata; `alias = "ap"` names the command)
    └── CLAUDE.md
```

A directory is a playbook if it exists under the playbooks root. The `.playbook`
manifest is optional — it holds metadata like version, alias, source, env
overrides, and sandbox settings.

## Commands

| | |
|---|---|
| `cpb create <name>` | a new playbook, plus its launcher command |
| `cpb install <url\|dir>` | copy a playbook in from a Git repo or directory |
| `cpb link <dir>` | symlink an external directory you're editing live |
| `cpb list` / `cpb info <name>` | what exists; one playbook in detail |
| `cpb run <name> [claude flags…]` | launch without the launcher command |
| `cpb start <dir>` | a throwaway session at any directory |
| `cpb alias` / `cpb rename` / `cpb delete` | manage names and remove playbooks |
| `cpb env` / `cpb env-profile` | per-playbook and shared environment overrides |
| `cpb auth status` | which login or token each playbook would use |
| `cpb update [name]` | update a playbook, or the tool itself |
| `cpb self-uninstall` | remove everything `cpb` installed |

Add `--sandbox` to any launch to run it in a microVM.

## Documentation

| | |
|---|---|
| [Installation](docs/installation.md) | install script, devbox/Nix, npx, source builds, uninstalling |
| [Managing playbooks](docs/playbooks.md) | create, install, link, launch, rename, update, delete |
| [Authentication](docs/authentication.md) | shared logins, long-lived tokens, isolated accounts |
| [Environment overrides](docs/environment.md) | per-playbook variables and shared env profiles |
| [Sandboxed sessions](docs/sandbox.md) | running a playbook inside a Docker Sandbox microVM |
| [Agent guide](docs/AGENT-GUIDE.md) | driving `cpb` unattended from an agent or CI |
| [SPEC-v4.md](SPEC-v4.md) | the behavioral contract |
| [Contributing](CONTRIBUTING.md) | development, tests, pull requests |

## License

MIT
