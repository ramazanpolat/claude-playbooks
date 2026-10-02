# Contributing

## Development

Go 1.26+ is the only toolchain you need (go.mod: `go 1.26.0`; CI and the
arena's `golang:1.26` build use 1.26, and CI fails if the directive rises
above it). Release artifacts are built with the same Go, and the darwin-arm64 asset is executed on a
macOS runner before anything is published, so the downloadable binaries never
come from a build path nothing exercised. Go dependencies are managed
in `go.mod` (cobra, toml).

Tests never touch your real Claude credentials: the `cmd` tests run under a
scratch HOME with a stub `security` on PATH (`resetCommandTestState`), the
`internal/auth` package stubs the Keychain lookup in `TestMain`, and the e2e
suite stubs both binaries. Keep new tests inside those helpers.

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

Use the bug template. The output of `cpb --version`, your OS,
and an exact command sequence beat any amount of description.

## Release process

A release needs its docs first: README, docs/ (tutorials, guides,
reference), examples/ for every new clause, and AGENTS.md. The maintainer's rule
and the checklist are in [AGENTS.md, "Before any release"](AGENTS.md#before-any-release);
never tag without them.

GitHub releases are created from `v*` tags only when the tagged commit is
already on `main`. Tags pushed from feature branches are ignored by the
release workflow.

```bash
git checkout main
git pull --ff-only
git tag -a vX.Y.Z -m vX.Y.Z
git push origin vX.Y.Z
```
