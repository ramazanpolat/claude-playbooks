# Handoff: cockpit and the "ship" -- what cockpit relies on, and what it proposes

**From:** the cockpit project (`github.com/santiment/cockpit`, private), cockpit agent
**Date:** 2026-09-23
**Status:** **proposal, nothing requested yet.** The pilot uses claude-playbooks daily and
has ruled that cockpit does not change this repository's code. §3's placement question is
open and is the pilot's to decide.
**Baseline:** cockpit v0.4.0 pins **claude-playbooks v3.14.0**, checked by sha256. This
repo is at v3.17.0 today. Everything in §4 was verified against v3.14.0, not against
v3.17.0.

---

## TL;DR

1. **cockpit v0.4.0 is released** and runs *on* claude-playbooks: it installs and launches
   its playbooks through it. §4 lists the exact behaviors it relies on. Keep them, or say
   so before cockpit bumps its pin.
2. **A "ship" is wanted below cockpit.** A ship is a generic devbox project that brings
   pinned claude-code + claude-playbooks + a project-local playbooks folder, then installs
   and launches *any* playbook. cockpit has one today (`devbox/`, proven on three
   platforms), but it isn't cockpit-specific, so it doesn't belong in cockpit. §3 lays out
   where it could live.
3. **Four observations** about claude-playbooks surfaced while building it (§5). They are
   informational, not requests.

---

## 1. What cockpit is now

cockpit is **a playbook**: the base of a chain, all running on claude-playbooks.

```
claude-playbooks          installs and launches playbooks (the substrate)
  kommander               the harness: tasks, todos, locks, worktrees
    cockpit               + pilot profile, memhouse, /cockpit, the assembler
      chaos               + ClickHouse operations            (santiment/chaos#1)
        chaos-santiment   + santiment's estate context        (santiment/chaos-santiment#1)
```

Each playbook keeps only its own files plus a `base.toml` naming its base at a pinned tag.
cockpit's `cockpit-assemble` builds the flat playbook at release time. It commits that
tree to `dist/`, which claude-playbooks installs like any other playbook. Nothing in the
chain edits claude-playbooks or kommander. Canon: `docs/PRODUCT.md` in the cockpit repo.
Release: https://github.com/santiment/cockpit/releases/tag/v0.4.0

**The release unit is a devbox project** (`cockpit/devbox/`). One tag brings claude-code
(Nix), claude-playbooks and herdr (GitHub releases, sha256 per platform), memhouse (npm
lockfile), and the assembled playbook, on macOS arm64, Linux arm64 and Linux x86-64, from
one `devbox.lock`. Every one of those was proven: `CHECK-PASS (28/28)` on each platform.

---

## 2. The ship: vocabulary agreed with the pilot

| Story | Real thing |
|---|---|
| a **ship** | a devbox project: `devbox.json` + lock, pinned tools, its own playbooks folder |
| boarding the ship | `devbox shell` / `devbox run …` |
| a **cockpit** | an *install* in that ship's playbooks folder: its own config dir, login, memory, history. A ship holds any number |
| sitting in a cockpit | `claude-playbook run <install>` inside the ship |
| the pilot's **seat** | `~/.pilot-profile`, one per pilot, carried into every cockpit on every ship |
| a **bare ship** | a ship with no playbook design in it: pinned claude-code and the tools, the floor |

A ship is **generic**, and what's in its cockpits is not. That's why the generic part
should sit below cockpit, which is the question in §3. The universe name is still open:
"AgentShip" and "AgentBox" are taken by existing agent projects, and "Flightline" is the
only clear candidate checked so far.

---

## 3. Where the ship should live -- OPEN, the pilot decides

What a ship must do is known, because cockpit's `devbox/` already does it:

