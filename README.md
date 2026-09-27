# Claude Playbooks

[![CI](https://github.com/ramazanpolat/claude-playbooks/actions/workflows/ci.yml/badge.svg)](https://github.com/ramazanpolat/claude-playbooks/actions/workflows/ci.yml)

**Run many isolated Claude Codes on one machine, and describe each one in a
file.** Separate settings, hooks, memory, environment, plugins and logins,
each behind its own command.

```
-- kommander.cpb: a recipe (it names no playbook)
INCLUDE 'bare.cpb';                           -- the model route
ALTER PLAYBOOK
  ADD MARKETPLACE kommander FROM '~/path/to/kommander-playbook'
  ADD PLUGIN kommander@kommander
  SET AGENT 'kommander'
  ALLOW TOOL 'Bash(kommander-helper *)'
  SET STATUSLINE 'bash ~/path/to/kommander-playbook/hooks/statusline.sh';
```

```bash
cpb APPLY kommander.cpb TO kommander-agent --dry-run   # what would change, and every command it would run
cpb APPLY kommander.cpb TO kommander-agent             # build it (the playbook is created if missing)
kommander-agent                                        # run it
```

That is an agent in one file: a route, Kommander as a plugin and the
main-thread agent, its tool permission and its status line. The same recipe
builds it under any name, and a layer on top `INCLUDE`s it. The kommander
repository is private: point the path at your checkout, or, with access, use
`FROM 'github:ramazanpolat/kommander-playbook'`.
[The full example, three stacked layers →](examples/08-kommander-agent/)

![claude-playbook demo](docs/demo.gif)

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/ramazanpolat/claude-playbooks/main/install.sh | sh
```

Linux and macOS, amd64/arm64. Installs `claude-playbook` and the shorter `cpb`.
[devbox, Nix, npx and source builds →](docs/guides/installation.md)

## What a playbook is

Claude Code reads its configuration from `CLAUDE_CONFIG_DIR` (default
`~/.claude`). A playbook is one such directory under `~/.claude-playbooks/`
with its own `CLAUDE.md`, `settings.json`, hooks, history, MCP servers and
plugins, plus a launcher command that opens Claude Code bound to it. Your
`~/.claude` never moves.

- Test a hook, a model or a plugin without touching your daily setup
- Keep work and personal setups, or two accounts, apart and running at once
- Share a setup as a Git repository, or as one `playbook.cpb`

## The grammar

`cpb <VERB> <OBJECT> <name> <clause> ...`, read and written like SQL DDL:

```bash
cpb CREATE PLAYBOOK scratch                        # a fresh playbook and its `scratch` command
cpb CREATE ENV router SET ANTHROPIC_BASE_URL=http://localhost:20128/v1
cpb ALTER PLAYBOOK scratch USE ENV router SET VAR MAX_THINKING_TOKENS=8000
cpb ALTER DEFAULTS USE ENV router                  # env sets under every playbook, in order
cpb SHOW PLAYBOOK scratch --json                   # state; EXPLAIN shows what a launch sets, and why
cpb SHOW CREATE ALL > playbook.cpb                 # this machine, as statements
cpb APPLY playbook.cpb                             # on the next machine
cpb DROP PLAYBOOK scratch --yes
```

- **Objects:** `PLAYBOOK`, `ENV` (a named env set), `DEFAULTS`.
- **Secrets by reference:** `SET TOKEN FROM 'keychain:pilot/token'` through a
  secret helper you configure; a credential-looking literal is refused unless
  you say `AS PLAINTEXT`, and `SHOW CREATE` never prints one.
- **Playbook files:** `APPLY` validates every statement before writing
  anything, runs them in order, and applying again changes nothing.
  `INCLUDE` stacks files.
- **Plugins and the agent:** `ADD MARKETPLACE` and `ADD PLUGIN` run Claude
  Code's own `claude plugin` commands for that playbook only; `SET AGENT` pins
  the main-thread agent in the playbook's `settings.json`.
- **The rest of the agent:** `ADD MCP SERVER` (a credential only by
  reference), `ALLOW` / `DENY TOOL`, `SET STATUSLINE`, `SET MODEL`, the
  `/model` picker (`ADD MODEL … SET MODEL PICKER ONLY`), and `ADD SKILL`
  from a directory or a git repository.
- **Recipes:** an `ALTER PLAYBOOK` with no name applies to whatever
  `APPLY … TO <playbook>`, `TO '~/.claude'` or a `USE PLAYBOOK` line names.
- **Queries:** `cpb "SELECT name, envs FROM PLAYBOOKS"`; anything beyond
  columns runs in ClickHouse's `clickhouse local`, over the same redacted
  `--json` rows.

Kept as commands: `cpb install <url>` (= `CREATE PLAYBOOK … FROM`),
`cpb run <name>`, `cpb start <dir>`, `cpb update`, `cpb auth status`, and
`--sandbox` on any launch. The older `env`, `env-profile`, `list`, `info`,
`alias`, `rename`, `link` and `delete` commands still work, hidden from help.

## Learn it

| | |
|---|---|
| [Your first playbook.cpb](docs/tutorials/first-playbook.md) | create, route, run, export, apply elsewhere |
| [Stack layers into an agent](docs/tutorials/stacked-agent.md) | bare -> Kommander -> a layer on top, as recipes |
| [Examples 01-14](examples/) | one small `playbook.cpb` per idea, from a first playbook to a stacked agent, all applied in CI |
| [CLI grammar](docs/reference/cli-grammar.md) | every statement, clause, file rule and output format |
| [Guides](docs/README.md) | installation, playbooks, configuring an agent, environment, authentication, sandbox, agents, SQL |
| [SPEC-v4.md](SPEC-v4.md) · [Contributing](CONTRIBUTING.md) | the behavioral contract · development |

## License

MIT
