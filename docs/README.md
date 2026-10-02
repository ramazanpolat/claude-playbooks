# Documentation

Pages are grouped by what you came for:

- **tutorials/**: learn it once, start to finish.
- **guides/**: how to do one common task.
- **reference/**: complete and dry: every statement, flag, file and format.
- **[examples/](../examples/)**: one small `playbook.cpb` per idea, applied in CI.

## Tutorials

| | |
|---|---|
| [Your first playbook.cpb](tutorials/first-playbook.md) | create, route, run, export, apply elsewhere |
| [Stack layers into an agent](tutorials/stacked-agent.md) | `INCLUDE`, recipes, `USE PLAYBOOK`, plugins and the agent, layer by layer |

## Guides

| | |
|---|---|
| [Installation](guides/installation.md) | install script, devbox/Nix, npx, source builds, updating, uninstalling |
| [Managing playbooks](guides/managing-playbooks.md) | create, install, link, launch, rename, update, delete |
| [Authentication](guides/authentication.md) | shared logins, long-lived tokens, isolated accounts |
| [Environment overrides](guides/environment.md) | per-playbook variables and shared env profiles |
| [Sandboxed sessions](guides/sandbox.md) | running a playbook inside a Docker Sandbox microVM |
| [Try someone else's playbook](guides/play.md) | `cpb play`: preview, confirm, run in a throwaway playbook, keep, update |
| [Query with SQL](guides/query-with-sql.md) | `cpb SELECT …`, and `cpb SHOW … --json` piped into `ch local` |
| [Configure an agent](guides/configure-an-agent.md) | MCP servers, tools, status line, model, skills; one recipe for many targets |
| [The terminal UI](guides/tui.md) | `cpb tui`: browse playbooks, sessions and env sets; SHOW CREATE, copy, export, resume |
| [Resume a session](guides/resume-a-session.md) | `cpb sessions`, `RESUME`, and the resume line a launch prints |
| [Agent guide](guides/agent-guide.md) | driving `cpb` unattended from an agent or CI |

## Reference

| | |
|---|---|
| [CLI grammar](reference/cli-grammar.md) | the `cpb <VERB> <OBJECT>` statements, playbook files, output formats |

## Design and history

| | |
|---|---|
| [Design decisions](design/decisions.md) | the decisions behind the grammar, with when they were made |
| [History](history/) | notes kept for the record, not maintained |

The behavioral contract is [`SPEC-v4.md`](../SPEC-v4.md) in the repository root;
when a document here and the spec disagree, the spec wins. Development and
release process live in [`CONTRIBUTING.md`](../CONTRIBUTING.md).
