#!/bin/sh
# Requires Go from go.mod. Document link/fence checks run as Go tests.
set -eu
cd "$(dirname "$0")/.."
case "${1:-all}" in
  all)
    go test ./...
    go vet ./...
    go run ./cmd/taskloom -base base.yaml -config config.yaml -check
    ;;
  race) go test -race ./... ;;
  *) echo "usage: sh scripts/check.sh [all|race]" >&2; exit 2 ;;
esac
