# Standalone build and test

Clone this repository alone. The Bash engine and shell entrypoint are selected
by the immutable `github.com/qiangli/bash` version in `go.mod`; neighboring
checkouts and `go.work` are never inputs.

Install Bashy with OCI support and provision the exact image identified by
`tools/bashy-build/image.lock`. The build runner verifies its digest before
starting. From the repository root:

```sh
bashy dag build
bashy dag binary-smoke
bashy dag test
```

These targets enter the official image. `build` emits the existing language
front door at `bin/bashsharp`, and a full shell driver at `bin/shell/bash` with
its native signal-launcher payload where required. The driver builds the
pinned Bash entrypoint with this checkout as the main Go module, so its Bash#
CLI dependency uses this checkout's source.

The Dockerfile is another standalone entrypoint using the same immutable
official image:

```sh
bashy oci build --target binaries -t localhost/bashsharp:standalone .
bashy oci build --target test -t localhost/bashsharp:test .
```

On a Linux OCI gate runner, run the complete sealed GNU Bash 5.3 compatibility
suite:

```sh
bashy dag test-bash53
```

The fixture fetcher retains the released GNU archive URL and SHA-256. It keeps
the GPL test data under `external/bash-5.3`, outside tracked source. The runner
and evidence writer are pinned public tools from the selected Bash revision.
The shell runs explicitly with Bash# enabled in a non-root, read-only,
network-free runtime container. Acceptance requires all 86 named fixtures,
with zero failures, timeouts or skips. Evidence is written to
`bin/bashsharp-bash53-evidence.json`, including the actual testee/payload
checksums and selected lower-module pin. `BASH53_BASELINE` can select prior
evidence for comparison.

This GNU Bash suite is not the licensed POSIX certification suite. Official
POSIX certification additionally requires the separately provisioned licensed
VSC corpus and its established runner; this repository does not bundle it or
claim certification from the 86-fixture result.

Bash and Bash# may build and test independently on separate runners when the
shared engine changes. This repository defaults to its own
`bashsharp-build-cache` volume, binaries/evidence directory, and Bash# image
and container names. Set `BASHY_BUILD_CPUS` to the runner's allocation and
`BASHY_BUILD_CACHE_VOLUME` when an additional isolated cache is needed. Do not
overcommit a shared runner by launching both at its full CPU allocation.
