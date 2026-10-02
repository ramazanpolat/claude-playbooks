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

The sandbox can live on another machine. `cpb run --sandbox-host me@buildbox
sre` runs the same launch there over ssh, where `cpb` and the playbook are
installed and `sbx` is logged in (a Linux host with a headless keyring; a Mac
keeps the sbx login in its Keychain, which an ssh session cannot open);
`[sandbox] host = "me@buildbox"` in the manifest makes it the playbook's home
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
matches them.

When a key does go in:

- **`[sandbox] secrets = "env"`** passes keys in as plain variables. It is the
  only way to do that, and a choice you make in the manifest.
- **A Claude Code login token** (`CLAUDE_CODE_OAUTH_TOKEN`), when the launch
  uses one, goes in: Claude Code checks its shape locally, so a placeholder
  would not work.
- **Never because a registration failed.** If `cpb` cannot register a key with
  the proxy, the launch stops and names the key and the host (never the
  value). Retry, or choose `secrets = "env"`. Before v3.26.0 it warned and
  passed the key in.

The shared `sbx` skills store stays out as well. `--sbx` is a
synonym for `--sandbox`; `--sandbox=BACKEND` picks the backend: `sbx`, the
default, or `openshell` on Linux
([below](#openshell-backend-linux)).

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
host = "me@buildbox"             # sandboxed launches run on that machine over ssh
secrets = "env"                     # pass API keys as plain variables instead of proxy injection
share_skills = true                 # mount sbx's shared skills store after all
```

The block is install-local: `cpb install` never adopts one shipped by a source,
and `cpb update` keeps yours.

`claude_version` matters for a playbook routed to a third-party backend that
rejects a newer Claude Code's tool schemas: the sandbox keeps running the last
version that works while the host moves on.

## OpenShell backend (Linux)

`--sandbox=openshell` runs the session in [NVIDIA OpenShell](https://github.com/NVIDIA/OpenShell)
instead of `sbx`: a container confined by Landlock and seccomp, with no network
unless a rule allows it. It is for a Linux host with Docker Engine. On macOS use
`sbx`, or `--sandbox-host` to a Linux machine. `cpb` supports OpenShell 0.1.2
and any later 0.1.x.

```bash
cpb run --sandbox=openshell sre                          # this folder is the workdir
cpb run --sandbox=openshell --mount ~/shared-libs:ro sre # one more directory, read-only
cpb run --sandbox=openshell --sandbox-fresh sre          # throw the sandbox away first
```

### One-time host setup

Run these once on the host, in order:

```bash
# 1. Telemetry off, before the gateway ever starts (OpenShell has no install-time switch).
mkdir -p ~/.config/openshell
echo OPENSHELL_TELEMETRY_ENABLED=false > ~/.config/openshell/gateway.env

# 2. The gateway uses Docker, and lets sandboxes mount host directories.
cat > ~/.config/openshell/gateway.toml <<'EOF'
version = 2
[openshell.gateway]
compute_driver = "docker"
[openshell.drivers.docker]
allow_driver_config = true
enable_bind_mounts = true
[openshell.drivers.docker.resource_admission]
enabled = false
EOF

# 3. Install OpenShell (Docker Engine 28+ must be installed already).
curl -LsSf https://raw.githubusercontent.com/NVIDIA/OpenShell/v0.1.2/install.sh | OPENSHELL_VERSION=v0.1.2 sh
openshell status                      # Connected. On a cold host the installer may
                                      # report a timeout although the install worked.

# 4. Keep the gateway running after you log out.
sudo loginctl enable-linger "$USER"
```

Step 2 applies to the whole gateway, so every sandbox on it can ask for host
mounts, not only `cpb`'s. OpenShell's docs warn that host mounts can bypass its
workspace isolation. `cpb` keeps each sandbox to the paths it mounted by listing
exactly those paths in the sandbox's filesystem policy.

Before touching anything, every launch checks the requirements: Linux, the
`openshell` CLI and its version, Docker Engine 28 or newer, a gateway that
answers, and not running as root. A failure refuses the launch with one line that
names the fix. A gateway that is starting gets up to 45 s. Linger off is only a
warning.

### How it differs from sbx

| | `sbx` | `openshell` |
|---|---|---|
| Network | your sbx policy (balanced by default) | nothing by default; `cpb` allows Claude Code's own hosts for the `claude` binary, the endpoint host, and `allow_net` |
| Files | the mounts | the mounts, and only the paths the sandbox's policy lists (Landlock, required) |
| Secrets | a placeholder the proxy swaps | the same, bound to one endpoint: sent anywhere else, the proxy refuses it (`403 credential_endpoint_mismatch`) |
| Between launches | kept running | **stopped** when the last session in it ends, started by the next launch (about 5 s) |
| Image | sbx's | built by `cpb` on first use, with Claude Code pinned |
| `--clone`, `share_skills` | yes | not yet: the launch refuses |
| macOS | yes | no |

The sandbox is stopped between launches because an idle OpenShell sandbox costs
about a third of a CPU core. A stopped one costs nothing and keeps its state,
including a `/login` made inside it.

### The image

`cpb` builds the image once per Claude Code version, on the gateway host, from a
recipe built into the `cpb` binary. The first build takes several minutes. The base image is pinned by digest, and
Claude Code is pinned to the version `cpb` was tested with (2.1.285), or to
`[sandbox] claude_version`. The version is fixed per sandbox:

- **You set `claude_version` to one the sandbox does not carry:** the launch
  refuses, and `--sandbox-fresh` rebuilds it.
- **A newer `cpb` moves the default:** the old sandbox keeps running, and `cpb`
  tells you `--sandbox-fresh` moves it.

Claude Code's self-updater is off inside, since the image is the pin. Old images
stay until you remove them (`docker image prune`, or
`docker image rm cpb-openshell/claude:<tag>`).

### Secrets

Each key (`ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_API_KEY`) becomes an OpenShell
provider named `cpb-<playbook>-<key>`, bound to the endpoint's host and port.
`cpb` hands the value to `openshell` in its environment, never on a command line.
Inside the sandbox, the variable holds OpenShell's own placeholder:

- **Rotation:** change the key in its env set, and the next launch updates it in place.
- **Revoke:** remove it, and the next launch detaches and deletes the provider.
- **`--sandbox-fresh`:** deletes them with the sandbox.
- **Failure:** a key that cannot be registered stops the launch, as with `sbx`.

### Removing it

Remove the sandboxes first (`openshell sandbox delete --all`, and
`openshell provider list` shows any `cpb-…` providers left), then follow
OpenShell's own uninstall steps, and remove `~/.config/openshell` too.
