#!/usr/bin/env python3
"""Builds the home page's card data from real cpb output, or checks it.

Applies showcase.cpb in a throwaway HOME (a `claude` stand-in from examples/.ci and
`stand-in-secret-helper` from this directory, so nothing needs a network or a login), then
reads what cpb itself reports: SHOW PLAYBOOK --json, EXPLAIN PLAYBOOK --json,
the APPLY --dry-run --json plan (plugins, which Claude Code would install),
and the files cpb wrote into each playbook's CLAUDE_CONFIG_DIR. The result is
the JSON block in site/index.html (<script id="showcase-data">).

  showcase-data.py --cpb <cpb> --write   rewrite the block in index.html
  showcase-data.py --cpb <cpb> --check   fail if the block differs from cpb

Every card on the page is drawn from that block, so the page can only say
what cpb said.
"""
import argparse
import difflib
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from sitelib import Run as _Run  # noqa: E402

HERE = Path(__file__).resolve().parent
REPO = HERE.parent
CI = REPO / "examples" / ".ci"
RECIPE = HERE / "showcase.cpb"
RUNTIMES = HERE / "runtimes.json"
PAGE = REPO / "site" / "index.html"
BLOCK = re.compile(r'(<script type="application/json" id="showcase-data">)(.*?)(</script>)', re.S)
CLAUDE_MD_LINES = 12


def sections(text):
    """The recipe's sections: [{name, tagline, text}], in file order."""
    lines = text.splitlines()
    declared = set(re.findall(r"^CREATE PLAYBOOK IF NOT EXISTS ([a-z][a-z0-9-]*)", text, re.M))
    marks = []
    for i, line in enumerate(lines):
        m = re.match(r"^-- ([a-z][a-z0-9-]*): (.+)$", line)
        if m and m.group(1) in declared:
            marks.append((i, m.group(1), m.group(2)))
    out = []
    for n, (i, name, tagline) in enumerate(marks):
        end = marks[n + 1][0] if n + 1 < len(marks) else len(lines)
        body = "\n".join(lines[i:end]).rstrip() + "\n"
        out.append({"name": name, "tagline": tagline, "text": body})
    for i, line in enumerate(lines):
        m = re.match(r"^-- ephemeral ([a-z][a-z0-9-]*): (.+)$", line)
        if m:
            j = i + 1
            while j < len(lines) and lines[j].startswith("--"):
                j += 1
            out.append({"name": m.group(1), "tagline": m.group(2), "text": "\n".join(lines[i:j]) + "\n", "ephemeral": True})
    return out


class Run(_Run):
    def __init__(self, cpb):
        super().__init__(cpb, [RECIPE.read_text(encoding="utf-8")])
        shutil.copy(RECIPE, self.home / "showcase.cpb")


def ephemeral(r, sec):
    """cpb start <dir> --delete, for real, with a claude that records its session."""
    stand = r.home / "stand"
    stand.mkdir()
    (stand / "claude").write_text(
        '#!/bin/sh\n{ echo "config=$CLAUDE_CONFIG_DIR"; test -d "$CLAUDE_CONFIG_DIR" && echo existed=yes;'
        ' echo "entries=$(ls -A "$CLAUDE_CONFIG_DIR" | wc -l)"; echo "args=$*"; } > "$HOME/session.log"\n',
        encoding="utf-8")
    os.chmod(stand / "claude", 0o755)
    target = r.home / "spike"
    before = target.exists()
    env = dict(r.env, PATH=f"{stand}:{r.env['PATH']}")
    code, out = r.raw("start", str(target), "--delete", env=env)
    if code != 0:
        sys.exit(f"cpb start --delete failed ({code}):\n{out}")
    log = dict(l.split("=", 1) for l in (r.home / "session.log").read_text(encoding="utf-8").splitlines() if "=" in l)
    listed = [p["path"] for p in r.json("SHOW", "PLAYBOOKS", "--json")]
    help_start = r.raw("start", "--help")[1]
    for flag in ("--delete", "--sandbox"):
        if flag not in help_start:
            sys.exit(f"cpb start --help no longer mentions {flag}")
    facts = {
        "config_dir_is_the_path": log.get("config") == str(target),
        "existed_during": log.get("existed") == "yes",
        "entries_at_start": int(log.get("entries", "-1")),
        "exists_after": target.exists(),
        "created_by_start": not before,
        "registered": any(str(target) in x for x in listed),
    }
    if not (facts["config_dir_is_the_path"] and facts["existed_during"] and facts["created_by_start"]
            and not facts["exists_after"] and not facts["registered"]):
        sys.exit(f"cpb start --delete did not behave as the page says: {facts}")
    return {
        "name": sec["name"],
        "ephemeral": True,
        "tagline": sec["tagline"],
        "path": "/tmp/spike",
        "command": "cpb start /tmp/spike --delete",
        "variant": "cpb start --sandbox --delete /tmp/spike",
        "lifecycle": facts,
        "recipe": sec["text"],
    }


