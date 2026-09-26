# AGENTS.md

For an agent (Claude Code, Codex, Gemini, OpenCode, …) asked to install,
verify, update, deploy or uninstall **claude-playbooks** (`cpb`,
`claude-playbook`). Each step gives the exact command and how to tell it
worked. The human docs are linked, not repeated.

If you are writing or driving playbooks rather than installing the tool,
read [docs/guides/agent-guide.md](docs/guides/agent-guide.md).

## Safety rules

- **Never handle a secret value.** Do not ask the human to paste a token, do
  not write one into a file, a command line or a message. Store secrets by
  reference (`SET … FROM '<ref>'`, see
  [docs/reference/cli-grammar.md](docs/reference/cli-grammar.md), "Secrets").
- **Ask the human first** before: `cpb self-uninstall`, `DROP PLAYBOOK`,
  `APPLY … --yes`, deleting anything under `~/.claude-playbooks/`, and any
  login (`/login`, `claude setup-token`): those need a person.
- **Do not edit** an installed playbook's files by hand; change state
  through `cpb` statements. Do not touch `~/.claude` (the machine's own
  Claude Code config).

## Prerequisites

| Need | Check | Expected |
|---|---|---|
| macOS or Linux | `uname -s` | `Darwin` or `Linux` |
| curl | `command -v curl` | a path, exit 0 |
| Claude Code, to launch playbooks | `command -v claude` | a path, exit 0 (install it from https://claude.ai/download if missing; `cpb` itself installs without it) |

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/ramazanpolat/claude-playbooks/main/install.sh | sh
```

Verify:

```sh
cpb --version           # exit 0, prints the installed version
command -v cpb          # a path, exit 0
```

If `cpb` is not found, the install directory is not on `PATH`: the script
printed a warning naming it and the `export PATH=…` line to add. Add it, open
a new shell, and verify again. Other install methods (devbox, Nix, npx, from
source): [docs/guides/installation.md](docs/guides/installation.md).

## Verify a working setup

```sh
cpb SHOW PLAYBOOKS --json   # exit 0, a JSON array (empty on a fresh machine)
cpb auth status             # exit 0, reports how each playbook authenticates
```

Parse `--json` output, never the human form.

## Set up from a playbook file

When the human hands you a `playbook.cpb` (or a stack of them joined by
`INCLUDE`), apply it and prove it converged:

```sh
cpb APPLY playbook.cpb --dry-run   # exit 0; one line per statement, nothing written
cpb APPLY playbook.cpb             # exit 0; ends "Applied …: N created, N changed, …"
cpb APPLY playbook.cpb             # verify: "0 created, 0 changed" (the file holds)
cpb EXPLAIN PLAYBOOK <name> --json # verify: the variables, plugins and agent a launch gets
```

- A file with `DROP PLAYBOOK` refuses without `--yes`: show the human the
  dry run's list and ask before adding it.
- `ADD MARKETPLACE` / `ADD PLUGIN` run `claude plugin …` and fetch from the
  network; `claude` must be on `PATH`. If one fails because a plugin wants to
  run a command its marketplace declares, show the human the command it
  printed; confirming it is theirs to do.
- A failure stops the run and says what already ran. Fix the file and apply
  it again; every statement is safe to repeat.
- A `SET … FROM '<ref>'` that fails its check means the secret is not stored:
  ask the human to store it (`with-secret --store <name>` or their helper's
  equivalent). Never ask for the value.

The statements and file rules:
[docs/reference/cli-grammar.md](docs/reference/cli-grammar.md). Worked
examples: [examples/](examples/).

## Update

```sh
cpb update --check      # exit 0; says whether a newer release exists
cpb update              # installs the latest release
cpb --version           # verify: the new version
```

A binary installed through devbox or Nix is never replaced by `cpb update`
(it refuses and says so): change the tag in the devbox project instead
(below).

## Deploy in a devbox project

```sh
devbox add "git+https://github.com/ramazanpolat/claude-playbooks?ref=refs/tags/<tag>#claude-playbook"
devbox run -- cpb --version       # verify: exit 0, the tag's version
```

Commit `devbox.json` and `devbox.lock`. To move to another tag, `devbox rm`
the old reference first, then `devbox add` the new one: a second
`devbox add` does not replace the first. Details and the project layout:
[docs/guides/installation.md](docs/guides/installation.md), "With devbox or Nix".

## Uninstall (ask the human first)

```sh
cpb self-uninstall --dry-run      # shows what would be removed; nothing changes
cpb self-uninstall -y             # removes the binary, launchers and ~/.claude-playbooks
cpb self-uninstall -y --keep-data # keeps ~/.claude-playbooks
```

Verify: `command -v cpb` exits non-zero. The full options and a manual
fallback: [docs/guides/installation.md](docs/guides/installation.md), "Uninstalling".

## When something fails

- Re-run the failing command and keep its exact output and exit code.
- Check [docs/guides/installation.md](docs/guides/installation.md) for the
  known cases (PATH, rate-limited GitHub API behind a shared IP).
- Report it at https://github.com/ramazanpolat/claude-playbooks/issues with
  the command, the output, `cpb --version` and `uname -a`. Never include a
  secret value; redact tokens before posting.
