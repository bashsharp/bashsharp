# Sprint 162 lane lower-2 findings

The exact upstream Go 1.27 harness owns the root verdicts. The library
emitter is the lower half of D1's compiled overlay route; package roots do
not change verdict until the Bashy caller and backend overlay consume its
per-file results.

| root | first cause | mechanism | status |
|---|---|---|---|
| `testdir:escape2.go` | blank-separated `//go:noescape` was absent from the declaration | attach every declaration pragma between the preceding declaration and the current one | fixed in `00700c00` |
| `testdir:escape2n.go` | blank-separated `//go:noescape` was absent from the declaration | attach every declaration pragma between the preceding declaration and the current one | fixed in `00700c00` |
| `testdir:nilptr3.go` | a comment between unary operators is not represented in the lowered expression, so formatting moves the second operand's line | full comment-position fidelity (Sprint 152 C1) | design; no general lower rule exists without retaining expression comments |
| `package:cmd/compile` | generated module is outside the standard-library internal visibility tree | per-origin library emission plus `go test -overlay` | moved to backend seam; lower prerequisite fixed in `8c91e4e0` |
| `package:cmd/compile/internal/abt` | generated test main cannot import `testing/internal/testdeps` outside cmd/go's test-main exception | per-origin library emission plus `go test -overlay` | moved to backend seam; lower prerequisite fixed in `8c91e4e0` |
| `package:cmd/compile/internal/amd64` | generated module cannot import a `cmd/compile/internal` dependency | per-origin library emission plus `go test -overlay` | moved to backend seam; lower prerequisite fixed in `8c91e4e0` |
| `package:cmd/compile/internal/base` | generated module cannot import a `cmd/internal` dependency | per-origin library emission plus `go test -overlay` | moved to backend seam; lower prerequisite fixed in `8c91e4e0` |
| `package:cmd/compile/internal/compare` | generated module cannot import a `cmd/compile/internal` dependency | per-origin library emission plus `go test -overlay` | moved to backend seam; lower prerequisite fixed in `8c91e4e0` |
| `package:cmd/compile/internal/devirtualize` | generated module cannot import a `cmd/compile/internal` dependency | per-origin library emission plus `go test -overlay` | moved to backend seam; lower prerequisite fixed in `8c91e4e0` |
| `package:cmd/compile/internal/dwarfgen` | generated module cannot import an `internal` dependency | per-origin library emission plus `go test -overlay` | moved to backend seam; lower prerequisite fixed in `8c91e4e0` |
| `package:cmd/compile/internal/importer` | generated module cannot import an `internal` dependency | per-origin library emission plus `go test -overlay` | moved to backend seam; lower prerequisite fixed in `8c91e4e0` |
| `package:cmd/compile/internal/inline/inlheur` | generated module cannot import a `cmd/compile/internal` dependency | per-origin library emission plus `go test -overlay` | moved to backend seam; lower prerequisite fixed in `8c91e4e0` |
| `package:cmd/compile/internal/ir` | generated module cannot import a `cmd/compile/internal` dependency | per-origin library emission plus `go test -overlay` | moved to backend seam; lower prerequisite fixed in `8c91e4e0` |
| `package:cmd/compile/internal/liveness` | generated module cannot import an `internal` dependency | per-origin library emission plus `go test -overlay` | moved to backend seam; lower prerequisite fixed in `8c91e4e0` |
| `package:cmd/compile/internal/logopt` | generated module cannot import a `cmd/internal` dependency | per-origin library emission plus `go test -overlay` | moved to backend seam; lower prerequisite fixed in `8c91e4e0` |
| `package:cmd/compile/internal/loopvar` | generated module cannot import a `cmd/compile/internal` dependency | per-origin library emission plus `go test -overlay` | moved to backend seam; lower prerequisite fixed in `8c91e4e0` |
| `package:cmd/compile/internal/noder` | generated module cannot import an `internal` dependency | per-origin library emission plus `go test -overlay` | moved to backend seam; lower prerequisite fixed in `8c91e4e0` |
| `package:cmd/compile/internal/rangefunc` | generated module cannot import a `cmd/compile/internal` dependency | per-origin library emission plus `go test -overlay` | moved to backend seam; lower prerequisite fixed in `8c91e4e0` |
| `package:cmd/compile/internal/reflectdata` | generated module cannot import a `cmd/compile/internal` dependency | per-origin library emission plus `go test -overlay` | moved to backend seam; lower prerequisite fixed in `8c91e4e0` |
| `package:cmd/compile/internal/ssa` | the package has assembly test companions and the flat backend refuses non-Go input | per-origin library emission plus `go test -overlay`; assembly remains native compiler artifact under D1 | moved to backend seam |
| `package:cmd/compile/internal/ssagen` | generated module cannot import an `internal` dependency | per-origin library emission plus `go test -overlay` | moved to backend seam; lower prerequisite fixed in `8c91e4e0` |
| `package:cmd/compile/internal/syntax` | generated module cannot import an `internal` dependency | per-origin library emission plus `go test -overlay` | moved to backend seam; lower prerequisite fixed in `8c91e4e0` |
| `package:cmd/compile/internal/test` | generated module cannot import a `cmd/compile/internal` dependency | per-origin library emission plus `go test -overlay` | moved to backend seam; lower prerequisite fixed in `8c91e4e0` |
| `package:cmd/compile/internal/typecheck` | generated module cannot import a `cmd/compile/internal` dependency | per-origin library emission plus `go test -overlay` | moved to backend seam; lower prerequisite fixed in `8c91e4e0` |
| `package:cmd/compile/internal/types` | generated module cannot import a `cmd/compile/internal` dependency | per-origin library emission plus `go test -overlay` | moved to backend seam; lower prerequisite fixed in `8c91e4e0` |
| `package:cmd/compile/internal/types2` | generated module cannot import a `cmd/compile/internal` dependency | per-origin library emission plus `go test -overlay` | moved to backend seam; lower prerequisite fixed in `8c91e4e0` |
| `package:cmd/internal/testdir` | generated module cannot import an `internal` dependency | per-origin library emission plus `go test -overlay` | moved to backend seam; lower prerequisite fixed in `8c91e4e0` |
| `package:go/types` | generated module cannot import an `internal` dependency | per-origin library emission plus `go test -overlay` | moved to backend seam; lower prerequisite fixed in `8c91e4e0` |
| `package:internal/types/errors` | generated module cannot import an `internal` dependency | per-origin library emission plus `go test -overlay` | moved to backend seam; lower prerequisite fixed in `8c91e4e0` |

