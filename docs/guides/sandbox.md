# Sandboxed sessions

`cpb` isolates a playbook's config directory and its environment. `--sandbox`
adds the third boundary: the process itself. The playbook's Claude Code runs
inside a [Docker Sandbox](https://docs.docker.com/ai/sandboxes/), a microVM with
its own kernel, filesystem and network stack, and sees only two host directories:
the directory you are working on and the playbook's own directory. Your
`~/.claude`, the rest of your home, your shell environment and your other
playbooks are not there.

```bash
brew trust docker/tap && brew install docker/tap/sbx && sbx login   # once, macOS
cpb run --sandbox sre                                     # current directory is the workdir
cpb run --sandbox --workdir ~/proj sre -p "run the tests"
cpb run --sandbox --sandbox-fresh --clone --workdir ~/untrusted-repo sre  # new sandbox on a private clone; host tree untouched
cpb run --sandbox --mount ~/shared-libs:ro sre            # one more directory, read-only
cpb run --sandbox --sandbox-fresh sre                     # throw the sandbox away and start over
```

## Your machine login stays outside

`~/.claude` is exactly what stays outside: a working directory or mount that
contains it (your home directory, say) is refused. A playbook that shares it runs
with a login of its own inside: `cpb` says so on the first sandboxed launch, and
one `/login` there gives the sandbox its own grant, kept inside the sandbox (gone
with `--sandbox-fresh`, never written to your machine). A token from an env
profile works as it does on the host.

## Always-on

To make a playbook sandboxed every time, say so once:

```bash
cpb CREATE PLAYBOOK sre SANDBOX       # [sandbox] always = true, isolated_login = true
cpb CREATE PLAYBOOK ops FROM <src> SANDBOX   # the same, for one from a source
cpb ALTER PLAYBOOK dev SET SANDBOX    # the same, for a playbook you have
sre -p "run the tests"                # sandboxed, no flag needed
sre --no-sandbox                      # this launch on the host; cpb says so on stderr
cpb ALTER PLAYBOOK sre UNSET SANDBOX  # back on the host; the login stays isolated
cpb start --sandbox --delete /tmp/x   # a throwaway session in a throwaway sandbox
```

`--no-sandbox` is the only override, and it is never silent.

## On another machine

The sandbox can live on another machine. `cpb run --sandbox-host me@buildbox
sre` runs the same launch there over ssh, where `cpb` and the playbook are
installed and `sbx` is logged in (a Linux host with a headless keyring; a Mac
keeps the sbx login in its Keychain, which an ssh session cannot open);
`[sandbox] host = "me@buildbox"` in the manifest makes it the playbook's home
for sandboxed launches. Everything about the sandbox, its login and its keys then
lives on that host.

## Secrets

API keys from your env sets never enter the sandbox either (profiles are the
place for them: a key set directly on the playbook lives in its `.playbook`,
which is on the mount, and a sandboxed launch refuses that). `cpb` registers them
with `sbx` as proxy-injected secrets for the endpoint host and hands the sandbox a
placeholder; the host-side proxy swaps the real key into the request headers on
the way out, and only for that host. Rotate the key in the profile and the next
launch updates it; remove it and the next launch revokes the mapping. A router on
this machine works too: a base URL at `localhost` is rewritten to the sandbox's
name for the host, and the policy and secret are registered the way the proxy
matches them.

When a key does go in:

- **`[sandbox] secrets = "env"`** passes keys in as plain variables. It is the
  only way to do that, and a choice you make in the manifest.
- **A Claude Code login token** (`CLAUDE_CODE_OAUTH_TOKEN`), when the launch
  uses one, goes in: Claude Code checks its shape locally, so a placeholder
  would not work.
- **Never because a registration failed.** If `cpb` cannot register a key with
  the proxy, the launch stops and names the key and the host (never the
  value). Retry, or choose `secrets = "env"`.

The shared `sbx` skills store stays out as well.

## Lifetime and environment

The sandbox is named `cpb-<playbook>` and reused across launches, so tools the
agent installs and its own state persist until `--sandbox-fresh` or `sbx rm`.
`--clone` counts only when the sandbox is created: to move an existing sandbox to
clone mode, recreate it with `--sandbox-fresh`. The environment is the same one an
ordinary launch computes (default profile, profiles, the playbook's block, one-off
flags, the authentication decision), reduced to the variables those layers set
plus the token and `CLAUDE_CONFIG_DIR`; nothing else of your shell reaches the
sandbox. Network egress follows your `sbx` policy (balanced by default: model
APIs, package managers, code hosts), widened per sandbox by the manifest and by
the host of `ANTHROPIC_BASE_URL` when the playbook is routed elsewhere.

## Manifest block

A playbook describes its sandbox in the `[sandbox]` table of its manifest.
`SET SANDBOX <key>=<value>` writes it key by key and `UNSET SANDBOX <key>`
forgets one, so you never edit the file:

```bash
cpb ALTER PLAYBOOK dev SET SANDBOX host=me@buildbox mounts=~/shared-libs:ro allow_net=internal.corp
cpb ALTER PLAYBOOK dev UNSET SANDBOX host
cpb SHOW PLAYBOOK dev                 # Sandbox: yes (mounts=~/shared-libs:ro, allow_net=internal.corp)
```

The keys, as the table holds them:

```toml
[sandbox]
workdir = "~/proj"                  # default --workdir
mounts = ["~/shared-libs:ro"]       # extra host paths, :ro for read-only
allow_net = ["internal.corp"]       # hosts allowed beyond the policy
claude_version = "2.1.263"          # pin the Claude Code installed inside
always = true                       # every launch sandboxed; --no-sandbox overrides one
host = "me@buildbox"             # sandboxed launches run on that machine over ssh
secrets = "env"                     # pass API keys as plain variables instead of proxy injection
share_skills = true                 # mount sbx's shared skills store after all
```

The block is install-local: `CREATE PLAYBOOK … FROM` never adopts one shipped
by a source, and `cpb update` keeps yours. `SHOW CREATE` writes it back as `SET
SANDBOX` statements. See [Sandbox](../../SPEC.md#sandbox) in
the reference.

`claude_version` matters for a playbook routed to a third-party backend that
rejects a newer Claude Code's tool schemas: the sandbox keeps running the last
version that works while the host moves on.
