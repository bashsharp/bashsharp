# Sprint 151 checker-verdict findings

Sprint: #151, Story: #76, Story-ID: 7f1711595732.

## Recipe split and method

`S151.5_checker_verdict.roots` has 247 roots. The 148 `testdir` roots split
by their first line into 95 `errorcheck`, 37 `run`, 6 `compile`, 5 `rundir`,
3 `compiledir`, 1 `errorcheckdir`, and 1 `errorcheckandrundir`. The other 99
roots are typechecker fixtures (48 fixtures each through
`cmd/compile/internal/types2` and `go/types`, plus 3 types2-local fixtures).

The audit ran the rebuilt Bashy front end with the Go 1.27 toolchain on PATH.
For `errorcheck` and typechecker inputs, diagnostics were compared at the
annotated line against the file's `ERROR`/`ERRORx` regular expressions;
tab-indented go/types continuation diagnostics were treated as notes, exactly
as `go/types/check_test.go` does. `run` and `compile` inputs were required to
pass `--check`; a recipe-named companion such as `cmplxdivide1.go` was loaded
with its root. The 43 single-file `run`/`compile` roots all check clean, and
`cmplxdivide.go` checks clean when its named companion is included. Their
reported `_ redeclared`, `cannot use ... as ... value`, and BashPP evaluator
failures happen after source checking and are not checker verdicts.

## Fixed mechanisms

1. **Checker flags were discarded.** A leading `-lang=go1.N` now feeds one
   per-load checker configuration shared by the program and explicit mapped
   packages. An explicit `Options.GoVersion` wins. `FakeImportC` is carried by
   the same value, and tests prove neither setting leaks across loads. This
   repairs the direct verdict for:

   - `testdir:embedvers.go`
   - `testdir:fixedbugs/issue23609.go`
   - `testdir:fixedbugs/issue31747.go`
   - `testdir:fixedbugs/issue34329.go`
   - `testdir:fixedbugs/issue46525.go`
   - `testdir:fixedbugs/issue49368.go`
   - `testdir:fixedbugs/issue51531.go`
   - both typechecker variants of `TestCheck/decls0.go` and
     `TestCheck/go1_13.go`, and both variants of
     `TestFixedbugs/issue66285.go`

2. **go/types does not validate compiler pragmas.** A compiler-adjacent pass
   now rejects misplaced `go:build`/`go:noinline` directives and invalid
   `go:embed` placement, declaration shape, missing `embed` import, and use
   before Go 1.16. It runs for the program and mapped packages. This repairs:

   - `testdir:directive.go`
   - `testdir:directive2.go`
   - `testdir:embedfunc.go`
   - `testdir:fixedbugs/issue48230.go`

`types.Config.Error` was already installed, and `ErrorList` already retained
every callback. A regression now pins two independent semantic errors. The
default `IgnoreFuncBodies == false` already enables unused local/import,
function-body, and initialization-cycle checks. `FakeImportC` was previously
false (not leaking); it is now selectable and still isolated. No wrong-package
import was found: explicit packages win before the supplied module importer,
and resolution remains recorded.

## Initialization-cycle finding

There is no false-positive `InitOrder` path in this leaf. Every sampled
`initialization cycle` first line is required by an `ERROR` annotation,
including `bug13343`, `bug223`, `bug459`, `bug463`, `issue4847`, all
`issue6703a` through `issue6703z`, `initexp`, `initloop`, `typecheckloop`, and
types2-local `issue71254`. On an erroneous package, `Load` returns the
collected checker diagnostics before `converter.lowerPackage` can consume
`Info.InitOrder`. On a valid package, `Info.InitOrder` is the only ordering
source and no cycle diagnostic is synthesized by gosource.

## Remaining errorcheck roots: missing compiler-only checks

These 28 roots still produce no diagnostic. They require gc phases or
compiler policy not exposed by go/types; they are not hidden or accepted as
passing.

- Stack-frame sizing (`stack frame too large`):
  `testdir:fixedbugs/bug385_64.go`, `issue20529.go`, `issue20780.go`,
  `issue22200.go`, `issue22200b.go`, `issue25507.go`, `issue52697.go`.
- Anonymous-interface representation cycles (`invalid recursive type`):
  `testdir:fixedbugs/bug398.go`, `issue16369.go`, `issue56103.go`.
