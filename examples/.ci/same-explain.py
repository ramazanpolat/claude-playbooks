#!/usr/bin/env python3
"""EXPLAIN --json from the new binary, against the old binary's.

Usage: same-explain.py <old.json> <new.json>

Every top-level key the old binary printed must come back unchanged and in
the same order: that is what a launch sets, and an upgrade must not change
it. A key the new binary adds is new information, not a change, so it is
not compared; its name is printed on stdout, one per line, for the log.
Exit 1, with a diff, when a kept key differs, moved or is gone.
"""
import difflib
import json
import sys

old_path, new_path = sys.argv[1], sys.argv[2]
with open(old_path) as f:
    old = json.load(f)
with open(new_path) as f:
    new = json.load(f)
if not isinstance(old, dict) or not isinstance(new, dict):
    print("EXPLAIN --json is not an object")
    sys.exit(1)
kept = {k: v for k, v in new.items() if k in old}
a = json.dumps(old, indent=1).splitlines()
b = json.dumps(kept, indent=1).splitlines()
if a != b:
    diff = difflib.unified_diff(a, b, "old", "new (keys the old one printed)", lineterm="")
    print("\n".join(list(diff)[:40]))
    sys.exit(1)
for k in new:
    if k not in old:
        print(k)
