#!/usr/bin/env bash
# Release ritual: every gate this repo's releases have historically passed,
# in one command. Run it before tagging; it exits non-zero if anything fails.
#
#   tests/release-ritual.sh [--version vX.Y.Z] [--fast] [--skip p1,p5,...]
#
# Phases:
#   p1  static      gofmt, go vet, go build
#   p2  unit        go test ./...
#   p3  shellcheck  this script + install/uninstall when shellcheck exists
#   p4  cli-scratch real-binary exercise in a sandboxed HOME + playbooks root:
#                     create/alias (incl. the name-repair spelling),
#                     install from a local git fixture, env overrides and
#                     env profiles through a stub claude, update with
#                     settings + data + [env] preservation and a migration
#                     receipt, list/info output, delete cleanup
#   p5  herdr       kommander repo's two cpb-facing e2e suites against the
#                     candidate binary (CLAUDE_PLAYBOOK_BIN). Runs inside a
#                     dedicated herdr workspace — never squeezes the caller's
#                     pane. Skipped (warn) outside herdr or without the repo.
#   p6  matrix      external update-preservation matrix if KP_MATRIX_SCRIPT
#                     points at one (the 10-case herdr suite from the
#                     claude-playbook-finetune task is the reference
#                     implementation). Skipped (warn) when unset.
#   p7  arena       gentar own-arena suites via gentar/run.sh. Skipped (warn)
#                     when docker or the bench prerequisites are missing.
#   p8  gates       release gates for --version: clean tree, on main,
#                     main == origin/main, tag absent, CHANGELOG-style
#                     sanity. With no --version this phase only reports.
#
# --fast runs p1 p2 p3 p4 p8. Every phase may also be skipped by name.
# Candidate binary is built once into the run root and reused everywhere.

set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION=""
FAST=0
SKIP=""
while [ $# -gt 0 ]; do
  case "$1" in
    --version) VERSION="${2:?}"; shift 2 ;;
    --fast) FAST=1; shift ;;
    --skip) SKIP="${2:?}"; shift 2 ;;
    *) echo "unknown flag: $1" >&2; exit 2 ;;
  esac
done

RUN_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/cpb-release-ritual.XXXXXX")"
SCRIPT_HOME="${HOME:-}"   # p4 swaps HOME; phases after it need the real one
BIN="$RUN_ROOT/claude-playbook"
FAILED=0
PHASES_RUN=""

phase_enabled() {
  case ",$SKIP," in *",$1,"*) return 1 ;; esac
  if [ "$FAST" = "1" ]; then
    case "$1" in p5|p6|p7) return 1 ;; esac
  fi
  return 0
}

report() { # report <name> <rc> [detail]
  if [ "$2" -eq 0 ]; then
    echo "PASS  $1${3:+  ($3)}"
  else
    echo "FAIL  $1${3:+  ($3)}" >&2
    FAILED=1
  fi
}

cleanup() { rm -rf "$RUN_ROOT"; }
trap cleanup EXIT

echo "== release ritual — $(date '+%Y-%m-%d %H:%M:%S') =="

# --- p1 static ---------------------------------------------------------------
if phase_enabled p1; then
  PHASES_RUN="$PHASES_RUN p1"
  out="$(gofmt -l "$REPO_ROOT/cmd" "$REPO_ROOT/internal" "$REPO_ROOT/e2e" 2>&1)"
  if [ -z "$out" ]; then report "p1 gofmt" 0; else report "p1 gofmt" 1 "unformatted: $out"; fi
  if (cd "$REPO_ROOT" && go vet ./... >/dev/null 2>&1); then report "p1 go vet" 0; else report "p1 go vet" 1; fi
  if (cd "$REPO_ROOT" && go build -o "$BIN" .); then report "p1 go build" 0; else report "p1 go build" 1; fi
  [ -x "$BIN" ] || { echo "no candidate binary — aborting later phases" >&2; FAILED=1; PHASES_RUN="$PHASES_RUN p2 p3 p4 p5 p6 p7 p8"; SKIP="p2,p3,p4,p5,p6,p7,p8"; }
fi

# --- p2 unit -----------------------------------------------------------------
if phase_enabled p2; then
  PHASES_RUN="$PHASES_RUN p2"
  (cd "$REPO_ROOT" && go test ./... >"$RUN_ROOT/p2.log" 2>&1)
  report "p2 go test ./..." $? "$(tail -1 "$RUN_ROOT/p2.log" | cut -c1-60)"
