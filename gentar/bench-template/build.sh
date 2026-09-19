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
#      SPEC-v4.md, which is why cpb has a [sandbox] claude_version pin at
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
WS=/tmp/cpb-agent-template-build

command -v sbx >/dev/null || { echo "sbx not installed on this host" >&2; exit 1; }

rm -rf "$WS" && mkdir -p "$WS"
sbx rm cpb-agent-tpl-build --force >/dev/null 2>&1 || true

sbx create --name cpb-agent-tpl-build shell "$WS" >/dev/null
sbx exec cpb-agent-tpl-build sh -lc \
  "npm install -g @anthropic-ai/claude-code@$VERSION && claude --version"
sbx stop cpb-agent-tpl-build >/dev/null
sbx template save cpb-agent-tpl-build "$TAG" >/dev/null
sbx rm cpb-agent-tpl-build --force >/dev/null
rm -rf "$WS"

echo "template $TAG built with claude-code $VERSION:"
sbx template ls | grep "$TAG" || true
