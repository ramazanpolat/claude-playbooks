#!/usr/bin/env python3
"""Checks that every logo the home page asks for is in its inline sprite.

The cards and the runtimes map draw their marks with <use href="#id"> (some
ids are built by the scripts from names in the data). This lints the page, its
scripts and the showcase data against the <symbol>s in site/index.html, so a
renamed or dropped icon fails CI instead of showing an empty box.

  check-sprites.py [--site site]
"""
import argparse
import json
import re
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--site", default=str(REPO / "site"))
    site = Path(ap.parse_args().site)
    page = (site / "index.html").read_text(encoding="utf-8")
    js = {f.name: f.read_text(encoding="utf-8") for f in site.glob("*.js") if not f.name.startswith(".")}
    symbols = set(re.findall(r'<symbol id="([^"]+)"', page))
    wanted = {}  # id -> where

    def want(i, where):
        wanted.setdefault(i, where)

    for i in re.findall(r'<use href="#([^"]+)"', page):
        want(i, "index.html")
    for name, text in js.items():
        for i in re.findall(r'<use href="#([a-z0-9-]+)"', text):
            want(i, name)
        for i in re.findall(r'\blg\("([a-z0-9-]+)"', text):
            want(i, name)
        for i in re.findall(r'\bicon\("([a-z0-9-]+)"\)', text):
            want("i-" + i, name)
        for i in re.findall(r'\bsym: "([a-z0-9-]+)"', text):
            want(i, name)
        for i in re.findall(r'"(i-[a-z]+|g-[a-z]+)"', text):
            want(i, name)
        m = re.search(r"BRAND_MCP = \{([^}]*)\}", text)
        if m:
            for b in re.findall(r':\s*"([a-z]+)"', m.group(1)):
                want("b-" + b, name)
        for b in re.findall(r'hasSym\("b-([a-z]+)"\)', text):
            want("b-" + b, name)
        for b in re.findall(r'"(b-[a-z]+)"', text):
            want(b, name)

    data = json.loads(re.search(r'id="showcase-data">(.*?)</script>', page, re.S).group(1).replace("<\\/", "</"))
    for p in data["playbooks"]:
        want("g-ghost" if p.get("ephemeral") else "g-" + p["name"], "the showcase data (a mark per playbook)")

    missing = {i: w for i, w in wanted.items() if i not in symbols}
    if missing:
        for i, w in sorted(missing.items()):
            print(f"FAIL  no <symbol id=\"{i}\"> in index.html (used by {w})", file=sys.stderr)
        sys.exit(1)
    print(f"ok    sprites: {len(wanted)} marks used, all {len(symbols)} symbols resolve")


if __name__ == "__main__":
    main()
