# Sprint 162 — lane `lower-bodyless` (S162.4 wave 1a, story #94)

Seam: `lower/**`. Authority: the exact upstream Go 1.27 harness. Every row
below is a Barrier B compiled row whose first line was
`LOWER-EUNSUPPORTED: function declaration without body`
(`barrier-b/active-retained-manifest.tsv` 39, `active-151-manifest.tsv` 2).

## Mechanism

A body-less function declaration is a compile-time fact of Go source: the
body is supplied by assembly, a `//go:linkname` or a `//go:wasmimport`, and
gc decides whether one is present. Upstream's `compile` / `errorcheck` /
`compiledir` actions invoke `go tool compile` without `-complete`, which
accepts the declaration; the generic form is a checker error (`generic
function is missing function body`) before lowering, and the Go-source
front end (go/types) already reports it. The emitter now passes a body-less
declaration through as written — receiver, name, type parameters, signature,
and the `//go:` directives the converter attached — instead of refusing it.
The runtime (execution) path still refuses, since it wraps a body it does
not have, and a Bash++ script cannot spell one (the parser demands the
body).

Reproducer: `lower/testdata/sprint162/bodyless/` — `asm-backed.go`
(body-less func + a caller), `method.go` (value and pointer receivers),
`directives.go` (`//go:noescape`, `//go:nosplit`, `//go:linkname`),
`generic.go` (negative: still diagnosed), `with-body.go` (fidelity control,
unchanged). Test: `lower/bodyless_test.go` — golden bytes, D1 identity,
textual + structural survival, the three negatives.

Also fixed on the way (same mechanism, runtime path): `inferChannelParameters`
and `functionResultPlan` walked a nil body and crashed with a nil-pointer
panic before the emitter could diagnose the declaration; both now skip a
body-less owner.

## Rows

