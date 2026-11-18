#!/bin/sh
# The one entry point for builds and tests: run a command in the official
# build image named by tools/bashy-build/image.lock, through bashy oci.
#
#   tools/bashy-build/run.sh SRC_DIR -- COMMAND...
#
# SRC_DIR is mounted at /work (chowned to the image's unprivileged user).
# Go module and build caches live in a named volume on this runner and are
# reused by every run, so nothing is downloaded twice. The volume hides the
# image's cache directories, so each run creates them inside it first.
set -eu
here=$(CDPATH= cd -P "$(dirname "$0")" && pwd)
lock=${BASHY_BUILD_LOCK:-$here/image.lock}
fail() { echo "bashy-build run: $*" >&2; exit 1; }
get() { sed -n "s/^$1=//p" "$lock" | head -n 1; }

[ $# -ge 3 ] && [ "$2" = "--" ] || fail "usage: run.sh SRC_DIR -- COMMAND..."
src=$(CDPATH= cd -P "$1" && pwd) || fail "no source directory: $1"
shift 2

image=$(get image)
digest=$(get image_digest)
[ -n "$image" ] || fail "image.lock names no image"
oci=${BASHY_OCI:-bashy oci}
# Run by the lock's unique tag and verify its digest: a local image is not
# reliably found by name@digest, and a mismatch must never fall back to a pull.
ref=$image
if [ -n "$digest" ]; then
  have=$($oci image inspect --format '{{.Digest}}' "$image" 2>/dev/null) ||
    fail "the locked image $image is not on this runner; build it with build.sh"
  [ "$have" = "$digest" ] || fail "$image is $have, but the lock says $digest"
fi
volume=${BASHY_BUILD_CACHE_VOLUME:-bashy-build-cache}
$oci volume exists "$volume" 2>/dev/null || $oci volume create "$volume" >/dev/null

# --init: PID 1 is a real init that reaps orphans. Without it PID 1 is
# bashy, killed grandchildren linger as zombies and look alive, and every
# process-group kill test fails in the image (yoke agentpty, chat, cligw,
# dag signal; passes with --init).
#
# BASHY_BUILD_CPUS=N caps one run at N CPUs and N parallel go packages, so
# several runs can share a runner without starving each other.
#
# podman-init starts its child with SIGTSTP, SIGTTIN and SIGTTOU blocked, and
# a blocked mask is inherited across exec: bashy, like bash, keeps it and
# every test process got it (a background terminal read then fails with EIO
# instead of stopping; bashy internal/cli job-control tests failed). dash
# starts the commands it forks with an empty mask (measured), so it forks
# bashy rather than exec-ing it: an exec keeps the blocked mask. Its exit
# status is bashy's. Both go away when bashy reaps as PID 1 (Sprint 653 R9).
exec $oci run --rm --init \
  -v "$volume:/var/cache/bashy-build:U" \
  -v "$src:/work:U" \
  -w /work \
  -e BASHY_BUILD_RUN=1 \
  ${BASHY_BUILD_OUT:+-v "$BASHY_BUILD_OUT:/out:U"} \
  ${BASHY_BUILD_CPUS:+--cpus "$BASHY_BUILD_CPUS" -e GOFLAGS=-p=$BASHY_BUILD_CPUS} \
  --entrypoint /usr/bin/dash \
  "$ref" -c '/bashy -c "$1"' dash \
  'mkdir -p "$GOMODCACHE" "$GOCACHE" "$GOTMPDIR" && '"$*"
