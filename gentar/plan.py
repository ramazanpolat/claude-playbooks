#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Ramazan Polat
"""This repo's run policy: which suites run when, decided once.

    gentar/plan.py plan     # what should THIS CI event run?  (the kit's plan job)
    gentar/plan.py lint     # bench-free adaptation checks    (part of run.sh --check)
    gentar/plan.py review <ref>   # what changed since <ref>      (part of run.sh --review)
    gentar/plan.py route    # [secrets] route, one name a line (run.sh maps the slots)

The policy is declared in gentar/policy.toml at adaptation (AGENTS.md,
decision 5) and read here, and only here. The workflow does what `plan`
prints; it holds no tag-parsing or PR-narrowing logic of its own, because
logic in YAML cannot be tested and this can.

`plan` needs no engine, no Docker, no bench and no secrets, so the kit runs
it on a GitHub-hosted runner (or the one GENTAR_CI_RUNNER names, never the
bench's) BEFORE any job may touch the self-hosted bench runner:
a pull request's code never reaches the bench-host unless this says so.

Phases (phase 1 for every PR, phase 2 only when asked):

  phase 1   every PR and every push to the default branch
              checks   bench-free, on a GitHub-hosted runner (run.sh --check)
              bench    [phase1].floor, plus on a PR the suites its body
                       declares in a `gentar: a b` line when bench = "declared"
  phase 2   the full regression (run.sh --sweep), only on the triggers
            [phase2].on names: dispatch, the `arena` tag, `v*-rc*` tags.
            Its job is named `arena / phase2`; release-gate.sh looks for it.
  targeted  an `arena-<suite>` tag or a dispatch naming suites: exactly
            those suites. Never counts as phase 2.
  release   a `v*` tag runs nothing here. A release is GATED on a green
            phase 2 of its commit (release-gate.sh), not tested after it.

With [arena] bench = "mirror" (a PUBLIC repository), nothing here reaches a
bench: phase 2 runs in a private mirror repository that pulls this one
(subject-template/mirror/), and its result comes back as the commit status
`arena/phase2`, which release-gate.sh reads ([phase2] evidence = "status").

Without a policy.toml, `plan` reproduces the 0.3.x kit's behaviour, so an
engine bump alone changes nothing an adopter did not choose.

Output: KEY=value lines on stdout, and the same appended to $GITHUB_OUTPUT
when it is set. Exit 0 with a plan; exit 2 for a policy or input that is
wrong (unknown key, unknown suite, a bad name) -- a refusal, never a guess.
"""
import json
import os
import re
import shutil
import sys
from pathlib import Path

if sys.version_info < (3, 11):
    for candidate in ("python3.14", "python3.13", "python3.12", "python3.11"):
        if shutil.which(candidate):
            os.execvp(candidate, [candidate, os.path.abspath(__file__), *sys.argv[1:]])
    try:
        import tomli as tomllib
    except ModuleNotFoundError:
        print("plan.py needs python 3.11+, or tomli (pip install tomli)", file=sys.stderr)
        sys.exit(2)                           # a refusal, not a failed check
else:
    import tomllib

HERE = Path(__file__).resolve().parent            # <repo>/gentar
SCENARIOS = HERE / "scenarios"
POLICY = HERE / "policy.toml"
NAME = re.compile(r"[A-Za-z0-9._-]+")
ENV_NAME = re.compile(r"[A-Za-z_][A-Za-z0-9_]*")       # the engine's rule
# Labels GitHub gives every self-hosted runner (lowercase; labels match
# case-insensitively).
DEFAULT_RUNNER_LABELS = {"self-hosted", "linux", "macos", "windows", "x64", "arm", "arm64"}

PHASE2_TRIGGERS = ("dispatch", "arena", "rc")
SETUP_TOOLS = ("go", "node", "python")
# GitHub-HOSTED runner labels phase 1's checks may run on. Hosted only: the
# checks job runs a pull request's code, which must never reach a
# self-hosted runner by way of this list.
CHECK_OS = ("ubuntu-latest", "macos-latest")
# The self-hosted route is the repository/organisation variable
# GENTAR_CI_RUNNER instead (a JSON runs-on value): an organisation without
# GitHub-hosted minutes, or one that keeps CI on its own runners, sets it
# and the plan and checks jobs run there (see ci_runner).

# Every key the policy may hold, with its default. An unknown key is a
# refusal: a typo like `benh = "declared"` must not silently mean "off".
SCHEMA = {
    "phase1": {"checks": True, "bench": "off", "floor": [], "setup": {},
               "os": ["ubuntu-latest"]},
    "phase2": {"on": list(PHASE2_TRIGGERS), "release_gate": True,
               "max_age_days": 0, "evidence": "job"},
    # Where the bench runs. "self-hosted": this repository's own runner
    # (label `arena`). "mirror": a PRIVATE mirror repository runs phase 2 on
    # its runner, and this one has no self-hosted job at all -- for a PUBLIC
    # repository, where a fork can bring its own workflow to any runner it
    # can reach. See subject-template/mirror/README.md.
    "arena": {"bench": "self-hosted", "mirror": ""},
    "check": {"allow_drift": [], "docs": False},
    # Ship git history into the bench (scenarios that clone their own
    # tags, an update path). Off by default: .git on CI holds the job's
    # auth header, so it is never copied; stage-git builds a fresh one.
    "stage": {"git": False},
    # Repository secrets the bench job receives BY NAME, for suites that
    # declare them (`credentials` / `pass_env`). See route_names().
    "secrets": {"route": []},
}

# [secrets] route: how many names the kit's workflow has slots for. Each
# slot is one `secrets[<name>]` lookup in the bench job, so the job sees
# exactly the declared secrets and never the whole set (no toJSON(secrets)).
ROUTE_SLOTS = 8
ROUTE_NAME = re.compile(r"[A-Z][A-Z0-9_]*")
# Names a route may not take. The kit already wires its own secrets
# (bench key, provider keys, judge key, telemetry), GitHub and the runner
# own theirs, and a routed value becomes an environment variable of
# run.sh and everything it starts, so names that steer a shell, a loader,
# libc, git, ssh, docker, python, node, perl, ruby or java would let a policy
# line change how the arena itself runs, not just what a suite sees. Bash's
# read-only variables are refused too: run.sh could not assign them. LC_*
# also rides ssh's default SendEnv to the bench-host. (agy on #79 added
# GLIBC_TUNABLES and BASHOPTS.)
ROUTE_RESERVED_PREFIXES = ("GITHUB_", "ACTIONS_", "RUNNER_", "GENTAR_", "LD_", "DYLD_",
                           "BASH_", "DOCKER_", "COMPOSE_", "GIT_", "SSH_", "PYTHON",
                           "ANTHROPIC_", "TYPESAFE_", "OTEL_", "GLIBC_", "MALLOC_", "LC_",
                           "NODE_", "PERL", "RUBY", "JAVA_")
