# Spike B — dependency-bridge mechanism classes

Story: S153.2 · Story-ID: 7f74c9ff55b9 · Sprint #153 · FINDINGS ONLY (no product edits)

## Method

Each root was run through a lane-local `bashy.real` in interpreted mode:

```
bashy.real --bashpp --source=go --go-file <corpus>/<file>
```

against the read-only Go corpus at `<corpus> = .../go/test`. Diagnostics below are
the exact first `gosource:`/`bash++` line emitted (user paths elided). The bridge
source read for attribution: `interp/bashpp_native_bridge.go`,
`bashpp_native_transport.go`, `bashpp_native_slices.go`, `bashpp_native_callback.go`,
`bashpp_native_types.go`, `bashpp_native_worker.go.txt`.

The bridge is a persistent, pure-Go, `CGO_ENABLED=0` child that runs *only imported
dependency operations* over a JSON value transport (`bashPPBridgeValue`). Anything
that would make the dependency (a) call back into interpreted code, (b) retain or
mutate interpreter-owned storage past the call, or (c) require code the worker
template cannot compile is refused fail-closed. The six mechanism classes below are
the concrete faces of those three constraints.

## Findings table

| Root | Exact diagnostic | Class (protocol limitation) | Smallest protocol change that closes the whole class | Size (h) |
|------|------------------|-----------------------------|------------------------------------------------------|----------|
| `fixedbugs/issue57184.go` | `gosource: asynchronous or retained original function callbacks are unsupported for reflect.TypeOf` | **C1a callback into interpreted code — reflect over a local type.** The value handed to `reflect.TypeOf` carries a local type whose methods are treated as retained/async callbacks; `validateLocalTransport` (transport.go:129) refuses. | Add a **type-only transport for `reflect.TypeOf/ValueOf/New`**: register the local type in the export table (`//TYPES`) and hand `reflect` a bare type/value descriptor *without* marking the value callback-bearing. A method callback is wired lazily only if a method is actually `Call`ed, reusing the existing method-callback path. | 6–8 |
| `convert.go` | `gosource: asynchronous or retained original function callbacks are unsupported for reflect.TypeOf` | **C1a** — same reflect-over-local-type shape. | Same as C1a (single change closes both). | (incl.) |
| `uintptrescapes3.go` | `gosource: original callback signature requires value-semantics parameters and supported results` | **C1b callback into interpreted code — retained function callback with pointer params.** `runtime.SetFinalizer(x, func(*T))` registers an interpreted func the native runtime keeps and may invoke later; the pointer parameter fails the value-semantics check. | Add a **retained-callback registry with pointer-identity delivery**: generalize the existing retained-callback server (today gated to `flag.Parse`-style mutators via `nativeRetainedPointerMutator`) so any retained func handle parks every later request as a callback server (machinery already in `enterCallbacks`/`requestCallbackCapable`), and admit pointer-receiver params by binding original pointer identity. | 12–16 |
| `tinyfin.go` | `gosource: original callback signature requires value-semantics parameters and supported results` | **C1b** — `runtime.SetFinalizer` finalizer func. | Same as C1b. | (incl.) |
| `mallocfin.go` | `gosource: original callback signature requires value-semantics parameters and supported results` | **C1b** — `runtime.SetFinalizer` finalizer func. | Same as C1b. | (incl.) |
| `rangegen.go` | `gosource: fmt.Fprintf requires a dependency-owned writer; original Write callbacks are unsupported` | **C3 dependency-owned io.Writer.** `fmt.Fprintf(w, …)` where `w` is interpreter-owned; refused at transport.go:16–19. The dependency cannot format into a writer it does not own. | Add a **Write-callback proxy handle**: represent an interpreter-owned `io.Writer` as a dependency handle whose `Write` calls back into the interpreter (reuse the synchronous Read/String callback path in reverse), and recognize the standard streams / `*os.File` as already dependency-owned. Closes every `fmt.F*`/`io.Copy`-to-local-writer case. | 6–8 |
| `fixedbugs/issue19028.go` (`.dir/main.go`) | interp: `could not import ./a (relative import path requires an import base)`; underlying class diagnostic (per spec) `original method T.f is not supported by dependency transport` | **C2 method on an interpreted type called by native code.** `reflect.TypeOf(x).Method(i)` enumerates *all* methods of local `T` — including unexported `f` and multi-result `H` — which the mirror does not reproduce (`OmittedMethods`, transport.go:47–52). (In interp mode the `rundir` relative import `./a` blocks first.) | **Mirror the full method set** of a local type — exported + unexported, any result arity — as callback stubs, so the reflect method set matches. Drop `OmittedMethods` by extending `bashPPLocalTypeGo` mirror generation. *Dependency:* method extraction may be owned by `gosource/` (S152 seam); if so, the descriptor must carry all methods — record in FINDINGS, do not edit `gosource/`. | 8–10 |
| `reflectmethod2.go` | `gosource: original method M.UniqueMethodName is not supported by dependency transport` | **C2** — `reflect.Type.Method`/`FieldByName` over local `M`'s methods. | Same as C2. | (incl.) |
| `reflectmethod3.go` | `gosource: original method M.UniqueMethodName is not supported by dependency transport` | **C2** — same reflect-method-enumeration shape. | Same as C2. | (incl.) |
| `typeparam/double.go` | `gosource: native slice retention or mutation is unsupported for reflect.DeepEqual` | **C4 retained slice.** `reflect.DeepEqual(a, b)` receives interpreter-owned slices; it is not in the read-only whitelist (`nativeSliceReadOnly`), so slices.go:211 refuses it as retention. | Add `reflect.DeepEqual` (and structural reflect readers) to the **read-only structural-emitter set**: it walks the transported value tree and returns a bool, retaining nothing — same footing as `encoding/json.Marshal`. Whitelisting lets the slice cross read-only (or compute it interpreter-side, cf. `slices.Equal`). | 3–5 |
| `typeparam/stringer.go` | `gosource: native slice retention or mutation is unsupported for reflect.DeepEqual` | **C4** — `reflect.DeepEqual` over slices. | Same as C4. | (incl.) |
| `typeparam/map.go` *(chosen retention root)* | `gosource: native slice retention or mutation is unsupported for reflect.DeepEqual` | **C4** — `reflect.DeepEqual` over slices/maps. | Same as C4. | (incl.) |
| `escape_runtime_atomic.go` *(chosen build-bridge root)* | `could not import internal/runtime/atomic (use of internal package internal/runtime/atomic not allowed)` | **C6 worker does not compile / dependency unbuildable.** The dependency is an `internal/` package that cannot be imported by the generated `package main` worker. | No in-protocol transport makes this runnable in a pure-Go isolated worker. Whole-class close = **detect unbuildable imports early and emit one uniform fail-closed diagnostic** ("dependency X cannot be built into the pure-Go bridge worker") instead of raw importer/`go build` noise. Actual execution: N/A. | 2–3 (diagnostic only) |
| `fixedbugs/bug514.go` *(chosen build-bridge root)* | `bash++ import "runtime/cgo": package requires cgo, which this pure-Go shell does not provide` | **C6** — cgo dependency; the `CGO_ENABLED=0` worker cannot build it. | Same as C6 (uniform early refusal). | (incl.) |
| `fixedbugs/issue34968.go` *(extra build-bridge witness)* | `could not import C (… unknown import path "C": internal error: module loader did not resolve import)` | **C6** — `import "C"` cgo; unresolvable in the isolated worker. | Same as C6. | (incl.) |

