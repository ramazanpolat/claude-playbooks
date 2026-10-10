# 20: a playbook that always runs in a sandbox

```
cpb APPLY playbook.cpb --dry-run
cpb APPLY playbook.cpb
cpb run box                           # every launch is sandboxed
cpb run --no-sandbox box              # this launch on your machine, said loudly
cpb SHOW CREATE PLAYBOOK box          # the [sandbox] settings, as statements
```

`sandbox.always = true` makes every launch of `box` run in a
[Docker Sandbox](https://docs.docker.com/ai/sandboxes/) (`sbx`). A sandbox
never sees your machine's login, so the playbook's login is isolated too:
`login = 'isolated'` in the same `SET` (a sandbox never isolates it on its
own). The other `sandbox.<key>` properties set the playbook's `[sandbox]`
table:

- `sandbox.backend = 'sbx'` names the backend (sbx is the one there is);
- `sandbox.allow_net = ['api.github.com']` lets the sandbox reach one more
  host;
- `sandbox.mounts = ['~/shared-libs:ro']` mounts one more directory,
  read-only, at its own path.

On the command line, quote a statement with a list in it: zsh reads `[ ]` as
a pattern (`cpb "ALTER PLAYBOOK box SET sandbox.mounts = ['~/a:ro']"`).

`SET sandbox.always = false` turns "always" off again (the login stays
isolated), `DELETE sandbox.<key>` forgets one setting, and `DELETE sandbox`
the whole table. The settings live in the playbook's `.playbook`, and `SHOW
CREATE` writes them back as statements.

A launch needs `sbx` installed and logged in (`sbx login` once). Without it,
the launch refuses before it touches anything and says what to install. That
refusal is what CI checks here.

Guide: [Sandboxed sessions](../../docs/guides/sandbox.md).
