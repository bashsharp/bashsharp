# S151.2 generics / interfaces — findings

Story #73 (Sprint 151). Root list: `../leaf/S151.2_generics.roots` (109 roots,
Go 1.27 corpus `test/`). Measured with a bashy built against this workspace
(`go build -modfile <private go.mod with replace> ./cmd/bashy`; the shared
`bashy/go.mod` replace is edited by other workers concurrently, so a private
modfile is the only reliable way to measure one workspace). Every root is run
the way the leaf-151 harness runs it: `--go-file` for `run`/`errorcheck`
(`--check`), `--go-import-base … --go-package … --go-file …` for `rundir`.

| state | PASS both modes | first-line clusters |
|---|---|---|
| branch point (c57dbb3c) | 1 / 109 | 19 `EGENERIC-INFER`, 22 `EGENERIC-CONSTRAINT`, 6 `ETYPESWITCH-OPERAND`, 5 `EINTERFACE-EMBED`, 3 `EINTERFACE-SIGNATURE`, … |
| after this branch | **38 / 109** | see "Remaining" below — 0 `EGENERIC-INFER`, 0 `EGENERIC-CONSTRAINT`, 0 `ETYPESWITCH-OPERAND` on runnable programs |

Every passing `run` root's stdout matches `go run` (Go's `println` writes to
stderr in both). Out-of-corpus reproducers live beside this file and are
pinned by `gosource/sprint151_generics_test.go` (run through `gosource.Load` +
`interp`, stdout compared with `go run`).

## The first question: should the interpreter infer at all?

No. `go/types` records the full instantiation of every generic function
callee in `Info.Instances` — inferred or written, complete or a prefix. The
converter now spells it as explicit `TypeArgs` on every `BashPPCall`
(`convert.go: instanceTypeArgs`, applied by `c.callee`), so the runtime binds
type parameters from the call and never runs `bashPPInferTypeArgs` for
Go-source programs. That one change (plus allocating `Info.Instances`, which
`newTypeInfo` never did — the existing `isInstantiation` lookup was dead)
took the list from 1 to 16 PASS with no interpreter edit, and it is what made
`EGENERIC-INFER` and the "string does not satisfy constraint" family vanish:
the runtime's inference read argument VALUES (`bashPPTypeOfArg`: int, bool,
else string), so any float, slice, func, or named argument inferred `string`.
The Bash++ dialect (no checker) still uses the runtime inference; nothing
there changed.

## Mechanisms landed (one commit each)

| commit | mechanism | reproducer |
|---|---|---|
| `gosource: spell every generic call's type arguments from go/types Instances` | explicit TypeArgs from `Info.Instances`; `checkedType` shared with `valueType` | `infer_typeargs.go` |
| `interp: substitute type parameters inside channel types` | `BashPPChanType` had no substitution case; `make(chan T)` bound whole | `chan_typeparam.go` |
| `interp: check a union term that names an interface by its type set` | `interface{ OrderedNumeric \| Complex }` | (absdiff3 progresses; see remaining) |
| `gosource: lower an instantiated generic function value as a forwarding closure` | `Abs[T]`, `pair[string,int]`, bare `Double` inferred from context → `func(a0 T0, …) R { return F[T](a0, …) }` | `generic_funcvalue.go`; spike rows `expr_indexlist_funcvalue.go`, `funcvalue_generic_selector.go` now load |
| `interp: read any(x), a call, or a copied interface as a type switch operand` | `switch any(x).(type)`, `switch f(i).(type)`, `iface := any(x)`, `xx := x` | `typeswitch_operand.go` |
| `interp: accept a single type term embedded in an interface` | `interface{ string }`, `interface{ *B; Set(string) }` | `embed_type_term.go` |
| `interp: compare method signatures by type tree; bind conversion targets that mention a type parameter` | instantiated `Iterator[int]` vs source `func(int) bool` compared as text; `IteratorFunc[R](it)` unbound | `iface_generic_method.go` |
| `interp: convert a function value to a named function type, keeping instantiated types as trees` | `IteratorFunc[int](it)`; `bashPPScalarNamedType` parses `Name[Args]` back (new `syntax.BashPPTypeExprFromText`) | `named_func_conversion.go` |
| `gosource: lower a method expression on a type parameter as a forwarding closure` | `f := T.String` | `typeparam_method_expr.go` |
| `interp: accept a type parameter as a map key` | `map[K]V` in generic struct/body | `map_typeparam_key.go` |
| `interp: defer the constraint check of a type-parameter argument to instantiation` | `type List[T Ordered] struct { next *List[T] }` | `recursive_constrained.go` |
| `interp: satisfy a type-term constraint by its type set alone` | `interface{ []int64 \| [3]int64 }`, `T []MyByte`, `P *S` | `term_constraints.go` |
| `interp: allow repeated blank type parameters` / `accept repeated blank type declarations` | `Pair[_, _]`, `type _[T any] struct{}` ×3 | `blank_typeparam.go` |

