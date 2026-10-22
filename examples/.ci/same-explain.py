#!/usr/bin/env python3
"""EXPLAIN --json from the new binary, against the old binary's.

Usage: same-explain.py <old.json> <new.json>

Every key the old binary printed, at any depth, must come back unchanged
and in the same order: that is what a launch sets, and an upgrade must not
change it. A key the new binary adds, at the top or inside an object the
old one printed (route.guard, say), is new information, not a change, so it
is not compared; its path is printed on stdout, one per line, for the log.
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
added = []


def keep(o, n, path):
    """n restricted to the keys o has, recursively through objects."""
    if not isinstance(o, dict) or not isinstance(n, dict):
        return n
    for k in n:
        if k not in o:
            added.append(path + k)
    return {k: keep(o[k], v, path + k + ".") for k, v in n.items() if k in o}


kept = keep(old, new, "")
a = json.dumps(old, indent=1).splitlines()
b = json.dumps(kept, indent=1).splitlines()
if a != b:
    diff = difflib.unified_diff(a, b, "old", "new (keys the old one printed)", lineterm="")
    print("\n".join(list(diff)[:40]))
    sys.exit(1)
for k in added:
    print(k)
