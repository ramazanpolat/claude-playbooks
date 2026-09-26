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
| [Stack layers into an agent](tutorials/stacked-agent.md) | `INCLUDE`, plugins and the agent, layer by layer |

## Guides

| | |
|---|---|
| [Installation](guides/installation.md) | install script, devbox/Nix, npx, source builds, updating, uninstalling |
| [Managing playbooks](guides/managing-playbooks.md) | create, install, link, launch, rename, update, delete |
| [Authentication](guides/authentication.md) | shared logins, long-lived tokens, isolated accounts |
| [Environment overrides](guides/environment.md) | per-playbook variables and shared env profiles |
| [Sandboxed sessions](guides/sandbox.md) | running a playbook inside a Docker Sandbox microVM |
| [Agent guide](guides/agent-guide.md) | driving `cpb` unattended from an agent or CI |

## Reference

| | |
|---|---|
| [CLI grammar](reference/cli-grammar.md) | the `cpb <VERB> <OBJECT>` statements, playbook files, output formats |

The behavioral contract is [`SPEC-v4.md`](../SPEC-v4.md) in the repository root;
when a document here and the spec disagree, the spec wins. Development and
release process live in [`CONTRIBUTING.md`](../CONTRIBUTING.md).
