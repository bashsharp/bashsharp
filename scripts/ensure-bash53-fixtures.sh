#!/bin/sh
# Same GNU Bash 5.3 corpus URL and SHA-256 as the released fixture provisioner.
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cache=${BASH53_FIXTURE_CACHE:-${XDG_CACHE_HOME:-$HOME/.cache}/bash-fixtures}
sha=0d5cd86965f869a26cf64f4b71be7b96f90a3ba8b3d74e27e8e9d9d5550f31ba
mkdir -p "$cache" "$root/external"
archive="$cache/bash-5.3.tar.gz"
if ! printf '%s  %s\n' "$sha" "$archive" | sha256sum -c - >/dev/null 2>&1; then
  curl -fL --retry 3 https://ftp.gnu.org/gnu/bash/bash-5.3.tar.gz -o "$archive"
  printf '%s  %s\n' "$sha" "$archive" | sha256sum -c -
fi
if [ ! -d "$root/external/bash-5.3/tests" ]; then
  tar -xzf "$archive" -C "$root/external"
fi