## Remaining roots (71) and why

Grouped by the mechanism that now stops them. "Not this card" means the
first failure is a mechanism owned elsewhere in Sprint 151; generics are no
longer the blocker for those roots.

### Not this card — evaluator: non-integral float constants (S151.5 evaluator)
`F(2.5, 1.0)` with `func F(a, b float64)` fails on HEAD without any
generics: "cannot use "5/2" as float64 value for parameter a" — the exact
rational spelling of an untyped float constant reaches a typed parameter.
Same for `x == 7.4` in a type-switch arm and for a range variable over
`[]float64{1.5}` passed to a closure. Roots: `typeparam/smallest.go`,
`typeparam/sum.go`, `typeswitch.go` (`cannot convert String to float64`),
`typeswitch1.go` (`string for float64`), `typeparam/dottype.go` (`dynamic
type float64, not int`). The reproducers here avoid fractional constants.

### Not this card — complex numbers
`real(z)`/`imag(z)`, `complex128` elements: `typeparam/absdiff2.go`,
`absdiffimp2.go`, `absdiff3.go` (its constraint problem is fixed; it now
stops at `re = real(z)`), `fixedbugs/issue79812.go`.

### Not this card — collections (S151.1/S151.5)
- Map keys beyond string/bool/integer (canonical-text storage): `*string`
  (`issue48453.go`), interface (`issue46591.go`), generic struct
  `key2[T1, T2]` (`metrics.go`).
- `reflect.DeepEqual` native slice retention: `map.go`, `mapimp.go`.
- Bridge float element in `map[K]V` literal (`maps.go`, `mapsimp.go`).
- Elided composite type with pointer element `[]P{{f: 1}}`
  (`issue50833.go` — its `P *S` constraint is fixed).
- `issue47723.go` (unsupported scalar call in index), `issue49295.go`,
  `issue58513.go` (`scalar cannot be used as func()()` — a func element in
  a slice literal), `graph.go` (`from is not a scalar` in a composite).

### Not this card — pointers (S151.3)
- `PT(&result[i])` / `p := &x` scalar forms: `settable.go`, `geninline.go`
  (`unsupported scalar expression *syntax.BashPPAddressExpr`).
- Nil-receiver / field-receiver pointer chains: `issue50109.go` (`expression
  is not a pointer`).
- `unsafe.Sizeof`/`Offsetof`: `pair.go`, `pairimp.go`, `issue53137.go`,
  `issue65417.go` (the latter's first line is still `EGENERIC-INFER` but it
  is the `str[unsafe.Sizeof(t)]` index, raised from inside `shouldPanic`).

### Not this card — converter runtime pieces (S151.4)
- `computed call runtime is not implemented` (calling a func-typed struct
  field `obj.PrintFn()(m)`, `s.f()`): `issue50690a/b/c.go`,
  `mdempsky/19.go`. The same limitation exists without generics
  (`op.Run(21)` on a `func(int) int` field fails on HEAD).
- `Go value requires one result` / `expression requires one local result`:
  `issue45722.go`, `issue50690a.go`.
- Unregistered bridge type for a generic pointer type through the
  dependency bridge: `issue50419.go`, `issue50481b.go`, `issue50481c.go`
  (`unregistered bridge type "*Foo[string, int]"`).

### Not this card — checker verdict (S151.5)
`typeparam/tparam1.go` is an `errorcheck` root; bashy `--check` emits the
expected `T redeclared in this block` diagnostics and exits 2, which is the
harness's errorCheck comparison to settle.

