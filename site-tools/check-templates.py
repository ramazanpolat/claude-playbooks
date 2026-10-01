#!/usr/bin/env python3
"""Applies the curated templates, and many customizer selections, with a real cpb.

The page and this check share one implementation (site/customizer-core.js,
run here under Node), so what a visitor copies is what is tested:

  1. site/p/*.cpb and site/p/index.txt are exactly what the code renders;
  2. every template file is a name-less recipe with the agreed header, holds no
     secret, applies to a new playbook, and applying it again changes nothing;
  3. for every template: its defaults, an "everything switched on" selection and
     a reproducible set of random selections, each in both forms (a complete
     playbook file, and a recipe plus the CREATE command), is planned with
     APPLY --dry-run --json (it must say ok), and the defaults and everything
     cases are applied for real, twice.

  check-templates.py --cpb <cpb> [--random N]
"""
import argparse
import json
import re
import shutil
import subprocess
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from sitelib import Run, REPO  # noqa: E402

TOOLS = REPO / "site-tools"
P = REPO / "site" / "p"
HEADER = re.compile(r"^-- (title|description|min-cpb|needs|create-with): (\S.*)$")
SECRETS = re.compile(r"ghp_[A-Za-z0-9]{20}|sk-[A-Za-z0-9_-]{20}|xox[bp]-|AKIA[0-9A-Z]{16}|Bearer [A-Za-z0-9._-]{20}")


def node(*args):
    p = subprocess.run(["node", str(TOOLS / "templates-cases.js"), *args], capture_output=True, encoding="utf-8")
    return p.returncode, p.stdout, p.stderr


def fail(msg):
    print(f"FAIL  {msg}", file=sys.stderr)
    return 1


def lint(path):
    """The agreed convention for a template file."""
    text = path.read_text(encoding="utf-8")
    bad = []
    if not text.isascii():
        bad.append("not ASCII")
    lines = text.splitlines()
    head = []
    for l in lines:
        if not l.startswith("--"):
            break
        head.append(l)
    keys = []
    for l in head:
        m = HEADER.match(l)
        if not m:
            bad.append(f"header line is not '-- key: value': {l[:50]}")
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
    ap.add_argument("--random", type=int, default=6)
    a = ap.parse_args()
    bad = 0

    code, out, err = node("--check")
    print((out or err).strip())
    if code:
        return 1

    ids = [l for l in (P / "index.txt").read_text(encoding="utf-8").split() if l]
    if ids != sorted(ids):
        bad |= fail("site/p/index.txt is not sorted")

    code, out, err = node("--cases", "--random", str(a.random))
    if code:
        print(err, file=sys.stderr)
        return 1
    cases = json.loads(out)

    # ---- the template files themselves
    r = Run(a.cpb, [p.read_text(encoding="utf-8") for p in P.glob("*.cpb")])
    try:
        for tid in ids:
            path = P / f"{tid}.cpb"
            for problem in lint(path):
                bad |= fail(f"site/p/{tid}.cpb: {problem}")
            name = "t-" + tid
            args = [str(path), "TO", name]
            code, out, err = r.split("APPLY", *args, "--dry-run", "--json")
            if code != 0 or not json.loads(out)["ok"]:
                bad |= fail(f"site/p/{tid}.cpb does not plan: {out[:300]} {err[:300]}")
                continue
            code, out = r.raw("APPLY", *args)
            if code != 0:
                bad |= fail(f"site/p/{tid}.cpb does not apply: {out[-300:]}")
                continue
            code, out = r.raw("APPLY", *args)
            if code != 0 or " 0 created, 0 changed, " not in out:
                bad |= fail(f"site/p/{tid}.cpb is not idempotent: {out[-200:]}")
        print(f"ok    templates: {len(ids)} files follow the convention, apply, and apply again as a no-op" if not bad else "")
    finally:
        r.close()

    # ---- the customizer's selections, one throwaway home per template
    by_t = {}
    for c in cases:
        by_t.setdefault(c["template"], []).append(c)
    planned = applied = 0
    for tid, group in by_t.items():
        r = Run(a.cpb, [c["text"] for c in group])
        try:
            for n, c in enumerate(group):
                f = r.home / f"c{n}.cpb"
                f.write_text(c["text"], encoding="utf-8")
                if c["mode"] == "recipe":
                    if c["create"]:
                        code, out = r.raw("CREATE", "PLAYBOOK", c["name"], *" ".join(c["create"]).split())
                        if code != 0:
                            bad |= fail(f"{c['id']}: CREATE PLAYBOOK {c['name']} {' '.join(c['create'])} failed: {out[-200:]}")
                            continue
                    args = [str(f), "TO", c["name"]]
                else:
                    args = [str(f)]
                code, out, err = r.split("APPLY", *args, "--dry-run", "--json")
                try:
                    ok = code == 0 and json.loads(out)["ok"]
                except ValueError:
                    ok = False
                if not ok:
                    bad |= fail(f"{c['id']} (#{n}) does not plan:\n{out[:400]}\n{err[:400]}\n{c['text']}")
                    continue
                planned += 1
                if c["kind"] in ("default", "everything"):
                    for second in (False, True):
                        code, out = r.raw("APPLY", *args)
                        if code != 0 or (second and " 0 created, 0 changed, " not in out):
                            bad |= fail(f"{c['id']}: {'second ' if second else ''}apply failed: {out[-300:]}")
                            break
                    else:
                        applied += 1
        finally:
            r.close()
    if not bad:
        print(f"ok    customizer: {planned} selections plan with APPLY --dry-run --json, {applied} also apply twice for real")
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
