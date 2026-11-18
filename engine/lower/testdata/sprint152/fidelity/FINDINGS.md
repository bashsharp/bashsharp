# Spike F — non-identity rewrites the Bash++ lowering applies to plain-Go input

Sprint 152 decision D1: a Go-only input must lower to itself — generated Go ==
original modulo the package clause, `//line` directives and the linked package
map. This spike measures the distance from that today. Findings only; no file
under `lower/` or `gosource/` was changed.

Tooling: `bashy.real` build `963ef4b` (`-dirty`), corpus
`$HOME/projects/poc/go/test` (Go 1.27), invoked from the corpus dir as
`bashy.real transpile --bashpp --source=go --go-file <root> -o <out> --map <out>.map`.
Diffs were taken with `gofmt` on both sides after dropping `//line` and
`// lower:N` lines. The eight roots:

| root | mode | diagnostic that fails today |
|---|---|---|
| codegen/append.go | asmcheck | opcode not found |
| codegen/issue58166.go | asmcheck | opcode not found |
| codegen/strings.go | asmcheck | opcode not found |
| codegen/issue60324.go | asmcheck | opcode not found |
| codegen/ifaces.go | asmcheck | opcode not found |
| escape_param.go | errorcheck -0 -m -l | gc `-m` rows no longer match |
| escape_iface.go | errorcheck -0 -m -l | gc `-m` rows no longer match |
| fixedbugs/issue24651b.go | errorcheck -0 -m -m | gc `-m` rows no longer match |

Evidence beyond the textual diff (scratch module, not the corpus harness):

* `go build -gcflags=-S` (amd64 and arm64) of original vs generated
  `issue58166.go`: the per-line opcode histogram is **identical**. The
  `// amd64:"INCQ"` row does not fail because of codegen drift — see H1.
* `go build -gcflags='-m -l'` on original vs generated, keyed by
  (origin line, message): escape_param 94/104 rows identical, 10 differ;
  escape_iface 44/46 identical, 2 differ + 29 new unmatched rows;
  issue24651b 4/5 identical, 1 lost + 5 new. Every differing row is attributed
  to one class below (C5, C7, C9, C10).
* `ifaces.go:26` on amd64: original emits `CALL runtime.typeAssert`, generated
  emits `CALL shellrt.AssertionPossible[...]` + `CALL shellrt.Assert[...]` (C9).

## H1 — harness-facing, not a rewrite: `//line` positions are unmatchable by asmcheck

Not a rewrite class (D1 allows `//line`), but it decides whether removing any
class below can flip an asmcheck row, so it is recorded first.

* Emitter: `lower/gosource.go:147` (`goSourcePositions`) writes
  `//line <origin>:<line>:<col>` with `<origin>` exactly as passed on the
  command line (`codegen/append.go`, relative), from `lower/compile.go:166-169`.
* Upstream `cmd/internal/testdir` keys assembly on the **absolute** file path
  (`long := filepath.Join(gorootTestDir, goFileName)` → `wantedAsmOpcodes(long)`),
  and its `rxLine` is `\((<fn>:\d+)\)\s+(.*)` — the line number must be followed
  by `)` immediately.
* With `//line` present, `-S` prints positions as
  `(codegen/issue58166.go:17[/abs/phys/main.go:30])`. Both the relative
  spelling and the `[phys]` suffix defeat `rxLine`, so **every** check reports
  `opcode not found` regardless of codegen (confirmed identical opcodes for
  issue58166). If bashy's compiled-mode harness reuses that regex verbatim,
  all five asmcheck roots fail here before any class below matters. Fix is on
  the harness/CLI contract (spell `//line` with the path the harness keys on,
  and match `file:line` with an optional `[...]` suffix) — 1-day.

## 1. Rewrite classes

Sites are `file:line` in this checkout (`lower/` and `gosource/`). "Roots"
lists which of the eight show the class. "Breaks" names the asmcheck pattern
or `-m` diagnostic it plausibly (P) or confirmedly (C, from the scratch builds
above) breaks. Estimate: **1-day** = mechanical, gate on `e.goSource`;
**design** = the rewrite is a contract of the Bash++ runtime path and needs a
decision on how pure-Go mode diverges.