### This card — still open (design items, not attempted)
- **Interfaces from non-mapped packages embedded in a local interface**:
  `type Strunger interface { fmt.Stringer; Strung() string }`
  (`fixedbugs/issue15528.go`), `encoding.BinaryMarshaler`
  (`typeparam/issue47713.go`). The runtime knows no method set for
  `fmt.Stringer`. Fix belongs in the converter: expand an embedded imported
  interface's method set from `go/types` (`types.Interface.Method`) into
  the local `BashPPInterfaceType` at lowering. Half a day.
- **Conversion to an interface / generic type used as a VALUE** (not only as
  a type-switch operand): `Stringer(v)` in an expression, `I[*S](x)`,
  `T[int](x)`, `Mer[X](x)`, `(interface{})(x)`: `typeparam/eface.go`,
  `issue47925b/c/d.go`, `issue53477.go`, `issue54456.go`,
  `typeparam/ifaceconv.go`. The scalar converter has no interface target;
  `bashPPInterfaceConversion` (added here) handles the operand position and
  the short declaration, and is the piece to route the assignment,
  argument and return positions through. One day.
- **Generic struct pointer identity across instantiation**: `cannot use
  *_Element[T] as *_Element[string]` (`list2.go`, `listimp2.go`) — a
  pointer stored while `T` was open is compared by text against the
  instantiated spelling; the assignability check needs the frame's
  bindings applied to the stored type. Half a day.
- **Method on an instantiated generic type reached through a value**
  (`C[int] has no field or method "reset"`, `issue47775b.go`; `memResource
  has no field or method "teardown"`, `fixedbugs/issue59709.go`): method
  set lookup by the instantiated NAME. Related to the previous item.
- **Named function type / func-valued field assignment**:
  `issue48137.go` (`cannot assign func()(T) to Bar`), `issue48617.go`
  (`Bar method CreateBar has wrong signature` — a named func type
  `Bar[T] func() Bar[T]` whose method's result is the receiver type;
  signature text of a recursive generic named type).
- **Interface value plumbing**: `fixedbugs/bug269.go` (`undefined value f`
  — interface from a call result in a `var` declaration),
  `fixedbugs/bug494.go` (promoted interface method through an embedded
  interface field), `fixedbugs/issue4590.go` (`struct cannot implement
  interface` — anonymous struct with embedded interface),
  `typeparam/equal.go` (comparison of two interface values holding
  unrepresented dynamic values), `typeparam/mdempsky/16.go`
  (`interface{ T() T }(nil).(T)` — nil interface conversion then a
  panicking assertion), `issue42758.go` (`nil is not a scalar` — `T(nil)`
  style zero).
- **Type parameter used as a selector root**: `mdempsky/15.go`,
  `mdempsky/20.go` (`E`/`T is not a structured value` — `T.M` method
  expressions on a pointer-constrained parameter and `E(x).f`).
- **Receiver generic type named through a value in another package**:
  `chans.go`, `chansimp.go` (`_Receiver is not a structured value` — a
  generic struct value whose field is read after a channel round trip).
- **Local types with the same name in different functions**:
  `typeparam/nested.go` (`type Int redeclared`) — the runtime has one type
  namespace (documented limitation in `typeString`); needs function-scoped
  type registration.
- `issue47740b.go` (`2 variable(s) but 1 value(s)` — tuple result from a
  generic method value), `issue47901.go` (`undefined callable make` — `make`
  reached through a computed call), `issue48185b.go` (composite literal of
  a generic named type in scalar position), `select.go` (`r is not a scalar`
  — a receive result used as an operand), `cons.go`, `issue50642.go`
  (`scalar call interrupted` — recursive generic call chains; needs a
  trace), `boundmethod.go` (silent exit 2 after `reflect.DeepEqual`; the
  three sub-cases each pass in isolation — the trimmed program with
  `StringStruct[myint]` prints correctly).

## Notes for whoever continues

- `bashPPScalarNamedType` (interp/bashpp_generic_body.go) is the seam for
  every place a scalar's type NAME becomes a type: instantiated names
  (`Name[Args]`) parse back to trees there.
- `forwardingClosure` (gosource/convert.go) is the seam for any "function
  value with no runtime representation": instantiated generic function,
  method expression on a type parameter. `(*T).M` on a pointer-constrained
  parameter would go through it too.
- The interp package's full short suite takes ~25–30 min on this shared
  machine; `-timeout 60m` is required or the default 10 m panics.
