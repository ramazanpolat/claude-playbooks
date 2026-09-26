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
cpb CREATE PLAYBOOK sre SANDBOX       # [sandbox] always = true, isolate_auth = true
cpb install <source> --sandbox        # same, for an installed playbook
sre -p "run the tests"                # sandboxed, no flag needed
sre --no-sandbox                      # this launch on the host; cpb says so on stderr
cpb start --sandbox --delete /tmp/x   # a throwaway session in a throwaway sandbox
```

`--no-sandbox` is the only override, and it is never silent.

## On another machine

The sandbox can live on another machine. `cpb run --sandbox-host polat@cockpit0
sre` runs the same launch there over ssh, where `cpb` and the playbook are
installed and `sbx` is logged in (a Linux host with a headless keyring; a Mac
keeps the sbx login in its Keychain, which an ssh session cannot open);
`[sandbox] host = "polat@cockpit0"` in the manifest makes it the playbook's home
for sandboxed launches. Everything about the sandbox, its login and its keys then
lives on that host.

## Secrets

API keys from your env profiles never enter the sandbox either (profiles are the
place for them: a key set directly on the playbook lives in its `.playbook`,
which is on the mount, and a sandboxed launch refuses that). `cpb` registers them
with `sbx` as proxy-injected secrets for the endpoint host and hands the sandbox a
placeholder; the host-side proxy swaps the real key into the request headers on
the way out, and only for that host. Rotate the key in the profile and the next
launch updates it; remove it and the next launch revokes the mapping. A router on
this machine works too: a base URL at `localhost` is rewritten to the sandbox's
name for the host, and the policy and secret are registered the way the proxy
matches them. The shared `sbx` skills store stays out as well. `--sbx` is a
synonym for `--sandbox`; `--sandbox=BACKEND` picks the backend, of which there is
one today, `sbx`.

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

A playbook can describe its sandbox in the manifest:

```toml
[sandbox]
workdir = "~/proj"                  # default --workdir
mounts = ["~/shared-libs:ro"]       # extra host paths, :ro for read-only
allow_net = ["internal.corp"]       # hosts allowed beyond the policy
claude_version = "2.1.263"          # pin the Claude Code installed inside
always = true                       # every launch sandboxed; --no-sandbox overrides one
host = "polat@cockpit0"             # sandboxed launches run on that machine over ssh
secrets = "env"                     # pass API keys as plain variables instead of proxy injection
share_skills = true                 # mount sbx's shared skills store after all
```

The block is install-local: `cpb install` never adopts one shipped by a source,
and `cpb update` keeps yours.

`claude_version` matters for a playbook routed to a third-party backend that
rejects a newer Claude Code's tool schemas: the sandbox keeps running the last
version that works while the host moves on.
