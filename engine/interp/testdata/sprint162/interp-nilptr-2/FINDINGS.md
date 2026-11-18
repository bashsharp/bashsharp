# Sprint 162 — lane `interp-nilptr-2` findings

Lane: S162.1 cluster set A, second pass (the mechanism rows left by
`interp-nilptr/FINDINGS.md`). Seam: `sh/interp`. Story #91, Story-ID
e2a7c4ef43c9. Reproducers with their negative sets live beside this file,
one directory per mechanism; `bashpp_sprint162_nilptr2_test.go` drives them.

"local" below = the check-then-run form of the harness backend
(`--bashpp --source=go --check`, then run; output vs `.out`, or empty) on the
lane's darwin build of bashy; the leaf verdict column is the evidence.

## Mechanisms landed (one commit each)

| mechanism | commit | reproducer |
|---|---|---|
| typed float variable read back as a float (typed scalar storage kind; the exact `n/d` text of a rational float is read by one helper in the cell reader, the reused-declaration check and the parameter check) | `c29eb4c9` | `typedfloat/` |
| nil function value as an argument to a func-typed parameter | `a318475f` | `nilfunc/` |
| channel compared across its direction types by identity (worker `equal`) | `c1398f66` | `chandir/` |
| type parameters bound inside a computed callee (`reflect.TypeOf(new(T)).Elem()`) | `f16f4352` | `generic/` |
| promoted method through a nil pointer faults in expression position (a failed lookup that raised the panic is the interrupted expression, not an undefined callable) | `3f376f40` | `promoted/` |
| `_ = recover()` / `r = recover()` assign the recovered value | `c9f1c0d7` | `recoverassign/` |
| runtime stack introspection of interpreted frames: `runtime.Caller`, `runtime.FuncForPC(pc).Name/Entry/FileLine`, `runtime.Stack`, `debug.Stack`, `debug.PrintStack` answered from a frame table (call position + identity per Bash++ frame; fault snapshot spliced in while a deferred call runs for the panic, kept until the recovering frame returns); a fault is raised at the evaluated expression's line | `4da3d55a` | `stack/` |
| a deferred call running for a panic may start goroutines (task launch gate asks whether the panic halts the frame) | `35549f1d` | `unwindgo/` |
| named pointer type (`type PS *dch`) bound as a pointer in parameters and results | `e3d0cfce` | `namedptr/` |
| a closure made in an instantiated generic frame carries the instantiated signature as its dynamic type; the if statement's init clause and condition and the for condition are bound in generic frames (the follow-on of `f16f4352` that `TestSprint153Evaluator/interface_func_literal` caught) | `4f485f2e` | `generic/closure_instantiated_type.go` |

## Roots

