#!/usr/bin/env python3
"""Keeps the customizer's list of reserved names equal to cpb's own.

The customizer refuses a playbook name that is a cpb keyword (cpb refuses it
too), so its list, KEYWORDS in site/customizer-core.js, has to be cpb's list:
a word missing from it lets a visitor build a file cpb then refuses, and an
extra word turns away a name cpb accepts. Two checks:

  1. the list equals the grammar's, read from internal/grammar/parse.go;
  2. a real cpb agrees: every word on the list is refused as a playbook name,
     and the words this list once held by mistake are accepted.

  check-keywords.py --cpb <cpb>
"""
import argparse
import json
import re
import subprocess
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from sitelib import Run, REPO  # noqa: E402

# words that are not keywords, but that an earlier copy of the list held
NOT_KEYWORDS = ["alias", "isolated", "login", "select"]


def cpb_keywords():
    src = (REPO / "internal" / "grammar" / "parse.go").read_text(encoding="utf-8")
    try:
        i = src.index("func init()")
        j = src.index("} {\n\t\tkeywords[w]", i)
    except ValueError:
        sys.exit("FAIL  internal/grammar/parse.go no longer has the keyword list this check reads (func init ... keywords[w]); update site-tools/check-keywords.py")
    return set(re.findall(r'"([A-Z]+)"', src[i:j]))


def site_keywords():
    p = subprocess.run(["node", "-e", "console.log(JSON.stringify(require('./site/customizer-core.js').KEYWORDS))"],
                       cwd=REPO, capture_output=True, encoding="utf-8")
    if p.returncode != 0:
        sys.exit(f"FAIL  could not read KEYWORDS from site/customizer-core.js: {p.stderr}")
    return set(json.loads(p.stdout))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--cpb", required=True)
    a = ap.parse_args()
    bad = 0
    theirs, ours = cpb_keywords(), site_keywords()
    if len(theirs) < 20:
        sys.exit(f"FAIL  read only {len(theirs)} keywords from the grammar source; the parse is wrong")
    for word in sorted(theirs - ours):
        print(f"FAIL  {word} is a cpb keyword but is not in KEYWORDS (site/customizer-core.js)", file=sys.stderr)
        bad = 1
    for word in sorted(ours - theirs):
        print(f"FAIL  {word} is in KEYWORDS (site/customizer-core.js) but is not a cpb keyword", file=sys.stderr)
        bad = 1

    r = Run(a.cpb, [])
    try:
        for word in sorted(theirs):
            code, out = r.raw("CREATE", "PLAYBOOK", word.lower(), "NO", "LAUNCHER")
            # refused for any reason: IF, for one, is read as a clause, not rejected as a name
            if code == 0:
                print(f"FAIL  cpb accepted the keyword {word.lower()} as a playbook name: {out.strip()[:120]}", file=sys.stderr)
                bad = 1
        for word in NOT_KEYWORDS:
            code, out = r.raw("CREATE", "PLAYBOOK", word, "NO", "LAUNCHER")
            if code != 0:
                print(f"FAIL  cpb refused {word} as a playbook name: {out.strip()[:120]}", file=sys.stderr)
                bad = 1
    finally:
        r.close()
    if not bad:
        print(f"ok    keywords: the customizer's {len(ours)} reserved names are cpb's, and cpb refuses each as a playbook name")
    return bad


if __name__ == "__main__":
    sys.exit(main())
