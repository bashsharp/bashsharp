# Sprint 162 — lane `interp-nilptr` findings

Lane: S162.1 cluster set A (nil / pointer path, selector assignment, expression
forms). Seam: `sh/interp`. Story #91, Story-ID e2a7c4ef43c9.

Mechanisms landed (one commit each; reproducers with the negative set under
this directory):

| mechanism | commit | reproducers |
|---|---|---|
| nil dereference is Go's runtime panic (`runtime error: invalid memory address or nil pointer dereference`), recoverable (`3454f1fd` adds a nil pointer field on the way to a selector target); nil value receiver, nil interface method, nil func call, promoted method through nil embedded pointer; a frame called by a running deferred call keeps the panic running; a panic in call arguments does not fail the recovered frame; range / index over a pointer to an array | `ef3bde4f` | `nilderef/`, `rangeptr/` |
| nil as a first-class value: func-value and typed-nil comparison, value switch (func / map / slice / chan / pointer / interface / composite tag), `x = nil`, typed nil to a dependency, nil interface conversion, `recover()` as a native argument | `c95a77b7` | `nilvalue/` |
| composite literals compared and switched on, composite literal converted to a named struct/array type, `new(T)` and `any(new(T))` as operands, `f = v.M` method values | `c5ee56b1` | `exprform/` |

"local" below = the check-then-run form of the harness backend
(`--bashpp --source=go --check`, then run; combined output vs `.out`) on the
lane's darwin build; the leaf verdict column is the evidence. Only `run`
recipes are verified locally; `rundir` / `runindir` / `runoutput` roots are
marked "leaf".

## ENIL-DEREF (24)

| root | first cause | mechanism | status |
|---|---|---|---|
| `testdir:fixedbugs/issue19246.go` | nil deref was a static refusal | runtime panic | fixed in `ef3bde4f` (local PASS) |
| `testdir:fixedbugs/issue27518a.go` | same | runtime panic | fixed in `ef3bde4f` (local PASS) |
| `testdir:fixedbugs/issue38496.go` | same (`*m[1]`) | runtime panic | fixed in `ef3bde4f` (local PASS) |
| `testdir:fixedbugs/issue43835.go` | same (`bad, _ = true, *p`, `return true, *p`) | runtime panic | fixed in `ef3bde4f` (local PASS) |
| `testdir:fixedbugs/issue72860.go` | same (`*p >= 0`) | runtime panic | fixed in `ef3bde4f` (local PASS) |
| `testdir:fixedbugs/issue73748a.go`, `issue73748b.go` | same | runtime panic | fixed in `ef3bde4f` (local PASS) |
| `testdir:fixedbugs/issue8336.go` | same, in a select case channel expression (faults before the next case is evaluated) | runtime panic | fixed in `ef3bde4f` (local PASS) |
| `testdir:fixedbugs/issue32288.go` | nil deref refusal; then a frame called by the deferred function (`useStack(100)`) abandoned the rest of the defer, so `recover()` never ran | runtime panic + running-panic frame return | fixed in `ef3bde4f` (local PASS) |
| `testdir:fixedbugs/issue73476.go` | `for i := range (*p)` with nil `p *[4]int` and one iteration variable: Go does not evaluate `*p` (constant length) | range over pointer to array | fixed in `ef3bde4f` + `f28966cb` (local PASS) |
| `testdir:fixedbugs/issue22881.go` | nil deref forms now panic; the root also needs `m[0] /= z` divide-by-zero, `a[0]` on a nil slice and `m[0][:1]` slice bounds as runtime panics, and `m[0] = *p` element assignment to honour the interrupted sentinel | runtime panic (partial) | moved to lane `collections` (bounds / divide-by-zero panics; `bashpp_collection.go` element-assign site) |
| `testdir:fixedbugs/issue23017.go` | `m[2], p[1] = 2, 2` needs index-out-of-range as a runtime panic; the nil forms are fixed | runtime panic (partial) | moved to lane `collections` (bounds panic) |
| `testdir:fixedbugs/issue23837.go` | `h(p, q func() struct{})`: a func-typed parameter with an anonymous struct result is refused by the argument type check (`BASHPP-EARG-FUNCTYPE: h requires func()(struct)`) before any nil deref | func-type spelling of an anonymous struct result in `bashPPCheckArgs` | design/other: callable type identity (`bashpp_func.go` arg check) — not this lane's mechanism |
| `testdir:fixedbugs/issue40629.go` | nil deref now panics; a goroutine started from a deferred call while the panic unwinds never runs, so `<-c` blocks to the 60 s bound (`u2`-shaped: `defer func(){ c := make(chan bool); go func(){ …; c <- true }(); <-c }()`) | goroutine scheduling during panic unwinding | moved to lane `select/concurrency` / S162.3 (not a timeout knob: the shape is a deadlock) |
| `testdir:chan/powser1.go`, `chan/powser2.go` | the nil deref is real: a `PS` (`type PS *dch`) argument arrives nil in `get` after the demand-channel pipeline (`mkdch2` / `split` goroutines), i.e. a named-pointer-type value lost across goroutine/channel transport — a genuine nil where Go has a value | named pointer types through channels/goroutines | moved to lane `select/concurrency` / S162.3 (design finding: first cause upstream of the deref) |
| `testdir:fixedbugs/issue8048.go`, `issue8132.go`, `issue34123.go` | nil deref was a static refusal; their stack walks tolerate the dependency's frames | runtime panic | fixed in `ef3bde4f` (local PASS) |
| `testdir:fixedbugs/bug347.go`, `bug348.go`, `issue4562.go` | after the (now recoverable) panic, `runtime.Caller(i)` / `runtime.FuncForPC` must see the interpreted frame `main.f` at the fault's file:line | runtime stack introspection of interpreted frames | design: `runtime.Caller` is answered by the dependency process, which has no interpreted frames; record by ID (S162.3-A bridge) |
| `testdir:fixedbugs/issue27201.go`, `issue33724.go` | `runtime.Stack` / `debug.Stack` must show interpreted frames (`ExpectedInStackTrace` and not `NotExpectedInStackTrace`); issue27201 also `_, _, _, _ = runtime.Caller(0)`-shaped tuple assignment from a native call (`BASHPP-EASSIGN-CALL`) | same | design: same as above |

