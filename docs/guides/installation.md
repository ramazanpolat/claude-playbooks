# Installation

Every way to get `cpb` onto a machine, and every way to take it off
again.

## Install script (recommended)

```bash
curl -fsSL https://raw.githubusercontent.com/ramazanpolat/claude-playbooks/main/install.sh | sh
```

The script detects your OS and architecture, downloads the right binary from the
latest GitHub Release, verifies it against the release's `SHA256SUMS`, and
installs it to `/usr/local/bin` (or `~/.local/bin` if that's not writable). Linux
and macOS, amd64/arm64 (no native Windows — WSL works).

Verify:

```bash
cpb --version
```

Want a different command name? Use a shell alias (`alias pb=cpb`) or
a hard link (`ln "$(command -v cpb)" ~/.local/bin/pb` — works for
both install locations). Do not use a symlink: a symlink to the binary under any
other name is treated as a playbook launcher and dispatched accordingly.

## Shell completions

The installer never edits your shell rc files. To enable completions (optional),
add one line to your rc file yourself:

```bash
echo 'source <(cpb completion zsh)'  >> ~/.zshrc     # zsh
echo 'source <(cpb completion bash)' >> ~/.bashrc    # bash
```

Each shell has a prerequisite the line cannot supply. Without it, the line
loads nothing and TAB fails:

- **zsh** needs its completion system started *before* that line. Frameworks
  (oh-my-zsh, prezto) already do it; a bare `~/.zshrc` does not, and the line
  then prints `command not found: compdef`. Put
  `autoload -U compinit && compinit` above it.
- **bash** needs **bash 4.2 or newer** and the **bash-completion** package
  (`apt install bash-completion`, `dnf install bash-completion`, or on macOS
  `brew install bash bash-completion@2`). Without the package, TAB prints
  `_get_comp_words_by_ref: command not found`. The bash that ships with macOS
  is 3.2, where `source <(...)` silently loads nothing.

  Installing the packages is not enough on macOS. Your terminal must actually
  run Homebrew's bash (`chsh -s "$(brew --prefix)/bin/bash"`, after adding that
  path to `/etc/shells`), and Homebrew does not load bash-completion for you.
  Add this above the completion line in `~/.bashrc` (under the stock bash 3.2
  it skips itself quietly):

  ```bash
  [[ -r "$(brew --prefix)/etc/profile.d/bash_completion.sh" ]] && . "$(brew --prefix)/etc/profile.d/bash_completion.sh"
  ```

  Linux packages load it themselves, from `/etc/bash.bashrc` or `/etc/profile.d`.

  macOS terminals start *login* shells, which read `~/.bash_profile`, not
  `~/.bashrc`. Keep both lines in `~/.bashrc` and have `~/.bash_profile` load it
  (`[ -r ~/.bashrc ] && . ~/.bashrc`). Homebrew's own hint suggests
  `~/.bash_profile` instead, but `self-uninstall` only cleans `~/.bashrc`.

Keep each line byte for byte as shown: `self-uninstall` removes only exact
matches of `source <(cpb completion bash)` and `source <(cpb completion zsh)`.
Any other form (an
absolute path, extra spaces, `eval "$(...)"`) outlives the binary and errors in
every new shell.

## With devbox or Nix

