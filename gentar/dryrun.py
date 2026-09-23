#!/usr/bin/env python3
"""Run an own-arena scenario locally, without a bench.

    gentar/dryrun.py gentar/scenarios/first-suite.toml
    gentar/dryrun.py gentar/scenarios/*.toml        # sweep
    gentar/dryrun.py                                # every suite

Why this exists: a scenario is shell, and shell in TOML is three levels of
quoting deep. The arena is the only thing that ever runs these, at the cost
of a bench VM and several minutes per suite -- so a missing quote cost a
full round trip to find. This executes the same steps, the same driver turns
and the same assertions in a scratch HOME, in about a second, and fails the
same way.

What it is NOT: a bench. There is no sandbox, no template, no network policy,
no real agent. It proves the shell and the assertions; the arena still proves
the isolation. Suites declaring `credentials` are skipped -- they need a real
agent and a real key. Suites whose [driver] uses `pick` or `abort` turns come
back UNVERIFIED with a nonzero exit: those turns need the real driver, and a
picker that never matched or a danger gate that never fired must not read as
a pass.

Two adaptations live at the top of the file (the only edits most subjects
need):

  prepare(env)      called once before the sweep; build your CLI or stage
                    fixtures here (env["HOME"] is the scratch home,
                    env["WORKSPACE_DIR"] the staged checkout)
  SKIP_STEP_SUBSTR  substrings of [oracle].steps that prepare() already
                    covered locally (e.g. "docker build"), skipped verbatim

Layout matches the bench: the repo is staged into WORKSPACE_DIR, which is a
directory UNDER HOME, and steps run with WORKSPACE_DIR as cwd. So a `~/...`
assertion is about the pilot's home, never about a file that shipped in the
checkout.

One difference from the bench has bitten before, so expect more of its kind:
the scratch HOME always has a stub `claude` on PATH, and a default bench has
none. A suite that depends on an agent EXISTING must put the stub on PATH
itself rather than inherit it from this harness.

Needs a checkout of the engine for its scenario parser (no Docker, no
bench): `gentar/run.sh --stage-engine` once, or GENTAR_ENGINE pointing
at an existing one.
"""
import os, pty, re, select, shlex, shutil, subprocess, sys, tempfile, time
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
# The engine supplies the scenario PARSER, so a dry run needs a checkout
# of it — not a bench, not Docker. Resolution order: an explicit
# override, then the pinned clone run.sh maintains at gentar/.arena, so
# the parser that validates a suite here is the same version the arena
# will run it with. `gentar/run.sh --stage-engine` creates that clone
# without spending a bench.
_engine_env = os.environ.get("GENTAR_ENGINE")
_here = Path(__file__).resolve().parent
ENGINE = Path(_engine_env) if _engine_env else _here / ".arena/coordinator"
if not ENGINE.exists():
    sys.exit(f"gentar engine not found at {ENGINE}\n"
             "  stage it once:  gentar/run.sh --stage-engine\n"
             "  or point at an existing checkout:  "
             "GENTAR_ENGINE=/path/to/gentar/coordinator gentar/dryrun.py")
sys.path.insert(0, str(ENGINE))

# tomllib is 3.11+, and stock macOS still ships 3.9 — so the cheap check an
# adopter is told to run FIRST is the one most likely to fail on a machine
# nobody prepared. Three ways out, in order of least surprise:
#
#   1. re-exec under a newer interpreter if one is already on PATH;
#   2. otherwise use tomli, which IS tomllib under its pre-stdlib name —
#      the same parser, so a scenario cannot mean one thing here and
#      another in the arena;
#   3. otherwise say exactly what to install, rather than "needs 3.11+".
#
# The arena itself never reaches any of this: the coordinator image is
# python:3.12-slim. This is purely about the host-side replay.
if sys.version_info < (3, 11):
    for candidate in ("python3.14", "python3.13", "python3.12", "python3.11"):
        if shutil.which(candidate):
            os.execvp(candidate, [candidate, os.path.abspath(__file__), *sys.argv[1:]])
    try:
        import tomli  # noqa: F401  — imported for the check; the parser imports it
    except ModuleNotFoundError:
        sys.exit(
            f"dryrun needs a TOML parser and this is python {sys.version.split()[0]} "
            "(tomllib arrived in 3.11).\n"
            "  either:  pip install tomli\n"
            "  or:      install any python 3.11+ and re-run "
            "(brew install python@3.12, apt install python3.12, ...)\n"
            "The arena is unaffected either way — it runs python 3.12 in a "
            "container. This is only the local replay."
        )