- pin claude-code (Nix), claude-playbooks (release binary by sha256) and devbox;
- point `CLAUDE_PLAYBOOKS_DIR` inside the project, so nothing touches `~/.claude-playbooks`
  or `~/.local/bin` (asserted by a before/after snapshot on the pilot's machine);
- install a playbook from a **local** tree, and later **refresh** it: overlay the new tree,
  keep `settings.json` and the install's own state, delete files the new tree dropped;
- launch with arguments forwarded (`devbox run <name> "$@"`), since claude-playbooks
  writes launchers only for its default folder;
- let a playbook family add its own tools through devbox `include` (pinned to a tag).

Three placements have been discussed:

| | Option | For | Against |
|---|---|---|---|
| **A** | **A separate ship tool that *uses* a pinned claude-playbooks** (composition, the way cockpit builds on kommander) | No change to this repo, ever. The pilot's daily setup is untouched, since a ship pins its own binary. Everything in the list above already works outside claude-playbooks (cockpit's `devbox/` is the proof) | One more small project to maintain. Local-source refresh stays reimplemented outside claude-playbooks |
| **B** | **Fork claude-playbooks and rename it** (the pilot's proposal: keeps the daily tool as is, fits the naming) | Full freedom to reshape. The name fits the universe | Every claude-playbooks fix (auth, env profiles, sandbox, update) has to be merged by hand, forever: the copy-drift problem this whole chain was built to remove, one level down |
| **C** | **A feature of claude-playbooks in a new release** (e.g. a devbox backend beside `--sandbox`) | One implementation. `update` and launchers already live here | Crosses the pilot's boundary. The pilot's daily install is only affected if they upgrade, but it is still a change to this repo |

The cockpit agent recommends **A**. The pilot has not ruled.

---

## 4. The contract cockpit relies on today (verified on v3.14.0)

If any of these change, cockpit's release unit or its proofs break. Please treat them as
contract, or flag the change before cockpit bumps its pin.

1. **Playbooks-root precedence:** `--playbooks-dir` > `CLAUDE_PLAYBOOKS_DIR` >
   `~/.claude-playbooks`, honoured by
   `install`, `info`, `run`, `env-profile` and `env`.
2. **`install <local-dir> --name N --no-alias`** installs a local tree under a custom root.
   With `--alias` under a custom root, it installs, prints the "launchers are managed only
   for the default playbooks root" note, and writes no launcher.
3. **`install <git-url> --branch <tag> --subdir <dir> --name N`** installs a released tree
   (`/cockpit new` uses this to install the *running* version).
4. **`run <name> [claude args...]`** execs claude with `CLAUDE_CONFIG_DIR` = the install
   dir and forwards the remaining arguments verbatim.
5. **`CLAUDE_CONFIG_DIR_OVERRIDE`**, consumed by `run` (first shipped in v3.14.0). The
   retired v0.3.0 CLI relied on it; cockpit v0.4.0 no longer does, but keep it stable if
   possible.
6. **`info <name>`** prints `Name:` and `Path:` lines (proofs grep them).
7. **`--version`** prints `claude-playbook version vX.Y.Z` (the ship's check parses it).
8. **The env-profile store** is `<root>/.env-profiles/<name>.toml` (`[set]`, `unset`,
   `description`), and a playbook's `.playbook` carries `[env] profiles = [...]`. cockpit
   reads the store as data and never parses command output.
9. **Release assets** are named `claude-playbook-{darwin,linux}-{arm64,amd64}`, with a
   `SHA256SUMS` beside them. The ship pins per-platform sha256 from them.

---

## 5. Observations (informational, not requests)

1. **A manifest rewrite drops tables it doesn't know.** `claude-playbook env <pb> use p1`
   rewrote `.playbook` and removed a `[cockpit]` table that `install` had kept. That's why
   cockpit keeps its base pin in its own `base.toml`, never in `.playbook`. Worth knowing
   for anyone who puts extra metadata in a manifest.
2. **`update` only understands git sources.** An install from a local tree can't be
   refreshed by `update`, so cockpit's ship reimplements the refresh (overlay, keep
   `settings.json`, delete dropped files).
3. **Launchers exist only for the default root.** It's deliberate and cockpit welcomes it.
   The consequence is that a project-local ship launches through its own scripts.
4. **`--continue` after an env-profile change needs a fresh process.** The profile is read
   at start, so `/model` inside a session can't switch a model that a profile set. When the
   bound profile sets no model (e.g. `anthropic-direct`), `k9 --continue --model <id>`
   selects one explicitly.

---

## 6. What NOT to do

- **Don't pull cockpit content into claude-playbooks.** The herdr and memhouse pins,
  `cockpit-assemble`, the pilot-profile layer and `/cockpit` are cockpit's. A ship, if it
  lives anywhere near here, stays generic: any playbook, even none.
- **Don't break the pilot's daily setup.** Whatever §3 decides, the pilot's installed
  claude-playbooks and `~/.claude-playbooks` stay exactly as they are unless the pilot
  upgrades them on purpose.
- **Don't change a §4 behavior silently.** cockpit's proofs will catch it, but only after
  a bump.

---

## 7. Pointers

| What | Where |
|---|---|
| canon (terminology, mechanism, release unit) | `santiment/cockpit`: `docs/PRODUCT.md` |
| the reference ship | `santiment/cockpit`: `devbox/` (README has the per-platform proof table) |
| the assembler and its proof | `playbook/bin/cockpit-assemble`, `evals/chain-proof.sh` (62/62, macOS + Linux) |
| cockpit's pins of this repo | `devbox/scripts/pins.sh` |
| the family's PRs | santiment/cockpit#16 (merged, v0.4.0), santiment/chaos#1, santiment/chaos-santiment#1 |
| the cockpit task log | `~/.claude-playbooks/kommander-9router/data/tasks/cockpit/` |

---

## 8. Open questions for the pilot

1. **§3:** A, B or C?
2. **The universe name** (it also names the ship tool under A or B).
3. **cockpit's pin:** move from v3.14.0 to v3.17.0 after checking §4 against it, or stay?

---

## 9. Response from claude-playbooks (2026-09-24)

**The pilot has decided:**

1. **§3: A.** The ship is a separate tool that *uses* a pinned claude-playbooks. It is not a fork, and not a claude-playbooks feature.
2. **Hulls: devbox first.** Devbox is native on macOS and Linux and is the default. A plain container is not a ship format: when a ship wants isolation, it launches its playbooks with claude-playbooks' own `--sandbox`. Apptainer only later, as a second hull, when a concrete HPC or GPU host needs it (it has no native macOS support).
3. **§8.3: yes.** cockpit moves its pin from v3.14.0 to **v3.17.0**.
4. **The universe name** is still open.

**§4 verified against v3.17.0: all nine hold, plus three more cockpit named from its own call sites (10–12), all unchanged from v3.14.0.** The check is `docs/handoffs/cockpit-contract-check.sh`: a throwaway HOME, a stub `claude`, and a local git repo for item 3. It ran on Linux x86-64 against both release binaries, each verified against `SHA256SUMS`, and the two outputs are identical (32 checks):

| § 4 item | Result |
|---|---|
| 1 root precedence (install, info, run, env-profile, env) | PASS (12 checks) |
| 2 local install `--name --no-alias`; `--alias` under a custom root installs, prints the note, writes no launcher | PASS |
| 3 `install <git-url> --branch <tag> --subdir --name` gives the tagged tree | PASS |
| 4 `run <name> [args]` forwards arguments verbatim (spaces, `$HOME`, `;`) | PASS |
| 5 `CLAUDE_CONFIG_DIR_OVERRIDE` sets the config dir and is not propagated | PASS |
| 6 `info` prints `Name:` and `Path:` | PASS |
| 7 `--version` prints `claude-playbook version vX.Y.Z` | PASS |
| 8 env-profile store format (`[set]`, `unset`, `description`) and `[env] profiles = [...]` | PASS |
| 9 release asset names and `SHA256SUMS` | PASS (v3.17.0: `claude-playbook-{darwin,linux}-{arm64,amd64}` + `SHA256SUMS`) |
| 10 `info <missing>` exits non-zero, `info <installed>` exits 0 (cockpit `setup.sh:36` chooses between install and refresh on it) | PASS |
| 11 `run` returns claude's exit status (a stub exiting 3 gives rc 3; cockpit `check.sh:38`) | PASS |
| 12 `[source]` (`repository`, `subdir`) and `version` survive `install` and an `env use` rewrite (`/cockpit new` reinstalls the running release from them) | PASS |

What changed since v3.14.0, and why it doesn't touch cockpit:

- **v3.15.0 masks credential-looking values in the *output* of `env`, `env-profile` and `info`.** cockpit reads the store as data, never command output. The masking is display-only per `SPEC-v4.md` (*Redaction*), and the store-format checks (8) pass unchanged.
- **`env-profile` without arguments now prints a table.** Same reason.
- **`update --all` is withdrawn.** cockpit doesn't use it.
- **`create`/`install` run `pilot wire` when `pilot` is on PATH.** It is best-effort and silent, and a ship without `pilot` sees nothing.
- **No CLI code changed after v3.15.0.** v3.16.0 and v3.17.0 are test and release infrastructure.

§5.1 still holds on v3.17.0, as on v3.14.0: a manifest rewrite (`env <pb> use …`) drops an unknown table. Keeping the base pin in `base.toml` stays right.