fi

# --- p3 shellcheck -----------------------------------------------------------
if phase_enabled p3; then
  PHASES_RUN="$PHASES_RUN p3"
  if command -v shellcheck >/dev/null 2>&1; then
    shellcheck "$REPO_ROOT/tests/release-ritual.sh" "$REPO_ROOT/install.sh" "$REPO_ROOT/uninstall.sh" >/dev/null 2>&1
    report "p3 shellcheck" $?
  else
    echo "SKIP  p3 shellcheck (not installed)"
  fi
fi

# --- p4 cli-scratch ----------------------------------------------------------
if phase_enabled p4; then
  PHASES_RUN="$PHASES_RUN p4"
  SB="$RUN_ROOT/scratch"; H="$SB/home"; PB="$SB/playbooks"
  mkdir -p "$H/.claude-playbooks" "$PB"
  export HOME="$H"
  p4() { "$BIN" --playbooks-dir "$PB" "$@"; }
  rc=0
  # create + alias + name-repair (the claude/alias-name-repair behavior)
  p4 create demo --alias d >/dev/null 2>&1 || rc=1
  [ -f "$PB/demo/CLAUDE.md" ] || rc=1
  p4 alias demo --remove >/dev/null 2>&1 || rc=1
  p4 alias demo demo >/dev/null 2>&1 || rc=1
  "$BIN" --playbooks-dir "$PB" list 2>/dev/null | grep '^demo' >/dev/null || rc=1
  report "p4 create/alias/repair" $rc

  # install from a local git fixture, then update it with a migration
  rc=0
  FIX="$RUN_ROOT/fixture"; git init -q "$FIX"
  printf 'version = "1.0.0"\nname = "fx"\n' > "$FIX/.playbook"
  echo '# fx' > "$FIX/CLAUDE.md"
  mkdir "$FIX/migrations"
  # $3 is the install dir the CLI passes; do not assume env vars.
  # shellcheck disable=SC2016
  printf '#!/bin/sh\nmkdir -p "$3/data"; echo ran >> "$3/data/migration-receipt"\n' > "$FIX/migrations/apply.sh"
  chmod +x "$FIX/migrations/apply.sh"
  git -C "$FIX" add -A >/dev/null && git -C "$FIX" -c user.name=t -c user.email=t@e.invalid commit -qm v1
  p4 install "file://$FIX" --name fx --no-alias >/dev/null 2>&1 || rc=1
  [ -f "$PB/fx/CLAUDE.md" ] || rc=1
  report "p4 install" $rc

  # env overrides + env profiles: manifest state, launch effect through a
  # stub claude, delete-while-attached refusal. The stub security makes the
  # no-token path deterministic on darwin (see e2e/launch_env_test.go).
  rc=0
  STUB="$SB/bin"; mkdir -p "$STUB"
  # shellcheck disable=SC2016
  printf '#!/bin/sh\nenv > "$CPB_RITUAL_ENVDUMP"\n' > "$STUB/claude"
  printf '#!/bin/sh\nexit 1\n' > "$STUB/security"
  chmod +x "$STUB/claude" "$STUB/security"
  p4 env-profile glm set ANTHROPIC_BASE_URL=http://ritual/v1 >/dev/null 2>&1 || rc=1
  p4 env-profile glm unset CLAUDE_CODE_OAUTH_TOKEN >/dev/null 2>&1 || rc=1
  p4 env fx use glm >/dev/null 2>&1 || rc=1
  p4 env fx set RITUAL_OWN=yes >/dev/null 2>&1 || rc=1
  grep -q 'profiles = \["glm"\]' "$PB/fx/.playbook" || rc=1
  p4 env-profile glm delete >/dev/null 2>&1 && rc=1   # refused while attached
  p4 env fx use ghost >/dev/null 2>&1 && rc=1         # refused: no such profile
  DUMP="$SB/envdump"
  CPB_RITUAL_ENVDUMP="$DUMP" PATH="$STUB:$PATH" CLAUDE_CODE_OAUTH_TOKEN=shell-token \
    CLAUDE_PLAYBOOKS_OAUTH_TOKEN_FILE=/dev/null p4 run fx >/dev/null 2>&1 || rc=1
  grep -q '^ANTHROPIC_BASE_URL=http://ritual/v1$' "$DUMP" 2>/dev/null || rc=1
  grep -q '^RITUAL_OWN=yes$' "$DUMP" 2>/dev/null || rc=1
  grep -q '^CLAUDE_CONFIG_DIR=' "$DUMP" 2>/dev/null || rc=1
  grep -q '^CLAUDE_CODE_OAUTH_TOKEN=' "$DUMP" 2>/dev/null && rc=1   # unset by profile
  report "p4 env/env-profile launch" $rc

  rc=0
  # pilot state that must survive the update
  printf '{"env":{"X":"kept"},"hooks":{}}\n' > "$PB/fx/settings.json"
  mkdir -p "$PB/fx/data/tasks/live"
  echo live > "$PB/fx/data/tasks/live/TASK.md"
  # advance the fixture to v2 with a migration
  # v2 also ships an [env] block, which must never be adopted
  printf 'version = "2.0.0"\nname = "fx"\n\n[env.set]\nATTACK = "1"\n' > "$FIX/.playbook"
  printf '# fx2\n' > "$FIX/CLAUDE.md"
  git -C "$FIX" add -A >/dev/null && git -C "$FIX" -c user.name=t -c user.email=t@e.invalid commit -qm v2
  p4 update fx >/dev/null 2>&1 || rc=1
  grep -q fx2 "$PB/fx/CLAUDE.md" || rc=1
  grep -q '"X": *"kept"' "$PB/fx/settings.json" || rc=1
  grep -q live "$PB/fx/data/tasks/live/TASK.md" || rc=1
  [ -f "$PB/fx/data/migration-receipt" ] || rc=1  # migration ran against the install
  grep -q 'profiles = \["glm"\]' "$PB/fx/.playbook" || rc=1   # install-local [env] kept
  grep -q 'RITUAL_OWN' "$PB/fx/.playbook" || rc=1
  grep -q 'ATTACK' "$PB/fx/.playbook" && rc=1                   # source [env] ignored
  report "p4 install/update preservation" $rc

  rc=0
  # Not grep -q: under pipefail an early grep exit sends the producer
  # SIGPIPE on its next line and the pipeline fails on timing alone.
  p4 info fx 2>/dev/null | grep 'Update from:' >/dev/null || rc=1
  p4 update fx --check 2>/dev/null | grep 'up to date' >/dev/null || rc=1
  p4 delete fx -y >/dev/null 2>&1 || rc=1
  [ ! -d "$PB/fx" ] || rc=1
  report "p4 info/check/delete" $rc
  if [ -n "$SCRIPT_HOME" ]; then HOME="$SCRIPT_HOME"; export HOME; fi
