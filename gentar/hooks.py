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
#     would let a suite pass that fails on the bench.
# Anything prepare() installs into the scratch ~/.local/bin is hidden
# automatically; list only what it does not. Example: ("cpb", "pilot").
# cpb: suites create it themselves, as a real install does (a relative
# symlink).
HIDE_FROM_PATH = ("cpb",)


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


# No suite needs a bench template beyond the default and cpb-agent-bench-v1
# (baked on the bench-host; nothing to stage for a dry-run).
TEMPLATES = {}
