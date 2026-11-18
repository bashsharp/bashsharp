# Sprint 165 runtime-1 findings (S165.3, story #99)

Lane: runtime/bridge — `bashpp_*bridge*`, `bashpp_native_*`, `bashpp_capture*`,
the panic/recover path. Evidence: Barrier C `active-153-manifest.tsv` re-run
locally on darwin from the frozen candidate (every root below was reproduced
in-process with `bashy.real --bashpp --source=go`) and run 0 on the leaf host
where it had landed. Rows recorded by ID on #162 (D1–D7) and D9 are never
relabeled here.

Status legend: `fixed in <commit>` · `moved to <seam>` (first cause is another
seam's; the request is under "Requests to other seams") · `design` (a written
finding with the number, no mechanism this sprint) · `by-ID D<n>` (recorded
decision; not a mechanism).

## Cluster 1 — "output barrier" (16 rows)

Finding: **there is no output-barrier mechanism.** None of the 16 rows is a
stray diagnostic, an interpreter trace or a bridge worker's stderr. Every one
is the root's own self-check printing its complaint with `println`/`print`
(to stderr, which upstream's `run` action captures with the program's
stdout) instead of panicking, or an `.out`-compared root printing a wrong
value. The "output should be empty … Instead saw" first line is the
harness's honest description of a wrong-value row whose author chose
`println("BUG …")` over `panic`. The rows are 14 distinct semantics defects
plus 2 design rows, grouped by first cause:

