# A shared launch copies a playbook's own login over the machine's login

**Status:** fixed. Path 1 (install) was fixed in v3.22.1, and the rest in
v3.23.1 by a same-account check. Found on 2026-09-27 while building `ISOLATED
LOGIN` for v3.23.0.

## Fixed in v3.23.1: only the machine's own account is copied

- **When a shared sync (`LinkCredentials`) finds a regular
  `.credentials.json` with a grant,** it compares the `accountUuid` in the
  playbook's `.claude.json` with the machine's own, from
  `~/.claude/.claude.json` or `~/.claude.json`.
  - **Equal:** the newer login is copied, as before. This is the heal for a
    refresh, which Claude Code writes by rename, replacing the link.
  - **Different, or unknown on either side:** the file is kept as
    `.credentials.json.cpb-own-<stamp>`. The account keys leave the playbook's
    `.claude.json`, with a backup. The link comes back, and one warning names
    neither account nor any value.
  - **No machine store at all:** unchanged. The sync leaves the playbook's
    own login where it is.
- **`findAccountState` reads only the machine's own state.** It no longer
  walks other playbooks, so an isolated playbook's identity is never seeded
  into a shared one.
- **This covers path 2 and every case under "Also affected".**
- **Tests:**
  - `internal/auth/same_account_test.go`: different, unknown on each side,
    same, and no machine store;
  - the flipped identity-walk test;
  - `cmd/shared_sync_test.go`: path 2 end to end.

  Against v3.23.0 they fail with the machine store overwritten. The arena
  check `shared-sync-same-account-ok` runs path 2 and the heal on the real
  binary.
- **On macOS** the per-directory Keychain item is the primary store, so this
  file path runs only when Claude Code falls back to the file. See
  `docs/guides/authentication.md`.

## Fixed in v3.22.1: a source never carries a login

- **`install` and `CREATE PLAYBOOK … FROM` (a directory or git)** leave a source's `.credentials.json` out of the install, whatever it is (a
  file or a link). The same goes for the account state in the source's
  `.claude.json`: the keys `internal/auth` lists as account and identity
  state (`oauthAccount`, `userID`, the onboarding and install markers, and
  the cached feature flags and eligibility). This applies at the install
  root and in its config subdirectory. `update` already preserved both
  files.
  - Each is one stderr line that names the source and the keys, never a
    value: `Warning: ignored <source>'s .credentials.json: a playbook source
    never carries a login`.
  - The source itself is not touched, and the rest of the source installs as
    before.
- **`CREATE PLAYBOOK … LINK <dir>`** develops in place, so it
  deletes nothing of the pilot's.
  - A regular `.credentials.json` there is renamed to
    `.credentials.json.cpb-ignored-<YYYY-MM-DD-HH_MM_SS>` before the first
    sync.
  - `.claude.json` is copied to `.claude.json.cpb-backup-<…>` before the same
    keys leave it.
  - A directory with `isolated_login = true` keeps both: its login is its own.
- **Tests** (`cmd/install_login_test.go`): repro 1 through a directory and
  through `file://` git, a shipped link, and LINK (set aside) next to an
  isolated LINK (kept). Each asserts the machine store is byte-for-byte
  unchanged. Against the v3.22.0 code the install and LINK tests fail with
  the machine store overwritten. The arena check `install-never-carries-login-ok`
  runs the install path on the real binary.

Still open: path 2 below (one isolated launch, then a shared one), and the
other cases under "Also affected". Fix 2 covers them all and needs its
design pass.

## The mechanism

`auth.LinkCredentials` (`internal/auth/auth.go`) runs on every shared-mode
sync. That covers `CREATE PLAYBOOK`, `install`, `LINK`, and every launch that is not
token-mode or isolated. It turns the playbook's `.credentials.json` into a
symlink to `~/.claude/.credentials.json`. When it finds a **regular file**
there instead, one that parses and carries an account grant
(`claudeAiOauth.accessToken`), and that file is **newer** than the machine's
store, it first **copies the file over `~/.claude/.credentials.json`**. Then
it replaces the file with the link.

The copy was added on 2026-07-10 (`d49e141`, "credentials symlink healing")
to keep a token that Claude Code refreshed inside a playbook after the
symlink had become a file. For that case, the same account refreshed, it is
right. The code never checks that the file belongs to the **same account**,
though, and "newer" is only an mtime comparison. Any grant in a regular file
wins over the machine's login if its mtime is later, and every shared
playbook moves to that account from its next launch.

