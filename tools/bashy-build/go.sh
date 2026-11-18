#!/bin/sh
# go.sh ARGS... runs `go ARGS` inside the locked official build image, with
# the current directory mounted at /work. Scripts that take a GO command (the
# Bash 5.3 gate's testee and harness builds) use GO=tools/bashy-build/go.sh,
# so what they test is built by the same image and toolchain as the release.
# GOOS, GOARCH, CGO_ENABLED and GOWORK pass through; an -o PATH is written
# through a mounted output directory.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
q() { printf "'%s'" "$(printf '%s' "$1" | sed "s/'/'\\\\''/g")"; }
cmd=""
for v in GOOS GOARCH CGO_ENABLED GOWORK; do
  eval "val=\${$v-}"
  [ -n "$val" ] && cmd="$cmd $v=$(q "$val")"
done
cmd="$cmd go"
out=""
while [ $# -gt 0 ]; do
  if [ "$1" = -o ] && [ $# -gt 1 ]; then
    mkdir -p "$(dirname "$2")"
    out=$(cd "$(dirname "$2")" && pwd)
    cmd="$cmd -o $(q "/out/$(basename "$2")")"
    shift 2
    continue
  fi
  cmd="$cmd $(q "$1")"
  shift
done
BASHY_BUILD_OUT=$out exec "$here/run.sh" "$(pwd)" -- "$cmd"
