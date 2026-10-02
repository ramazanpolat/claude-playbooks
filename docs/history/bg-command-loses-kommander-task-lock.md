# Claude Code's `/bg` (background session) breaks Kommander's task-lock reattach

**Status:** RESOLVED 2026-09-22 in `kommander-playbook` v3.11.5, with a second,
distinct cause found while fixing it and resolved in v3.12.1. See *Resolution*
at the end; the investigation below is kept as the record of what was observed.

Found live 2026-09-21: the pilot ran `/bg` on an interactive Claude Code session mid-task
(active Kommander task `macminim-oom-recovery`), then reattached. The
reattached session's statusline showed `TASK: none` — the task association
was lost, even though Kommander is specifically designed to survive a quit/
resume via `claude --resume` (session-keyed locks, auto-reattach on
`SessionStart`; see `kommander-playbook`'s `TASK-OPS.md` "Session identity and
resume").

## What was actually observed

Before backgrounding: task `macminim-oom-recovery` was locked with
`session=9a929da8-9c37-45b8-917f-98ab39eb674d`, `pid=7683`.

After `/bg` + reattach: the pilot reported `TASK: none` in the statusline.
Investigating from inside the reattached session:

- `ps -p 7683` — dead. The original process is gone, not merely backgrounded
  in the Unix-job-control sense.
- The reattached session's own process tree includes a process literally named
  `claude bg-spare --bg-spare /tmp/cc-daemon-501/<hash>/spare/<hash>.claim.sock`
  — `/bg` appears to hand off to a spare/daemon process via a claim socket,
  not simply detach-and-reattach the same PID.
- Running `kommander-helper lock reattach` (the same auto-reattach the
  `SessionStart` hook calls) did **not** reclaim the lock — it silently no-op'd.
- Force-reacquiring the lock (`kommander-helper lock acquire --force
  macminim-oom-recovery`) revealed the new effective session id:
  `session=ca2eb16d-8be6-4b0a-8319-57a62699fb9f` — **different** from the
  original `9a929da8-...`.

## Diagnosis

Kommander's reattach is keyed on `CLAUDE_CODE_SESSION_ID` staying stable
across a resume (this is true for plain `claude --resume <id>`, which is what
the whole design assumes — see `kommander-playbook`'s
`hooks/session-start.sh` and `TASK-OPS.md`). `/bg` apparently does **not**
preserve that session id the same way: the reattached session had a genuinely
different one, so Kommander's session-id match correctly failed — the
`.lock.held` marker still pointed at the old, now-dead session, and nothing
matched it. This may be working exactly as designed on both sides
(Kommander's matching is deliberately strict; `/bg` may not be intended as a
resume-equivalent at all) — but the *pilot's* expectation, reasonably, was
that `/bg` + reattach behaves like `claude --resume` for task continuity, and
it doesn't.

## Not pinned down at the time (both since answered — see *Resolution*)

- Whether `/bg`'s session-id change is intentional Claude Code behavior (a
  genuinely new session by design) or an artifact of the bg-spare/claim-socket
  hand-off that could preserve the original id with more care.
- Whether the fix belongs in Claude Code itself (preserve session id across
  `/bg`), or in `kommander-playbook`'s reattach logic (recognize a `/bg`-
  resumed session some other way — e.g. falling back to matching on cwd +
  most-recent stale lock when the pilot explicitly reclaims), or both.

## Where the actual fix likely belongs

**Not `claude-playbooks`** (this repo — the generic multi-playbook launcher).
This is Kommander-specific session/lock behavior, which lives in the separate
[`kommander-playbook`](https://github.com/ramazanpolat/kommander-playbook)
repo (`hooks/session-start.sh`, `TASK-OPS.md`'s "Session identity and resume"
section, the `kommander-helper` binary's `lock reattach` command) — checked
out locally at `~/DEV/kommander-playbook`. This note lives here because that's
where the pilot asked for it; a fix, if any, needs to land there instead.

## Workaround (superseded)

`kommander-helper lock acquire --force <task-name>` after confirming with the
pilot that the original session is really gone (as the normal stale-lock path
already documents) — that's what actually recovered this case. Still the
escape hatch for the ambiguous cases below, but no longer needed for the
common one.

## Resolution

Both open questions above were answered, and both landed in `kommander-playbook`.

**1. The `/bg` session-id change is an artifact, not a design choice** — and
there is nothing to wait for upstream. `/bg` hands off to a pre-warmed spare
daemon over a claim socket, so the reattached session is a genuinely different
OS process and gets a new session id. No official doc or flag promises session-id
stability across `/bg`. Related upstream reports:
[anthropics/claude-code#87399](https://github.com/anthropics/claude-code/issues/87399)
and [#92187](https://github.com/anthropics/claude-code/issues/92187).

**Fixed in v3.11.5** by giving Kommander its own recovery: lock markers now
record the acquiring session's `cwd=`, and when the exact session-id match finds
nothing, `lock reattach` adopts the single *stale* (dead-holder) lock whose
recorded cwd matches. Only ever one unambiguous candidate — two cwd-matching
orphans, a foreign cwd, or a live holder are all left alone — and the
`SessionStart` banner reports the recovery as heuristic rather than presenting
it as an ordinary resume. Cases `BF-01`..`BF-12`, plus a container scenario that
kills a real holder and starts a real session under a new id.

Recording `cwd=` also put arbitrary user data on the marker line for the first
time, which broke every greedy `sed 's/.*pid=...'` reader in the tree — a
directory named `.../pid=99991` was read back as the holder pid, making a *live*
lock look stale. Fixed in the same release; marker fields are now parsed by
cutting the `cwd=` payload off first and taking the first ` name=` match.

**2. A second, unrelated cause of the same symptom: `claude --continue`.**
Found 2026-09-22 while verifying the above — and note it is the *opposite*
shape, so the v3.11.5 fallback cannot help. `--continue` resumes a session that
is **still running**, producing a second live process carrying the **same**
session id. The exact-session reattach had no liveness check (correct for
resume-after-quit, where the holder is always dead), so the marker silently
moved to the newcomer and neither process was told. Both then resolved the task
and both could write `TASK.md`. Observed directly: two `acquired:` lines for one
session id with no `released:` between them, and two agents each convinced they
had shipped the same release.

**Fixed in v3.12.1**: liveness decides, not session identity — reattach only
from a holder that is dead or is the caller, and the refusal is *reported*
(banner names the holding pid and offers `--force`) rather than silent. Cases
`CL-01`..`CL-07`.

If you hit `TASK: none` after backgrounding or resuming, check which shape it is
before forcing anything: a **dead** holder pid is the `/bg` case (v3.11.5
recovers it automatically); a **live** holder pid in another pane is the
`--continue` case, and forcing the lock there takes it from a session that is
still using it.
