#!/usr/bin/env python3
"""Tests tour-outputs.py on small pages: the check has to fail when it should.

The cases are the ones that went wrong or could: a command with no output of its
own must not swallow the output of the command after it; a changed output, or
an output the transcript cannot account for, fails; the width a SHOW SESSIONS
table gets from its pid does not.
"""
import subprocess
import sys
import tempfile
from pathlib import Path

HERE = Path(__file__).resolve().parent
TOOL = HERE / "tour-outputs.py"


def cmd(text, comment=""):
    c = f'<span class="comment"># {comment}</span>' if comment else ""
    return f'<span class="cmd"><span class="prompt">$ </span>{text}</span>{c}\n'


def out(text):
    return f'<span class="out">{text}</span>\n'


def run(page, transcript, *flags):
    with tempfile.TemporaryDirectory() as d:
        (Path(d) / "p.html").write_text("<pre><code>" + page + "</code></pre>", encoding="utf-8")
        (Path(d) / "t.txt").write_text(transcript, encoding="utf-8")
        r = subprocess.run([sys.executable, str(TOOL), str(Path(d) / "p.html"), str(Path(d) / "t.txt"), *flags],
                           capture_output=True, encoding="utf-8")
        return r.returncode, r.stderr + r.stdout, (Path(d) / "p.html").read_text(encoding="utf-8")


T = "$ cpb A\nalpha\n$ cpb B\nbeta\n"
bad = 0


def expect(name, ok, why=""):
    global bad
    print(("ok    " if ok else "FAIL  ") + name + ("" if ok else f": {why}"))
    bad |= not ok


code, msg, _ = run(cmd("cpb A") + out("alpha") + cmd("cpb B") + out("beta"), T)
expect("a matching page passes", code == 0, msg)

code, msg, _ = run(cmd("cpb A") + out("ALPHA") + cmd("cpb B") + out("beta"), T)
expect("a changed output fails", code == 1 and "cpb A" in msg, msg)

# a command with no output of its own, in front of a command that has one
code, msg, _ = run(cmd("cpb show-only", "comment") + cmd("cpb A") + out("ALPHA"), T)
expect("a command without output does not swallow the next one's output", code == 1 and "cpb A" in msg, msg)

code, msg, _ = run(cmd("cpb show-only") + cmd("cpb A") + out("alpha"), T)
expect("... and the next command is still checked when it matches", code == 0, msg)

code, msg, _ = run(cmd("cpb A") + out("alpha") + cmd("cpb unknown") + out("whatever"), T)
expect("an output under a command the transcript lacks fails", code == 1 and "cpb unknown" in msg, msg)

code, msg, _ = run(cmd("cpb A") + out("alpha") + out("an orphan block"), T)
expect("an output block under no command fails", code == 1 and "output blocks" in msg, msg)

sessions = "$ cpb SHOW SESSIONS\nPLAYBOOK  PID   TTY\n--------  ---   ---\nworker    3122  -\n"
page = cmd("cpb SHOW SESSIONS") + out("PLAYBOOK  PID     TTY\n--------  ---     ---\nworker    447747  -")
code, msg, _ = run(page, sessions)
expect("a SHOW SESSIONS table of another pid width passes", code == 0, msg)
code, msg, _ = run(page, sessions.replace("TTY", "TTX"))
expect("... but a changed column still fails", code == 1, msg)

V = "$ cpb --version\ncpb version v4.0.0-rc1\n"
code, msg, _ = run(cmd("cpb --version") + out("cpb version v4.0.0-rc0"), V)
expect("a tagged build's version must match the page exactly", code == 1, msg)
code, msg, _ = run(cmd("cpb --version") + out("cpb version v4.0.0-rc1"), V)
expect("... and passes when it does", code == 0, msg)
code, msg, _ = run(cmd("cpb --version") + out("cpb version v4.0.0-rc1"), "$ cpb --version\ncpb version d35d311\n")
expect("an untagged build (a commit id) only has to print a version line", code == 0, msg)
code, msg, written = run(cmd("cpb --version") + out("cpb version v4.0.0-rc1"), "$ cpb --version\ncpb version d35d311\n", "--write")
expect("--write leaves the page's version alone for an untagged build", code == 0 and "v4.0.0-rc1" in written, msg + written)
code, msg, _ = run(cmd("cpb --version") + out("not a version"), "$ cpb --version\ncpb version d35d311\n")
expect("... but a line that is not a version fails", code == 1, msg)

code, msg, written = run(cmd("cpb A") + out("ALPHA"), T, "--write")
expect("--write puts the real output on the page", code == 0 and ">alpha<" in written, msg + written)

sys.exit(1 if bad else 0)
