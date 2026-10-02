"""Shared by the site's checks: a throwaway HOME with a cpb, the `claude` and
secret-helper stand-ins (`claude` from examples/.ci, the helper from this
directory), and an offline stand-in for the
GitHub sources recipes use (skills from `github:owner/repo`, marketplaces).

A recipe's `github:` skills are served from a local git mirror through git's
`insteadOf`, so cpb copies them exactly as it would and nothing needs a
network; a marketplace is a name the stand-in `claude` maps from its source.
"""
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
HELPER = HERE / "stand-in-secret-helper"


class Run:
    def __init__(self, cpb, texts):
        """texts: the recipes this run will apply (scanned for what to stand in for)."""
        self.home = Path(tempfile.mkdtemp(prefix="site-"))
        self.homes = {str(self.home), str(self.home.resolve())}
        (self.home / "bin").mkdir()
        os.symlink(Path(cpb).resolve(), self.home / "bin" / "cpb")
        for stand_in in (CI / "claude", HELPER):
            os.chmod(stand_in, 0o755)
        text = "\n".join(texts)
        for name in sorted(set(re.findall(r"FROM '~/skills/([^']+)'", text))):
            d = self.home / "skills" / name
            d.mkdir(parents=True)
            (d / "SKILL.md").write_text(f"# {name}\n", encoding="utf-8")
        # github: skill sources are served from a local mirror (git's insteadOf),
        # so the run needs no network; cpb copies the skill exactly as it would.
        repos = {}
        for _name, repo, sub in re.findall(r"ADD SKILL (\S+) FROM 'github:([^']+)' SUBDIR '([^']+)'", text):
            repos.setdefault(repo, set()).add(sub)
        git_env = {"GIT_TERMINAL_PROMPT": "0", "GIT_CONFIG_COUNT": str(len(repos))}
        for n, (repo, subs) in enumerate(sorted(repos.items())):
            m = self.home / "mirror" / repo.replace("/", "-")
            for sub in sorted(subs):
                (m / sub).mkdir(parents=True)
                (m / sub / "SKILL.md").write_text(f"# {sub.split('/')[-1]}\n", encoding="utf-8")
            for cmd in (["init", "-q", "-b", "main"], ["add", "."],
                        ["-c", "user.email=site@example.invalid", "-c", "user.name=site", "commit", "-qm", "mirror"]):
                subprocess.run(["git", *cmd], cwd=m, check=True, capture_output=True)
            git_env[f"GIT_CONFIG_KEY_{n}"] = f"url.file://{m}.insteadOf"
            git_env[f"GIT_CONFIG_VALUE_{n}"] = f"https://github.com/{repo}"
        markets = " ".join(
            f"{repo}={name}"
            for name, repo in re.findall(r"ADD MARKETPLACE (\S+) FROM 'github:([^']+)'", text)
        )
        self.env = dict(
            os.environ,
            HOME=str(self.home),
            PATH=f"{CI}:{self.home / 'bin'}:{os.environ['PATH']}",
            CPB_SECRET_HELPER=str(HELPER),
            FAKE_MARKETS=markets,
            **git_env,
        )

    def norm(self, s):
        for h in sorted(self.homes, key=len, reverse=True):
            s = s.replace(h, "~")
        return s

    def cpb(self, *args, merge=False):
        p = subprocess.run(
            ["cpb", *args], cwd=self.home, env=self.env, encoding="utf-8",
            stdout=subprocess.PIPE, stderr=subprocess.STDOUT if merge else subprocess.PIPE,
        )
        if p.returncode != 0:
            sys.exit(f"cpb {' '.join(args)} failed ({p.returncode}):\n{p.stdout}\n{p.stderr or ''}")
        return p.stdout

    def raw(self, *args, env=None):
        p = subprocess.run(["cpb", *args], cwd=self.home, env=env or self.env, encoding="utf-8",
                           stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        return p.returncode, p.stdout

    def split(self, *args):
        """(exit code, stdout, stderr): for --json, whose stdout is one object."""
        p = subprocess.run(["cpb", *args], cwd=self.home, env=self.env, encoding="utf-8",
                           stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        return p.returncode, p.stdout, p.stderr

    def json(self, *args):
        return json.loads(self.cpb(*args))

    def close(self):
        shutil.rmtree(self.home, ignore_errors=True)