from gentar.toml_scenario import TomlScenario

# Steps whose substring appears here are skipped verbatim (prepare()
# already did the equivalent locally). Example: ("docker build",).
SKIP_STEP_SUBSTR = (
    # Every suite begins by building claude-playbook in a golang:1.21
    # container from the staged checkout, then installing it to
    # ~/.local/bin. prepare() does both, with the local toolchain.
    'docker run --rm -u "$(id -u):$(id -g)" -e HOME=/tmp -v "$WORKSPACE_DIR":/src',
    'install -m 755 "$WORKSPACE_DIR/claude-playbook" "$HOME/.local/bin/claude-playbook"',
)

# Executables that must NEVER be found on your real PATH while a suite
# runs — names your suites CREATE themselves (a launcher, an alias
# binary). Anything prepare() installs into the scratch ~/.local/bin is
# hidden automatically; list only what it does not. Example: ("cpb",).
# cpb: suites create it themselves, as a real install does (a relative
# symlink). pilot: not created by a suite, but claude-playbook's create and
# install call `pilot wire` whenever pilot is on PATH, and a bench has none --
# reaching the pilot's real one would edit scratch playbooks through the
# host's own lock dir.
HIDE_FROM_PATH = ("cpb", "pilot")


def prepare(env: dict) -> None:
    """Stand in for this suite's own build-and-install, in its fresh home.

    Mirrors the bench: run.sh freezes the version into the staged checkout
    as .gentar-version (the bench has no usable .git), the container step
    builds $WORKSPACE_DIR/claude-playbook with it, and the install step
    copies it to ~/.local/bin. cli-head-build asserts the binary reports that
    version, so it is resolved the same way, --match 'v*' included.

    No `cpb` symlink: nothing on the bench makes one; the suites that need it
    create it themselves. `go build` caches on its own, so building per suite
    costs little.
    """
    ws = env["WORKSPACE_DIR"]
    v = subprocess.run(
        ["git", "-C", str(REPO), "describe", "--tags", "--always", "--dirty", "--match", "v*"],
        capture_output=True, text=True).stdout.strip() or "dev"
    Path(ws, ".gentar-version").write_text(v + "\n")
    built = Path(ws, "claude-playbook")
    r = subprocess.run(
        ["go", "build", "-ldflags",
         f"-X github.com/ramazanpolat/claude-playbooks/cmd.Version={v}", "-o", str(built), "."],
        cwd=ws, capture_output=True, text=True)
    if r.returncode:
        sys.exit("build failed:\n" + r.stderr)
    dest = Path(env["HOME"], ".local/bin/claude-playbook")
    dest.parent.mkdir(parents=True, exist_ok=True)
    shutil.copy(built, dest)
    os.chmod(dest, 0o755)


def scratch_home() -> str:
    home = tempfile.mkdtemp(prefix="dryrun-home-")
    bindir = Path(home, ".local/bin")
    bindir.mkdir(parents=True)
    # The bench template ships a real claude; here a stub stands in.
    # Scenarios needing to observe what a launch handed it install
    # their own.
    (bindir / "claude").write_text("#!/bin/sh\nexit 0\n")
    os.chmod(bindir / "claude", 0o755)
    return home


