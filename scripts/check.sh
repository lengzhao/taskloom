#!/bin/sh
# Requires Go from go.mod. Document link/fence checks run as Go tests.
set -eu
cd "$(dirname "$0")/.."
mode=${1:-all}
case "$mode" in
  unit) go test ./... ;;
  race) go test -race ./... ;;
  all)
    go test ./...
    go vet ./...
    check_dir=$(mktemp -d "${TMPDIR:-/tmp}/taskloom-check.XXXXXX")
    trap 'rm -f "$check_dir/taskloom" "$check_dir/taskloom-admin"; rmdir "$check_dir"' EXIT HUP INT TERM
    go build -o "$check_dir/taskloom" ./cmd/taskloom
    "$check_dir/taskloom" -base base.yaml -config config.yaml -check
    go build -o "$check_dir/taskloom-admin" ./cmd/taskloom-admin
    ;;
  *) echo "usage: sh scripts/check.sh [all|unit|race]" >&2; exit 2 ;;
esac