Chosen **native slice retention** roots: `typeparam/double.go`, `typeparam/stringer.go`,
`typeparam/map.go` (all `reflect.DeepEqual` over interpreter slices; these are the only
retention witnesses in `typeparam/`). Chosen **build dependency bridge** roots:
`escape_runtime_atomic.go` (internal package) and `fixedbugs/bug514.go` (cgo), with
`fixedbugs/issue34968.go` (`import "C"`) as a third witness.

## Recommended order

Ranked by (roots unblocked ÷ cost) and by unlocking later work:

1. **C4 — `reflect.DeepEqual` read-only slice** (3–5h). Cheapest; unblocks the three
   `typeparam/` retention roots and generalizes the existing read-only whitelist. Do first.
2. **C3 — dependency-owned `io.Writer` (Write-callback proxy)** (6–8h). `fmt.Fprintf`/`io.Copy`
   to interpreter writers is pervasive; the proxy handle is reusable by C1b later.
3. **C1a — `reflect.TypeOf/ValueOf` type-only transport** (6–8h). Closes the two `reflect.TypeOf`
   roots and lays the type-registration groundwork C2 also needs.
4. **C2 — full method mirror for interpreted types** (8–10h). Depends on the type-descriptor
   work from C1a; may need a `gosource/` seam change — gate on that finding, do not edit `gosource/`.
5. **C1b — retained finalizer / async function callbacks** (12–16h). Hardest: GC/finalizer
   lifetime semantics are non-deterministic; build last, on the C3 proxy + C1a registry.
6. **C6 — unbuildable dependencies** (2–3h, diagnostic only). Not truly closable in a pure-Go
   isolated worker; only unify the fail-closed diagnostic. Lowest priority.

Total genuinely-closing effort (C1–C4): ~35–47h; C6 is a diagnostic-quality tail, not a close.
