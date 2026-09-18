# Sprint 162 Bashy Go-library caller findings

| root | first cause | mechanism | status |
|---|---|---|---|
| package:cmd/compile | no per-origin Go-library caller for overlay input | separate library load units and atomic per-origin output | backend overlay still required |
| package:cmd/compile/internal/abt | no per-origin Go-library caller for overlay input | separate library load units and atomic per-origin output | backend overlay still required |
| package:cmd/compile/internal/amd64 | no per-origin Go-library caller for overlay input | separate library load units and atomic per-origin output | backend overlay still required |
| package:cmd/compile/internal/base | no per-origin Go-library caller for overlay input | separate library load units and atomic per-origin output | backend overlay still required |
| package:cmd/compile/internal/compare | no per-origin Go-library caller for overlay input | separate library load units and atomic per-origin output | backend overlay still required |
| package:cmd/compile/internal/devirtualize | no per-origin Go-library caller for overlay input | separate library load units and atomic per-origin output | backend overlay still required |
| package:cmd/compile/internal/dwarfgen | no per-origin Go-library caller for overlay input | separate library load units and atomic per-origin output | backend overlay still required |
| package:cmd/compile/internal/importer | no per-origin Go-library caller for overlay input | separate library load units and atomic per-origin output | backend overlay still required |
| package:cmd/compile/internal/inline/inlheur | no per-origin Go-library caller for overlay input | separate library load units and atomic per-origin output | backend overlay still required |
| package:cmd/compile/internal/ir | no per-origin Go-library caller for overlay input | separate library load units and atomic per-origin output | backend overlay still required |
| package:cmd/compile/internal/liveness | no per-origin Go-library caller for overlay input | separate library load units and atomic per-origin output | backend overlay still required |
| package:cmd/compile/internal/logopt | no per-origin Go-library caller for overlay input | separate library load units and atomic per-origin output | backend overlay still required |
| package:cmd/compile/internal/loopvar | no per-origin Go-library caller for overlay input | separate library load units and atomic per-origin output | backend overlay still required |
| package:cmd/compile/internal/noder | no per-origin Go-library caller for overlay input | separate library load units and atomic per-origin output | backend overlay still required |
| package:cmd/compile/internal/rangefunc | no per-origin Go-library caller for overlay input | separate library load units and atomic per-origin output | backend overlay still required |
| package:cmd/compile/internal/reflectdata | no per-origin Go-library caller for overlay input | separate library load units and atomic per-origin output | backend overlay still required |
| package:cmd/compile/internal/ssa | no per-origin Go-library caller for overlay input | separate library load units and atomic per-origin output | backend overlay still required |
| package:cmd/compile/internal/ssagen | no per-origin Go-library caller for overlay input | separate library load units and atomic per-origin output | backend overlay still required |
| package:cmd/compile/internal/syntax | no per-origin Go-library caller for overlay input | separate library load units and atomic per-origin output | backend overlay still required |
| package:cmd/compile/internal/test | no per-origin Go-library caller for overlay input | separate library load units and atomic per-origin output | backend overlay still required |
| package:cmd/compile/internal/typecheck | no per-origin Go-library caller for overlay input | separate library load units and atomic per-origin output | backend overlay still required |
| package:cmd/compile/internal/types | no per-origin Go-library caller for overlay input | separate library load units and atomic per-origin output | backend overlay still required |
| package:cmd/compile/internal/types2 | no per-origin Go-library caller for overlay input | separate library load units and atomic per-origin output | backend overlay still required |
| package:cmd/internal/testdir | no per-origin Go-library caller for overlay input | separate library load units and atomic per-origin output | backend overlay still required |
| package:go/types | no per-origin Go-library caller for overlay input | separate library load units and atomic per-origin output | backend overlay still required |
| package:internal/types/errors | no per-origin Go-library caller for overlay input | separate library load units and atomic per-origin output | backend overlay still required |

## Requests to other seams

The backend seam must invoke the shipped library command with the GoFiles,
TestGoFiles, and XTestGoFiles file classes, build one overlay replacement for
each emitted original, and run `go test -overlay` at the original import path.
