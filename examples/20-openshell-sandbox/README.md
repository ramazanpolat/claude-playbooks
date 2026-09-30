# 20: a playbook in an OpenShell sandbox (experimental, Linux)

```
cpb APPLY playbook.cpb --dry-run
cpb APPLY playbook.cpb
cpb run --sandbox=openshell box                       # this folder is the workdir
cpb run --sandbox=openshell --mount ~/notes:ro box    # one more directory, read-only
```

`--sandbox=openshell` runs Claude Code in an
[NVIDIA OpenShell](https://github.com/NVIDIA/OpenShell) sandbox instead of a
Docker Sandbox. It runs on a Linux host with Docker Engine 28+ and OpenShell
0.1.x, set up once as the
[sandbox guide](../../docs/guides/sandbox.md#openshell-backend-experimental-linux)
shows (telemetry off, host mounts allowed, linger).

What happens on the first launch:

- `cpb` builds the image once, with Claude Code pinned, and creates the
  sandbox `cpb-box`. It mounts this folder and the playbook's directory at
  their own paths, and nothing else of the host.
- The router at `localhost:20128` is reached as `host.openshell.internal:20128`,
  and that host is the only one allowed besides Claude Code's own.
- An API key the `router` env set carries (`ANTHROPIC_AUTH_TOKEN` or
  `ANTHROPIC_API_KEY`) stays outside: OpenShell injects it into requests to
  that endpoint only, and the sandbox sees a placeholder.
- When the session ends, the sandbox is stopped, because an idle one costs about
  a third of a CPU core. The next launch starts it again, with its state kept.

Without OpenShell (on macOS, or on a host without it), the launch refuses
before it touches anything and says what is missing. That refusal is what CI
checks here.

Reference: SPEC-v4.md, "OpenShell backend (experimental, v3.26.0)".