## Requests to other seams

### Bashy transpile caller

Add a Go-only library-output flag whose output operand is a directory. Its
core caller diff is:

```diff
 opts.Package = goProg.Package
+opts.Library = goLibrary
 res, err := lower.Compile(file, opts)
@@
+if goLibrary {
+    for _, generated := range res.Files {
+        outputPath := filepath.Join(output, filepath.Base(generated.Name))
+        // Use the existing atomic output writer for source and its map.
+        // Reject duplicate basenames before writing any output.
+        writeGeneratedLibraryFile(outputPath, generated, goProg)
+    }
+    return 0
+}
 return writeOutputsAtomic(output, res.Source, mapFile, mapData)
```

The flag parser must require `--source=go`, reject `--go-package` flattening,
permit an existing output directory, and keep `GoFiles+TestGoFiles` and
`XTestGoFiles` as separate `gosource.Load` units so the second call passes
`Package: p_test`. Those loads set
`gosource.Options.PreserveNativeInit = true`. Each generated map uses
`FileResult.Mappings`; a duplicate
or unresolved source basename fails before any write.

### Package frontend and backend

The external-test unit must retain the tested package as an import edge rather
than flattening an explicit package map. The backend then enumerates all three
cmd/go file classes, requires one overlay replacement per original tested
source, and runs `go test -overlay` at the original import path. It must record
the overlay inventory and compile argv proof required by the D1 design note.
No package-root leaf is meaningful until both integrations select
`Options.Library`; compiling `Result.Source` in library mode intentionally
produces no bytes.
