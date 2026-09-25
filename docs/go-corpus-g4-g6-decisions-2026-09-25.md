# Go corpus G4 + G6 decisions — 2026-09-25

Sprint 270, stories #759 (G4, `9fa63957d084`) and #761 (G6, `c8365c7b8c50`).
Pinned source: Go 1.27.1 `test/`. Starting state: the Linux leaf run of sh
`8b4eafc1`; the lane's sh base is `8ab770ed` (a descendant). Local checks run
on darwin/arm64 through the lane's `bashsharp --bashpp --source=go`, the
interpreted backend's direct Go-source argv, with the upstream runenv
(`GOEXPERIMENT=fieldtrack` where the recipe says `-goexperiment fieldtrack`).
No fixture edit, no timeout raise, no native fallback.

## Fixed (general repairs in sh)

sh commits: `69dda147` (G4 unsafe view) and `d12ce1a9` (G6 nointerface +
dependency string panics), branch `s270-g46-cornice` on `8ab770ed`.

| key (interpreted) | first cause | fix |
|---|---|---|
| `fixedbugs/issue47928.go` (G6) | `//go:nointerface` was ignored: the promoted `Bad` satisfied `interface{ Bad() }` | `interp`: under `GOEXPERIMENT=fieldtrack` a method carrying `//go:nointerface` is absent from dynamic interface satisfaction (gc: `noder/lex.go` `pragmaFlag`, `types2/lookup.go` `nointerface`); without the experiment the pragma is ignored, as gc does |
| `typeparam/mdempsky/15.go` (G6) | same, through generic receivers and promotion | same fix |
| `fixedbugs/issue30862.go` (G6, runindir) | same, but the method lives in an imported (linked) package whose directives were never attached | `gosource`: a linked package's `//go:nointerface` lines travel with its methods; its other directives keep their existing treatment |
| `fixedbugs/issue28748.go` (G6) | a `panic(string)` raised inside a native dependency call (here reflect's "closure returned zero Value") ended the program as a bridge failure | native worker: a string panic raised by the called dependency function returns as a program panic with the same string, so the caller's `recover` sees it (as it does for `regexp.MustCompile`); worker validation errors, runtime errors and callback implementation failures keep their paths |
| `fixedbugs/issue8004.go` (G4) | `(*reflect.SliceHeader)(unsafe.Pointer(p))` was refused at conversion (`BASHPP-EUNSAFE-VIEW` on amd64, `-LAYOUT` elsewhere) | `interp`: the conversion is legal Go and now yields a pointer that still names the source storage (stored, compared, converted back); every read or write through it is refused with the same diagnostic. The fixture only stores the header pointer, so it passes; no bytes are ever reinterpreted |

Local evidence (all rc=0, empty output as the fixtures require):
`bashsharp --bashpp --source=go --go-file <root>` for issue8004, issue28748,
issue47928 and mdempsky/15 (the last two with `GOEXPERIMENT=fieldtrack`);
issue30862 as a module program (`--go-import-path issue30862.dir
--go-package issue30862.dir/a=… --go-package issue30862.dir/b=… --go-file
main.go`, `GOEXPERIMENT=fieldtrack`).

## Passing locally without a change

| key | evidence | note |
|---|---|---|
| `fixedbugs/issue19078.go` (G4) | rc=0 in 12 s locally at the lane base | the leaf's 60 s timeout predates the collector pacing (`dc563272`) and printer reuse (`ace86713`) now in the base; needs the Linux replay to confirm. The bound was not raised |

## Excluded (exact root + interpreted mode)

| key | family | source evidence |
|---|---|---|
| `fixedbugs/issue9110.go` | gc-observation | `runtime.ReadMemStats(&stats1)` … `runtime.GC(); runtime.ReadMemStats(&stats2)`; `if int(stats2.HeapObjects)-int(stats1.HeapObjects) > 20 { print("BUG: object leak: ", …) }` — asserts the native runtime's sudog cache; the interpreter's goroutines/selects/`sync.Cond` waits are not native runtime objects (local run: `BUG: object leak: 0 -> 809 -> 845`) |
| `fixedbugs/issue15277.go` | gc-observation | `func inuse() int64 { runtime.GC(); var st runtime.MemStats; runtime.ReadMemStats(&st); return int64(st.Alloc) }` with `delta < 9<<20` / `delta > 1<<20` checks around `new(big)` (`type big [10 << 20]byte`) — every assertion is a MemStats.Alloc delta of the native heap. (`//go:build amd64`; the leaf's `signal: killed` is the boxed 10 MiB array, which would not make the MemStats assertions meaningful even if cheap.) |
| `maymorestack.go` | compiler-diagnostic | recipe `// run -gcflags=-d=maymorestack=main.mayMoreStack`; `if count == 0 { panic("mayMoreStack not called") } else if count != wantCount { … }` with `wantCount = 128` — the count is the number of gc-emitted stack-split prologues (the `//go:nosplit` hook itself and runtime code excluded), a `-d=` code-generation artifact; the interpreted backend also has no representation for recipe `-gcflags` (harness deviation "recipe flags … remain explicit evidence only") |

`tools/validate-go-corpus-catalog.sh` passes with the three rows (targets
reclassified repair → excluded; counts 278/273/271 excluded, 299/279 repair).

## Open (not excludable)

Also pre-existing on darwin/arm64 (unchanged by these commits):
`TestS270G4UnsafeRawLayoutExclusionEvidence/slice3…` expects the amd64
`BASHPP-EUNSAFE-VIEW` text and gets `BASHPP-EUNSAFE-LAYOUT` off amd64.


| key | exact cause |
|---|---|
| `fixedbugs/bug260.go` (G6) | `fmt.Sprintf("%p", &b1[0])` vs `&b1[1]`: element pointers of an interpreter array cross to the native `fmt` as independent descriptors, so their addresses are not `base + i*sizeof(T)`. Needs a layout-consistent address model for interpreter arrays; no unsafe, no GC observation — not one of the seven families |
| `nilptr.go` (G6) | `var dummy [256 << 20]byte` is materialised as one boxed cell per byte (`fatal error: runtime: out of memory`). The opaque address word of `&dummy` is already small (the `> 256<<20` guard passes) and the p1–p16 probes are nil dereferences the interpreter raises; the gap is a compact representation for large zero arrays |
| `peano.go` (G6) | native stack per interpreted call: measured locally, a plain `rec(n-1)+1` recursion overflows gc's 1 GB goroutine stack between depth 20 000 and 50 000; peano needs `count` depth 362 880. `interpreter-stack` blocked-design (165 D10e); deep recursion is not excludable |
