#!/usr/bin/env python3
"""Writes the brand logos into the shared sprite (site-tools/sprite.html).

The logos on the cards are the tools' own marks, drawn from Simple Icons
(https://simpleicons.org, CC0 1.0), pinned to one release and inlined as SVG
<symbol>s so the page needs no image files and no request. Run it when ICONS
changes (it needs the network once); site-tools/check-sprites.py, which CI
runs, fails if the page uses a symbol the sprite lacks.

  brand-icons.py [--sprite site-tools/sprite.html]   then: sync-sprite.py --write

The marks are trademarks of their owners and appear only to identify the tools
a playbook works with; the page says so and does not claim any endorsement.
"""
import argparse
import re
import sys
import urllib.request
from pathlib import Path

VERSION = "16.33.0"
# slug -> label
ICONS = {
    "github": "GitHub",
    "linear": "Linear",
    "notion": "Notion",
    "sentry": "Sentry",
    "cloudflare": "Cloudflare",
    "claude": "Claude",
    "anthropic": "Anthropic",
    "docker": "Docker",
    "deepseek": "DeepSeek",
}
REPO = Path(__file__).resolve().parent.parent
BLOCK = re.compile(r"(<!-- brand-sprite:start -->)(.*?)(<!-- brand-sprite:end -->)", re.S)


def fetch(slug):
    url = f"https://cdn.jsdelivr.net/npm/simple-icons@{VERSION}/icons/{slug}.svg"
    with urllib.request.urlopen(url, timeout=30) as r:
        svg = r.read().decode()
    m = re.search(r'<path d="([^"]+)"', svg)
    if not m:
        sys.exit(f"{slug}: no path in {url}")
    return m.group(1)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--sprite", default=str(REPO / "site-tools" / "sprite.html"))
    a = ap.parse_args()
    page = Path(a.sprite).read_text(encoding="utf-8")
    m = BLOCK.search(page)
    if not m:
        sys.exit("no <!-- brand-sprite:start --> ... <!-- brand-sprite:end --> block in the sprite")
    syms = [f'<symbol id="b-{slug}" viewBox="0 0 24 24"><title>{label}</title><path d="{fetch(slug)}"/></symbol>'
            for slug, label in ICONS.items()]
    body = f"\n<!-- Simple Icons {VERSION}, CC0 1.0; marks belong to their owners -->\n" + "\n".join(syms) + "\n"
    Path(a.page).write_text(page[: m.start(2, encoding="utf-8")] + body + page[m.end(2):])
    print(f"wrote {len(syms)} brand symbols into {a.sprite}")


if __name__ == "__main__":
    main()
