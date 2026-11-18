# Sprint 153 — S153.4b interpreted evaluator semantics

Story-ID: e58cccba74f8. Every root below was run natively (`go run`) and
through the interpreter (`bashy.real --bashpp --source=go --go-file …`,
`rundir` roots with `--go-package test/<pkg>=<file> --go-import-base test`),
reduced to an outside-corpus program, and either fixed by a general
mechanism or left open with the reduced program under `open/`.

Fixed mechanisms have their reproducer (plus a positive control) under
`interp/testdata/sprint153/<mechanism>/`, run by `TestSprint153Evaluator`
(native oracle vs interpreter, exact stdout/stderr/status). Module-shaped
mechanisms (multi-package) are `go.mod` directories run through
`gosource.Load`. Diagnostic-only mechanisms are pinned by
`TestSprint153FatalDiagnostic*`.

Files touched outside the lane's listed ownership but unclaimed by the
other lanes: `bashpp_readonly.go`, `bashpp_p1.go`, `bashpp_concurrency.go`
(task failure text only), `api.go` (two runner fields),
`gosource_{values,arguments,collection_values,nil,calls}.go`, and the new
`gosource_{method_expr,method_package,struct_identity}.go`.

## Roots

| Root | Native | Interpreted before | Mechanism | Status |
|---|---|---|---|---|
| `reorder.go` | 0 | `BASHPP-EASSIGN-ARITY` at p9's `x, x = m[0]` (the manifest's `[1 100 3]` row no longer reproduces; every pN passes) | comma-ok map index / assertion in plain `=`; `true`/`false` as interface initialiser; asserted scalar loses kind | fixed: b2ab06b0, 04c3e935, b219700f |
| `fixedbugs/issue4167.go` | 0 | exit 2, no diagnostic | multi-result call spread into arguments; computed callee in statement position; method expression `(*T).M` / `T.M` as forwarding closure; `(*p)(&n)` pointer conversion | fixed: 5073decb, 994f9f2c, e144401f, 67227737 |
| `fixedbugs/bug367.go` (rundir) | 0 | `panic: should not satisfy main.I` | unexported interface method identity is package-qualified (package read from the linked-package marker) | fixed: 709b86a9 |
| `fixedbugs/issue13160.go` | 0 | `scalar call interrupted` | `ps[i] = new(T)` delivered as a call (fixed: 70464b34); then a torn read: the program races a writer and a reader on one `[]*int` slot; interpreter slots are two-word interfaces and `expand.NewObject`'s `json.Marshal` walk panics on a half-written one | **open** — `open/issue13160_torn_read.go`; needs element storage that is word-atomic or a validator that does not traverse shared storage (`expand/`, not evaluator) |
| `fixedbugs/issue45045.go` | 0 | `scalar call interrupted` | `unsafe.Pointer`/`reflect.StringHeader` rewriting and `runtime.SetFinalizer` on interpreter-owned storage | **open** — no memory model to emulate; not general |
| `fixedbugs/issue24491b.go` | 0 | `scalar call interrupted` | `unsafe.Pointer`↔`uintptr` liveness with finalizers and `runtime.GC` | **open** — same class |
| `typeparam/issue48042.go` | 0 | `undefined type: T`, `scalar call interrupted` | explicit type argument `g[T]()` bound through the frame; interface owning a func literal; `return new(T)` delivered as a call | fixed: d5c0079d, 59cad25f, 70464b34 |
| `fixedbugs/bug510.go` (rundir) | 0 | `scalar call interrupted` | typed nil pointer `(*A)(nil)` as a dependency-call argument, and `reflect.New(...)` producing a value of a local alias type | **open** — bridge (`bashpp_native_*`); `open/bug510_typed_nil_native_arg.go` |
| `typeparam/cons.go` | 0 | `scalar call interrupted` ×3 | returned/passed type assertion keeps the asserted cell; a call's result as an interface source (generic struct field `Tail List[a]`) | fixed: 4b84e70e, a94d658a |
| `alias1.go` | 0 | `panic: byte != uint8` | `byte`≡`uint8`, `rune`≡`int32` in dynamic identity and assignability; `flag = true` (predeclared booleans on an assignment's RHS were looked up as variables) | fixed: 5792b830, 14091eb6 |
| `fixedbugs/issue42401.go` (rundir) | 0 | `panic: FAIL` | `//go:linkname s test/a.s` aliases a variable across packages | **open** — compiler directive; not interpretable |
| `fixedbugs/issue47087.go` (rundir) | 0 | `BASHPP-ECOMPARE-NONCOMPARABLE` | struct literal type identity (fields + package-qualified unexported names); run-time panic for comparing an uncomparable identical dynamic type | fixed: 7531fea2, a2e13d04 |
| `fixedbugs/issue52856.go` (rundir) | 0 | `panic: 0` | same struct literal identity (embedded `int` is unexported, hence package-qualified) | fixed: 7531fea2 |
| `fixedbugs/bug260.go` | 0 | `FAIL`, exit 1 | `%p` of consecutive array elements must differ by the element's size/alignment | **open** — the interpreter has no address arithmetic; pointer formatting is the bridge's synthetic identity |
| `fixedbugs/gcc65755.go` | 0 | exit 2, no diagnostic | function-local type declarations are frame-scoped (fixed: 55f19b96); remaining: the bridge keys local struct codecs by bare name, so two `s` shapes collide in the worker | **open** — bridge; `open/gcc65755_local_type_codec.go` |
| `fixedbugs/issue24491a.go` | 0 | exit 2 | `unsafe.Pointer`↔`uintptr` with finalizers | **open** — same class as issue24491b |
| `fixedbugs/issue69507.go` | 0 | exit 2, no diagnostic | `range f()` over a call's result; `i := 1` in a function whose first argument is a closure bound `$1` (a literal looked up as a function name) | fixed: 7c87a542, e71adf50 |
| `typeparam/issue48645a.go` | 0, prints `func(func(int) bool)` | exit 2, no diagnostic | fatal diagnostic dropped across `return f()` (fixed: d20b6050); remaining: a closure handed to `reflect.TypeOf` | **open** — bridge (function values do not cross); `open/issue48645a_closure_to_reflect.go`; now reports the reason and exits 1 |
| `typeparam/boundmethod.go` | 0 | `cannot convert Int to Stringer` | `Stringer(v).String()` — interface conversion as a value (fixed: e3810745); remaining: `reflect.DeepEqual` on local slices | **open** — bridge slice policy; `open/boundmethod_deepequal_slices.go` |
| `fixedbugs/issue10353.go` | 0 | (passes at this revision) | — | passes |
| `fixedbugs/issue16331.go` | 0 | `bash++: task failed: exit status 1` | a task's fatal diagnostic reported as its failure (fixed: 97ad1048); remaining: typed nil func `(func())(nil)` to `reflect.TypeOf`, `reflect.MakeFunc` over an original function, and an original method value through reflect | **open** — bridge; `open/issue16331_method_value_transport.go` |

## Needs for other lanes (not edited here)

* Bridge (`bashpp_native_*`): typed nil pointers/funcs as native arguments
  (bug510, issue16331); local struct codecs keyed by bare name collide for
  same-named function-local types (gcc65755); `reflect.DeepEqual` on local
  slices (boundmethod); function values to `reflect` (issue48645a,
  issue16331); a local struct with an interface-typed field is not emitted
  for the worker (`undefined: Shape`, met while writing
  `interface_call_source` — reproducer kept generic-only for that reason).
* `expand/`: `NewObject` validates by walking the payload with
  `json.Marshal`; under a program-level data race on a slice slot this
  panics inside the interpreter (issue13160).
* `gosource/`: a first-class package tag on declarations and type literals
  would replace the `__gosource_pkg_N_` marker reads in
  `gosource_method_package.go` (an interface method's package is that of
  the *declared* interface it is spelled in; an anonymous interface literal
  reads as the program package) and `gosource_struct_identity.go` (a struct
  literal's package is read from its source's top-level declarations; a
  source with none reads as the program package). `syntax.Printer` panics
  on a method with an unnamed receiver (`func (S) private()`), met while
  dumping merged programs.

## Commands

```sh
WS=$(pwd); U=<umbrella checkout holding the sibling bashy repo>
awk -v ws="$WS" '{sub(/=> \.\.\/sh$/, "=> " ws)}1' $U/bashy/go.mod > $WS/.bashy-build.mod; cp $U/bashy/go.sum $WS/.bashy-build.sum
(cd $U/bashy && go build -modfile=$WS/.bashy-build.mod -o $WS/bashy.real ./cmd/bashy)
$WS/bashy.real --bashpp --source=go --go-file <root.go>
$WS/bashy.real --bashpp --source=go --go-file <dir>/main.go --go-package test/a=<dir>/a.go --go-import-base test
PATH=/bin:/usr/bin:$(dirname $(which go)) go test -count=1 -timeout 30m -run GoSource ./interp/...   # baseline: the 8 known darwin failures
PATH=/bin:/usr/bin:$(dirname $(which go)) go test -count=1 -timeout 30m ./interp/...
PATH=/bin:/usr/bin:$(dirname $(which go)) go test -short -timeout 30m ./...
```

## S153.4b — second pass (evaluator owner 2)

Continued from 4260767f. Each closed mechanism has an outside-corpus
reproducer plus a nearby positive control under
`interp/testdata/sprint153/<mechanism>/`, run by `TestSprint153Evaluator`.

### Test harness

`TestSprint153Evaluator` was red on the clean tree before any edit: a mechanism
directory that holds only a lane's prose (`bridge`, `triage`, `output`) tripped
the `programs == 0` fatal before any reproducer ran. The harness now skips a
documentation-only directory. This surfaced one **pre-existing** divergence it
had been masking — `concurrency/deadlock_negative` compares full stderr, and
the interpreter cannot reproduce Go's goroutine stack dump after
`fatal error: all goroutines are asleep - deadlock!`. That reproducer belongs
to the concurrency lane (`concurrency/` testdata); its exact-stderr comparison
is the divergence, not an evaluator regression.

### Closed

| Root(s) | Mechanism | Commit | Reproducer |
|---|---|---|---|
| leaf-153 r1a asserted func handles; prerequisite for reflectmethod2/3 | A type assertion/switch compared an interface's dynamic type against the asserted type as text, but a `BashPPFuncType` rendered `func(p)(r)` — run-together params, always-parenthesised results, a trailing `()` for no results — while a dependency reports `func(string)`, `func(io.Writer, string) (int, error)`. Render function types Go-canonically at that boundary. | `func type assert text` | `func_type_assert_text/` |
| reflectmethod2/3 (then blocked, see open) | A call whose callee is a computed expression yielding a dependency func handle (`x.(func(M))(v)`) fell through to "computed callee is not a function"; the statement dispatcher only resolved local closures and named cells. Evaluate the computed callee and, when it is a native func handle, invoke it on the dependency. | `computed callee native` | `computed_callee_native/` |
| `typeparam/struct.go` | Embedding an instantiated generic and filling it with an alias of that instantiation (`field E[int]`, literal `Eint{…}`) was rejected because the composite-literal type check compared raw text; the pointer embed (`*Eint` into `*E[int]`) failed the same way. Resolve aliases before the comparison and make the alias canonicaliser transparent through a pointer. | `struct embed type alias` | `struct_embed_alias/` |
| part of `fixedbugs/bug517.go` (array-length arm) | `unsafe.Sizeof`/`Alignof` in a constant array length were handed to the dependency evaluator, where the pseudo-function has no callable symbol. Fold them from the operand's static type in the constant-integer evaluator, for operand shapes fixed by their own syntax (basic conversion, call of a declared function with a basic result, function value, basic literal). | `unsafe Sizeof/Alignof array length` | `array_length_unsafe/` |

### Open — needs another lane or an architectural change

1. **`unsafe` compile-time operators in the scalar and const paths** (sizeof.go;
   const-initializer regression issue15550/issue30709; issue57823 SliceData).
   * *Scalar position* (`println(unsafe.Sizeof(t))`): `bashPPBridgeScalar`
     claims the call before the evaluator sees it, because
     `bashPPBridgeHandles` (`bashpp_native_values.go`, bridge lane) returns true
     for any `unsafe.X(...)` selector (`r.bashPPImports["unsafe"] != ""`).
     NEEDED in the bridge lane: `bashPPBridgeHandles` must decline the
     compile-time operators `unsafe.Sizeof`/`Alignof`/`Offsetof` so the
     evaluator can fold them (the folding core is `bashPPUnsafeConstOperator` in
     `gosource_unsafe.go`, ready to extend to the syntax AST).
   * *Const-initializer* (`const _ = unsafe.Sizeof(func(){})`): rejected by
     `bashPPConstantScalarExpr` (`BASHPP-ECONST-EXPR`), and even if accepted the
     const group evaluates via `bashPPEvalScalarExpr`, which hits the bridge as
     above. NEEDED: recognise the operator as constant and fold it without the
     bridge (a rewrite-to-literal in the const group, owned, once the bridge
     declines it).
   * `Offsetof` needs struct field layout; `SliceData`/`String`/`StringData`
     are runtime conversions, not constants — both the bridge's (runtime).
   * A **package-level** array type whose length calls a package function
     (`type B [unsafe.Sizeof(F())]*byte`) is still rejected: the length is
     evaluated before the package's functions register in `bashPPFuncs`. A
     function-local type with the same length works. Ordering fix only.

2. **Generic map/slice element conversion by the instantiated type**
   (`typeparam/map.go`: `BASHPP-ECOLLECTION-ELEMENT: cannot use string value as
   float64`). `mapper[F,T any](s []F, f func(F) T)` with a func-literal argument
   binds `T` to `F`, so `make([]T,…)` carries the wrong element type. NEEDED
   (outside may-edit set): `bashPPInferTypeFromParam` (`bashpp_func.go:812`) has
   no `*syntax.BashPPFuncType` case, so a `f func(F) T` parameter contributes no
   `T` binding; add one recursing into Params/Results, or correct the
   func-literal type-arg propagation in `bashpp_generic_body.go`. The named-func
   arm (`strconv.Itoa`) already works via the converter's explicit type args.

3. **Function-valued struct field called** (`s.fn(args)`; abi/idata.go
   `undefined callable computed function`). `f := s.fn; f(args)` already works;
   only the direct call is misrouted to method dispatch (`type S has no method
   fn`). The fix must be one choke point because the call reaches the method
   resolver from statement, decl, scalar-expression and bridge-argument
   positions (the last two via `bashpp_scalar.go`/`bashpp_native_values.go`,
   both out of lane). NEEDED in `bashpp_func.go`: in `bashPPBindLocalSelector`
   (≈`:660`, and `bashPPBindMethod` ≈`:1371`), when `sel.method == nil &&
   sel.interfaceSpec == nil`, resolve the selector as a field via
   `bashPPResolveField`; if its `fieldType` underlies to a `*BashPPFuncType`,
   read the field value and return `bashPPClosure(cell.vr.Str)` instead of
   erroring. (A self-contained owned intercept was prototyped and reverted: it
   closed statement + value positions but not the bridge-argument position,
   which `bashpp_native_values.go:153` resolves directly.)

4. **Selector on a value returned through a call/interface**
   (`fixedbugs/issue21879.go` `?.frame has no fields`; `fixedbugs/issue54542.go`
   `ESELECTOR-ASSIGN: target is not a structured value`). `caller().frame`
   reads the selector's receiver with `bashPPReadExpr(x.X)`
   (`bashpp_struct.go:646`), which for a call result returns `meta == nil`, so
   the struct field set is unknown. The field is then a native `runtime.Frame`,
   so the full root additionally needs the bridge. Reducing the
   call-result-as-struct half (owned, `bashPPReadExpr` must carry the call's
   result `meta`) from the native half is the next step.

5. **Typed nil func value to reflect** (`fixedbugs/issue16331.go`). `(func())(nil)`
   evaluates correctly on its own (`f := (func())(nil); f == nil` → true); it
   fails only as an argument to `reflect.TypeOf`/`reflect.MakeFunc`, prepared by
   the bridge (`nil is not a scalar`). Bridge lane (`bashpp_native_*`): a typed
   nil function value must cross as a native argument.

6. **reflectmethod2/3 next blocker.** With mechanisms 1–2 above closed, the
   assertion `.(func(M))` now compares the asserted `func(M)` against the
   handle's dependency-reported `func(main.M)` — a local type unqualified in the
   asserted text but package-qualified in the dynamic type. Package-qualifying
   local type names inside a function signature at the bridge boundary is the
   gosource/bridge lane's `__gosource_pkg_N_` work (see "Needs for other lanes"
   above).

7. **Panicking self-checks** (leaf-153 r1a `panic:` rows). `maymorestack.go`
   needs `-d=maymorestack` gcflag semantics — not interpretable, record only.
   The `unsafe.Pointer`/`uintptr`/finalizer roots (issue24491a/b, issue45045)
   remain the no-memory-model class recorded in the first pass. The remaining
   `makeslice: len out of range` / `interface conversion` / `defer of nil func`
   rows each reduce to one of the bridge or memory-model classes above and are
   not evaluator-local.
