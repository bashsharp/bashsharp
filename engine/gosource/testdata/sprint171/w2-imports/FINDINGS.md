# Sprint 171 — lane `w2-gosource-imports` findings

Lane: cluster B of the Sprint 171 target TSV (imported-symbol front end +
type-switch initializer), story #360. Start tuple: product `be8a1cf7`. The
Sprint 165 salvage `refs/salvage/lane-203` was reviewed and split by
mechanism; its `const-interface` piece edits `interp/bashpp_interface.go`
(outside this seam) and is filed below as a request, its `comparable-decl`
piece closes no row in this cluster and was dropped (it is sound — its test
passes — and remains in the salvage ref for a lane that needs it).

Measurement: every root below was run locally on the darwin dev box with a
bashy built against this tree, with the harness's own interpreted argv
(`--bashpp --source=go [--go-import-base/--go-import-path/--go-package]
--go-file`), against a baseline bashy built at the start tuple. "First
cause" is the baseline's first line; "after" is this tree's.

## Mechanisms (one commit each)

| # | mechanism | commit |
|---|---|---|
| M1 | type switch with an init statement converts to a block scoping the init around the switch (`gosource/convert.go`) | `gosource: convert a type switch with an init statement` |
| M2 | imported generic functions instantiated at the call: the instantiation closure records reached `alias.Name[T...]`, the helper registers each, the call names it (`interp/bashpp_sprint171_imported_instances.go`, bridge/worker) | `interp: instantiate imported generic functions at the call` |
| M3 | method expressions on imported named types lower to the closure a type parameter's does (`gosource/convert.go`) | `gosource: lower method expressions on imported types to closures` |
| M4 | unsafe.Sizeof/Alignof/Offsetof recorded as expressions in every statement position (constant-valued calls generally; the unsafe operators also over a type parameter) and never claimed as a dependency request (`gosource/convert.go`, `interp/bashpp_native_values.go`) | `gosource+interp: treat unsafe layout operators as expressions` |
| M5 | unsafe.String answered by the interpreter from the byte storage the pointer names (`interp/bashpp_native_values.go`) | `interp: answer unsafe.String from interpreter-owned byte storage` |
| M2b | helper aliases settled before any symbol is emitted; an instantiation naming a package the helper does not import is skipped — caught by the interp gate (`TestGoSourceOriginalUnwrap`, `TestGoSourceTestingOriginalErrors`: `errors.AsType[*fs.PathError]` left the helper unbuildable) | `interp: settle helper aliases before registering instantiations` |

## Gates

`go test -short ./lower/ ./gosource/` green on the final tree; `./interp/` on the final tree shows only the 8 pre-existing darwin `GoSource*` failures (verified against the start tuple with the same test list).

## Rows

