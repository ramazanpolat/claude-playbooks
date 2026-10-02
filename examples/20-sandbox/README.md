# 20: a playbook that always runs in a sandbox

```
cpb APPLY playbook.cpb --dry-run
cpb APPLY playbook.cpb
cpb run box                           # every launch is sandboxed
cpb run --no-sandbox box              # this launch on your machine, said loudly
cpb SHOW CREATE PLAYBOOK box          # the [sandbox] settings, as statements
```

`SET SANDBOX` on its own makes every launch of `box` run in a
[Docker Sandbox](https://docs.docker.com/ai/sandboxes/) (`sbx`), and isolates
its login: the sandbox never sees your machine's login. The keyed form,
`SET SANDBOX <key>=<value> ...`, sets the playbook's `[sandbox]` table:

- `backend=sbx` names the backend (sbx is the one there is);
- `allow_net=api.github.com` lets the sandbox reach one more host;
- `mounts=~/shared-libs:ro` mounts one more directory, read-only, at its own
  path.

`UNSET SANDBOX` turns "always" off again (the login stays isolated), and
`UNSET SANDBOX <key>` forgets one setting. The settings live in the
playbook's `.playbook`, and `SHOW CREATE` writes them back as statements.

A launch needs `sbx` installed and logged in (`sbx login` once). Without it,
the launch refuses before it touches anything and says what to install. That
refusal is what CI checks here.

Guide: [Sandboxed sessions](../../docs/guides/sandbox.md).
