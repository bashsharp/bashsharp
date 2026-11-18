# S153.4a — dependency-bridge value shapes (bridge owner)

Sprint: #153 · Story: S153.4 · Story-ID: e58cccba74f8

Each root below is listed with its mechanism class and either the commit that
closed the bridge-side refusal, or the reason it stays open. "Closed" means the
bridge no longer refuses the value shape; several roots then fail further
downstream in evaluator / lower / gosource lanes that this lane does not own —
those residual causes are named so the owning lane can pick them up.

## Commits (mechanism → subject)

- C1 `interp: materialise locally declared interfaces with method elements`
- C2 `interp: refuse constant-expression array lengths in helper local types`
- C3 `interp: keep the helper's materialised local-type set dependency-closed`
- C4 `interp: transport unmaterialised array values by realised structural spelling`
- C5 `interp: materialise instantiated local generic types in the dependency helper`
- C6 `interp: reconcile direct original slice buffers by observed call behaviour`

## Cluster 1 — worker did not compile ("build dependency bridge: exit status 1")

The generated worker program no longer fails to compile for any of these roots;
the export-table / local-type generator emits only declarations the helper can
build. Residual diffs are downstream of the bridge.

| root | class | status |
|------|-------|--------|
| abi/idata.go | worker-compile: interface with method element (`Value`) → uncompilable struct field | **C1** closed. Then fails in evaluator: BASHPP-EEXPR-UNDEFINED (computed function `complexVal`/`floatVal`), NOT bridge — evaluator lane. |
| chan/powser2.go | worker-compile: interface with method element (`item`) | **C1** closed. Then BASHPP-ENIL-DEREF at powser2.go:133 — evaluator lane. |
| fixedbugs/issue45851.go | worker-compile: interface with method element (`Value`) | **C1** closed → MATCH native. |
| fixedbugs/issue11286.go | worker-compile: array length is a const name (`[D]float64`) | **C2** closed → MATCH native. |
| fixedbugs/issue20780b.go | worker-compile: array-alias length is a const expr (`type Big = [N]int`, N=2e6) | **C2** closed. Then panic `interface conversion: expand.invalidObject` — evaluator lane. |
| fixedbugs/issue71932.go | worker-compile: array length is a const expr (`[C*C]byte`) | **C2** closed → MATCH native. |
| fixedbugs/bug517.go | worker-compile: array length is `unsafe.Sizeof(...)` | **C2** closed (type no longer materialised). Then BASHPP-ECOLLECTION-LENGTH: the evaluator cannot compute `unsafe.Sizeof` as an array length — OPEN, evaluator/`unsafe` compile-time operator, not a bridge value shape. |
| sizeof.go | worker-compile: many function-local `T1`/`T`/`T2` reused across scopes referenced uncompilable siblings | **C3** closed (materialised set now dependency-closed). Then `unknown imported symbol or method: unsafe.Sizeof` — OPEN. See "unsafe compile-time operators" below. |
| typeparam/struct.go | worker-compile: struct embedding instantiated-generic aliases (`Eint = E[int]`) | **C3** closed (unmaterialisable aliases/embeds dropped, closure enforced). Then BASHPP-ESTRUCT-TYPE `cannot use Eint as E[int]` — evaluator/struct-embedding of generic aliases, NOT bridge. |
| blank.go | not a bridge value shape | OPEN: `gosource: package-level name T._ declared twice in the lowered file` — gosource/lower seam (Sprint 152), not `interp/`. Blank method/field names collide in the lowered file before the bridge is reached. |
| fixedbugs/issue20014.go | rundir recipe | OPEN: `runindir` multi-file package with `-goexperiment fieldtrack`; program-loading/import-resolution, not a bridge value shape. |

## Cluster 2 — unregistered bridge type

Instantiation / array / embedding shapes now transport by structure.

| root | class | status |
|------|-------|--------|
| typeparam/issue50419.go | instantiated local generic `*Foo[string, int]` with mirrored `String` | **C5** closed → MATCH native. |
| (array `[N]int`, `[C*C]byte` values) | value of unmaterialised named array type | **C4** closed — value crosses under realised structural spelling `[n]T`. |
| typeparam/issue50481b.go | linked-package generic (`b.Foo[string,int]`) in a `.dir` | OPEN: `could not import ./b (relative import path requires an import base)` — rundir/linked-package import resolution, gosource/loader seam, not a bridge value shape. Same for issue50481c, issue51219, issue21120. |
| typeparam/issue50481c.go | linked-package generic | OPEN (as above). |
| typeparam/issue51219.go | linked-package type `__gosource_pkg_0_T` | OPEN (as above) — the linked-package machinery, not the bridge. |
| fixedbugs/issue21120.go | linked-package types across a/b/main | OPEN (as above). |

