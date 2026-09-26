# 06 — a playbook from Git

```
cpb APPLY playbook.cpb     # clones the tag, installs it as `kommander`, launcher `k`
k                          # run it
cpb update kommander       # later: take the source's new version
```

`CREATE PLAYBOOK … FROM` is the statement form of `cpb install <url>`, which
stays as a shortcut. The source is recorded, so applying the file again is a
no-op, and a file that names a different source for an existing playbook gets
a warning, never a silent re-install. `LINK <dir>` instead of `FROM` develops a
playbook in place from your own checkout.
