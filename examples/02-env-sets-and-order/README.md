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
`REPLACE ENV router WITH fast` swaps one set for another at the same place;
run again, it changes nothing. A recipe states `USE ENV`; `REPLACE` is for the
prompt, and `SHOW CREATE` never writes it.

`router` holds the router's own key by reference, resolved at launch by the
sample secret helper (`.setup` stores a placeholder; see
[04](../04-secret-references/)). A route to another provider with no key of
its own is refused at launch: Claude Code would send your Claude login there.
