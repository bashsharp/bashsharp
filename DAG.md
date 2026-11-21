---
name: bashsharp
description: Standalone Bashsharp builds and tests in the locked official image
---

# Bashsharp standalone tasks

A fresh checkout needs Bashy with OCI support and the image identified by
`tools/bashy-build/image.lock`. Public dependencies are immutable Go module
pins; every build uses `GOWORK=off`. No umbrella or adjacent checkout is used.

`bashy dag build`, `test`, and `binary-smoke` enter the official image unless
already inside it. `BASHY_BUILD_CPUS` selects the per-run CPU allocation;
`BASHY_BUILD_CACHE_VOLUME` defaults to `bashsharp-build-cache`. Bash and Bashsharp
can run on separate runners with independent caches and artifacts.

## Tasks

### build
Emit `bin/bashsharp` and the lower shell driver `bin/shell/bash` (plus its
native `.real` payload where required). The language executable keeps its
existing front door. The shell driver comes from the exact pinned Bash module.
Effects: exec, net, read, write

```bash
set -e
if [ "${BASHY_BUILD_RUN:-}" != 1 ]; then
  "${BASHY:-bashy}" scripts/container-task.sh build
else
export GOWORK=off
mkdir -p bin
go build -mod=readonly ./...
go build -mod=readonly -trimpath -o bin/bashsharp ./cmd/bashsharp
"${BASHY:-bashy}" scripts/build-shell.sh
fi
```

### test
Run all repository Go tests without sibling modules or overlays.
Effects: exec, net, read, write

```bash
set -e
if [ "${BASHY_BUILD_RUN:-}" != 1 ]; then
  "${BASHY:-bashy}" scripts/container-task.sh test
else
GOWORK=off go test -mod=readonly -count=1 ./...
fi
```

### binary-smoke
Exercise both emitted executables in the locked environment.
Requires: build
Effects: exec, read

```bash
set -e
if [ "${BASHY_BUILD_RUN:-}" != 1 ]; then
  "${BASHY:-bashy}" scripts/container-task.sh binary-smoke
else
[ "$(bin/bashsharp -c 'printf "%s\n" bashsharp-smoke')" = bashsharp-smoke ]
[ "$(bin/shell/bash --bashpp -c 'printf "%s\n" bashsharp-shell-smoke')" = bashsharp-shell-smoke ]
fi
```

### build-bsh
Emit `bin/bsh`, the certified shell: Bash 5.3 + Bash# + the POSIX-required
coreutils in one stripped executable. `cmd/bsh` is its own module (requiring
only bashsharp, bash and coreutils), so it builds from its own go.mod with
`GOWORK=off` and links no Yoke, Bashy or ycode package. Prints size and time.
Effects: exec, net, read, write

```bash
set -e
if [ "${BASHY_BUILD_RUN:-}" != 1 ]; then
  "${BASHY:-bashy}" scripts/container-task.sh build-bsh
else
mkdir -p bin
out="$PWD/bin/bsh"
cd cmd/bsh
start=$(date +%s)
GOWORK=off go build -mod=readonly -trimpath \
  -ldflags "-s -w -X 'github.com/bashsharp/bashsharp/cli.bashVersion=5.3.0(1)-bsh-${VERSION:-dev}'" \
  -o "$out" .
end=$(date +%s)
echo "built bin/bsh ($(wc -c < "$out" | tr -d ' ') bytes, $((end - start))s)"
fi
```

### test-bsh
Test the `cmd/bsh` module alone: the zero-Yoke/zero-Bashy link proof (no
allowlist), the go.mod requirement check, argv0 routing, and the binary smoke
(Bash 5.3, Bash# default, in-process coreutils under an empty PATH).
Effects: exec, net, read, write

```bash
set -e
if [ "${BASHY_BUILD_RUN:-}" != 1 ]; then
  "${BASHY:-bashy}" scripts/container-task.sh test-bsh
else
cd cmd/bsh
GOWORK=off go test -mod=readonly -count=1 .
fi
```

### container-build
Build independently in the immutable official image.
Effects: exec, net, read, write

```bash
"${BASHY:-bashy}" scripts/container-task.sh build
```

### container-test
Test independently in the immutable official image.
Effects: exec, net, read, write

```bash
"${BASHY:-bashy}" scripts/container-task.sh test
```

### build-test-tools
Build the unchanged sealed fixture harness and evidence writer from the same
pinned public Bash module. The helpers are separate modules and are not linked
into the language executable.
Effects: exec, net, read, write

```bash
set -e
if [ "${BASHY_BUILD_RUN:-}" != 1 ]; then
  "${BASHY:-bashy}" scripts/container-task.sh build-test-tools
else
"${BASHY:-bashy}" scripts/build-test-tools.sh
fi
```

### test-bash-fixtures
Fetch the checksum-pinned external GNU Bash 5.3 fixture data. It is test data,
not repository source or a runtime dependency.
Effects: exec, net, read, write

```bash
"${BASHY:-bashy}" scripts/ensure-bash53-fixtures.sh
```

### test-bash53
Run every named GNU Bash 5.3 fixture in a sealed, non-root, network-free OCI
runtime, with the unchanged harness and explicit Bashsharp-mode shell testee.
This is Bash compatibility coverage, not licensed POSIX certification. Run on
a Linux OCI gate runner; all Go compilation still uses the official image.
Requires: build, build-test-tools, test-bash-fixtures
Effects: exec, net, read, write

```bash
set -e
image=$(sed -n 's/^image=//p' tools/bashy-build/image.lock)
digest=$(sed -n 's/^image_digest=//p' tools/bashy-build/image.lock)
oci="${BASHY:-bashy} oci"
[ "$($oci image inspect --format '{{.Digest}}' "$image")" = "$digest" ]
pin=$(sed -n 's/^[[:space:]]*github.com\/qiangli\/bash[[:space:]]*\([^[:space:]]*\).*/\1/p' go.mod)
[ -n "$pin" ]
BASH53_ARTIFACT=B BASH53_BASHPP=1 BASH53_GATE=1 \
BASH53_TESTEE_BIN="$PWD/bin/shell/bash" \
BASH53_SOURCE_COMMIT="$(git rev-parse HEAD)" \
BASH53_LOWER_PINS="github.com/qiangli/bash@$pin" \
BASH53_BASE_IMAGE="$image" \
BASH53_EVIDENCE="${BASH53_EVIDENCE:-bin/bashsharp-bash53-evidence.json}" \
"${BASHY:-bashy}" scripts/test-bash-container.sh
```

### docker-build
Build the standalone Dockerfile after verifying the locked base image digest.
Effects: exec, net, read, write

```bash
set -eu
oci="${BASHY:-bashy} oci"
image=$(sed -n 's/^image=//p' tools/bashy-build/image.lock)
digest=$(sed -n 's/^image_digest=//p' tools/bashy-build/image.lock)
[ "$($oci image inspect --format '{{.Digest}}' "$image")" = "$digest" ]
$oci build --pull=never --build-arg "BUILD_IMAGE=$image" -t "${BASHSHARP_IMAGE:-localhost/bashsharp-standalone:dev}" -f Dockerfile .
```

### vet
Run static Go checks in the same official image using `container-test` for
regression coverage; this leaf is intended for an image session.
Effects: exec, read

```bash
GOWORK=off go vet -mod=readonly ./...
```

### clean
Remove only this repository's emitted binaries and evidence.
Effects: destroy

```bash
rm -rf bin
```
