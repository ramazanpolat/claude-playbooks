#!/usr/bin/env python3
"""Keeps the inline logo/icon sprite identical in every page.

The sprite (site-tools/sprite.html) holds the tools' marks (brand-icons.py
writes those), the hand-drawn marks for playbooks and runtimes, and the small
UI icons. Each page carries it inline between <!-- sprite:start --> and
<!-- sprite:end -->, so it needs no request and works from any preview.

  sync-sprite.py --write    copy the sprite into the pages
  sync-sprite.py --check    fail if a page's copy differs (CI runs this)
"""
import argparse
import re
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
PAGES = ["index.html", "templates.html"]
BLOCK = re.compile(r"(<!-- sprite:start -->\n)(.*?)(\n<!-- sprite:end -->)", re.S)


def main():
    ap = argparse.ArgumentParser()
    g = ap.add_mutually_exclusive_group(required=True)
    g.add_argument("--write", action="store_true")
    g.add_argument("--check", action="store_true")
    a = ap.parse_args()
    sprite = (REPO / "site-tools" / "sprite.html").read_text(encoding="utf-8").rstrip("\n")
    bad = 0
    for name in PAGES:
        path = REPO / "site" / name
        if not path.exists():
            continue
        page = path.read_text(encoding="utf-8")
        m = BLOCK.search(page)
        if not m:
            print(f"FAIL  {name}: no <!-- sprite:start --> block", file=sys.stderr)
            bad = 1
            continue
        if m.group(2) == sprite:
            continue
        if a.write:
            path.write_text(page[: m.start(2)] + sprite + page[m.end(2):], encoding="utf-8")
            print(f"wrote the sprite into {name}")
        else:
            print(f"FAIL  {name}: its sprite differs from site-tools/sprite.html (run sync-sprite.py --write)", file=sys.stderr)
            bad = 1
    if bad:
        sys.exit(1)
    if a.check:
        print("ok    sprite: every page carries site-tools/sprite.html")


if __name__ == "__main__":
    main()
