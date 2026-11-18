# Sprint 162 interp-control-4 findings

Lane: S162.1 cluster set C, fourth pass — select/recover/panic path, const
exprs. Seam: sh/interp (bashpp_*.go). Every fix starts from an
outside-corpus reproducer under `interp/testdata/sprint162/interp-control-4/
<mechanism>/` with the expected output taken from gc, plus its negative set.
Local runs are darwin spot checks with a `bashy.real` built against this
tree; the leaf manifests are the evidence.

| root | first cause | mechanism | status |
|---|---|---|---|
| `testdir:ken/chan.go` | struct storage is one Go map shared between tasks; distinct-field writes from two tasks (legal Go) crash the runtime's concurrent-map check | storage lock (`struct_field_race/`) | fixed in `9ce5059b` |
| `testdir:fixedbugs/issue26094.go` | (a) a failed one-result assertion was a fatal diagnostic, not a recoverable `*runtime.TypeAssertionError`; (b) function-local types with one spelling were one type, so `X.(T)` never panicked with `(types from different scopes)`; (c) `panic(nil)` printed `panic: nil` | runtime error panics (`runtime_error/`); local type identity (`type_scope/`) | fixed in `888c5b02` + `519d193d` |
| `testdir:interface/noeq.go` | a nil `func()` variable passed to an `interface{}` parameter travelled as text, so its dynamic type was `string` and `x == x` compared strings | func values into interfaces (`func_interface/`) | fixed in `6c5ccfa1` |
| `testdir:fixedbugs/issue43942.go` | recovering `defer panic(3)` left the aborted panic 2 on the chain; a helper called from a cleanup was treated as abandoned | frame-scoped panic unwinding (`aborted_panics/`) | fixed in `0cb9eeba` |
| `testdir:fixedbugs/issue48898.go` | same as issue43942 (`defer print(x)` lost, `recover().(int)` saw 2 for 4) | frame-scoped panic unwinding | fixed in `0cb9eeba` |
| `testdir:recover.go` | (a) `return recover()` refused; (b) `defer recover()` inside a deferred function did not squelch; (c) a deferred method that survived a nested panic in a callee was halted before its own recover; (d) tests 9–14 use `reflect.ValueOf(v).Method(i)` on interpreter-owned values | (a)–(c) `0cb9eeba`, `0ee108f4`, `c833f79f`; (d) is the bridge seam | (a)–(c) fixed; passes with the reflect tests skipped (`GOSSAINTERP=1`); (d) **moved to S162.3-A bridge**: `gosource: dependency mutation of interpreter-owned references is unsupported for Method` |
| `testdir:recover1.go` | `v = recover()` refused; a second recover in the same deferred call saw the outer frame's panic; `defer recover()` with nothing to take counted as a failed cleanup (exit 1) | frame-owned recover (`aborted_panics/frame_owned.go`) | fixed in `c833f79f` |
| `testdir:fixedbugs/bug401.go` | `return complex(1, 0)` — a predeclared value builtin (`complex`) in return position is refused as an undeclared callable (`BASHPP-ERETURN-CALL`); not the recover form | builtin-type / complex cluster | moved to the builtin-type lane |
| `testdir:fixedbugs/issue8613.go`, `issue1304.go`, `issue10486.go`, `convert5.go`, `divmod.go`, `recover3.go`, `fixedbugs/issue27961.go` | integer division by zero was a fatal diagnostic, not `runtime error: integer divide by zero` | runtime error panics | issue8613, issue1304, issue10486 pass locally; convert5/divmod/recover3/issue27961 have a further first cause (below) |
| `testdir:fixedbugs/issue27718.go` | float division by zero must yield ±Inf/NaN; the scalar evaluator is `go/constant` and cannot represent them | design: IEEE float arithmetic for runtime float values | **design finding**, not fixed |
| `testdir:recover3.go` | after the divide fix: nil pointer dereference and index out of range are still fatal diagnostics | runtime error panics — hook needed at the nil/pointer and collections lanes' sites (request below) | moved to the nil/pointer and collections lanes |
| `testdir:recover2.go` | `BASHPP-ECOLLECTION-BOUNDS` fatal diagnostic; Go panics `runtime error: index out of range [123] with length 10` | same request | moved to the collections lane |
| `testdir:divmod.go` | after the divide fix it runs but exceeds 60 s locally (exhaustive loops over the divide/modulo table) | per-call cost | **design finding** for S162.3-B (deadline family), not a knob |
| `testdir:convert5.go` | after the divide fix: exit 2 with no output — a later first cause in the conversion family | not investigated (expression forms) | moved to the expression-forms lane |
| `testdir:fixedbugs/issue27961.go` | `BASHPP-ECOLLECTION-ELEMENT` wraps a float division (`1.0 / v.D()`) — the float division-by-zero design finding above | see issue27718 | design |
| `testdir:fixedbugs/bug331.go`, `issue11369.go`, `issue58671.go`, `issue22326.go`, `noinit.go` | `var _ = …` spelled twice in one block was `_ redeclared in this block` | blank identifier (`blank_decl/`) | issue11369, issue22326, noinit pass locally (fixed in `13c39c66`); bug331 (`cannot convert String to float64` — a string constant converted to float64 in a tuple-returning func value) and issue58671 (generic variadic inference `g(1, 'a', 2.3)`) have further first causes in the expression-forms / generic-body lanes |
| `testdir:fixedbugs/bug191.go` | `Go execution requires package main with func main()` — a `rundir` root whose package is not main | harness/backend seam (rundir) | moved to the backend lane |
| `testdir:chancap.go`, `fixedbugs/bug279.go`, `bug292.go`, `bug339.go`, `bug479.go`, `bug517.go`, `issue15550.go`, `issue30709.go`, `issue53137.go`, `issue54220.go`, `issue60601.go`, `issue9604b.go` | `unsafe.Sizeof/Offsetof/Alignof` (and `unsafe.SliceData`, `issue57823`/`issue59293`) are constant expressions go/types has already evaluated; the interpreter has no memory layout and refuses them (`unknown imported symbol`, `BASHPP-ECONST-EXPR`) | constant folding of `unsafe` calls at the converter | **request to the gosource seam** (below); not fixed here |
| `testdir:fixedbugs/issue11945.go` | `const _ = real(0)` — a constant builtin call the const predicate does not model | const predicate: `real`, `imag`, `complex`, `len`/`cap` of constants, `min`/`max` | not fixed (budget); same converter folding request covers it |
| `testdir:fixedbugs/issue6866.go` | a package-level const group references constants declared in a LATER group; Go resolves package-level declarations in dependency order, the evaluator in source order | dependency-ordered package-level constant initialization | **design finding**; the converter is the natural place (request below) |
| `testdir:fixedbugs/issue8047.go` | `defer ((func())(nil))()` — a nil func value was deferred silently and never called; `f()` on a nil func variable was `unknown imported symbol or method: f` | nil function call (`nil_func_call/`) | fixed in `d55bd493` |
| `testdir:fixedbugs/issue70156.go`, `bug434.go`, `typeparam/issue48225.go` | `reflect` on interpreter values / other | bridge / other lanes | not mine |

