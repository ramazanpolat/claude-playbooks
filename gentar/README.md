# claude-playbooks × gentar (own arena)

This repo carries its own [gentar](https://github.com/agent-realm/gentar)
arena: scenarios live here, run here, and every run writes a report you
can hand to an agent to fix what failed.

## Quickstart (local)

Prereqs: Docker, and network reach to a **bench-host** (any Linux
machine with [`sbx`](https://github.com/docker-sandbox) — the default
`.env` points at the shared one; change it if you host your own).

```bash
gentar/run.sh cli-head-build      # build CLI from THIS tree, prove it
gentar/run.sh cli-release-install # the README's documented install path
ls gentar/reports/                # report-<run_id>.md per run
```

First run clones gentar into `gentar/.arena` and seeds `.env` from
`.env.example` — edit that if your bench-host differs.

Exit code is the verdict: `0` pass · `1` fail · `2` usage/config
refusal.

## The fix loop

A failing run writes `gentar/reports/report-<run_id>.md` stating: what
ran (every step, with output), what was asserted and what it actually
saw, and a reproduce command (`gentar/run.sh <suite>` — the runner
rewrites the engine's central-arena default on copy). Feed it to an
agent:

> Read gentar/reports/report-<id>.md, fix the repo, rerun
> `gentar/run.sh cli-head-build`, iterate until pass.

The subject is your **working tree** (uncommitted changes included) —
fix and rerun, no commit needed to test.

## Suites

| Suite | Proves |
|---|---|
| `cli-head-build` | CLI builds from this checkout (nested-Docker golang) and works: version floats from `git describe`, `list`, `--help` |
| `cli-release-install` | the README's install path verbatim, pinned release |
| `playbook-lifecycle` | create → list → info → alias → rename → delete, each checked against the filesystem, not the command's own output |
| `playbook-install-local` | `install` from a local directory: manifest-named target, faithful copy, launcher, delete cleans both |
| `launcher-run-version` | `run` (and the launcher alias) actually spawns claude with the playbook wired — keyless, via `--version` |
| `docs-honesty` | README's documented commands answer `--help` in the built binary; referenced files exist and stay executable |
| `env-overrides` | manifest `[env]`, an attached env profile, one-off launch flags (before/after the name, via the launcher), an env file, and the `start -- --delete` boundary, all proven against the environment a stub `claude` actually received |
| `auth-status` | `auth status` table and JSON for a fresh playbook (shared-login, no grant), read-only against a timestamp marker, unknown-name refusal, and `isolate_auth` reported as isolated |
| `playbook-update` | native `update`: `--check` installs nothing, content moves, `settings.json` and `.claude.json` survive, the migration runs with the version pair, entries are backed up — and `--all` skips what it cannot update, leaves up-to-date playbooks alone, and refuses contradictory flags |
| `playbook-link` | `link` develop-in-place: the entry is a symlink, edits outside are live inside, native update is refused, and `delete` removes the link without following it |
| `config-dir-override` | `CLAUDE_CONFIG_DIR_OVERRIDE` end to end against the environment a stub `claude` received: honoured, consumed, a bare `CLAUDE_CONFIG_DIR` still ignored, refused in a layer, refused relative, refused with `--sandbox`, and reported-then-ignored by `start` |

### Simulated pilots

Suites with a `[driver]` block drive a **pty** instead of running a shell
script, so paths that need a human at the terminal become testable. Everything
above runs headless, where a confirmation prompt reads EOF and cancels — which
is why `delete`'s interactive path had never been exercised at all.

| Suite | Proves |
|---|---|
| `pilot-interactive-delete` | a scripted pilot declining a `delete` (nothing happens) and then confirming it (the playbook and both its launchers go, a bystander does not), both branches in one pty session |
| `pilot-agent-session` | a **real agent**, launched through a playbook, doing a real task — and the marker it writes carries a token only the playbook's own `CLAUDE.md` supplied, so the file is proof the playbook governed the session. Needs an agent credential; left out of the sweep when none is set |

## Before you push a suite

```bash
gentar/dryrun.py                                   # every suite
gentar/dryrun.py gentar/scenarios/playbook-update.toml
```

Runs a suite's steps, driver turns and assertions in a scratch `HOME` in about
a second — no bench, no sandbox, no network. A scenario is shell inside TOML,
three levels of quoting deep, and the arena was the only thing that ever ran
it: one missing quote cost a bench VM and several minutes to find. This finds
it before the push.

It is not a substitute for the arena. There is no sandbox, no template and no
network policy, so it proves the shell and the assertions while the arena
proves the isolation. Suites declaring `credentials` are skipped.

Add a suite = add a TOML here. Schema and vocabulary:
[gentar scenario schema](https://github.com/agent-realm/gentar/blob/main/coordinator/gentar/toml_scenario.py)
— decisions and reality assertions, never scripts.

## CI (`.github/workflows/arena.yml`)

Triggers (edit to taste — this is your workflow, conditions are yours):

- `push` to `main` — every suite
- `push` of tags `v*` — every suite against the tagged commit (release proof)
- **keyword tags** — run the arena on ANY commit, no merge needed:
  - `git tag arena && git push origin arena` → every suite, at that commit
  - `git tag arena-<scenario> && git push origin arena-<scenario>` → one suite
    (e.g. `arena-playbook-lifecycle`); unknown name fails fast with exit 2
  - re-run by deleting and re-pushing the tag
- `workflow_dispatch` — pick a scenario manually

Runs on a self-hosted runner labeled `arena` (needs Docker + reach to
the bench-host; GitHub-hosted runners cannot reach an internal
bench-host). All suites run, the job fails if any failed, and reports
upload as an artifact either way. The bench key lives in the runner's
per-job temp dir — nothing secret persists in the workspace. Dispatch
inputs reach the shell through `env`, never through `${{ }}`
interpolation. The engine itself comes from `GENTAR_REF` (default
gentar's `main`) and runs with bench-key reach — pin the ref to an
audited SHA if that trust boundary matters to you. One-time setup,
~5 min on any always-on machine with Docker (the pilot's Mac
qualifies):

> GitHub → this repo → Settings → Actions → Runners → New self-hosted
> runner → follow the commands → when configuring, labels: `arena`.

The workflow stages the checkout exactly like `run.sh` does, so local
and CI run the same way.

## Layout

```
gentar/
  scenarios/*.toml   # suites (this repo's own)
  run.sh             # local kickoff — stage, run, report
  reports/           # run reports land here (gitignored)
  .arena/            # gentar checkout (gitignored, auto-cloned)
```