- Object/layout limits (`channel element type too large`, `map element type
  too large`, or `larger than address space`):
  `testdir:fixedbugs/issue20027.go`, `issue42058a.go`, `issue42058b.go`,
  `issue49767.go`, `issue49814.go`, `issue78355.go`.
- Escape, not-in-heap, and fieldtrack metadata unavailable to go/types:
  `testdir:fixedbugs/issue14999.go`, `issue63333.go`, `issue74626.go`,
  `notinheap.go`, and `testdir:typeparam/issue54765.go`.
- Remaining compiler directive/linker policy:
  `testdir:fixedbugs/issue18459.go` (`nowritebarrier` outside runtime),
  `issue18882.go` (`cgo_ldflag` syntax), `issue48097.go` (`-complete` and
  `noescape`), `testdir:linkname3.go`, `testdir:nowritebarrier.go`, and
  `testdir:uintptrkeepalive.go`.
- Compiler-only builtin operand policy:
  `testdir:fixedbugs/issue79258.go` expects
  `illegal types for operand: println`.

## Remaining errorcheck wording/shape mismatches (Sprint 154)

These roots do report the correct rejection; only diagnostic text/shape fails
the corpus regex. They are intentionally not rewritten here.

- `testdir:fixedbugs/issue63489a.go`: expected
  `file declares //go:build go1.21`; actual
  `cannot range over 10 (untyped int constant): requires go1.22 or later`.
- `testdir:fixedbugs/issue63489b.go`: expected
  `file declares //go:build go1.21`; actual
  `cannot range over 10 (untyped int constant): requires go1.22 or later`.
- `testdir:initloop.go`: expected one regex-shaped diagnostic
  `a refers to b\n.*b refers to c\n.*c refers to a|initialization loop`;
  actual go/types callbacks are `initialization cycle for a`, followed by
  separately positioned tab-indented notes `a refers to b`, `b refers to c`,
  and `c refers to a`.

## Why the 99 typechecker roots remain red in the full execution ledger

Direct checker comparison is not the source of their `no error expected`
first lines. These files deliberately contain errors. The full roots execute
the Go checker test harness through Bash++, and that interpreted harness still
misclassifies its own diagnostics:

- The following 16 paired roots use the test-only `assert`/`trace` builtins,
  which `DefPredeclaredTestFuncs` installs by mutating the checker Universe:
  both `{cmd/compile/internal/types2,go/types}/TestCheck/` variants of
  `builtins0.go`, `builtins1.go`, `const0.go`, `const1.go`, `constdecl.go`,
  `conversions0.go`, `expr3.go`, and `literals.go`. The active symptom is an
  unexpected `undefined: assert`; that is interpreted package-global mutation,
  not the outer gosource checker or importer.
- The remaining paired fixtures emit legitimate secondary go/types notes
  beginning with a tab. The reference callback explicitly drops them before
  matching `ERROR` comments; the interpreted callback path retains them as
  standalone expected-error candidates. The exact roots are both checker
  variants of `TestCheck/{cycles0.go,cycles2.go,cycles3.go,cycles5.go,
  cycles6.go,decls0.go,decls2,decls3.go,decls4.go,go1_13.go,importdecl0,
  init0.go,init1.go,init2.go,labels.go,main0.go,stmt0.go,typeparams.go}`;
  `TestExamples/types.go`; and
  `TestFixedbugs/{issue39634.go,issue39938.go,issue41124.go,issue42758.go,
  issue48018.go,issue48234.go,issue48582.go,issue48656.go,issue48962.go,
  issue48974.go,issue49043.go,issue49276.go,issue51503.go,issue52698.go,
  issue65711.go,issue66285.go,issue6977.go,issue75918.go,issue76478.go,
  issue79265.go,issue80172.go}`.
- The three types2-local roots
  `TestLocal/issue47996.go`, `TestLocal/issue68183.go`, and
  `TestLocal/issue71254.go` similarly exercise parser-error recovery or
  multi-line cycle formatting in the interpreted test harness. They are not
  valid clean packages rejected by gosource.

Consequently, the active ledger's `_ redeclared`, `cannot use ... as ...`, and
all 99 `no error expected` execution failures remain evaluator/lowering or
diagnostic-policy work. Adding fake declarations, filtering checker callbacks,
or special-casing corpus paths in gosource would conceal those mechanisms and
was deliberately not done.
