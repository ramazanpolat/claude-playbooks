# Installation

Every way to get `claude-playbook` onto a machine, and every way to take it off
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
claude-playbook --version
```

The installer also creates a `cpb` symlink — a shorter name for the same binary:

```bash
cpb --version
```

The docs use `cpb` throughout; `claude-playbook` works everywhere `cpb` appears.

Want a different command name? Use a shell alias (`alias pb=claude-playbook`) or
a hard link (`ln "$(command -v claude-playbook)" ~/.local/bin/pb` — works for
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
- **bash** needs **bash 4 or newer** and the **bash-completion** package
  (`apt install bash-completion`, `dnf install bash-completion`, or on macOS
  `brew install bash bash-completion@2`). Without the package, TAB prints
  `_get_comp_words_by_ref: command not found`. The bash that ships with macOS
  is 3.2, where `source <(...)` silently loads nothing.

  Installing the package is not enough on macOS: Homebrew does not load it
  for you. Add this above the completion line in `~/.bashrc`:

  ```bash
  [[ -r "$(brew --prefix)/etc/profile.d/bash_completion.sh" ]] && . "$(brew --prefix)/etc/profile.d/bash_completion.sh"
  ```

  Linux packages load it themselves, from `/etc/bash.bashrc` or `/etc/profile.d`.

Keep the line in exactly this form: `self-uninstall` finds and removes these
`source <(... completion ...)` lines, so a rewritten one would outlive the
binary and error in every new shell.

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
installs to `~/.local/bin` only (never `/usr/local/bin`, no sudo), creates the
`cpb` link, and says what it did. After that the tool is a plain install — `cpb`,
`claude-playbook`, and every playbook launcher work directly, and later npx
invocations simply run the installed binary. Uninstall is the usual
`cpb self-uninstall`.

Knobs, for the cases where you do not want that:

| Variable | Effect |
|---|---|
| `CPB_NPX_BOOTSTRAP=0` | Ephemeral mode: install nothing, delegate to nothing — download to `~/.claude-playbooks/bin/<tag>/` and run from there |
| `CPB_VERSION=v3.9.1` | Fetch a specific release. With `CPB_NPX_BOOTSTRAP=0` it tests a pinned version beside an installed one |
| `CPB_NPX_CACHE=<dir>` | Override the ephemeral-mode cache dir |
| `CPB_NPX_INSTALL_DIR=<dir>` | Override the bootstrap install dir |

By default the shim fetches the release matching the package's own version,
falling back to the latest GitHub release. Native Windows is not supported (use
WSL); the npm package refuses to install there.

## From a clone

```bash
git clone https://github.com/ramazanpolat/claude-playbooks.git
cd claude-playbooks
./install.sh
```

## Build from source

Requires [Go](https://go.dev/dl/) 1.21+:

```bash
git clone https://github.com/ramazanpolat/claude-playbooks.git
cd claude-playbooks
./build.sh
mv claude-playbook /usr/local/bin/
```

## Updating the tool

With **no** playbook name, `update` self-updates the `claude-playbook` binary to
the latest GitHub release:

```bash
cpb update            # download + install the latest release
cpb update --check    # report the latest version without installing
cpb update --force    # reinstall even if already on the latest
```

It downloads the release asset for your OS/architecture, verifies it, and
atomically replaces the running binary (resolving the `cpb` symlink so the real
binary is updated). If the install directory needs elevated privileges to write,
it says so.

To update a *playbook* rather than the tool, see
[Managing playbooks](playbooks.md#update).

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
registry (`~/.local/state/claude-playbook/launchers`), so launchers are removed
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
#    (in the binary's directory and ~/.local/bin: ls -l | grep claude-playbook)
# 2. Remove any `source <(claude-playbook completion ...)` lines from your
#    shell config (~/.zshrc or ~/.bashrc)
# 3. rm -rf ~/.claude-playbooks
# 4. sudo rm /usr/local/bin/claude-playbook   # or wherever the binary lives
```
