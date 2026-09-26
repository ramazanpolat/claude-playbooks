# 07 — a plugin and an agent

```
cpb APPLY playbook.cpb --dry-run   # the `claude plugin` commands it would run
cpb APPLY playbook.cpb
cpb EXPLAIN PLAYBOOK greeter       # Plugins: greet@hello / Agent: greeter (playbook settings)
cpb run greeter -p "hi"            # "Hello from greeter. …"
```

`ADD MARKETPLACE` and `ADD PLUGIN` run Claude Code's own `claude plugin`
commands with the playbook as `CLAUDE_CONFIG_DIR`, so the plugin is installed
for this playbook only. cpb reads the state first: applying again runs
nothing. `'./hello-marketplace'` resolves against this file's directory. A
marketplace can also come from `'github:<owner>/<repo>'` or a git URL.
`SET AGENT` pins the main-thread agent. A plugin that runs a command its
marketplace declares is never accepted for you: the statement fails and shows
the command to review and confirm by hand.
Example 08 stacks this into layers.