## EEXPR-NIL (13) and RHS nil (5)

| root | first cause | mechanism | status |
|---|---|---|---|
| `testdir:fixedbugs/bug450.go` | `f == nil` on a closure variable | func-value comparison | fixed in `c95a77b7` (local PASS) |
| `testdir:fixedbugs/issue53635.go` | `switch []T(nil) { case nil: }` etc. | typed nil comparison + value switch | fixed in `c95a77b7` (local PASS) |
| `testdir:ken/slicearray.go`, `ken/sliceslice.go` | `by = nil` on a package-level slice | `x = nil` | fixed in `c95a77b7` (local PASS) |
| `testdir:switch.go` | `switch f := func() {}; f { case nil: }`, `switch m := make(map[int]int); m`, then composite-literal tags and cases | value switch, composite comparison | fixed in `c95a77b7` + `c5ee56b1` (local PASS) |
| `testdir:fixedbugs/bug444.go` | `reflect.TypeOf(nil)`, `T(nil)` now cross; `[]byte(nil)[0]` must be an index-out-of-range runtime panic | bounds panic | moved to lane `collections` |
| `testdir:fixedbugs/issue23814.go` | `string([]byte(nil))`: the collection-to-string conversion reads its operand through `bashPPCollectionOperand` (`bashpp_collection_convert.go`), which does not materialise a typed nil conversion | typed nil as a conversion operand | moved to lane `collections` — request below |
| `testdir:fixedbugs/issue44830.go` | `reflect.TypeOf(unsafe.Pointer(nil))`: `unsafe.Pointer` is not a nilable type the evaluator models | unsafe.Pointer | design: unsafe (see AddressExpr) |
| `testdir:print.go` | `println((interface{})(nil))`, `println((map[int]int)(nil))` …: the `println` builtin must print typed nils as Go does (`(0x0,0x0)`, `0x0`) | builtin println of nil values | moved to lane `select/recover/const` (builtin-type cluster) / S162.3 output |
| `testdir:typeparam/issue42758.go` | `switch interface{}(nil) { case int(0), T(0), U(0): }` fixed; then `map[interface{}]int{…}` — `BASHPP-ECOLLECTION-KEY: unsupported map key type interface` | interface-keyed maps | moved to lane `collections` (key cluster) |
| `testdir:fixedbugs/issue16331.go`, `issue25897a.go`, `issue39541.go`, `issue52788.go`, `issue52788a.go` | `reflect.MakeFunc(reflect.TypeOf((func())(nil)), F)`: the typed nil now crosses; the root needs `reflect.MakeFunc` with an interpreted callback (`original callback signature requires value-semantics parameters and supported results`, `unregistered nil bridge type "func()(interface)"`) | bridge callback lifecycle | moved to S162.3-A bridge lane |
| `testdir:fixedbugs/issue16095.go` | `sink = nil`; `y[i] = 99` on `y := new([20]byte)` | `x = nil`, pointer-to-array index | fixed in `ef3bde4f` + `c95a77b7` (local PASS) |
| `testdir:fixedbugs/issue54343.go` | `m = nil` fixed; then `runtime.SetFinalizer` + `runtime.GC` must run the finaliser | GC / finalizers | design: record by ID |
| `testdir:range4.go` | `saved = nil` fixed; `func7` (`defer save(i)` inside a range-over-func body, `defer save(5)` after) runs the defers in the wrong order: `[4 3 2 1 5 -1]` for `[5 4 3 2 1 -1]` — defers registered inside the range-over-func body belong to the enclosing frame, not to the yield closure | range-over-func defer ownership | moved to lane `select/recover/const` (range-over-func / defer path, `bashpp_range.go` yield) |

