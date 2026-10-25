# Changelog

## [Unreleased]

### Breaking changes

- **A playbook routed to another provider with no key of its own, on a shared
  login, no longer launches** (the login guard, below): Claude Code would
  send your Claude login or token there. The refusal names the address and
  the fixes; `examples/upgrade.sh` names each affected playbook. Fix, one
  line: `cpb ALTER ENV <set> SET ANTHROPIC_AUTH_TOKEN FROM '<ref>'` (the
  provider's own key), or `cpb ALTER PLAYBOOK <name> SET login = 'isolated'
  BLOCK VAR CLAUDE_CODE_OAUTH_TOKEN`.

### Known gaps

- The website's `cpb play` template `router-glm` (`site/p/router-glm.cpb`)
  routes to a router without a router key, so the login guard refuses it
  once a login can reach it. The site is unchanged in this release.

### Accounts

- **The OAuth token takes a secret reference.** `CREATE ENV acct-work SET
  CLAUDE_CODE_OAUTH_TOKEN FROM 'keychain:…'` makes an account: attach it to
  switch a playbook's account (`REPLACE ENV`), or pick it for one launch
  (`cpb run --env-set acct-work <name>`). The launch execs through the secret
  helper, which sets the token; cpb never holds its value, and a token exported
  in the shell cannot stand in for it. Until now this key was refused as a
  reference. An isolated playbook takes a token reference only from what it
  supplies itself, as it does a value: one in `DEFAULTS` never reaches it.
- **The login guard: your Claude login or token never goes to another
  provider.** A launch whose `ANTHROPIC_BASE_URL` is not Anthropic's, with no
  provider key (`ANTHROPIC_AUTH_TOKEN` or `ANTHROPIC_API_KEY`, a value or a
  reference, or an `apiKeyHelper`), is refused before anything changes when a
  Claude login or token could reach it, naming the fixes. `EXPLAIN --json`
  gains `route.guard`. See *Breaking changes* for what it refuses on
  upgrade.
- **A token over an isolated playbook's own login runs on Linux again.**
  v4.0.0 refuses that launch everywhere (earlier releases deleted the stored
  login, and the playbook needed `/login` again). On Linux the login is now set aside for the
  run and put back when it ends (a killed run's comes back at the next
  launch). On macOS, where the login lives in the Keychain, the launch is
  refused before anything changes, naming the fixes: `cpb run --block
  CLAUDE_CODE_OAUTH_TOKEN <name>` for one launch, `BLOCK VAR
  CLAUDE_CODE_OAUTH_TOKEN` for good, or dropping the account's env set.

## [v4.0.0-rc5] -- 2026-10-24

### Upgrading from rc4

- **Three things rc4 accepted are now refused**, each with a one-line
  reason: a new launcher named after an existing command (`claude`, a keyword, or a command already on PATH), a
  source URL carrying a credential (`https://TOKEN@host/…`), and a token
  launch over an isolated playbook's own login. A launcher that already
  exists keeps working.
- **Installing from a source changes what it copies:** the source's
  launcher, cpb's own records and one machine's runtime state stay behind.
  See Changed and Fixed below.
- **An isolated playbook no longer gets a token from `DEFAULTS`.** One that
  launched on a `CLAUDE_CODE_OAUTH_TOKEN` from an env set in `DEFAULTS`
  under rc4 launches without it: give it the token in its own block or an
  env set it uses, or log in there.
- **A playbook file is read as written:** a comma after a quoted item
  separates it from the next (a re-apply that stored `'x',` as `x,` now
  stores `x`), and an empty quoted list item is refused.

### Changed

- **A new launcher never takes an existing command's name.** Creating a
  launcher is refused when its name is `claude` (any case), a keyword,
  starts with `-`, holds a control character, or is a command already on PATH or a file in the launcher directory that cpb did
  not write. A playbook named after a command (`git`, `python3`) needs
  `SET launcher = '<another name>'` or `SET launcher = ''`. A launcher
  that already exists keeps working, survives a `RENAME TO` that keeps its
  name, and can still be removed.
- **A source never chooses the launcher.** `CREATE PLAYBOOK … FROM` ignores
  a `launcher` in the source's manifest, with a note: the launcher is
  `SET launcher`, else the playbook's name.
- **A stated login wins over the source's.** `CREATE PLAYBOOK … FROM …
  SET login = 'shared'` installs a shared login even when the source's
  manifest sets `isolated_login`; without `SET login` the source's value is
  kept.

### Fixed

- **A Foundry route is no longer reported as Anthropic's.** `EXPLAIN`'s
  `route.egress` reads `unknown` when `CLAUDE_CODE_USE_FOUNDRY` is on or
  `ANTHROPIC_FOUNDRY_BASE_URL` is set, in a layer or in the environment, as for
  Bedrock and Vertex; `cpb play` asks for the typed confirmation of a recipe
  that sets either.

- **Playbook files: commas and quotes parse as written.** In a playbook
  file a comma after a quoted item is the separator, never part of the
  item: `ALLOW TOOL 'Bash(git diff *)', 'Read'`, `SET A='x', B=y` and
  `ARGS '--a', '--b'` give `Bash(git diff *)`, `x` and `--a` (they used to
  keep the comma). A fully quoted property value keeps the quotes inside
  it: `SET statusline.command = '"/opt/My Tools/sl.sh"'` stores
  `"/opt/My Tools/sl.sh"`. A file that starts with a UTF-8 byte-order mark
  is read (it was refused). An empty quoted list item is refused with or
  without a comma after it (`'', 'Read'` used to drop it silently).
  Unchanged: an unquoted final value keeps its comma
  (`SET NO_PROXY=a,b,`), an unquoted `ARGS` item is taken as written, and
  command-line values are parsed as before.
- **A rename keeps the launcher the playbook has.** `RENAME TO` gave a
  playbook with no launcher (`SET launcher = ''`) a launcher named after its
  new name; it now gets none. A custom launcher stays, and the new name
  gets a launcher beside it unless that name cannot be one (a command on
  PATH): then the rename goes ahead with a note instead of failing.
- **`APPLY --dry-run` refuses what the run refuses about names**: a
  `RENAME TO` onto an existing playbook, a launcher another playbook
  answers to, a launcher that cannot be created. Names are judged against
  the registry the file's earlier statements leave.
- **`SHOW CREATE … --skip-secrets` no longer deletes a withheld value when
  the file is applied.** An env set with a withheld literal is written as
  `CREATE ENV IF NOT EXISTS` plus an additive `ALTER ENV`, so `APPLY` keeps
  the stored value (the TUI's export always withholds). Such a set no longer
  converges exactly: a key added to it since is not removed.
- **A source no longer brings cpb's own records.** `CREATE PLAYBOOK … FROM`
  drops a source's `[play]`, `[apply]`, `[skills]` and `[mcp]` records with a
  note, so `cpb update` cannot follow the source author's recipe; `update`
  keeps the live records.
- **A source URL carrying a credential is refused** (`https://TOKEN@host/…`),
  dry run included. An install recorded with one still updates, with the URL
  masked in SHOW, SELECT, update and git's output.
- **`update` keeps one backup, without values.** The old `.playbook` and the
  preserved files are removed from the backup after a successful update,
  earlier updates' backups are removed, and `DROP PLAYBOOK` removes the last.
- **A `LINK`ed playbook whose target is gone can be dropped.**
- **A source never brings one machine's runtime state.** Installing from a
  used directory or a repository holding one leaves out its session records
  (`projects/`, `history.jsonl` and the like), set-aside logins
  (`.credentials.json.*`), state backups (`.claude.json.*`, `*.cpb-backup-*`)
  and `.apply/`, with a note; `update` never lets them replace the install's
  own.
- **A backup kept by an incomplete rollback is never removed** by a later
  update or `DROP PLAYBOOK` (it is renamed `….incomplete` and named on drop).
- **An isolated playbook's login is never deleted by a token launch.** A
  token in an env set listed in `DEFAULTS` was taken as an isolated
  playbook's own token: it was injected, and the playbook's own login was
  deleted from its store. `DEFAULTS` is the machine's layer and no longer
  supplies an isolated playbook's token. A token the playbook supplies itself
  (its block, an env set it uses, `--env`) over a login it holds is now
  refused before anything changes, naming the ways out: `--block
  CLAUDE_CODE_OAUTH_TOKEN` to use the login, removing the token from the
  playbook, or `/logout` first to use the token. `auth status` shows such a
  playbook as `error` with the reason.
- **A token launch keeps a shared playbook's stored login.** When the token
  path stripped a store's grant that was another account's, or one not
  confirmed as the machine's, that login was lost. It is now copied to
  `.credentials.json.cpb-own-<stamp>` first, with one line on stderr; the
  machine's own account's newer grant goes over the machine store first.
- **A stale `~/.claude/.claude.json` no longer passes for the machine's
  account.** It was read ahead of `~/.claude.json`, so after the machine
  logged in to another account an old account's grant could be copied over
  the machine's login. `~/.claude.json` now decides whenever it exists, and
  when the two files name different accounts no login is promoted.

## [v4.0.0-rc4] -- 2026-10-18

### Upgrading from rc3

- **The remaining rc3 clause forms are refused**, each with a one-line hint
  naming the property that replaces it. A playbook on disk needs nothing:
  its manifest and `settings.json` keep the same keys. A recipe or a
  `SHOW CREATE` file written by rc3 stops at the first such line:
  - `LAUNCHER <name>` / `NO LAUNCHER`: `SET launcher = '<name>'` /
    `SET launcher = ''`;
  - `SET MODEL '<model>'` / `UNSET MODEL`, `SET AGENT '<agent>'` /
    `UNSET AGENT`: `SET model = …`, `SET agent = …`, `RESET model`,
    `RESET agent`;
  - `SANDBOX`, `SET SANDBOX`, `SET SANDBOX <key>=<value>`, `UNSET SANDBOX`,
    `UNSET SANDBOX <key>`: `SET sandbox.always = true, login = 'isolated'`,
    `SET sandbox.<key> = …`, `SET sandbox.always = false`,
    `RESET sandbox.<key>`;
  - `SET STATUSLINE '<command>' [REFRESH <n>] [IF UNSET]`,
    `SET STATUSLINE REFRESH <n>`, `UNSET STATUSLINE [REFRESH]`:
    `SET [IF UNSET] statusline.command = …, statusline.refresh = <n>`,
    `RESET statusline`, `RESET statusline.refresh`;
  - `SET STATUSLINE PREVIOUS`: gone with the status line history; set the
    command again;
  - `SET MODEL PICKER ONLY|APPEND` / `UNSET MODEL PICKER`:
    `SET model_picker.mode = …`, `RESET model_picker.mode`;
  - `ALTER DEFAULTS SET SECRET HELPER '<command>'` / `UNSET SECRET HELPER`:
    `ALTER DEFAULTS SET secret_helper = …`, `RESET secret_helper`.
  Regenerate a machine's file with `cpb SHOW CREATE ALL`, or edit the lines
  the hints name.
- **`SET MODEL=<value>` on the command line is refused**: it used to set the
  model property, being a variable that lost its `VAR`. Write
  `SET VAR MODEL=<value>` for the variable or `SET model = …` for the
  property.
- **`sandbox.always = true` needs `login = 'isolated'`**, already set or in
  the same statement; it no longer isolates the login on its own.
- **`SET IF UNSET` refuses `launcher`, `login`, `global_instructions`,
  `sandbox.always`, `sandbox.share_skills` and `sandbox.secrets`**: those
  keys always hold a value.
- **JSON and `SELECT`:**
  - `SHOW PLAYBOOK --json` and `SELECT … FROM PLAYBOOKS` give one
    `statusline` object, `{command, refresh}`, in place of the `statusline`,
    `statusline_refresh` and `statusline_history` fields.
  - `SHOW DEFAULTS --json` and `SELECT … FROM DEFAULTS` give
    `secret_helper` as the command (a string or null) and
    `secret_helper_from`, in place of the `{command, from}` object.
- **`SHOW CREATE` writes a different shape**: `CREATE OR ALTER PLAYBOOK`
  with the launcher (`RESET launcher` for the default), `login`,
  `global_instructions`, the sandbox keys and the whole `USE ENV` list
  (`USE ENV ''` for none) on the `CREATE`, and a
  `RESET` for every property that is not set (`RESET sandbox` in place of
  `sandbox.always = false`). Applying it twice changes nothing, and it puts
  back a model or an env set added elsewhere.
- **The status line history is no longer kept.** cpb no longer writes or
  reads `<playbooks root>/.state/statusline-history.json`; a leftover one
  can be deleted. `REVERT STATUSLINE` (never released) is not a clause.
- **`WITH` is a reserved word** (`REPLACE ENV <old> WITH <new>`): a new
  playbook or env set cannot be named `with` unquoted.

### Added

- **`REPLACE ENV <old> WITH <new>`** (SPEC.md, *Grammar*), in `ALTER
  PLAYBOOK` and `ALTER DEFAULTS`: one env set swapped for another at the
  same position, so switching a playbook's account is one clause. Run again,
  it changes nothing. New attached elsewhere, neither attached, or an
  unknown new set is an error, and nothing is written. `SHOW CREATE` writes
  the whole `USE ENV` list, never `REPLACE`; a played recipe may not hold it.
  `WITH` is a reserved word.
- **`USE ENV` inside `CREATE [OR ALTER] PLAYBOOK`** (SPEC.md, *Grammar*):
  the whole list, so one statement creates or converges a playbook with its
  env sets. Every set must exist before anything is created. On a playbook
  that exists, `CREATE OR ALTER` replaces the list only where it differs; a
  statement without `USE ENV` keeps it. `ADD`, `DROP` and `REPLACE ENV` stay
  in `ALTER`. `SHOW CREATE` now writes `USE ENV` on the `CREATE`, not in
  the `ALTER` after it.
- **The launcher is an ordinary key** (SPEC.md, *Grammar*): `ALTER PLAYBOOK
  … SET launcher = 's', model = 'x'` is one statement, as it already was in
  `CREATE`. The launcher name is checked before anything is written, and it
  changes last, only where it differs. Only `RENAME TO` stays a statement of
  its own (with `SET launcher` at most).
- **Exact convergence** (SPEC.md, *Playbook properties*, *SHOW CREATE and
  APPLY*): `RESET` is accepted in `CREATE OR ALTER PLAYBOOK` (a no-op on a
  new empty playbook; on one installed `FROM` a source it acts on the
  installed copy), and `SHOW CREATE` writes `RESET` for every property that
  is not set (`RESET sandbox` when the table is empty, replacing
  `sandbox.always = false`) and the whole env list, `USE ENV ''` for none.
  So a playbook's own `SHOW CREATE` puts back a model, a status line or an
  env set added elsewhere. Collections stay additive. `USE ENV ''` is the
  explicit empty list in `ALTER PLAYBOOK`, `ALTER DEFAULTS` and `CREATE`.
- **`SET IF UNSET` takes only keys whose default is no value** (SPEC.md,
  *Playbook properties*): `model`, `agent`, `statusline.*`,
  `model_picker.mode` and the sandbox keys without a default. `launcher`,
  `login`, `global_instructions`, `sandbox.always`, `sandbox.share_skills`
  and `sandbox.secrets` are refused with a one-line reason: they are never
  unset, so an explicit `login = 'shared'` read as unset. `SET IF UNSET` is
  now accepted in `CREATE OR ALTER PLAYBOOK` too (judged after the
  creation on a new playbook, so a source's `settings.json` counts, and
  on one that exists as it stands).
- **`global_instructions` on `APPLY … TO ~/.claude` is refused before
  anything is written** (SPEC.md, *TO a plain config directory*), `SET` and
  `RESET` alike, and `~/.claude` is recognized by filesystem identity (a
  symlink to it, a case variant). The check now runs while `APPLY`
  validates the files.

### Changed

- **`FROM` reads "install from a source"** in SPEC, README, the guides and bare `cpb`'s empty-state hint: `CREATE PLAYBOOK … FROM` installs a playbook from a Git repository or a local directory. The keyword and what it does are unchanged.
- **The sandbox is marked experimental** in SPEC, README, the guides and `--help`: it works and is supported in v4, but its flags and `sandbox.*` keys may change or move to a separate runner. Nothing changes at launch.
- **`CREATE OR ALTER PLAYBOOK … SET …` converges** (SPEC.md, *Grammar*,
  *Playbook properties*): it creates the playbook, or, on one that exists,
  applies its `SET` list as `ALTER PLAYBOOK … SET` would (the properties,
  then the launcher, each only where it differs). `FROM`, `BRANCH`, `SUBDIR`
  and `LINK` are only compared, and a different source stays a warning.
  Applied again, it changes nothing. `CREATE PLAYBOOK IF NOT EXISTS` keeps
  SQL's meaning: on a playbook that exists it changes nothing. `APPLY` folds
  an `OR ALTER`'s pairs as an `ALTER`'s when the playbook exists before the
  run, and `SHOW CREATE` writes `CREATE OR ALTER PLAYBOOK` with the launcher,
  login, global_instructions and sandbox: one converging statement.
- **`SET IF UNSET` takes any property whose default is no value** (see
  below) and applies whole or not at all: only when none of the keys it
  names is set; otherwise it changes nothing and prints `PLAYBOOK x: <key> is
  set; SET IF UNSET changed nothing`. `APPLY` folds it as one clause.
- **Property keys are lowercase and exact.** `SET MODEL=glm-5.3`, a variable
  that lost its `VAR`, used to set the model property from the command
  line; it is refused now with `a variable is SET VAR MODEL=<value>`.
- **A list property also takes its items comma-joined**: `sandbox.mounts =
  ~/libs:ro,~/data` needs no `[ ]`, which zsh reads as a pattern. An item
  never holds a comma; `SHOW CREATE` writes `['a', 'b']`.
- **The status line is a table**: `statusline.command` and
  `statusline.refresh` (were `statusline` and `statusline_refresh`), and
  `RESET statusline` for both. `SHOW PLAYBOOK --json` and `SELECT` have one
  `statusline` object, `{command, refresh}`, in place of the
  `statusline`, `statusline_refresh` and `statusline_history` fields.
- **`RESET model_picker` is the mode**: the rows are a collection, which
  `DROP MODEL` empties; it used to remove the rows too.
- **The words of removed clauses are no longer reserved** (`LAUNCHER`, `NO`,
  `SANDBOX`, `AGENT`, `SECRET`, `HELPER`, `PICKER`, `ONLY`, `APPEND`,
  `REFRESH`): where a clause starts they still give the hint, and elsewhere a
  name, a launcher or a value may be one.
- **Adjacent property clauses render as one list**: `SHOW CREATE` and the
  plans write `SET a = 1, b = 2` and `RESET a, b`, which parse back to the
  same clauses.

- **The status line, the model picker's mode and the secret helper are
  properties** (SPEC.md, *Status line and model*, *Model picker*,
  *Secrets*): `SET statusline.command = '<command>', statusline.refresh = 10`,
  `RESET statusline`, `RESET statusline.refresh`; `SET model_picker.mode =
  'only'`, `RESET model_picker.mode`; `ALTER DEFAULTS SET secret_helper =
  '<command>'`, `RESET secret_helper`.
  - `SET IF UNSET statusline.command = …` offers a status line without
    replacing one; the condition goes right after `SET`.
  - **The status line history is gone.** cpb keeps no record of the status
    lines it replaced, so there is no `REVERT STATUSLINE`; rc3's
    `SET STATUSLINE PREVIOUS` is refused with a hint to set the command
    again. A status line another tool wrote is that tool's to restore.
    `SHOW PLAYBOOK --json` and `SELECT`'s `statusline` lose their `history`
    field. A leftover `<playbooks root>/.state/statusline-history.json` is
    no longer read and can be deleted.
  - The old `SET` / `UNSET STATUSLINE [REFRESH]`, `SET STATUSLINE PREVIOUS`,
    `SET` / `UNSET MODEL PICKER` and `SET` / `UNSET SECRET HELPER` forms are
    refused with a one-line hint. `ADD` / `DROP MODEL` are unchanged.
  - **`SHOW DEFAULTS --json` and `SELECT … FROM DEFAULTS`**: `secret_helper`
    is now the command (a string or null) and `secret_helper_from` says
    where it is set, in place of the `{command, from}` object.
  - `examples/coverage.sh` read a clause kind with an underscore as two
    words; it reads each kind whole now.

- **The sandbox is the `sandbox.<key>` properties** (SPEC.md, *Sandbox*):
  `SET sandbox.always = true, login = 'isolated'`, `SET sandbox.backend =
  'sbx', sandbox.mounts = ['~/libs:ro']`, `RESET sandbox.host`, and `RESET
  sandbox` for the whole table. Values are typed: `true` / `false`, lists as
  `['a', 'b']` or `'a,b'`, strings.
  - `sandbox.always` is one key like the others and **no longer isolates the
    login on its own**: `sandbox.always = true` needs `login = 'isolated'`,
    already or in the same statement, and is refused otherwise with that
    fix. `SHOW CREATE` writes both.
  - `CREATE PLAYBOOK … SANDBOX`, `SET SANDBOX`, `SET SANDBOX <key>=<value>`,
    `UNSET SANDBOX` and `UNSET SANDBOX <key>` are refused with a one-line
    hint. A recipe's `create-with: SANDBOX` header is unchanged.
  - `SHOW CREATE` writes `sandbox.always = …` always, with the table's other
    set keys, on the `CREATE`.

- **The launcher, the model and the agent are properties** (SPEC.md,
  *Playbook properties*), beside `login` and `global_instructions`:
  `CREATE PLAYBOOK x SET launcher = 'w', model = 'claude-opus-5-5'`,
  `ALTER PLAYBOOK x SET agent = 'reviewer'`, `ALTER PLAYBOOK x RESET
  launcher, model`.
  - `launcher = ''` is no launcher; `RESET launcher` puts back the default,
    the playbook's own name; `launcher = '<its name>'` is that default and
    records nothing.
  - `CREATE … SET model = …, agent = …` writes them into the new playbook's
    `settings.json` in the same statement.
  - `LAUNCHER <name>`, `NO LAUNCHER`, `SET MODEL '<model>'`, `UNSET MODEL`,
    `SET AGENT '<agent>'` and `UNSET AGENT` are refused with a one-line hint
    to the property. `SET MODEL PICKER` and `ADD` / `DROP MODEL` are
    unchanged.
  - `SHOW CREATE` writes the launcher in the `CREATE` (the default as
    `RESET launcher`, since a playbook's name may be a keyword), and `SET
    model = …` / `SET agent = …`.
- **Whole or nothing, everywhere a statement mixes steps.** A launcher beside
  other keys is checked (taken by another playbook, a file cpb did not
  write, a linked target) before any key is written, in `ALTER` and `CREATE
  OR ALTER`; a `CREATE`'s `SET` list is checked before the directory exists,
  and a later failure removes the playbook the statement created; `LINK`
  refuses a property inside `SET IF UNSET` before linking. On a new
  playbook installed `FROM` a source, `RESET` and `SET IF UNSET` act on the
  installed copy. Under a `--playbooks-dir` root a launcher change is
  recorded as the alias with a note, alone or beside other keys. A
  credential inside `SET IF UNSET` is withheld from the `APPLY` record, a
  dry run judges `SET IF UNSET` against the sandbox keys its earlier
  statements set, and a hint never names a form the statement refuses
  (`ALTER DEFAULTS`, `CREATE`'s `SET LOGIN`).
- **The examples' stand-in claude records plugins in `settings.json`**, as
  Claude Code does, so `SHOW CREATE` writes their plugin clauses and the
  upgrade check compares them.

- **A one-word value reads the same quoted or bare**, in a file and on the
  command line (`login = isolated`); a value with a space or a comma is
  quoted. Keys are lowercase.
- **`SHOW CREATE` writes the properties on the playbook's `CREATE`**
  (`login = …, global_instructions = …`, always), and `SHOW PLAYBOOK` marks
  the default value `(default)`.
- **The `[apply]` undo follows `SET IF UNSET`**: what a `SET IF UNSET` list
  offered is removed only where the playbook still holds the list's values.

### Fixed

- **A comma inside a quoted property value was dropped** when it ended the
  value (`statusline.command = 'cmd a,b,'` lost its last comma). A comma
  after the quotes separates the next pair; one inside is the value's own.

## [v4.0.0-rc3] -- 2026-10-16

### Upgrading from rc2

- **`ISOLATED LOGIN` is refused.** A recipe from rc1 or rc2 that says
  `ISOLATED LOGIN` or `UNSET ISOLATED LOGIN` stops with a one-line hint:
  write `SET login = 'isolated'`, or `RESET login` for the shared login. A playbook on
  disk needs nothing: its manifest keeps `isolated_login`.
- **JSON: `isolated_login` is gone.** `SHOW PLAYBOOK --json` and `SELECT`
  give `login` (`"shared"` or `"isolated"`) and `global_instructions` in place of the
  `isolated_login` bool. A script that read `isolated_login` now reads
  `login == "isolated"`.
- **The licence is Apache-2.0.** From this release on, cpb is under the
  Apache License 2.0, with a `NOTICE` file. Every release carries `LICENSE`
  and `NOTICE` beside its binaries, `install.sh` places them in
  `<prefix>/share/doc/cpb`, and the npm package carries `NOTICE`.
  v4.0.0-rc2 and earlier releases keep their MIT licence.
- **A new playbook leaves `~/.claude`'s own CLAUDE.md and rules out**
  (`global_instructions = 'excluded'` is the default; see *Added*). An
  existing playbook is not changed.

### Added

- **Playbook properties** (SPEC.md, *Playbook properties*): what can change
  after a playbook is created is a `key = 'value'` pair.
  `CREATE PLAYBOOK … SET login = 'isolated', global_instructions = 'inherited'`
  gives the starting values, `ALTER PLAYBOOK … SET …` changes them, and
  `ALTER PLAYBOOK … RESET login, global_instructions` puts them back to the
  default.
  What is fixed at creation (`FROM`, `BRANCH`, `SUBDIR`, `LINK`) stays a
  keyword clause of `CREATE`. Two properties for now:
  - `login` (`'shared'`, `'isolated'`) is what `ISOLATED LOGIN` was.
  - `global_instructions` (`'excluded'`, `'inherited'`) is new. Claude Code
    loads the CLAUDE.md of every ancestor of the working directory, so
    `~/.claude`'s own `CLAUDE.md` and `rules/` loaded into every playbook run
    under `$HOME`. `'excluded'` keeps them out with one `claudeMdExcludes`
    entry in the playbook's `settings.json`, which every launch path reads.
    **A new playbook excludes them by default**; an existing one keeps
    loading them until `SET global_instructions = 'excluded'` (nothing is
    migrated). The project's own `CLAUDE.md` loads either way.
  - In a playbook file a value is quoted (`login = 'isolated'`); on the
    command line the shell removes the quotes, so `SET login = isolated`
    works there.
  - Each key is its field: `SHOW PLAYBOOK --json` and `EXPLAIN --json` have
    `"login"` and `"global_instructions"`, and `SELECT`'s `PLAYBOOKS` has
    `login` and `global_instructions` columns. `SHOW CREATE` writes
    `SET login = …, global_instructions = …`, both always, so a recipe never
    leans on a default. `cpb play` keeps the login isolated and the global
    instructions excluded: a recipe may isolate more, never less.
- **`EXPLAIN PLAYBOOK --json` gains `route`** (#209): where a launch from
  this environment sends its requests (base URL, host, models), an
  authentication state (`none`, `token-set`, `oauth-login`, `unknown`) and an
  egress class (`anthropic`, `unknown`), from non-secret values and states
  only. `auth status` finds a login kept in the Keychain.

- **`cpb update` follows the files a playbook was applied from** (SPEC.md,
  *The `[apply]` record*). A clause a base file dropped used to stay in every
  playbook built on it until removed by hand.
  - An `APPLY` that gives a playbook name-less statements (by `TO` or `USE
    PLAYBOOK`) records the files in its manifest as `[apply]`, and what the
    statements wrote in `.apply/recipe.cpb`. The record holds references
    only: a credential-looking literal is kept as `<withheld>`.
  - `cpb update <name>` reads the files again and runs only the statements
    they give `<name>`. First, one `ALTER PLAYBOOK` removes what they wrote
    last time and no longer write the same way: the same undo as a played
    playbook's update. A status line `SET STATUSLINE … IF UNSET` offered is
    removed only where the files wrote it. `--dry-run` shows the line diff
    and the plan; `--json` gives the plan as `APPLY --dry-run --json` does.
  - Not recorded: a dry run, a directory target, a linked playbook, files
    from a pipe, a statement that names its playbook, and a playbook that
    updates from `[play]` or `[source]`.
  - `SHOW PLAYBOOK` has an `Applied from:` line, and a new last field
    `apply` in `--json`. `SELECT` has a new `apply` column. The error for a
    playbook with nothing to update from now names `[apply]`.

### Changed

- **No output prints a character of a credential** (#208; SPEC.md,
  *Redaction*). A masked value used to keep up to four characters at each
  end (`sk-a...9f2c (51 chars, plaintext)`). It now prints only its length,
  `<redacted, N chars>`, a URL's userinfo included, and `SHOW` and `EXPLAIN`
  read a stored plaintext credential as `<plaintext, N chars>`. The JSON
  forms and `SHOW CREATE` already printed no value.
- **`ISOLATED LOGIN` is gone** for `SET login = 'isolated'` (CREATE and
  ALTER), and `UNSET ISOLATED LOGIN` for `SET login = 'shared'`. A recipe
  from rc1 or rc2 that says `ISOLATED LOGIN` is refused with that hint; edit
  the line. State on disk is unchanged (`isolated_login` in the manifest).
- **`SHOW PLAYBOOK --json` and `SELECT`: `isolated_login` (a bool) is now
  `login`** (`"shared"` or `"isolated"`, `"isolated"` for a sandboxed
  playbook too), beside the new `global_instructions`.

- **`cpb update` undoes env sets and sandbox settings too** (SPEC.md,
  *`cpb update <name>`*). Before an update, the undo now also drops an env
  set the files no longer attach (`DROP ENV`) and unsets a sandbox setting
  they no longer set (`UNSET SANDBOX <key>`; `always` for a bare `SET
  SANDBOX`). A base that stops routing through an env set now stops routing
  its children through it at their update. The login is still never unset.
  A played recipe can hold neither `ENV` nor `SANDBOX` clauses, so for
  play only the shared code moves.
- **A setting set more than once is written once** (SPEC.md, *A setting set
  more than once*). In a stack of files (a base recipe, then the child that
  overrides it), `APPLY` used to write the base's value and then the
  child's on every run. A stack never converged: each re-apply reported
  changes, and the live configuration held the base's value in between.
  - `APPLY` now drops a clause, or an entry of a list clause, whose key a
    later statement for the same target replaces outright. That covers a
    variable, a tool rule, the model, the agent, the status line and its
    refresh, a sandbox setting, and `USE ENV`.
  - The run and the plan name what was dropped and where it is set again,
    and `--dry-run --json` adds `overridden` to the statement (a new field).
  - A statement left with nothing to write is `unchanged`, so applying a
    stack again reports `0 changed`.

### Fixed

- **A played playbook's update failed when the recipe dropped both
  its status line and its refresh.** Its undo held `UNSET STATUSLINE` and
  `UNSET STATUSLINE REFRESH`, which one statement may not combine. The undo
  is now `UNSET STATUSLINE` alone, which removes the refresh too.

## [v4.0.0-rc2] -- 2026-10-03

### Changed

- **A playbook whose manifest cannot be read no longer takes the others
  down** (SPEC.md, *Unreadable manifests*). In rc1, one `.playbook` with a
  key cpb does not define made every command fail, every launcher
  included.
  - Statements that name another playbook run, and so do their launchers.
  - The unreadable playbook itself is refused with its read error (the file
    and the line), and so is its launcher.
  - Lists print what they can read, then one stderr line per playbook left
    out (`playbook "<name>" is left out: …`), and exit 1. `--json` keeps
    its shape. The lists are `SHOW PLAYBOOKS`, bare `cpb`, `SELECT`,
    `SHOW ENV(S)`, `SHOW SESSIONS` and `cpb auth status`. The TUI shows the
    rows, with the line in its status bar.
  - Refused while a manifest cannot be read, naming the file:
    - `SHOW CREATE ALL`;
    - `DROP ENV`;
    - every statement that claims a launcher name (`CREATE PLAYBOOK`,
      `RENAME TO`, `LAUNCHER`, `cpb play --keep`).
    `APPLY` refuses a file holding a claim or a `DROP ENV` before it writes
    anything.
  - A launcher name that no readable playbook claims is no longer reported
    as stale when the owner may be the unreadable playbook.

- **`install.sh` names a rate limit as one.** When GitHub refuses the
  latest-release lookup with 403 or 429, the message says it was
  rate-limited and to set `CPB_INSTALL_VERSION`. Before, it blamed the
  internet connection. Any other status is given as its HTTP code. A
  transfer that fails, even after a status, counts as no answer, and nothing
  is installed. A `GITHUB_TOKEN` authenticates the lookup on GitHub's API
  only: it is passed to curl outside its command line, and no redirect is
  followed. `CPB_INSTALL_API_BASE` points the lookup elsewhere, for tests.
- **The npx shim names a rate limit as one too.** It applies to its own
  latest-release lookup, both for an unpinned run and for the stand-in
  release when the package's version is not published yet. A 403 or 429
  asks for a `CPB_NPX_VERSION` pin, or a retry. Any other status is given
  as its code. `CPB_INSTALL_API_BASE` applies to the shim as well.

### Fixed

- **`cpb self-uninstall` with a manifest it could not read** said it would
  remove `0 playbook(s)` and then deleted the whole playbooks root. It now
  refuses and removes nothing. `--keep-data` and `--binary-only` still run.

## [v4.0.0-rc1] -- 2026-10-03

cpb v4 is one CLI with one grammar. It breaks v3 on purpose: names, files,
flags and JSON change, and v4 reads no v3 name. Nothing converts v3 state
inside the product. [SPEC.md](SPEC.md) describes v4 as it is.

### Upgrading from v3

- **Install v4 fresh.** A v3 binary does not update itself into v4: it asks
  for `claude-playbook-<os>-<arch>`, which v4 releases do not ship, and
  `cpb self-update` never crosses a major version on its own (#177).
- **v4 reads no v3 name.** An old key in a manifest or env set file is the
  generic `unknown key "<key>" in <file>:<line>`, and that playbook does not
  launch (#176). An `.env-profiles/` directory, its `.default` and the old
  launcher receipt are not read. The old `CLAUDE_*` environment variables are
  ignored. A sandbox created by v3 has no v4 marker and is refused: remove
  it, or launch with `--sandbox-fresh`.
- **A one-off cutover script converts one machine.** It lives outside the
  product, and the release notes link it. It refuses while a session is live,
  backs up the whole playbooks root first, renames the keys and files listed
  under *Renamed*, proves every file it rewrites by parsing it before and
  after, then repoints the launchers and checks the result with v4. It never
  touches `CLAUDE.md`, `settings.json`, history or credentials.
- **Playbook sources must ship v4 keys** (`launcher`, not `alias`) and declare
  `[update] migrate` if they have a migration (#173, #176).

### Removed

- **The pre-grammar commands** (#172): `create`, `link`, `delete`, `rename`,
  `alias`, `dealias`, `list`, `info`, `env`, `env-profile`. Use `CREATE`,
  `ALTER`, `DROP` and `SHOW` statements. `sessions` is `SHOW SESSIONS`.
- **`install`** (#173): use `CREATE PLAYBOOK <name> FROM <source>`, with
  `BRANCH`, `SUBDIR`, `LAUNCHER`, `NO LAUNCHER`, `SANDBOX` and `ISOLATED
  LOGIN`. The name is required.
- **`update` with no name**, which updated cpb itself: use `cpb self-update`
  (#173). `update --all` and `update --check` are gone; `update <name>
  --dry-run` replaces the check.
- **`RESUME`** (#175): resume with `cpb run <playbook> --resume <id>` or
  `<launcher> --resume <id>`; `SHOW SESSIONS` prints that command for each session, with a `cd` into its folder first. The
  TUI's recent-sessions view and its `R` key are gone with it.
- **The `claude-playbook` executable name** (#178): there is one executable,
  `cpb`, and no `claude-playbook` link or reserved launcher name.
- **The pilot profile coupling** (#170): `NO PILOT PROFILE`, the
  `pilot_profile` field, column and TUI row, and the APPLY warning
  `pilot_profile_third_party_endpoint`. A new playbook's `CLAUDE.md` imports
  nothing.
- **Status line panels and the status line host rule** (#170): `ADD PANEL`,
  `DROP PANEL`, the `PANELS` table, the `panels` field and the warning
  `statusline_held_by_host`. Existing `statusline.d` files stay on disk,
  unmanaged.
- **The OpenShell sandbox backend** (#179). OpenShell needs Landlock, and the
  kernel of the sandboxes the end-to-end suites run in does not provide it, so
  no suite can run it end to end. `sbx` is the only backend.
- **`--sbx`** (#174): use `--sandbox=sbx`.
- **A manifest's top-level `subdir`**, a config directory below the playbook
  root: a playbook's config is its root. A source manifest or an installed
  playbook that names one is refused as an unknown key. `SUBDIR`, the slice
  of a source recorded as `[source] subdir`, stays.

### Renamed

The executable, assets and flake (#178):

| v3 | v4 |
|---|---|
| `claude-playbook` (with `cpb` a link to it) | `cpb` |
| release assets `claude-playbook-<os>-<arch>` | `cpb-<os>-<arch>` |
| flake attribute `#claude-playbook` | `#cpb` |

Environment variables (#178). The old names are not read.

| v3 | v4 |
|---|---|
| `CLAUDE_PLAYBOOKS_DIR` | `CPB_PLAYBOOKS_DIR` |
| `CLAUDE_LAUNCHER_DIR` | `CPB_LAUNCHER_DIR` |
| `CLAUDE_LAUNCHER_RECEIPT` | `CPB_LAUNCHER_RECEIPT` |
| `CLAUDE_PLAYBOOKS_ISOLATE_AUTH` | `CPB_ISOLATED_LOGIN` |
| `CLAUDE_PLAYBOOKS_OAUTH_TOKEN_FILE` | `CPB_OAUTH_TOKEN_FILE` |
| `CLAUDE_CONFIG_DIR_OVERRIDE` | `CPB_CONFIG_DIR` |
| `CLAUDE_PLAYBOOK_UPDATE_REPO`, `CLAUDE_PLAYBOOK_UPDATE_API_BASE`, `CLAUDE_PLAYBOOK_UPDATE_DOWNLOAD_BASE` | `CPB_UPDATE_REPO`, `CPB_UPDATE_API_BASE`, `CPB_UPDATE_DOWNLOAD_BASE` |
| `CLAUDE_PLAYBOOK_TARGET`, `CLAUDE_PLAYBOOK_PATH` (for a migration) | `CPB_PLAYBOOK_NAME`, `CPB_PLAYBOOK_DIR` |
| `install.sh` `VERSION`, `INSTALL_DIR`, `DEFAULT_INSTALL_DIR`, `INSTALL_URL`, `REPO`, `ASSET_PREFIX`, `DOWNLOAD_BASE_URL` | `CPB_INSTALL_VERSION`, `CPB_INSTALL_DIR`, `CPB_INSTALL_DEFAULT_DIR`, `CPB_INSTALL_URL`, `CPB_INSTALL_REPO`, `CPB_INSTALL_ASSET_PREFIX`, `CPB_INSTALL_DOWNLOAD_BASE` |
| npx shim `CPB_VERSION` | `CPB_NPX_VERSION` |

Files and keys (#176, #178):

| v3 | v4 |
|---|---|
| `.playbook` `alias` | `launcher` |
| `.playbook` `isolate_auth` | `isolated_login` |
| `.playbook` `[env] profiles` | `[env] sets` |
| `.playbook` `[env] unset`, an env set's `unset` | `block` |
| `migrations/apply.sh`, run whenever present | `[update] migrate = "<path>"`, run only when declared (#173) |
| `<root>/.env-profiles/`, its `.default` | `<root>/.env-sets/`, its `.defaults` |
| `~/.local/state/claude-playbook/launchers` | `~/.local/state/cpb/launchers` |
| `<cache>/claude-playbook/registry.lock`, `<rc>.claude-playbook.lock` | `<cache>/cpb/registry.lock`, `<rc>.cpb.lock` |
| sandbox `~/.claude-playbook-sandbox`, `~/.claude-playbook-logins/` | `~/.cpb-sandbox`, `~/.cpb-logins/` |

`~/.claude-playbooks` stays the default playbooks root.

Grammar words and flags (#180):

| v3 | v4 |
|---|---|
| `ALIAS <name>`, `NO ALIAS` | `LAUNCHER <name>`, `NO LAUNCHER` |
| `CREATE` / `ALTER ENV … DESCRIBE '<text>'` | `DESCRIPTION '<text>'` (`DESCRIBE` stays the statement that lists a SELECT table's columns) |
| `ADD MCP SERVER … ENV K=V`, `ENV K FROM '<ref>'` | `VAR K=V`, `VAR K FROM '<ref>'` |
| `--env-profile NAME` | `--env-set NAME` |
| `--unset KEY` | `--block KEY` |
| "env profile" | "env set" |
| "alias", "command name" (meaning a launcher) | "launcher" |

JSON (#174, #175, #183):

| v3 | v4 |
|---|---|
| APPLY: one `warning` per statement | `warnings`, every warning with its own code; `summary.warnings` counts warnings |
| `auth status` modes `isolated`, `own-token`, `own-login` | `isolated-login`, `playbook-token`, `shared-login` with `token_blocked: true` |
| `auth status` `isolated` | `isolated_login` |
| `auth status` `expires_at`, `daemon_since` (local offset) | the same keys, in UTC |
| `auth status --claude` `loggedIn`, `subscriptionType`, `authMethod` | `logged_in`, `subscription_type`, `auth_method` |
| EXPLAIN `layer.kind` `DEFAULTS`, `ENV`, `PLAYBOOK` | `defaults`, `env`, `playbook` |
| SHOW and SELECT `sandbox` (a boolean) | an object: `always`, `backend`, `host`, `workdir`, `mounts`, `allow_net`, `secrets`, `claude_version`, `share_skills` |
| SHOW SESSIONS `resume`: `<launcher> --resume <id>` | `cd '<cwd>' && <launcher> --resume <id>` |

The APPLY JSON schema stays 1.

### Changed

- **Strict decoding** (#176): `.playbook`, env set files and cpb's own state
  refuse every key they do not define, naming the key and its line. A launch
  refuses over a manifest it cannot read.
- **`cpb self-update`** (#173, #177) is its own command. It installs the
  newest release of the running major version only; `--major` crosses one.
  Drafts and pre-releases never count, and it installs nothing when the
  release list cannot be read.
- **`cpb update <name>`** (#173) refetches a played recipe or a recorded
  source, and needs exactly one name. A migrate step runs only when declared,
  after a yes (`--yes` or a terminal), outside the registry lock, and only if
  its checksum still matches what `--dry-run` showed.
- **`cpb run` and the launchers guard resuming** (#175): a `--resume <id>`
  whose session is live, or ran in another folder, is refused, and so is a
  `--continue` whose newest session in this folder is live.
- **`SET STATUSLINE` always applies** (#170). `… IF UNSET` applies only where
  no status line is set.
- **`ALTER PLAYBOOK … SET SANDBOX` and `UNSET SANDBOX`** write the
  `[sandbox]` table, bare or by key (#174), and `SHOW CREATE` writes it back
  that way.
- **SHOW PLAYBOOK** has `description`, `homepage`, `author`, `last_used` and
  `migrate` (#172, #173).
- **Every hint and error names a statement**, never a removed command (#172).
- **SELECT prints one shape on both engines**: a table on a terminal, TSV
  with a header row in a pipe, JSON rows with `--json`. A query run through
  `clickhouse local` printed bare TSV in a pipe and ignored `--json`. A
  `FORMAT` in the query still wins, and cannot be combined with `--json`.
  ClickHouse's `JSON` type keeps only an object's non-null keys.
- **DESCRIBE has a `comment` column**: `name`, `type`, `comment`, one line
  saying what each column means, as ClickHouse's `DESC` has; SPEC.md lists
  the same lines. It prints in SELECT's three forms (in a pipe, TSV with a
  header row, where v3 printed the terminal table).
- **SHOW's `launcher` is the command you type**: the playbook's recorded
  `LAUNCHER`, or its name when the default launcher is in place; null under
  `NO LAUNCHER` (and when the default launcher is not in place). v3 reported
  only an alternate name. SHOW SESSIONS' resume line
  uses it. cpb no longer writes `version = "0.1.0"` into a manifest it
  creates.

### Added

- **`cpb play <ref>`** (#162-#165, #167, #169): try someone else's playbook
  from a template name, an https URL, a `github:` ref or a local file. It
  fetches the recipe once, previews what it would do, asks for a typed
  confirmation when it would change an endpoint, a proxy, TLS or a secret,
  runs it in a throwaway playbook (sandboxed where `sbx` is installed), and
  cleans up on every exit. `--keep` keeps it as a playbook that `cpb update`
  refreshes.
- **A sample secret helper**, `examples/secret-helper/cpb-secret-file`
  (#171).
- **Examples** 08 (an agent stacked from playbook files), 17 (`SET STATUSLINE
  … IF UNSET`), 20 (an `sbx` sandbox) and 21 (`cpb play`); 15 is rebuilt as a
  routed playbook with its own login (#169, #170, #171, #179).
- **[SPEC.md](SPEC.md)**, one specification by statement, replacing `SPEC-v4.md` and `docs/reference/cli-grammar.md` (#185).

### Not in this release

- **Pilot integration is deferred.** cpb names no pilot-profile component.
  Wiring a playbook to a pilot profile is the pilot's own step, `pilot wire`,
  a pilot-profile command that cpb never runs.
- **Statement completion** is planned; only playbook names complete.

## Earlier releases

The notes below record the v3 changes that [SPEC.md](SPEC.md) used to carry
in its body. Every v3 release has its own notes on the
[releases page](https://github.com/ramazanpolat/claude-playbooks/releases).

### v3.27.0

- A `github:` marketplace source takes a branch or tag (`#<ref>` or
  `@<ref>`). A `github:` ref that looks like a commit is refused; a git URL's
  commit-like `#<ref>` is still accepted, with the warning
  `marketplace_ref_not_cloneable`.

### v3.26.0

- Built-in `SELECT --json` keys follow the query's column order; before,
  they were sorted.
- A sandboxed launch refuses when a backend key cannot be registered as a
  proxy secret (#148); before, it warned and passed the key in as a plain
  value.

### v3.25.0

- `cpb tui`, read only, and a `cpb tui` hint at the end of bare `cpb` on a
  terminal.
- `SHOW SESSIONS` and the `SESSIONS` table, `tty` included.
- `SET STATUSLINE PREVIOUS` and the status line history.

### v3.23.1

- A shared launch sets another account's own login aside
  (`.credentials.json.cpb-own-<stamp>`); before, it was copied over the
  machine's login
  ([write-up](https://github.com/ramazanpolat/claude-playbooks/blob/v3.27.0/docs/known-issues/shared-launch-copies-own-login-over-machine-login.md)).

### v3.23.0

- `SET ISOLATED LOGIN`, `UNSET ISOLATED LOGIN` and `CREATE PLAYBOOK …
  ISOLATED LOGIN`; before, only `SANDBOX` or a hand edit set
  `isolated_login`.
- `SET STATUSLINE … REFRESH <n>` and `SET` / `UNSET STATUSLINE REFRESH`, with
  `statusline_refresh` in `SHOW PLAYBOOK --json`.

### v3.22.1

- A security fix: `CREATE PLAYBOOK … FROM` leaves a source's
  `.credentials.json` and account state out of the install, and `LINK` sets
  them aside. Before, the first credential sync copied a source's login over
  the machine's, so installing a source could switch the account
  ([write-up](https://github.com/ramazanpolat/claude-playbooks/blob/v3.27.0/docs/known-issues/shared-launch-copies-own-login-over-machine-login.md)).

### v3.22.0

- The model picker: `ADD` / `DROP MODEL`, `SET MODEL PICKER ONLY | APPEND`,
  `UNSET MODEL PICKER`, and `model_picker` in `SHOW PLAYBOOK --json`.
- `APPLY --dry-run --json`, schema 1.
- `DESCRIBE [TABLE]` / `DESC`, and `SELECT`'s own tables and row blocks on a
  terminal; a ClickHouse query's output in a pipe stays TSV.

### v3.21.0

- `ADD` / `DROP MCP SERVER`, `ALLOW` / `DENY` / `UNSET TOOL`, `SET` / `UNSET
  STATUSLINE`, `SET` / `UNSET MODEL` and `ADD` / `DROP SKILL`, with
  `mcp_servers` and `skills` in `SHOW PLAYBOOK --json`.
- Recipes (`ALTER PLAYBOOK` without a name), `USE PLAYBOOK`, and `APPLY … TO
  <playbook|dir>`, a plain config directory included.
- A git URL's `#<ref>` is compared url and ref apart, so applying the same
  `url#ref` again changes nothing.

### v3.20.0

- The statement grammar, a front end over the manifest and env set files,
  with secret references (`SET … FROM '<ref>'`, `[env.refs]`) and `AS
  PLAINTEXT`. A credential-looking literal needs `AS PLAINTEXT`; files that
  already held one keep working.
- `ADD` / `DROP MARKETPLACE`, `ADD` / `DROP PLUGIN`, `SET` / `UNSET AGENT`,
  with `marketplaces`, `plugins` and `agent` in `SHOW PLAYBOOK --json`.
- `INCLUDE`, a reserved word. `SHOW CREATE` quotes a name that is a keyword,
  and a quoted keyword is accepted as a new name.

### v3.15.0

- Credential-looking values are redacted in `SHOW`, `EXPLAIN` and the TUI.
- The missing-`claude` error comes after every input error; before, only a
  flag missing its value was reported ahead of it.

### v3.14.0

- `CPB_CONFIG_DIR` (then `CLAUDE_CONFIG_DIR_OVERRIDE`): a caller-supplied
  config directory for `run` and launcher dispatch.

### v3.13.0

- `--sandbox-host` and `[sandbox] host`: a sandboxed launch on another
  machine, over ssh.

### v3.12.1

- Backend API keys reach a sandbox as proxy-injected secrets; the sandbox
  sees a placeholder.
- Sandboxes are created without the shared skills store unless `share_skills
  = true`, and carry a marker; sandboxes from cpb 3.12.0 and earlier mounted
  the store and have none.

### v3.12.0

- `[sandbox] always`, `--no-sandbox`, `CREATE PLAYBOOK … SANDBOX`, and `start
  --sandbox`.

### v3.11.0

- `run --sandbox`: Docker Sandboxes through `sbx`.

### v3.10.1

- Launcher receipt lines carry two tab-separated fields after the path.

### v3.5.0

- A manifest may hold credential values under `[env.set]`, so a TOML error
  no longer echoes the parser's message.
