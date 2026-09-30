# Claude Playbooks

[![CI](https://github.com/ramazanpolat/claude-playbooks/actions/workflows/ci.yml/badge.svg)](https://github.com/ramazanpolat/claude-playbooks/actions/workflows/ci.yml)

**If Claude Code is where you work, `cpb` is how you run more than one of it.**

One `~/.claude` holds one setup: one set of settings and hooks, one memory,
one login, one environment, all with full access to your machine. `cpb`
gives you as many Claude Codes as you need, each behind its own command, and
each isolated as far as you choose:

| Layer | What each playbook gets | How |
|---|---|---|
| **Config home** | its own `CLAUDE.md`, `settings.json`, hooks, memory, history, sessions, plugins, MCP servers and skills. Your `~/.claude` never moves. | every playbook, always |
| **Environment** | its own variables, set or blocked at launch: another model backend, another token, another proxy. Your shell stays as it is. | `USE ENV`, `SET VAR`, `BLOCK VAR` |
| **Login** | its own Anthropic account, sharing nothing with `~/.claude` | `ISOLATED LOGIN` |
| **Process** | a microVM with its own kernel, filesystem and network. It sees only your working directory and its own config, and your machine's login never enters it. By default a host-side proxy injects your backend API keys, so the sandbox never holds them ([sandbox guide](docs/guides/sandbox.md#secrets)). | `SANDBOX`, or `--sandbox` on any launch |

You describe each playbook in a file, apply it anywhere, and get the same
Claude Code every time.

```bash
cpb CREATE PLAYBOOK work                           # a playbook, and a `work` command that opens it
cpb CREATE PLAYBOOK side ISOLATED LOGIN            # a second account, running beside the first
cpb CREATE PLAYBOOK sre SANDBOX                    # every launch inside a microVM
work                                               # Claude Code, bound to that playbook
```

![claude-playbook demo](docs/demo.gif)

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/ramazanpolat/claude-playbooks/main/install.sh | sh
```

Linux and macOS, amd64/arm64. Installs `claude-playbook` and the shorter `cpb`;
`cpb update` updates it. [devbox, Nix, npx and source builds →](docs/guides/installation.md)

## What you can do with it

### Try things without touching your daily setup

A new hook, plugin, model or `CLAUDE.md` goes into a scratch playbook, not
into `~/.claude`. When you're done, drop it.

```bash
cpb CREATE PLAYBOOK scratch
cpb ALTER PLAYBOOK scratch ADD MARKETPLACE mine FROM '~/src/my-plugins' ADD PLUGIN hello@mine
scratch                                            # try it
cpb DROP PLAYBOOK scratch --yes                    # gone; ~/.claude was never touched
cpb start /tmp/spike --delete                      # or a throwaway session in a throwaway folder
```

### Route one playbook to another model

An env set is a named group of variables. Attach it to one playbook, or to
every playbook through `DEFAULTS`. `EXPLAIN` shows every variable a launch
sets, and which layer set it.

```bash
cpb CREATE ENV router SET ANTHROPIC_BASE_URL=http://localhost:20128/v1
cpb CREATE PLAYBOOK glm NO PILOT PROFILE ISOLATED LOGIN
cpb ALTER PLAYBOOK glm USE ENV router SET VAR ANTHROPIC_MODEL=glm-5.3
cpb ALTER PLAYBOOK glm BLOCK VAR ANTHROPIC_API_KEY   # removed even if your shell exports it
cpb EXPLAIN PLAYBOOK glm
```

A playbook routed away from Anthropic should share nothing personal with that
provider. `ISOLATED LOGIN` keeps your Anthropic login and account state out of
it. A new playbook also imports your `~/.pilot-profile/` notes, if you keep
one, and `NO PILOT PROFILE` leaves them out; cpb warns when a playbook that
has them is routed elsewhere. [Environment →](docs/guides/environment.md)

### Keep two accounts apart, both running

A playbook with `ISOLATED LOGIN` has a login of its own: one `/login` inside
it, and it shares nothing with `~/.claude`. Work and personal, or two
organizations, run side by side in two terminals.
[Authentication →](docs/guides/authentication.md)

### Let an agent loose without letting it near your machine

With `--sandbox`, the playbook's Claude Code runs inside a
[Docker Sandbox](https://docs.docker.com/ai/sandboxes/). It sees your working
directory and the playbook's own directory. Your home directory, `~/.claude`,
your shell's environment and your other playbooks are not there.

```bash
cpb run --sandbox work                                         # this folder is the workdir
cpb run --sandbox --sandbox-fresh --clone --workdir ~/untrusted-repo work   # a private clone; your tree is untouched
cpb run --sandbox --mount ~/shared-libs:ro work                # one more directory, read-only
cpb run --sandbox-host me@buildbox work                        # the same launch, sandboxed on another machine
```

- **API keys stay outside, by default.** A backend key from an env set
  (`ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`) reaches the sandbox as a
  placeholder, and the host-side proxy puts the real key into requests to
  that endpoint only. Two cases put the real key inside: `secrets = "env"` in
  the playbook's `[sandbox]` block, which opts out, and a key the proxy fails
  to register, which cpb warns about on stderr. A Claude Code login token
  (`CLAUDE_CODE_OAUTH_TOKEN`), when the launch uses one, goes in as well:
  Claude Code needs it.
- **Your machine's login never enters.** A mount that contains `~/.claude` is
  refused. A playbook that shares your login gets one of its own inside the
  sandbox instead: one `/login` there, kept in the sandbox.
- **Network egress** follows your `sbx` policy. By default that allows model
  APIs, package managers and code hosts.
- **Always on:** a playbook created with `SANDBOX` runs sandboxed on every
  launch. `--no-sandbox` is the only way out for one launch, and cpb says so
  when you use it.

Needs `sbx` (Docker Sandboxes) installed and logged in.
[Sandbox →](docs/guides/sandbox.md)

### Put the whole setup in a file

Everything above is a statement, so a playbook is a file. `APPLY` checks every
statement before it writes anything, `--dry-run` shows what would change and
every command it would run, and applying twice changes nothing.

```
-- reviewer.cpb: a recipe (it names no playbook)
INCLUDE 'base.cpb';                                  -- shared layers stack
ALTER PLAYBOOK
  ADD MCP SERVER github URL 'https://api.githubcopilot.com/mcp/'
      HEADER 'Authorization' FROM 'keychain:pilot/github-mcp'   -- a secret by reference
  ALLOW TOOL 'Bash(gh pr *)'
  DENY TOOL 'Bash(git push *)'
  ADD SKILL review FROM 'https://github.com/me/skills' SUBDIR review
  SET MODEL 'claude-opus-5-5'
  SET STATUSLINE 'bash ~/bin/statusline.sh' REFRESH 10;
```

```bash
cpb APPLY reviewer.cpb TO reviewer --dry-run       # the plan, statement by statement
cpb APPLY reviewer.cpb TO reviewer                 # build it; the playbook is created if missing
cpb SHOW CREATE ALL > machine.cpb                  # this whole machine, as statements
cpb APPLY machine.cpb                              # on the next machine
```

- **Secrets by reference.** A reference is resolved at launch through the
  secret helper you configure (`ALTER DEFAULTS SET SECRET HELPER`). A literal
  that looks like a credential is refused unless you write `AS PLAINTEXT`,
  and `SHOW CREATE` never prints a value.
- **The agent's whole configuration:** plugins and marketplaces, the
  main-thread agent, MCP servers, tool permissions, the status line and its
  panels, the default model and the `/model` picker, and skills from a folder
  or a git repository.
- **Share it:** a recipe applies to any playbook, or to `~/.claude` itself
  (`TO '~/.claude'`). A playbook can also come from a git repository, pinned
  to a branch or a tag (`cpb CREATE PLAYBOOK <name> FROM <url> BRANCH <ref>`),
  or run in place from a folder you are developing (`LINK <dir>`).

### See everything that is running

```bash
cpb sessions                                       # live Claude Code sessions, in every playbook
cpb RESUME                                         # resume this folder's latest one, in its own playbook
cpb tui                                            # browse playbooks, sessions and env sets; export a .cpb
cpb "SELECT name, envs, sandbox FROM PLAYBOOKS"    # state as tables; add ClickHouse for full SQL
```

`RESUME` won't resume a session that is still live somewhere else. The
terminal UI and `SELECT` read the same redacted `--json` the statements print,
so neither ever shows a secret value.

## Built to be relied on

- **Stable since v3.24.0.** Breaking changes wait for a major version. New
  clauses and new `--json` fields arrive in minor releases, and a deprecated
  command keeps working, with a warning, before it goes.
  [What is stable →](docs/reference/cli-grammar.md#stability-from-v3240)
- **Tested on every change.** CI applies all 19 examples, and on Linux and
  macOS upgrades from the previous release and checks that the new build
  reads the same state identically. Every release also passes a full
  [arena](gentar/README.md) regression on a dedicated test machine, on the
  exact commit it is tagged from.
- **Another account's login never replaces yours.** When a playbook that
  shares your login holds one of a different account, cpb sets it aside
  instead of copying it over `~/.claude`'s. The upgrade test checks that a
  made-up machine login is never touched.

## The grammar

`cpb <VERB> <OBJECT> <name> <clause> ...`, read and written like SQL DDL. The
objects are `PLAYBOOK`, `ENV` (a named env set) and `DEFAULTS`; `SHOW`,
`SHOW CREATE` and `EXPLAIN` read back what the statements did. Also kept as
commands: `cpb install <url>`, `cpb run <name>`, `cpb start <dir>`,
`cpb update`, `cpb auth status`. The older `env`, `env-profile`, `list`,
`info`, `alias`, `rename`, `link` and `delete` commands still work, hidden and
deprecated, and warn on each use; v4.0.0 removes them.
[CLI grammar →](docs/reference/cli-grammar.md)

## Learn it

| | |
|---|---|
| [Your first playbook.cpb](docs/tutorials/first-playbook.md) | create, route, run, export, apply elsewhere |
| [Stack layers into an agent](docs/tutorials/stacked-agent.md) | bare -> Kommander -> a layer on top, as recipes |
| [Examples 01-19](examples/) | one small `playbook.cpb` per idea, from a first playbook to a stacked agent, sessions and the TUI, all applied in CI |
| [Guides](docs/README.md) | installation, playbooks, configuring an agent, environment, authentication, sandbox, sessions, the TUI, SQL |
| [CLI grammar](docs/reference/cli-grammar.md) | every statement, clause, file rule and output format |
| [SPEC-v4.md](SPEC-v4.md) · [Contributing](CONTRIBUTING.md) | the behavioral contract · development |

## License

MIT
