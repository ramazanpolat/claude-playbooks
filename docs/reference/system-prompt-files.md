# System-prompt files — spec (draft)

Status: **draft for the pilot's review.** Nothing here is implemented.
Queued 2026-09-26 as P11 and pulled forward the same day ("let's build next
layers"). Open points are marked **Open** and listed at the end; facts
still being verified empirically are marked **To verify**.

## What it is

A playbook can declare files it ships whose text Claude Code appends to its
system prompt at every launch. This is how a playbook's standing orders
(Kommander's protocol, a seat's rules) reach the model as system prompt,
rather than only as `CLAUDE.md` memory.

```toml
# the playbook's .playbook, as its source ships it
system_prompt_files = ["prompts/kommander.md"]
```

cpb passes the files to `claude` with `--append-system-prompt-file` at
launch. **cpb never reads, parses or interprets their contents**; it only
checks where they are.

## The declaration

- **Key:** `system_prompt_files`, a top-level list in the `.playbook`
  manifest, in the order the files are appended.
- **Paths** are relative to the install root and must stay inside it: no
  absolute path, no `..` that leaves the root, no symlink that resolves
  outside it (the same rule as the manifest's other paths, e.g. `subdir`).
  A path that breaks the rule makes the manifest invalid.
- **Source-owned.** Unlike `[env]`, which is install-local and which
  `update` keeps, this list is part of what the source ships: `install`
  records it and `update` takes the source's new list, as it takes the
  source's new files. A playbook's author decides its standing orders.
- **A missing file refuses the launch**, naming the path. A launch that
  silently dropped a playbook's standing orders would run it as a different
  playbook.

## At launch

`cpb run`, a launcher and `cpb start` (for a directory with a manifest) add

```
claude --append-system-prompt-file <install>/<file> [...]
```

before the pilot's own arguments; an `--append-system-prompt[-file]` the
pilot passes is kept and comes after. A sandboxed launch passes the paths as
they are mounted inside the sandbox.

- **Open (to verify):** whether `claude` accepts the flag more than once
  and appends each file in order. If it takes only one, cpb writes the files
  in declaration order into one generated file in the config directory
  (`.cpb-system-prompt.md`, rewritten at every launch and never edited by
  hand) and passes that. Concatenation copies bytes; it still interprets
  nothing.
- **To verify:** a size limit on the appended text; how the text sits next
  to `CLAUDE.md` memory; and resume. `claude --help` says a resumed
  conversation keeps its recorded system prompt (`--system-prompt-snapshot`)
  until it is compacted, so a changed file reaches a resumed session only
  after that. `EXPLAIN` says so.

## Visible where state is visible

- `SHOW PLAYBOOK --json` gains `"system_prompt_files": ["prompts/kommander.md"]`
  (an empty array when none); the human form gains a `Prompt files:` line.
- `EXPLAIN PLAYBOOK` lists the files a launch appends, in order, each with
  the layer that declared it (below), and flags a missing one.
- `SELECT` (v3.21.0) sees the field on `PLAYBOOKS`.
- `SHOW CREATE` writes nothing for a source-owned list: it arrives with
  `FROM`. Files the pilot adds (below) are written as statements.

## Files the pilot adds (Open)

The source's list covers a playbook as shipped. Whether a pilot can add
their own on top, and how, is open:

- a grammar clause, `ALTER PLAYBOOK x ADD SYSTEM PROMPT FILE '<path>'` /
  `DROP SYSTEM PROMPT FILE '<path>'`, stored install-local (a separate key,
  e.g. `local_system_prompt_files`, kept by `update` as `[env]` is), appended
  after the source's files; or
- nothing: a pilot who wants their own standing orders writes them into a
  layer (next section).

## Layering (Open: the pilot decides)

The chain today is kommander → cockpit → chaos → a company distribution such
as chaos-santiment. Each layer's standing orders must reach the model, in
chain order. Today the layers are joined **at build time**: a layer's
`playbook/base.toml` names its base (source, ref, subdir), what of it to
remove and how settings merge, and cockpit's assembler writes one flat tree
that is installed as one playbook (`dist/` carries `CLAUDE.md` together with
`CLAUDE.cockpit.md` and `CLAUDE.chaos.md`). Three shapes for where each
layer's `system_prompt_files` come from:

**A. Declared by the assembly (build time).** Each layer declares its own
files; the assembler concatenates the lists, base first, into the flat
tree's `.playbook`. cpb reads one manifest and knows nothing of chains.
- For: no new concept in cpb; the assembler already owns layering (removals,
  settings merges), so prompt files join the same mechanism; one install is
  one playbook, as today; a component stays standalone.
- Against: layering stays a release-time step; a pilot cannot put a layer on
  an existing install without a new assembled release.

**B. A relation between installed playbooks (run time).**
`CREATE PLAYBOOK chaos-santiment FROM <src> ON TOP OF chaos`: cpb records
the base, and at launch appends the base chain's files first, then the
playbook's own.
- For: layers compose at install time without an assembler.
- Against: only the prompt files would compose. Skills, hooks, settings and
  `CLAUDE.md` still need assembling, so a playbook would be half-layered by
  cpb and half by the assembler; updating a base would change every
  playbook above it; cpb would learn the chain, which today only the
  assembler knows.

**C. Playbook files (INCLUDE, v3.21.0).** A layer's `playbook.cpb`
`INCLUDE`s its base's and adds its own files with the pilot clause above.
- For: the grammar composes, and a layer's contribution is readable text.
- Against: it layers *configuration statements*, not the files a source
  ships: the prompt files still have to exist in the install, so the
  shipped trees must be assembled anyway. It depends on both the pilot
  clause and INCLUDE.

The spec does not choose. **A** needs nothing more from cpb than this page.
**B** and **C** each need a further section here once chosen.

## Not in scope

cpb does not generate, template or edit prompt text, fetch prompt files from
anywhere, or merge them with `CLAUDE.md`. Variables, conditionals or
per-launch selection of prompt files are not planned.

## Open points

1. One `--append-system-prompt-file` flag or several (verification running).
2. Size limit, coexistence with `CLAUDE.md`, resume behaviour (verification
   running).
3. Files the pilot adds: a clause stored install-local, or none.
4. Layering: A, B or C, the pilot's decision.
