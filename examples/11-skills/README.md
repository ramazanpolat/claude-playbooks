# 11 — skills

```
cpb APPLY playbook.cpb --dry-run    # link skills/release-notes to ./release-notes
cpb APPLY playbook.cpb
cpb SHOW CREATE PLAYBOOK writer     # … ADD SKILL release-notes FROM '<absolute path>'
```

`ADD SKILL <name> FROM <source>` puts a skill directory (one that holds
`SKILL.md`) at `<playbook>/skills/<name>`. How depends on the source:

- **A directory is linked**: `'./dir'` in a playbook file (resolved against
  the file's directory), `'/abs/dir'` or `'~/dir'`. It is a skill you are
  working on, so edits reach the next session without another `APPLY`.
- **A git source is copied**: `FROM 'github:<owner>/<repo>'`, an `https://…`
  or `git@…` URL, with `BRANCH <ref>` and `SUBDIR <dir>` if the skill is not
  the repository root. The copy is pinned: it survives the source moving,
  and `cpb update writer` refreshes it from the recorded source.

cpb records every skill it adds in the playbook's manifest. `DROP SKILL
release-notes` removes only what cpb put there, and refuses a
`skills/<name>` it did not. A skill that ships inside a plugin stays the
plugin's: `ADD PLUGIN` brings it.
