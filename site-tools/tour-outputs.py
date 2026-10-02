#!/usr/bin/env python3
"""Checks, or rewrites, the outputs the tour pastes under its commands.

site/tour.html shows commands with the output they really printed.
tour-transcript.sh runs those commands in throwaway homes and prints each one
with its real output. This reads the page's command/output pairs and compares
each with the transcript:

  tour-outputs.py <tour.html> <transcript>           fail if a pasted output differs
  tour-outputs.py <tour.html> <transcript> --write   put the real output on the page

What varies from run to run (a process id, an age like 0s) is masked when
comparing, so a slow runner does not fail the check; --write keeps the real
values. A command the transcript does not contain (a launcher run, a
curl | sh) is not checked.
"""
import difflib
import html
import re
import sys

PAIR = re.compile(
    r'(<span class="cmd"><span class="prompt">\$ </span>(.*?)</span>(?:\s*<span class="comment">.*?</span>)?\n<span class="out">)(.*?)(</span>)',
    re.S,
)


def clean(t):
    return html.unescape(re.sub(r"<[^>]+>", "", t))


def esc(t):
    return t.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")


def mask(text, cmd=""):
    text = re.sub(r"(?m)^(\w+\s+)\d+(\s)", r"\1PID\2", text)        # a SHOW SESSIONS row's pid
    text = re.sub(r"(?<![\w.-])\d+[smhd](?![\w.-])", "N", text)      # ages: 0s, 12m
    if cmd == "cpb SHOW SESSIONS":
        # that table sizes its PID column to the pid, so its spacing and the
        # dashes under the header vary with the run, not with cpb
        text = re.sub(r" {2,}", " ", text)
        text = re.sub(r"-{2,}", "-", text)
    return text


def read_transcript(path):
    t, cur = {}, None
    for line in open(path, encoding="utf-8").read().splitlines():
        if line.startswith("### "):
            continue
        if line.startswith("$ "):
            cur = line[2:]
            t.setdefault(cur, [])
        elif cur is not None:
            t[cur].append(line)
    return t


def main():
    if len(sys.argv) < 3:
        sys.exit(__doc__)
    page_path, trans_path = sys.argv[1], sys.argv[2]
    write = "--write" in sys.argv[3:]
    page = open(page_path, encoding="utf-8").read()
    trans = read_transcript(trans_path)
    if not trans:
        sys.exit(f"FAIL  {trans_path} holds no commands")

    seen = bad = 0
    changed = []

    def sub(m):
        nonlocal seen, bad
        cmd = clean(m.group(2)).strip()
        if cmd not in trans:
            return m.group(0)
        seen += 1
        real = "\n".join(trans[cmd]).rstrip("\n")
        have = clean(m.group(3)).rstrip("\n")
        if mask(real, cmd) == mask(have, cmd):
            return m.group(0)
        bad += 1
        changed.append(cmd)
        if not write:
            print(f"FAIL  the tour's output for `{cmd[:90]}` is not what cpb prints:", file=sys.stderr)
            for l in difflib.unified_diff(have.splitlines(), real.splitlines(), "page", "real", lineterm="", n=0):
                if not l.startswith(("---", "+++", "@@")):
                    print("      " + l[:170], file=sys.stderr)
        return m.group(1) + esc(real) + m.group(4)

    out = PAIR.sub(sub, page)
    if seen == 0:
        sys.exit("FAIL  no command on the page matched the transcript; the page or the transcript changed shape")
    if write:
        open(page_path, "w", encoding="utf-8").write(out)
        print(f"ok    tour: {seen} outputs checked, {bad} rewritten from the real run")
        return 0
    if bad:
        print("      regenerate with: site-tools/tour-outputs.py site/tour.html <transcript> --write", file=sys.stderr)
        return 1
    print(f"ok    tour: all {seen} pasted outputs are what cpb prints")
    return 0


if __name__ == "__main__":
    sys.exit(main())
