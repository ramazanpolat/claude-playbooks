# cpb (Claude PlayBooks)

[![CI](https://github.com/ramazanpolat/claude-playbooks/actions/workflows/ci.yml/badge.svg)](https://github.com/ramazanpolat/claude-playbooks/actions/workflows/ci.yml)

**If Claude Code is where you work, `cpb` is how you run more than one of it.**

One `~/.claude` holds one setup: one set of settings and hooks, one memory,
one login, one environment, all with full access to your machine. `cpb` gives
you as many Claude Codes as you need, each behind its own command, each
isolated as far as you choose, and each described in a file you can apply
anywhere:

| Layer | What each playbook gets | How |
|---|---|---|
| **Config home** | its own `CLAUDE.md`, `settings.json`, hooks, memory, history, sessions, plugins, MCP servers and skills; `~/.claude`'s own `CLAUDE.md` and rules stay out of it. Your `~/.claude` never moves. | every playbook (`global_instructions = 'excluded'`, the default) |
| **Environment** | where its requests go and with which credential, from an account; its own variables, set or blocked at launch: a proxy, a feature flag, a tool's setting. Your shell stays as it is. | `USE ACCOUNT`; `USE ENV`, `SET VAR`, `BLOCK VAR` |
| **Login** | its own Claude login, sharing nothing with `~/.claude` | `SET login = 'isolated'` |
| **Process** (experimental) | a microVM with its own kernel, filesystem and network. It sees only your working directory and its own config, and your machine's login never enters it. By default a host-side proxy injects your backend API keys, so the sandbox never holds them ([sandbox guide](docs/guides/sandbox.md#secrets)). | `SET sandbox.always = true`, or `--sandbox` on any launch |

```bash
cpb CREATE PLAYBOOK work                           # a playbook, and a `work` command that opens it
cpb CREATE PLAYBOOK side SET login=isolated   # a second Claude login, running beside the first
cpb CREATE PLAYBOOK sre SET sandbox.always = true, login = isolated   # every launch inside a microVM
work                                               # Claude Code, bound to that playbook
```

![cpb demo](docs/demo.gif)

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/ramazanpolat/claude-playbooks/main/install.sh | sh
```

Linux and macOS, amd64/arm64. Installs one executable, `cpb`; `cpb self-update` updates it. [devbox, Nix, npx and source builds →](docs/guides/installation.md)

## What you can do with it

### Try things without touching your daily setup

A new hook, plugin, model or `CLAUDE.md` goes into a scratch playbook, not into
`~/.claude`:

```bash
cpb CREATE PLAYBOOK scratch
cpb ALTER PLAYBOOK scratch ADD MARKETPLACE mine FROM '~/src/my-plugins' ADD PLUGIN hello@mine
scratch                                            # try it
cpb DROP PLAYBOOK scratch --yes                    # gone; ~/.claude was never touched
cpb start /tmp/spike --delete                      # or a throwaway session in a throwaway folder
```

### Try someone's playbook safely

`cpb play frontend-craft` (a template, a URL, `github:…@tag` or a file) shows exactly what the recipe would do and asks first. A recipe never picks where your requests go (`--account` runs it on one of your accounts); a proxy or secret must be typed to confirm, the session runs in a sandbox where one is available, and it is removed when it ends; `--keep` keeps it as yours. [Try someone else's playbook →](docs/guides/play.md)

### Route one playbook to another model

An account is one record: where requests go, how they authenticate, and the
provider's model names. Every launch derives its address, key and models from
one account, so an address never changes while its key stays behind.

```bash
cpb CREATE ACCOUNT glm SET provider = gateway, base_url = http://localhost:8080/v1, auth = none, model = glm-5.3
cpb CREATE PLAYBOOK glm SET login=isolated USE ACCOUNT glm
cpb EXPLAIN PLAYBOOK glm      # Account: glm (gateway http://localhost:8080/v1, …)
```

A router that takes a key holds it by reference, `auth_token FROM '<ref>'`, resolved by your [secret helper](SPEC.md#secrets-optional). `login = 'isolated'` keeps your Anthropic login away from that provider; a new playbook keeps `~/.claude`'s `CLAUDE.md` out (`global_instructions = 'excluded'`) and its own imports nothing. [Accounts →](docs/guides/accounts.md) · [Environment →](docs/guides/environment.md) · [two logins side by side →](docs/guides/authentication.md)

### Let an agent loose without letting it near your machine

Experimental: the sandbox works and is supported in v4, but its flags and `sandbox.*` keys may change or move to a separate runner without the usual notice.
With `--sandbox`, Claude Code runs in a [Docker Sandbox](https://docs.docker.com/ai/sandboxes/)
that sees your working directory and the playbook's own directory, and not
your home, `~/.claude`, your shell's environment or your other playbooks.

```bash
cpb run --sandbox work                                         # this folder is the workdir
cpb run --sandbox --sandbox-fresh --clone --workdir ~/untrusted-repo work   # a private clone; your tree is untouched
cpb run --sandbox --mount ~/shared-libs:ro work                # one more directory, read-only
cpb run --sandbox-host me@buildbox work                        # the same launch, sandboxed on another machine
```

By default your backend API keys stay on the host: the sandbox sees a
placeholder that a host-side proxy swaps for the real key, for that endpoint
only ([when a key does go in](docs/guides/sandbox.md#secrets)). A playbook with `sandbox.always = true` is sandboxed on every launch. Needs `sbx`. [Sandbox →](docs/guides/sandbox.md)

### Put the whole setup in a file

Every statement above can live in a file. `APPLY` checks all of it before
writing anything, `--dry-run` shows every change and command, and applying
twice changes nothing.

```
-- reviewer.cpb: a recipe (it names no playbook)
INCLUDE 'base.cpb';                                  -- shared layers stack
ALTER PLAYBOOK
  ADD MCP SERVER github URL 'https://api.githubcopilot.com/mcp/'
      HEADER 'Authorization' FROM 'keychain:github-mcp'   -- a secret by reference
  ALLOW TOOL 'Bash(gh pr *)'  DENY TOOL 'Bash(git push *)'
  ADD SKILL review FROM 'https://github.com/me/skills' SUBDIR review
  SET model = 'claude-opus-5-5';
```

```bash
cpb APPLY reviewer.cpb TO reviewer --dry-run       # the plan, statement by statement
cpb APPLY reviewer.cpb TO reviewer                 # build it; the playbook is created if missing
cpb SHOW CREATE ALL > machine.cpb                  # this whole machine, as statements
cpb APPLY machine.cpb                              # on the next machine
```

Secrets are references, resolved at launch by your helper; `SHOW CREATE` never
prints a value. A recipe covers the whole agent (plugins, MCP servers, tools,
status line, model and `/model` picker, skills) and applies to any
playbook or to `~/.claude`. A playbook can also be installed from a source, a git repository or a folder (`FROM <url-or-dir> BRANCH <ref>`), or run in place from a folder (`LINK <dir>`).

### See everything that is running

```bash
cpb SHOW SESSIONS                                  # live Claude Code sessions, in every playbook
cpb run work --continue                            # this folder's latest, in its playbook; never one live elsewhere
cpb tui                                            # browse playbooks, sessions and env sets; never shows a secret
cpb "SELECT name, envs, login FROM PLAYBOOKS"   # state as tables; add ClickHouse for full SQL
```

## Built to be relied on

- **Tested on every change:** CI applies all 23 examples and, on Linux and
  macOS, upgrades from the previous release and checks the state reads the
  same. A full arena regression runs on the exact commit each release is
  tagged from.
- **Another Claude login never replaces yours:** cpb sets it aside rather
  than copying it over `~/.claude`'s.
- **Your Claude login never goes to another provider:** an account with
  another provider's key keeps every Claude login out of its runs.

## Learn it

| | |
|---|---|
| [Your first playbook.cpb](docs/tutorials/first-playbook.md) | create, route, run, export, apply elsewhere |
| [Stack layers into an agent](docs/tutorials/stacked-agent.md) | base -> an agent -> a team layer, as recipes |
| [Examples 01-23](examples/) | one `playbook.cpb` per idea, from a first playbook to sessions, the TUI, `cpb play` and accounts, all applied in CI |
| [Ask in plain words](docs/guides/assistant.md) | `cpb ask "<request>"` shows the cpb command that does it |
| [Guides](docs/README.md) · [SPEC.md](SPEC.md) | how-tos for every area · every statement, command, file and output format: the contract |
| [Changelog](CHANGELOG.md) · [Contributing](CONTRIBUTING.md) | what changed in each release · development |

## License

Apache-2.0 ([LICENSE](LICENSE), [NOTICE](NOTICE)).
