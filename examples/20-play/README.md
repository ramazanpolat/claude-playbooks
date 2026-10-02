# 20: try someone else's playbook

```
cpb play --check ./reviewer.cpb                  # what it holds, and what to look at
cpb play --dry-run --json ./reviewer.cpb         # the plan, for a program
cpb play ./reviewer.cpb                          # preview, yes, run, removed afterwards
cpb play ./router.cpb                            # type router.example.net to run it
cpb play ./reviewer.cpb --keep                   # keep it as a playbook of your own
cpb update reviewer                              # fetch it again; the diff before anything changes
```

`cpb play` runs a recipe someone else wrote in a throwaway playbook, after it
has shown you what the recipe would do and you said yes. Here the recipes are
local files; the same works for a curated template name (`cpb play
code-reviewer`), an https URL, or `github:<owner>/<repo>/<path>.cpb@<tag>`.

- `reviewer.cpb` is a recipe a team might share: a model and tool rules, with
  a header (`title`, `description`, `min-cpb`) the preview shows.
- `refused.cpb` holds what a played recipe may not: `USE ENV` would attach your
  env sets, and a plaintext token is never shared. `--check` names both lines
  and exits 1.
- `router.cpb` moves the endpoint. That is never a plain yes: you type the
  host, the playbook gets its own login, and your credentials are blocked in
  it. `--yes` alone is refused and names `--trust-endpoint`.
- `--keep` builds the recipe as a playbook in your store (here `reviewer`),
  records where it came from (`SHOW PLAYBOOK reviewer`: `Played from:`), and
  runs nothing. `cpb update` fetches it again and shows the diff first.

`playbook.cpb` is the other path: once you trust a recipe, include it in a
playbook of your own and `APPLY` it, like any other file. That is what CI
applies here; `.check` runs the `cpb play` lines above.

Guide: [Try someone else's playbook](../../docs/guides/play.md).
