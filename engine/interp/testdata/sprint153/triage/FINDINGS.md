# Sprint 153 · S153.4 — Interpreted-evaluator triage (FINDINGS)

Story-ID: e58cccba74f8

Triage lane: reproduce interpreted-evaluator failures for the corpus roots below.
No product edits. For each genuine interp-only failure a reduced, outside-corpus Go
program (≤30 lines) that shows the same class of failure is saved under
`<root-basename>/main.go`; each reduced program **passes natively** (`go run`).

Method per root:
- Native oracle: `go run <corpus-file>` (timeout 70).
- Interpreted: `bashy.real --bashpp --source=go --go-file <corpus-file>` (timeout 70).
- `--check` was clean (exit 0) for every silent exit-2 root, so those failures are at
  interpreter **runtime**, not in the lower/gosource check phase.

Legend for "stdout": MATCH = interpreted stdout equals native stdout; DIFF = differs.

## Summary table

| Root | Native (exit, first line) | Interpreted (exit, first line) | stdout | Reduced reproducer | Suspected mechanism |
|------|---------------------------|-------------------------------|--------|--------------------|---------------------|
| fixedbugs/bug260.go | 0, `OK` | 1, stdout `FAIL` (stride ≠ elem size) | DIFF | bug260/main.go | array-element `&a[i]` `%p` pointer stride |
| fixedbugs/issue24491a.go | 0, (none) | 2, `BASHPP-EEXPR-FORM: unsupported scalar expression *syntax.BashPPAddressExpr` | MATCH | issue24491a/main.go | `unsafe.Pointer`/`uintptr` address round-trip |
| fixedbugs/issue45045.go | 0, (none) | 2, `scalar call interrupted` (reduced: `gosource: original callback signature requires value-semantics parameters`) | MATCH | issue45045/main.go | `runtime.SetFinalizer` callback / GC |
| fixedbugs/gcc65755.go | 0, (none) | 2, (silent) | MATCH | gcc65755/main.go | `reflect.TypeOf` on func/method-local type |
| fixedbugs/issue16331.go | 0, (none) | 1, `BASHPP-EEXPR-NIL: nil is not a scalar` | MATCH | issue16331/main.go | `reflect.MakeFunc` / typed-nil func value |
| typeparam/issue48645a.go | 0, `func(func(int) bool)` | 2, `gosource: asynchronous or retained original function callbacks are unsupported for reflect.TypeOf` | DIFF | issue48645a/main.go | `reflect.TypeOf` on a Go func value |
| fixedbugs/issue10353.go | 0, (none) | 1, `bash++: task failed: exit status 1` | MATCH | issue10353/main.go | bound method value passed as func across goroutine |
| typeparam/boundmethod.go | 0, (none) | 2, `BASHPP-ESELECTOR-TYPE: local v has no typed selector path` | MATCH | boundmethod/main.go | method expression `T.String` on type parameter |
| typeparam/issue48042.go | 0, (none) | 2, `BASHPP-EPOINTER-TYPE: undefined type: T` | MATCH | issue48042/main.go | `*T`/`new(T)` in a generic method (type-param pointer) |
| typeparam/cons.go | 0, (none) | 2, `scalar call interrupted` (reduced: `BASHPP-EINTERFACE-MISSING: List does not implement interface (missing method Match)`) | MATCH | cons/main.go | type-assert to instantiated generic interface / generic-interface method resolution |
| fixedbugs/issue13160.go | 0, (none) | 2, `scalar call interrupted` | MATCH | issue13160/main.go | many goroutines racing shared pointer arena (scheduler) |
| fixedbugs/issue69507.go | 0, (none) | 2, (silent); reduced: exit 0 wrong output (`0` vs `2`) | MATCH/DIFF | issue69507/main.go | range-over-func iterator (`Seq[V] func(yield ...)`) |
| fixedbugs/issue23188.go | 0, (none) | 0, (none) | MATCH | — (PASSES) | LHS index eval order — supported (positive control) |
| fixedbugs/issue42401.go | 1, `package command-line-arguments is not a main package` | 2, `Go execution requires package main with func main()` | MATCH | — (native also fails) | `// rundir` multi-file test, not single-file runnable |
| fixedbugs/issue47087.go | 1, `package command-line-arguments is not a main package` | 2, `Go execution requires package main with func main()` | MATCH | — (native also fails) | `// rundir` multi-file test |
| fixedbugs/issue52856.go | 1, `package command-line-arguments is not a main package` | 2, `Go execution requires package main with func main()` | MATCH | — (native also fails) | `// rundir` multi-file test |
| fixedbugs/issue29919.go | 1, `package command-line-arguments is not a main package` | 2, `Go execution requires package main with func main()` | MATCH | — (native also fails) | `// rundir` multi-file test |
| fixedbugs/bug510.go | 1, `package command-line-arguments is not a main package` | 2, `Go execution requires package main with func main()` | MATCH | — (native also fails) | `// rundir` multi-file test |
| fixedbugs/issue47928.go | 1, `panic: FAIL` | 2, `panic: FAIL` | MATCH | — (native also fails) | `// run -goexperiment fieldtrack`; experiment not enabled → both fail |

