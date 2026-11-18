# S152 asmcheck residue after H1

Method: each original `go/test/codegen` root was transpiled with an isolated
`bashy.real` bound to this checkout, then the original and generated scratch
modules were built with `GOARCH=amd64 go build -gcflags=-S`.  The quoted source
diffs are gofmt-normalised after dropping `//line` and `// lower:` lines.  The
S152.1 ref `27ffe0c4` was also transpiled as a control.  `no (S152.1: yes)`
means current master does not close the row, but the fetched S152.1 emitter did
in the same scratch comparison.

| root | failing pattern | source diff excerpt (original → generated) | class | current master closes it? | if no: one-line fix suggestion (file) |
|---|---|---|---|---|---|
| `codegen/append.go:18` | `^.*moveSliceNoCapNoScan\\b` | `var r []int` → `var r []int; _ = r` | C2 — Sink statements | no (S152.1: yes) | Do not emit go-source unused sinks; they move the return's code position. `lower/compile.go` |
| `codegen/clobberdead.go:20` | `^MOVL \\$3735936685, command-line-arguments\\.x` | `//go:noinline`<br>`func use(T) {}` → `func use(__bpp0_unnamed0 T) {}` | C1 — All comments dropped | partial | Preserve function `//go:noinline` through Go-source conversion (then retain the original package mode for the qualified symbol). `gosource/convert.go` |
| `codegen/clobberdeadreg.go:22` | `^MOVQ \\$-2401018187971961171, AX` | `//go:noinline` / `//go:registerparams`<br>`func RegArgsCall(int, int, int, S) {}` → `func RegArgsCall(__bpp0_unnamed0 int, ...) {}` | C1 — All comments dropped | partial | Carry both function directives into emitted Go so the calls remain non-inline and register-parameter calls. `gosource/convert.go` |
| `codegen/comparisons.go:54` | `^CMPW command-line-arguments[.+_a-z0-9]+\\(SP\\),` | `package codegen` → `package main` | C5 — `func main` renamed + synthetic `main` | no | Preserve the source package for Go-source compilation instead of forcing `main`. `lower/compile.go` |
| `codegen/condmove.go:120` | `^CMOVQHI` | `r = r - ldexp(y, rexp-yexp)` → `r = (r - ldexp(y, (rexp - yexp)))` | C3 — Untyped constants materialised | no | Under `goSource`, retain the source expression/constant form instead of materialising contextual types. `gosource/convert.go` |
| `codegen/ifaces.go:26` | `^CALL runtime.typeAssert` | `return x.(I)` → `return __bpp0_rt.MustValue(__bpp0_rt.Assert[I](...))` | C9 — Type assertions routed through the runtime | no (S152.1: yes) | Emit a plain Go type assertion in Go-source mode. `lower/checked_values.go` |
| `codegen/issue59297.go:11` | `^MOVQ AX, BX` | `//go:noinline`<br>`func h(a, b int) {}` → `func h(a, b int) {}` | C1 — All comments dropped | partial | Preserve `//go:noinline` on Go-source function declarations. `gosource/convert.go` |
| `codegen/issue60324.go:11` | `^LEAQ command-line-arguments\\.h\\.func1` | `func main() {` → `func __bpp0_sourceMain() {`<br>`func main() {}` | C5 — `func main` renamed + synthetic `main` | no (S152.1: yes) | Keep an input `func main` as the entry point; do not add a wrapper. `lower/callables.go` |
| `codegen/memcombine.go:389` | `^ADDQ \\(` | `uint64(s[7])<<56 \| ...` → `((((uint64)(s[7]) << 56) \| ...) \| ...)` | C4 — Expression re-parenthesisation | no | Print Go-source expressions with their original precedence/parentheses. `lower/compile.go` |
| `codegen/memops.go:17` | `^CMPB command-line-arguments.x\\+1\\(SB\\), [$]0` | `package codegen` → `package main` | C5 — `func main` renamed + synthetic `main` | no | Preserve the source package for Go-source compilation instead of forcing `main`. `lower/compile.go` |
| `codegen/regabi_regalloc.go:12` | `^MOVQ BX, CX` | `//go:registerparams`<br>`func g(int, int, int) {}` → `func g(__bpp0_unnamed0 int, ...) {}` | C1 — All comments dropped | partial | Preserve `//go:registerparams` on Go-source declarations. `gosource/convert.go` |
| `codegen/slices.go:158` | `^.*runtime\\.mallocgc` | `a := make([]int, len(s))` → `a := make([]int, len(s)); _ = a` | C2 — Sink statements | no (S152.1: yes) | Do not emit a sink after a Go-source short declaration. `lower/compile.go` |
| `codegen/switch.go:249` | `^CMPL \\(.*\\), \\$1836345390$` | `case ".htm":` → `case string(".htm"):` | C3 — Untyped constants materialised | no | Leave untyped case constants unwrapped in Go-source mode. `gosource/convert.go` |
| `codegen/zerosize.go:16` | `^MOVQ \\$0, command-line-arguments\\.s\\+56\\(SP\\)` | `//go:noinline`<br>`func g(**int, int, int, int, int, int) {}` → `func g(__bpp0_unnamed0 **int, ...) {}` | C1 — All comments dropped | partial | Preserve `//go:noinline` so `g`/`noliteral` cannot erase the stack object. `gosource/convert.go` |

C1 (all comments dropped) accounts for 5 roots: clobberdead, clobberdeadreg, issue59297, regabi_regalloc, zerosize.

C2 (sink statements) accounts for 2 roots: append, slices.

C3 (untyped constants materialised) accounts for 2 roots: condmove, switch.

C4 (expression re-parenthesisation) accounts for 1 root: memcombine; C5 (main/package rewriting) accounts for 3: comparisons, issue60324, memops.

C9 (runtime type assertions) accounts for 1 root: ifaces; C6/C7 are on master but account for none of these 14.

Sprint: #152
Story: #77
Story-ID: 9a27ae296c91
