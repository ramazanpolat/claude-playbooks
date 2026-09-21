# Claude Code's `/bg` (background session) breaks Kommander's task-lock reattach

**Status:** known, unfixed, root cause only partially pinned down. Found live
2026-09-21: the pilot ran `/bg` on an interactive Claude Code session mid-task
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

## Not pinned down

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

## Workaround

`kommander-helper lock acquire --force <task-name>` after confirming with the
pilot that the original session is really gone (as the normal stale-lock path
already documents) — that's what actually recovered this case.