claude-playbooks is a Nix flake, so a [devbox](https://www.jetify.com/devbox)
project pins it like any other package:

```bash
devbox add "git+https://github.com/ramazanpolat/claude-playbooks?ref=refs/tags/v4.0.0-rc1#cpb"
devbox run -- cpb --version
```

### Using it in a devbox project

The usual shape keeps a project's playbooks **inside** the project and gives each
one a `devbox run` command. A `devbox.json` like this:

```json
{
  "packages": [
    "git+https://github.com/ramazanpolat/claude-playbooks?ref=refs/tags/v4.0.0-rc1#cpb",
    "claude-code@latest"
  ],
  "env": {
    "CPB_PLAYBOOKS_DIR": "$DEVBOX_PROJECT_ROOT/.playbooks"
  },
  "shell": {
    "scripts": {
      "myplaybook": "exec cpb run myplaybook \"$@\""
    }
  }
}
```

then:

```bash
devbox run -- cpb CREATE PLAYBOOK myplaybook FROM <git-url-or-local-dir>
devbox run myplaybook         # launch; arguments go to claude
```

- **`CPB_PLAYBOOKS_DIR`** puts installs in `.playbooks/` in the project, so
  nothing lands in `~/.claude-playbooks`. Add `.playbooks/` to `.gitignore`: it
  holds each playbook's own logins and history.
- **The script is the launcher.** cpb writes launcher commands only
  for its default folder, so a project-local playbook is launched with
  `devbox run <name>` (or `devbox run -- cpb run <name>`). The `"$@"` passes your
  arguments through to claude, and `exec` makes claude's exit code the command's.
- **`claude-code@latest`** brings Claude Code itself into the project; drop it to
  use the `claude` already on your PATH.
- **Commit `devbox.json` and `devbox.lock`**: the lock pins claude-playbooks to the
  tag's exact commit, and nixpkgs too (see the rate-limit note below).

### Without devbox

Flakes must be enabled, which a default Nix install does not do; the flag below
enables them for one command, or set `experimental-features = nix-command flakes`
in `nix.conf`:

```bash
nix --extra-experimental-features 'nix-command flakes' run "git+https://github.com/ramazanpolat/claude-playbooks?ref=refs/tags/v4.0.0-rc1#cpb" -- --version
nix --extra-experimental-features 'nix-command flakes' profile add "git+https://github.com/ramazanpolat/claude-playbooks?ref=refs/tags/v4.0.0-rc1#cpb"
```

(`nix profile add` is the current name; older Nix versions call it `nix profile install`.)
devbox needs none of this: it enables flakes itself.

### Notes

- **Use the `git+https:` form shown here.** The shorter
  `github:ramazanpolat/claude-playbooks/v4.0.0-rc1#cpb` also works, but it
  is resolved through GitHub's API, which rate-limits unauthenticated callers per
  IP: behind a shared public IP, `nix` and `devbox` alike fail with HTTP 403. Use
  it only with a GitHub token configured for Nix (`access-tokens`) or on a
  non-shared IP. `git+https` makes no API call, and devbox.lock pins the tag's
  exact commit. One caveat: a **brand-new** devbox project also resolves devbox's
  own `github:NixOS/nixpkgs` reference once, through that same API, so behind a
  shared IP its first `devbox add`/`devbox run` can still hit the rate limit.
  The dependable fixes are a committed `devbox.lock` (it pins nixpkgs, so no call
  is made at all) or a GitHub token for Nix (`access-tokens`). Waiting for the
  hourly reset is not: on a busy shared IP, other users can spend the whole
  60-call budget before devbox's lookup runs (seen three times in a row on one).
- **Pin a tag.** The flake builds from source at the ref you give; the version
  it reports is the last release's, so a commit between releases would claim a
  version it isn't.
- **The first install compiles.** There is no binary cache, so Nix fetches the
  Go toolchain and builds: measured 39 s cold on an 8-core Linux VM. On a 4-core
  host, a full devbox project install took 188 s with the flake against 73 s
  with a downloaded release binary, so the build adds about two minutes there.
  Later installs of the same ref are instant.
- **Update through devbox**, not with `cpb self-update`: the binary lives in
  the read-only Nix store, and `cpb self-update` refuses to touch it. **Replace** the
  entry -- a `devbox add` with a different tag does not replace the old one, it
  adds a second cpb package beside it. Either change the tag in
  `devbox.json` and run `devbox install`, or remove the old reference first:

  ```bash
  devbox rm "git+https://github.com/ramazanpolat/claude-playbooks?ref=refs/tags/<old-tag>#cpb"
  devbox add "git+https://github.com/ramazanpolat/claude-playbooks?ref=refs/tags/<new-tag>#cpb"
  ```

**Claude Code from Nix.** cpb itself does not need Claude Code, but the
playbooks it launches do, and the plugin clauses (`ADD PLUGIN`, `DROP
PLUGIN`) need Claude Code **2.1.268 or newer**: they run `claude plugin
install --json` and `claude plugin uninstall --json`. nixpkgs has carried older claude-code (2.1.245 at the time
of writing). On an older claude, a statement that installs or uninstalls a
plugin is refused in one line naming both versions, before any command
changes anything.
Install Claude Code from https://claude.ai/download, or pin a newer one.

## Run it with npx (no install needed)

On a machine with Node:

```bash
npx cpb-cli --version
```

(requires the package to be published to npm — see below if it is not).

Straight from the repo, no publish needed — but npm 12+ refuses git packages by
default (`EALLOWGIT`). Opt in once — the value is an enum (`all` / `none` /
`root`), and `root` is enough: it allows git only for packages you name
directly, never transitive dependencies:

```bash
npm config set allow-git root
npx github:ramazanpolat/claude-playbooks --version
```

One-off without touching config: prefix with `npm_config_allow_git=root`. Revert
with `npm config delete allow-git`. Remote-tarball URLs are blocked the same way,
so there is no URL form that works without this opt-in.

The first run bootstraps a normal install: it downloads the release binary,
verifies it against the release's `SHA256SUMS` (same policy as `install.sh`),
installs `cpb` to `~/.local/bin` only (never `/usr/local/bin`, no sudo), and says
what it did. After that the tool is a plain install — `cpb` and every playbook
launcher work directly, and later npx
invocations simply run the installed binary. Uninstall is the usual
`cpb self-uninstall`.

Knobs, for the cases where you do not want that:

| Variable | Effect |
|---|---|
| `CPB_NPX_BOOTSTRAP=0` | Ephemeral mode: install nothing, delegate to nothing — download to `${XDG_CACHE_HOME:-~/.cache}/cpb/npx/<tag>/` and run from there |
| `CPB_NPX_VERSION=v4.0.0` | Fetch a specific release. With `CPB_NPX_BOOTSTRAP=0` it tests a pinned version beside an installed one |
| `CPB_NPX_CACHE=<dir>` | Override the ephemeral-mode cache dir |
| `CPB_NPX_INSTALL_DIR=<dir>` | Override the bootstrap install dir |

By default the shim fetches the release matching the package's own version,
or the latest GitHub release when the package has none (`0.0.0`).

Right after a release-prep commit lands on main, the package names a release
that is not published yet. Until it is, the shim runs the newest published
release instead and says so in one line on stderr:

```
cpb v4.0.1 is not published yet; running v4.0.0
```

It only does this when that release's binary is missing (HTTP 404). It never
falls back to a prerelease (`-rcN`) or to another major version, and any other
download failure is an error. A version you pin yourself with `CPB_NPX_VERSION` is
never replaced: if it is missing, the shim stops. Native Windows is not supported (use
WSL); the npm package refuses to install there.

## From a clone

```bash
git clone https://github.com/ramazanpolat/claude-playbooks.git
cd claude-playbooks
./install.sh
```

## Build from source

Requires [Go](https://go.dev/dl/) 1.26+:

```bash
git clone https://github.com/ramazanpolat/claude-playbooks.git
cd claude-playbooks
./build.sh
mv cpb /usr/local/bin/
```

## Updating the tool

`self-update` updates the `cpb` binary to the newest GitHub
release of its major version:

```bash
cpb self-update            # the newest release of this major version
cpb self-update --check    # the newest of this major and the newest overall; installs nothing
cpb self-update --major    # allow a new major version (read its release notes first)
cpb self-update --force    # reinstall even if already on the newest
```

- **A new major version is never installed on its own.** When one is out,
  `self-update` says so in one line and stays on your major version:
  ``v5.0.0 is available, a new major version: run `cpb self-update --major` (read its release notes first)``.
- **Pre-releases are never installed**, and neither is an older version.
- **If the release list cannot be read** (a rate limit, no network), it says
  why and installs nothing. Set `GITHUB_TOKEN` to raise GitHub's rate limit.

It downloads the release asset for your OS/architecture, verifies it, and
atomically replaces the running binary (resolving the `cpb` symlink so the real
binary is updated). If the install directory needs elevated privileges to write,
it says so. A binary installed through devbox or Nix is never replaced:
`self-update` refuses and tells you to change the tag in devbox instead (see
[With devbox or Nix](#with-devbox-or-nix)).

To update a *playbook* rather than the tool, see
[Managing playbooks](managing-playbooks.md#update).

## Uninstalling

Remove only the binary:

```bash
curl -fsSL https://raw.githubusercontent.com/ramazanpolat/claude-playbooks/main/uninstall.sh | sh
```

Or run the local uninstaller from a clone:

```bash
./uninstall.sh
```

The script delegates to `cpb self-uninstall --binary-only`, so one implementation
owns all cleanup: the binary, its `cpb` sibling, launcher symlinks, and any
completion lines you added. Every launcher the tool creates is recorded in a
registry (`~/.local/state/cpb/launchers`), so launchers are removed
wherever they were created — including custom `--launcher-dir` locations — while
a link you renamed or repointed yourself is left alone. Playbooks are untouched,
and `~/.claude-playbooks` is never deleted.

### Remove everything

To remove the tool, all its installed playbooks, their launcher commands, the
completion lines in your rc files, and the binary in one step:

```bash
cpb self-uninstall          # prompts for confirmation
cpb self-uninstall -y       # skip prompt
cpb self-uninstall -y --keep-data     # keep ~/.claude-playbooks
cpb self-uninstall -y --keep-binary   # keep the binary
cpb self-uninstall --dry-run          # preview without removing
```

If the binary can't be removed (e.g. installed to `/usr/local/bin` and you're not
root), the command prints a `sudo rm <path>` hint and continues cleaning up
everything else.

### Manual fallback

If you can't run the binary:

```bash
# 1. Remove launcher symlinks pointing at the binary
#    (in the binary's directory and ~/.local/bin: ls -l | grep cpb)
# 2. Remove any `source <(cpb completion ...)` lines from your
#    shell config (~/.zshrc or ~/.bashrc)
# 3. rm -rf ~/.claude-playbooks
# 4. sudo rm /usr/local/bin/cpb   # or wherever the binary lives
```