| # | class | example (original → generated) | emitter site | roots | breaks | estimate |
|---|---|---|---|---|---|---|
| C1 | **All comments dropped** — doc, inline, asmcheck `// amd64:"…"`, errorcheck `// ERROR "…"`, `//go:noinline`, `//go:norace`, `// asmcheck` header | `//go:noinline`<br>`func main() { // ERROR "cannot inline main…"` → `func __bpp0_sourceMain() {` | `gosource/convert.go:485` (`function()` builds `BashPPFuncDecl` without `Doc`), `:989` (`statements()` never reads `ast.CommentGroup`); `gosource/source.go:143` parses with `ParseComments` but `:380-470` only *validates* directives; `gosource/directives.go:13-44` + `lower/callables.go:199-206` re-attach **only** `//go:embed` on package vars — the one comment path that exists; header `lower/compile.go:339` | all 8 | **C** issue24651b:21 `cannot inline main: marked go:noinline` lost (becomes `can inline … cost 34`). **P** if bashy's harness reads asmcheck/ERROR patterns from the generated file (as upstream `wantedAsmOpcodes(fn)` does from the file it compiles) there are zero checks left | directives on func/var decls: **1-day** (ride the `go:embed` path in `directives.go`/`callables.go:199`); free-floating comments: **design** (the Bash++ AST carries `Stmt.Comments`, but positions are re-flowed per statement) |
| C2 | **Sink statements** `_ = x` after `:=` and `var` (and type-switch bindings) | `i := 0` → `i := int(0)`<br>`_ = i`; `var r []int` → `var r []int`<br>`_ = r` | `lower/compile.go:719-726` (`unused()`), `:973-977` (var decl; note `:974` already gates it off for `const` under `goSource`), `:1073` (short decl); `lower/callables.go:733` (type switch) | append, issue58166, escape_param, escape_iface, (all reproducers) | none observed: opcodes identical, `-m` rows identical. Pure identity violation | **1-day** |
| C3 | **Untyped constants materialised** via the type-checker's contextual type | `i := 0` → `i := int(0)`; `tmp != 0` → `tmp != float64(0)`; `println("Foo(", x)` → `println(string("Foo("), x)`; `[]byte("foo")` → `([]byte)(string("foo"))`; `[]int{3,4,5}` → `[]int{int(3), int(4), int(5)}`; `var x = 5` → `var x int = int(5)` | `gosource/convert.go:638-652` (`expr()` wraps every constant whose recorded type is typed in `BashPPConvertExpr`); spelled by `lower/compile.go:1393-1406` | all except ifaces (7) | none observed in `-m` (gc reprints constants; only columns shift, e.g. issue24651b `22:30` → `22:47`) or `-S`. **P** any asmcheck pattern that greps a constant spelling in `-S` for `string(...)`-wrapped operands (none in these 5) | **1-day** if gated to the cases the runtime needs (float/rune defaulting into `any`, per the comment at `:643-645`); otherwise design |
| C4 | **Expression re-parenthesisation** — every binary/unary/paren/conversion/call-callee is wrapped | `i*ldc+n` → `((i * ldc) + n)`; `[]rune(s)` → `([]rune)(s)`; `f(1)()` → `(f(int(1)))()`; `s == s` → `(s == s)`; `x*(x+1)*(x+2)` → `((x * (x + int(1))) * (x + int(2)))` | `lower/compile.go:1380-1392` (paren/unary/binary), `:1393-1400` (conversion `(T)(x)`), `:1428`, `:1473` (callee `(f)(…)`) | all 8 | none: `-m` reprints (`can inline Foo … as: func(int) int { return x * (x + 1) * (x + 2) }` matches). Identity only | **1-day** (precedence-aware printing, or under `goSource` emit the source text the converter already keeps in `Word`/`Rhs` — `gosource/convert.go:215-226`) |
| C5 | **`func main` renamed + synthetic `main`** (`__bppN_sourceMain`, wrapper `main` that calls it; wrapper emitted even when the source has no `main`; package clause forced to `main`) | `//go:noinline`<br>`func main() {…}` → `func __bpp0_sourceMain() {…}`<br>`func main() {`<br>`//line f.go:10:1`<br>`__bpp0_sourceMain() }` | rename `lower/callables.go:13-18` (`goName`), `lower/callables_runtime.go:181-182`; wrapper `lower/compile.go:378`; synthetic call statement with **bogus position** `p.File.Pos()` (= first declaration) `gosource/source.go:319-325`; package name `lower/compile.go:154-156`, `:339` | rename: issue24651b, issue60324 (+ every reproducer); wrapper: all 8 | **C** issue24651b: `10: inlining call to main` / `inlining call to Foo` / `inlining call to Bar` land on line 10 (Foo's line, via the bogus `//line …:10:1`), `25: can inline main with cost 36 as: func() { main() }` is past EOF — both are *Unmatched Errors* for errorcheck. **P** asmcheck: `command-line-arguments\.h\.func1` in issue60324 survives, but the wrapper's `//line` continuation attributes wrapper code to real origin lines | rename + skip-wrapper-when-source-has-main: **1-day**; wrapper for non-`main` packages (asmcheck roots are `package codegen` with no `main`): **design** — either emit the original package clause (needs `go tool compile`, not `go build`, in the harness) or accept the extra `func main() {}` |
| C6 | **Guard prologue + runtime imports** — any `*p` or `x.f` in the file sets `e.guarded`, which imports `__bppN_fmt "fmt"`, `__bppN_rt ".../shellrt"`, `__bppN_os "os"` and wraps `main` in a `defer func(){ recover … GuardError … os.Exit }()`. Under `goSource` the deref path never calls the runtime (C7), so for deref/selector-only files the guard and all three imports are dead | `func F(t *T) int { return *t.p }` → three imports + 9-line `defer` in `main` | trigger `lower/callables_checked.go:9-22` (`findCheckedValues`: `BashPPDerefExpr`/`BashPPSelectorExpr` ⇒ guarded ⇒ bridge+output); body `:31-33` (`guardBoundary`), placed by `lower/compile.go:370-378`; imports `lower/compile.go:322-335`, `:343-356` | append, ifaces, escape_param, escape_iface | **C** escape_param `443: func literal does not escape`, `446: ... argument does not escape`; escape_iface `267`, `270` (same two) — attributed to origin-file lines past EOF by `//line` continuation ⇒ *Unmatched Errors*. **P** asmcheck: extra `CALL` rows in `main` only | **1-day**: under `goSource`, set `guarded` only for `BashPPTypeAssertExpr` (the sole rewrite that still calls `shellrt`); imports then follow |
| C7 | **Explicit (auto-)dereference and selector parens** | `*p = r` → `(*(p)) = r`; `box.pair.p1` → `(*((*(box)).pair)).p1`; `v.p` → `(v).p`; `**i` → `(*(*(i)))` | `lower/checked_values.go:23-29` (`checkedDeref`, `goSource` branch returns `(*(p))`); `lower/compile.go:1357-1362` (`BashPPDerefExpr`); `lower/callables_presence.go:97-101` (selector: explicit deref when the projection is a pointer, then `(base).sel`) | append, escape_param, escape_iface | **C** 10 escape_param rows: `ignoring self-assignment in box.pair.p1 = box.pair.p2` at 64, 90, 95, 100, 105, 110, 127, 131, 141 (gc now prints `(*(*box).pair).p1`, and the `// ERROR` regexes spell the source form) and `405: *(*v.p) escapes to heap` (now `*(*(*v).p)`) | **1-day** (under `goSource` emit `*x` and `x.f` verbatim; the pointer projection is only needed by the runtime path) |
| C8 | **Import binding aliasing** — every import gets a hygiene alias (`__gosource_import_<file>_<n>`) and each use is rewritten through it | `import "strings"` … `strings.HasPrefix(s, "str")` → `import __gosource_import_0_0 "strings"` … `__gosource_import_0_0.HasPrefix(s, string("str"))` | rename `gosource/source.go:262-281`; spelled by `gosource/convert.go:521-536` (`importSpec`) and `:102-111` (`ident`); printed `lower/callables.go:495-506` (`importLines`, one `import` line per alias, no group) | strings | none in these roots (`-memequal` patterns don't name the package). **P** any asmcheck pattern naming an imported symbol (`strings\.HasPrefix`, `CALL …/pkg\.Fn`) still matches, since the alias does not change link names | **1-day** (alias only on collision; keep `import (...)` grouping and order) |
| C9 | **Type assertions routed through the runtime** (`x.(T)` → `__bppN_rt.MustValue(__bppN_rt.Assert[T](any(x), Assertion{…Site…}))`, comma-ok → `MustAssertOK(AssertOK[T](…))`), plus **tuple-assign splitting** for non-identifier targets (`__gosource_tuple_<pos>_<i>` temporaries then per-target assigns) | `v1 := x.(M0)` → 1-line runtime call with source site; `sink, *(&ok) = y.(int)` → `t0, t1 := MustAssertOK(...)`<br>`sink = t0`<br>`(*(&ok)) = t1` | `lower/checked_values.go:34-49` (`checkedAssertion`), `lower/callables_checked.go:36-52` (`valueAssertion`), `lower/checked_values.go:10-20` (site metadata); tuple split `gosource/tuple.go:87-99` | ifaces, escape_iface | **C** ifaces.go:26 `CALL runtime.typeAssert` gone (now `shellrt.AssertionPossible`/`shellrt.Assert`), also the `MOVL 16\(.*\)`/`MOVQ 8\(.*\)(.*\*1)` rows. **C** escape_iface `244: x.(int) escapes to heap` → `shellrt.MustValue[go.shape.int](…) escapes to heap`; `245: .autotmp_13 escapes to heap` → `__gosource_tuple_4219_0 escapes to heap`; plus `shellrt.ValueAbort{...}`/`ValueError{...}` rows in shellrt's own files (filtered by errorcheck's file prefix, harmless) | **design** — the checked assertion is the runtime's diagnostic contract (`Impossible`, `ValueSite`); pure-Go mode must decide to emit plain `x.(T)` and a plain tuple assignment |
| C10 | **Synthetic receiver/parameter names** — unnamed and `_` receivers/params become `__bppN_unnamed<i>` | `func (M1) M() {}` → `func (__bpp0_unnamed0 M1) M() {`<br>`}`; `func (_ *M1) M(_ int)` → `func (__bpp0_unnamed0 *M1) M(__bpp0_unnamed1 int)` | `lower/compile.go:635-643` (`syntheticName`), `:759-764` (receiver), `:782-790` (params) | escape_iface | **C** escape_iface `29`, `86`, `141: unnamed0 does not escape` — three new *Unmatched Errors* (gc reports nothing for an unnamed receiver) | **1-day** (only name what the runtime path has to reference) |
| C11 | **Declaration reordering, inferred type injection, `any` spelling, layout** — package-level `var`s hoisted first (zero-init then `InitOrder`), then all `type`/`const`, then funcs; `var x = e` gets its inferred type; `any` → `interface{}` (incl. `[T any]`); one-line bodies/struct types re-flowed | `var Fi = F[I]` (declared after `F`, before `type I`) → `var Fi func(x I) I = F[I]` first in file; `func F[T any](x T) T` → `func F[T interface{}](x T) T`; `func (t *T) M() {}` → `func (t *T) M() {`<br>`}`; `type Node struct {`<br>`p *Node`<br>`}` → `type Node struct{ p *Node }` | buckets `gosource/source.go:494-590` (`lowerPackage`: imports/decls/inits/funcs), assembly order `:306-313`; `lower/compile.go:365-366` writes `globalDecls` (vars, via `lower/callables.go:199-207`) before `declarations` (types+funcs); inferred type `gosource/convert.go:571-590` (`valueDecl`, `types.TypeString`); `any` `gosource/convert.go:368-370`, `:471-484` (`typeParams`); layout `lower/compile.go:830` (`" {\n" + body + "}\n"`), `lower/types.go:336` (`typeDecl`) | ifaces, escape_param, escape_iface, issue24651b, strings (`var bsink` hoisted) | none observed (`//line` keeps every row on its origin line; `-m` output identical for these). Identity only. **P** `any`→`interface{}` changes nothing gc prints (`interface {}` either way) | ordering under `goSource`: **1-day** (gc orders init itself; the bucket order exists for the interpreter's forward-reference rule, `source.go:306-309`); inferred type + `any`: **1-day**; layout: **1-day** with `go/format` on a rebuilt `ast`, else cosmetic |

Reproducers (5–15 lines each, transpiled with the same build from a scratch
copy; `<class>.generated.go` is the verbatim output including `//line` and
`// lower:` markers):

| class | reproducer |
|---|---|
| C1 | `comments-dropped.go` → `comments-dropped.generated.go` |
| C2 | `sink-statements.go` → `sink-statements.generated.go` |
| C3 | `untyped-constants.go` → `untyped-constants.generated.go` |
| C4 | `reparenthesised-exprs.go` → `reparenthesised-exprs.generated.go` |
| C5 | `main-rename.go` → `main-rename.generated.go` (also shows the bogus `//line …:6:1` on the synthetic call and `var x int = int(5)`) |
| C6 | `guard-prologue.go` → `guard-prologue.generated.go` (a single `*t.p` pulls in three imports and the `defer`) |
| C7 | `explicit-deref.go` → `explicit-deref.generated.go` |
| C8 | `import-aliasing.go` → `import-aliasing.generated.go` |
| C9 | `type-assertion.go` → `type-assertion.generated.go` (value and comma-ok forms, tuple split) |
| C10 | `synthetic-receiver-names.go` → `synthetic-receiver-names.generated.go` |
| C11 | `decl-reordering.go` → `decl-reordering.generated.go` |

Every reproducer also exhibits C2/C5 because they are unconditional.

The `.generated.go` files are the transpiler output passed through
`gofmt -s -w` so the repo's `gofmt -s -d .` CI gate stays clean. gofmt's only
change is inserting a bare `//` line between each `// lower:N` marker and the
`//line` directive that follows it (Go 1.19+ doc-comment normalisation). That
is a fidelity fact in its own right: the emitter's marker/directive pairing
(`lower/gosource.go:147` writes the directive directly under the marker from
`lower/compile.go:588-596`) is not gofmt-stable, so "generated == gofmt(generated)"
already fails on every root before any class in the table. 1-day (emit the
separator, or attach markers after the directive).

## 2. Per-root summary

| root | classes present | what fails today (best evidence) |
|---|---|---|
| codegen/append.go | C1 C2 C3 C4 C5 C6 C7 C11 | H1 (all rows); no codegen drift expected from C2/C3/C4/C7 |
| codegen/issue58166.go | C1 C2 C3 C4 C5 | H1 only — opcodes confirmed identical by line |
| codegen/strings.go | C1 C3 C4 C5 C8 C11 | H1; `-memequal`/`countrunes` rows should hold once H1 is fixed |
| codegen/issue60324.go | C1 C3 C4 C5 | H1; `h.func1` pattern is in `__bpp0_sourceMain` but line-attributed correctly |
| codegen/ifaces.go | C1 C4 C5 C6 C9 C11 | H1, then C9 (line 26 `runtime.typeAssert` genuinely gone) |
| escape_param.go | C1 C2 C3 C4 C5 C6 C7 C11 | C7 (10 rows) + C6 (2 unmatched rows) |
| escape_iface.go | C1 C2 C3 C4 C5 C6 C9 C10 C11 | C9 (2 rows) + C10 (3 unmatched) + C6 (2 unmatched) |
| fixedbugs/issue24651b.go | C1 C3 C4 C5 C11 | C1 (`go:noinline` lost, row 21) + C5 (3 bogus rows at line 10, 1 past EOF) |

## 3. Recommendation — order of removal

Ordered by rows flipped per commit, one class per commit as D1 asks.

1. **H1 first** (harness/CLI, not the emitter): spell `//line` with the path
   the harness keys on and tolerate `file:line[phys]` in the asm matcher.
   Until this lands, none of the five asmcheck roots can pass no matter what
   the emitter does — issue58166 already has byte-identical opcodes per line.
   Expected: issue58166, strings, issue60324, most of append flip.
2. **C6 guard prologue + imports** (1-day): gate `guarded` on type
   assertions only under `goSource`. Flips 2 unmatched rows in each of
   escape_param and escape_iface and removes three imports from four roots.
3. **C7 explicit deref / selector parens** (1-day): 10 escape_param rows.
   With 2+3 escape_param should pass outright (94 + 10 + 0 unmatched).
4. **C5 main rename + synthetic call position** (1-day part): stop
   renaming `main`, don't emit the wrapper when the source already has
   `main`, and never mark the synthetic call with a borrowed position.
   Flips issue24651b's 4 bogus rows; with C1's directive part it passes.
5. **C1 directives** (1-day part): carry `//go:*` directives on func and
   var decls through the existing `go:embed` path. Flips
   issue24651b:21. Free-floating comments (asmcheck/ERROR patterns) are a
   design item only if the harness reads patterns from the generated file.
6. **C10 synthetic receiver/param names** (1-day): 3 escape_iface rows.
7. **C9 checked type assertions + tuple split** (design): the last 2
   escape_iface rows and ifaces.go:26. Needs a decision on whether pure-Go
   mode keeps the runtime's assertion diagnostics at all.
8. **C2, C3, C4, C8, C11** (identity only, no observed row impact): sinks,
   constants, parens, import aliasing, ordering/inferred types/`any`/layout.
   Land in that order; C2 and C4 are the cheapest and remove the most
   textual noise, which makes the remaining diffs reviewable.

Sprint: #152
Story: #77
Story-ID: 9a27ae296c91
