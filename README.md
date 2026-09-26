# Claude Playbooks

[![CI](https://github.com/ramazanpolat/claude-playbooks/actions/workflows/ci.yml/badge.svg)](https://github.com/ramazanpolat/claude-playbooks/actions/workflows/ci.yml)

**Run many isolated Claude Codes on one machine, and describe each one in a
file.** Separate settings, hooks, memory, environment, plugins and logins,
each behind its own command.

```
-- bare.cpb
CREATE PLAYBOOK IF NOT EXISTS kommander-agent;
ALTER PLAYBOOK kommander-agent USE ENV glm-5.3-flash;

-- kommander.cpb
INCLUDE 'bare.cpb';
ALTER PLAYBOOK kommander-agent
  ADD MARKETPLACE kommander FROM '~/path/to/kommander-playbook'
  ADD PLUGIN kommander@kommander
  SET AGENT 'kommander';

-- chaos.cpb
INCLUDE 'kommander.cpb';
ALTER PLAYBOOK kommander-agent
  ADD MARKETPLACE chaos-stub FROM './chaos-stub'
  ADD PLUGIN chaos@chaos-stub;
```

```bash
cpb APPLY chaos.cpb --dry-run   # what would change, and every command it would run
cpb APPLY chaos.cpb             # build it, layer by layer
kommander-agent                 # run it
```

That is an agent built from three stacked layers: a bare playbook, Kommander as
a plugin and the main-thread agent, and a layer on top. The kommander
repository is private: point the path at your checkout, or, with access, use
`FROM 'github:ramazanpolat/kommander-playbook'`.
[The full example →](examples/08-kommander-agent/)

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
- **Plugins and the agent:** `ADD MARKETPLACE`, `ADD PLUGIN` and `SET AGENT`
  run Claude Code's own `claude plugin` commands for that playbook only.

Kept as commands: `cpb install <url>` (= `CREATE PLAYBOOK … FROM`),
`cpb run <name>`, `cpb start <dir>`, `cpb update`, `cpb auth status`, and
`--sandbox` on any launch. The older `env`, `env-profile`, `list`, `info`,
`alias`, `rename`, `link` and `delete` commands still work, hidden from help.

## Learn it

| | |
|---|---|
| [Your first playbook.cpb](docs/tutorials/first-playbook.md) | create, route, run, export, apply elsewhere |
| [Stack layers into an agent](docs/tutorials/stacked-agent.md) | bare -> Kommander -> a layer on top |
| [Examples 01-08](examples/) | one small `playbook.cpb` per idea, all applied in CI |
| [CLI grammar](docs/reference/cli-grammar.md) | every statement, clause, file rule and output format |
| [Guides](docs/README.md) | installation, playbooks, environment, authentication, sandbox, agents, SQL over `--json` |
| [SPEC-v4.md](SPEC-v4.md) · [Contributing](CONTRIBUTING.md) | the behavioral contract · development |

## License

MIT