## Requests to other seams

### gosource (converter): fold checker-evaluated constant expressions

go/types records a constant value (`types.Info.Types[expr].Value`) for
`unsafe.Sizeof(x)`, `unsafe.Offsetof(x.f)`, `unsafe.Alignof(x)`, `real(c)`,
`imag(c)`, `len(constArray)` and every other constant expression, using
`types.SizesFor("gc", GOARCH)`. The interpreter has no memory layout and
cannot answer `unsafe.Sizeof` for an arbitrary expression. Request: when
converting an original Go program, emit the recorded constant value as a
typed literal for a call of `unsafe.Sizeof/Offsetof/Alignof` and for a
`real`/`imag`/`complex`/`len`/`cap` call whose TypeAndValue carries a
constant. This closes the `unsafe.*` cluster (12 roots) and issue11945
without a second layout implementation. Same place for issue6866:
package-level `const`/`var` groups emitted in dependency order (the order
the spec defines) rather than source order.

### nil/pointer lane and collections lane: raise runtime errors

The mechanism landed in `888c5b02` (`bashpp_sprint162_runtime_error.go`):

    return …, r.bashPPRaiseRuntimeError(bashPPRuntimeErrorString,
        bashPPRuntimeErrorMessage+"invalid memory address or nil pointer dereference")

    return …, r.bashPPRaiseRuntimeError(bashPPRuntimeBoundsError,
        bashPPRuntimeErrorMessage+fmt.Sprintf("index out of range [%d] with length %d", i, n))

    // nil map write: type runtime.plainError, no "runtime error: " prefix
    return …, r.bashPPRaiseRuntimeError(bashPPRuntimePlainError,
        "assignment to entry in nil map")

