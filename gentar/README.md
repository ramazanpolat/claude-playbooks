# claude-playbooks × gentar (own arena)

This repo carries its own [gentar](https://github.com/agent-realm/gentar)
arena: scenarios live here, run here, and every run writes a report you
can hand to an agent to fix what failed.

## What this directory declares

| Declaration | Where | This repo's value |
|---|---|---|
| subject name | `subject = "…"` in every scenario TOML (`run.sh` reads it from there) | `claude-playbooks` |
| suites | `scenarios/*.toml` — decisions + reality assertions | see below |
| credentials | `credentials = [names]` per suite — entries are ALTERNATIVES, a list entry is an all-of group (`["KEY", ["TOKEN","BASE_URL"]]` = the key alone, or the token and its endpoint together). None present refuses (exit 2) before a bench exists | per suite |
| trigger | `.github/workflows/gentar-arena.yml` (and/or a dispatch job into a central arena) | see workflow |
| engine pin | `GENTAR_REF` in `run.sh` — a release tag, re-fetched every run | `v0.3.0` |

## Quickstart (local)

Prereqs: Docker, and network reach to a **bench-host** you provide: any
Linux machine with [`sbx`](https://docs.docker.com/ai/sandboxes/)
installed and logged in once, reachable over SSH.

```bash
gentar/run.sh --stage-engine   # clone the pinned engine; no Docker, no bench
gentar/dryrun.py               # replay every suite locally (~1s)
gentar/run.sh cli-head-build   # the real thing
ls gentar/reports/             # report-<run_id>.md per run
```

`--stage-engine` also seeds `gentar/.arena/.env` from the engine's
`.env.example`, whose values are **placeholders**. Set
`GENTAR_BENCH_HOST` and `GENTAR_BENCH_USER` there to your own machine,
and `GENTAR_BENCH_KEY_FILE` (there or in the shell) to the key that
reaches it. gentar ships no bench-host: left unset the coordinator
refuses with exit 2 before any bench exists, and left as the shipped
placeholder the run fails at ssh with `Could not resolve hostname
bench.example.internal`.

Exit code is the verdict: `0` pass · `1` fail · `2` usage/config
refusal.

Two arenas on one Docker host collide on the published ClickHouse and
OTLP ports. Move yours without editing anything — the engine's compose
file reads both as env knobs:

```bash
GENTAR_CLICKHOUSE_HOST_PORT=8124 GENTAR_OTLP_HOST_PORT=14320 \
  gentar/run.sh cli-head-build
```

A run leaves no containers or volumes behind, guaranteed twice over.
(Plain `docker compose run --rm` would not: `--rm` removes only the
coordinator, while `clickhouse` and `otelcol` come up via `depends_on` as
ordinary `up` containers and `clickhouse` owns a named volume.)

- **Every container is `--rm`.** The compose spec has no per-service
  auto-remove key and `compose up` has no `--rm`, so the runner starts
  each arena service as a one-off `compose run -d --rm` — the only way to
  get daemon-level `AutoRemove`. A stopped container is then removed by
  the Docker daemon itself, whatever stopped it: ctrl-c, `SIGKILL`, OOM,
  or the runner dying before its trap can fire.
- **A trap tears the project down anyway** — pass, fail, refusal and
  ctrl-c alike — covering the network and anything else left over.

Check residue by this project's own label, never global counts (another
arena may share the host):

```bash
docker ps -a --filter label=com.docker.compose.project=arena-claude-playbooks
```

To keep a stack up and inspect ClickHouse, set `GENTAR_KEEP_ARENA=1`; you
then own the teardown, which the runner prints as two commands. Both are
needed: `compose down` alone refuses the network with "Resource is still
in use", because it does not stop one-off containers.

## When this repo's code changes

The suites here assert what is true of this repo, so the two move
together.

- **Code changed, behaviour did not** — nothing to do. The PR trigger
  re-runs the suites against the change before it lands; the pass is the
  evidence.
- **Behaviour changed** — the scenarios change in the **same pull
  request**. A scenario asserts reality; stale reality fails honestly,
  and that failure is the suite working. New behaviour is usually a new
  suite: dry-run it, run it once for real, ship it with the feature.
  Splitting the code change and the scenario change across two PRs
  leaves main red in between, and a red main teaches people to ignore
  the arena.
- **The engine changed** — nothing happens until someone bumps
  `GENTAR_REF` in `run.sh`. That is a deliberate change: bump, run every
  suite, commit the bump on its own with the outcome in the message.
- **The repo grew behaviour nothing asserts** — the case with no
  failure. Existing suites still pass, the board stays green, and
  coverage decays quietly. Nothing catches this by running; someone has
  to look.

```bash
gentar/run.sh --review     # no engine, no Docker, no bench
```

It lists what the repo ships that no suite mentions, and the diff since
the scenarios last changed. It **reports and stops** — never fails,
never writes. A gap is a question, not a defect: some of those should
have a suite and some never will, and deciding which needs someone who
has read the repo. Worth running when a feature lands, or periodically.

Its blind spot, stated so you do not trust it too far: it compares
shipped executables and scripts against names the suites mention, so a
**behaviour change inside a file a suite already names** does not show
up. The diff is there for that.

## Narrowing a pull request

A PR runs every suite it can. The bench-host is one shared machine, so a
README typo and a rewrite of the install path cost the same wall-clock —
and when the expensive tier makes every PR slow, people stop running it
at all. A PR can say what it needs:

```
gentar: auth-flow config-migration
```

anywhere in the **PR body**, one line. Those suites run, plus whatever
`GENTAR_FLOOR` names, and nothing else. No line means today's behaviour:
everything runnable.

**Set `GENTAR_FLOOR`** (a repo variable) to the cheap deterministic
suites. They run whatever a PR declares, so a too-narrow pick costs
coverage on the slow tier and never on the fast guard rails.

Two deliberate limits:

- **Declared, not inferred.** A rule that reads the diff and picks for
  you fails by silently *excluding* the suite that mattered — a green PR
  that never tested the change, which is the one outcome this engine
  exists to refuse. A human narrowing on purpose is visible in the PR and
  reviewable like any other claim in it.
- **Only a PR narrows.** Pushes to the default branch and `v*` tags
  ignore the declaration and run everything, so nothing a PR skipped
  stays skipped.

A suite name is letters, digits, dot, dash, underscore; anything else in
that line is refused with exit 2 before a bench is spent — a PR body is
text a stranger can write.

## The fix loop

Every terminal outcome writes `gentar/reports/report-<run_id>.md`
stating what ran (every step, with output), what was asserted and what
it actually saw, and a reproduce command (`gentar/run.sh <suite>` — the
runner rewrites the engine's central-arena default on copy). On a
failure, that file is a work order:

```
Read gentar/reports/report-<id>.md, fix the repo, rerun
`gentar/run.sh <suite>`, iterate until it passes.
```

The subject is the **working tree** (uncommitted changes included) — fix
and rerun, no commit needed to test.

Exit `1` is a verdict: an assertion saw something other than the claim.
Exit `2` is a refusal before any bench existed — a missing credential,
an unknown scenario name, a budget cap below the suite's declared spend.
A refusal is a usage error in the invocation, never a red test.

The engine is pinned by `GENTAR_REF`, re-fetched and re-checked-out on
every run, **and the coordinator image is rebuilt from it**. That last
part is not optional: the compose service is `build: ./coordinator`, so
without a build step `docker compose run` reuses a cached image, and on a
long-lived runner that image drifts months behind the source while
`GENTAR_REF` looks perfectly honoured. A fresh checkout is not a fresh
engine.

## Suites

`gentar/run.sh --review` is the live inventory: every suite with its
assertion count, and what the repo ships that no suite names. This table
only says what each suite is for.

| Suite | Proves |
|---|---|
| `cli-head-build` | the CLI builds from THIS checkout, uncommitted changes included, and reports its version |
| `cli-release-install` | the README's documented install path works against a published release |
| `docs-honesty` | the surface README and `docs/` document exists in the shipped binary and checkout |
| `playbook-lifecycle` | create / alias / rename / delete, each stage checked against the filesystem |
| `playbook-install-local` | `install` from a local directory |
| `playbook-link` | `link` develop-in-place, and a delete that must not follow the symlink |
| `playbook-update` | native `update` onto a newer source; the withdrawn `--all` explains itself |
| `launcher-run-version` | `run` launches claude with the playbook wired (keyless, via `--version`) |
| `env-overrides` | manifest `[env]`, env profiles and launch flags reach the child process |
| `config-dir-override` | `CLAUDE_CONFIG_DIR_OVERRIDE` end to end |
| `auth-status` | `auth status` reports without touching anything |
| `pilot-interactive-delete` | a simulated pilot answering prompts on a pty |
| `pilot-self-uninstall` | a simulated pilot at the most destructive prompt the tool has |
| `pilot-agent-session` | a REAL agent launched through a playbook, governed by it (needs a credential) |

## This subject's adaptations of the kit

`run.sh` is the pristine v0.3.0 kit. Everything this repo changed is below;
carry it forward when re-copying the kit for an engine bump.

- **No `pull_request` trigger** in the workflow (one line removed, with a
  note). The repo is public and the runner persistent; the pilot chose no PR
  trigger at all, on top of requiring approval for all external
  contributors. Test before merge with the `arena` / `arena-<suite>` tags.
- **Ports pinned** to 8126 (ClickHouse) and 4320 (OTLP) in the workflow: the
  `arena` runner is shared with other arenas.
- **`dryrun.py` hooks**: `prepare()` builds `claude-playbook` the way the
  bench does; `SKIP_STEP_SUBSTR` skips the container build and install it
  replaces; `HIDE_FROM_PATH` hides `cpb` (suites create it) and `pilot`
  (`create`/`install` call it, and a bench has none).
- **`cpb-agent-bench-v1`** is this repo's own bench image, built by
  `bench-template/build.sh` on the bench-host, used only by
  `pilot-agent-session`.

## Before you push a suite

```bash
gentar/dryrun.py                                   # every suite
gentar/dryrun.py gentar/scenarios/cli-head-build.toml
```

Needs **python 3.11+, or 3.9/3.10 with `tomli`** (`pip install tomli`) —
it parses TOML on your machine, and `tomllib` only became stdlib in 3.11
while stock macOS still ships 3.9. It re-execs under a newer interpreter
if one is on PATH, so on most machines this is invisible. The arena is
unaffected: the coordinator runs python 3.12 in a container.

Runs a suite's steps, driver turns and assertions in a scratch home in about
a second — no bench, no sandbox, no network. A scenario is shell inside TOML,
three levels of quoting deep, and the arena was the only thing that ever ran
it: one missing quote cost a bench VM and several minutes to find. This finds
it before the push.

It needs a checkout of the engine for its scenario parser — the one
`gentar/run.sh --stage-engine` makes, so a suite is validated by the
same engine version that will run it. `GENTAR_ENGINE=/path/to/gentar/coordinator`
points it at an existing checkout instead.

The layout mirrors a bench: your checkout is staged into `WORKSPACE_DIR`,
which sits *under* `HOME` rather than being it, and steps run with the
workspace as cwd. So a `~/…` assertion asks about the pilot's home, never
about a file that shipped in your repo.

It is not a substitute for the arena. There is no sandbox, no template and no
network policy, so it proves the shell and the assertions while the arena
proves the isolation. Two kinds of suite it will not claim to have checked:
those declaring `credentials` are skipped (they need a real agent and a real
key), and those whose `[driver]` uses `pick` or `abort` turns come back
`UNVERIFIED` with a nonzero exit — those need the real driver, and a picker
that never matched must not read as a pass.

Add a suite = add a TOML here. The schema is the engine's
`coordinator/gentar/toml_scenario.py` — read the pinned copy under
`gentar/.arena/` after staging, since that is the parser your suite will
face. Decisions and reality assertions, never scripts.

## CI (`.github/workflows/`)

The arena workflow runs every suite on pull requests, pushes to main,
and `v*`/`arena*` tags (edit its `on:` block to taste — triggers are
yours, the arena doesn't care; the PR trigger's cost is documented in
the file). It needs a self-hosted runner labeled `arena` with Docker +
reach to the bench-host; GitHub-hosted runners cannot reach an internal
bench-host. One-time setup, ~5 min on any always-on machine with Docker:

GitHub → this repo → Settings → Actions → Runners → New self-hosted
runner → follow the commands → when configuring, labels: `arena`.

Secrets/vars the workflow reads:

- `secrets.BENCH_SSH_KEY` — key the coordinator uses to reach the bench-host
- `secrets.GENTAR_BENCH_HOST`, `secrets.GENTAR_BENCH_USER` — the bench-host itself; the
  engine ships none, so without these no suite can run (secrets, because a public
  repo's logs are public)
- `secrets.GENTAR_CLONE_KEY` — read-only deploy key, only if the ENGINE repo is private
- `vars.GENTAR_REPO_URL` — only to clone the engine from a fork or mirror
- `secrets.ANTHROPIC_API_KEY` or `secrets.ANTHROPIC_AUTH_TOKEN` + `vars.ANTHROPIC_BASE_URL` — agent suites
- `vars.ANTHROPIC_DEFAULT_{SONNET,OPUS,HAIKU,FABLE}_MODEL` — all four, for a routed endpoint
- `GENTAR_BUDGET_CAP` in the workflow — ceiling the budget guard enforces

The three bench values are required; the workflow refuses with a named error
before staging anything if one is missing or still the placeholder. Everything
else is optional. The workflow's sweep is `gentar/run.sh --sweep`: suites whose
credentials are absent are skipped and named, not run into a red refusal. It
tears down with `gentar/run.sh --down`.

**Credential grouping.** `credentials` lists *alternatives*. A provider that is
a pair must be a nested list — `[["ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL"]]`.
Written flat, the two are either-or: the token wins alone and the URL is
dropped. The engine warns when a declared credential is set but not forwarded,
in the log and in the report.

The workflow stages the checkout exactly like `run.sh` does, so local
and CI run the same way.

## Layout

```
gentar/
  scenarios/*.toml   # suites (this repo's own)
  run.sh             # local kickoff — stage, run, report
  dryrun.py          # local, bench-less step/assertion replay
  reports/           # run reports land here (gitignored)
  .arena/            # gentar checkout (gitignored, auto-cloned)
```
