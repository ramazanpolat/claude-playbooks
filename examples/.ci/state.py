#!/usr/bin/env python3
"""The state an upgrade must keep, as one JSON document, for examples/upgrade.sh.

Usage: state.py <HOME>

What the registry holds, read from the files cpb and Claude Code write: per
playbook, its manifest, its settings.json, the MCP servers of its
.claude.json, whether its login links the machine's, its skills, its
CLAUDE.md and the stand-in claude's plugin state; then the launchers, the
env sets, DEFAULTS and the secret helper. Paths under HOME read as $HOME,
so the state of two homes compares.

Left out, each for a reason:
- the [apply] and [play] records and .apply/: they say how a playbook came
  to be, and SHOW CREATE does not carry them by design (it names every
  playbook, so applying it records neither);
- cpb's .state/ (the status line history): state, not configuration, which
  SHOW CREATE never writes;
- the stand-in claude's plugin state (.fake-claude/): Claude Code records
  marketplaces and plugins in settings.json, which is compared and which
  SHOW CREATE reads, but the stand-in keeps them in files of its own, which
  SHOW CREATE cannot see;
- what a launch leaves behind (the stand-in's launch-env).
An empty settings.json reads as none: a key added and removed again leaves
"{}" where there was no file.
"""
import hashlib
import json
import os
import sys
import tomllib

home = sys.argv[1]
reg = os.path.join(home, ".claude-playbooks")


def read_json(path):
    try:
        with open(path) as f:
            return json.load(f)
    except FileNotFoundError:
        return None


def read_toml(path):
    try:
        with open(path, "rb") as f:
            return tomllib.load(f)
    except FileNotFoundError:
        return None


def link_or_kind(path):
    if os.path.islink(path):
        return "link:" + os.readlink(path)
    if os.path.isdir(path):
        return "dir"
    if os.path.exists(path):
        return "file"
    return None


def playbook(name):
    d = os.path.join(reg, name)
    out = {"linked": os.readlink(d) if os.path.islink(d) else None}
    m = read_toml(os.path.join(d, ".playbook"))
    if m is not None:
        for k in ("apply", "play"):
            m.pop(k, None)
        # A manifest that holds only the playbook's own name is no manifest:
        # the directory names the playbook.
        if m.get("name") == name:
            del m["name"]
    out["manifest"] = m or None
    s = read_json(os.path.join(d, "settings.json"))
    out["settings"] = s or None
    cj = read_json(os.path.join(d, ".claude.json")) or {}
    out["mcp_servers"] = cj.get("mcpServers")
    out["credentials"] = link_or_kind(os.path.join(d, ".credentials.json"))
    sk = os.path.join(d, "skills")
    out["skills"] = {n: link_or_kind(os.path.join(sk, n)) for n in sorted(os.listdir(sk))} if os.path.isdir(sk) else None
    try:
        with open(os.path.join(d, "CLAUDE.md"), "rb") as f:
            out["claude_md_sha256"] = hashlib.sha256(f.read()).hexdigest()
    except FileNotFoundError:
        out["claude_md_sha256"] = None
    return out


state = {"playbooks": {}, "launchers": {}, "env_sets": {}, "defaults": None, "secret_helper": None}
if os.path.isdir(reg):
    for n in sorted(os.listdir(reg)):
        if not n.startswith("."):
            state["playbooks"][n] = playbook(n)
    es = os.path.join(reg, ".env-sets")
    if os.path.isdir(es):
        for n in sorted(os.listdir(es)):
            if n.endswith(".toml"):
                state["env_sets"][n[:-5]] = read_toml(os.path.join(es, n))
        p = os.path.join(es, ".defaults")
        if os.path.exists(p):
            with open(p) as f:
                state["defaults"] = f.read().split()
        p = os.path.join(es, ".secret-helper")
        if os.path.exists(p):
            with open(p) as f:
                state["secret_helper"] = f.read().strip()
# Launchers sit beside the cpb that wrote them ($HOME/bin in this job). Each
# is a link to the binary that wrote it, which differs between the two
# releases, so only its name counts.
bindir = os.path.join(home, "bin")
if os.path.isdir(bindir):
    for n in sorted(os.listdir(bindir)):
        if n != "cpb" and os.path.islink(os.path.join(bindir, n)):
            state["launchers"][n] = "link"

text = json.dumps(state, indent=1, sort_keys=True)
for h in sorted({home, os.path.realpath(home)}, key=len, reverse=True):
    text = text.replace(h, "$HOME")
print(text)
