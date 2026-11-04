#!/usr/bin/env bash
# Pre-commit / CI checks for the Go worker. golangci-lint is pinned in worker/go.mod as a tool.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)/worker"
unformatted=$(gofmt -l .)
if [ -n "$unformatted" ]; then
  echo "gofmt needed on:"; echo "$unformatted"; exit 1
fi
go vet ./...
go tool golangci-lint run ./...
go test ./...
