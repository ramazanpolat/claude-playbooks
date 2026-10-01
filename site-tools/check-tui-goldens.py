#!/usr/bin/env python3
"""Checks that the `cpb tui` blocks in the tour page still match the real TUI
test goldens (internal/tui/testdata/*.golden), so the site can't quietly
drift from what `cpb tui` actually renders.

Usage: check-tui-goldens.py <tour.html> <testdata dir>
"""
import html
import re
import sys

ENTITIES = {
    "&mdash;": "—",
    "&times;": "×",
    "&rarr;": "→",
    "&hellip;": "…",
}


def unescape(text):
    for entity, char in ENTITIES.items():
        text = text.replace(entity, char)
    return html.unescape(text)


def normalize(text):
    # The golden files are a fixed 80x24 (or 120x40) frame, padded with blank
    # rows between the table and the footer so the layout is pixel-stable in
    # a real terminal. The page drops that padding to stay compact, so blank
    # lines are not meaningful here: every *non-blank* row, in order, with
    # trailing spaces stripped, is what must match exactly.
    return [line.rstrip() for line in text.splitlines() if line.strip() != ""]


def main():
    index_path, testdata_dir = sys.argv[1], sys.argv[2]
    page = open(index_path, encoding="utf-8").read()

    pattern = re.compile(
        r'data-verify="internal/tui/testdata/([a-z0-9-]+\.golden)"[\s\S]*?<pre><code>([\s\S]*?)</code></pre>'
    )
    matches = pattern.findall(page)
    if not matches:
        print("FAIL  no tui-block data-verify=\"internal/tui/testdata/...\" blocks found in the page", file=sys.stderr)
        return 1

    fail = False
    for golden_name, raw_block in matches:
        golden_path = f"{testdata_dir}/{golden_name}"
        try:
            golden_text = open(golden_path, encoding="utf-8").read()
        except FileNotFoundError:
            print(f"FAIL  {golden_name}: no such golden file at {golden_path}", file=sys.stderr)
            fail = True
            continue

        page_lines = normalize(unescape(raw_block))
        golden_lines = normalize(golden_text)

        if page_lines != golden_lines:
            print(f"FAIL  {golden_name}: page content does not match the golden", file=sys.stderr)
            for i, (a, b) in enumerate(zip(page_lines, golden_lines)):
                if a != b:
                    print(f"      line {i}: page={a!r} golden={b!r}", file=sys.stderr)
            if len(page_lines) != len(golden_lines):
                print(f"      {len(page_lines)} lines on the page, {len(golden_lines)} in the golden", file=sys.stderr)
            fail = True
        else:
            print(f"ok    {golden_name}")

    return 1 if fail else 0


if __name__ == "__main__":
    sys.exit(main())
