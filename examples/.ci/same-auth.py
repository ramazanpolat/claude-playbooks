#!/usr/bin/env python3
"""auth status --json from the new binary, against the old binary's.

Usage: same-auth.py <old.json> <new.json>

Every row must come back unchanged, with one exception by design: since
v4.2.0 an account says where a launch's requests go and how they
authenticate, so a playbook whose layers set one of its variables (an older
release's routing through an env set or its own variables) is refused at
launch, and auth status says so (mode "error", naming the variable). Such a
row is not compared; its name is printed on stdout, one per line, for the
log. Exit 1, with a diff, when any other row differs, moved or is gone.
"""
import difflib
import json
import sys

with open(sys.argv[1]) as f:
    old = json.load(f)
with open(sys.argv[2]) as f:
    new = json.load(f)
if not isinstance(old, list) or not isinstance(new, list) or len(old) != len(new):
    print("auth status --json: not two lists of the same length")
    sys.exit(1)
kept_old, kept_new = [], []
for o, n in zip(old, new):
    if (isinstance(n, dict) and n.get("mode") == "error"
            and "since v4.2.0 it is the account's" in n.get("mode_error", "")
            and o.get("name") == n.get("name")):
        print(n.get("name"))
        continue
    kept_old.append(o)
    kept_new.append(n)
a = json.dumps(kept_old, indent=2).splitlines()
b = json.dumps(kept_new, indent=2).splitlines()
if a != b:
    print("\n".join(difflib.unified_diff(a, b, "old", "new", lineterm="", n=2)))
    sys.exit(1)
