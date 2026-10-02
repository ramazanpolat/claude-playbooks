# History

Notes kept for the record. They describe cpb as it was when they were
written, and nothing here is maintained or current: for how cpb works today,
read the [reference](../../SPEC.md) and the [guides](../guides/).

| Note | What it records |
|---|---|
| [pilot-profile-integration.md](pilot-profile-integration.md) | how cpb once wrote another tool's imports into every new playbook's `CLAUDE.md`, and why that coupling was removed |
| [bg-command-loses-kommander-task-lock.md](bg-command-loses-kommander-task-lock.md) | a bug in a playbook built on cpb, investigated here before it moved to its own repository |
| [env-status-prints-secret-values.md](env-status-prints-secret-values.md) | a secret leak in the removed `env` command's status output; v4 has no such command, and SHOW and EXPLAIN never print a value |
