# 06 — a playbook from Git

```
cpb APPLY playbook.cpb     # clones the tag, installs playbooks/dba as `dba`, launcher `dba`
dba                        # run it
cpb update dba             # later: refresh from the source (a tag stays that tag)
```

`BRANCH v1.0.0` pins a tag: `update` refreshes that tag and never moves to
a later release. Name a branch (or leave `BRANCH` out) to follow new versions.
`CREATE PLAYBOOK … FROM` is the statement form of `cpb install <url>`, which
stays as a shortcut. `SUBDIR` installs one playbook out of a repository that
ships several. The source is recorded, so applying the file again is a no-op,
and a file that names a different source for an existing playbook gets a
warning, never a silent re-install. `LINK <dir>` instead of `FROM` develops a
playbook in place from your own checkout.
