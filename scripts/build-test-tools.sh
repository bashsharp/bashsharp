#!/bin/sh
# Conformance runners are published by the same immutable lower module.
set -eu
repo=$(CDPATH= cd -P "$(dirname "$0")/.." && pwd)
cd "$repo"
export GOWORK=off
pin=$(go list -mod=readonly -m -f '{{.Version}}' github.com/qiangli/bash)
[ -n "$pin" ] || { echo 'build-test-tools: missing Bash dependency version' >&2; exit 2; }
mkdir -p bin
GOBIN="$repo/bin" go install -trimpath "github.com/qiangli/bash/tools/bash53suite@$pin"
GOBIN="$repo/bin" go install -trimpath "github.com/qiangli/bash/tools/bash53evidence@$pin"
