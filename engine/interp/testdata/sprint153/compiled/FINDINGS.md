# S153.1 compiled-mode findings

All reductions here are authored outside the upstream corpus.  Reproduction
used the lane-local `bashy.real`, `transpile --bashpp --source=go`, and `go run`
of the resulting source.  Directory reductions use the explicit package map:
`--go-import-base=test --go-import-path=<main-path> --go-package=<dep-path>=<dep-file> --go-file=<main-file>`.

| Root | Cause and generated-vs-original delta | Decision |
| --- | --- | --- |
| `fixedbugs/bug367.go` | Both direct and compiled execution treat an embedded `p.S.hidden` as satisfying `main.I.hidden`. Go package-private method identity is not preserved by the interpreter type-assertion runtime. | **(c)** S153.4b; no helper fix. |
| `fixedbugs/issue52856.go` | Both modes make identical anonymous structs returned by `main` and by dependency `a` assertion-compatible. The interpreter loses the defining package in dynamic type identity. | **(c)** S153.4b; no helper fix. |
| `fixedbugs/issue42401.go` | Both modes leave dependency initialization/linkname state incorrect (`a.Value` is not the initialized/linknamed storage). This is interpreter runtime package initialization/linkname behavior. | **(c)** S153.4b; no helper fix. |
| `fixedbugs/issue29919.go` | Original has `package a` with `var x = f()` and imports it blank from `main`, so its stack contains `test/a.init`. Generated code flattens it into `main`: `var __gosource_pkg_0_x = __gosource_pkg_0_f()`, with no dependency `a.init` frame. | **(b) -> 152.** Package linker flattening removes dependency init identity; no runtime helper can reconstruct it. |
| `fixedbugs/issue19467.go` | Original calls `test/mysync.(*WaitGroup).Add`; generated code declares `type __gosource_pkg_0_WaitGroup` and `func (wg *__gosource_pkg_0_WaitGroup) Add`. `runtime.Callers` therefore reports `main.(*__gosource_pkg_0_WaitGroup).Add`. | **(b) -> 152.** Package linker mangles and flattens the linked package symbol. |
| `fixedbugs/issue20014.go` | Original’s `go:"track"` fields are reported through linker-populated `fieldTrackInfo` (`-k=main.fieldTrackInfo`). Generated source retains `var fieldTrackInfo string` but the compiled artifact leaves it empty, producing only four zero lines rather than the two field names. | **(b) -> 152.** Emitter/linker does not preserve the Go linker field-track contract. |
| `rangegen.go` | The first generated source contains range-over-function text; the compiled pipeline lowers that generated program and exceeds the 60-second harness bound, while native completes in about one second. The emitted lowering is the owner of range-over-function control-flow expansion, not a reusable `__bpp_rt` primitive. | **(b) -> 152.** Investigate the emitted range-over-function state machine/per-call overhead; no helper fix. |

No `(a)` helper defect was found, so this story has no runtime-helper commit.
