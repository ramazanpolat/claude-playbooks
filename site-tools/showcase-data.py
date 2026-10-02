#!/usr/bin/env python3
"""Builds the home page's card data from real cpb output, or checks it.

Applies showcase.cpb in a throwaway HOME (examples/.ci supplies stand-ins for
`claude` and the secret helper, so nothing needs a network or a login), then
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

HERE = Path(__file__).resolve().parent
REPO = HERE.parent
CI = REPO / "examples" / ".ci"
RECIPE = HERE / "showcase.cpb"
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
    return out


class Run:
    def __init__(self, cpb):
        self.home = Path(tempfile.mkdtemp(prefix="showcase-"))
        self.homes = {str(self.home), str(self.home.resolve())}
        (self.home / "bin").mkdir()
        os.symlink(Path(cpb).resolve(), self.home / "bin" / "cpb")
        for stand_in in ("claude", "with-secret"):
            os.chmod(CI / stand_in, 0o755)
        text = RECIPE.read_text()
        for name in sorted(set(re.findall(r"FROM '~/skills/([^']+)'", text))):
            d = self.home / "skills" / name
            d.mkdir(parents=True)
            (d / "SKILL.md").write_text(f"# {name}\n")
        markets = " ".join(
            f"{repo}={name}"
            for name, repo in re.findall(r"ADD MARKETPLACE (\S+) FROM 'github:([^']+)'", text)
        )
        self.env = dict(
            os.environ,
            HOME=str(self.home),
            PATH=f"{CI}:{self.home / 'bin'}:{os.environ['PATH']}",
            CPB_SECRET_HELPER=str(CI / "with-secret"),
            FAKE_MARKETS=markets,
        )
        shutil.copy(RECIPE, self.home / "showcase.cpb")

    def norm(self, s):
        for h in sorted(self.homes, key=len, reverse=True):
            s = s.replace(h, "~")
        return s

    def cpb(self, *args, merge=False):
        p = subprocess.run(
            ["cpb", *args], cwd=self.home, env=self.env, text=True,
            stdout=subprocess.PIPE, stderr=subprocess.STDOUT if merge else subprocess.PIPE,
        )
        if p.returncode != 0:
            sys.exit(f"cpb {' '.join(args)} failed ({p.returncode}):\n{p.stdout}\n{p.stderr or ''}")
        return p.stdout

    def json(self, *args):
        return json.loads(self.cpb(*args))

    def close(self):
        shutil.rmtree(self.home, ignore_errors=True)


def build(cpb):
    secs = sections(RECIPE.read_text())
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
            name = s["name"]
            show = r.json("SHOW", "PLAYBOOK", name, "--json")
            explain = r.json("EXPLAIN", "PLAYBOOK", name, "--json")
            d = Path(show["path"])
            login = "sandbox" if show["sandbox"] else "isolated" if show["isolated_login"] else "shared"

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
                    for k in sorted(os.scandir(p), key=lambda e: e.name):
                        kid = {"name": k.name}
                        if k.is_symlink():
                            kid["link"] = r.norm(os.readlink(k.path))
                        kids.append(kid)
                    files.append({"name": n, "kind": "dir", "children": kids})
                else:
                    files.append({"name": n, "kind": "file"})
                    text = r.norm(p.read_text())
                    if n == "CLAUDE.md":
                        text = "\n".join(text.splitlines()[:CLAUDE_MD_LINES]) + "\n"
                    texts[n] = text

            pbs.append({
                "name": name,
                "tagline": s["tagline"],
                "path": r.norm(show["path"]),
                "launcher": name if (r.home / "bin" / name).exists() else None,
                "login": login,
                "pilot_profile": show["pilot_profile"],
                "model": (explain["model"] or {}).get("name"),
                "model_picker": show["model_picker"],
                "skills": [{"name": k["name"], "source": r.norm(k["source"])} for k in show["skills"]],
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
    page = Path(a.page).read_text()
    m = BLOCK.search(page)
    if not m:
        sys.exit(f'{a.page}: no <script type="application/json" id="showcase-data"> block')

    if a.write:
        new = page[: m.start(2)] + "\n" + dump(data) + "\n" + page[m.end(2):]
        Path(a.page).write_text(new)
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