| root | stray output (verbatim first line) | first cause | mechanism | status |
|---|---|---|---|---|
| `fixedbugs/bug258.go` | `BUG 3` | `math.Pow(2, 3) != 8`: the untyped constant crossed the bridge as a bare int and was compared with a float64 | native compare — an untyped constant operand takes the typed native operand's type before the dependency compares (Go's untyped conversion, checker-verified) | fixed (native-compare) |
| `fixedbugs/bug006.go` | `zero` | `g float64 = 4.5 * iota` compares equal to `0.0` AND not unequal to `4.5`: typed-float constant comparison in the scalar evaluator | const/scalar evaluator | moved to S165.1 (const cluster) |
| `fixedbugs/bug364.go` | `BUG: defer` + `3/10` `1/5` `1/10` | `fmt.Sprintf("%v", 0.3)`-shaped float rendering prints the interpreter's exact rational | GoSource typed-float rendering (the reverted read-back, GoSource-only form) | moved to S165.1 (const cluster; Classic-parity gate) |
| `fixedbugs/issue11326b.go` | `incorrect value: 10` | huge-exponent constant expression folded to the wrong value | const folding | moved to S165.1 |
| `fixedbugs/issue12577.go` | `BUG: got 0 want -0.0` ×2 | negative zero is not representable in the `go/constant` scalar model | scalar float model (`-0.0`) | moved to S165.1 (design-adjacent: exact arithmetic has no signed zero) |
| `fixedbugs/issue6899.go` | `0` (want `-0`) | same: `println(math.Copysign(0, -1))` loses the sign in the scalar read-back; `println` of a float also renders as a rational, not gc's `%e` form | scalar float model + builtin `print` float formatting | moved to S165.1 |
| `deferprint.go` | `3/2` for `1.5`; `&{[] 0 …}` for nil chan/func/map/slice | `println` renders an untyped float constant as a rational and a nil reference value as its transport struct; gc prints constants via their string form and references as `0x0` / `[0/0]0x0` | builtin `print`/`println` rendering (GoSource) | moved to S165.1 (scalar rendering; Classic-parity gate) |
| `fixedbugs/issue73917.go`, `issue73920.go` | `…:27:9: fn: command not found` | `var fn = S.M` (a package-level var whose initialiser spells a function value by name) held the initialiser text; the deferred `fn(p)` found no function and fell to the shell | var declarations bind a function value spelled by name | fixed (callable-decl) |
| `fixedbugs/bug491.go` | `BUG` + `complex call not ordered: (1+0i) 2 (3+0i)` | the ordering is right (`a == (1+0i)`, `c == (3+0i)`); `real(a) != 1` evaluates TRUE — the `real`/`imag` builtin result compared with an untyped constant | builtin `real`/`imag` result kind vs untyped constant comparison | moved to S165.1 (builtin-type cluster; Barrier C regression witness: PASS at B) |
| `fixedbugs/bug352.go` | `BUG: bug352 [0]byte`, `BUG: bug352 struct{}` | `&x[1] != &x[2]` for zero-size elements: Go's zero-size addresses coincide; interpreter element references are distinct per index | pointer identity of zero-size elements | design — the value model has no address arithmetic; 1 row |
| `fixedbugs/issue9110.go` | `BUG: object leak: 0 -> 877 -> 934` | `runtime.ReadMemStats` heap-object delta over interpreter-owned values | collector's view of interpreter-owned values | design — D7/D9 ground (no native memory model); 1 row |
| `inline_callers.go` | `got [runtime.Callers reflect.Value.call …]` | `runtime.Callers` from an original function sees the bridge worker's frames, not `main.h main.g main.f` | frames | design — see cluster 4 frames; 1 row |
| `typeparam/issue49547.go` | `want: main.F[main.foo], got: main.bppInstance_<sha>` | `%T` of an instantiated local generic type prints the helper's generated name | materialisation naming — a generic helper declaration `type F[T any] …` + `reflect.TypeFor[F[foo]]()` would print Go's spelling | design this sprint (recorded in 118's descriptor comment); the identity mechanism below makes it reachable — 1 row |
| `fixedbugs/issue14646.go` | `Expected line = 18 but got line = 15` | `runtime.Caller(0)` inside a deferred closure reports the enclosing call's line | frames (defer site vs closure line) | cluster 4 frames — see below |
| `fixedbugs/issue20014.go` (compiled + interpreted) | `output does not match expected`; `build dependency bridge: exit status 1` | `.dir` root: compiled output mismatch is lowering; interpreted the helper does not compile for the package set | package/lowering | moved to S165.4 / S165.2 (`.dir` package bridge build) |

## Cluster 2 — unregistered bridge types (9 rows)

| root | first cause | mechanism | status |
|---|---|---|---|
| `typeparam/issue47272.go` (`Option[int]`) | the instantiation is only REACHED (through `Some[int]`'s result `Option[T]`), never spelled, so the helper's fixed namespace lacked it | instantiation closure: from every spelled instantiation and generic call through the generic declarations' signatures, bodies and method bodies, with the bindings applied (`bashpp_sprint165_runtime_instantiations.go`) | fixed (bridge-identity) |
| `typeparam/issue48317.go` (`*A[int]`) | `A[T]{}` inside `a[T]` called as `a[int]()` | same | fixed (bridge-identity) |
| `typeparam/issue50481c.go` (`__gosource_pkg_0_T`) | a generic named type over a scalar underlying (`type T[…] int`) crossed with the bare declaration name: scalar cells key on the base name | scalar bridge boundary restores the cell's instantiated declared type | fixed (bridge-identity) |
| `fixedbugs/issue71857.go` (`*sync/atomic.Uint64`) | the interpreter spells a pointer to a handed value as `*` + the handed identity, which is not a Go expression for the helper's resolver | helper resolves the `*` prefix on a registered identity; every type the helper hands over is registered under that identity (`seenTypes`) | fixed (bridge-identity) |
| `fixedbugs/bug257.go` (`hash.Hash`) | `m = md5.New()` sends the interface identity `hash.Hash` of a package the program never imported | identity registration at hand-over (same) | fixed (bridge-identity) |
| `typeparam/issue48598.go` (nil `IteratorFunc[int]`) | after the closure registers the type, the next defect: `IteratorFunc[R](nil)` — a nil conversion to a LOCAL func-typed named type — binds a native nil, and the method call on it (`it.Iterate()`) was routed to the dependency on the strength of the native value | original-method dispatch: a two-part call whose receiver's declared (or dynamic) type is a local type declaring the method runs the original body whatever holds the value (method-dispatch) | fixed (method-dispatch) |
| `fixedbugs/gcc65755.go` (`s`) | two function-local types spelled `s` are ambiguous in the helper's flat namespace and both are dropped | site-keyed materialisation + lexical resolution (162's `goSourceLocalTypes`) at every crossing | design — 1 row |
| `fixedbugs/issue39541.go` (nil `func()(interface)`) | `reflect.MakeFunc` over an original function retained across 100 goroutines | D9's MakeFunc clause (not in D9's named list — manager to reconcile) | by-ID D9 (candidate) |
| `typeparam/mdempsky/15.go` (`*interface`) | `new(T)` with `T = interface{ EBad() }` as a type argument, then `//go:nointerface` must hide `EBad` from the type switch | anonymous interface type arguments + gc pragma fidelity | design — pragma fidelity (S165.4 family); 1 row |

