#!/usr/bin/env python3
"""The grammar the previous release spoke, for examples/upgrade.sh only.

v4.0.0-rc3 replaced ISOLATED LOGIN with playbook settings (SETTINGS,
MODIFY SETTING) and added the memory setting, which SHOW CREATE writes for
every playbook. The upgrade check compares the old binary's SHOW CREATE ALL
with the new one's and re-applies the old release's examples with the new
binary, so across that change it needs this map, and nothing else does.
Delete it, and its two calls in upgrade-one.sh, once the previous release
is rc3 or later.

  prev-syntax.py translate <src dir> <dst dir>
      Copy an example directory, rewriting its .cpb files from the old
      grammar to the new one.
  prev-syntax.py render <file>
      Print a new SHOW CREATE as the previous release rendered the same
      state: MODIFY SETTING login = 'isolated' is SET ISOLATED LOGIN, and
      memory = 'shared' (state the previous release wrote, which never
      excluded ~/.claude) is dropped. Anything else is left as it is, so a
      real difference still fails the comparison.
"""
import pathlib
import re
import shutil
import sys


def translate(src, dst):
    shutil.copytree(src, dst)
    for f in pathlib.Path(dst).rglob("*.cpb"):
        t = f.read_text()
        t = re.sub(r"\bUNSET ISOLATED LOGIN\b", "MODIFY SETTING login = 'shared'", t)
        t = re.sub(r"\bSET ISOLATED LOGIN\b", "MODIFY SETTING login = 'isolated'", t)
        t = re.sub(r"\bISOLATED LOGIN\b", "SETTINGS login = 'isolated'", t)
        f.write_text(t)


SETTING = re.compile(r"^  MODIFY SETTING (.*?)(;?)$")
ITEM = re.compile(r"^\s*([a-z]+) = '([a-z]+)'\s*$")


def render(path):
    out = []
    for line in open(path).read().split("\n"):
        m = SETTING.match(line)
        if not m:
            out.append(line)
            continue
        body, semi = m.group(1), m.group(2)
        keep, rest = [], []
        for item in body.split(","):
            im = ITEM.match(item)
            if im and im.groups() == ("login", "isolated"):
                keep.append("SET ISOLATED LOGIN")
            elif im and im.groups() == ("memory", "shared"):
                continue
            else:
                rest.append(item.strip())
        if rest:  # not the previous release's state: left for the diff
            keep.append("MODIFY SETTING " + ", ".join(rest))
        if keep:
            out.append("  " + "\n  ".join(keep) + semi)
            continue
        if semi:
            if out and out[-1].startswith("ALTER PLAYBOOK"):
                out.pop()  # the ALTER held this clause only
                while out and out[-1] == "":
                    out.pop()
            elif out:
                out[-1] += ";"
    sys.stdout.write("\n".join(out))


if __name__ == "__main__":
    if sys.argv[1] == "translate":
        translate(sys.argv[2], sys.argv[3])
    elif sys.argv[1] == "render":
        render(sys.argv[2])
    else:
        sys.exit("usage: prev-syntax.py translate <src> <dst> | render <file>")