## SELECTOR-ASSIGN (10)

Every row is an assignment to a field of a dependency-owned value or to an
imported package variable — `cmd.Dir = dir`, `cmd.Stdout = &out` (`*exec.Cmd`
handle), `cond1.L = &mu1` (`sync.Cond`), `runtime.MemProfileRate = 0` — not a
selector on an interpreter-owned struct. The native worker
(`bashpp_native_worker.go.txt`) has no field-set or package-variable-set
operation (`byte-set` on byte slices is the only write), so the mechanism is a
bridge one.

| root | first cause | mechanism | status |
|---|---|---|---|
| `testdir:const7.go`, `fixedbugs/issue11771.go`, `fixedbugs/issue54542.go`, `nosplit.go` | `cmd.Dir = dir` on an `*exec.Cmd` native handle | native field set | moved to S162.3-A bridge lane — request below |
| `testdir:fixedbugs/issue19658.go`, `linkx_run.go` | `cmd.Stdout = &buf`, `cmd.Env = os.Environ()` | native field set | moved to S162.3-A bridge lane |
| `testdir:fixedbugs/issue9110.go` | `cond1.L = &mu1` on a `sync.Cond` value | native field set | moved to S162.3-A bridge lane |
| `testdir:chan/select2.go`, `finprofiled.go` | `runtime.MemProfileRate = n` — an imported package variable | native package-variable set | moved to S162.3-A bridge lane |
| `testdir:fixedbugs/issue8606b.go` | (same class: native value field write) | native field set | moved to S162.3-A bridge lane |

## AddressExpr (26)

24 of the 26 rows are `unsafe.Pointer(&x)` memory reinterpretation
(`*(*uintptr)(unsafe.Pointer(&s))`, `(*[3]int64)(unsafe.Pointer(uintptr(…)))`,
`(*reflect.SliceHeader)(unsafe.Pointer(&s))`, `uintptr(unsafe.Pointer(&b[len(b)-1])) + 1`):
`abi/part_live.go`, `abi/part_live_2.go`, `cmp.go`, `fixedbugs/bug513.go`,
`issue17381.go`, `issue27695b.go`, `issue27695c.go`, `issue29362.go`,
`issue29362b.go`, `issue30041.go`, `issue35027.go`, `issue40917.go`,
`issue4585.go`, `issue46938.go`, `issue48536.go`, `issue52612.go`,
`issue75365.go`, `issue80004.go`, `ken/cplx3.go`, `slicecap.go`,
`unsafe_slice_data.go`, `unsafe_string_data.go`, `uintptrescapes.go`,
`fixedbugs/issue11656.go`. A pure-Go evaluator with its own value model has no
memory layout to reinterpret; this is a design finding to record by ID (the
same class as `unsafe.Sizeof/Offsetof` folding, which is a different, foldable
mechanism). Not fixable by a general mechanism without a native memory model.

| root | first cause | mechanism | status |
|---|---|---|---|
| `testdir:fixedbugs/bug206.go` | `[]ast.Expr{&ast.Ident{}}`: the address of a dependency composite as an element of a slice of a dependency interface type | native composite address as a collection element | moved to lane `collections` (element conversion of native values) / bridge |
| `testdir:typeparam/geninline.go` (rundir) | `_ = IVal[int](&l)`: conversion of an address to a generic interface type in a blank assignment | generic interface conversion of `&l` | leaf: not verifiable locally (rundir); the interface-conversion path now accepts `&l` for the non-generic case (`c5ee56b1`) |

## CompositeLit (8) and NewExpr (5)