## Cluster 3 — dependency mutation of interpreter-owned references (7 rows)

Finding: **not one mechanism and not one decision — three shapes.** The
refusal is `validateLocalTransport`'s catch-all for a `call` whose arguments
carry interpreter-owned references and whose callable is not on a reviewed
list. Read per row:

| root | first cause | shape | status |
|---|---|---|---|
| `fixedbugs/bug243.go` (`Addr`) | `listen.Addr()` on a local channel type held natively was routed to the dependency; then `listen.Addr().Error()` — a method on a computed receiver holding a dependency error — had no dispatch | dispatch, not reflect: original-method dispatch + native receiver method binding (method-dispatch) | fixed (method-dispatch) |
| `typeparam/double.go` (`reflect.DeepEqual`) | DeepEqual over local named slices was not on the read-only structural reader list | read-only reader: DeepEqual walks both operands, retains nothing, writes nothing, invokes no method — same class as slices.Equal / json.Marshal (reflect-readonly) | fixed (reflect-readonly) |
| `fixedbugs/issue27695.go` (`MethodByName`), `fixedbugs/issue25897b.go` (`MethodByName`), `reflectmethod4.go` (`reflect.ValueOf` + `Method(0)`), `fixedbugs/issue77779.go` (`reflect.ValueOf(...).Field(0).Interface().(Renderer).Render()`) | `reflect.Value.Method/MethodByName` over a transported local value yields a method value the program RETAINS and invokes later (`go f(c)` across 100 goroutines; `f.Interface().(func(...))` then called; `.Method(0).Interface().(func())()`), and issue77779 needs `Field(0).Interface()` to re-box the promoted embedded value with its mirrored method | D7/D9's retained-callback clause: the method value is a callback the dependency holds after the request that created it returns; the mirrored stubs exist (a materialised type presents its method set to reflect), so a mechanism would be the retained-callback protocol (`retainedFunctionCallback`, today a reviewed callable list) generalised to method values of materialised types — every later invocation is a callback-capable request parked on whatever frame runs it. **4 rows; not bounded this sprint** (issue25897b additionally runs them concurrently with `runtime.GC`, D9's ground) | design — recommend by ID under D9's retained clause, or a Sprint 166 mechanism with the number 4 |

## Cluster 4 — next-defect panics

Grouped by mechanism; the biggest groups first. "next defect" = what the
same root reports after the fix, measured in-process on darwin.