ROUTE_RESERVED = {"BENCH_SSH_KEY", "PATH", "HOME", "USER", "LOGNAME", "SHELL", "PWD",
                  "OLDPWD", "IFS", "ENV", "CDPATH", "PS4", "SHELLOPTS", "BASHOPTS", "UID",
                  "EUID", "PPID", "GROUPS", "TMPDIR", "TERM", "LANG", "LANGUAGE", "CI",
                  "HOSTNAME", "PROMPT_COMMAND", "GCONV_PATH", "LOCPATH", "NLSPATH",
                  "HOSTALIASES", "RES_OPTIONS", "LOCALDOMAIN"}

README_MAX_LINES = 150
# [text](target) and [text](target "title") / (target 'title')
_LINK = re.compile(r"\[[^\]]*\]\(\s*<?([^)\s>]+)>?(?:\s+[\"'][^\"']*[\"'])?\s*\)")

# The kit files --check compares byte for byte against the pinned engine's
# copies. hooks.py, policy.toml and the scenarios are the subject's own.
# OPTIONAL ones may be absent: a repo that runs its arena only by hand has
# no workflow, a repo that never releases needs no release gate, and one
# outside mirror mode no mirror.sh.
KIT_FILES = {
    "gentar/run.sh": "subject-template/gentar/run.sh",
    "gentar/dryrun.py": "subject-template/gentar/dryrun.py",
    "gentar/plan.py": "subject-template/gentar/plan.py",
    "gentar/release-gate.sh": "subject-template/gentar/release-gate.sh",
    "gentar/mirror.sh": "subject-template/gentar/mirror.sh",
    ".github/workflows/gentar-arena.yml":
        "subject-template/.github/workflows/gentar-arena.yml",
}
OPTIONAL_KIT_FILES = {".github/workflows/gentar-arena.yml", "gentar/release-gate.sh",
                      "gentar/mirror.sh"}