def stage_subject(workspace: str) -> None:
    """Copy the repo into the scratch WORKSPACE, as the bench does.

    In the arena, oracle pushes the staged subject INTO the bench
    workspace, so $WORKSPACE_DIR contains the checkout at its root and
    steps can write alongside it without dirtying anything real. The
    same must hold here or a suite that passes locally and fails in the
    bench (or worse, the reverse) is a harness lie. .git, the engine
    clone and old reports stay out — dead weight on the bench too.

    The workspace is a directory UNDER the home, never the home itself
    (the bench's is `<home>/gentar-workspaces/<run>`). Collapsing the
    two made a repo file named `.config/tool` satisfy a `~/.config/tool`
    assertion before any step created it (PR #27 review).
    """
    def skip(directory, contents):
        d = Path(directory)
        if d == REPO:
            return [".git"]
        if d == REPO / "gentar":
            return [c for c in contents if c in (".arena", "reports")]
        return []
    shutil.copytree(REPO, workspace, dirs_exist_ok=True, symlinks=True,
                    ignore=skip)



def sealed_path(home: str, hidden: set) -> str:
    """The scratch bin, then the host PATH with `hidden` made unreachable.

    The host PATH stays so steps find git, go and the rest of userland. But
    the subject's own executables must not be found there: once a suite
    removes the scratch copy — an uninstall suite does exactly that — a
    later call must fail as "not found", not fall through to the pilot's
    REAL install and run it against the scratch HOME. That happened to the
    first real adopter (claude-playbooks): the dry-run reached the pilot's
    installed CLI, which created launchers next to itself in the real
    ~/.local/bin. A local check that can modify the machine it runs on is
    worse than no check.

    `hidden` is everything prepare() put in the scratch bin — subject-
    provided by definition, including the stub `claude`, so a suite that
    deletes the stub cannot reach the pilot's real, authenticated agent —
    plus HIDE_FROM_PATH.

    Hiding is per executable, not per directory. A directory holding a
    hidden name is replaced by a shim directory of symlinks to everything
    ELSE in it, so a tool that shares ~/.local/bin with the CLI stays
    reachable. Dropping the whole directory fails in ways a fresh bench
    cannot.
    """
    # Two ways a hidden binary stayed reachable, both found in review:
    #   - CASE. macOS filesystems are case-insensitive by default: a real
    #     `Widget` on disk is what `widget` resolves to, but a plain `n in
    #     hidden` test said "not hidden" and symlinked it into the shim.
    #     Names are compared casefolded.
    #   - ALIASES. `cpb` ships as a symlink to `claude-playbook`. Hiding the
    #     name `claude-playbook` left `cpb` pointing straight at the pilot's
    #     real install — the adopter's exact shape. Anything that resolves to
    #     the SAME FILE (device + inode, after following links — which also
    #     catches hardlinks) as a hidden binary is hidden too.
    hidden_cf = {h.casefold() for h in hidden}
    dirs = [d for d in os.environ.get("PATH", "").split(os.pathsep)
            if d and d != f"{home}/.local/bin"]

    def listing(d):
        try:
            return os.listdir(d)
        except OSError:
            return []

    def ident(path):
        try:
            st = os.stat(path)            # follows symlinks: the real file
            return (st.st_dev, st.st_ino)
        except OSError:
            return None

    targets = set()                        # the real files behind hidden names
    for d in dirs:
        for n in listing(d):
            if n.casefold() in hidden_cf:
                t = ident(os.path.join(d, n))
                if t:
                    targets.add(t)

    def is_hidden(d, n):
        return n.casefold() in hidden_cf or (
            bool(targets) and ident(os.path.join(d, n)) in targets)

    shims = Path(home, ".dryrun-path-shims")
    out = [f"{home}/.local/bin"]
    for i, d in enumerate(dirs):
        names = listing(d)
        if not any(is_hidden(d, n) for n in names):
            out.append(d)
            continue
        shim = shims / str(i)
        # Rebuilt from empty, never topped up: an entry left from an earlier
        # sealing would survive a skip, and a stale link to a hidden binary
        # is exactly the leak this function exists to close.
        shutil.rmtree(shim, ignore_errors=True)
        shim.mkdir(parents=True)
        for n in names:
            src = os.path.join(d, n)
            if is_hidden(d, n) or os.path.isdir(src) or not os.access(src, os.X_OK):
                continue
            os.symlink(src, shim / n)
        out.append(str(shim))
    return os.pathsep.join(out)