gated on `r.bashPPGoSource`, at the sites that today return
`BASHPP-ENIL-DEREF`, `BASHPP-ECOLLECTION-BOUNDS` and `BASHPP-ENIL-MAP`. The
returned sentinel is `errBashPPScalarInterrupted`; every consumer that
prints an error from these paths must treat that sentinel as "already
unwinding" (the assignment, short-declaration and tuple consumers already
do after `888c5b02`). recover2.go and recover3.go then pass end to end
(recover3 verified locally up to its nil-deref check).

### gosource (test): TestMultipleFileRuntimeAndLowerPositions

After `888c5b02` an integer division by zero in an original Go program is
a Go panic with a traceback, not a positioned fatal diagnostic, so
`gosource/source_test.go:184` — which asserts the old form — fails on
darwin with the panic report (the position is still named, in gc's
traceback shape `\tb.go:3`). The test's assertion needs the exact diff
below; the source name of a multi-file panic site is resolved through
`File.SourceAt` (`bashPPPanicTrace`), so the position it checks is there:

    -	if err = r.Run(context.Background(), p.File); err == nil || !strings.Contains(err.Error()+stderr.String(), "b.go:3:") {
    +	if err = r.Run(context.Background(), p.File); err == nil || !strings.Contains(err.Error()+stderr.String(), "b.go:3") {

This is the only `go test -short ./...` failure outside the pre-existing
darwin set, and it is a test of the old diagnostic, not of Go behaviour.

### bridge lane (S162.3-A)

`reflect.ValueOf(v).Method(i)` / `reflect.TypeOf(v).Method(i).Func` on an
interpreter-owned value (recover.go tests 9–14): `gosource: dependency
mutation of interpreter-owned references is unsupported for Method`.

### Observed, not in this lane

- `ls[0].rv += v` (op-assign through an element of a slice of pointers)
  reports `BASHPP-EUPDATE-TYPE: BASHPP-EPOINTER-TARGET: pointer field path
  no longer names struct storage`; `[]*T{{1}, {2}}` (elided `&T` in a
  composite literal) reports `BASHPP-ESTRUCT-TYPE: <inferred> is not a
  supported struct type`; `var x interface{} = g` for a declared function
  `g` reports `BASHPP-EINTERFACE-VALUE: undefined value g`, and `f = g`
  (assigning a declared function to a func-typed variable) reports
  `BASHPP-EASSIGN-UNDECLARED: RHS g is not declared`. All are
  expression-forms / nil-pointer lane shapes (a declared function as a
  value).
- `fmt.Printf("%T", x)` for an interface holding a nil func prints
  `string`; the bridge has no func handle for a nil func value.

## Residual risks of the storage lock

`expand.ObjectString` (JSON rendering of a struct for `${var}`) iterates the
map by reflection outside the lock; a shell-level render of a struct that
another task is writing concurrently is still a runtime map race. No
original Go program reaches that path (fmt goes through the bridge, which
snapshots under the lock).