`*<unsupported embedded field>` placeholder (bashpp_native_values.go
bashPPBridgeTypeText): embedded fields still render a placeholder rather than a
real type. Not exercised to a green root here; recorded for S153.2 (embedded
field transport needs the struct renderer to emit the embedded type by
structure, and the codec to read/write the promoted storage).

## Cluster 3 — native slice retention or mutation unsupported

Replaced the per-name policy with mechanism classes (C6):
- **read-only consumer**: elements handed back unchanged → nothing written back.
- **in-place mutator**: changed elements written back over the visible length.
- **retained-by-native**: storage the buffer writeback cannot reach (a slice
  view nested in another value, a slice-view receiver, or storage without slice
  identity) keeps the same refusal message. `TestGoSourceBridgeSliceRetainedRefusal`
  proves it is refused promptly, not hung.

| root | class | status |
|------|-------|--------|
| inline_callers.go | in-place mutator (`runtime.Callers(skip, pcs)` fills the buffer) | **C6** closed — no longer refused; buffer written back. Residual: the reported PCs are the interpreter's reflect-dispatch frames (`reflect.Value.Call`, `main.dispatch`) not the original call frames — interpreter execution-model difference, out of bridge scope. |
| fixedbugs/issue7690.go | in-place mutator (`runtime.Stack(buf, false)`) | **C6** closed → MATCH native. |
| typeparam/map.go | read-only consumer (`reflect.DeepEqual(got, want)`) | **C6** closed — DeepEqual no longer refused. Then BASHPP-ECOLLECTION-ELEMENT `cannot use string value as float64` building a map — evaluator/collection lane. |
| typeparam/mapimp.go, stringerimp.go, issue48462.go | read-only consumer via `.dir` | OPEN: `could not import ./a` — rundir/linked-package import resolution, not the slice policy. |
| fixedbugs/issue29919.go, issue19467.go | `.dir` linked packages | OPEN (as above). |
| os/exec.Command, io/ioutil.WriteFile, unsafe.SliceData (from story list) | read-only consumer / retained | Not reproduced to a green root in this pass; the reconciled-buffer path admits read-only consumers generally. `unsafe.SliceData` is an `unsafe` compile-time operator (see below). |

## Cross-cutting OPEN items to hand off

- **unsafe compile-time operators** (`unsafe.Sizeof`, `unsafe.Alignof`,
  `unsafe.Offsetof`, `unsafe.SliceData`): these are not reflectable imported
  symbols; the worker cannot dispatch them and it is wrong to. They must be
  computed by the evaluator at the call site from the operand's type. Roots:
  sizeof.go, bug517.go (array length). **Evaluator lane**, not bridge.
- **`.dir` / rundir linked packages**: `./a`, `./b` relative imports fail at
  loader time before any bridge request. gosource/loader seam. Roots:
  issue50481b/c, issue51219, issue21120, mapimp, stringerimp, issue48462,
  issue29919, issue19467, issue20014.
- **embedded-field transport** (`*<unsupported embedded field>`): S153.2. The
  struct renderer and codec need to carry an embedded field by its structural
  type and read/write the promoted storage.
- **callbacks / methods-on-original-types / dependency-owned writer rows**
  (S153.2, deferred by the story): the instantiated-generic mirrored-method
  path (C5) reuses the existing local-callback ownership; a *retained* callback
  on an instantiated generic, an async function callback, and a dependency that
  writes back through a retained interpreter aggregate still need the protocol
  extension S153.2 will bring (a session-scoped writer identity for
  interpreter-owned aggregates, distinct from the pointer-origin writeback).

## Bonus closure

`TestGoSourceEmbeddedImportedMethodLocalCodecGap` documented a gap (a local
type referencing another local type that embeds an imported field —
holder → box{sync.Mutex} — emitted an undefined `box` into the worker). The
dependency-closure fix (C3) drops the referencing type instead, so both the
promoted and explicit spellings now match native Go in all three modes. The
test's own instruction was to promote it to a three-mode case once it passed;
done, renamed to `TestGoSourceEmbeddedImportedMethodLocalCodec`.

## Gates run

- `go test -count=1 -timeout 30m -run 'GoSource' ./interp/...` before first edit:
  8 known darwin failures recorded (Tour callback boundaries, retained-callback
  policy, image retained-consumer boundary, native-read callback alias, nil
  aggregate func/channel fields, receive channel storage, TCP scratch SIGTERM,
  unwrap pointer reentry). Change must not add to that list.
- Per-mechanism: `go test ./interp -run TestGoSourceBridge... -v` (all pass).
- Full `go test -count=1 -timeout 30m ./interp/...` and
  `go test -short -timeout 30m ./...` before declaring done.
