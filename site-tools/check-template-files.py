#!/usr/bin/env python3
"""Checks the template files in site/p/ against a real cpb.

site/p/<id>.cpb are plain recipes. `cpb play <name>` resolves a template name
to one of them at the running cpb's own release tag, so a template that stops
being playable is a bug for every release that tags it. This runs on every
change to them and to cpb's play code:

  1. `cpb play --check site/p`: cpb's own rules for a template directory (the
     header, what a played recipe may not hold, risks, and an index.txt that
     lists exactly the files here, sorted);
  2. each file follows the agreed convention (ASCII, a header of
     `-- key: value` lines, one name-less ALTER PLAYBOOK, no secret), plans
     with APPLY --dry-run --json, applies to a new playbook in a throwaway
     HOME (stand-ins for `claude` (examples/.ci) and the secret helper (site-tools/), a
     local mirror for `github:` skill sources), and applying it again
     changes nothing.

  check-template-files.py --cpb <cpb>
"""
import argparse
import json
import re
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from sitelib import Run, REPO  # noqa: E402

P = REPO / "site" / "p"
HEADER = re.compile(r"^-- (title|description|min-cpb|needs|create-with): (\S.*)$")
SECRETS = re.compile(r"ghp_[A-Za-z0-9]{20}|sk-[A-Za-z0-9_-]{20}|xox[bp]-|AKIA[0-9A-Z]{16}|Bearer [A-Za-z0-9._-]{20}")


def fail(msg):
    print(f"FAIL  {msg}", file=sys.stderr)
    return 1


def lint(path):
    """The agreed convention for a template file; returns the problems."""
    text = path.read_text(encoding="utf-8")
    bad = []
    if not text.isascii():
        bad.append("not ASCII")
    lines = text.splitlines()
    head = []
    for line in lines:
        if not line.startswith("--"):
            break
        head.append(line)
    keys = []
    for line in head:
        m = HEADER.match(line)
        if not m:
            bad.append(f"header line is not '-- key: value': {line[:50]}")
        else:
            keys.append(m.group(1))
    for k in ("title", "description", "min-cpb"):
        if k not in keys:
            bad.append(f"header lacks {k}")
    if len(lines) <= len(head) or lines[len(head)] != "":
        bad.append("no blank line after the header")
    body = "\n".join(lines[len(head):])
    if not body.lstrip().startswith("ALTER PLAYBOOK\n"):
        bad.append("the body is not one name-less ALTER PLAYBOOK")
    for word in ("CREATE ", "USE PLAYBOOK", "INCLUDE", "ALTER DEFAULTS", "DROP ", "UNSET ", " USE ENV", "ALTER ENV"):
        if word in body:
            bad.append(f"the recipe contains {word.strip()}")
    if SECRETS.search(text):
        bad.append("looks like it holds a secret")
    if not text.endswith("\n"):
        bad.append("no newline at the end")
    return bad


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--cpb", required=True)
    a = ap.parse_args()
    bad = 0

    ids = [l for l in (P / "index.txt").read_text(encoding="utf-8").split() if l]
    files = sorted(p.stem for p in P.glob("*.cpb"))
    if ids != sorted(ids):
        bad |= fail("site/p/index.txt is not sorted")
    if ids != files:
        bad |= fail(f"site/p/index.txt lists {ids}; the files are {files}")

    r = Run(a.cpb, [(P / f"{t}.cpb").read_text(encoding="utf-8") for t in files])
    try:
        code, out = r.raw("play", "--check", str(P))
        print(out.rstrip())
        if code != 0:
            bad |= fail("cpb play --check site/p failed")

        for tid in files:
            path = P / f"{tid}.cpb"
            for problem in lint(path):
                bad |= fail(f"site/p/{tid}.cpb: {problem}")
            args = [str(path), "TO", "t-" + tid]
            code, out, err = r.split("APPLY", *args, "--dry-run", "--json")
            try:
                ok = code == 0 and json.loads(out)["ok"]
            except ValueError:
                ok = False
            if not ok:
                bad |= fail(f"site/p/{tid}.cpb does not plan: {out[:300]} {err[:300]}")
                continue
            code, out = r.raw("APPLY", *args)
            if code != 0:
                bad |= fail(f"site/p/{tid}.cpb does not apply: {out[-300:]}")
                continue
            code, out = r.raw("APPLY", *args)
            if code != 0 or " 0 created, 0 changed, " not in out:
                bad |= fail(f"site/p/{tid}.cpb is not idempotent: {out[-200:]}")
    finally:
        r.close()

    if not bad:
        print(f"ok    templates: {len(files)} files follow the convention, apply to a new playbook, and apply again as a no-op")
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