def run_one(path: Path, env: dict, home: str, workspace: str) -> int:
    sc = TomlScenario(path)
    if sc.credentials:
        print(f"{path.name}: SKIPPED (declares credentials; needs a real agent)")
        return 0
    fails, log = 0, []
    # cwd is the WORKSPACE (where the checkout was staged), as on a
    # bench; HOME in env stays a separate directory.
    sh = lambda c: subprocess.run(["sh", "-c", c], env=env, cwd=workspace,
                                  capture_output=True, text=True)

    for i, step in enumerate(sc.steps):
        if any(s in step for s in SKIP_STEP_SUBSTR):
            continue
        r = sh(step)
        if r.returncode:
            fails += 1
            log.append(f"  step {i} EXIT {r.returncode}\n    {step[:160]}\n    {(r.stderr or r.stdout).strip()[:300]}")

    unreplayed: list[str] = []
    if sc.driver_command:
        fails += drive(sc, env, workspace, log, unreplayed)

    # File assertions go through the same shell as the steps, for the
    # same reason the engine runs them inside the bench: `test -e` and
    # `grep -F` resolve a RELATIVE path against the workspace there, so
    # checking from the harness's own cwd would answer a different
    # question than the arena does. `~` is expanded to the scratch home
    # exactly as check_files expands it to the bench pilot's.
    for f in sc.files:
        p = f["path"].replace("~", home, 1) if f["path"].startswith("~") else f["path"]
        contains = f.get("contains")
        if contains is None:
            # file-exists
            if sh(f"test -e {shlex.quote(p)}").returncode:
                fails += 1
                log.append(f"  file MISSING {f['path']}")
        else:
            # file-contains — the engine greps, so a file with the wrong
            # contents must fail here too, not just be present (PR #27
            # review). grep also reports a missing file as nonzero,
            # matching check_files exactly.
            if sh(f"grep -F -- {shlex.quote(contains)} {shlex.quote(p)}").returncode:
                fails += 1
                log.append(f"  file {f['path']} LACKS {contains!r} (or is missing)")

    for i, c in enumerate(sc.commands):
        r = sh(c["command"])
        ok = r.returncode == 0 and c.get("contains", "") in (r.stdout + r.stderr)
        if not ok:
            fails += 1
            want = f"  (wanted {c['contains']!r})" if "contains" in c else ""
            log.append(f"  verify {i} FAIL{want}\n    {c['command'][:180]}\n    {(r.stdout + r.stderr).strip()[:300] or '(no output)'}")

    if not fails:
        verdict = "ALL PASS"
    elif unreplayed and fails == len(unreplayed):
        # Nothing actually failed — the harness just cannot verify
        # these. Say so instead of either lying (ALL PASS) or crying
        # wolf (FAILURES); the exit code is still nonzero, so CI and the
        # caller treat "unverified" as "not proven".
        verdict = (f"UNVERIFIED ({', '.join(sorted(set(unreplayed)))} "
                   "turns need the arena)")
    else:
        verdict = f"{fails} FAILURES"
    print(f"{path.name}: {verdict}")
    for line in log:
        print(line)
    if fails:
        print(f"  (home kept for inspection: {home})")
    return fails


