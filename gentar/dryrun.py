#!/usr/bin/env python3
"""Run an own-arena scenario locally, without a bench.

    gentar/dryrun.py gentar/scenarios/playbook-update.toml
    gentar/dryrun.py gentar/scenarios/*.toml        # sweep

Why this exists: a scenario is shell, and shell in TOML is three levels of
quoting deep. The arena is the only thing that ever ran these, at the cost of
a bench VM and several minutes per suite -- so a missing quote cost a full
round trip to find. This executes the same steps, the same driver turns and
the same assertions in a scratch HOME, in about a second, and fails the same
way.

What it is NOT: a bench. There is no sandbox, no template, no network policy,
no real agent. It proves the shell and the assertions; the arena still proves
the isolation. Suites declaring `credentials` are skipped -- they need a real
agent and a real key.

The binary is built once from the checkout with the same version injection
the scenarios use, because some of them assert on `--version`.
"""
import os, pty, re, select, shutil, subprocess, sys, tempfile, time
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
ENGINE = Path.home() / "agent-realm/gentar/coordinator"
if not ENGINE.exists():
    sys.exit(f"gentar engine not found at {ENGINE} (clone agent-realm/gentar)")
sys.path.insert(0, str(ENGINE))

# tomllib is 3.11+. Stock macOS ships 3.9, so rather than fail on the default
# interpreter, re-exec under the first newer one on PATH.
if sys.version_info < (3, 11):
    for candidate in ("python3.14", "python3.13", "python3.12", "python3.11"):
        if shutil.which(candidate):
            os.execvp(candidate, [candidate, os.path.abspath(__file__), *sys.argv[1:]])
    sys.exit(f"needs python 3.11+ for tomllib; this is {sys.version.split()[0]} "
             "and no newer python3.1x was found on PATH")

from gentar.toml_scenario import TomlScenario


def build() -> str:
    out = Path(tempfile.mkdtemp(prefix="dryrun-bin-")) / "claude-playbook"
    v = subprocess.run(["git", "-C", str(REPO), "describe", "--tags", "--always", "--dirty"],
                       capture_output=True, text=True).stdout.strip() or "dev"
    r = subprocess.run(
        ["go", "build", "-ldflags",
         f"-X github.com/ramazanpolat/claude-playbooks/cmd.Version={v}", "-o", str(out), "."],
        cwd=REPO, capture_output=True, text=True)
    if r.returncode:
        sys.exit("build failed:\n" + r.stderr)
    return str(out)


def scratch_home(binary: str) -> str:
    home = tempfile.mkdtemp(prefix="dryrun-home-")
    bindir = Path(home, ".local/bin")
    bindir.mkdir(parents=True)
    shutil.copy(binary, bindir / "claude-playbook")
    os.chmod(bindir / "claude-playbook", 0o755)
    os.symlink("claude-playbook", bindir / "cpb")
    # The bench template ships a real claude; here a stub stands in. Scenarios
    # needing to observe what a launch handed it install their own.
    (bindir / "claude").write_text("#!/bin/sh\nexit 0\n")
    os.chmod(bindir / "claude", 0o755)
    return home


def run_one(path: Path, binary: str) -> int:
    sc = TomlScenario(path)
    if sc.credentials:
        print(f"{path.name}: SKIPPED (declares credentials; needs a real agent)")
        return 0
    home = scratch_home(binary)
    env = dict(os.environ, HOME=home, WORKSPACE_DIR=str(REPO),
               PATH=f"{home}/.local/bin:" + os.environ["PATH"])
    sh = lambda c: subprocess.run(["sh", "-c", c], env=env, cwd=home,
                                  capture_output=True, text=True)
    fails, log = 0, []

    for i, step in enumerate(sc.steps):
        # The docker build and its install are what `build()` already did.
        if "docker run" in step or ("install -m 755" in step and "claude-playbook" in step):
            continue
        r = sh(step)
        if r.returncode:
            fails += 1
            log.append(f"  step {i} EXIT {r.returncode}\n    {step[:160]}\n    {(r.stderr or r.stdout).strip()[:300]}")

    if sc.driver_command:
        fails += drive(sc, env, home, log)

    for f in sc.files:
        p = f["path"].replace("~", home, 1) if f["path"].startswith("~") else f["path"]
        if not os.path.lexists(p):
            fails += 1
            log.append(f"  file MISSING {f['path']}")

    for i, c in enumerate(sc.commands):
        r = sh(c["command"])
        ok = r.returncode == 0 and c.get("contains", "") in (r.stdout + r.stderr)
        if not ok:
            fails += 1
            want = f"  (wanted {c['contains']!r})" if "contains" in c else ""
            log.append(f"  verify {i} FAIL{want}\n    {c['command'][:180]}\n    {(r.stdout + r.stderr).strip()[:300] or '(no output)'}")

    print(f"{path.name}: {'ALL PASS' if not fails else f'{fails} FAILURES'}")
    for line in log:
        print(line)
    if fails:
        print(f"  (home kept for inspection: {home})")
    return fails


def drive(sc, env, home, log) -> int:
    """Replay [driver].turns over a pty, as gentar/scripted.py does."""
    pid, fd = pty.fork()
    if pid == 0:
        os.chdir(home)
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
            ok = True
            log.append(f"  turn {i}: type {kind!r} not replayed locally (arena only)")
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
    binary = build()
    return 1 if sum(run_one(p, binary) for p in paths) else 0


if __name__ == "__main__":
    sys.exit(main())