| root | first cause | mechanism | status |
|---|---|---|---|
| `testdir:abi/leaf2.go`, `abi/spills4.go` | `(i4{12, 34, 6, 8}) != z` | composite comparison | fixed in `c5ee56b1` (local PASS) |
| `testdir:fixedbugs/issue9006.go` | `switch (T1{}) {` | composite value switch | fixed in `c5ee56b1` (local PASS) |
| `testdir:fixedbugs/issue73888.go`, `issue73888b.go` | `testNode(SourceRange{})` | composite conversion to a named type | fixed in `c5ee56b1` (local PASS) |
| `testdir:method5.go` | `Tbigv([2]uintptr{5, 6})` conversion; `p[0]` on a pointer receiver to a named array; `f = t1.M` method values; `psv.M` through nil, `i.M` on a nil interface, promoted `t1.M` through nil embedded pointers must panic | composite conversion, pointer-to-array index, method value assignment, nil faults | fixed in `ef3bde4f` + `c5ee56b1` (local PASS) |
| `testdir:complit.go` | `t = T{0, 7.2, "hi", &t}`: the untyped float constant `7.2` as a `float64` field fails element conversion (`BASHPP-ECOLLECTION-ELEMENT: cannot use string value as float64`); the literal is then retried as a scalar and refused | untyped float constant as a struct field | moved to lane `collections` (element conversion) |
| `testdir:fixedbugs/bug465.go` (rundir), `fixedbugs/issue11053.go` (rundir), `fixedbugs/issue32595.go` (rundir), `fixedbugs/issue47068.go` (rundir), `fixedbugs/issue30862.go` (runindir) | `(T{1, 2}) == (T{3, 4})`, `fmt.Sprint(new(int32))`, `reflect.TypeOf(new([0]byte)).Elem()`, `interface{}(new(EmbedImported))` — the shapes are fixed in `c5ee56b1` (`exprform/composite_new_forms.go`) | composite comparison, `new` bridge, converted interface | leaf: rundir recipes are not runnable by the local check-then-run form |
| `testdir:typeparam/mdempsky/15.go` | `reflect.TypeOf(new(T)).Elem()` with a type parameter `T`: `new(T)` now crosses, but as `unregistered bridge type "*T"` — the instantiated type argument is not substituted before the bridge type text is formed | generic type argument substitution in bridge type text | moved to S162.3-A bridge lane (type registry) |

## OPERAND (27)

| root | first cause | mechanism | status |
|---|---|---|---|
| `testdir:abi/spills3.go`, `fixedbugs/bug433.go`, `fixedbugs/issue43570.go`, `fixedbugs/issue55122.go`, `fixedbugs/issue55122b.go`, `fixedbugs/issue5809.go`, `fixedbugs/issue80196.go` | `x != (T{…})`, `x == ([32]byte{})`: the composite operand was refused and the comparison fell to the scalar path, which refused `x` | composite comparison | fixed in `c5ee56b1` (local PASS) |
| `testdir:fixedbugs/issue8947.go` | `switch p {` on a pointer | value switch | fixed in `c95a77b7` (local PASS) |
| `testdir:fixedbugs/issue67190.go` | `switch ch1 { case ch2: }` with `ch1 chan struct{}` and `ch2 <-chan struct{}` (the same channel under two direction types): the value switch selects, but channel comparison across direction types reports unequal | channel identity across direction conversions | moved to lane `select/concurrency` (`bashpp_chan_value*`) |
| `testdir:fixedbugs/bug328.go` | `println(p)` with `p unsafe.Pointer` nil must print `0x0` | println of a nil pointer | design: unsafe / builtin println (see EEXPR-NIL `print.go`) |
| `testdir:fixedbugs/issue15281.go` | `println(x)` fixed; the root then measures `runtime.MemStats` deltas (`expected delta at least 9MB`) | GC accounting | design: record by ID |
| `testdir:literal.go` | `assert(f01 == -f00, "f01")` with `var f00 float32 = 3.14159`: the typed float variable is read back as a `String` constant, so unary `-` is refused | typed float variable storage | moved to lane `select/recover/const` (const/scalar cluster) |
| `testdir:bigalg.go`, `closedchan.go`, `copy.go`, `fixedbugs/dse_move_auxint.go`, `fixedbugs/issue29013a.go`, `fixedbugs/issue46304.go`, `fixedbugs/issue5373.go`, `fixedbugs/issue5515.go`, `floatcmp.go`, `typeparam/graph.go`, `typeparam/issue49295.go`, `typeparam/issue51303.go` | `println(…, a)` of an array, `XChan(c)` conversion of a channel, `copy(my16(…))`, `[8]byte(dst)` slice-to-array conversion, a struct field initialised from a struct variable (`plist: []P{p1}`), `[1][]byte{s}`, `Slice(b)`, `nan` (a float variable) as a struct-literal element, `visited[from] = true` with a struct key, `T(r.buf[:n])`, `ss[E, []E](x)` — each is a collection element / conversion / builtin argument that reaches the scalar evaluator with a structured value | collection element conversion, named-collection conversions, builtin arguments | moved to lane `collections` (element / conversion clusters); none is a nil or pointer mechanism |
| `testdir:fixedbugs/bug449.go` (runoutput), `fixedbugs/issue30908.go` (rundir), `typeparam/select.go` (rundir) | not verifiable locally | — | leaf |

