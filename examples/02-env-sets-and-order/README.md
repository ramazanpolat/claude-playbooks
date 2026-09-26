# 02 — env sets, in order

```
cpb APPLY playbook.cpb
cpb EXPLAIN PLAYBOOK work     # every variable a launch sets, and the layer that decided it
cpb run work
```

An env set is a named group of variables that several playbooks can share.
`USE ENV router thinking` attaches both, in order: a later set overrides an
earlier one. The playbook's own `SET VAR` overrides every set, so `work` runs
`glm-5.3-flash` while other users of `router` get `glm-5.3`. `BLOCK VAR` removes
a variable at launch even when your shell exports it. `ADD ENV x FIRST` or
`ADD ENV x AFTER router` places one set without restating the list.