fi

# --- p5 herdr suites ---------------------------------------------------------
# Runs the kommander repo's cpb-facing suites against the candidate binary,
# inside a dedicated workspace so the caller's pane is never squeezed.
if phase_enabled p5; then
  PHASES_RUN="$PHASES_RUN p5"
  KOM="${KP_KOMMANDER_REPO:-$HOME/DEV/kommander-playbook}"
  if [ "${HERDR_ENV:-}" != "1" ] || [ ! -d "$KOM/tests/e2e" ]; then
    echo "SKIP  p5 herdr suites (need herdr pane + kommander repo at $KOM)"
  else
    WS_JSON="$(herdr workspace create --label cpb-release-ritual 2>/dev/null)" || WS_JSON=""
    if [ -z "$WS_JSON" ]; then
      echo "SKIP  p5 herdr suites (workspace create failed)"
    else
      WS="$(printf '%s' "$WS_JSON" | python3 -c 'import json,sys; print(json.load(sys.stdin)["result"]["workspace"]["workspace_id"])')"
      RPANE="$(printf '%s' "$WS_JSON" | python3 -c 'import json,sys; print(json.load(sys.stdin)["result"]["root_pane"]["pane_id"])')"
      RUNNER="$(herdr pane split "$RPANE" --direction right --no-focus 2>/dev/null | python3 -c 'import json,sys; print(json.load(sys.stdin)["result"]["pane"]["pane_id"])')"
      RC5="$RUN_ROOT/p5.rc"; : > "$RC5"
      herdr pane send-text "$RUNNER" "CLAUDE_PLAYBOOK_BIN='$BIN' KP_E2E_REQUIRE_HERDR=1 bash '$KOM/tests/e2e/herdr-real-user-playbook-install.sh' > '$RUN_ROOT/p5a.log' 2>&1; a=\$?; CLAUDE_PLAYBOOK_BIN='$BIN' KP_E2E_REQUIRE_HERDR=1 bash '$KOM/tests/e2e/herdr-e2e.sh' > '$RUN_ROOT/p5b.log' 2>&1; echo \$((a + \$?)) > '$RC5'" >/dev/null
      herdr pane send-keys "$RUNNER" Enter >/dev/null
      rc=124
      for _ in $(seq 1 240); do
        [ -s "$RC5" ] && { rc="$(cat "$RC5")"; break; }
        sleep 5
      done
      herdr workspace close "$WS" >/dev/null 2>&1 || true
      if [ "$rc" -eq 124 ]; then
        report "p5 herdr suites" 1 "timeout — logs: $RUN_ROOT/p5a.log p5b.log (kept)"
        trap - EXIT   # keep logs for inspection
      else
        report "p5 herdr suites" "$rc" "real-user + herdr-e2e (exit sum $rc)"
      fi
    fi
  fi
