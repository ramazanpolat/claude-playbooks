# The image cpb's OpenShell backend runs Claude Code in. It is
# embedded in the cpb binary and built on first use, on the
# gateway host, as cpb-openshell/claude:<version>-<hash of this file>. The
# base is pinned by digest; Claude Code by CLAUDE_CODE_VERSION ([sandbox]
# claude_version, else the version cpb pins). npm puts the
# native binary at a fixed system path, which the sandbox policy names.
FROM nvcr.io/nvidia/base/ubuntu:24.04@sha256:c280ee89f8bfcbdaba6179ad4347f60c509e841cbe63eb93002f01bf70e0819c
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ca-certificates curl git nodejs npm procps python3 \
 && rm -rf /var/lib/apt/lists/*
ARG CLAUDE_CODE_VERSION
RUN npm install -g "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" \
 && test -x /usr/local/lib/node_modules/@anthropic-ai/claude-code/bin/claude.exe