| root | mode | first cause (baseline) | mechanism | status |
|---|---|---|---|---|
| `testdir:fixedbugs/gcc61244.go` | compiled | `gosource: unsupported type switch initializer` | M1 | fixed (transpiles and runs) |
| `testdir:fixedbugs/gcc61244.go` | interpreted | `gosource: unsupported type switch initializer` | M1 resolves the switch; next: `16:14: BASHPP-EINTERFACE-VALUE: a has no dynamic type` (`(interface{})(a)` with `const a = 0`) | moved to 170/174 evaluator — request R1 below is the exact 7-line fix (salvage piece, test-proven) |
| `package:cmd/compile/internal/noder` | compiled | `gosource: unsupported type switch initializer` | M1 | mechanism landed; the package's next defect is W4's measurement (this lane has no package argv) |
| `testdir:fixedbugs/bug279.go` | interpreted | `unknown imported symbol or method: unsafe.Sizeof` | M4 | fixed |
| `testdir:fixedbugs/bug339.go` | interpreted | `unknown imported symbol or method: unsafe.Sizeof` | M4 | fixed |
| `testdir:typeparam/issue48094.go` | interpreted | `unknown imported symbol or method: __gosource_import_0_0_0.Sizeof` (unsafe under the linked package's rewritten alias) | M4 (the operator is recognised by the checker's object, not by the alias spelling) | fixed (rundir argv) |
| `testdir:fixedbugs/issue62203.go` | interpreted | `unknown imported symbol or method: maps.Clone` | M2 | fixed |
| `testdir:fixedbugs/issue68415.go` | interpreted | `unknown imported symbol or method: unique.Make` | M2 | fixed |
| `testdir:unsafe_string.go` | interpreted | `unknown imported symbol or method: unsafe.String` | M5 | fixed |
| `testdir:reflectmethod5.go` | interpreted | `unknown imported symbol or method: reflect.Type` (`var h = reflect.Type.Method`, a method expression) | M3 resolves it; next: `dependency mutation of interpreter-owned references is unsupported for reflect.ValueOf` | design — Sprint 153 reflect.ValueOf refusal (`interp/bashpp_native_transport.go`, `TestGoSourceBridgeReflectRefusals`): reflect.ValueOf over a method-bearing local type hands the dependency a value whose callbacks it could retain |
| `testdir:reflectmethod6.go` | interpreted | same as reflectmethod5 | M3; same next defect | design — Sprint 153 reflect.ValueOf refusal |
| `testdir:fixedbugs/issue24547.go` | interpreted | `unknown imported symbol or method: String` | none — first cause is not the front end: `var i fmt.Stringer = s` (s a local struct embedding `*bytes.Buffer`) is bridged as a native scalar of type `fmt.Stringer` (`receiver={"kind":"string","type":"fmt.Stringer","text":"s"}`), so `i.String()` never consults the dynamic value's promoted method through the embedded imported field; `s.String()` directly works | moved to interp interface dispatch (`bashpp_interface.go` / `bashpp_native_promoted.go`): an interface cell holding an interpreter-owned struct must dispatch through the value's method set, promoting through embedded imported fields by depth |
| `testdir:fixedbugs/issue53137.go` | interpreted | `unknown imported symbol or method: unsafe.Offsetof` | M4 resolves it; next: `23:8: BASHPP-EUNSAFE-OFFSET` (`unsafe.Offsetof(d.B)`, d `*S[K]`, promoted field of a generic struct) | moved to 170/174 evaluator (generic layout — design per the plan's rule) |
| `testdir:fixedbugs/issue54220.go` | interpreted | `unknown imported symbol or method: unsafe.Offsetof` | M4 resolves it; next: `23:10: BASHPP-EUNSAFE-OFFSET` (`Offsetof(v.i2)`, field of imported `atomic.Int64` — private layout) | moved to 170/174 evaluator; see D1 |
| `testdir:fixedbugs/issue57823.go` | interpreted | `unknown imported symbol or method: unsafe.SliceData` | none (see R3); the root then observes finalizers on interpreter storage | by-ID D9 (Sprint 165 interp-const) |
| `testdir:fixedbugs/issue59293.go` | interpreted | `unknown imported symbol or method: unsafe.SliceData` | none — `SliceData`/`StringData` return a pointer INTO interpreter storage; the bridge value channel has no interpreter-pointer kind, so they cannot be answered beside unsafe.String (M5) | moved to interp value builtins (request R3) |
| `testdir:fixedbugs/issue60601.go` | interpreted | `unknown imported symbol or method: unsafe.Sizeof` | M4 resolves it; next: SIGSEGV in `interp/bashpp_sprint165_const.go:129` (`goSourceStaticExprType`, DerefExpr case dereferences a nil pointer type on `*new(T)`); with R2 applied locally: `15:14: BASHPP-EUNSAFE-TYPE` | moved to 170/174 evaluator; request R2 is the nil guard (generic `Sizeof(*new(T))` layout then divide-by-zero — design per plan) |
| `testdir:fixedbugs/issue9604b.go` | interpreted | `BASHPP-ECOLLECTION-ELEMENT: 79:24: unknown imported symbol or method: unsafe.Sizeof` | M4 resolves it; next: `BASHPP-EUNSAFE-TYPE` on `unsafe.Sizeof(int(0))` — the C3 constant boundary wraps `int(0)` in a `BashPPConvertExpr` with `ConvType` (Lit) and no `ConvTypeExpr`, which `goSourceStaticExprType` does not read; with R2 applied locally: `BASHPP-EBUILTIN-TYPE: scalar cannot be used as *big.Int` | moved to 170/174 evaluator (R2 then a big.Int row) |
| `testdir:sizeof.go` | interpreted | `34:12: unknown imported symbol or method: unsafe.Sizeof` | M4 resolves it; next: `43:5: BASHPP-EUNSAFE-OFFSET` (`unsafe.Offsetof(p2.C)` through a pointer) | moved to 170/174 evaluator |
| `testdir:typeparam/issue47716.go` | interpreted | `unknown imported symbol or method: unsafe.Sizeof` | M4 resolves it (the generic `size`/`align` bodies fold under the frame); next: `44:28: BASHPP-EUNSAFE-TYPE` (`unsafe.Offsetof(v4.f2)`, `Tstruct[interface{}]`) | moved to 170/174 evaluator (generic layout — design per plan) |
| `testdir:typeparam/pair.go` | interpreted | `21:18: unknown imported symbol or method: unsafe.Sizeof` | M4 resolves it; next: `21:18: BASHPP-EUNSAFE-TYPE` (`unsafe.Sizeof(p.f1)`, field of `pair[int32,int64]`) | moved to 170/174 evaluator (instantiated field layout) |
| `testdir:typeparam/pairimp.go` | interpreted | `main.go:15:18: unknown imported symbol or method: unsafe.Sizeof` | M4 resolves it; next: `main.go:15:18: BASHPP-EUNSAFE-TYPE` (same shape as pair.go through the linked package) | moved to 170/174 evaluator |
| `testdir:typeparam/dictionaryCapture.go` | interpreted | `unknown imported symbol or method: ii0` (Barrier C) | none — PASSES at the start tuple `be8a1cf7` already (baseline bashy, rc=0), closed by 169/170 work before this lane | not this lane's; listed in the leaf TSV so the closure is leaf-proven |
| `testdir:typeparam/dictionaryCapture-noinline.go` | interpreted | as above | none — passes at the start tuple | as above |

## D1 — checker-constant unsafe operators the evaluator cannot lay out

`pair.go`, `pairimp.go`, `sizeof.go:43`, `issue54220`, `issue9604b:79` are all
*constant-valued* per go/types (the checker holds the number), but the
interpreter's layout engine (`goSourceLayoutType`/`goSourceOffsetof`) cannot
settle the operand's type. Sprint 162 and 165 already requested that the
converter materialise the checker's value while the generated Go keeps the
spelling (so `unsafe` stays used). This lane confirms there is no carrier
for that in the tree today: `BashPPCall` has no folded-value field and
`gosource.Program` carries no constant table; adding either touches
`syntax/` and `gosource/source.go` (W1's), so it is recorded here as the
design, not implemented. Folding to a `uintptr(N)` conversion in the
converter was rejected: it drops the import use in lowered Go (the very
cluster-C row W3 is unwinding).

## Requests to other seams (exact diffs)

R1 — `interp/bashpp_interface.go`, in `bashPPCellForInterfaceExpr`, the
`*syntax.BashPPIdent` case, after the `undefined value` check (from the
salvage; `TestSprint165ConstantInInterface` in the salvage ref drives it;
closes `gcc61244` interpreted):

```go
		// An untyped constant's name stores what its literal would: the
		// value with the constant's default type (`const a = 0; var i any =
		// a` holds an int). The constant cell carries no type of its own,
		// so the scalar path names the default from the value's kind.
		if cell.constant && cell.declType == nil && cell.typeName == "" && cell.interfaceValue == nil && cell.vr.Kind == expand.String {
			return r.bashPPScalarInterfaceCell(expr)
		}
```

R2 — `interp/bashpp_sprint165_const.go`, `goSourceStaticExprType` (a nil
guard that turns the `issue60601` SIGSEGV into the `BASHPP-EUNSAFE-TYPE`
row, and the `ConvType`-only conversion the C3 boundary emits):

```go
	case *syntax.BashPPConvertExpr:
		if x.ConvTypeExpr == nil && x.ConvType != nil {
			return &syntax.BashPPNamedType{Name: x.ConvType}, true
		}
		return x.ConvTypeExpr, x.ConvTypeExpr != nil
	...
	case *syntax.BashPPDerefExpr:
		typ, ok := r.goSourceStaticExprType(x.X)
		pointer, pointerOK := r.bashPPUnderlyingType(typ).(*syntax.BashPPPointerType)
		if !ok || !pointerOK {
			return nil, false
		}
		return pointer.Element, true
```

R3 — `unsafe.SliceData(s)` / `unsafe.StringData(s)` as interpreter value
builtins (the `bashPPValueBuiltin` family, beside `new`/`append`): SliceData
is `&s[0]`'s pointer when `cap(s) > 0`, the nil pointer for a nil slice, a
non-nil pointer to no element for a non-nil empty slice; StringData is a
pointer to the string's first byte (nil for `""`). They return
`*bashPPPointer` cells, which is why they cannot ride the bridge-value
return channel M5 uses. Closes `issue59293`; `issue57823` stays D9.

R4 — `issue24547`: interface dispatch on an interface cell holding an
interpreter-owned struct must resolve the method on the dynamic value's
method set (promotion through embedded imported fields by depth), instead
of bridging the interface variable as a native scalar of its declared
imported interface type.
