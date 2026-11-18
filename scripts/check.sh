#!/bin/sh
# Requires Go from go.mod; optional design validation requires Ruby's standard YAML library.
set -eu
cd "$(dirname "$0")/.."
mode=${1:-all}
case "$mode" in
  unit) go test ./... ;;
  race) go test -race ./... ;;
  design) ruby scripts/check-design.rb ;;
  all)
    go test ./...
    go vet ./...
    check_dir=$(mktemp -d "${TMPDIR:-/tmp}/taskloom-check.XXXXXX")
    trap 'rm -f "$check_dir/taskloom" "$check_dir/taskloom-admin"; rmdir "$check_dir"' EXIT HUP INT TERM
    go build -o "$check_dir/taskloom" ./cmd/taskloom
    "$check_dir/taskloom" -base examples/minimal/base.yaml -config examples/minimal/config.yaml -check
    "$check_dir/taskloom" -base base.yaml -config config.yaml -check
    go build -o "$check_dir/taskloom-admin" ./cmd/taskloom-admin
    ruby scripts/check-design.rb
    ;;
  *) echo "usage: sh scripts/check.sh [all|unit|race|design]" >&2; exit 2 ;;
esac
