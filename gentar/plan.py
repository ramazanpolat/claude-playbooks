#!/usr/bin/env python3
"""This repo's run policy: which suites run when, decided once.

    gentar/plan.py plan     # what should THIS CI event run?  (the kit's plan job)
    gentar/plan.py lint     # bench-free adaptation checks    (part of run.sh --check)

The policy is declared in gentar/policy.toml at adaptation (AGENTS.md,
decision 5) and read here, and only here. The workflow does what `plan`
prints; it holds no tag-parsing or PR-narrowing logic of its own, because
logic in YAML cannot be tested and this can.

`plan` needs no engine, no Docker, no bench and no secrets, so the kit runs
it on a GitHub-hosted runner (or the one GENTAR_CI_RUNNER names, never the
bench's) BEFORE any job may touch the self-hosted bench runner:
a pull request's code never reaches the bench-host unless this says so.

Phases (the pilot's words: phase 1 for every PR, phase 2 only when asked):

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
               "max_age_days": 0},
    "check": {"allow_drift": [], "docs": False},
    # Ship git history into the bench (scenarios that clone their own
    # tags, an update path). Off by default: .git on CI holds the job's
    # auth header, so it is never copied; stage-git builds a fresh one.
    "stage": {"git": False},
}

README_MAX_LINES = 150
# [text](target) and [text](target "title") / (target 'title')
_LINK = re.compile(r"\[[^\]]*\]\(\s*<?([^)\s>]+)>?(?:\s+[\"'][^\"']*[\"'])?\s*\)")

# The kit files --check compares byte for byte against the pinned engine's
# copies. hooks.py, policy.toml and the scenarios are the subject's own.
# OPTIONAL ones may be absent: a central-dispatch subject has no own-arena
# workflow, and a repo that never releases needs no release gate.
KIT_FILES = {
    "gentar/run.sh": "subject-template/gentar/run.sh",
    "gentar/dryrun.py": "subject-template/gentar/dryrun.py",
    "gentar/plan.py": "subject-template/gentar/plan.py",
    "gentar/release-gate.sh": "subject-template/gentar/release-gate.sh",
    ".github/workflows/gentar-arena.yml":
        "subject-template/.github/workflows/gentar-arena.yml",
}
OPTIONAL_KIT_FILES = {".github/workflows/gentar-arena.yml", "gentar/release-gate.sh"}


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
    for f in _strings(out["check"]["allow_drift"], "[check] allow_drift"):
        if f not in KIT_FILES:
            raise Refuse(f"[check] allow_drift: {f!r} is not a kit file "
                         f"(kit files: {', '.join(KIT_FILES)})")
    return out


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


def judged_suites():
    """Suites with a judged (semantic) turn: they send screens to a judge,
    so they never run in phase 1 and need fixtures (AGENTS.md decision 7)."""
    out = {}
    for f in sorted(SCENARIOS.glob("*.toml")):
        try:
            data = tomllib.loads(f.read_text())
        except tomllib.TOMLDecodeError:
            continue
        driver = data.get("driver") or {}
        turns = driver.get("turns") or []
        idx = [i for i, t in enumerate(turns) if isinstance(t, dict) and "judge" in t]
        goal = bool(driver.get("goal"))
        soft = bool((data.get("verify") or {}).get("judge"))
        if idx or goal or soft:
            name = (data.get("scenario") or {}).get("name", f.stem)
            out[name] = {"turns": idx, "goal": goal,
                         "data": (data.get("scenario") or {}).get("data", ""), "file": f.name}
    return out


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
    raw = (env.get("GENTAR_CI_RUNNER") or "").strip()
    if not raw:
        return None
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
    # pre-scheduling guard (`contains(vars.GENTAR_CI_RUNNER, 'arena')`):
    # runner labels match regardless of case, so `Arena` IS the bench
    # runner (Codex).
    if "arena" in raw.lower():
        raise Refuse("GENTAR_CI_RUNNER names the `arena` label (or a label containing "
                     "'arena', in any case): the checks run pull request code and must "
                     "never land on the bench runner")
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
           "setup": {}, "os": ["ubuntu-latest"]}
    runner = ci_runner(env)
    # Where plan and checks ran, in the plan output (claude-playbooks-dc):
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

    p1, p2 = policy["phase1"], policy["phase2"]
    res["setup"] = dict(p1["setup"])
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
            data = tomllib.loads(f.read_text())
        except tomllib.TOMLDecodeError as exc:
            problems.append(f"{f.name}: not valid TOML: {exc}")
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
    for mine, theirs in KIT_FILES.items():
        a, b = repo / mine, Path(engine_root) / theirs
        if not b.exists():
            continue                      # an older engine without this file
        if not a.exists():
            if mine in OPTIONAL_KIT_FILES:
                print(f"note: {mine} not present (fine for a central-dispatch subject, "
                      f"or one that does not gate releases)")
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
    judged = judged_suites()
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
            picks = {d.name: len(list(d.glob("*.txt"))) for d in gdir.glob("*") if d.is_dir()}
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
    # 4. the docs standard, when the policy opts in ([check] docs = true).
    if policy and policy["check"]["docs"]:
        problems += [f"docs: {p}" for p in docs_problems(HERE.parent)]
    # 5. the PR invariant rests on the kit's workflow; say so when it cannot.
    wf = ".github/workflows/gentar-arena.yml"
    if wf in allowed:
        print(f"note: {wf} is allowed to drift, so 'no pull_request job reaches "
              f"the self-hosted runner unless the plan says so' is NOT verified")
    return problems


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


def main(argv):
    cmd = argv[1] if len(argv) > 1 else ""
    try:
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
        if cmd == "gate-config":           # release-gate.sh's view of [phase2]
            p2 = (policy or {}).get("phase2") or SCHEMA["phase2"]
            print(f"release_gate={'true' if p2['release_gate'] else 'false'}")
            print(f"max_age_days={p2['max_age_days']}")
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
    print("usage: gentar/plan.py plan | lint [engine-root] | gate-config | stage-git <repo> <dest>",
          file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv))
