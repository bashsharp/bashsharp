# S153.2 — dependency-bridge callbacks, methods and writers (bridge owner)

Sprint: #153 · Story: S153.2 · Story-ID: 7f74c9ff55b9

Continuation of S153.4a (FINDINGS.md in this directory). Each root is listed
with the commit that closed its bridge-side class, or the open reason. Class
letters follow SPIKE-B.md. Reproducers: `interp/testdata/sprint153/writer-proxy/`,
`reflect-type-only/`, `method-mirror/`, `embedded-transport/`,
`retained-callback/`, tests `gosource_sprint153_{writer,reflect,methods,embedded,retained}_test.go`
and `bashpp_output_order_test.go`.

## Commits (mechanism → subject)

- C3 `interp: alias handle pointees for dependency writers and drain child output per request`
- C3′ `interp: claim wg.Go through an original pointer to the native WaitGroup`
- C1a `interp: transport reflect.TypeOf arguments type-only`
- C2 `interp: mirror the full expressible method set of materialised local types`
- C2′ `interp: run original method callbacks on typed-nil pointer receivers`
- E4 `interp: transport embedded struct fields by real embedding and promoted storage`
- S1 `interp: decode interface-identity scalars at their own kind`
- C1b `interp: record the retained-finalizer design and lock its prompt refusal`

## C3 — dependency-owned writer + ordered output

The measured shape of every writer root was one mechanism: an original
pointer (`&b`, `new(bytes.Buffer)`) whose pointee is a native handle decoded
in the worker as a *copy*, severing identity, so fmt.F* writers were refused
fail-closed. The worker now binds such a pointer to the handle's own
addressable storage; the transport admits fmt.Fprint/Fprintln/Fprintf for a
handle or a pointer-to-handle; pointer-to-handle receivers route method calls
to the bridge. No Write-callback proxy was needed for this class — the writer
was already dependency storage.

| root | status |
|------|--------|
| rangegen.go | writer refusal **closed (C3)**. Residual: `invalid bool` — scalar values carrying a declared `interface{}` identity (interpreted variadic `...any` re-spread into fmt) failed worker decode; see "interface-identity scalars" below. |
| fixedbugs/bug483.go | **closed (C3)** → matches native (exit 0). |
| output/interleaving (S153.5 handoff) | **closed (C3)**: per-request output-drain barrier — session-owned pipes, host-written random sentinel consumed by the copier before the interpreter continues; program exit waits for the copiers. `TestGoSourceOrderedOutputInterleaving` Skipf → Fatalf, passes twice. |
| var b bytes.Buffer / new(bytes.Buffer) / *bytes.Buffer parameter shapes | **closed (C3)**, reproducers in writer-proxy/. |
| original type with its own Write method | stays refused promptly (`requires a dependency-owned writer`), `TestGoSourceBridgeWriterRefusal`. A true Write-callback proxy is only needed for this residual shape; no corpus root in this story's list requires it. |

The story's "7 roots" tally is not enumerable from this repo's data
(`gosource/testdata/sprint151/leaf/active-151-leaf1.tsv` records only
rangegen.go for this diagnostic); the class mechanism plus bug483/rangegen
were verified directly.

C3′: the pointer-to-native receiver surface let `p.Go(f)` reach the bridge
before the WaitGroup launcher saw it; the launcher now unwraps an original
pointer to the dependency's WaitGroup. `TestGoSourceWaitGroupPointerReceiverBlocked`
flipped from recorded-gap early-return to a full native comparison — passes.

## C1a — reflect.TypeOf type-only transport

