# Handoff: optional `pilot-profile` integration

**From:** the `pilot-profile` project (`github.com/agent-realm/pilot-profile`, private)
**Date:** 2026-09-22
**Status:** proposed — nothing has been changed in this repo
**Size:** two small, independent changes. Neither adds a dependency.

---

## TL;DR

Two optional touchpoints, both no-ops when `pilot-profile` is not installed:

1. **`cmd/create.go:140`** — add four `@import` lines to `defaultClaudeMD`.
2. **`cmd/create.go:132`** and **`cmd/install.go:283`** — after `installLauncher(...)`,
   run `pilot wire <dest>` if `pilot` is on `PATH`; ignore any failure.

Do **not** vendor the profile schema, tooling, or migrations into this repo. §4 says why.

---

## 1. What `pilot-profile` is

A shared, plain-markdown description of the user and their environment, at
`~/.pilot-profile/`: identity, preferences, machines, servers, accounts. Every Claude
Code configuration on the machine imports it, so the user describes their staging box
once instead of once per playbook.

It ships a `pilot` CLI and a `pilot-secret` reference resolver, both on `PATH`. It is
**not** a playbook and does not want to become one — it has no `CLAUDE_CONFIG_DIR` and
no launcher.

It used to live inside `chaos`, a ClickHouse playbook. In 2026-09 a rename internal to
`chaos` broke six unrelated installs, because the thing every playbook depended on was a
subdirectory of one playbook. Extracting it fixed that. **The same reasoning applies to
this repo** — see §4.

## 2. Why `claude-playbook` is involved at all

`claude-playbook` creates and manages the config directories that consume a profile. It
is the only component that knows where every playbook on the machine lives. That makes
it the natural place to *connect* a profile — and the wrong place to *own* one.

The dependency edge must point this way:

```
claude-playbook  ──optionally calls──▶  pilot   (component)
```

and never the reverse. `pilot` must never need `claude-playbook`, because the stock
`~/.claude` is not managed by it and still deserves a profile.

## 3. The two changes

### 3a. Pointer in the `create` template

`cmd/create.go:140`, `defaultClaudeMD`. Append a section:

```go
"## Pilot profile\n\n" +
    "The shared pilot profile at `~/.pilot-profile/`, imported when present and\n" +
    "silently skipped when not. See https://github.com/agent-realm/pilot-profile\n\n" +
    "@~/.pilot-profile/PROFILE.md\n" +
    "@~/.pilot-profile/identity.md\n" +
    "@~/.pilot-profile/preferences.md\n" +
    "@~/.pilot-profile/capture-protocol.md\n\n" +
```

**Why this is free.** Claude Code skips a missing `@import` silently. A user with no
profile sees no error, no warning, and no behaviour change — the lines are inert. A user
with a profile gets it in every new playbook with no wiring step.

**Why it is safe to put in `CLAUDE.md` here specifically.** A created playbook's
`CLAUDE.md` is not tracked by git and is never reset over. (That distinction matters a
lot elsewhere — see §5.)

### 3b. Wire on create and install

`cmd/create.go:132` and `cmd/install.go:283`, immediately after `installLauncher(...)`:

```go
// Optional: connect the new playbook to the pilot profile, if that component
// is installed. Best-effort and entirely silent when absent — pilot-profile is
// never a requirement. https://github.com/agent-realm/pilot-profile
if pilot, err := exec.LookPath("pilot"); err == nil {
    _ = exec.Command(pilot, "wire", dest).Run()   // cmd/install.go: configDest
}
```

**Requirements on this code:**

- **Never fail the install.** Discard the error. A broken or half-installed `pilot` must
  not be able to break `claude-playbook`.
- **Never install anything.** If `pilot` is absent, do nothing and say nothing. Do not
  prompt, do not suggest, do not fetch.
- **Do not add a Go dependency.** `os/exec` and nothing else.

`pilot wire` is idempotent, and is itself careful about what it touches: it decides per
install whether to write `CLAUDE.md` or `CLAUDE.local.md`, skips rather than dirty a
tracked `CLAUDE.md`, and records anything it creates in that clone's
`.git/info/exclude` so `git status` stays clean. Running it twice is a no-op.

## 4. What NOT to do

**Do not embed the profile itself into `claude-playbook`.** This was considered and
rejected on 2026-09-22.

- It would make `claude-playbook` **mandatory** in order to have a profile. The stock
  `~/.claude` is not managed by this tool, and excluding it defeats the point — the
  profile describes the user's machine, not their playbook setup.
- This repo would end up owning the profile's schema, capture protocol and migrations,
  so a profile bugfix would require a `claude-playbook` release. That is exactly the
  ownership inversion that `chaos` created and that the extraction undid.
- The test that settles it: *if the user uninstalled `claude-playbook` tomorrow, would
  they still want the file describing their staging box?* Yes. So the tooling that
  maintains it cannot live here.

The **pointer** is fine to embed anywhere — it is four inert lines. The **tooling** is
not. Keep that distinction and there is no tension.

## 5. One thing worth knowing regardless

While designing `pilot wire` we measured something about this repo's own ecosystem that
may matter to `claude-playbook` independently of anything above.

`cpb update`'s overlay replaces every top-level entry the source ships, and
kommander-style `update.sh` **refuses to run at all** on a dirty tree (only `VERSION` and
`settings.json` are exempt). So **anything written into a tracked `CLAUDE.md` of an
installed playbook is first a blocker, then a casualty.**

On the author's machine, 8 of 20 installs are currently dirty on `CLAUDE.md` — the four
`chaos` installs because a rename was hand-applied to their tracked file. Those installs
cannot update until it is resolved.

If `claude-playbook` ever grows a feature that writes into an installed playbook's
`CLAUDE.md`, the same trap is waiting. `CLAUDE.local.md` — untracked and gitignored — is
the safe target, and an untracked `CLAUDE.md` (which is what `cpb create` produces) is
also safe. The rule is *never a **tracked** `CLAUDE.md`*, not *never `CLAUDE.md`*.

## 6. Possible follow-on, not requested

`pilot-profile` ships a `.agentship` TOML manifest mirroring `.playbook`. A playbook
could one day declare:

```toml
[dependencies]
pilot-profile = ">=0.1"
```

`claude-playbook` does not read this and is not being asked to. It is recorded only so
that the field name is not invented twice if dependency resolution is ever added.

## 7. Verifying

```sh
cpb create testpb --playbooks-dir /tmp/pb --no-alias
grep -c '^@~/.pilot-profile/' /tmp/pb/testpb/CLAUDE.md   # want 4
```

With `pilot` absent from `PATH`, the same command must still succeed and print nothing
extra. That is the whole acceptance criterion for 3b.

---

**Contact:** the `pilot-profile` repo's README has the full design; `DESIGN.md` there
carries the argument for extraction and `PACKAGING.md` the release plan.
