#!/bin/sh
# The host orchestrates OCI only; Go runs inside the locked official image.
set -eu
repo=$(CDPATH= cd -P "$(dirname "$0")/.." && pwd)
cd "$repo"
task=${1:?DAG target required}
case "$task" in build|test|binary-smoke|build-test-tools|build-bsh|test-bsh) ;; *) echo 'unsupported container task' >&2; exit 2;; esac
cpus=${BASHY_BUILD_CPUS:-8}
export BASHY_BUILD_CPUS=$cpus
export BASHY_BUILD_CACHE_VOLUME=${BASHY_BUILD_CACHE_VOLUME:-bashsharp-build-cache}
exec "${BASHY:-bashy}" tools/bashy-build/run.sh "$repo" -- "GOWORK=off GOMAXPROCS=$cpus GOFLAGS=-p=1 GOMEMLIMIT=2GiB /bashy dag --no-journal --force $task"