# With [arena] bench = "mirror", this repository's workflow is the kit's
# PUBLIC variant: plan and checks, GitHub-hosted, and no self-hosted job.
MIRROR_WORKFLOW = "subject-template/mirror/public/.github/workflows/gentar-arena.yml"
MIRROR_REPO = re.compile(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+")
EVIDENCE = ("job", "status")


def kit_files(policy):
    """KIT_FILES, with the workflow this repository's [arena] mode needs."""
    files = dict(KIT_FILES)
    if mirror_mode(policy):
        files[".github/workflows/gentar-arena.yml"] = MIRROR_WORKFLOW
    return files


def mirror_mode(policy):
    return bool(policy) and policy["arena"]["bench"] == "mirror"


class Refuse(Exception):
    pass


def load_policy(path=POLICY):
    """The policy with defaults filled in, or None when there is none."""
    if not path.exists():
        return None
    try:
        raw = tomllib.loads(path.read_text())
    except tomllib.TOMLDecodeError as exc:
        raise Refuse(f"{path.name}: not valid TOML: {exc}")
    out = {}
    for table, keys in SCHEMA.items():
        given = raw.pop(table, {})
        if not isinstance(given, dict):
            raise Refuse(f"{path.name}: [{table}] must be a table")
        unknown = sorted(set(given) - set(keys))
        if unknown:
            raise Refuse(f"{path.name}: unknown key(s) in [{table}]: "
                         f"{', '.join(unknown)} (known: {', '.join(keys)})")
        out[table] = {**keys, **given}
    if raw:
        raise Refuse(f"{path.name}: unknown table(s): {', '.join(sorted(raw))} "
                     f"(known: {', '.join(SCHEMA)})")
    if not isinstance(out["check"]["docs"], bool):
        raise Refuse("[check] docs must be true or false")
    if not isinstance(out["stage"]["git"], bool):
        raise Refuse("[stage] git must be true or false")
    p1, p2 = out["phase1"], out["phase2"]
    if not isinstance(p1["checks"], bool):
        raise Refuse("[phase1] checks must be true or false")
    if p1["bench"] not in ("off", "declared"):
        raise Refuse(f"[phase1] bench must be \"off\" or \"declared\" (got {p1['bench']!r})")
    for name in _strings(p1["floor"], "[phase1] floor"):
        _suite(name, "[phase1] floor")
    if not isinstance(p1["setup"], dict):
        raise Refuse("[phase1] setup must be a table, e.g. { go = \"1.21\" }")
    for tool, ver in p1["setup"].items():
        if tool not in SETUP_TOOLS:
            raise Refuse(f"[phase1] setup: unknown tool {tool!r} "
                         f"(known: {', '.join(SETUP_TOOLS)}; anything else "
                         "goes in gentar/phase1-setup.sh)")
        if not isinstance(ver, str) or not re.fullmatch(r"[0-9][0-9A-Za-z.x*-]*", ver):
            raise Refuse(f"[phase1] setup: {tool} version must be a string like \"1.21\"")
    oses = _strings(p1["os"], "[phase1] os")
    if not oses:
        raise Refuse("[phase1] os must name at least one runner")
    for o in oses:
        if o not in CHECK_OS:
            raise Refuse(f"[phase1] os: {o!r} is not a GitHub-hosted runner the "
                         f"checks may use (known: {', '.join(CHECK_OS)})")
    for trig in _strings(p2["on"], "[phase2] on"):
        if trig not in PHASE2_TRIGGERS:
            raise Refuse(f"[phase2] on: unknown trigger {trig!r} "
                         f"(known: {', '.join(PHASE2_TRIGGERS)})")
    if not isinstance(p2["release_gate"], bool):
        raise Refuse("[phase2] release_gate must be true or false")
    if not isinstance(p2["max_age_days"], int) or isinstance(p2["max_age_days"], bool) \
            or p2["max_age_days"] < 0:
        raise Refuse("[phase2] max_age_days must be a whole number of days (0 = no limit)")
    if p2["evidence"] not in EVIDENCE:
        raise Refuse(f"[phase2] evidence must be \"job\" or \"status\" (got {p2['evidence']!r})")
    arena = out["arena"]
    if arena["bench"] not in ("self-hosted", "mirror"):
        raise Refuse(f"[arena] bench must be \"self-hosted\" or \"mirror\" (got {arena['bench']!r})")
    if not isinstance(arena["mirror"], str) or (arena["mirror"] and not MIRROR_REPO.fullmatch(arena["mirror"])):
        raise Refuse(f"[arena] mirror must be the private mirror as \"owner/name\" (got {arena['mirror']!r})")
    if p2["evidence"] == "status" and not arena["mirror"]:
        # Anyone allowed to post a commit status could otherwise prove a
        # release with one (agy on #81).
        raise Refuse("[phase2] evidence = \"status\" needs [arena] mirror = \"owner/name\": a status "
                     "counts only when it links a run of that mirror")
    if arena["bench"] == "mirror":
        # No self-hosted runner serves this repository, so nothing here may
        # need one, and phase 2's proof is the mirror's commit status.
        if not arena["mirror"]:
            raise Refuse("[arena] bench = \"mirror\" needs [arena] mirror = \"owner/name\"")
        if p1["bench"] != "off" or p1["floor"]:
            raise Refuse("[arena] bench = \"mirror\": this repository has no bench runner, so "
                         "[phase1] bench must be \"off\" and [phase1] floor empty (the mirror "
                         "runs phase 2; its schedule covers the default branch)")
        if p2["release_gate"] and p2["evidence"] != "status":
            raise Refuse("[arena] bench = \"mirror\": the arena / phase2 job runs in the mirror, "
                         "so the release gate reads its commit status: set [phase2] evidence = \"status\"")
    route_names(out)
    for f in _strings(out["check"]["allow_drift"], "[check] allow_drift"):
        if f not in KIT_FILES:
            raise Refuse(f"[check] allow_drift: {f!r} is not a kit file "
                         f"(kit files: {', '.join(KIT_FILES)})")
    return out


def route_names(policy):
    """[secrets] route, validated: the repository secrets the bench job
    receives, in slot order. Names only -- a value never passes through
    here, or through the plan's outputs."""
    names = _strings(((policy or {}).get("secrets") or {}).get("route", []),
                     "[secrets] route")
    if len(names) > ROUTE_SLOTS:
        raise Refuse(f"[secrets] route names {len(names)} secrets; the kit's workflow "
                     f"has {ROUTE_SLOTS} slots")
    seen = set()
    for n in names:
        if not ROUTE_NAME.fullmatch(n):
            raise Refuse(f"[secrets] route: {n!r} is not a secret name (UPPER_SNAKE_CASE: "
                         "capitals, digits, underscore, starting with a letter)")
        if n in ROUTE_RESERVED or n.startswith(ROUTE_RESERVED_PREFIXES):
            raise Refuse(f"[secrets] route: {n!r} is reserved (the kit, GitHub or the "
                         "arena's own environment uses it); give the secret another name")
        if n in seen:
            raise Refuse(f"[secrets] route: {n!r} is listed twice")
        seen.add(n)
    return list(names)


def _strings(v, where):
    if not isinstance(v, list) or not all(isinstance(x, str) for x in v):
        raise Refuse(f"{where} must be a list of strings")
    return v


def _suite(name, where):
    """A suite name that exists here. Checked before any bench is spent."""
    if not NAME.fullmatch(name):
        raise Refuse(f"{where}: {name!r} is not a suite name "
                     "(letters, digits, dot, dash, underscore)")
    if not (SCENARIOS / f"{name}.toml").exists():
        raise Refuse(f"{where}: no suite {name!r} in gentar/scenarios/")
    return name


def _declared(pr_body, where="PR body"):
    """Suites a PR body declares on a `gentar: a b` line (first one wins)."""
    for line in (pr_body or "").splitlines():
        m = re.match(r"\s*gentar:\s*(.*)$", line.rstrip("\r"))
        if m:
            return [_suite(w, where) for w in m.group(1).split()]
    return []


def _dedup(names):
    seen, out = set(), []
    for n in names:
        if n not in seen:
            seen.add(n)
            out.append(n)
    return out


FIXTURES = HERE / "judge-fixtures"
MIN_FIXTURES = 3


def _load(f):
    """A suite's TOML, read as the engine reads it: bytes, decoded as UTF-8
    whatever the locale (agy on #96). Raises TOMLDecodeError or
    UnicodeDecodeError."""
    with open(f, "rb") as fh:
        return tomllib.load(fh)


def _judged_parts(data):
    """The engine's TomlScenario.uses_judge, on the parsed TOML: a judged
    turn, a goal pilot, or a soft [[verify.judge]] check. One rule for
    plan, lint and run.sh (`scenarios`); a test holds it to the engine's."""
    driver = data.get("driver")
    driver = driver if isinstance(driver, dict) else {}   # the engine refuses a
    turns = driver.get("turns")                           # scalar; this must not
    turns = turns if isinstance(turns, list) else []      # crash on one (agy)
    verify = data.get("verify")
    verify = verify if isinstance(verify, dict) else {}
    idx = [i for i, t in enumerate(turns) if isinstance(t, dict) and "judge" in t]
    goal = bool(driver.get("goal"))
    soft = bool(verify.get("judge"))
    return idx, goal, soft


def _identity(f, data):
    """A suite is its file name, everywhere. The engine names a suite by
    `[scenario] name` (the file name when absent), but run.sh sweeps,
    forwards and guards by file name and plan selects by it: a `name` that
    differed broke the sweep and let `run.sh <name>` pass the pull request
    guard under a name with no file (ground-up review A7, GB2)."""
    if not NAME.fullmatch(f.stem):
        raise Refuse(f"{f.name}: a suite file name is letters, digits, dot, dash, "
                     f"underscore")
    sc = data.get("scenario", {})
    if not isinstance(sc, dict):
        raise Refuse(f"{f.name}: [scenario] must be a table")
    name = sc.get("name", f.stem)
    if name != f.stem:
        raise Refuse(f"{f.name}: [scenario] name = {name!r} differs from the file name; "
                     f"the kit runs a suite by its file name, so remove `name` or rename "
                     f"the file to {name}.toml")
    return sc


def judged_suites(strict=True):
    """Suites with a judged (semantic) turn: they send screens to a judge,
    so they never run in phase 1 and need fixtures (AGENTS.md decision 7).
    Not strict (lint), a suite with no clear identity is skipped here: lint
    lists it among its problems."""
    out = {}
    for f in sorted(SCENARIOS.glob("*.toml")):
        try:
            data = _load(f)
        except (tomllib.TOMLDecodeError, UnicodeDecodeError):
            continue
        try:
            sc = _identity(f, data)
        except Refuse:
            if strict:
                raise
            continue
        idx, goal, soft = _judged_parts(data)
        if idx or goal or soft:
            out[f.stem] = {"turns": idx, "goal": goal, "data": sc.get("data", ""),
                           "file": f.name}
    return out


def scenario_facts(directory=None):
    """What run.sh needs from each suite, read with a TOML parser.

    run.sh read these with awk and grep, and diverged from the engine
    (ground-up review A7): a step printing `goal=1` made a suite judged, a
    comment holding triple quotes hid the [scenario] table, and
    `credentials = []` read as a credential never set. A suite that does
    not parse, or that the engine would refuse for these fields, is
    refused here, before anything runs."""
    facts = []
    for f in sorted((directory or SCENARIOS).glob("*.toml")):
        try:
            data = _load(f)
        except (tomllib.TOMLDecodeError, UnicodeDecodeError) as exc:
            raise Refuse(f"{f.name}: not valid TOML: {exc}")
        facts.append(_suite_facts(f, data))
    return facts


def _suite_facts(f, data):
    """One suite's facts, or Refuse. lint lists the same refusals, so
    --check names every problem run.sh would refuse (agy on #96)."""
    sc = _identity(f, data)
    for table in ("driver", "verify"):
        if table in data and not isinstance(data[table], dict):
            raise Refuse(f"{f.name}: {table} must be a table — write [{table}]")
    subject = sc.get("subject", "")
    if not isinstance(subject, str) or (subject and not NAME.fullmatch(subject)):
        raise Refuse(f"{f.name}: bad subject name: {subject!r} (letters, digits, dot, "
                     f"dash, underscore)")
    creds = sc.get("credentials", [])
    env = sc.get("pass_env", [])
    if not isinstance(creds, list) or not isinstance(env, list):
        raise Refuse(f"{f.name}: scenario.credentials and scenario.pass_env are lists")
    groups = []
    for i, entry in enumerate(creds):
        names = entry if isinstance(entry, list) else [entry]
        if not names or any(not isinstance(n, str) or not ENV_NAME.fullmatch(n)
                            for n in names):
            raise Refuse(f"{f.name}: scenario.credentials[{i}] must be an env var name "
                         f"or a flat list of them (a group that travels together)")
        groups.append(names)
    for i, n in enumerate(env):
        if not isinstance(n, str) or not ENV_NAME.fullmatch(n):
            raise Refuse(f"{f.name}: scenario.pass_env[{i}] must be an env var name")
    idx, goal, soft = _judged_parts(data)
    forwarded = list(dict.fromkeys([n for g in groups for n in g] + env))
    return {"stem": f.stem, "subject": subject, "judged": bool(idx or goal or soft),
            "groups": groups, "forwarded": forwarded}


def _no_judged(names):
    """Phase 1 never runs a judged suite — drop it, and say so."""
    judged = judged_suites()
    kept = [n for n in names if n not in judged]
    dropped = [n for n in names if n in judged]
    return kept, (f"; judged suite(s) {', '.join(dropped)} left for phase 2" if dropped else "")


def ci_runner(env):
    """GENTAR_CI_RUNNER, validated: None (GitHub-hosted, the default) or the
    runs-on labels of the runner the plan and checks jobs use instead.

    The workflow reads the variable itself (`fromJSON(vars.GENTAR_CI_RUNNER)`
    in runs-on); this is the same value, checked where it can be tested.
    Never the bench runner: the checks run pull request code, and the
    `arena` label is where benches are driven from."""
    # The bench label is checked whatever GENTAR_CI_RUNNER says: the bench
    # job runs on it (agy on #98). Unstripped, as the workflow reads it, so
    # a stray space is refused here instead of queueing on a label no
    # runner has; never a default label, which every self-hosted runner
    # carries, so the bench job and its secrets could land on any of them.
    bench = env.get("GENTAR_BENCH_LABEL") or ""
    if bench and (not NAME.fullmatch(bench) or bench.lower() in DEFAULT_RUNNER_LABELS):
        raise Refuse(f"GENTAR_BENCH_LABEL must be one runner label of the bench runner's own, "
                     f"not a default one ({', '.join(sorted(DEFAULT_RUNNER_LABELS))}): {bench!r}")
    raw = (env.get("GENTAR_CI_RUNNER") or "").strip()
    if not raw:
        return None
    # runs-on reads the DECODED value, and the workflow's guard the raw
    # text: `"\u0061rena"` passed the one and named the bench runner in the
    # other (Codex on #98). No runner label needs an escape.
    if "\\" in raw:
        raise Refuse(f"GENTAR_CI_RUNNER holds a JSON escape (a backslash): {raw!r}; write "
                     f"the labels plainly")
    try:
        value = json.loads(raw)
    except ValueError:
        raise Refuse(f"GENTAR_CI_RUNNER is not JSON: {raw!r} (write it as "
                     f"[\"self-hosted\", \"linux-ci\"] or \"linux-ci\")") from None
    labels = [value] if isinstance(value, str) else value
    if (not isinstance(labels, list) or not labels
            or not all(isinstance(x, str) and NAME.fullmatch(x) for x in labels)):
        raise Refuse(f"GENTAR_CI_RUNNER must be a runner label or a list of them: {raw!r}")
    # Case-insensitive and by substring, exactly as the workflow's own
    # pre-scheduling guard (`contains(vars.GENTAR_CI_RUNNER, 'arena')`, and
    # the same for GENTAR_BENCH_LABEL): runner labels match regardless of
    # case, so `Arena` IS the bench runner (Codex). A bench runner labelled
    # otherwise (`my-bench`, say) is caught by its own label.
    for label in ["arena"] + ([bench] if bench else []):
        if label.lower() in raw.lower():          # no escapes (above): raw IS decoded
            raise Refuse(f"GENTAR_CI_RUNNER names the bench runner's label {label!r} (or a "
                         f"label containing it, in any case): the checks run pull request "
                         f"code and must never land on the bench runner")
    # A job lands on any runner that carries ALL its labels. Default labels
    # alone (`self-hosted`, `linux`, `x64`, ...) are carried by every
    # self-hosted runner, the bench runner included (ground-up review C4,
    # SB03): the CI runner must be picked by a label of its own. Checked
    # here, after plan was scheduled, since the workflow cannot loop over a
    # list; it still stops the checks, and fails the run loudly.
    if all(x.lower() in DEFAULT_RUNNER_LABELS for x in labels):
        raise Refuse(f"GENTAR_CI_RUNNER {raw} names only default runner labels, which "
                     f"every self-hosted runner carries, the bench runner included: add a "
                     f"label only your CI runner has, e.g. [\"self-hosted\", \"linux-ci\"]")
    return labels


def plan(env, policy):
    """What this event runs. Pure: env in, plan out."""
    event = env.get("GITHUB_EVENT_NAME", "")
    ref = env.get("GITHUB_REF", "")
    default = env.get("DEFAULT_BRANCH", "main")
    fork = (event == "pull_request"
            and env.get("PR_HEAD_REPO", "") != env.get("GITHUB_REPOSITORY", ""))
    tag = ref[len("refs/tags/"):] if ref.startswith("refs/tags/") else None
    dispatch = [_suite(w, "dispatch input")
                for w in (env.get("SCENARIO_INPUT") or "").split()]

    res = {"checks": False, "bench": "none", "suites": [], "reason": "",
           "setup": {}, "os": ["ubuntu-latest"], "route": route_names(policy)}
    runner = ci_runner(env)
    # Where plan and checks ran, in the plan output:
    # a subject can see it without reading repository settings.
    res["runner"] = json.dumps(runner) if runner else "github-hosted"
    if runner is not None:
        # One entry, named after the runner; the workflow's runs-on reads
        # the variable, so this only names the matrix job.
        res["os"] = ["+".join(runner)]
        if fork:
            # The workflow already skips plan for this case (it would run
            # the fork's plan.py on a self-hosted runner); said here too.
            return {**res, "reason": "fork PR: never on a self-hosted runner "
                                     "(GENTAR_CI_RUNNER is set), not even the checks"}

    def targeted(names, why):
        names = _dedup(names)
        if names:
            res.update(bench="targeted", suites=names, reason=why)
        else:
            res.update(reason=why + " (nothing to run)")
        return res

    if policy is None:                       # the 0.3.x kit, reproduced
        floor = (env.get("GENTAR_FLOOR") or "").split()
        if event == "pull_request":
            if fork:
                return {**res, "reason": "fork PR: never on the self-hosted runner"}
            picked = _declared(env.get("PR_BODY"))
            if picked:
                names, note = _no_judged([_suite(f, "GENTAR_FLOOR") for f in floor] + picked)
                return targeted(names, "no policy.toml: PR narrowed by its gentar: line" + note)
            return {**res, "bench": "phase2", "reason": "no policy.toml: PR runs every suite"}
        if event == "workflow_dispatch" and dispatch:
            return targeted(dispatch, "no policy.toml: dispatch names suites")
        if tag and tag.startswith("arena-"):
            return targeted([_suite(tag[len("arena-"):], "keyword tag")],
                            f"no policy.toml: keyword tag {tag}")
        # The workflow triggers on every branch (it cannot name the default
        # one); 0.3.x ran on the default branch only.
        if event == "push" and ref.startswith("refs/heads/") and ref != f"refs/heads/{default}":
            return {**res, "reason": "no policy.toml: pushes run on the default branch only"}
        if event in ("push", "workflow_dispatch"):
            return {**res, "bench": "phase2", "reason": "no policy.toml: full sweep"}
        return {**res, "reason": f"no policy.toml: nothing runs on {event or 'this event'}"}

    if mirror_mode(policy):
        if runner is not None:
            raise Refuse("[arena] bench = \"mirror\": this repository's checks run GitHub-hosted "
                         "(its workflow has no self-hosted job); unset GENTAR_CI_RUNNER")
        out = _plan_policy(env, policy, res, event, ref, default, fork, tag, dispatch, targeted)
        if out["bench"] != "none":
            out = {**out, "bench": "none", "suites": [],
                   "reason": f"{out['reason']} -- runs in the private mirror "
                             f"{policy['arena']['mirror']}, not here (gentar/mirror.sh dispatch <sha>)"}
        return out
    return _plan_policy(env, policy, res, event, ref, default, fork, tag, dispatch, targeted)


def _plan_policy(env, policy, res, event, ref, default, fork, tag, dispatch, targeted):
    """plan() under a policy.toml."""
    p1, p2 = policy["phase1"], policy["phase2"]
    res["setup"] = dict(p1["setup"])
    runner = ci_runner(env)
    if runner is None:
        res["os"] = list(dict.fromkeys(p1["os"]))
    elif any(o != "ubuntu-latest" for o in p1["os"]):
        raise Refuse(f"[phase1] os lists {', '.join(o for o in p1['os'] if o != 'ubuntu-latest')} "
                     f"but GENTAR_CI_RUNNER sends the checks to one self-hosted runner: "
                     f"macOS checks need a GitHub-hosted runner. Drop it from [phase1] os, "
                     f"or unset GENTAR_CI_RUNNER")

    if event == "pull_request":
        res["checks"] = p1["checks"]
        if fork:
            return {**res, "reason": "phase 1, fork PR: bench-free checks only, "
                                     "never the self-hosted runner"}
        if p1["bench"] == "off":
            return {**res, "reason": "phase 1: bench-free checks only "
                                     "([phase1] bench = \"off\")"}
        names, note = _no_judged(list(p1["floor"]) + _declared(env.get("PR_BODY")))
        return targeted(names, "phase 1: floor + the suites this PR declares" + note)

    if event == "push" and ref == f"refs/heads/{default}":
        res["checks"] = p1["checks"]
        names, note = _no_judged(list(p1["floor"]))
        return targeted(names, f"phase 1 on {default}: checks + floor" + note)

    # Tag rules are for a tag PUSH. A dispatch run ON a tag ref (a drift
    # run of a release, claude-playbooks' monitor) is a dispatch: it used to
    # hit the release rule here and plan nothing, reading "success" with the
    # arena skipped, and a dispatch naming suites was swallowed the same way.
    if tag is not None and event == "push":
        if tag == "arena":
            if "arena" in p2["on"]:
                return {**res, "bench": "phase2", "reason": "phase 2: the `arena` tag"}
            return {**res, "reason": "the `arena` tag is not a phase 2 trigger here"}
        if tag.startswith("arena-"):
            return targeted([_suite(tag[len("arena-"):], "keyword tag")],
                            f"targeted: keyword tag {tag}")
        if re.fullmatch(r"v.*-rc.*", tag):
            if "rc" in p2["on"]:
                return {**res, "bench": "phase2", "reason": f"phase 2: release candidate {tag}"}
            return {**res, "reason": f"{tag}: rc tags are not a phase 2 trigger here"}
        if tag.startswith("v"):
            return {**res, "reason": f"{tag}: a release is gated by release-gate.sh "
                                     "on a green phase 2 of its commit, not run here"}
        return {**res, "reason": f"tag {tag}: not an arena trigger"}

    if event == "workflow_dispatch":
        if dispatch:
            return targeted(dispatch, "targeted: dispatch names suites")
        if "dispatch" in p2["on"]:
            return {**res, "bench": "phase2", "reason": "phase 2: manual dispatch"}
        return {**res, "reason": "dispatch is not a phase 2 trigger here"}

    return {**res, "reason": f"nothing runs on {event or 'this event'} ({ref})"}


def emit(res):
    lines = [
        f"checks={'true' if res['checks'] else 'false'}",
        f"bench={res['bench']}",
        f"suites={' '.join(res['suites'])}",
        f"reason={res['reason']}",
        f"os={json.dumps(res['os'])}",
        f"runner={res.get('runner', 'github-hosted')}",
    ] + [f"setup_{t}={res['setup'].get(t, '')}" for t in SETUP_TOOLS]
    # Secret NAMES for the bench job's slots (route_1 .. route_8, empty when
    # unused); the workflow looks each one up as secrets[<name>].
    route = res.get("route", [])
    lines.append(f"route={' '.join(route)}")
    lines += [f"route_{i}={route[i - 1] if i <= len(route) else ''}"
              for i in range(1, ROUTE_SLOTS + 1)]
    out = "\n".join(lines) + "\n"
    sys.stdout.write(out)
    gh = os.environ.get("GITHUB_OUTPUT")
    if gh:
        with open(gh, "a") as f:
            f.write(out)


# ---- lint: bench-free adaptation checks (run.sh --check) ------------------

PAIRISH = re.compile(r".*_(BASE_URL|URL|ENDPOINT|HOST)$")


def docs_problems(repo):
    """The docs standard, MECHANICALLY: what is missing, never how good it
    is (review judges content). A short README (what / why / how), docs
    with tutorials and guides, examples each with a README, an AGENTS.md
    agent entry, and every relative link resolving."""
    repo = Path(repo)
    out = []
    readme = repo / "README.md"
    if not readme.is_file():
        out.append("README.md: missing (what it is, why it exists, how it is used)")
    elif len(readme.read_text().splitlines()) > README_MAX_LINES:
        out.append(f"README.md: {len(readme.read_text().splitlines())} lines — keep it short "
                   f"(<= {README_MAX_LINES}); move the manual into docs/")
    for sub in ("tutorials", "guides"):
        if not [p for p in (repo / "docs" / sub).glob("*.md") if p.is_file()]:
            out.append(f"docs/{sub}/: missing or empty")
    examples = [d for d in sorted((repo / "examples").glob("*"))
                if d.is_dir() and not d.name.startswith(".")]
    if not examples:
        out.append("examples/: missing or empty (smallest to full, each with a README.md)")
    for d in examples:
        if not (d / "README.md").is_file():
            out.append(f"examples/{d.name}/README.md: missing")
    if not (repo / "AGENTS.md").is_file():
        out.append("AGENTS.md: missing (the agent entry for installing and deploying)")
    pages = [readme, repo / "AGENTS.md", *sorted((repo / "docs").rglob("*.md")),
             *sorted((repo / "examples").rglob("*.md"))]
    for page in pages:
        if not page.is_file():
            continue
        for target in _LINK.findall(page.read_text()):
            if re.match(r"[a-z][a-z0-9+.-]*:", target) or target.startswith("#"):
                continue                      # URLs, mailto:, in-page anchors
            path = target.split("#", 1)[0]
            base = repo if path.startswith("/") else page.parent   # /x = repo root
            if path and not (base / path.lstrip("/")).exists():
                out.append(f"{page.relative_to(repo)}: broken link {target}")
    return out


def lint(policy, engine_root):
    """Problems, as strings. Empty = clean."""
    problems = []
    # 1. credentials: a flat list holding an endpoint-looking name reads like
    #    a pair and means "either one" -- the token wins alone and the URL is
    #    dropped (the first real adopter's bug). Nest the pair.
    for f in sorted(SCENARIOS.glob("*.toml")):
        try:
            data = _load(f)
        except (tomllib.TOMLDecodeError, UnicodeDecodeError) as exc:
            problems.append(f"{f.name}: not valid TOML: {exc}")
            continue
        try:
            _suite_facts(f, data)
        except Refuse as exc:                 # listed with the rest, not alone
            problems.append(str(exc))
            continue
        creds = (data.get("scenario") or {}).get("credentials") or []
        flat = [c for c in creds if isinstance(c, str)]
        if len(flat) > 1 and any(PAIRISH.match(c) for c in flat):
            problems.append(
                f"{f.name}: credentials {flat} is a flat list of ALTERNATIVES; "
                f"an endpoint there is dropped when the token wins. Nest the "
                f"pair: [[\"TOKEN\", \"BASE_URL\"]]")
    # 2. kit drift: the kit's files must match the pinned engine's copies,
    #    so an engine bump stays a re-copy. Subject-owned: hooks.py,
    #    policy.toml, scenarios/.
    allowed = set(policy["check"]["allow_drift"]) if policy else set()
    repo = HERE.parent
    for mine, theirs in kit_files(policy).items():
        a, b = repo / mine, Path(engine_root) / theirs
        if not b.exists():
            continue                      # an older engine without this file
        if not a.exists():
            if mine in OPTIONAL_KIT_FILES:
                print(f"note: {mine} not present (fine for a repo without the kit's CI "
                      f"workflow, a release gate or the arena mirror)")
            else:
                problems.append(f"{mine}: missing (copy it from the kit)")
        elif a.read_bytes() != b.read_bytes():
            if mine in allowed:
                print(f"note: {mine} differs from the kit (allowed by [check] allow_drift)")
            else:
                problems.append(
                    f"{mine}: differs from the pinned kit's copy. Re-copy it; "
                    f"adaptations belong in hooks.py / policy.toml / repo vars "
                    f"(or list it in [check] allow_drift, and own the difference)")
    # 3. judged suites: synthetic data declared, phase 2 only, fixtures to
    #    measure them with (bin/judge-eval in the engine).
    judged = judged_suites(strict=False)
    floor = set((policy or {}).get("phase1", {}).get("floor", []))
    for name, j in sorted(judged.items()):
        if j["data"] != "synthetic":
            problems.append(f"{j['file']}: judged turns need [scenario] data = \"synthetic\" "
                            f"(the engine refuses the run otherwise)")
        if name in floor:
            problems.append(f"policy.toml: [phase1] floor has {name}, which has judged turns — "
                            f"judged suites run in phase 2 only")
        if j.get("goal"):
            gdir = FIXTURES / name / "goal"
            # an action counts only with a screen in it: an empty `done/`
            # covered nothing (agy on #99)
            picks = {d.name: n for d in gdir.glob("*")
                     if d.is_dir() and (n := len(list(d.glob("*.txt"))))}
            if sum(picks.values()) < MIN_FIXTURES or "done" not in picks or len(picks) < 2:
                problems.append(
                    f"{j['file']}: its goal pilot needs {MIN_FIXTURES}+ fixture screens under "
                    f"gentar/judge-fixtures/{name}/goal/<expected action>/, covering 2+ actions "
                    f"including done (has {sum(picks.values())} over {sorted(picks) or 'none'})")
        for i in j["turns"]:
            for label in ("yes", "no"):
                have = len(list((FIXTURES / name / str(i) / label).glob("*.txt")))
                if have < MIN_FIXTURES:
                    problems.append(
                        f"{j['file']}: judged turn {i} has {have} {label} fixture(s) under "
                        f"gentar/judge-fixtures/{name}/{i}/{label}/ — needs {MIN_FIXTURES}+")
    # 4. a routed secret no suite declares reaches nothing: run.sh forwards
    #    only names a suite asks for (`credentials` / `pass_env`). Said here,
    #    so a typo in either place is caught before a bench is spent.
    asked = set()
    for f in sorted(SCENARIOS.glob("*.toml")):
        try:
            sc = _load(f).get("scenario") or {}
        except (tomllib.TOMLDecodeError, UnicodeDecodeError):
            continue                          # reported by check 1
        listed = [v for v in (sc.get("credentials"), sc.get("pass_env"))
                  if isinstance(v, list)]          # anything else: check 1 says so
        for c in [c for v in listed for c in v]:
            asked.update([c] if isinstance(c, str)
                         else [x for x in c if isinstance(x, str)] if isinstance(c, list) else [])
    for n in route_names(policy):
        if n not in asked:
            problems.append(f"policy.toml: [secrets] route has {n}, but no suite declares it "
                            f"in `credentials` or `pass_env`, so it would reach nothing")
    # 5. the docs standard, when the policy opts in ([check] docs = true).
    if policy and policy["check"]["docs"]:
        problems += [f"docs: {p}" for p in docs_problems(HERE.parent)]
    # 6. the PR invariant rests on the kit's workflow; say so when it cannot.
    wf = ".github/workflows/gentar-arena.yml"
    if wf in allowed:
        print(f"note: {wf} is allowed to drift, so 'no pull_request job reaches "
              f"the self-hosted runner unless the plan says so' is NOT verified")
    return problems


# Never staged, from git or not: gentar/.arena holds .env and the bench key,
# gentar/reports holds old verdicts, and .git holds the job's auth header on
# CI (stage-git builds a fresh one when the policy asks for history).
STAGE_EXCLUDE = (".git", "gentar/.arena", "gentar/reports")


def shipped_files(repo: str):
    """What git would ship from `repo`, relative and sorted: tracked files
    plus the untracked ones it does not ignore (`git ls-files --cached
    --others --exclude-standard`). A submodule or nested repository is
    listed by its own git, so its ignored files stay out too and its .git
    never comes along; one with nothing checked out is an empty directory
    (a trailing `/`). None when `repo` has no .git of its own: not a work
    tree root, so it is staged whole, as before. Refuses when git cannot
    list a repo that has a .git: falling back to the whole tree would ship
    exactly the files this exists to keep out."""
    import subprocess

    if not os.path.lexists(os.path.join(repo, ".git")):
        return None
    # The repo's own config must not run anything here (core.fsmonitor is a
    # program git starts for ls-files), and an outer GIT_DIR/GIT_WORK_TREE
    # must not redirect git to another repository (agy on #92).
    env = {k: v for k, v in os.environ.items() if not k.startswith("GIT_")}
    r = subprocess.run(["git", "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false",
                        "-C", repo, "ls-files", "-z", "--cached", "--others",
                        "--exclude-standard"], capture_output=True, env=env)
    if r.returncode != 0:
        raise Refuse(f"stage-tree: git ls-files in {repo} failed: "
                     f"{r.stderr.decode(errors='replace').strip()[:300]}")
    out = []
    # A path is listed once per merge stage while a conflict is open.
    for rel in sorted({os.fsdecode(p).rstrip("/") for p in r.stdout.split(b"\0") if p}):
        src = os.path.join(repo, rel)
        if any(rel == x or rel.startswith(x + "/") for x in STAGE_EXCLUDE):
            continue
        # A tracked directory replaced by a symlink (not yet staged): git still
        # lists the old children, which now resolve through the link. The
        # link ships as a link; its "children" are not files of this repo
        # (Codex on #92).
        parts = rel.split("/")
        if any(os.path.islink(os.path.join(repo, *parts[:i])) for i in range(1, len(parts))):
            continue
        if os.path.isdir(src) and not os.path.islink(src):
            inner = shipped_files(src)
            out += [f"{rel}/{p}" for p in inner] if inner else [rel + "/"]
        elif os.path.lexists(src):                 # tracked but deleted on disk: gone
            out.append(rel)
    return out


def stage_tree(repo: str, dest: str) -> str:
    """Copy the subject's files from `repo` into `dest` (an empty directory):
    in a git work tree, only what git would ship (shipped_files), so a
    gitignored .env, .npmrc or build output never reaches a bench (C2).
    Without a .git, the whole tree minus STAGE_EXCLUDE, by the same tar
    pipe run.sh used before. Symlinks are copied as links, never followed.
    Returns how it staged, for the log line."""
    import subprocess

    dest = os.path.abspath(dest)
    files = shipped_files(repo)
    if files is None:
        pack = subprocess.Popen(["tar", *(f"--exclude=./{x}" for x in STAGE_EXCLUDE),
                                 "-cf", "-", "."], cwd=repo, stdout=subprocess.PIPE)
        unpack = subprocess.run(["tar", "-xf", "-", "-C", dest], stdin=pack.stdout)
        pack.stdout.close()
        if pack.wait() != 0 or unpack.returncode != 0:
            raise Refuse(f"stage-tree: copying {repo} into {dest} failed")
        return "the whole tree (not a git work tree)"
    for rel in files:
        src, dst = os.path.join(repo, rel), os.path.join(dest, rel)
        if rel.endswith("/"):
            os.makedirs(dst, exist_ok=True)
            continue
        os.makedirs(os.path.dirname(dst), exist_ok=True)
        if os.path.islink(src):
            os.symlink(os.readlink(src), dst)
        elif os.path.isfile(src):
            shutil.copy2(src, dst)
    return f"{len(files)} file(s) git would ship (ignored files left out)"


def stage_git(repo: str, dest: str) -> None:
    """Give the staged subject (`dest`, the working-tree copy) a .git with
    the repository's history and tags, and nothing else of its .git: a
    local `git clone` writes a NEW config, so an auth header that
    actions/checkout left in the source config is not copied, and the
    origin (a host path) is removed. Refuses (exit 2) on a shallow
    source, which has no tags to ship."""
    import subprocess
    import tempfile

    def git(*args, cwd=None):
        r = subprocess.run(["git", *args], cwd=cwd, capture_output=True, text=True)
        if r.returncode != 0:
            raise Refuse(f"stage-git: git {args[0]} failed: {r.stderr.strip()[:300]}")
        return r.stdout.strip()

    if git("-C", repo, "rev-parse", "--is-shallow-repository") == "true":
        raise Refuse("[stage] git = true needs full history, and this checkout is shallow "
                     "(the kit workflow's bench job checks out with fetch-depth: 0; a local "
                     "shallow clone needs `git fetch --unshallow`)")
    if os.path.exists(os.path.join(dest, ".git")):
        raise Refuse(f"stage-git: {dest}/.git already exists")
    with tempfile.TemporaryDirectory() as tmp:
        clone = os.path.join(tmp, "c")
        git("clone", "-q", "--no-checkout", "--no-hardlinks", repo, clone)
        git("-C", clone, "remote", "remove", "origin")
        os.rename(os.path.join(clone, ".git"), os.path.join(dest, ".git"))
    git("-C", dest, "reset", "-q")                 # the index matches HEAD; files are the copy's


# --- review: what changed since <ref> ------------------------------------
#
# run.sh --review's second half. The first half (in run.sh) compares names:
# what the repo ships against what the suites mention. Its blind spot is a
# change INSIDE a file a suite already names -- a new statement in an
# existing binary. This half closes that from the other side: it starts from
# the commits, not the names, so every changed path is listed whether or not
# a suite mentions it, and each suite that mentions one is named for
# re-reading. Deterministic: git objects only (the ref and HEAD, never the
# working tree), sorted output, no network, no bench. It reports; it never
# fails a build.
REF_SAFE = re.compile(r"[A-Za-z0-9._/~^@{}-]+")
# Same exclusions as run.sh's candidate list: the kit and the tests are not
# what a fresh machine runs.
NOT_SHIPPED = re.compile(r"^(gentar|test|tests|\.github)/")


def _git(repo, *args):
    import subprocess
    r = subprocess.run(["git", "-C", str(repo), *args], capture_output=True, text=True)
    if r.returncode != 0:
        raise Refuse(f"git {' '.join(args[:2])}: {r.stderr.strip() or 'failed'}")
    return r.stdout


def _executables(repo, rev):
    """Paths at `rev` a fresh machine can RUN: the executable bit, or bin/."""
    out = set()
    for rec in _git(repo, "ls-tree", "-r", "-z", rev).split("\0"):
        if not rec:
            continue
        meta, path = rec.split("\t", 1)
        mode = meta.split()[0]
        if (mode == "100755" or path.startswith("bin/")) and not NOT_SHIPPED.match(path):
            out.add(path)
    return out


def suite_mentions(text):
    """Every token a suite uses, split on path separators -- run.sh's rule,
    so `./install.sh` and `bin/tool` count as mentions of their basenames."""
    lines = [l for l in text.splitlines() if not l.lstrip().startswith("#")]
    return set(t for t in re.split(r"[^A-Za-z0-9_.-]+", "\n".join(lines)) if t)


def _mentioned(path, tokens):
    base = path.rsplit("/", 1)[-1]
    return bool(base) and (base in tokens or base.rsplit(".", 1)[0] in tokens)


def review(repo, since):
    if since.startswith("-") or not REF_SAFE.fullmatch(since):
        raise Refuse(f"bad ref: {since!r}")
    try:
        old = _git(repo, "rev-parse", "-q", "--verify", f"{since}^{{commit}}").strip()
    except Refuse:
        raise Refuse(f"ref {since!r} not found here (a shallow CI checkout needs "
                     f"fetch-depth: 0)") from None
    new = _git(repo, "rev-parse", "HEAD").strip()
    changes = []                                   # (status, path, old path)
    recs = _git(repo, "diff", "--name-status", "-z", "-M", old, new).split("\0")
    i = 0
    while i < len(recs) and recs[i]:
        st = recs[i][0]
        if st in "RC":
            changes.append((st, recs[i + 2], recs[i + 1]))
            i += 3
        else:
            changes.append((st, recs[i + 1], None))
            i += 2
    # Outside gentar/ only. A move ACROSS the boundary keeps its outside
    # side: a subject file moved into gentar/ is gone from the subject, and
    # a suite naming it must be re-read (Codex on #53).
    ours = []
    for st, path, frm in changes:
        in_new = path.startswith("gentar/")
        in_old = frm is not None and frm.startswith("gentar/")
        if frm is None or (not in_new and not in_old):
            if not in_new:
                ours.append((st, path, frm))
        elif in_new and not in_old:
            if st == "R":
                ours.append(("D", frm, None))
        elif not in_new and in_old:
            ours.append(("A", path, None))
    was, now = _executables(repo, old), _executables(repo, new)
    # The suites as committed at HEAD, like the diff: an uncommitted edit to a
    # scenario must not move a changed path into "covered" (Codex).
    rel = "./" + os.path.relpath(SCENARIOS, repo).replace(os.sep, "/")
    # not -r: the scenarios directory's own files, as run.sh globs them
    names = sorted(n for n in _git(repo, "ls-tree", "-z", "--name-only", new, rel + "/")
                   .split("\0") if n.endswith(".toml"))
    suites = {n.rsplit("/", 1)[-1]: suite_mentions(_git(repo, "show", f"{new}:./{n}"))
              for n in names}

    label = old[:12] if old.startswith(since) else f"{since} ({old[:12]})"
    lines = [f"since {label} .. HEAD ({new[:12]})", ""]
    lines.append(f"changed paths outside gentar/: {len(ours)}")
    for st, path, frm in sorted(ours, key=lambda c: c[1]):
        lines.append(f"  {st}  {frm} -> {path}" if frm else f"  {st}  {path}")
    added, removed = sorted(now - was), sorted(was - now)
    lines += ["", "executables:"]
    lines += [f"  added    {p}" for p in added] + [f"  removed  {p}" for p in removed]
    changed_exe = sorted(p for st, p, _ in ours if p in now and p in was and st == "M")
    lines += [f"  changed  {p}" for p in changed_exe]
    if not (added or removed or changed_exe):
        lines.append("  (none)")
    touched = {p for _, p, _ in ours} | {f for _, _, f in ours if f}
    lines += ["", "suites that mention a changed path (re-read them):"]
    unmentioned = set(touched)
    hits = 0
    for name, tokens in suites.items():
        paths = sorted(p for p in touched if _mentioned(p, tokens))
        unmentioned -= set(paths)
        if paths:
            hits += 1
            lines.append(f"  {name:<28} {', '.join(paths)}")
    if not hits:
        lines.append("  (none)")
    lines += ["", "changed paths no suite mentions:"]
    lines += [f"  {p}" for p in sorted(unmentioned)] or ["  (none)"]
    lines += ["", "not covered here: --help output per executable (it needs the binaries",
              "run, which belongs on a bench, not on this host)."]
    return "\n".join(lines)


def main(argv):
    cmd = argv[1] if len(argv) > 1 else ""
    try:
        if cmd == "stage-tree" and len(argv) == 4:   # needs no policy
            print(f"staged: {stage_tree(argv[2], argv[3])}", file=sys.stderr)
            return 0
        if cmd == "review" and len(argv) == 3:    # needs no policy: a broken
            print(review(HERE.parent, argv[2]))   # one must not hide the diff
            return 0
        if cmd == "scenarios" and len(argv) == 2:   # run.sh's view of the suites
            # One tab-separated line per suite: file stem, subject, judged
            # (1/0), credential groups (`;` between groups, a space between
            # names), every name forwarded (credentials, then pass_env).
            for x in scenario_facts():
                print("\t".join([x["stem"], x["subject"], "1" if x["judged"] else "0",
                                 ";".join(" ".join(g) for g in x["groups"]),
                                 " ".join(x["forwarded"])]))
            return 0
        policy = load_policy()
        if cmd == "plan":
            emit(plan(os.environ, policy))
            return 0
        if cmd == "stage-git" and len(argv) == 4:
            if (policy or {}).get("stage", {}).get("git"):
                stage_git(argv[2], argv[3])
                print("staged: git history (fresh local clone, no remote, no config "
                      "carried over)", file=sys.stderr)
            return 0
        if cmd == "route":                 # run.sh's view of [secrets] route
            for n in route_names(policy):
                print(n)
            return 0
        if cmd == "gate-config":           # release-gate.sh's view of [phase2]
            p2 = (policy or {}).get("phase2") or SCHEMA["phase2"]
            print(f"release_gate={'true' if p2['release_gate'] else 'false'}")
            print(f"max_age_days={p2['max_age_days']}")
            print(f"evidence={p2.get('evidence', 'job')}")
            return 0
        if cmd == "arena-config":          # mirror.sh's view of [arena]
            arena = (policy or {}).get("arena") or SCHEMA["arena"]
            print(f"bench={arena['bench']}")
            print(f"mirror={arena['mirror']}")
            return 0
        if cmd == "lint":
            engine = argv[2] if len(argv) > 2 else str(HERE / ".arena")
            problems = lint(policy, engine)
            for p in problems:
                print(f"check: {p}", file=sys.stderr)
            print(f"lint: {'clean' if not problems else f'{len(problems)} problem(s)'}"
                  + ("" if policy else " (no policy.toml: 0.3.x behaviour)"))
            return 1 if problems else 0
    except Refuse as exc:
        print(f"plan: {exc}", file=sys.stderr)
        return 2
    print("usage: gentar/plan.py plan | lint [engine-root] | gate-config | arena-config | "
          "route | scenarios | stage-tree <repo> <dest> | stage-git <repo> <dest> | "
          "review <ref>",
          file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv))