## Local tally

118 lane roots (interpreted rows of the eight first-line patterns): 33 local
PASS after the four commits, 11 not runnable locally (`rundir` / `runindir` /
`runoutput`), the rest recorded above by first cause. Leaf request:
`leaf-interp-nilptr-1.tsv` (the 118 roots + canaries `testdir:defer.go`,
`testdir:method3.go`, `testdir:closure1.go`, all native-PASS in Barrier B and
local PASS here).

## Pre-existing failures noted (darwin, before and after every change)

The 8 `GoSource*` tests named in the contract, plus `TestBashPPRecoverIsDirectOnly`
(three classic-Bash++ subtests: `recover()` with nothing to recover yields
`<nil>` instead of the empty payload — introduced by `a1dfcd5c`, "evaluate
recover in expression position"; fails on `e9f7feab` before this lane's
commits). `TestConcurrencyScheduleMatrix` fails under heavy machine load and
passes alone (twice) on this tree.

## Requests to other seams / lanes

1. **lane `collections`** — `bashpp_collection.go` index-assign
   (`value, child, err := r.bashPPEvalElement(rhs, typ.Element)` in the
   indexed-assignment path): skip the report when
   `errors.Is(err, errBashPPScalarInterrupted)`, so `m[0] = *p` with a nil `p`
   panics without printing `scalar call interrupted` (issue22881). Same for
   `bashPPCollectionOperand` in `bashpp_collection_convert.go`: a typed nil
   conversion `[]byte(nil)` as the operand of `string(…)` should read as the
   zero value of its type (`r.goSourceNilElement` / `bashPPZeroValue`)
   (issue23814). Bounds and divide-by-zero runtime panics can reuse
   `bashPPRuntimeError` + `r.goSourceRuntimeFault(err)` from
   `bashpp_sprint162_nilptr.go` (a sentinel value per fault text, converted
   at the expression entry points — the entry points already convert any
   `*bashPPRuntimeError`).
2. **lane `select/concurrency` / S162.3** — (a) a goroutine started from a
   deferred call while a panic is unwinding never runs (`issue40629`,
   reproducer shape in the ENIL-DEREF row); (b) channel comparison across
   direction types (`issue67190`); (c) a named pointer type value lost across
   the demand-channel pipeline in `chan/powser1.go`.
3. **S162.3-A bridge lane** — native field set (`cmd.Dir = dir`,
   `cond.L = &mu`) and imported package-variable set
   (`runtime.MemProfileRate = 0`): a `field-set` op next to `byte-set` in the
   worker (`FieldByName(sel).Set(decode(arg))` on the handle's addressable
   value) and a `set` op on `symbols[q.Selector]`, with a runner-side
   candidate in `bashPPStructuredAssign` before the
   `BASHPP-ESELECTOR-ASSIGN` refusal when the root cell is a native handle or
   the selector names an import. 10 roots.
4. **lower owner** — `lower/gosource_pointer_field_assign_test.go`
   (`TestGoSourcePointerFieldAssignRejectsNilStorage`) runs the interpreter
   on `lst.head.next = &element{…}` with a nil `head` and asserts the old refusal. With `3454f1fd` the interpreter
   reports Go's panic; the exact diff needed:
   `-	qt.Assert(t, qt.StringContains(stderr, "BASHPP-ENIL-DEREF:"))`
   `+	qt.Assert(t, qt.StringContains(stderr, "panic: runtime error: invalid memory address or nil pointer dereference"))`
   (status 2 and empty stdout unchanged). `lower/callables_entry_test.go`
   asserts the compiled runtime's own `entry_rt.ValueError` and is unaffected.
5. **integrator** — `ef3bde4f` touches the panic path in `bashpp_func.go`
   (`bashPPInvoke`: `goSourceNestedDeferRunning` at frame return,
   `bashPPPanicHalts()` instead of `bashPPPanicking()` for "frame abandoned";
   `bashPPCallValues`: no `bashPPShortFailureSeq++` while a panic unwinds) and
   the two value-call sites (`bashPPScalarFuncCall`, `goSourceCallResultCells`).
   They are small and general but sit in the recover/panic lane's neighbourhood.
