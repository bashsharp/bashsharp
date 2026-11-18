#!/bin/sh
# Stable per-workspace identity for concurrent agent-owned test containers.

bashy_test_lane() {
  if [ -n "${BASHY_TEST_LANE:-}" ]; then
    raw=$BASHY_TEST_LANE
  else
    raw=ws-$(printf '%s\n' "$1" | cksum | awk '{ print $1 }')
  fi
  lane=$(printf '%s' "$raw" | tr 'A-Z' 'a-z' | sed 's/[^a-z0-9_.-]/-/g; s/^-*//; s/-*$//' | cut -c1-48)
  case "$lane" in ''|*[!a-z0-9_.-]*) return 2 ;; esac
  printf '%s\n' "$lane"
}