fi

# --- p6 external matrix ------------------------------------------------------
if phase_enabled p6; then
  PHASES_RUN="$PHASES_RUN p6"
  if [ -n "${KP_MATRIX_SCRIPT:-}" ] && [ -x "${KP_MATRIX_SCRIPT:-}" ]; then
    KP_UM_SKIP_REPO_SUITES=1 bash "$KP_MATRIX_SCRIPT" >"$RUN_ROOT/p6.log" 2>&1
    report "p6 update matrix" $? "$(tail -1 "$RUN_ROOT/p6.log" | cut -c1-60)"
  else
    echo "SKIP  p6 update matrix (set KP_MATRIX_SCRIPT to the matrix suite)"
  fi
fi

# --- p7 arena ----------------------------------------------------------------
if phase_enabled p7; then
  PHASES_RUN="$PHASES_RUN p7"
  if ! command -v docker >/dev/null 2>&1 || [ ! -x "$REPO_ROOT/gentar/run.sh" ]; then
    echo "SKIP  p7 arena (docker or gentar/run.sh missing)"
  elif command -v nc >/dev/null 2>&1 && nc -z 127.0.0.1 8123 >/dev/null 2>&1; then
    echo "SKIP  p7 arena (port 8123 busy — another arena stack is up; run via CI dispatch instead)"
  else
    "$REPO_ROOT/gentar/run.sh" cli-head-build >"$RUN_ROOT/p7.log" 2>&1
    report "p7 arena cli-head-build" $?
  fi
fi

# --- p8 release gates --------------------------------------------------------
if phase_enabled p8; then
  PHASES_RUN="$PHASES_RUN p8"
  cd "$REPO_ROOT" || exit 1
  rc=0
  [ -z "$(git status --porcelain)" ] || { echo "  gate: working tree dirty" >&2; rc=1; }
  [ "$(git branch --show-current)" = "main" ] || { echo "  gate: not on main" >&2; rc=1; }
  git fetch -q origin >/dev/null 2>&1
  [ -z "$(git rev-list origin/main..main)" ] || { echo "  gate: local main ahead of origin" >&2; rc=1; }
  report "p8 repo gates (clean/main/synced)" $rc
  if [ -n "$VERSION" ]; then
    rc=0
    [[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "  gate: bad version format" >&2; rc=1; }
    ! git rev-parse "tags/$VERSION" >/dev/null 2>&1 || { echo "  gate: tag $VERSION exists" >&2; rc=1; }
    git merge-base --is-ancestor origin/main HEAD 2>/dev/null || { echo "  gate: HEAD not up to date with origin/main" >&2; rc=1; }
    report "p8 tag gates ($VERSION)" $rc
    echo "  next: git tag -a $VERSION -m '...' && git push origin main $VERSION"
  fi
fi

echo "== phases:$PHASES_RUN =="
if [ "$FAILED" -ne 0 ]; then
  echo "RITUAL FAILED" >&2
  exit 1
fi
echo "RITUAL PASSED"
