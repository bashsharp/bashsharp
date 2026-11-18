#!/bin/sh
# Build the full shell driver with this checkout as the main Go module.
set -eu
repo=$(CDPATH= cd -P "$(dirname "$0")/.." && pwd)
cd "$repo"
export GOWORK=off
bashy=${BASHY:-$(command -v bashy)}
bash_dir=$(go list -mod=readonly -m -f '{{.Dir}}' github.com/qiangli/bash)
[ -n "$bash_dir" ] || { echo 'build-shell: Bash dependency is not downloaded' >&2; exit 2; }
mkdir -p bin/shell
goos=$(go env GOOS)
goarch=$(go env GOARCH)
hostos=$(go env GOHOSTOS)
hostarch=$(go env GOHOSTARCH)
ext=
[ "$goos" != windows ] || ext=.exe
if [ "$goos" = "$hostos" ] && [ "$goarch" = "$hostarch" ] && [ "$(BASHY="$bashy" "$bashy" "$bash_dir/scripts/launcher-wanted.sh" build-shell)" = 1 ]; then
  BASHY_EXE= "$bash_dir/scripts/go-product.sh" build -mod=readonly -trimpath -o bin/shell/bash.real github.com/qiangli/bash/cmd/bash
  cc -x c -std=c11 -O2 -Wall -Wextra -Werror -o bin/shell/bash "$bash_dir/native/siglaunch.c.in"
else
  rm -f bin/shell/bash.real
  BASHY_EXE= "$bash_dir/scripts/go-product.sh" build -mod=readonly -trimpath -o "bin/shell/bash$ext" github.com/qiangli/bash/cmd/bash
fi
