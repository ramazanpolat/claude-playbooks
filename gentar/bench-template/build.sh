#!/bin/sh
# gentar/bench-template/build.sh — our own bench template, for the
# agent-in-the-loop suite only.
#
#   ssh <bench-host> "VERSION=$(cat gentar/bench-template/VERSION) sh -s" \
#       < gentar/bench-template/build.sh
#
# Why ours and not gentar's `gentar-bench-v1`:
#
#   1. The pin must not move under us. gentar's template pins claude-code
#      in ITS repo, bumped on ITS schedule. Our agent suite runs through a
#      third-party Anthropic-compatible endpoint, and claude-code 2.1.265+
#      sends the Artifact tool, whose schema at least one such backend
#      (GLM) rejects with 400 on EVERY interactive turn -- documented in
#      SPEC.md, which is why cpb has a [sandbox] claude_version pin at
#      all. A gentar bump past 2.1.265 would break our suite for a reason
#      that has nothing to do with claude-playbooks.
#   2. Blast radius. Our endpoint credential enters benches made from this
#      template. Keeping it off the shared gate template means a suite of
#      ours cannot widen what gentar's own gate runs with.
#
# The pin lives in VERSION (single source); pilot-agent-session asserts the
# same version from inside the bench, so a drifted or stale template fails
# loudly instead of mysteriously.
#
# This builds the template ONLY. Reaching a router additionally needs an
# egress rule on the bench-host -- sandboxes are default-deny and gentar
# applies no per-scenario allow rules. See gentar/README.md.
set -e

TAG=cpb-agent-bench-v1
# The documented invocation PIPES this script to the bench-host, where the
# repo -- and so the VERSION file -- is not present. An inherited VERSION
# therefore wins, and the caller passes it from the file; the local read is
# for running this script from a checkout on the bench-host itself. Falling
# back to a literal silently would defeat "the pin lives in VERSION".
VERSION=${VERSION:-$(cat VERSION 2>/dev/null || echo 2.1.234)}

command -v sbx >/dev/null || { echo "sbx not installed on this host" >&2; exit 1; }

# A workspace and a build sandbox of this run's own, so two bench users (or
# two runs) on one host never share, delete or race on each other's. Both go
# when the script ends, however it ends.
WS=$(mktemp -d "${TMPDIR:-/tmp}/cpb-agent-template-build.XXXXXX")
BOX=cpb-agent-tpl-build-$(printf '%s' "${WS##*.}" | tr '[:upper:]' '[:lower:]')
cleanup() { sbx rm "$BOX" --force >/dev/null 2>&1 || true; rm -rf "$WS"; }
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

sbx create --name "$BOX" shell "$WS" >/dev/null
sbx exec "$BOX" sh -lc \
  "npm install -g @anthropic-ai/claude-code@$VERSION && claude --version"
sbx stop "$BOX" >/dev/null
sbx template save "$BOX" "$TAG" >/dev/null

echo "template $TAG built with claude-code $VERSION:"
sbx template ls | grep "$TAG" || true
