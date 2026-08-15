# Contributing

## Development

Go 1.21+. No other dependencies.

```bash
go test ./...     # full suite
go vet ./...
gofmt -l .        # must print nothing
sh -n install.sh uninstall.sh
```

CI runs exactly these on every PR. A PR with red CI is not reviewed.

## What the codebase promises

- `SPEC-v4.md` is the contract. A behavior change without a matching spec
  change is a bug in the PR, not in the spec.
- The registry is stateless: playbook discovery reads the filesystem on
  every invocation. Do not add index files, caches, or daemons.
- Invariants are enforced at write time, for operations the tool performs.
  Do not add code that defends against hand-edited state (manifests,
  symlinks); hand-made inconsistency fails loudly at use time instead.
- The installer never edits shell rc files. Uninstall removes only what
  the tool provably created.
- Shell scripts are POSIX sh: no bashisms, tested against dash and macOS
  /bin/sh.

## Pull requests

- Branch from `main`; one concern per PR.
- Every bug fix carries a regression test that fails without the fix.
- Destructive-path changes (install, uninstall, delete, rename) need a
  sandboxed end-to-end run in the PR description showing what was removed
  and, just as important, what survived.
- Commit messages explain why, not what.

## Reporting bugs

Use the bug template. The output of `claude-playbook --version`, your OS,
and an exact command sequence beat any amount of description.
