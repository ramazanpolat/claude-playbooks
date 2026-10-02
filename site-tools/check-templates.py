#!/usr/bin/env python3
"""Plans and applies the customizer's output with a real cpb.

The page and this check share one implementation (site/customizer-core.js,
run here under Node), so what a visitor copies is what is tested:

  1. site/p/*.cpb and site/p/index.txt are exactly what the code renders (the
     files themselves are checked against cpb by check-template-files.py);
  2. for every template: its defaults, an "everything switched on" selection and
     a reproducible set of random selections, each in both forms (a complete
     playbook file, and a recipe plus the CREATE command), is planned with
     APPLY --dry-run --json (it must say ok), and the defaults and everything
     cases are applied for real, twice.

  check-templates.py --cpb <cpb> [--random N]
"""
import argparse
import json
import subprocess
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from sitelib import Run, REPO  # noqa: E402

TOOLS = REPO / "site-tools"


def node(*args):
    p = subprocess.run(["node", str(TOOLS / "templates-cases.js"), *args], capture_output=True, encoding="utf-8")
    return p.returncode, p.stdout, p.stderr


def fail(msg):
    print(f"FAIL  {msg}", file=sys.stderr)
    return 1


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

    code, out, err = node("--cases", "--random", str(a.random))
    if code:
        print(err, file=sys.stderr)
        return 1
    cases = json.loads(out)

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