def drive(sc, env, cwd, log, unreplayed) -> int:
    """Replay [driver].turns over a pty, as gentar/scripted.py does.

    `unreplayed` collects turn types this harness cannot exercise, so
    the caller can refuse to print ALL PASS for them.
    """
    pid, fd = pty.fork()
    if pid == 0:
        os.chdir(cwd)
        os.execvpe("sh", ["sh", "-c", sc.driver_command], env)
    buf, fails = "", 0

    def pump(pattern, timeout):
        nonlocal buf
        rx, end = re.compile(pattern, re.IGNORECASE), time.time() + timeout
        while time.time() < end:
            if rx.search(buf):
                return True
            if select.select([fd], [], [], 0.4)[0]:
                try:
                    chunk = os.read(fd, 4096).decode(errors="replace")
                except OSError:
                    break
                if not chunk:
                    break
                buf += chunk
        return bool(rx.search(buf))

    for i, t in enumerate(sc.turns):
        kind = t["type"]
        if kind == "answer":
            ok = pump(t["prompt"], t.get("timeout", 60))
            if ok:
                os.write(fd, (t.get("send", "") + "\n").encode())
                buf = ""   # consumed, so the next turn matches a fresh prompt
            if not ok:
                log.append(f"  turn {i} answer /{t['prompt']}/: prompt never appeared")
        elif kind == "expect":
            ok = pump(t["pattern"], t.get("timeout", 60))
            if not ok:
                log.append(f"  turn {i} expect /{t['pattern']}/: pattern never appeared")
        else:
            # `pick` (navigate a picker by ❯ cursor line) and `abort`
            # (the danger gate) need the real driver. Counting them as
            # passes printed ALL PASS for a suite whose picker label was
            # absent or whose danger gate never fired — a harness lie in
            # the dangerous direction (PR #27 review). Not replayed is
            # not verified: the suite is reported UNVERIFIED and the
            # dry-run does not claim success for it.
            ok = False
            unreplayed.append(kind)
            log.append(f"  turn {i}: type {kind!r} needs the real driver "
                       f"— NOT verified here (run it in the arena)")
        if not ok:
            fails += 1
    try:
        while select.select([fd], [], [], 1.0)[0]:
            if not os.read(fd, 4096):
                break
    except OSError:
        pass
    os.waitpid(pid, 0)
    return fails


def main() -> int:
    paths = [Path(a) for a in sys.argv[1:]] or sorted((REPO / "gentar/scenarios").glob("*.toml"))
    # A FRESH home, workspace and prepare() per suite, as the engine gives
    # each scenario a fresh bench. Sharing one across the sweep let a suite's
    # state leak into the next (claude-playbooks: an uninstall suite removed
    # the binary and every later suite ran without it — then past it, see
    # sealed_path). A suite that passes here only because an earlier one
    # left something behind is a harness lie.
    fails = 0
    for p in paths:
        home = scratch_home()
        # Workspace UNDER home, as on a bench — never equal to it, or a
        # checkout file can satisfy a `~/...` assertion for free.
        workspace = os.path.join(home, "gentar-workspaces/dryrun")
        os.makedirs(workspace, exist_ok=True)
        stage_subject(workspace)
        bindir = f"{home}/.local/bin"
        # prepare() runs with the ordinary PATH: it is your trusted build
        # step and needs your toolchain. The SUITE then runs sealed.
        env = dict(os.environ, HOME=home, WORKSPACE_DIR=workspace,
                   PATH=f"{bindir}:" + os.environ["PATH"])
        prepare(env)
        hidden = set(os.listdir(bindir)) | set(HIDE_FROM_PATH)
        env["PATH"] = sealed_path(home, hidden)
        failed = run_one(p, env, home, workspace)
        fails += failed
        if failed:
            print(f"  scratch home kept for inspection: {home}")
        else:
            shutil.rmtree(home, ignore_errors=True)
    return 1 if fails else 0


if __name__ == "__main__":
    sys.exit(main())