| group | roots | first cause | mechanism | status |
|---|---|---|---|---|
| recovered runtime faults are strings | `fixedbugs/issue19040.go`, `fixedbugs/issue32187.go`, `fixedbugs/issue79236b.go`, `recover2.go`, `typeparam/issue51521.go` | the nil dereference, bounds, makeslice, channel close/send and uncomparable faults raise through `bashPPRaise(text)` with the message alone, so `recover().(error)` fails | the message says which runtime value Go panics with: `runtime error: …` → errorString/boundsError, the fixed plainError set → plainError; a program's own string never boxes (runtime-panic) | issue32187, issue79236b fixed; issue19040 → next defect panicwrap (below); recover2 → next defect slice-bounds diagnostic (request to S165.1); issue51521 → next defect exit leak (below) |
| panicwrap wording | `fixedbugs/issue19040.go`, `fixedbugs/issue52072.go` | a value method reached through an interface holding a nil `*T` faults as a plain nil dereference; Go's itab wrapper panics `value method main.T.F called using nil *T pointer` (plainError) at INVOCATION — a deferred `i.M()` binds without fault and panics when it runs, after the named result was set | bind the wrapper as a callable whose invocation raises (runtime-panic, `bashPPRuntimeErrorCall.panicWrap`) | fixed (runtime-panic) |
| exit status leaks past a recovered panic | `typeparam/issue51521.go` | `return r.M()` with r a nil interface: the callee lookup raises, and the return statement marked the frame failed as well (`bashPPShortFailureSeq++`), so the frame exited 2 after its deferred call had recovered | the raising lookup is the unwind; it is not a failed producer (runtime-panic) | fixed (runtime-panic) |
| structured / dependency-owned panic values | `fixedbugs/issue4066.go` (+ the story's "error %v rendering of an errors.New value recovered through recover" row) | `panic(terr{})` travelled as rendered text; `panic(fmt.Errorf(…))` travelled as its SOURCE text (`fmt.Errorf("e")`) | box a struct/collection/pointer argument with its declared type; evaluate a dependency-owned argument and keep the dependency's value as the dynamic value, rendering the report by its Error text (runtime-panic) | fixed (runtime-panic) |
| interface assertions over dependency-owned values | (outside-corpus: `var e any = errors.New("x"); e.(error)` answered false; `switch x := v.(type) { case error: }` missed) — reached by the fixed row above and by every recovered dependency error | the interpreter's method-set check knows nothing of a dependency type's methods; an imported interface name (`fmt.Stringer`) was not even recognised as an interface | the dependency's `assignable` answer is the authority for a dependency-owned dynamic value against an interface (local or imported); its methods bind through the bridge (native-assert) | fixed (native-assert) |
| function values spelled by name in `var` declarations | `fixedbugs/issue73917.go`, `fixedbugs/issue73920.go`, `typeparam/issue48225.go` | `var fn = S.M`, `var newInt = Foo[int]{val: 1}.Get`: only a function LITERAL initialiser bound a closure; the others left the variable holding the initialiser text, and the call through it fell to the dependency dispatch (`unknown imported symbol or method: fn`) / the shell (`fn: command not found`) | bind the closure the short declaration binds (callable-decl) | fixed (callable-decl) |
| pointer to an interface variable across the bridge | `fixedbugs/issue70156.go` | `new(interface{})`'s pointee crossed as an empty string of type `interface{}`, so `reflect.ValueOf(pi).Elem().IsNil()` was false and `Elem().Kind()` followed the boxed value | transport the pointee as the interface value it holds and type the pointer by the interface (bridge-identity) | fixed (bridge-identity) |
| native comparison with an untyped constant | `fixedbugs/bug258.go` | see cluster 1 | native-compare | fixed |
| interface-conversion identity (evaluator) | `named.go` (`interface {} is bool, not main.Bool` — the boxing of `*&b` / `Bool(true)` loses the named type), `fixedbugs/issue18911.go` (`struct { x int }` types from different scopes), `fixedbugs/issue29612.go` (`.dir`: `*main.__gosource_pkg_1_T is not interface: missing method foo` — a method-bearing type from a flattened package loses its method set across the package prefix), `reflectmethod1/2/3.go` (`reflect.TypeOf(v).Method(0).Func.Interface().(func(M))(v)` — `func(main.M)` vs `func(M)`: the asserted function type spelled with the local name does not match the reflected signature spelled with the package-qualified name; a type-identity normalisation for function types crossing back from reflect — bridge-adjacent, but the same three roots then need `Method(0).Func` = the retained method value above), `interface/embed3.go` (`main.__gosource_pkg_0_I1` — the flattened package identity in a runtime message: `p.I1` is what Go prints — the `.dir` prefix mechanism, S165.4/S165.2 family), `typeparam/issue54302.go` (`.dir`, `iface.(*G[T])` inside a generic) | evaluator type identity | moved to S165.1 (compare/convert) — 8 rows; reflectmethod1–3 shared with D9's retained clause |
| `panic: FAIL/N` self-checks (evaluator) | `fixedbugs/bug434.go` (negative zero — scalar model), `fixedbugs/issue42401.go` (compiled + interpreted; `.dir`), `fixedbugs/issue50190.go` (anonymous-struct identity through aliases and function-local alias types), `fixedbugs/issue15039.go` (`big != bad` — const/string folding), `func8.go` (`x() == (y() == "abc")`: comparison operand order), `convT2X.go` (`u128 != iu128`: 128-bit struct boxed to interface compares unequal), `typeparam/equal.go` (`t == i` type parameter vs interface comparison), `typeparam/settable.go` (`got [addr], want [addr]` — pointer identity through a generic setter), `fixedbugs/issue52856.go` (compiled), `fixedbugs/issue47087.go` (compiled: `comparing uncomparable type struct { _ []int }` — a blank-field struct is comparable in Go when `_` is ignored? no: Go's rule makes it uncomparable; the compiled row is lowering), `bug367.go` (compiled) | evaluator compare/const; compiled rows lowering | moved to S165.1 (7 interpreted) / S165.4 (3 compiled) |
| `//go:nointerface` pragma | `fixedbugs/issue47928.go` (`-goexperiment fieldtrack`), `typeparam/mdempsky/15.go` | the pragma hides a method from interface satisfaction; no interpreter notion of it | pragma fidelity | design — 2 rows (S165.4 family) |
| argument count | `fixedbugs/bug148.go`, `fixedbugs/issue63657.go` (`f: expected N argument(s), got 0`) | a call whose FIRST argument is the untyped `nil` — `f(nil)` into an interface parameter, `f(nil, &b, 3)` into a pointer parameter — arrives with no arguments at all | call-argument binding of a leading `nil` (evaluator) | moved to S165.1 — 2 rows |
| makeslice of zero-size elements | `fixedbugs/issue29190.go`, `fixedbugs/issue7550.go` | `make([]struct{}, maxInt)` must succeed (no allocation) and `append` past it must panic `growslice: len out of range`; the interpreter allocates one `[]any` slot per element | value model: no zero-size elements | design — 2 rows; the makeslice raise site also spells the message without Go's `runtime error: ` prefix (request to S165.1) |
| nil dereference rows | `fixedbugs/bug454.go` (`for i := range arr` over a nil `*[10]int` — Go ranges the array TYPE's length without dereferencing when only the index is used; the interpreter dereferences), `fixedbugs/issue79762.go` (the fault is right; the root checks `debug.Stack()` for the faulting frames `f1`/`f2`/`(*T).foo` — a traceback-contents row, frames), `devirtualization_nil_panics.go` (the panic's reported line is the interpreter's statement line, 644, not the deferring source line 28 — frames), `typeparam/issue51521.go` (fixed above) | evaluator range; frames | bug454 moved to S165.1 (1); issue79762, devirtualization → frames (below) |
| runtime.Caller / Callers / traceback frames | `inline_caller.go` (`skip=5 runtime.Caller failed`), `inline_callers.go`, `fixedbugs/issue18149.go` (`//line` directive filename in `runtime.Caller` — the interpreter reports the physical file), `fixedbugs/issue22662.go` (`//line ??:1` cleared filename), `fixedbugs/issue14646.go` (defer site line vs closure line), `devirtualization_nil_panics.go`, `fixedbugs/issue79762.go` (`debug.Stack()` contents) | `runtime.Caller(skip)` is answered by the interpreter's frame table (`bashpp_sprint162_nilptr2_stack.go`); it reports the physical file and the statement line, not the `//line`-adjusted name (`SourceFile.LineDirectives` carries it) nor the deferred call's own line; `runtime.Callers` from a callback sees the worker's frames | frames: one mechanism for the directive-adjusted filename (the front end already records `LineDirectives`; the frame table needs to consult them) would close issue18149 + issue22662; the deferred-closure line and the skip-depth and traceback rows need the frame table to model the defer site and `debug.Stack()` — 7 rows | design — recorded with the number; the `LineDirectives` half is bounded (request: interp integrator / the frame-table owner) |
| `.dir` package init / identity | `fixedbugs/issue29919.go` (`missing a.init` compiled / `missing a.go:15` interpreted — package init order and `runtime.Caller` inside init), `fixedbugs/issue20014.go` | multi-package init order + frames | S165.2 / S165.4 | moved — 2 rows |
| `exit status 1` rows | `fixedbugs/bug260.go` (prints `FAIL` — `%p` of consecutive array elements must differ by the element's alignment: the interpreter has no layout; D7 ground), `fixedbugs/issue7419.go` (exit 1, no output: `var x = 1e-779137` must be exactly 0 — an underflowing float constant is folded to a nonzero rational; const cluster) | layout (design) / const evaluator | bug260 design (D7 ground, 1); issue7419 moved to S165.1 (1) |
| `// errorcheck` printed as output | `index1.go`, `index2.go` | these are `errorcheck` roots whose interpreted mode still RUNS them (the generated `index.go` self-check prints its own header) — a harness routing question for `errorcheck` recipes generated by `runoutput` roots | harness (S165.0) | moved to the harness owner — 2 rows |
| `unexpected fault address` | `recover4.go` | a real segmentation fault: the root maps memory with `syscall.Mmap` and expects a recoverable fault; the interpreter's dependency worker takes the SIGSEGV for real | design — no native memory model (D7 ground) | design — 1 row |
| `mayMoreStack not called` | `maymorestack.go` | `-gcflags=-d=maymorestack=main.mayMoreStack` compiler hook | by design (compiler debug hook) | design — 1 row |
| `reflect.Value.Set/Interface on zero Value` | `fixedbugs/issue13160.go` | `reflect.ValueOf(&slice).Elem().Index(i).Set(...)` over an interpreter slice through a transported pointer: the worker's rebuilt copy is a zero Value at that index (run 0 reports `Interface on zero Value` on the frozen candidate — the Barrier C first line was already stale) | reflect view over interpreter slices — the same ground as cluster 3's design row | design — 1 row |
| `never GC'd` | `fixedbugs/issue54343.go` | D7 by ID; NOTE: on this candidate the root now reports `original callback signature requires value-semantics parameters` (the `runtime.SetFinalizer` argument crosses with the interface pointee transport) — a different first line, the same recorded decision | by-ID D7 | never relabeled |
| `object leak` | `fixedbugs/issue9110.go` | see cluster 1 | design | — |

Run 0 (the same candidate re-measured on the leaf, `leaf-165r0`, without the
deadline shard) agrees with Barrier C on every row above except two, both
pre-existing on the frozen candidate: `fixedbugs/issue27695.go` reaches the
60 s bound at run 0 (`command exceeded time limit`; Barrier C had the
refusal) and `fixedbugs/issue13160.go` reports `Interface on zero Value`
(Barrier C had `Set`). Neither is a regression of this lane.

## Requests to other seams

### S165.1 (evaluator lane / interp integrator)

- **`slice bounds out of range` as a runtime panic** (`recover2.go` test2/test3):
  the slice-expression bounds check reports `BASHPP-ECOLLECTION-SLICE: slice
  bounds out of range [5:15] with length 10` as a diagnostic (exit 1); Go
  panics with `runtime error: slice bounds out of range [:15] with capacity
  10` (a boundsError, recoverable). The raise exists for indexing
  (`bashPPSprint162CollectionBoundsPanic`); the slice form needs the same,
  with Go's wording (`[:high] with capacity N` / `[low:high]` / `[N:] with
  length M`). With `bashPPRaise` now boxing `runtime error: …` messages, the
  raise alone makes `recover().(error)` work.
- **`assignment to entry in nil map` as a runtime panic**: the nil-map store
  reports `BASHPP-ENIL-MAP` (exit 2); Go panics with plainError
  `assignment to entry in nil map`. `bashPPRaise("assignment to entry in nil
  map")` boxes it correctly now.
- **makeslice message** (`bashpp_sprint162_interp_collections_2.go:89`):
  Go's message is `runtime error: makeslice: len out of range` (errorString);
  the raise spells it without the prefix. Prefixing it is one string change
  and the boxing follows. (The two makeslice roots need zero-size elements
  as well — design above.)
- **typed-nil conversion of a local func type** (`IteratorFunc[R](nil)`,
  `IteratorFunc(nil)`): the conversion binds a NATIVE nil
  (`goSourceTypedNilBridgeValue` / `goSourceNilCallableOrChannel`) although
  the type is the program's own; `var f IteratorFunc` binds the interpreter's
  zero value. The dispatch half is fixed here; the value should not cross to
  the dependency at all for a local type.
- **`IteratorFunc(func(...) {...})`**: `BASHPP-EEXPR-FORM: unsupported scalar
  expression *syntax.BashPPFuncLit` — converting a function literal to a
  named function type (convert cluster).
- **float constant into a float parameter** (`Some(2.5)` with `T` inferred as
  float64): `cannot use "5/2" as float64 value for parameter val` — the exact
  rational spelling reaches parameter binding (const cluster; Classic-parity
  gate).
- **float division by zero**: `1/z` with `z` a float variable holding -0
  reports `BASHPP-EEXPR-DIVZERO`; Go yields ±Inf (only integer division
  panics).
- Cluster 1 rows moved above: bug006, bug364, issue11326b, issue12577,
  issue6899, deferprint (rendering / const), bug491 (`real(a) != 1` with
  `a == (1+0i)` is true — the `real`/`imag` result compares wrong against an
  untyped constant; Barrier C regression witness), func8, named, issue50190,
  issue15039, convT2X, typeparam/equal, typeparam/settable, bug148,
  issue63657, bug454, issue7419, issue18911, issue54302.

### interp integrator (shared hubs this lane edited — please review as one batch)

The lane's mechanisms live in `bashpp_sprint165_runtime_instantiations.go`
and `bashpp_sprint165_runtime_panic.go`; the hub edits are additive hooks,
each one call:

- `bashpp_func.go`: `bashPPBindInterfaceMethod` — dependency-owned dynamic
  value binds its method through the bridge; panicwrap wrapper; the
  return-statement failure mark skips a raising lookup.
- `bashpp_interface.go`: `bashPPTypeAssertCell` and `typeCaseTypeMatches`
  — dependency-owned dynamic value against an interface answers from the
  dependency.
- `bashpp_p1.go`: `bashPPDeclare` — `var x = <function value by name>` binds
  the closure.
- `bashpp_panic.go`: `bashPPRaise` boxes runtime messages; `panic(v)` boxes
  structured and dependency-owned arguments.
- `gosource_methods.go`: `goSourceLocalMethod` — computed dependency-owned
  receiver.
- `gosource_values.go`: `goSourceValueCells` — a callee lookup that raised
  is not re-evaluated.
- `bashpp_import.go`: one cache field on `bashPPToolchain`.

### S165.4 / harness

- `interface/embed3.go`, `fixedbugs/issue29612.go`, `fixedbugs/issue29919.go`,
  `fixedbugs/issue20014.go`: `.dir` package prefixes (`__gosource_pkg_N_`) in
  runtime messages and package init order — the same family as the compiled
  `-m` name fidelity rows.
- `index1.go`, `index2.go`: `errorcheck` recipes reached in interpreted mode.
- `fixedbugs/issue47928.go`, `typeparam/mdempsky/15.go`: `//go:nointerface`.

### manager (decisions)

- D9 candidate: `fixedbugs/issue39541.go` (`reflect.MakeFunc` over an
  original function, retained across goroutines) is not in D9's named list.
- Cluster 3's four reflect rows: by ID under D9's retained clause, or a
  Sprint 166 mechanism (the number is 4, plus reflectmethod1–3 which share
  the retained-method-value ground once their type-identity row is fixed).
- `fixedbugs/issue54343.go` (D7) changed first line on this candidate.
