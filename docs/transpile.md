# Transpiling Bash#

`bashy transpile --bashsharp input.bsh -o main.go` and `bashsharp transpile
--bashsharp input.bsh -o main.go` emit the same ordinary Go source and source
map. Add `--standalone` to also write `go.mod` in the output directory, with
module name `main`, `go 1.27`, the runtime requirement used by this module, and
a proxy-resolvable replacement to the pinned `qiangli/sh` fork revision. This
mode requires Go 1.27 or newer and an explicit `-o`; because the directory may
already belong to another Go module, an existing `go.mod` is left untouched
unless `--force` is present. Without `--standalone`, emitted source and maps are
unchanged and no module file is written. On a clean machine, build with `go
build -mod=mod .`; the first build downloads the pinned runtime and records its
checksums in `go.sum`.
