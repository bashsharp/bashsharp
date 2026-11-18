# Sprint 165 interp-const findings

Lane: `interp-const`, story #97. The constant-folding mechanism is restricted
to original Go programs; Classic Bash++ exact-rational rendering is an explicit
negative gate. Run 0 reproduced Barrier C's first lines for every row below;
no row changed owner before this mechanism.

| root | first cause | mechanism | status |
|---|---|---|---|
| `testdir:chancap.go` | `unsafe.Sizeof((*byte)(nil))` was rejected as a const initializer | declared-type `unsafe.Sizeof` fold | fixed in this commit; leaf pending |
| `testdir:fixedbugs/bug279.go` | scalar `unsafe.Sizeof`/`Alignof` calls were sent to the dependency bridge | declared-type unsafe constant fold before bridge dispatch | fixed in this commit; leaf pending |
| `testdir:fixedbugs/bug292.go` | scalar `unsafe.Offsetof` had no evaluator implementation | struct field layout through `go/types.Sizes` | fixed in this commit; leaf pending |
| `testdir:fixedbugs/bug339.go` | `unsafe.Sizeof` of an interface value was sent to the dependency bridge | declared interface layout | fixed in this commit; leaf pending |
| `testdir:fixedbugs/bug479.go` | a directory companion's unsafe call was rejected as a const initializer | GoSource-only constant call predicate and fold | fixed in this commit; leaf pending |
| `testdir:fixedbugs/bug517.go` | package-level array length `unsafe.Sizeof(F())` was evaluated before `F` registered | declared function signature scan plus existing array-length fold | fixed in this commit; leaf pending |
| `testdir:fixedbugs/issue11945.go` | `real` and `imag` were evaluated but rejected by the const-expression predicate | constant builtin predicate | fixed in this commit; leaf pending |
| `testdir:fixedbugs/issue15550.go` | `unsafe.Sizeof(func literal)` was rejected by the const-expression predicate | declared function-value layout | fixed in this commit; leaf pending |
| `testdir:fixedbugs/issue30709.go` | repeated package and local const specs containing `unsafe.Sizeof(func literal)` were rejected | GoSource-only constant call predicate and fold | fixed in this commit; leaf pending |
| `testdir:fixedbugs/issue53137.go` | promoted generic struct field `Offsetof` was sent to the dependency bridge | instantiated declared struct layout | fixed in this commit; leaf pending |
| `testdir:fixedbugs/issue54220.go` | `Offsetof` requires the private layout of an imported atomic type | imported declared-type layout is absent from the interpreter registry | unresolved in this mechanism; see request |
| `testdir:fixedbugs/issue57823.go` | after `unsafe.SliceData`, the root depends on finalizer observation of interpreter-owned storage | collector-visible native memory | by-ID D9 |
| `testdir:fixedbugs/issue59293.go` | `unsafe.SliceData` and `unsafe.StringData` are value builtins, not constant folds | unsafe value builtins | not fixed in constant-folding mechanism |
| `testdir:fixedbugs/issue60601.go` | `unsafe.Sizeof(*new(T))` requires instantiated type-parameter layout and then runtime divide-by-zero behavior | generic declared layout | not fixed in constant-folding mechanism |
| `testdir:fixedbugs/issue6866.go` | a package const depends on a const declared in a later declaration group | dependency-ordered package initialization | design recorded by Sprint 162; compiled output row belongs to lower/runtime output triage |
| `testdir:fixedbugs/issue9604b.go` | `unsafe.Sizeof` reached scalar evaluation inside a collection element | declared-type unsafe constant fold before bridge dispatch | fixed in this commit; leaf pending |
| `testdir:sizeof.go` | scalar `unsafe.Sizeof` calls over declared local types were sent to the dependency bridge | declared-type unsafe constant fold | fixed in this commit; leaf pending |
| `testdir:typeparam/pairimp.go` | imported directory companion reaches `unsafe.Sizeof` over an instantiated local pair | instantiated declared layout | fixed in this commit; leaf pending |
| `testdir:unsafe_string.go` | `unsafe.String` is a value builtin over interpreter-owned byte storage | unsafe value builtin | not fixed in constant-folding mechanism |
| `testdir:unsafebuiltins.go` | most assertions reinterpret or derive native memory via `unsafe.Pointer`, `Add`, and `Slice` | native memory reinterpretation | by-ID D7 |

## Requests to other seams

- The GoSource converter already has the authoritative `go/types.Info` value
  for constants involving imported private-layout types. To close
  `fixedbugs/issue54220.go` without inventing an imported type layout in the
  interpreter, materialize checker-evaluated `unsafe.Sizeof`, `Alignof`, and
  `Offsetof` constants while retaining an import-use marker in generated Go.
  This is the converter request already recorded by Sprint 162; no evaluator
  approximation should replace it.
