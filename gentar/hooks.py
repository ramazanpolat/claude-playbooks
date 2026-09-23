"""This repo's dry-run hooks — the only part of the dry-run that is yours.

gentar/dryrun.py is the kit's file and stays byte-identical to the pinned
engine's copy (`gentar/run.sh --check` compares), so everything a subject
needs to adapt lives here. Any name left out keeps dryrun.py's default.
REPO below is the checkout, for a prepare() that builds from it.
"""
import os
import shutil
import subprocess
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent

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
# runs. Two reasons to list one:
#   - your suites CREATE it (a launcher, an alias binary), so finding
#     the installed copy would let a broken install pass;
#   - your code CALLS it and a bench does not have it, so finding it here
#     would let a suite pass that fails on the bench. (claude-playbooks'
#     CLI runs `pilot` on every create; benches have no `pilot`.)
# Anything prepare() installs into the scratch ~/.local/bin is hidden
# automatically; list only what it does not. Example: ("cpb", "pilot").
# cpb: suites create it themselves, as a real install does (a relative
# symlink). pilot: claude-playbook's create and install call `pilot wire`
# whenever pilot is on PATH, and a bench has none.
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


def stage_pilot(env: dict) -> bool:
    """Put the REAL `pilot`, at the release cpb-pilot-bench-v1 bakes in, on
    this suite's scratch PATH -- or say it cannot.

    pilot-profile is private and this repo is public, so the source is a
    local pilot-profile checkout (PILOT_PROFILE_SRC, default
    ~/agentship/pilot-profile), exported with `git archive` at the SHA pinned
    in bench-template/PILOT_PROFILE, exactly as build-pilot.sh does for the
    template. False -- the suite is UNVERIFIED, never faked -- when there is
    no checkout (a hosted runner) or its tag does not resolve to the pin.
    """
    pin = dict(line.split("=", 1) for line in
               (REPO / "gentar/bench-template/PILOT_PROFILE").read_text().splitlines()
               if "=" in line and not line.startswith("#"))
    src = Path(os.environ.get("PILOT_PROFILE_SRC",
                              Path.home() / "agentship/pilot-profile"))
    if not (src / ".git").exists():
        return False
    got = subprocess.run(["git", "-C", str(src), "rev-parse", "-q", "--verify",
                          pin["TAG"] + "^{commit}"], capture_output=True, text=True)
    if got.returncode or got.stdout.strip() != pin["SHA"]:
        return False
    # Never under ~/.pilot-profile: the suite asserts wiring creates no profile.
    root = Path(env["HOME"], "opt/pilot-profile")
    root.mkdir(parents=True, exist_ok=True)
    archive = subprocess.run(["git", "-C", str(src), "archive", "--format=tar", pin["SHA"]],
                             capture_output=True, check=True).stdout
    subprocess.run(["tar", "-x", "-C", str(root)], input=archive, check=True)
    (root / ".pinned-sha").write_text(pin["SHA"] + "\n")
    subprocess.run([str(root / "install.sh"), "--bin", str(Path(env["HOME"], ".local/bin")),
                    "--no-init"], env=env, capture_output=True, check=True)
    return True


# cpb-pilot-bench-v1 bakes pilot-profile into the bench (build-pilot.sh);
# stage_pilot stands in for it wherever a checkout of the pinned release is.
TEMPLATES = {"cpb-pilot-bench-v1": stage_pilot}