def runtimes(r, pbs):
    """Where each playbook runs, checked against what this cpb accepts."""
    plan = json.loads(RUNTIMES.read_text(encoding="utf-8"))
    code, out = r.raw("run", "--sandbox=nope", "work")
    m = re.search(r"unknown sandbox backend \"nope\" \(available: ([^)]*)\)", out)
    if code == 0 or not m:
        sys.exit(f"cpb run --sandbox=nope did not name the backends: {out}")
    backends = [b.strip() for b in m.group(1).split(",")]
    helps = {"run": r.raw("run", "--help")[1], "start": r.raw("start", "--help")[1]}
    by_name = {p["name"]: p for p in pbs}
    zones = {z["id"]: z for z in plan["zones"]}
    for z in plan["zones"]:
        if z.get("backend") and z["backend"] not in backends:
            sys.exit(f"zone {z['id']} names backend {z['backend']}, cpb offers {backends}")
    for l in plan["launches"]:
        words = l["cmd"].split()
        pb = by_name.get(l["playbook"])
        if pb is None or l["zone"] not in zones:
            sys.exit(f"launch {l}: unknown playbook or zone")
        if words[0] == "cpb":
            sub = words[1]
            for flag in (w.split("=")[0] for w in words if w.startswith("--")):
                if flag not in helps[sub]:
                    sys.exit(f"launch '{l['cmd']}': cpb {sub} --help has no {flag}")
            for w in words:
                if w.startswith("--sandbox="):
                    b = w.split("=", 1)[1]
                    if b not in backends or zones[l["zone"]].get("backend") != b:
                        sys.exit(f"launch '{l['cmd']}': backend {b} does not match zone {l['zone']}")
        elif not (r.home / "bin" / words[0]).exists():
            sys.exit(f"launch '{l['cmd']}': no launcher {words[0]}")
        if pb.get("login") == "sandbox" and zones[l["zone"]]["kind"] == "host":
            sys.exit(f"{pb['name']} is always sandboxed but is placed on the host")
    return {"zones": plan["zones"], "launches": plan["launches"], "backends": backends}