## Reproduced (a Linux VM, throwaway `HOME`, made-up stores, cpb at 9642295)

Each run starts with `~/.claude/.credentials.json` =
`{"claudeAiOauth":{"accessToken":"MACHINE-ACCOUNT"}}` and an mtime one hour
ago.

1. **Installing a source that ships `.credentials.json`.** This is a local
   directory or a git repository whose tree has a `.credentials.json`
   carrying a grant (`OTHER-ACCOUNT`), with an mtime two days ago. Run
   `cpb "CREATE PLAYBOOK x FROM '<dir>'"` or the same with `FROM 'file://<repo>'`.
   The machine's store then reads `OTHER-ACCOUNT`. `copyDirWithinRoot` copies
   the file and gives it a fresh mtime. `install` then calls
   `SyncCredentials`, which sees a newer file and copies it over the
   machine's. The source's age does not matter.
   - For a published playbook, this means **installing it can swap
     pilot's Claude account for the publisher's**, silently. Everything the
     pilot then does in any shared playbook runs as that account, with its
     history and data on that account's side.
   - `update` is not affected: it preserves `.credentials.json` and never
     takes one from upstream (`defaultPreserved` in `cmd/update.go`).
2. **One isolated launch, then a shared one.**
   - `CREATE PLAYBOOK z`, then `CPB_ISOLATED_LOGIN=true cpb run z`,
     which detaches the link.
   - A `/login` there as another account writes a regular file. The repro
     writes it by hand.
   - A later plain `cpb run z` copies it over the machine's store:
     `OTHER-ACCOUNT`.
   - The variable reads as "this launch is isolated", but its login outlives
     the launch and wins at the next one.

## Also affected, by the same mechanism (not run)

- **Removing `isolated_login` by hand** from a playbook that holds its own
  login. `UNSET ISOLATED LOGIN` refuses exactly this in v3.23.0, but a hand
  edit bypasses the check: cpb enforces its rules when its own commands
  write, and does not guard state edited by hand.
- **Copying or restoring a playbook directory** that holds a login of its
  own, such as an isolated playbook copied as a template, or a backup
  unpacked into the playbooks root. With `cp -a` or `tar`, the mtime is kept.
  It is copied over the machine's store if it is newer.
- **Root's question: can Claude Code turn the symlink into a file by
  itself?** The healing code exists because it did, at least before
  2026-07-10: a write by rename replaces a symlink with a file. That was not
  re-verified against Claude Code 2.1.283, which would need a real login.
  - In that case the file normally holds the **same** account, refreshed,
    and the copy is the intended heal.
  - A `/login` as a different account in a *shared* playbook changes the
    machine's login by design ("`/login` anywhere logs in everywhere"),
    whether it writes through the link or replaces it. So this path is not
    unintended on its own. It becomes a hazard only in combination with the
    cases above.

## Checking a machine for exposure

`cpb auth status` reports store kinds only, never values. A playbook is
exposed only in shared mode with a file store. A playbook in **token** mode
is not, because `LinkCredentials` never runs there and the stored grant is
quarantined at every launch; it becomes exposed only if the machine token is
removed while its file still holds a grant.

## Suggested fix

1. **Built in v3.22.1** (above). `install` and `LINK` never take a source's
   `.credentials.json` or its account state: skipped in the copy, as
   `update` already does, with one line each. It is a skip, not a refusal of
   the whole source. That closes path 1, the security-relevant one.
2. **Built in v3.23.1** (above). **`LinkCredentials` never overwrites the machine's store with a different
   account.**
   - Copy only when the file's grant is the *same* account as the machine's.
     Otherwise keep the file aside as
     `.credentials.json.cpb-own-<YYYY-MM-DD-HH_MM_SS>` (mode 0600), link the
     shared store, and warn on stderr. The warning names the playbook, never
     the store.
   - Deciding "same account" needs a key that survives a refresh, such as the
     account record in the playbook's `.claude.json` against the machine's.
     That needs a check against Claude Code's current files before it is
     built.
3. `CPB_ISOLATED_LOGIN=true` could record, in the playbook, that
   a login was made under isolation, so a later shared launch treats it as
   fix 2 does. Fix 2 already covers this case.

Tests for 1 and 2 are the two repros above as unit tests, with the machine's
store unchanged as the assertion.