## Grouped by mechanism

### G0 — Not genuine interpreter failures (native also fails; no reduction)
- **rundir harness**: issue42401, issue47087, issue52856, issue29919, bug510.
  These are `// rundir` multi-file tests; a single-file `go run` fails with
  "not a main package" and the interpreter refuses for the same reason. Same on both.
- **goexperiment fieldtrack**: issue47928. `// run -goexperiment fieldtrack`; without the
  experiment the native binary also `panic: FAIL`. Same on both.

### G1 — reflect over Go func/type values
- gcc65755 — `reflect.TypeOf` on a method-local struct type (silent exit 2).
- issue16331 — `reflect.MakeFunc` + typed-nil `(func())(nil)` → `EEXPR-NIL nil is not a scalar`.
- issue48645a — `reflect.TypeOf(closure).String()` → gosource: retained func callback unsupported.
Common thread: the interpreter has no faithful `reflect` view of Go function/closure
values and locally-defined types.

### G2 — unsafe pointers, finalizers, and the memory/address model
- issue24491a — `uintptr(unsafe.Pointer(&x))` round-trip → `EEXPR-FORM ... BashPPAddressExpr`.
- issue45045 — `runtime.SetFinalizer` callback → callback value-semantics unsupported.
- bug260 — `&arr[i]` `%p` addresses do not have real element-size stride (prints FAIL).
Common thread: simulated pointers/addresses (no real `fork`, no real memory layout) —
address arithmetic, `unsafe` round-trips, and finalizer callbacks are not modelled.

### G3 — bound method values / method expressions
- issue10353 — bound method value `x.foo` passed as a `func()` across a goroutine.
- boundmethod — method expression `T.String` where `T` is a type parameter
  (`ESELECTOR-TYPE: local v has no typed selector path`).
Common thread: a method turned into a first-class func value loses its typed receiver path.

### G4 — generics: type-parameter pointer & interface resolution
- issue48042 — `*T` / `new(T)` inside a generic method → `EPOINTER-TYPE: undefined type: T`.
- cons — type assertion to an instantiated generic interface `List[a]` in a recursive
  generic `Map` → `EINTERFACE-MISSING: List does not implement interface`.
Common thread: an unresolved type parameter used as a pointer target / interface instance.

### G5 — concurrency & range-over-func iterators
- issue13160 — many goroutines racing a shared pointer arena → `scalar call interrupted`.
- issue69507 — range-over-func iterator (`Seq[V] func(yield func(V) bool)`); reduced case
  produces the wrong result (empty map) instead of iterating.

### Positive control (no failure)
- issue23188 — left-hand-side index evaluation order; interpreted output matches native.

## Notes / needs (no product edits made)
- Reduced-case diagnostics sometimes differ from the corpus-root diagnostic (issue45045,
  cons, issue69507): the reduction isolates the same *class* of construct but the
  interpreter surfaces a nearer error (or, for issue69507, a wrong result rather than the
  silent exit 2 seen on the full program). This is called out inline in the table.
- All reduced reproducers were confirmed to pass under native `go run` (`println` in
  issue10353 writes "ok" to stderr, exit 0).
- Fixes are out of scope for this lane. Several groups (G1, G2) likely require gosource/lower
  seam work (Sprint 152 territory) rather than `interp/` alone — flagged here per the ground
  rules, no lower/gosource edits attempted.

## Commands used
```
# lane-local bashy
awk -v ws="$WS" '{sub(/=> \.\.\/sh$/, "=> " ws)}1' bashy/go.mod > $WS/.bashy-build.mod
cp bashy/go.sum $WS/.bashy-build.sum
(cd bashy && go build -modfile=$WS/.bashy-build.mod -o $WS/bashy.real ./cmd/bashy)
# per root
go run <corpus-file>                                           # native oracle
bashy.real --bashpp --source=go --go-file <corpus-file>       # interpreted
bashy.real --bashpp --source=go --check --go-file <corpus-file>  # check phase (clean)
```