def build(cpb):
    secs = sections(RECIPE.read_text(encoding="utf-8"))
    r = Run(cpb)
    try:
        plan = r.json("APPLY", "showcase.cpb", "--dry-run", "--json")
        if not plan["ok"]:
            sys.exit(f"showcase.cpb does not plan: {plan['error']}")
        dry = r.cpb("APPLY", "showcase.cpb", "--dry-run", merge=True)
        plugins, markets = {}, {}
        for st in plan["statements"]:
            tgt = st["target"].get("name")
            for a in st["actions"]:
                argv = a.get("argv") or []
                if argv[:3] == ["claude", "plugin", "install"]:
                    plugins.setdefault(tgt, []).append(argv[3])
                if argv[:4] == ["claude", "plugin", "marketplace", "add"]:
                    markets.setdefault(tgt, []).append(argv[4])

        applied = r.cpb("APPLY", "showcase.cpb", merge=True).strip().splitlines()[-1]
        again = r.cpb("APPLY", "showcase.cpb", merge=True).strip().splitlines()[-1]
        if " 0 created, 0 changed, " not in again:
            sys.exit(f"applying twice changed something: {again}")

        pbs = []
        for s in secs:
            if s.get("ephemeral"):
                pbs.append(ephemeral(r, s))
                continue
            name = s["name"]
            show = r.json("SHOW", "PLAYBOOK", name, "--json")
            explain = r.json("EXPLAIN", "PLAYBOOK", name, "--json")
            d = Path(show["path"])
            login = "sandbox" if show["sandbox"]["always"] else "isolated" if show["isolated_login"] else "shared"

            env = []
            for v in explain["vars"]:
                if v["key"].startswith("CPB_MCP_"):
                    continue
                item = {"key": v["key"]}
                for k in ("value", "ref", "blocked"):
                    if k in v:
                        item[k] = v[k]
                lay = v["layer"]
                item["from"] = "PLAYBOOK" if lay["kind"] == "PLAYBOOK" else f"ENV {lay['name']}"
                env.append(item)

            mcp = []
            for m in show["mcp_servers"]:
                item = {"name": m["name"], "transport": m["transport"]}
                if m.get("url"):
                    item["url"] = m["url"]
                if m.get("command"):
                    item["command"] = m["command"]
                    item["args"] = m.get("args", [])
                mcp.append(item)

            files, texts = [], {}
            order = ["CLAUDE.md", "settings.json", ".claude.json", ".playbook", "skills"]
            names = sorted(e.name for e in os.scandir(d) if e.name != ".fake-claude")
            names = [n for n in order if n in names] + [n for n in names if n not in order]
            for n in names:
                p = d / n
                if p.is_dir():
                    kids = []
                    by_name = {k["name"]: k for k in show["skills"]}
                    for k in sorted(os.scandir(p), key=lambda e: e.name):
                        kid = {"name": k.name}
                        if k.is_symlink():
                            kid["link"] = r.norm(os.readlink(k.path))
                        elif k.name in by_name:
                            kid["source"] = r.norm(by_name[k.name]["source"])
                            kid["subdir"] = by_name[k.name]["subdir"]
                        kids.append(kid)
                    files.append({"name": n, "kind": "dir", "children": kids})
                else:
                    files.append({"name": n, "kind": "file"})
                    text = r.norm(p.read_text(encoding="utf-8"))
                    if n == "CLAUDE.md":
                        text = "\n".join(text.splitlines()[:CLAUDE_MD_LINES]) + "\n"
                    texts[n] = text

            pbs.append({
                "name": name,
                "tagline": s["tagline"],
                "path": r.norm(show["path"]),
                "launcher": name if (r.home / "bin" / name).exists() else None,
                "login": login,
                "model": (explain["model"] or {}).get("name"),
                "model_picker": show["model_picker"],
                "skills": [
                    {"name": k["name"], "source": r.norm(k["source"]), "subdir": k["subdir"], "mode": k["mode"]}
                    for k in show["skills"]
                ],
                "marketplaces": markets.get(name, []),
                "plugins": plugins.get(name, []),
                "mcp": mcp,
                "tools": show["tools"],
                "env": env,
                "files": files,
                "file_text": texts,
                "recipe": s["text"],
            })

        return {
            "recipe": "site-tools/showcase.cpb",
            "apply": {
                "dry_run": [r.norm(l) for l in dry.strip().splitlines()],
                "applied": applied,
                "again": again,
            },
            "playbooks": pbs,
            "runtimes": runtimes(r, pbs),
        }
    finally:
        r.close()


def dump(data):
    return json.dumps(data, indent=2, ensure_ascii=False).replace("</", "<\\/")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--cpb", required=True)
    ap.add_argument("--page", default=str(PAGE))
    mode = ap.add_mutually_exclusive_group(required=True)
    mode.add_argument("--write", action="store_true")
    mode.add_argument("--check", action="store_true")
    a = ap.parse_args()

    data = build(a.cpb)
    page = Path(a.page).read_text(encoding="utf-8")
    m = BLOCK.search(page)
    if not m:
        sys.exit(f'{a.page}: no <script type="application/json" id="showcase-data"> block')

    if a.write:
        new = page[: m.start(2)] + "\n" + dump(data) + "\n" + page[m.end(2):]
        Path(a.page).write_text(new, encoding="utf-8")
        print(f"wrote {len(data['playbooks'])} playbooks into {a.page}")
        return

    have = json.loads(m.group(2).replace("<\\/", "</"))
    if have == data:
        print(f"ok    showcase data: {len(data['playbooks'])} cards match what cpb reports")
        return
    diff = difflib.unified_diff(
        dump(have).splitlines(), dump(data).splitlines(), "page", "cpb", lineterm="", n=2)
    print("FAIL  showcase data in the page differs from what cpb reports:", file=sys.stderr)
    print("\n".join(list(diff)[:80]), file=sys.stderr)
    print("      regenerate with: site-tools/showcase-data.py --cpb <cpb> --write", file=sys.stderr)
    sys.exit(1)


if __name__ == "__main__":
    main()