| root | status |
|------|--------|
| convert.go | **closed (C1a)** → matches native. Original function args of reflect.TypeOf cross as bare type descriptors (signature rendered in the program's own spellings, resolved by the worker, zero value handed to reflect); method-bearing values are admitted read-only; the returned descriptor is not marked callback-bearing. |
| typeparam/issue48645a.go | **closed (C1a)** → matches native (`func(func(int) bool)`). |
| fixedbugs/issue57184.go | TypeOf stage closed; now stops at `reflect.MakeFunc(typ, func(args []reflect.Value)...)` — an original closure with `[]reflect.Value` parameters refused at callback registration. OPEN: a MakeFunc trampoline is a distinct protocol extension (original callback invoked with reflect-value vectors), not bounded within this story. |
| testing.AllocsPerRun rows (fixedbugs/issue4667.go, issue4618.go, issue36516.go) | OPEN **by design**. The observable is the child's allocation count and the callback trampoline's own child-side allocations (JSON encode/decode per invocation) are part of that measurement; serving the callback would answer with a number native Go never produces. Refusal kept, promptness locked by `TestGoSourceBridgeReflectRefusals`. |
| reflect.TypeOf over a variadic original function | still refused at callback registration (`variadic original callbacks are unsupported`) before the type-only rewrite sees it — bounded residual, no root requires it. |

## C2 — full method mirror

Every method with an expressible non-variadic signature — exported or
unexported, any result arity — is now mirrored by the generalised stub
(zero-arity methods routed by a new `General` marker). Mirror signatures
record their own local-name refs; a mirror naming an unmaterialised type is
dropped back into OmittedMethods without dropping its owner type. The method
set did **not** need `gosource/` — no seam change was required.

| root | status |
|------|--------|
| reflectmethod2.go / reflectmethod3.go | method-set refusal **closed (C2)**: `reflect.TypeOf(v).MethodByName("UniqueMethodName")` resolves against the mirrored set. Residual: the invoking spelling `m.Func.Interface().(func(M))(v)` fails `gosource: computed call runtime is not implemented` — the evaluator's statement-position refusal for computed callees (`interp/bashpp_p1.go`, `bashPPCall`, CalleeExpr branch). **Evaluator lane**, not bridge: the callee value is already a native func handle the bridge can dispatch; the statement dispatcher never evaluates it. |
| fixedbugs/issue19028.go (.dir) | OPEN: `rundir` relative import `./a` blocks at the loader before any bridge request (same class as the S153.4a `.dir` rows). The method-set half is closed by C2. |
| dependency-invoked mirrors | proven by `method-mirror/marshaler_invoked.go`: json.Marshal runs an original `MarshalJSON() ([]byte, error)` body via the callback and marshals its result. |
| variadic method signatures | stay omitted; owner type keeps the prompt omitted-method refusal for non-fmt consumers (`TestGoSourceBridgeMethodMirrorRefusal`). |

C2′: the full mirror surfaced two pre-mirror policies. A typed-nil pointer
of a method-bearing local type was refused at transport, regressing the Tour
nil-interface program once its M() was mirrored; a nil receiver now binds a
typed-nil pointer cell and the original body runs exactly as native Go
invokes it (nil-checking bodies observe nil; dereferencing bodies panic as
the original would). Boundary tests superseded faithfully were flipped to
native comparisons — mirrored fmt.Formatter (mirror + writer aliasing
together), nil-receiver String, wrong-signature/[]error Unwrap — while the
value-receiver copied-slice mutation stays refused
(`TestGoSourceCallbackReferenceBoundary`, `TestGoSourceUnwrapTransportBoundaries`).

## E4 — embedded-field transport

`*<unsupported embedded field>` **closed**: the helper renderer emits real
embedding of the rendered element type (promotion is the dependency's own Go
semantics — promoted methods included), the structural spelling renders the
element type text, the host transports embedded storage under its promoted
name, and generated codecs read/write that storage (`&Dst.Base`,
`&Dst.Mutex`). Embedded instantiated-generic spellings stay unmaterialised
with the prompt unregistered refusal (`TestGoSourceBridgeEmbeddedRefusal`).

## C1b — retained callbacks with pointer parameters (SetFinalizer)

OPEN by design; per the story instruction the design is recorded instead of
half-landed, because a faithful close needs **two** designs, and the second
has no bounded form:

1. *Retained-callback registry with pointer-identity delivery* — protocol
   work generalising the retained-callback server so a parked request can
   deliver a callback whose parameter is a pointer bound to original
   identity. Bounded (the SPIKE-B sketch), but insufficient alone.
2. *A GC-lifetime mirror.* The finalizer trigger is the child runtime
   collecting an allocation that mirrors interpreter-owned storage
   (`x := new(int32)` lives in interpreter cells). Mirrored child
   allocations are either pinned forever by the session's handle/origin
   tables (finalizers never fire → tinyfin's own 5s timeout panics) or
   collected out of sync with the original's liveness (finalizers fire when
   native Go would not — uintptrescapes3 asserts exactly that they must
   not). tinyfin further asserts tiny-allocation *combination* behaviour, a
   runtime implementation detail no mirror reproduces.

Roots uintptrescapes3.go, tinyfin.go, mallocfin.go: refusal
`original callback signature requires value-semantics parameters and
supported results` — prompt, locked by
`TestGoSourceBridgeRetainedFinalizerRefusal`.

## Interface-identity scalars (rangegen residual)

An interpreted `...any` variadic re-spread into an imported call sends each
scalar with `Type:"interface {}"`; worker decode resolved that identity to
the interface type and failed (`integer for interface {}`, `invalid bool`).
Closed in the follow-up commit (see git log) — a scalar carrying a declared
interface identity decodes at its own kind and stays assignability-checked
against the interface target.

## Gates

- Baseline `go test -count=1 -timeout 30m -run GoSource ./interp/...` before
  first edit: the 8 known darwin failures recorded (Tour callback boundaries,
  retained-callback policy, image retained-consumer boundary, native-read
  slice boundaries, nil aggregate func/channel fields, receive channel
  storage, TCP scratch SIGTERM, unwrap pointer reentry). Post-change sweeps
  show the same set and nothing new.
- Full `go test -count=1 -timeout 30m ./interp/...` and
  `go test -short -timeout 30m ./...` before declaring done.
