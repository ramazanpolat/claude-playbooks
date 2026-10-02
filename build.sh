#!/bin/sh
set -e
VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo "dev")
go build -ldflags "-X github.com/ramazanpolat/claude-playbooks/cmd.Version=$VERSION" -o cpb .
echo "Built cpb $VERSION"