Leaf evidence (the leaf host): `leaf-lower-bodyless-r3` — candidate
`lower-bodyless` (sh `4deba4ca`, the four commits of this lane on the
published bashy `548c3a4`), base harness `33ac42d` (which already carries
the backend lane's compile-only invocation, `18791c8`, and `847f826`),
57 roots = the 41 body-less roots + 3 native-PASS `compile` canaries
(bug087, issue17005, issue45258: PASS) + the 10 wave-2 rows + 3 native-PASS
`-m` canaries (escape_array, escape_closure, escape_map: compiled PASS,
interpreted zero-credit as in Barrier B). Manifests in
`leaf-lower-bodyless-r3/manifests/`, status in `logs/status.txt`, errorcheck
output in `evidence/evidence-compiled/testdir.go-test.json`. `r4` is the
same subset on the backend lane's own bundle (`18791c8`, older than base):
identical rows plus issue9608 compiled, fixed in base by `847f826`.

Before the emitter half (Barrier B): 0 of 41 compiled. On r3: **25 of 41
PASS in both modes; 7 more PASS compiled** with the interpreted row the
zero-credit `-m` / `-live` / `-d=` compiler-artifact one (as for the `-m`
canaries); **2 PASS compiled** with an interpreted `compilation succeeded
unexpectedly` (issue20780, issue48097 expect gc's own `-complete` /
`missing function body` diagnostics, which go/types cannot express — the
S162.1 D5 family); **7 still FAIL compiled** for three causes recorded
below: the synthesised main (issue4099, inlinegcpc, linkname,
live_uintptrkeepalive — 4, closed by the bashy request), a `//go:noescape`
after a blank line (escape2, escape2n — 2, the gosource request), a
comment-split operand (nilptr3 — 1, C1). Status column = the r3 verdict.

| root | recipe | first cause | mechanism | status |
|---|---|---|---|---|
| testdir:abi/result_live.go | errorcheck -0 -live | body-less func | pass-through | compiled PASS; interpreted zero-credit (`-m`/`-live`/`-d=` recipe) |
| testdir:escape2.go | errorcheck -0 -m -l | body-less func | pass-through | compiled FAIL: `//go:noescape` separated from its func by a blank line is dropped (gosource request); interpreted zero-credit (`-m`/`-live`/`-d=` recipe) |
| testdir:escape2n.go | errorcheck -0 -N -m -l | body-less func | pass-through | compiled FAIL: `//go:noescape` separated from its func by a blank line is dropped (gosource request); interpreted zero-credit (`-m`/`-live`/`-d=` recipe) |
| testdir:escape_calls.go | errorcheck -0 -m -l | body-less func | pass-through | compiled PASS; interpreted zero-credit (`-m`/`-live`/`-d=` recipe) |
| testdir:fixedbugs/bug089.go | compile | body-less func | pass-through | PASS (both modes, leaf r3) |
| testdir:fixedbugs/bug150.go | compile | body-less func | pass-through | PASS (both modes, leaf r3) |
| testdir:fixedbugs/bug245.go | compile | body-less func | pass-through | PASS (both modes, leaf r3) |
| testdir:fixedbugs/bug392.go | compiledir | body-less func (pkg2.go) | pass-through | PASS (both modes, leaf r3) |
| testdir:fixedbugs/bug443.go | compile | body-less func | pass-through | PASS (both modes, leaf r3) |
| testdir:fixedbugs/issue11699.go | compile | body-less func | pass-through | PASS (both modes, leaf r3) |
| testdir:fixedbugs/issue13587.go | errorcheck -0 -l -d=wb | body-less func | pass-through | compiled PASS; interpreted zero-credit (`-m`/`-live`/`-d=` recipe) |
| testdir:fixedbugs/issue17596.go | compile | body-less func | pass-through | PASS (both modes, leaf r3) |
| testdir:fixedbugs/issue19168.go | errorcheck -0 -l -d=wb | body-less func | pass-through | compiled PASS; interpreted zero-credit (`-m`/`-live`/`-d=` recipe) |
| testdir:fixedbugs/issue20780.go | errorcheck | body-less func | pass-through | compiled PASS; interpreted FAIL: `compilation succeeded unexpectedly` (go/types has no `-complete`; S162.1 D5 family) |
| testdir:fixedbugs/issue23311.go | compiledir | body-less linkname func (main.go) | pass-through | PASS (both modes, leaf r3) |
| testdir:fixedbugs/issue28430.go | compile | body-less func | pass-through | PASS (both modes, leaf r3) |
| testdir:fixedbugs/issue30061.go | compile | body-less linkname func | pass-through | PASS (both modes, leaf r3) |
| testdir:fixedbugs/issue31010.go | compile | body-less func | pass-through | PASS (both modes, leaf r3) |
| testdir:fixedbugs/issue31915.go | compile -d=ssa/check/on | body-less func | pass-through | PASS (both modes, leaf r3) |
| testdir:fixedbugs/issue4099.go | errorcheck -0 -m | body-less func | pass-through | compiled FAIL: synthesised `main` → `can inline main` unmatched (class A; needs the bashy `Options.Package` request); interpreted zero-credit (`-m`/`-live`/`-d=` recipe) |
| testdir:fixedbugs/issue42944.go | errorcheck -0 -live | body-less func | pass-through | compiled PASS; interpreted zero-credit (`-m`/`-live`/`-d=` recipe) |
| testdir:fixedbugs/issue43479.go | compiledir | body-less func (a.go) | pass-through | PASS (both modes, leaf r3) |
| testdir:fixedbugs/issue43677.go | compile | body-less func | pass-through | PASS (both modes, leaf r3) |
| testdir:fixedbugs/issue45323.go | compile | body-less func | pass-through | PASS (both modes, leaf r3) |
| testdir:fixedbugs/issue45344.go | compile | body-less func | pass-through | PASS (both modes, leaf r3) |
| testdir:fixedbugs/issue48097.go | errorcheck -complete | body-less func | pass-through | compiled PASS; interpreted FAIL: `compilation succeeded unexpectedly` (go/types has no `-complete`; S162.1 D5 family) |
| testdir:fixedbugs/issue53454.go | compile | body-less func | pass-through | PASS (both modes, leaf r3) |
| testdir:fixedbugs/issue58826.go | compile -dynlink | body-less func | pass-through | PASS (both modes, leaf r3) |
| testdir:fixedbugs/issue74836.go | compile | body-less func | pass-through | PASS (both modes, leaf r3) |
| testdir:fixedbugs/issue9608.go | rundir | body-less func (issue9608.go) | pass-through | PASS (both modes, leaf r3) |
| testdir:func2.go | compile | body-less func | pass-through | PASS (both modes, leaf r3) |
| testdir:import.go | compile | body-less func | pass-through | PASS (both modes, leaf r3) |
| testdir:import2.go | compiledir | body-less func (import2.go) | pass-through | PASS (both modes, leaf r3) |
| testdir:internal/runtime/sys/inlinegcpc.go | errorcheck -0 -+ -p=internal/runtime/sys -m | body-less func | pass-through | compiled FAIL: synthesised `main` → `can inline main` unmatched (class A; needs the bashy `Options.Package` request); interpreted zero-credit (`-m`/`-live`/`-d=` recipe) |
| testdir:linkname.go | errorcheckandrundir -0 -m -l=4 | body-less linkname func (linkname2.go) | pass-through | compiled FAIL: synthesised `main` → `can inline main` unmatched (class A; needs the bashy `Options.Package` request); interpreted FAIL: `missing error` on a `-m` recipe (class=missing partition check, zero-credit by recipe-flag rule) |
| testdir:live1.go | compile | body-less func | pass-through | PASS (both modes, leaf r3) |
| testdir:live2.go | errorcheck -0 -live -wb=0 | body-less func | pass-through | compiled PASS; interpreted zero-credit (`-m`/`-live`/`-d=` recipe) |
| testdir:live_uintptrkeepalive.go | errorcheck -0 -m -live -std | body-less func | pass-through | compiled FAIL: synthesised `main` → `can inline main` unmatched (class A; needs the bashy `Options.Package` request); interpreted zero-credit (`-m`/`-live`/`-d=` recipe) |
| testdir:nilcheck.go | errorcheck -0 -N -d=nil | body-less func | pass-through | compiled PASS; interpreted zero-credit (`-m`/`-live`/`-d=` recipe) |
| testdir:nilptr3.go | errorcheck -0 -d=nil | body-less func | pass-through | compiled FAIL: `return *\n/* */\n*p` joined by gofmt (C1 residue, S162.5 row); interpreted zero-credit (`-m`/`-live`/`-d=` recipe) |
| testdir:parentype.go | compile | body-less func | pass-through | PASS (both modes, leaf r3) |

In-process check (darwin, `gosource.Load` + `lower.Compile` on each root's
named file): 40/41 lower without a diagnostic; `bug392.dir/pkg2.go` imports
its sibling `./one`, which needs the package map the backend passes (not a
lowering matter).

## Wave 2 — compiled `-m` / escape fidelity (27 rows)

Method: for each single-file root, lower in-process, then diff gc's notes
(`go build -gcflags=<recipe flags>` of a scratch module, go 1.27) on the
original against the generated file, keyed by line (columns are not part of
upstream errorcheck's match — it keys on `file:line` and the message regexp).
Every remaining diff was one of four classes, three of them emitter
mechanisms fixed here and one a design row:

| class | mechanism | commit | rows it closes (emitter side) |
|---|---|---|---|
| grouped `var (` / `type (` spec marked at the group line | mark the spec at its own line | `0c04ca2c` | escape_reflect (4 missing) |
| multi-line composite literal / struct type / call argument list collapsed onto one line | line-faithful layout (`goBracketed`) | `ea7e0c1f` | escape_slice (3), issue21709 (2), devirtualization (2) |
| `func main() {}` synthesised for a non-main Go package (`can inline main` unmatched) | no synthesised main under `Options.Package != "main"` | `4deba4ca` | devirtualization (the extra); needs the caller to pass the package (see requests) |
| `__gosource_pkg_N_` prefix on names of an imported test package (`errorcheckdir` roots: issue18895, issue19261, issue37837, issue42284, issue56280) | gosource's package flattening (`gosource/convert.go` `mangledName`) qualifies every linked package's names because the compiled module is one package; a single-package rule cannot keep them — the name is right only if the generated module has one package per input package | — | design row: S162.2 / D1 (multi-package module layout), not a lowering rule |

After the three commits, gc's line-keyed `-m` notes on the generated Go are
identical to the original's for escape_reflect.go, escape_slice.go,
fixedbugs/issue21709.go, escape_mutations.go (all four PASS compiled on
leaf r3), and identical for devirtualization.go once the package clause is
the source's (r3: `extra=1`, the synthesised main's `can inline main`). The
five `errorcheckdir` roots no longer show a mangled name on r3 (the harness
now compiles each directory package on its own, `847f826`); what remains on
them is the same synthesised main (`p.go:25:6: can inline main`).

### Wave-2 rows on leaf r3

| root | status |
|---|---|
| testdir:escape_reflect.go | compiled PASS; interpreted zero-credit (`-m`/`-live`/`-d=` recipe) |
| testdir:escape_slice.go | compiled PASS; interpreted zero-credit (`-m`/`-live`/`-d=` recipe) |
| testdir:fixedbugs/issue21709.go | compiled PASS; interpreted zero-credit (`-m`/`-live`/`-d=` recipe) |
| testdir:devirtualization.go | compiled FAIL: synthesised `main` → `can inline main` unmatched (class A; needs the bashy `Options.Package` request); interpreted zero-credit (`-m`/`-live`/`-d=` recipe) |
| testdir:escape_mutations.go | compiled PASS; interpreted zero-credit (`-m`/`-live`/`-d=` recipe) |
| testdir:fixedbugs/issue18895.go | compiled FAIL: synthesised `main` → `can inline main` unmatched (class A; needs the bashy `Options.Package` request); interpreted FAIL: `missing error` on a `-m` recipe (class=missing partition check, zero-credit by recipe-flag rule) |
| testdir:fixedbugs/issue19261.go | compiled FAIL: synthesised `main` → `can inline main` unmatched (class A; needs the bashy `Options.Package` request); interpreted FAIL: `missing error` on a `-m` recipe (class=missing partition check, zero-credit by recipe-flag rule) |
| testdir:fixedbugs/issue37837.go | compiled FAIL: synthesised `main` → `can inline main` unmatched (class A; needs the bashy `Options.Package` request); interpreted FAIL: `missing error` on a `-m` recipe (class=missing partition check, zero-credit by recipe-flag rule) |
| testdir:fixedbugs/issue42284.go | compiled FAIL: synthesised `main` → `can inline main` unmatched (class A; needs the bashy `Options.Package` request); interpreted FAIL: `missing error` on a `-m` recipe (class=missing partition check, zero-credit by recipe-flag rule) |
| testdir:fixedbugs/issue56280.go | compiled FAIL: synthesised `main` → `can inline main` unmatched (class A; needs the bashy `Options.Package` request); interpreted FAIL: `missing error` on a `-m` recipe (class=missing partition check, zero-credit by recipe-flag rule) |

The other 20 retained-compiled rows in this family are not lowering rows.
Their recipes carry a `-d=` debug flag (`-d=ssa/prove/debug=1`,
`-d=wb`, `-d=nil`, `-d=defer`, `-d=typeassert`, `-d=tailcall=1`,
`-d=inlfuncswithclosures=1` on closure3, `-d=escapemutationscalls,zerocopy`
on escape_mutations, …) and the compiled backend retains `-d=` for
errorcheck "as evidence only" instead of passing it to gc
(`bashpp_backend_test.go`, the compile-phase branch: `-p=`, `-e`, `-C`,
`-d=` are dropped from `-gcflags`). Every expectation then goes missing
(`class=missing`, e.g. prove.go missing=474, loopbce missing=122). First
cause: backend seam. The compile-only invocation of S162.4b (gc invoked
directly with the recipe flags, as upstream does) is the fix; recorded here
so the rows are not attributed to the emitter. The same applies to the
body-less roots whose recipe has `-d=` (nilcheck, nilptr3, live2,
issue13587, issue19168, …): after the pass-through they land in this bucket
until the backend half lands.

## Requests to other seams

* gosource (`gosource/directives.go`, `attachEmbedDirectives`): a `//go:`
  directive separated from its declaration by a blank line is not in the
  declaration's `Doc` group (go/ast attaches only the adjacent group), so
  the converter drops it, while gc's pragma handling applies it to the next
  declaration regardless (escape2.go:1352 `//go:noescape`, blank line,
  `func F1([]byte)` — the test's own "annotations take effect regardless of
  where they are" case). Leaf r3: escape2 / escape2n compiled
  `wording=1;extra=5` (`moved to heap: buf1/buf3/x/y`, all callers of the
  four blank-line-separated `//go:noescape` funcs). Suggested diff — attach
  the directive lines of every comment group that lies after the previous
  top-level declaration's end and before this one's start, not only `Doc`:

  ```go
  // in attachEmbedDirectives, for *ast.FuncDecl (and the var case alike):
  //   attach(d.Name, d.Doc)
  // becomes
  //   for _, group := range groupsBetween(f, prevEnd, d.Pos()) { attach(d.Name, group) }
  // where groupsBetween returns f.Comments whose Pos() > prevEnd and End() < d.Pos(),
  // and prevEnd is the End() of the preceding declaration (f.Package for the first).
  ```

* bashy (`internal/agentos/transpile.go`, outside this sprint's `sh` seams —
  manager's call): `transpile --source=go` lowers every Go package as
  `package main` because it never sets `lower.Options.Package`. The front
  end reports the source's package (`GoSourceProgram.Package`); passing it
  through — `opts.Package = goProg.Package` next to the existing
  `opts.Importer = goProg.Importer` — makes a non-main input lower to its own
  package with no synthesised main (commit `4deba4ca` is the lower half).
  Without it the generated module is still `package main` + `func main() {}`
  and gc's `can inline main` stays an unmatched note on every non-main
  `errorcheck -0 -m` root. (`go build -o <artifact> .` of a non-main package
  writes the archive, so the backend's build step is unaffected.)
* backend lane (`bashpp-tests/tools/upstream-harness/testdata/backend/`,
  S162.4b): compile-only recipes (`compile`, `errorcheck`, `compiledir`)
  must invoke the pinned toolchain the way upstream's `compile` action does
  — `go tool compile` without `-complete` — on the generated module.
  cmd/go's `go build` adds `-complete` to any package with no non-Go files,
  and gc then reports `<file>:<line>:<col>: missing function body` for
  every body-less declaration (linknamed ones exempt), on the generated
  module exactly as on the input. Until that lands every row above stays
  FAIL with that new first line, which is this lane's evidence that the
  emitter half is done.

## Not this lane / not this wave (recorded, not touched)

* An untyped constant argument to a parameter of a sized type is retyped by
  the emitter (`Store(&x, 1)` → `Store(&x, uint64(1))`,
  `reflect.TypeOf(int(0))` → `reflect.TypeOf(int(int(0)))`, `IPv4(127, 0,
  0, 1)` → `IPv4(byte(127), …)`), independent of whether the callee has a
  body. gc's `-m` message text is unchanged by it in every row measured
  (constants fold), so no row is keyed to it today; a D1 fidelity class
  nonetheless (the Sprint 152 `untyped-constants` fixture covers a narrower
  shape).
* A grouped `var (` / `type (` declaration is emitted split into single
  declarations, and a `const (` group's `iota` is spelled out (`A = 0`,
  `B = 1`): lines are right after `0c04ca2c`, the text is not the input's.
  D1 class, no `-m` row keyed to it.
* nilptr3.go:248–250 (`return *` / `/* */` / `*p`, issue 42673): the
  operand is split from its operator by a comment line; gofmt itself joins
  `*\n\n*p` to `**p`, so the emitter's gofmt step cannot keep the second
  `removed nil check` on line 250 without retaining the comment (C1). Leaf
  r3: `missing=1`. Recorded as an S162.5 position row.
* Comments are dropped (Sprint 152 C1, still open by design): a dropped
  comment line inside a multi-line construct becomes a blank line, and two
  or more become one after gofmt — the only remaining way an inner line can
  drift under `ea7e0c1f`.
