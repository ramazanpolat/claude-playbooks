# Documentation

| | |
|---|---|
| [Installation](installation.md) | install script, npx, source builds, uninstalling |
| [Managing playbooks](playbooks.md) | create, install, link, launch, rename, update, delete |
| [Authentication](authentication.md) | shared logins, long-lived tokens, isolated accounts |
| [Environment overrides](environment.md) | per-playbook variables and shared env profiles |
| [Sandboxed sessions](sandbox.md) | running a playbook inside a Docker Sandbox microVM |
| [Agent guide](AGENT-GUIDE.md) | driving `cpb` unattended from an agent or CI |

The behavioral contract is [`SPEC-v4.md`](../SPEC-v4.md) in the repository root;
when a document here and the spec disagree, the spec wins. Development and
release process live in [`CONTRIBUTING.md`](../CONTRIBUTING.md).
