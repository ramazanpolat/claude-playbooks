# AGENTS.md

For an agent (Claude Code, Codex, Gemini, OpenCode, …) asked to install,
verify, update, deploy or uninstall **claude-playbooks** (`cpb`,
`cpb`). Each step gives the exact command and how to tell it
worked. The human docs are linked, not repeated.

If you are writing or driving playbooks rather than installing the tool,
read [docs/guides/agent-guide.md](docs/guides/agent-guide.md). If you are
preparing a release, read [Before any release](#before-any-release) first.

## Safety rules

- **Never handle a secret value.** Do not ask the human to paste a token, do
  not write one into a file, a command line or a message. Store secrets by
  reference (`SET … FROM '<ref>'`, see
  [docs/reference/cli-grammar.md](docs/reference/cli-grammar.md), "Secrets").
- **Ask the human first** before: `cpb self-uninstall`, `DROP PLAYBOOK`,
  `APPLY … --yes`, `APPLY … TO '<dir>'`, deleting anything under
  `~/.claude-playbooks/`, and any login (`/login`, `claude setup-token`):
  those need a person.
- **A playbook for a non-Anthropic route, or a throwaway,** is created
  `CREATE PLAYBOOK <name> ISOLATED LOGIN`. Without `ISOLATED LOGIN`, a
  `/login` in it writes through to the machine's login. Its CLAUDE.md goes to
  that provider with every request: never add imports to it, and if an
  existing playbook's CLAUDE.md imports files, report it to the human rather
  than editing it yourself.
- **`cpb play` confirmations are the human's.** Never pass `--yes`,
  `--trust-endpoint` or `--trust-secret` on a human's behalf without their
  explicit go: typing the host or the secret is how a person agrees that their
  requests, or a secret, may go there, and an agent must not agree for them.
  To inspect a recipe, use `cpb play <ref> --dry-run --json` (or `--check`),
  which runs nothing.
- **Do not edit** an installed playbook's files by hand; change state
  through `cpb` statements. Do not touch `~/.claude` (the machine's own
  Claude Code config) unless the human asks; then use
  `APPLY … TO '~/.claude' --dry-run` first, and apply only on their yes.

## Prerequisites

| Need | Check | Expected |
|---|---|---|
| macOS or Linux | `uname -s` | `Darwin` or `Linux` |
| curl | `command -v curl` | a path, exit 0 |
| Claude Code, to launch playbooks | `command -v claude` | a path, exit 0 (install it from https://claude.ai/download if missing; `cpb` itself installs without it) |
| Claude Code 2.1.268+, for the plugin clauses | `claude --version` | `2.1.268` or newer (`ADD PLUGIN` / `DROP PLUGIN` refuse an older claude in one line; nixpkgs has shipped older ones) |
| Claude Code 2.1.242+, for the model picker to show | `claude --version` | `2.1.242` or newer reads `modelPicker`, 2.1.257+ its `behavesAs` (the `BEHAVES AS` part of `ADD MODEL`). cpb writes the key either way |

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
cpb APPLY playbook.cpb --dry-run --json   # the same plan as one JSON object (schema 1): parse this, not the lines
cpb APPLY playbook.cpb             # exit 0; ends "Applied …: N created, N changed, …"
cpb APPLY playbook.cpb             # verify: "0 created, 0 changed" (the file holds)
cpb EXPLAIN PLAYBOOK <name> --json # verify: the variables, plugins, agent, MCP servers, tools and model a launch gets
```

A **recipe** (an `ALTER PLAYBOOK` that names no playbook) needs a target:
`cpb APPLY recipe.cpb TO <playbook>`, or a `USE PLAYBOOK <name>;` line in the
file. Without one it is refused before anything is written.

- A file with `DROP PLAYBOOK` refuses without `--yes`: show the human the
  dry run's list and ask before adding it.
- `ADD MARKETPLACE` / `ADD PLUGIN` run `claude plugin …` and fetch from the
  network; `claude` must be on `PATH`. If one fails because a plugin wants to
  run a command its marketplace declares, show the human the command it
  printed; confirming it is theirs to do.
- A failure stops the run and says what already ran. Fix the file and apply
  it again; every statement is safe to repeat.
- `ADD MCP SERVER` runs `claude mcp add-json`; `ADD SKILL` from a git
  source clones it. Both need `claude` or `git` on `PATH` and, for a remote
  source, the network.
- A `SET … FROM '<ref>'` that fails its check means the secret is not stored:
  ask the human to store it with their secret helper. Never ask for the
  value.

The statements and file rules:
[docs/reference/cli-grammar.md](docs/reference/cli-grammar.md). Worked
examples: [examples/](examples/).

## Update

```sh
cpb self-update --check   # exit 0; says whether a newer release exists
cpb self-update           # installs the latest release
cpb --version             # verify: the new version
```

A binary installed through devbox or Nix is never replaced by `cpb self-update`
(it refuses and says so): change the tag in the devbox project instead
(below).

## Deploy in a devbox project

```sh
devbox add "git+https://github.com/ramazanpolat/claude-playbooks?ref=refs/tags/<tag>#cpb"
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

## The OpenShell sandbox backend (maintainers)

`--sandbox=openshell` runs only on a Linux host with
OpenShell 0.1.x (see [the sandbox guide](docs/guides/sandbox.md#openshell-backend-linux)).
CI cannot run it. Two things keep it honest:

- **Unit tests** (`cmd/sandbox_openshell_test.go`) put fake `openshell`,
  `docker`, `systemctl` and `loginctl` first on PATH and check every call. They
  also check that a key's value reaches `openshell` only in its environment. A
  change to the backend seam must also leave `TestSbxCallLogGolden`
  (`cmd/testdata/sbx-golden/`) passing unchanged: that test pins the whole
  sbx launch.
- **The end-to-end run** (`tests/openshell-e2e.sh <cpb binary>`) runs
  on a disposable Linux host with OpenShell, never on a workstation. It uses dummy keys, restarts the gateway twice and restores it.
  It must end `0 failed`, including the Claude Code TUI under the generated
  policy.

**Bumping the Claude Code the image carries** (`openshellClaudeVersion` in
`cmd/sandbox_openshell.go`):

1. Change the constant, build the binary, and run `tests/openshell-e2e.sh` on
   such a host. Every step must pass, including the TUI.
2. Only then commit the new pin.
3. Say it in the release notes: "OpenShell sandboxes: Claude Code A → B.
   Existing sandboxes keep A until `--sandbox-fresh`."

The same run is required before accepting a new OpenShell minor, and before
changing the recipe (`cmd/openshell/Dockerfile`, whose base image is pinned by
digest).

## Before any release

No release without its docs. The maintainer's rule (2026-09-26), verbatim:

```
"1) a good readme, short, precise, represents a) what is it b) why it exists c) how it is used 2) a docs with full tutorials and some guides for some common operations 3) examples with smallest features/usages/utilities to full blown ones, each has their own readme.md files 4) an agent entry for installation and deployment, etc."
```

Before a version is tagged, check each item, for every feature in that
release:

- [ ] **README.md** says what cpb is, why it exists and how it is used:
      short and precise, with the current grammar in its first example.
- [ ] **docs/**: the tutorials, the guides for common operations, and the
      reference ([docs/reference/cli-grammar.md](docs/reference/cli-grammar.md))
      are current. Nothing built is still marked **planned**.
- [ ] **examples/** covers every new clause, from the smallest use to the
      full-blown one, each directory with its own README.md, and all of them
      pass in CI (`examples/check.sh`; `examples/coverage.sh` fails when a
      grammar clause has no reference entry, or appears in no `.cpb` file
      or `.check` command that CI runs: README prose does not count).
- [ ] **AGENTS.md** (this file) is current: install, verify, update, deploy.

**And for the release as a whole:**

- [ ] **The release notes name every change of a result:** a statement, a
      clause, a command, a flag, a file format, a `--json` shape or a code.
- [ ] **The upgrade from the previous release passes:** the CI `upgrade`
      job, on ubuntu and macOS, green on the commit to be tagged
      (`examples/upgrade.sh`). It compares with the newest release of
      `package.json`'s major, and has nothing to compare with before a
      major's first release.
- [ ] **A full arena regression (phase 2) is green on the exact commit to be
      tagged.** release.yml's gate refuses a tag without one, and
      `gentar/policy.toml` `[phase2] max_age_days = 2` refuses one older
      than two days. A re-run is allowed only for an infrastructure failure
      (the judge unreachable, a provider refusing the key); a real red means
      fix first, then a fresh pass. (The maintainer's rule, 2026-09-29. It
      replaces "7 consecutive green nights".)
- [ ] **No open security issue and no known data-loss bug.** Check
      `docs/known-issues/`.
- [ ] **Every review finding** on the release's PRs is fixed or answered
      **on the PR itself**, as a fix commit or a reply naming the finding.
      `gh pr view <n> --comments` prints the reviews and the replies to check.

**Nightlies are drift monitors (report on red).** Every night
`arena-nightly` runs phase 2 on main; a dispatch with `ref` runs another ref.
Not on a release tag: the kit's plan never gives a `v*` tag the bench, so a
tag dispatch would be skipped and read as green (v3.25.0 on 2026-09-30).
`arena-nightly` fails when a dispatched run's `arena / phase2` job is skipped
or absent, naming the ref and plan's reason. A red is fixed like any bug.
They gate nothing.

If any is missing, build it first; never tag without it.

### Releasing from a release branch

A minor is released from its own branch, `release/vX.Y`, while main
moves on. The release workflow publishes a tag only when its commit is on
main or on `origin/release/vX.Y` for the tag's own minor. Any other tag fails
the run, with the reason (`.github/scripts/release-refs.sh`). The order:

1. **The fix lands on main first**, as a normal PR.
2. **`git cherry-pick -x`** it to `release/vX.Y`, in a PR against that
   branch. Only fixes go to a release branch, never features.
3. **The release-prep commit on the branch**:
   - `package.json` → X.Y.Z, which npx serves and release.yml requires to
     equal the tag;
   - the install pins in `docs/guides/installation.md` (`refs/tags/vX.Y.Z`);
   - the reference's status line.
4. **A green full arena phase 2 on that head.** Check the arena bench is
   idle first, then dispatch `gentar-arena.yml` on the branch with no
   scenario. The run's head sha must be the exact commit you will tag, which
   is what the release gate checks. A new commit on the branch needs a new
   pass, and a pass older than two days does not count.
5. **Tag `vX.Y.Z` on that head.** The release gate also requires a green
   `arena / phase2` on the tagged sha.
6. **The main bump PR, opened at tag time.** Once vX.Y.Z is tagged, CI's
   npx check fails every push to main until main's `package.json` says
   X.Y.Z: it compares against the newest release, wherever it was tagged. The
   same commit moves the pins in `installation.md`. Merge it right after the
   release publishes.

A release cut from main (a new minor) needs only steps 3 to 5, on main: the
release-prep commit, a green full phase 2 on it, and the tag on it. Its bump
commit passes the npx check with a warning until the tag exists (the version
is ahead of the newest release, and not yet tagged). Meanwhile npx runs the
newest published release and says so on stderr (`bin/npx-shim.sh`).