| root | first cause | mechanism | status |
|---|---|---|---|
| `testdir:fixedbugs/bug347.go` | `runtime.Caller(i)` / `FuncForPC(pc).Name()` answered by the dependency; then the fault in a select case reported at the `select` line | stack introspection + fault at the expression's line | fixed in `4da3d55a` (local PASS) |
| `testdir:fixedbugs/bug348.go` | `runtime.Caller` answered by the dependency | stack introspection | fixed in `4da3d55a` (local PASS) |
| `testdir:fixedbugs/issue4562.go` | same | stack introspection | fixed in `4da3d55a` (local PASS) |
| `testdir:fixedbugs/issue27201.go` | `_ = recover()` refused (BASHPP-EASSIGN-CALL); then `runtime.Stack(buf[:], false)` answered by the dependency | recover assignment + stack introspection (runtime.Stack fills the caller's byte slice) | fixed in `c9f1c0d7` + `4da3d55a` (local PASS) |
| `testdir:fixedbugs/issue33724.go` | `o.NotExpectedInStackTrace()` through a nil `*Outer` reported "undefined callable o"; then `debug.Stack()` answered by the dependency | promoted nil receiver + stack introspection (debug.Stack returns a dependency-owned []byte) | fixed in `3f376f40` + `4da3d55a` (local PASS) |
| `testdir:fixedbugs/issue40629.go` | a `go` statement in a deferred call running for the panic launched nothing, so `<-c` waited to the bound | task launch during unwind | fixed in `35549f1d` (local PASS, well inside the bound) |
| `testdir:fixedbugs/issue23837.go` | `h(nil, nil)` with `p, q func() struct{}` refused by the argument check | nil func argument | fixed in `a318475f` (local PASS) |
| `testdir:fixedbugs/issue67190.go` | `switch ch1 { case ch2: }` across `chan T` / `<-chan T` compared unequal in the worker | channel identity across direction types | fixed in `c1398f66` (local PASS) |
| `testdir:literal.go` | `var f00 float32 = 3.14159` read back as a String, `-f00` refused; then `equal(f09, 1/f10)` failed the parameter check on the exact text | typed float storage | fixed in `c29eb4c9` (local PASS) |
| `testdir:method5.go` | fixed by the first pass (`c5ee56b1`); local PASS on this tree, included in the leaf for the verdict | — | leaf |
| `testdir:typeparam/mdempsky/15.go` | `reflect.TypeOf(new(T)).Elem()` reached the bridge as `*T` — fixed in `f16f4352`; the root then instantiates `T` with an anonymous interface type (`interface{ EBad() }`) and `new(T)` reaches the bridge as `*interface`: the worker's `resolveType` cannot build an interface type with methods (reflect has no InterfaceOf), and the helper's type registry does not register the program's anonymous interface types by their canonical spelling | bridge type registry: anonymous interface types with methods | moved to S162.3-A bridge lane — request 1 below |
| `testdir:chan/powser1.go` | the nil `PS` in `get` was a named pointer type bound without its pointer flag — fixed in `e3d0cfce`; the root then reaches `getn`'s select, whose `dat[i] = nil` elements (`case it = <-dat[0]:`) resolve to a channel with neither a native handle nor an interpreter channel, which `bashPPSelect` counts as a local case beside the native ones — `gosource: mixed native/interpreted channel select requires atomic arbitration` (chan/powser1.go:146). A nil channel case is never ready; it belongs in the native select as a nil channel, not as a local case | nil channel as a select case among native cases | moved to lane `select/concurrency` — request 2 below |
| `testdir:chan/powser2.go` | same named-pointer cause fixed; the root then needs `(<-in.dat).(*rat)` — a type assertion whose operand is a parenthesised receive expression (`BASHPP-ESELECTOR-EXPR: unsupported structured expression`; `v := <-in.dat; v.(*rat)` works) — and afterwards the same select shape as powser1 (`req := make([]chan int, 2)`) | receive expression as a type-assertion operand; select over channel elements | moved to lane `select/concurrency` — request 2 below |

## Design (recorded, not attempted)

- The 24 `unsafe.Pointer(&x)` memory-reinterpretation roots, `issue44830`
  (`unsafe.Pointer(nil)`), the GC / finalizer rows (`issue54343`,
  `issue15281`) and the `reflect.MakeFunc` rows stay as recorded in
  `interp-nilptr/FINDINGS.md`: a pure-Go evaluator with its own value model
  has no memory layout to reinterpret, no collector to drive, and the
  callback bridge does not retain interpreted functions for MakeFunc.
- Stack introspection reports interpreted frames only. Frames Go's runtime
  would show (`runtime.gopanic`, `runtime.main`, `runtime.goexit`) are not
  fabricated; a call spanning lines is placed at its statement's first line
  (Go reports the line of the call's parenthesis); a function literal is
  named `<enclosing frame>.funcN` with N counted among the literals seen on
  the stack rather than by the declaration's source order; `runtime.Callers`
  and `runtime.CallersFrames` are not answered (they fill and iterate
  caller-owned slices the frame table does not model) and keep the
  dependency path.

## Requests to other seams / lanes

1. **S162.3-A bridge lane** — register the program's anonymous interface
   types with methods (`interface{ EBad() }`, `interface{ XGood() }`) in the
   helper's type registry under their canonical `format.Node` spelling, so
   `resolveType` finds them; `bashPPBridgeTypeText` in
   `bashpp_native_values.go` must then spell a `*syntax.BashPPInterfaceType`
   with methods the same way (today it returns "" and the pointer text
   becomes `*interface`). `typeparam/mdempsky/15.go` also type-switches
   `interface{}(new(T)).(type)` on such types.
2. **lane `select/concurrency`** — (a) a nil channel as a select case
   among native cases (`dat[i] = nil`; `case it = <-dat[0]:`): in
   `bashPPSelect` (`bashpp_concurrency.go`, the `c.native != nil` /
   `hasLocal` classification) a channel operand with neither a native
   handle nor an interpreter channel is a nil channel — never ready — and
   should join the native select as a `nil` channel value rather than mark
   the select as mixed (chan/powser1.go:146, chan/powser2.go); (b) a receive
   expression as the operand of a type assertion, `(<-in.dat).(*rat)`
   (chan/powser2.go:108).
3. **lane `select/recover/const`** — a recovered runtime fault has dynamic
   type string: `r.(error)` on the value of `recover()` after a nil
   dereference reports `interface value has dynamic type string, not error`
   (Go: `runtime.Error`). Not needed by this lane's roots; noted from the
   `nilfunc` reproducer's first draft.
4. **integrator** — `4da3d55a` adds three fields to `callFrame` and three to
   `Runner` (`api.go`), one line in `bashPPEnterFrame`, one line each in
   `bashPPRaiseValue` and `bashPPRecover` (`bashpp_panic.go`), a claim in
   `bashPPBridgeCall` next to the sync/atomic claim
   (`bashpp_native_values.go`), and routes the four expression-scoped fault
   wrappers (`bashPPEvalScalarExpr`, `bashPPEvalTypedValue`,
   `bashPPReadExpr`, `bashPPPointerExprValue`, `bashPPAddress`) through
   `goSourceRuntimeFaultAt`. `c1398f66` is one clause in the worker's
   `equal` op (`bashpp_native_worker.go.txt`). `35549f1d` is one predicate
   in `gosource_task_arguments.go` (`bashPPPanicHalts` for `bashPPPanicking`).

## Pre-existing failures noted (darwin)

The 8 `GoSource*` tests named in the contract; see the report for the
package-run tally on this tree.
